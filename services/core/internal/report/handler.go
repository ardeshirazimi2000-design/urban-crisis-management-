package report

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/idempotency"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/media"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

type Module struct {
	Pool    *pgxpool.Pool
	Guard   *auth.Guard
	Area    gis.Area
	Limiter *httpx.RateLimiter
	Now     func() time.Time
}

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("POST /reports", m.create)
	r.Auth("POST /reports/phone", m.createPhone)
	r.Auth("GET /reports", m.list)
	r.Auth("GET /reports/mine", m.listMine)
	r.Auth("GET /reports/{id}", m.get)
	r.Auth("GET /reports/{id}/related", m.related)
	r.Auth("POST /reports/{id}/claim", m.claim)
	r.Auth("POST /reports/{id}/review", m.review)
}

// Report is the API representation. Location precision depends on the caller's permission.
type Report struct {
	ID               uuid.UUID    `json:"id"`
	Type             string       `json:"type"`
	Description      string       `json:"description"`
	Severity         *string      `json:"severity"`
	Status           string       `json:"status"`
	Source           string       `json:"source"`
	OccurredAt       *time.Time   `json:"occurred_at"`
	ReceivedAt       time.Time    `json:"received_at"`
	Location         gis.Location `json:"location"`
	EnrichmentStatus string       `json:"enrichment_status"`
	DuplicateOf      *uuid.UUID   `json:"duplicate_of,omitempty"`
	ReviewerID       *uuid.UUID   `json:"reviewer_id,omitempty"`
	ReviewReason     *string      `json:"review_reason,omitempty"`
	ReviewedAt       *time.Time   `json:"reviewed_at,omitempty"`
	MediaCount       int          `json:"media_count"`
	Version          int          `json:"version"`
	reporter         uuid.UUID
}

const selectCols = `r.id, r.type, r.description, r.severity, r.status, r.source, r.occurred_at, r.received_at,
	ST_Y(r.location::geometry), ST_X(r.location::geometry), r.accuracy_m, r.location_source, r.enrichment_status,
	r.duplicate_of, r.reviewer_id, r.review_reason, r.reviewed_at,
	(SELECT count(*) FROM report_media rm WHERE rm.report_id = r.id), r.version, r.reporter_ref`

func scanReport(row pgx.Row) (Report, error) {
	var rp Report
	err := row.Scan(&rp.ID, &rp.Type, &rp.Description, &rp.Severity, &rp.Status, &rp.Source, &rp.OccurredAt, &rp.ReceivedAt,
		&rp.Location.Lat, &rp.Location.Lng, &rp.Location.AccuracyM, &rp.Location.Source, &rp.EnrichmentStatus,
		&rp.DuplicateOf, &rp.ReviewerID, &rp.ReviewReason, &rp.ReviewedAt, &rp.MediaCount, &rp.Version, &rp.reporter)
	return rp, err
}

// present applies location privacy: exact coordinates only with report:read_precise or for the reporter.
func present(p *auth.Principal, rp Report) Report {
	if p.Has(auth.ReportReadPrecise) || rp.reporter == p.UserID {
		rp.Location.Precision = "exact"
		return rp
	}
	a := gis.Approximate(gis.Point{Lat: rp.Location.Lat, Lng: rp.Location.Lng})
	rp.Location.Lat, rp.Location.Lng = a.Lat, a.Lng
	rp.Location.AccuracyM = max(rp.Location.AccuracyM, 1100)
	rp.Location.Precision = "approximate"
	return rp
}

type createResponse struct {
	ReportID           uuid.UUID `json:"report_id"`
	Status             string    `json:"status"`
	ReceivedAt         time.Time `json:"received_at"`
	CorrelationID      string    `json:"correlation_id"`
	PossibleDuplicates int       `json:"possible_duplicates"`
}

// DuplicateRadiusM and DuplicateWindow define the "possible duplicate" hint; reports are never auto-merged.
const (
	DuplicateRadiusM = 200
	DuplicateWindow  = 30 * time.Minute
)

