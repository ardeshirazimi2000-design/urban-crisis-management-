package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/idempotency"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// MaxRegionKm2 bounds a single alert's target region (Tehran province ≈ 18,800 km²).
const MaxRegionKm2 = 20000

type Module struct {
	Pool                    *pgxpool.Pool
	Guard                   *auth.Guard
	RequireDistinctApprover bool
	MaxValidity             time.Duration
	Now                     func() time.Time
}

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("GET /alerts/templates", m.templates)
	r.Auth("POST /alerts", m.create)
	r.Auth("GET /alerts", m.list)
	r.Auth("GET /alerts/public", m.public)
	r.Auth("GET /alerts/{id}", m.get)
	r.Auth("GET /alerts/{id}/preview", m.preview)
	r.Auth("POST /alerts/{id}/submit", m.submit)
	r.Auth("POST /alerts/{id}/approve", m.approve)
	r.Auth("POST /alerts/{id}/dispatch", m.dispatch)
	r.Auth("POST /alerts/{id}/cancel", m.cancel)
	r.Auth("GET /alerts/{id}/delivery", m.delivery)
}

type Alert struct {
	ID              uuid.UUID         `json:"id"`
	IncidentID      *uuid.UUID        `json:"incident_id"`
	Mode            string            `json:"mode"`
	Severity        string            `json:"severity"`
	Status          string            `json:"status"`
	IssuerOrgID     uuid.UUID         `json:"issuer_org_id"`
	IssuerName      string            `json:"issuer_name"`
	Region          json.RawMessage   `json:"region"`
	RegionLabel     string            `json:"region_label"`
	RegionAreaKm2   float64           `json:"region_area_km2"`
	TemplateCode    string            `json:"template_code"`
	TemplateVersion int               `json:"template_version"`
	Params          map[string]string `json:"params"`
	RenderedText    string            `json:"rendered_text"`
	Channels        []string          `json:"channels"`
	IssuedAt        *time.Time        `json:"issued_at"`
	ExpiresAt       time.Time         `json:"expires_at"`
	CreatedBy       uuid.UUID         `json:"created_by"`
	SubmittedBy     *uuid.UUID        `json:"submitted_by"`
	ApprovedBy      *uuid.UUID        `json:"approved_by"`
	ApprovalReason  *string           `json:"approval_reason"`
	ApprovedAt      *time.Time        `json:"approved_at"`
	CancelledBy     *uuid.UUID        `json:"cancelled_by"`
	CancelReason    *string           `json:"cancel_reason"`
	DispatchKey     *string           `json:"dispatch_key,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	Version         int               `json:"version"`
}

const selectCols = `a.id, a.incident_id, a.mode, a.severity, a.status, a.issuer_org_id, o.name, ST_AsGeoJSON(a.region_geom)::text,
	a.region_label, ST_Area(a.region_geom)/1e6, a.template_code, a.template_version, a.params, a.rendered_text, a.channels,
	a.issued_at, a.expires_at, a.created_by, a.submitted_by, a.approved_by, a.approval_reason, a.approved_at,
	a.cancelled_by, a.cancel_reason, a.dispatch_key, a.created_at, a.updated_at, a.version`

func scan(row pgx.Row) (Alert, error) {
	var a Alert
	var region string
	err := row.Scan(&a.ID, &a.IncidentID, &a.Mode, &a.Severity, &a.Status, &a.IssuerOrgID, &a.IssuerName, &region,
		&a.RegionLabel, &a.RegionAreaKm2, &a.TemplateCode, &a.TemplateVersion, &a.Params, &a.RenderedText, &a.Channels,
		&a.IssuedAt, &a.ExpiresAt, &a.CreatedBy, &a.SubmittedBy, &a.ApprovedBy, &a.ApprovalReason, &a.ApprovedAt,
		&a.CancelledBy, &a.CancelReason, &a.DispatchKey, &a.CreatedAt, &a.UpdatedAt, &a.Version)
	a.Region = json.RawMessage(region)
	return a, err
}

func Load(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Alert, error) {
	sql := `SELECT ` + selectCols + ` FROM alerts a JOIN organizations o ON o.id = a.issuer_org_id WHERE a.id=$1`
	if forUpdate {
		sql += ` FOR UPDATE OF a`
	}
	a, err := scan(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return a, httpx.ErrNotFound
	}
	return a, err
}

func canView(p *auth.Principal, org uuid.UUID) bool {
	return p.CanInOrg(auth.AlertDraft, org) || p.CanInOrg(auth.AlertApprove, org) || p.CanInOrg(auth.AlertReadDelivery, org)
}

func (m *Module) loadVisible(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Alert, error) {
	a, err := Load(ctx, q, id, forUpdate)
	if err != nil {
		return a, err
	}
	if !canView(auth.FromContext(ctx), a.IssuerOrgID) {
		return a, m.Guard.Deny(ctx, "alert:read", "alert", id.String(), "out_of_org_scope", httpx.ErrNotFound)
	}
	return a, nil
}

func writeAudit(ctx context.Context, tx pgx.Tx, action string, a Alert, reason string, details map[string]any) error {
	p := auth.FromContext(ctx)
	return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: action, TargetType: "alert",
		TargetID: a.ID.String(), Outcome: "success", Reason: reason, Details: details, CorrelationID: httpx.CorrelationID(ctx)})
}

func (m *Module) templates(w http.ResponseWriter, r *http.Request) error {
	if err := m.Guard.Require(r.Context(), auth.AlertDraft, "alert_template", ""); err != nil {
		return err
	}
	out := []map[string]any{}
	for _, t := range Templates {
		out = append(out, map[string]any{"code": t.Code, "version": t.Version, "title": t.Title, "body": t.Body, "params": t.Params})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out, "channel_max_runes": ChannelMaxRunes})
	return nil
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	key, err := idempotency.Key(r)
	if err != nil {
		return err
	}
	var req CreateRequest
	if err := httpx.DecodeJSON(w, r, &req, 256<<10); err != nil {
		return err
	}
	if req.Params == nil {
		req.Params = map[string]string{}
	}
	if err := req.Validate(m.now(), m.MaxValidity); err != nil {
		return err
	}
	if err := m.Guard.RequireOrg(ctx, auth.AlertDraft, req.IssuerOrgID, "alert", ""); err != nil {
		return err
	}
	regionJSON, _ := json.Marshal(req.Region)
	if _, err := gis.ValidateGeoJSONArea(ctx, m.Pool, "region", regionJSON, MaxRegionKm2); err != nil {
		return err
	}
	var out Alert
	var replay *idempotency.Stored
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		replay, err = idempotency.Begin(ctx, tx, p.UserID, "POST /alerts", key, idempotency.Hash(req))
		if err != nil || replay != nil {
			return err
		}
		var issuer string
		if err := tx.QueryRow(ctx, `SELECT name FROM organizations WHERE id=$1`, req.IssuerOrgID).Scan(&issuer); err != nil {
			return httpx.Validation(httpx.FieldDetail{Field: "issuer_org_id", Reason: "not_found"})
		}
		if req.IncidentID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM incidents WHERE id=$1)`, *req.IncidentID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return httpx.Validation(httpx.FieldDetail{Field: "incident_id", Reason: "not_found"})
			}
		}
		t, _ := FindTemplate(req.TemplateCode, req.TemplateVersion)
		text := Render(t, req.Mode, issuer, req.RegionLabel, req.ExpiresAt, req.Params)
		if err := CheckChannelLengths(text, req.Channels); err != nil {
			return err
		}
		id := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO alerts (id, incident_id, mode, severity, issuer_org_id, region_geom, region_label,
			template_code, template_version, params, rendered_text, channels, expires_at, created_by, idempotency_key)
			VALUES ($1,$2,$3,$4,$5, ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($6), 4326))::geography, $7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			id, req.IncidentID, req.Mode, req.Severity, req.IssuerOrgID, string(regionJSON), req.RegionLabel, req.TemplateCode,
			req.TemplateVersion, req.Params, text, req.Channels, req.ExpiresAt, p.UserID, p.UserID.String()+":"+key); err != nil {
			return err
		}
		if out, err = Load(ctx, tx, id, false); err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, "alert.draft", out, "", map[string]any{"mode": req.Mode, "template": req.TemplateCode}); err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, p.UserID, "POST /alerts", key, http.StatusCreated, out)
	})
	if err != nil {
		return err
	}
	if replay != nil {
		replay.Write(w)
		return nil
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	var orgs []uuid.UUID
	all := false
	for _, perm := range []auth.Permission{auth.AlertDraft, auth.AlertApprove, auth.AlertReadDelivery} {
		a, o := p.OrgScope(perm)
		all = all || a
		orgs = append(orgs, o...)
	}
	if !all && len(orgs) == 0 {
		return m.Guard.Deny(ctx, "alert:read", "alert", "", "missing_permission", nil)
	}
	status, err := httpx.OneOf(r, "status", Statuses...)
	if err != nil {
		return err
	}
	limit, err := httpx.PageSize(r, 50, 200)
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+selectCols+` FROM alerts a JOIN organizations o ON o.id=a.issuer_org_id
		WHERE ($1 OR a.issuer_org_id = ANY($2)) AND ($3 = '' OR a.status = $3) ORDER BY a.updated_at DESC LIMIT $4`,
		all, orgs, status, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Alert{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return err
		}
		items = append(items, a)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return rows.Err()
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	a, err := m.loadVisible(ctx, m.Pool, id, false)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, a)
	return nil
}

