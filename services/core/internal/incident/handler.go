package incident

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/report"
)

type Module struct {
	Pool  *pgxpool.Pool
	Guard *auth.Guard
	Area  gis.Area
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("POST /incidents", m.create)
	r.Auth("GET /incidents", m.list)
	r.Auth("GET /incidents/{id}", m.get)
	r.Auth("POST /incidents/{id}/transitions", m.transition)
	r.Auth("POST /incidents/{id}/assessment", m.assess)
	r.Auth("POST /incidents/{id}/reports", m.linkReport)
}

type Incident struct {
	ID             uuid.UUID  `json:"id"`
	Code           string     `json:"code"`
	Type           string     `json:"type"`
	Title          string     `json:"title"`
	Severity       string     `json:"severity"`
	ResponseLevel  string     `json:"response_level"`
	Status         string     `json:"status"`
	OwnerOrgID     uuid.UUID  `json:"owner_org_id"`
	Location       *gis.Point `json:"location"`
	CreatedBy      uuid.UUID  `json:"created_by"`
	OpenedAt       time.Time  `json:"opened_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ClosedAt       *time.Time `json:"closed_at"`
	Version        int        `json:"version"`
	ReportCount    int        `json:"report_count"`
	ActiveAssigns  int        `json:"active_assignments"`
	AllowedTargets []string   `json:"allowed_transitions"`
}

const selectCols = `i.id, i.code, i.type, i.title, i.severity, i.response_level, i.status, i.owner_org_id,
	ST_Y(i.location::geometry), ST_X(i.location::geometry), i.created_by, i.opened_at, i.updated_at, i.closed_at, i.version,
	(SELECT count(*) FROM incident_reports ir WHERE ir.incident_id = i.id),
	(SELECT count(*) FROM assignments a WHERE a.incident_id = i.id AND a.status IN ('proposed','assigned','acknowledged','en_route','on_scene'))`

func scan(row pgx.Row) (Incident, error) {
	var in Incident
	var lat, lng *float64
	err := row.Scan(&in.ID, &in.Code, &in.Type, &in.Title, &in.Severity, &in.ResponseLevel, &in.Status, &in.OwnerOrgID,
		&lat, &lng, &in.CreatedBy, &in.OpenedAt, &in.UpdatedAt, &in.ClosedAt, &in.Version, &in.ReportCount, &in.ActiveAssigns)
	if lat != nil && lng != nil {
		in.Location = &gis.Point{Lat: *lat, Lng: *lng}
	}
	in.AllowedTargets = AllowedTargets(in.Status)
	return in, err
}

func Load(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Incident, error) {
	sql := `SELECT ` + selectCols + ` FROM incidents i WHERE i.id=$1`
	if forUpdate {
		sql += ` FOR UPDATE OF i`
	}
	in, err := scan(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return in, httpx.ErrNotFound
	}
	return in, err
}

// CanRead checks org-scoped read, or a responder's read access via an assignment to them.
func CanRead(ctx context.Context, q db.DBTX, p *auth.Principal, in Incident) (bool, error) {
	if p.CanInOrg(auth.IncidentRead, in.OwnerOrgID) {
		return true, nil
	}
	if p.Has(auth.IncidentReadAssign) {
		var ok bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assignments WHERE incident_id=$1 AND assignee_id=$2)`, in.ID, p.UserID).Scan(&ok)
		return ok, err
	}
	return false, nil
}