// create implements POST /reports: validates, persists the report, media links, history and the
// report.created outbox event in ONE transaction, then answers 202 (durably accepted, not verified).
func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ReportCreate, "report", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	key, err := idempotency.Key(r)
	if err != nil {
		return err
	}
	if m.Limiter != nil && !m.Limiter.Allow("report:"+p.UserID.String()) {
		return httpx.ErrRateLimited
	}
	var req CreateRequest
	if err := httpx.DecodeJSON(w, r, &req, 16<<10); err != nil {
		return err
	}
	req.Normalize()
	now := m.now().UTC()
	if err := req.Validate(m.Area, now); err != nil {
		return err
	}

	var resp createResponse
	var replay *idempotency.Stored
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		replay, err = idempotency.Begin(ctx, tx, p.UserID, "POST /reports", key, idempotency.Hash(req))
		if err != nil || replay != nil {
			return err
		}
		resp, err = insertReport(ctx, tx, p.UserID, "citizen_app", req)
		if err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, p.UserID, "POST /reports", key, http.StatusAccepted, resp)
	})
	if err != nil {
		return err
	}
	if replay != nil {
		replay.Write(w)
		return nil
	}
	httpx.JSON(w, http.StatusAccepted, resp)
	return nil
}

// createPhone records a report taken by an operator from a phone call. It enters the same review queue as
// citizen reports (it is not auto-accepted); the caller's details are stored separately as personal data.
func (m *Module) createPhone(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ReportIntake, "report", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	key, err := idempotency.Key(r)
	if err != nil {
		return err
	}
	var req PhoneRequest
	if err := httpx.DecodeJSON(w, r, &req, 16<<10); err != nil {
		return err
	}
	req.Normalize()
	if err := req.Validate(m.Area, m.now().UTC()); err != nil {
		return err
	}
	var resp createResponse
	var replay *idempotency.Stored
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		replay, err = idempotency.Begin(ctx, tx, p.UserID, "POST /reports/phone", key, idempotency.Hash(req))
		if err != nil || replay != nil {
			return err
		}
		if resp, err = insertReport(ctx, tx, p.UserID, "phone", req.CreateRequest); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO report_contacts (report_id, caller_name, caller_phone, callback_requested, address_text, recorded_by)
			VALUES ($1, NULLIF($2,''), NULLIF($3,''), $4, NULLIF($5,''), $6)`,
			resp.ReportID, req.CallerName, req.CallerPhone, req.CallbackRequested, req.AddressText, p.UserID); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "report.phone_intake",
			TargetType: "report", TargetID: resp.ReportID.String(), Outcome: "success",
			Details:       map[string]any{"type": req.Type, "callback_requested": req.CallbackRequested, "has_caller_phone": req.CallerPhone != ""},
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, p.UserID, "POST /reports/phone", key, http.StatusAccepted, resp)
	})
	if err != nil {
		return err
	}
	if replay != nil {
		replay.Write(w)
		return nil
	}
	httpx.JSON(w, http.StatusAccepted, resp)
	return nil
}

// insertReport persists a validated report with its media links, history entry and report.created outbox event
// inside tx. source records the intake channel (citizen_app | phone).
func insertReport(ctx context.Context, tx pgx.Tx, reporter uuid.UUID, source string, req CreateRequest) (createResponse, error) {
	id := uuid.New()
	var receivedAt time.Time
	err := tx.QueryRow(ctx, `INSERT INTO reports (id, reporter_ref, source, type, description, occurred_at, location, accuracy_m, location_source)
		VALUES ($1,$2,$3,$4,$5,$6,`+fmt.Sprintf(gis.PointSQL, "$7", "$8")+`,$9,$10) RETURNING received_at`,
		id, reporter, source, req.Type, req.Description, req.OccurredAt, req.Location.Lng, req.Location.Lat,
		req.Location.AccuracyM, req.Location.Source).Scan(&receivedAt)
	if err != nil {
		return createResponse{}, err
	}
	if err := media.AttachToReport(ctx, tx, reporter, id, req.MediaIDs); err != nil {
		return createResponse{}, err
	}
	var dups int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM reports o, reports n
		WHERE n.id = $1 AND o.id <> n.id AND o.type = n.type
		  AND o.status NOT IN ('rejected') AND o.received_at > n.received_at - $3::interval
		  AND ST_DWithin(o.location, n.location, $2)`, id, DuplicateRadiusM,
		fmt.Sprintf("%d seconds", int(DuplicateWindow.Seconds()))).Scan(&dups); err != nil {
		return createResponse{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO report_events (report_id, event_type, actor_id, payload, correlation_id)
		VALUES ($1, 'created', $2, $3, $4)`, id, reporter,
		map[string]any{"possible_duplicates": dups, "media": len(req.MediaIDs), "source": source}, httpx.CorrelationID(ctx)); err != nil {
		return createResponse{}, err
	}
	// Event payload is minimised: no reporter or caller identity, no media; description is capped
	// (needed by the AI assist classifier; topic ACLs restrict consumers).
	if _, err := outbox.Enqueue(ctx, tx, outbox.Event{
		Type: "report.created", Aggregate: outbox.Aggregate{Type: "report", ID: id, Version: 1}, OccurredAt: receivedAt,
		Payload: map[string]any{
			"report_id": id, "report_type": req.Type, "source": source, "description": truncateRunes(req.Description, 1000),
			"location":    map[string]any{"lat": req.Location.Lat, "lng": req.Location.Lng, "accuracy_m": req.Location.AccuracyM},
			"occurred_at": req.OccurredAt, "received_at": receivedAt, "media_count": len(req.MediaIDs),
			"possible_duplicates": dups,
		},
	}); err != nil {
		return createResponse{}, err
	}
	return createResponse{ReportID: id, Status: StatusReceived, ReceivedAt: receivedAt,
		CorrelationID: httpx.CorrelationID(ctx), PossibleDuplicates: dups}, nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

type listResponse struct {
	Items      []Report `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

// list implements the operator queue: filter by status/type/time/proximity, keyset-paginated by received_at.
func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ReportRead, "report", ""); err != nil {
		return err
	}
	return m.listWhere(w, r, nil)
}