// preview shows the exact rendered text per channel and a reviewable summary of the target region.
func (m *Module) preview(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	a, err := m.loadVisible(ctx, m.Pool, id, false)
	if err != nil {
		return err
	}
	var cLat, cLng, minX, minY, maxX, maxY float64
	if err := m.Pool.QueryRow(ctx, `SELECT ST_Y(ST_Centroid(region_geom::geometry)), ST_X(ST_Centroid(region_geom::geometry)),
		ST_XMin(region_geom::geometry), ST_YMin(region_geom::geometry), ST_XMax(region_geom::geometry), ST_YMax(region_geom::geometry)
		FROM alerts WHERE id=$1`, id).Scan(&cLat, &cLng, &minX, &minY, &maxX, &maxY); err != nil {
		return err
	}
	perChannel := map[string]any{}
	for _, c := range a.Channels {
		perChannel[c] = map[string]any{"text": a.RenderedText, "runes": len([]rune(a.RenderedText)), "max_runes": ChannelMaxRunes[c],
			"note": channelNote(c)}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"alert_id": a.ID, "mode": a.Mode, "status": a.Status, "is_test": a.Mode == "test",
		"rendered_text": a.RenderedText, "channels": perChannel,
		"region": map[string]any{"label": a.RegionLabel, "area_km2": a.RegionAreaKm2, "centroid": gis.Point{Lat: cLat, Lng: cLng},
			"bbox": []float64{minX, minY, maxX, maxY}, "geojson": a.Region},
		"template":   map[string]any{"code": a.TemplateCode, "version": a.TemplateVersion},
		"expires_at": a.ExpiresAt, "issuer": a.IssuerName,
	})
	return nil
}