func appendEvent(ctx context.Context, tx pgx.Tx, incidentID uuid.UUID, typ string, actor uuid.UUID, payload map[string]any) error {
	_, err := tx.Exec(ctx, `INSERT INTO incident_events (incident_id, event_type, actor_id, payload, correlation_id)
		VALUES ($1,$2,$3,$4,$5)`, incidentID, typ, actor, payload, httpx.CorrelationID(ctx))
	return err
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	key, err := idempotency.Key(r)
	if err != nil {
		return err
	}
	var req CreateRequest
	if err := httpx.DecodeJSON(w, r, &req, 32<<10); err != nil {
		return err
	}
	if err := req.Validate(m.Area); err != nil {
		return err
	}
	if err := m.Guard.RequireOrg(ctx, auth.IncidentCreate, req.OwnerOrgID, "incident", ""); err != nil {
		return err
	}
	var out Incident
	var replay *idempotency.Stored
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		replay, err = idempotency.Begin(ctx, tx, p.UserID, "POST /incidents", key, idempotency.Hash(req))
		if err != nil || replay != nil {
			return err
		}
		var orgExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id=$1)`, req.OwnerOrgID).Scan(&orgExists); err != nil {
			return err
		}
		if !orgExists {
			return httpx.Validation(httpx.FieldDetail{Field: "owner_org_id", Reason: "not_found"})
		}
		id := uuid.New()
		var seq int64
		if err := tx.QueryRow(ctx, `SELECT nextval('incident_code_seq')`).Scan(&seq); err != nil {
			return err
		}
		code := fmt.Sprintf("INC-%s-%04d", time.Now().UTC().Format("20060102"), seq)
		var lat, lng *float64
		if req.Location != nil {
			lat, lng = &req.Location.Lat, &req.Location.Lng
		}
		if _, err := tx.Exec(ctx, `INSERT INTO incidents (id, code, type, title, severity, response_level, status, owner_org_id, location, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8, CASE WHEN $9::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($10, $9), 4326)::geography END, $11)`,
			id, code, req.Type, req.Title, req.Severity, req.ResponseLevel, req.Status, req.OwnerOrgID, lat, lng, p.UserID); err != nil {
			return err
		}
		if err := appendEvent(ctx, tx, id, "created", p.UserID, map[string]any{"status": req.Status, "severity": req.Severity,
			"response_level": req.ResponseLevel, "reason": req.Reason}); err != nil {
			return err
		}
		for i, rid := range req.ReportIDs {
			rel := "evidence"
			if i == 0 {
				rel = "origin"
			}
			if err := link(ctx, tx, id, rid, rel, "پیوند هنگام تشکیل حادثه", p.UserID); err != nil {
				return err
			}
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "incident.created", Aggregate: outbox.Aggregate{Type: "incident", ID: id, Version: 1},
			Payload: map[string]any{"incident_id": id, "code": code, "type": req.Type, "severity": req.Severity, "status": req.Status,
				"response_level": req.ResponseLevel, "owner_org_id": req.OwnerOrgID, "location": req.Location, "report_ids": req.ReportIDs}}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "incident.create",
			TargetType: "incident", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"code": code, "severity": req.Severity}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		if out, err = Load(ctx, tx, id, false); err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, p.UserID, "POST /incidents", key, http.StatusCreated, out)
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

func link(ctx context.Context, tx pgx.Tx, incidentID, reportID uuid.UUID, rel, reason string, actor uuid.UUID) error {
	if err := report.MarkLinked(ctx, tx, reportID, actor, incidentID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO incident_reports (incident_id, report_id, relation_type, reason, linked_by) VALUES ($1,$2,$3,$4,$5)`,
		incidentID, reportID, rel, reason, actor)
	if db.IsUniqueViolation(err, "") {
		return httpx.Conflict("ALREADY_LINKED", "این گزارش قبلاً به این حادثه پیوند شده است")
	}
	if err != nil {
		return err
	}
	return appendEvent(ctx, tx, incidentID, "report_linked", actor, map[string]any{"report_id": reportID, "relation_type": rel, "reason": reason})
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	all, orgs := p.OrgScope(auth.IncidentRead)
	assignedOnly := false
	if !all && len(orgs) == 0 {
		if !p.Has(auth.IncidentReadAssign) {
			return m.Guard.Deny(ctx, string(auth.IncidentRead), "incident", "", "missing_permission", nil)
		}
		assignedOnly = true
	}
	status, err := httpx.OneOf(r, "status", Statuses...)
	if err != nil {
		return err
	}
	sev, err := httpx.OneOf(r, "severity", Severities...)
	if err != nil {
		return err
	}
	limit, err := httpx.PageSize(r, 50, 200)
	if err != nil {
		return err
	}
	cur, err := httpx.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	var curT *time.Time
	var curID *uuid.UUID
	if cur != nil {
		curT, curID = &cur.T, &cur.ID
	}
	activeOnly := r.URL.Query().Get("active") == "true"
	rows, err := m.Pool.Query(ctx, `SELECT `+selectCols+` FROM incidents i
		WHERE ($1 OR i.owner_org_id = ANY($2))
		  AND (NOT $3 OR EXISTS (SELECT 1 FROM assignments a WHERE a.incident_id=i.id AND a.assignee_id=$4))
		  AND ($5 = '' OR i.status = $5) AND ($6 = '' OR i.severity = $6)
		  AND (NOT $7 OR i.status NOT IN ('resolved','closed'))
		  AND ($8::timestamptz IS NULL OR (i.updated_at, i.id) < ($8, $9))
		ORDER BY i.updated_at DESC, i.id DESC LIMIT $10`,
		all || assignedOnly, orgs, assignedOnly, p.UserID, status, sev, activeOnly, curT, curID, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Incident{}
	for rows.Next() {
		in, err := scan(rows)
		if err != nil {
			return err
		}
		items = append(items, in)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	resp := map[string]any{"items": items}
	if len(items) > limit {
		last := items[limit-1]
		resp["items"] = items[:limit]
		resp["next_cursor"] = httpx.Cursor{T: last.UpdatedAt, ID: last.ID}.Encode()
	}
	httpx.JSON(w, http.StatusOK, resp)
	return nil
}

type timelineEntry struct {
	EventType     string          `json:"event_type"`
	ActorID       *uuid.UUID      `json:"actor_id"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"created_at"`
	CorrelationID string          `json:"correlation_id"`
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	in, err := Load(ctx, m.Pool, id, false)
	if err != nil {
		return err
	}
	ok, err := CanRead(ctx, m.Pool, p, in)
	if err != nil {
		return err
	}
	if !ok {
		return m.Guard.Deny(ctx, string(auth.IncidentRead), "incident", id.String(), "out_of_org_scope", httpx.ErrNotFound)
	}
	resp := map[string]any{"incident": in}

	timeline := []timelineEntry{}
	rows, err := m.Pool.Query(ctx, `SELECT event_type, actor_id, payload, created_at, correlation_id FROM incident_events
		WHERE incident_id=$1 ORDER BY created_at, id`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t timelineEntry
		if err := rows.Scan(&t.EventType, &t.ActorID, &t.Payload, &t.CreatedAt, &t.CorrelationID); err != nil {
			rows.Close()
			return err
		}
		timeline = append(timeline, t)
	}
	rows.Close()
	resp["timeline"] = timeline

	type linked struct {
		ReportID     uuid.UUID `json:"report_id"`
		Type         string    `json:"type"`
		Status       string    `json:"status"`
		RelationType string    `json:"relation_type"`
		Reason       string    `json:"reason"`
		LinkedAt     time.Time `json:"linked_at"`
	}
	reports := []linked{}
	rows, err = m.Pool.Query(ctx, `SELECT r.id, r.type, r.status, ir.relation_type, ir.reason, ir.linked_at
		FROM incident_reports ir JOIN reports r ON r.id = ir.report_id WHERE ir.incident_id=$1 ORDER BY ir.linked_at`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var l linked
		if err := rows.Scan(&l.ReportID, &l.Type, &l.Status, &l.RelationType, &l.Reason, &l.LinkedAt); err != nil {
			rows.Close()
			return err
		}
		reports = append(reports, l)
	}
	rows.Close()
	resp["reports"] = reports

	type assignment struct {
		ID           uuid.UUID  `json:"id"`
		ResourceID   uuid.UUID  `json:"resource_id"`
		ResourceName string     `json:"resource_name"`
		ResourceType string     `json:"resource_type"`
		Status       string     `json:"status"`
		AssigneeID   *uuid.UUID `json:"assignee_id"`
		AssignedAt   time.Time  `json:"assigned_at"`
		Version      int        `json:"version"`
	}
	assigns := []assignment{}
	rows, err = m.Pool.Query(ctx, `SELECT a.id, a.resource_id, rs.name, rs.type, a.status, a.assignee_id, a.assigned_at, a.version
		FROM assignments a JOIN resources rs ON rs.id = a.resource_id WHERE a.incident_id=$1 ORDER BY a.assigned_at DESC`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a assignment
		if err := rows.Scan(&a.ID, &a.ResourceID, &a.ResourceName, &a.ResourceType, &a.Status, &a.AssigneeID, &a.AssignedAt, &a.Version); err != nil {
			rows.Close()
			return err
		}
		assigns = append(assigns, a)
	}
	rows.Close()
	resp["assignments"] = assigns

	alerts := []map[string]any{}
	rows, err = m.Pool.Query(ctx, `SELECT id, status, mode, severity, region_label, expires_at FROM alerts WHERE incident_id=$1 ORDER BY created_at DESC`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var aid uuid.UUID
		var st, mode, sev, label string
		var exp time.Time
		if err := rows.Scan(&aid, &st, &mode, &sev, &label, &exp); err != nil {
			rows.Close()
			return err
		}
		alerts = append(alerts, map[string]any{"id": aid, "status": st, "mode": mode, "severity": sev, "region_label": label, "expires_at": exp})
	}
	rows.Close()
	resp["alerts"] = alerts
	httpx.JSON(w, http.StatusOK, resp)
	return nil
}

func (m *Module) transition(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req TransitionRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Incident
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		in, err := Load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		perm := RequiredPermission(in.Status, req.To)
		if !p.CanInOrg(perm, in.OwnerOrgID) {
			return m.Guard.Deny(ctx, string(perm), "incident", id.String(), fmt.Sprintf("transition %s->%s", in.Status, req.To), nil)
		}
		if in.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if !CanTransition(in.Status, req.To) {
			return httpx.Conflict("INVALID_TRANSITION", fmt.Sprintf("انتقال از %s به %s مجاز نیست", in.Status, req.To))
		}
		if _, err := tx.Exec(ctx, `UPDATE incidents SET status=$2, updated_at=now(), version=version+1,
			closed_at = CASE WHEN $2='closed' THEN now() WHEN $2='open' THEN NULL ELSE closed_at END WHERE id=$1`, id, req.To); err != nil {
			return err
		}
		payload := map[string]any{"from": in.Status, "to": req.To, "reason": req.Reason, "reopen": IsReopen(in.Status, req.To)}
		if err := appendEvent(ctx, tx, id, "status_changed", p.UserID, payload); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "incident.status_changed",
			Aggregate: outbox.Aggregate{Type: "incident", ID: id, Version: in.Version + 1}, Payload: payload}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "incident.transition",
			TargetType: "incident", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"from": in.Status, "to": req.To}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
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