func (m *Module) listMine(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ReportReadOwn, "report", ""); err != nil {
		return err
	}
	me := auth.FromContext(ctx).UserID
	return m.listWhere(w, r, &me)
}

func (m *Module) listWhere(w http.ResponseWriter, r *http.Request, reporter *uuid.UUID) error {
	ctx := r.Context()
	q := r.URL.Query()
	status, err := httpx.OneOf(r, "status", Statuses...)
	if err != nil {
		return err
	}
	typ, err := httpx.OneOf(r, "type", Types...)
	if err != nil {
		return err
	}
	limit, err := httpx.PageSize(r, 50, 200)
	if err != nil {
		return err
	}
	cur, err := httpx.DecodeCursor(q.Get("cursor"))
	if err != nil {
		return err
	}
	var since *time.Time
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return httpx.Validation(httpx.FieldDetail{Field: "since", Reason: "invalid_rfc3339"})
		}
		since = &t
	}
	var lat, lng, radius *float64
	if q.Get("near_lat") != "" {
		la, e1 := strconv.ParseFloat(q.Get("near_lat"), 64)
		ln, e2 := strconv.ParseFloat(q.Get("near_lng"), 64)
		rd, e3 := strconv.ParseFloat(q.Get("radius_m"), 64)
		if e1 != nil || e2 != nil || e3 != nil || rd <= 0 || rd > 50000 {
			return httpx.Validation(httpx.FieldDetail{Field: "near_lat,near_lng,radius_m", Reason: "invalid"})
		}
		lat, lng, radius = &la, &ln, &rd
	}
	var curT *time.Time
	var curID *uuid.UUID
	if cur != nil {
		curT, curID = &cur.T, &cur.ID
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+selectCols+` FROM reports r
		WHERE ($1 = '' OR r.status = $1) AND ($2 = '' OR r.type = $2)
		  AND ($3::timestamptz IS NULL OR r.received_at >= $3)
		  AND ($4::uuid IS NULL OR r.reporter_ref = $4)
		  AND ($5::float8 IS NULL OR ST_DWithin(r.location, ST_SetSRID(ST_MakePoint($6, $5), 4326)::geography, $7))
		  AND ($8::timestamptz IS NULL OR (r.received_at, r.id) < ($8, $9))
		ORDER BY r.received_at DESC, r.id DESC LIMIT $10`,
		status, typ, since, reporter, lat, lng, radius, curT, curID, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	p := auth.FromContext(ctx)
	resp := listResponse{Items: []Report{}}
	for rows.Next() {
		rp, err := scanReport(rows)
		if err != nil {
			return err
		}
		resp.Items = append(resp.Items, present(p, rp))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(resp.Items) > limit {
		last := resp.Items[limit-1]
		resp.Items = resp.Items[:limit]
		resp.NextCursor = httpx.Cursor{T: last.ReceivedAt, ID: last.ID}.Encode()
	}
	httpx.JSON(w, http.StatusOK, resp)
	return nil
}

type AIScore struct {
	ModelVersion   string          `json:"model_version"`
	PredictedType  *string         `json:"predicted_type"`
	TypeConfidence *float64        `json:"type_confidence"`
	UrgencySignal  *float64        `json:"urgency_signal"`
	Signals        json.RawMessage `json:"signals"`
	CreatedAt      time.Time       `json:"created_at"`
	Note           string          `json:"note"`
}

type HistoryEntry struct {
	EventType string          `json:"event_type"`
	ActorID   *uuid.UUID      `json:"actor_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type detail struct {
	Report
	Contact   *Contact         `json:"contact,omitempty"`
	Media     []media.Media    `json:"media"`
	AIScores  []AIScore        `json:"ai_scores"`
	History   []HistoryEntry   `json:"history"`
	Incidents []map[string]any `json:"incidents"`
}

func (m *Module) load(ctx context.Context, q db.DBTX, id uuid.UUID) (Report, error) {
	rp, err := scanReport(q.QueryRow(ctx, `SELECT `+selectCols+` FROM reports r WHERE r.id = $1`, id))
	if err == pgx.ErrNoRows {
		return rp, httpx.ErrNotFound
	}
	return rp, err
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	rp, err := m.load(ctx, m.Pool, id)
	if err != nil {
		return err
	}
	if !p.Has(auth.ReportRead) && !(p.Has(auth.ReportReadOwn) && rp.reporter == p.UserID) {
		// Hide existence from callers who may not see it, but still audit the attempt.
		return m.Guard.Deny(ctx, string(auth.ReportRead), "report", id.String(), "not_owner", httpx.ErrNotFound)
	}
	d := detail{Report: present(p, rp), AIScores: []AIScore{}, History: []HistoryEntry{}, Incidents: []map[string]any{}}
	if d.Media, err = media.ListForReport(ctx, m.Pool, id); err != nil {
		return err
	}
	// Caller details (phone intake) are personal data: only for roles that may see precise locations.
	if rp.Source == "phone" && p.Has(auth.ReportReadPrecise) {
		var c Contact
		err := m.Pool.QueryRow(ctx, `SELECT caller_name, caller_phone, callback_requested, address_text, recorded_by, created_at
			FROM report_contacts WHERE report_id=$1`, id).Scan(&c.CallerName, &c.CallerPhone, &c.CallbackRequested,
			&c.AddressText, &c.RecordedBy, &c.CreatedAt)
		if err == nil {
			d.Contact = &c
		} else if err != pgx.ErrNoRows {
			return err
		}
	}
	if p.Has(auth.ReportRead) {
		rows, err := m.Pool.Query(ctx, `SELECT model_version, predicted_type, type_confidence, urgency_signal, signals, created_at
			FROM report_ai_scores WHERE report_id=$1 ORDER BY created_at DESC`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			s := AIScore{Note: "سیگنال کمکی؛ جایگزین بررسی انسانی نیست"}
			if err := rows.Scan(&s.ModelVersion, &s.PredictedType, &s.TypeConfidence, &s.UrgencySignal, &s.Signals, &s.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			d.AIScores = append(d.AIScores, s)
		}
		rows.Close()
		rows, err = m.Pool.Query(ctx, `SELECT event_type, actor_id, payload, created_at FROM report_events WHERE report_id=$1 ORDER BY created_at`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h HistoryEntry
			if err := rows.Scan(&h.EventType, &h.ActorID, &h.Payload, &h.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			d.History = append(d.History, h)
		}
		rows.Close()
		rows, err = m.Pool.Query(ctx, `SELECT i.id, i.code, i.status, ir.relation_type FROM incident_reports ir
			JOIN incidents i ON i.id = ir.incident_id WHERE ir.report_id=$1`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var iid uuid.UUID
			var code, st, rel string
			if err := rows.Scan(&iid, &code, &st, &rel); err != nil {
				rows.Close()
				return err
			}
			d.Incidents = append(d.Incidents, map[string]any{"id": iid, "code": code, "status": st, "relation_type": rel})
		}
		rows.Close()
	}
	httpx.JSON(w, http.StatusOK, d)
	return nil
}

// related returns nearby reports (correlation candidates) without merging anything.
func (m *Module) related(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ReportRead, "report", r.PathValue("id")); err != nil {
		return err
	}
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	radius := 300.0
	if s := r.URL.Query().Get("radius_m"); s != "" {
		if radius, err = strconv.ParseFloat(s, 64); err != nil || radius <= 0 || radius > 5000 {
			return httpx.Validation(httpx.FieldDetail{Field: "radius_m", Reason: "must_be_1_to_5000"})
		}
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+selectCols+`, ST_Distance(r.location, b.location) AS dist
		FROM reports r, reports b
		WHERE b.id = $1 AND r.id <> b.id AND ST_DWithin(r.location, b.location, $2)
		  AND r.received_at BETWEEN b.received_at - interval '6 hours' AND b.received_at + interval '6 hours'
		ORDER BY dist LIMIT 50`, id, radius)
	if err != nil {
		return err
	}
	defer rows.Close()
	p := auth.FromContext(ctx)
	type item struct {
		Report
		DistanceM float64 `json:"distance_m"`
		SameType  bool    `json:"same_type"`
	}
	base, err := m.load(ctx, m.Pool, id)
	if err != nil {
		return err
	}
	out := []item{}
	for rows.Next() {
		var rp Report
		var dist float64
		if err := rows.Scan(&rp.ID, &rp.Type, &rp.Description, &rp.Severity, &rp.Status, &rp.Source, &rp.OccurredAt, &rp.ReceivedAt,
			&rp.Location.Lat, &rp.Location.Lng, &rp.Location.AccuracyM, &rp.Location.Source, &rp.EnrichmentStatus,
			&rp.DuplicateOf, &rp.ReviewerID, &rp.ReviewReason, &rp.ReviewedAt, &rp.MediaCount, &rp.Version, &rp.reporter, &dist); err != nil {
			return err
		}
		out = append(out, item{Report: present(p, rp), DistanceM: dist, SameType: rp.Type == base.Type})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out, "radius_m": radius})
	return rows.Err()
}

// claim moves a report into under_review and records who is reviewing it.
func (m *Module) claim(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := m.Guard.Require(ctx, auth.ReportReview, "report", id.String()); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var out Report
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		rp, err := m.load(ctx, tx, id)
		if err != nil {
			return err
		}
		if !CanTransition(rp.Status, StatusUnderReview) {
			return httpx.Conflict("INVALID_TRANSITION", "گزارش در وضعیت فعلی قابل برداشتن برای بررسی نیست")
		}
		tag, err := tx.Exec(ctx, `UPDATE reports SET status='under_review', reviewer_id=$2, version=version+1
			WHERE id=$1 AND version=$3`, id, p.UserID, rp.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.ErrVersionConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO report_events (report_id, event_type, actor_id, payload, correlation_id)
			VALUES ($1,'claimed',$2,'{}',$3)`, id, p.UserID, httpx.CorrelationID(ctx)); err != nil {
			return err
		}
		out, err = m.load(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, present(p, out))
	return nil
}