func channelNote(c string) string {
	switch c {
	case "cell_broadcast":
		return "نیازمند قرارداد و مجوز اپراتور؛ در MVP ارسال واقعی غیرفعال است"
	case "sms", "push":
		return "پذیرش توسط ارائه‌دهنده به معنی تحویل به کاربر نیست"
	default:
		return ""
	}
}

type versionReason struct {
	Reason  string `json:"reason"`
	Version int    `json:"version"`
}

func (req *versionReason) validate(reasonRequired bool) error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(!reasonRequired || req.Reason != "", "reason", "required")
	v.Check(len(req.Reason) <= 2000, "reason", "too_long")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}

// transition is the shared skeleton for submit/approve/cancel.
func (m *Module) transition(w http.ResponseWriter, r *http.Request, reasonRequired bool,
	fn func(ctx context.Context, tx pgx.Tx, a Alert, req versionReason) error) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req versionReason
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := req.validate(reasonRequired); err != nil {
		return err
	}
	var out Alert
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		a, err := m.loadVisible(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if a.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if err := fn(ctx, tx, a, req); err != nil {
			return err
		}
		out, err = Load(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

func (m *Module) submit(w http.ResponseWriter, r *http.Request) error {
	return m.transition(w, r, false, func(ctx context.Context, tx pgx.Tx, a Alert, req versionReason) error {
		p := auth.FromContext(ctx)
		if !p.CanInOrg(auth.AlertDraft, a.IssuerOrgID) {
			return m.Guard.Deny(ctx, string(auth.AlertDraft), "alert", a.ID.String(), "submit", nil)
		}
		if a.Status != Draft {
			return httpx.Conflict("INVALID_TRANSITION", "فقط پیش‌نویس قابل ارسال برای تأیید است")
		}
		if !a.ExpiresAt.After(m.now()) {
			return httpx.Conflict("ALERT_EXPIRED", "زمان اعتبار هشدار گذشته است")
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET status='pending_approval', submitted_by=$2, updated_at=now(), version=version+1 WHERE id=$1`,
			a.ID, p.UserID); err != nil {
			return err
		}
		return writeAudit(ctx, tx, "alert.submit", a, req.Reason, nil)
	})
}

// approve enforces four-eyes: the approver must differ from the author and the submitter (configurable).
func (m *Module) approve(w http.ResponseWriter, r *http.Request) error {
	return m.transition(w, r, true, func(ctx context.Context, tx pgx.Tx, a Alert, req versionReason) error {
		p := auth.FromContext(ctx)
		if !p.CanInOrg(auth.AlertApprove, a.IssuerOrgID) {
			return m.Guard.Deny(ctx, string(auth.AlertApprove), "alert", a.ID.String(), "approve", nil)
		}
		if a.Status != PendingApproval {
			return httpx.Conflict("INVALID_TRANSITION", "هشدار در وضعیت انتظار تأیید نیست")
		}
		if m.RequireDistinctApprover && (a.CreatedBy == p.UserID || (a.SubmittedBy != nil && *a.SubmittedBy == p.UserID)) {
			return m.Guard.Deny(ctx, string(auth.AlertApprove), "alert", a.ID.String(), "four_eyes_violation",
				httpx.NewError(http.StatusForbidden, "FOUR_EYES_REQUIRED", "تأییدکننده باید فردی غیر از تهیه‌کننده پیش‌نویس باشد"))
		}
		if !a.ExpiresAt.After(m.now()) {
			return httpx.Conflict("ALERT_EXPIRED", "زمان اعتبار هشدار گذشته است")
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET status='approved', approved_by=$2, approval_reason=$3, approved_at=now(),
			updated_at=now(), version=version+1 WHERE id=$1`, a.ID, p.UserID, req.Reason); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "alert.approved", Aggregate: outbox.Aggregate{Type: "alert", ID: a.ID, Version: a.Version + 1},
			Payload: map[string]any{"alert_id": a.ID, "mode": a.Mode, "severity": a.Severity, "channels": a.Channels,
				"expires_at": a.ExpiresAt, "approved_by": p.UserID, "incident_id": a.IncidentID}}); err != nil {
			return err
		}
		return writeAudit(ctx, tx, "alert.approve", a, req.Reason, map[string]any{"mode": a.Mode, "severity": a.Severity})
	})
}