// assess changes severity / response level with a mandatory reason.
func (m *Module) assess(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req AssessmentRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Incident
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		in, err := Load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		// Raising the response level to L3/L4 is a command decision.
		perm := auth.IncidentTransition
		if req.ResponseLevel == "L3" || req.ResponseLevel == "L4" {
			perm = auth.IncidentApprove
		}
		if !p.CanInOrg(perm, in.OwnerOrgID) {
			return m.Guard.Deny(ctx, string(perm), "incident", id.String(), "assessment", nil)
		}
		if in.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if in.Status == Closed {
			return httpx.Conflict("INCIDENT_CLOSED", "حادثه بسته است")
		}
		if _, err := tx.Exec(ctx, `UPDATE incidents SET severity=$2, response_level=$3, updated_at=now(), version=version+1 WHERE id=$1`,
			id, req.Severity, req.ResponseLevel); err != nil {
			return err
		}
		payload := map[string]any{"from_severity": in.Severity, "to_severity": req.Severity, "from_level": in.ResponseLevel,
			"to_level": req.ResponseLevel, "reason": req.Reason}
		if err := appendEvent(ctx, tx, id, "assessment_changed", p.UserID, payload); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "incident.assessment_changed",
			Aggregate: outbox.Aggregate{Type: "incident", ID: id, Version: in.Version + 1}, Payload: payload}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "incident.assessment",
			TargetType: "incident", TargetID: id.String(), Outcome: "success", Reason: req.Reason, Details: payload,
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
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

func (m *Module) linkReport(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req LinkRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Incident
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		in, err := Load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !p.CanInOrg(auth.IncidentLinkReport, in.OwnerOrgID) {
			return m.Guard.Deny(ctx, string(auth.IncidentLinkReport), "incident", id.String(), "out_of_org_scope", nil)
		}
		if in.Status == Closed {
			return httpx.Conflict("INCIDENT_CLOSED", "حادثه بسته است")
		}
		if err := link(ctx, tx, id, req.ReportID, req.RelationType, req.Reason, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE incidents SET updated_at=now(), version=version+1 WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "incident.report_linked",
			Aggregate: outbox.Aggregate{Type: "incident", ID: id, Version: in.Version + 1},
			Payload:   map[string]any{"incident_id": id, "report_id": req.ReportID, "relation_type": req.RelationType}}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "incident.link_report",
			TargetType: "incident", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"report_id": req.ReportID}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
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