// review records the operator's decision with reason, using optimistic locking on version.
func (m *Module) review(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := m.Guard.Require(ctx, auth.ReportReview, "report", id.String()); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req ReviewRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Report
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		rp, err := m.load(ctx, tx, id)
		if err != nil {
			return err
		}
		if rp.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if !CanTransition(rp.Status, req.Decision) {
			return httpx.Conflict("INVALID_TRANSITION", fmt.Sprintf("انتقال از %s به %s مجاز نیست", rp.Status, req.Decision))
		}
		if req.DuplicateOf != nil {
			if *req.DuplicateOf == id {
				return httpx.Validation(httpx.FieldDetail{Field: "duplicate_of", Reason: "self_reference"})
			}
			if _, err := m.load(ctx, tx, *req.DuplicateOf); err != nil {
				return httpx.Validation(httpx.FieldDetail{Field: "duplicate_of", Reason: "not_found"})
			}
		}
		var sev *string
		if req.Severity != "" {
			sev = &req.Severity
		}
		tag, err := tx.Exec(ctx, `UPDATE reports SET status=$2, review_reason=$3, reviewer_id=$4, reviewed_at=now(),
			duplicate_of=$5, severity=COALESCE($6, severity), version=version+1 WHERE id=$1 AND version=$7`,
			id, req.Decision, req.Reason, p.UserID, req.DuplicateOf, sev, req.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.ErrVersionConflict
		}
		evt := map[string]any{"from": rp.Status, "to": req.Decision, "reason": req.Reason, "severity": req.Severity, "duplicate_of": req.DuplicateOf}
		if _, err := tx.Exec(ctx, `INSERT INTO report_events (report_id, event_type, actor_id, payload, correlation_id)
			VALUES ($1,'reviewed',$2,$3,$4)`, id, p.UserID, evt, httpx.CorrelationID(ctx)); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "report.reviewed",
			Aggregate: outbox.Aggregate{Type: "report", ID: id, Version: req.Version + 1},
			Payload:   map[string]any{"report_id": id, "decision": req.Decision, "reason_given": req.Reason != "", "severity": req.Severity, "duplicate_of": req.DuplicateOf, "reviewer_id": p.UserID}}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "report.review",
			TargetType: "report", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"decision": req.Decision, "from": rp.Status}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = m.load(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, present(p, out))
	return nil
}

// MarkLinked moves an accepted report to linked_to_incident (called by the incident module inside its tx).
func MarkLinked(ctx context.Context, tx pgx.Tx, reportID, actor, incidentID uuid.UUID) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM reports WHERE id=$1 FOR UPDATE`, reportID).Scan(&status); err != nil {
		if err == pgx.ErrNoRows {
			return httpx.Validation(httpx.FieldDetail{Field: "report_id", Reason: "not_found"})
		}
		return err
	}
	if !CanTransition(status, StatusLinked) {
		return httpx.Conflict("REPORT_NOT_ACCEPTED", "فقط گزارش تأییدشده قابل پیوند به حادثه است")
	}
	if _, err := tx.Exec(ctx, `UPDATE reports SET status='linked_to_incident', version=version+1 WHERE id=$1`, reportID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO report_events (report_id, event_type, actor_id, payload, correlation_id)
		VALUES ($1,'linked',$2,$3,$4)`, reportID, actor, map[string]any{"incident_id": incidentID}, httpx.CorrelationID(ctx))
	return err
}