func (m *Module) cancel(w http.ResponseWriter, r *http.Request) error {
	return m.transition(w, r, true, func(ctx context.Context, tx pgx.Tx, a Alert, req versionReason) error {
		p := auth.FromContext(ctx)
		perm := auth.AlertDraft
		if a.Status != Draft {
			perm = auth.AlertCancel
		}
		if !p.CanInOrg(perm, a.IssuerOrgID) {
			return m.Guard.Deny(ctx, string(perm), "alert", a.ID.String(), "cancel", nil)
		}
		if !CanCancel(a.Status) {
			return httpx.Conflict("CANNOT_CANCEL", "این هشدار قابل لغو نیست؛ برای پیام ارسال‌شده، هشدار اصلاحیه صادر کنید")
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET status='cancelled', cancelled_by=$2, cancel_reason=$3, updated_at=now(),
			version=version+1 WHERE id=$1`, a.ID, p.UserID, req.Reason); err != nil {
			return err
		}
		// Stop anything not yet handed to a provider. In-flight/unknown attempts are left for reconciliation.
		if _, err := tx.Exec(ctx, `UPDATE notification_attempts SET status='superseded', detail='alert cancelled', updated_at=now()
			WHERE alert_id=$1 AND status='pending'`, a.ID); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "alert.cancelled", Aggregate: outbox.Aggregate{Type: "alert", ID: a.ID, Version: a.Version + 1},
			Payload: map[string]any{"alert_id": a.ID, "previous_status": a.Status, "cancelled_by": p.UserID}}); err != nil {
			return err
		}
		return writeAudit(ctx, tx, "alert.cancel", a, req.Reason, map[string]any{"from": a.Status})
	})
}

// dispatch hands an approved alert to the notification workers. It is idempotent on the Idempotency-Key:
// repeating the call returns the current state without creating new attempts. Dispatching anything that
// is not approved is blocked and recorded as a security event (AT-07).
func (m *Module) dispatch(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	key, err := idempotency.Key(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var out Alert
	status := http.StatusAccepted
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		a, err := m.loadVisible(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !p.CanInOrg(auth.AlertDispatch, a.IssuerOrgID) {
			return m.Guard.Deny(ctx, string(auth.AlertDispatch), "alert", id.String(), "dispatch", nil)
		}
		if a.DispatchKey != nil {
			if *a.DispatchKey == key {
				status = http.StatusOK
				out = a
				return nil
			}
			return httpx.Conflict("ALREADY_DISPATCHED", "این هشدار قبلاً ارسال شده است")
		}
		if a.Status != Approved {
			return m.Guard.Deny(ctx, string(auth.AlertDispatch), "alert", id.String(), "dispatch_without_approval:"+a.Status,
				httpx.Conflict("NOT_APPROVED", "ارسال هشدار بدون تأیید مجاز نیست"))
		}
		if !a.ExpiresAt.After(m.now()) {
			return httpx.Conflict("ALERT_EXPIRED", "زمان اعتبار هشدار گذشته است")
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET status='sending', dispatch_key=$2, issued_at=now(), updated_at=now(),
			version=version+1 WHERE id=$1`, id, key); err != nil {
			return err
		}
		for _, c := range a.Channels {
			if _, err := tx.Exec(ctx, `INSERT INTO notification_attempts (alert_id, channel, attempt_no) VALUES ($1,$2,1)`, id, c); err != nil {
				return err
			}
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "alert.dispatch_requested", Aggregate: outbox.Aggregate{Type: "alert", ID: id, Version: a.Version + 1},
			Payload: map[string]any{"alert_id": id, "channels": a.Channels, "mode": a.Mode}}); err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, "alert.dispatch", a, "", map[string]any{"channels": a.Channels, "mode": a.Mode}); err != nil {
			return err
		}
		out, err = Load(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, status, out)
	return nil
}

type Attempt struct {
	Channel           string     `json:"channel"`
	AttemptNo         int        `json:"attempt_no"`
	Status            string     `json:"status"`
	ProviderMessageID *string    `json:"provider_message_id"`
	ResultCode        *string    `json:"result_code"`
	Detail            *string    `json:"detail"`
	AttemptedAt       *time.Time `json:"attempted_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func (m *Module) delivery(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	a, err := Load(ctx, m.Pool, id, false)
	if err != nil {
		return err
	}
	if err := m.Guard.RequireOrg(ctx, auth.AlertReadDelivery, a.IssuerOrgID, "alert", id.String()); err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT channel, attempt_no, status, provider_message_id, result_code, detail, attempted_at, updated_at
		FROM notification_attempts WHERE alert_id=$1 ORDER BY channel, attempt_no`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	byChannel := map[string][]Attempt{}
	for rows.Next() {
		var at Attempt
		if err := rows.Scan(&at.Channel, &at.AttemptNo, &at.Status, &at.ProviderMessageID, &at.ResultCode, &at.Detail, &at.AttemptedAt, &at.UpdatedAt); err != nil {
			return err
		}
		byChannel[at.Channel] = append(byChannel[at.Channel], at)
	}
	channels := []map[string]any{}
	for _, c := range a.Channels {
		atts := byChannel[c]
		latest := "not_started"
		if len(atts) > 0 {
			latest = atts[len(atts)-1].Status
		}
		channels = append(channels, map[string]any{"channel": c, "status": latest, "attempts": atts})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"alert_id": id, "alert_status": a.Status, "channels": channels,
		"note": "accepted = پذیرش توسط ارائه‌دهنده؛ تحویل به کاربر را تضمین نمی‌کند. unknown = در حال تطبیق؛ بدون ارسال مجدد کورکورانه."})
	return rows.Err()
}

// public returns active operational alerts covering a point, for citizens.
func (m *Module) public(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.AlertReadPublic, "alert", ""); err != nil {
		return err
	}
	q := r.URL.Query()
	lat, e1 := strconv.ParseFloat(q.Get("lat"), 64)
	lng, e2 := strconv.ParseFloat(q.Get("lng"), 64)
	if e1 != nil || e2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return httpx.Validation(httpx.FieldDetail{Field: "lat,lng", Reason: "invalid"})
	}
	rows, err := m.Pool.Query(ctx, `SELECT a.id, a.severity, a.rendered_text, o.name, a.issued_at, a.expires_at, a.region_label
		FROM alerts a JOIN organizations o ON o.id=a.issuer_org_id
		WHERE a.mode='operational' AND a.status IN ('sending','sent','partially_sent') AND a.expires_at > now()
		  AND ST_Intersects(a.region_geom, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography)
		ORDER BY a.issued_at DESC LIMIT 20`, lng, lat)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var sev, text, issuer, label string
		var issued *time.Time
		var exp time.Time
		if err := rows.Scan(&id, &sev, &text, &issuer, &issued, &exp, &label); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "severity": sev, "text": text, "issuer": issuer, "issued_at": issued,
			"expires_at": exp, "region_label": label})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return rows.Err()
}

// RecomputeStatus updates an alert's aggregate status from its latest attempt per channel (inside tx).
func RecomputeStatus(ctx context.Context, tx pgx.Tx, alertID uuid.UUID) (string, error) {
	var status string
	var version int
	if err := tx.QueryRow(ctx, `SELECT status, version FROM alerts WHERE id=$1 FOR UPDATE`, alertID).Scan(&status, &version); err != nil {
		return "", err
	}
	if status == Cancelled || status == Expired || status == Draft || status == PendingApproval || status == Approved {
		return status, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (channel) channel, status FROM notification_attempts
		WHERE alert_id=$1 ORDER BY channel, attempt_no DESC`, alertID)
	if err != nil {
		return "", err
	}
	latest := map[string]string{}
	for rows.Next() {
		var c, s string
		if err := rows.Scan(&c, &s); err != nil {
			rows.Close()
			return "", err
		}
		latest[c] = s
	}
	rows.Close()
	next := AggregateStatus(latest)
	if next == status {
		return status, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE alerts SET status=$2, updated_at=now(), version=version+1 WHERE id=$1`, alertID, next); err != nil {
		return "", err
	}
	_, err = outbox.Enqueue(ctx, tx, outbox.Event{Type: "alert.status_changed", Aggregate: outbox.Aggregate{Type: "alert", ID: alertID, Version: version + 1},
		Payload: map[string]any{"alert_id": alertID, "from": status, "to": next, "channels": latest}})
	return next, err
}

// ExpireDue moves undelivered alerts past their expiry to expired (called by the notifier's sweeper).
func ExpireDue(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE alerts SET status='expired', updated_at=now(), version=version+1
			WHERE status IN ('draft','pending_approval','approved') AND expires_at <= now() RETURNING id, version`)
		if err != nil {
			return err
		}
		type exp struct {
			id uuid.UUID
			v  int
		}
		var ids []exp
		for rows.Next() {
			var e exp
			if err := rows.Scan(&e.id, &e.v); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, e)
		}
		rows.Close()
		for _, e := range ids {
			if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "alert.expired", Aggregate: outbox.Aggregate{Type: "alert", ID: e.id, Version: e.v},
				Payload: map[string]any{"alert_id": e.id}}); err != nil {
				return err
			}
			if err := audit.Write(ctx, tx, audit.Entry{Action: "alert.expire", TargetType: "alert", TargetID: e.id.String(),
				Outcome: "success", Reason: "expires_at reached"}); err != nil {
				return err
			}
		}
		// Pending attempts of sending alerts past expiry are superseded (never send stale warnings).
		if _, err := tx.Exec(ctx, `UPDATE notification_attempts na SET status='superseded', detail='alert expired', updated_at=now()
			FROM alerts a WHERE a.id=na.alert_id AND a.expires_at <= now() AND na.status='pending'`); err != nil {
			return err
		}
		n = int64(len(ids))
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("expire alerts: %w", err)
	}
	return n, nil
}
