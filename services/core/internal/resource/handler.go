package resource

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/incident"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

type Module struct {
	Pool       *pgxpool.Pool
	Guard      *auth.Guard
	Area       gis.Area
	StaleAfter time.Duration
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("POST /resources", m.create)
	r.Auth("GET /resources", m.list)
	r.Auth("GET /resources/{id}", m.get)
	r.Auth("POST /resources/{id}/location", m.updateLocation)
	r.Auth("POST /resources/{id}/status", m.setStatus)
	r.Auth("POST /incidents/{id}/assignments", m.assign)
	r.Auth("GET /assignments/mine", m.mine)
	r.Auth("POST /assignments/{id}/status", m.assignmentStatus)
}

type Resource struct {
	ID             uuid.UUID       `json:"id"`
	OrganizationID uuid.UUID       `json:"organization_id"`
	Type           string          `json:"type"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	Capabilities   json.RawMessage `json:"capabilities"`
	Capacity       *int            `json:"capacity"`
	Location       *gis.Point      `json:"location"`
	LastSeenAt     *time.Time      `json:"last_seen_at"`
	// Stale is true when the last position report is older than the threshold: it must not be shown as live.
	Stale     bool      `json:"stale"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int       `json:"version"`
}

const selectCols = `r.id, r.organization_id, r.type, r.name, r.status, r.capabilities, r.capacity,
	ST_Y(r.location::geometry), ST_X(r.location::geometry), r.last_seen_at, r.updated_at, r.version`

func (m *Module) scan(row pgx.Row) (Resource, error) {
	var rs Resource
	var lat, lng *float64
	err := row.Scan(&rs.ID, &rs.OrganizationID, &rs.Type, &rs.Name, &rs.Status, &rs.Capabilities, &rs.Capacity,
		&lat, &lng, &rs.LastSeenAt, &rs.UpdatedAt, &rs.Version)
	if lat != nil && lng != nil {
		rs.Location = &gis.Point{Lat: *lat, Lng: *lng}
	}
	rs.Stale = rs.Type != "shelter" && (rs.LastSeenAt == nil || time.Since(*rs.LastSeenAt) > m.StaleAfter)
	return rs, err
}

func (m *Module) load(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Resource, error) {
	sql := `SELECT ` + selectCols + ` FROM resources r WHERE r.id=$1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	rs, err := m.scan(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return rs, httpx.ErrNotFound
	}
	return rs, err
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	var req CreateRequest
	if err := httpx.DecodeJSON(w, r, &req, 16<<10); err != nil {
		return err
	}
	if err := req.Validate(m.Area); err != nil {
		return err
	}
	if err := m.Guard.RequireOrg(ctx, auth.ResourceManage, req.OrganizationID, "resource", ""); err != nil {
		return err
	}
	if req.Capabilities == nil {
		req.Capabilities = map[string]any{}
	}
	var out Resource
	err := db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		id := uuid.New()
		var lat, lng *float64
		if req.Location != nil {
			lat, lng = &req.Location.Lat, &req.Location.Lng
		}
		_, err := tx.Exec(ctx, `INSERT INTO resources (id, organization_id, type, name, capabilities, capacity, location, last_seen_at)
			VALUES ($1,$2,$3,$4,$5,$6, CASE WHEN $7::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($8, $7), 4326)::geography END,
			CASE WHEN $7::float8 IS NULL THEN NULL ELSE now() END)`,
			id, req.OrganizationID, req.Type, req.Name, req.Capabilities, req.Capacity, lat, lng)
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "resource.create",
			TargetType: "resource", TargetID: id.String(), Outcome: "success", Details: map[string]any{"type": req.Type},
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = m.load(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	all, orgs := p.OrgScope(auth.ResourceRead)
	if !all && len(orgs) == 0 {
		return m.Guard.Deny(ctx, string(auth.ResourceRead), "resource", "", "missing_permission", nil)
	}
	typ, err := httpx.OneOf(r, "type", Types...)
	if err != nil {
		return err
	}
	status, err := httpx.OneOf(r, "status", "available", "assigned", "en_route", "on_scene", "out_of_service")
	if err != nil {
		return err
	}
	limit, err := httpx.PageSize(r, 200, 1000)
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+selectCols+` FROM resources r
		WHERE ($1 OR r.organization_id = ANY($2)) AND ($3 = '' OR r.type = $3) AND ($4 = '' OR r.status = $4)
		ORDER BY r.type, r.name LIMIT $5`, all, orgs, typ, status, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Resource{}
	for rows.Next() {
		rs, err := m.scan(rows)
		if err != nil {
			return err
		}
		items = append(items, rs)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "stale_after_seconds": int(m.StaleAfter.Seconds())})
	return rows.Err()
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	rs, err := m.load(ctx, m.Pool, id, false)
	if err != nil {
		return err
	}
	if !auth.FromContext(ctx).CanInOrg(auth.ResourceRead, rs.OrganizationID) {
		return m.Guard.Deny(ctx, string(auth.ResourceRead), "resource", id.String(), "out_of_org_scope", httpx.ErrNotFound)
	}
	httpx.JSON(w, http.StatusOK, rs)
	return nil
}

type locationRequest struct {
	Lat        float64    `json:"lat"`
	Lng        float64    `json:"lng"`
	ObservedAt *time.Time `json:"observed_at"`
}

// updateLocation accepts position reports from resource managers or the responder on an active assignment.
// Late (offline-queued) reports older than the stored one are ignored rather than overwriting newer data.
func (m *Module) updateLocation(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req locationRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	var v httpx.Validator
	gis.ValidatePoint(&v, "location", gis.Point{Lat: req.Lat, Lng: req.Lng}, m.Area)
	now := time.Now().UTC()
	if req.ObservedAt == nil {
		req.ObservedAt = &now
	}
	v.Check(!req.ObservedAt.After(now.Add(2*time.Minute)), "observed_at", "in_future")
	if err := v.Err(); err != nil {
		return err
	}
	rs, err := m.load(ctx, m.Pool, id, false)
	if err != nil {
		return err
	}
	if !p.CanInOrg(auth.ResourceManage, rs.OrganizationID) {
		var assigned bool
		if err := m.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assignments WHERE resource_id=$1 AND assignee_id=$2
			AND status = ANY($3))`, id, p.UserID, ActiveAssignmentStatuses).Scan(&assigned); err != nil {
			return err
		}
		if !assigned || !p.Has(auth.AssignmentUpdate) {
			return m.Guard.Deny(ctx, string(auth.ResourceManage), "resource", id.String(), "not_assigned", nil)
		}
	}
	tag, err := m.Pool.Exec(ctx, `UPDATE resources SET location = ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography,
		last_seen_at=$4, updated_at=now() WHERE id=$1 AND (last_seen_at IS NULL OR last_seen_at < $4)`,
		id, req.Lng, req.Lat, *req.ObservedAt)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"applied": tag.RowsAffected() == 1})
	return nil
}

type statusRequest struct {
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Version int    `json:"version"`
}

func (m *Module) setStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req statusRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	var v httpx.Validator
	v.Check(req.Status == "available" || req.Status == "out_of_service", "status", "must_be_available_or_out_of_service")
	v.Check(req.Reason != "", "reason", "required")
	v.Check(req.Version > 0, "version", "required")
	if err := v.Err(); err != nil {
		return err
	}
	var out Resource
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		rs, err := m.load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !p.CanInOrg(auth.ResourceManage, rs.OrganizationID) {
			return m.Guard.Deny(ctx, string(auth.ResourceManage), "resource", id.String(), "out_of_org_scope", nil)
		}
		if rs.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assignments WHERE resource_id=$1 AND status = ANY($2))`,
			id, ActiveAssignmentStatuses).Scan(&active); err != nil {
			return err
		}
		if active {
			return httpx.Conflict("RESOURCE_HAS_ACTIVE_ASSIGNMENT", "منبع مأموریت فعال دارد؛ ابتدا مأموریت را ببندید یا لغو کنید")
		}
		if _, err := tx.Exec(ctx, `UPDATE resources SET status=$2, updated_at=now(), version=version+1 WHERE id=$1`, id, req.Status); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "resource.status",
			TargetType: "resource", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"from": rs.Status, "to": req.Status}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = m.load(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

type Assignment struct {
	ID           uuid.UUID  `json:"id"`
	IncidentID   uuid.UUID  `json:"incident_id"`
	IncidentCode string     `json:"incident_code"`
	ResourceID   uuid.UUID  `json:"resource_id"`
	ResourceName string     `json:"resource_name"`
	Status       string     `json:"status"`
	AssignedBy   uuid.UUID  `json:"assigned_by"`
	AssigneeID   *uuid.UUID `json:"assignee_id"`
	Reason       string     `json:"reason"`
	AssignedAt   time.Time  `json:"assigned_at"`
	ReleasedAt   *time.Time `json:"released_at"`
	Version      int        `json:"version"`
}

const assignmentCols = `a.id, a.incident_id, i.code, a.resource_id, rs.name, a.status, a.assigned_by, a.assignee_id, a.reason,
	a.assigned_at, a.released_at, a.version`

func scanAssignment(row pgx.Row) (Assignment, error) {
	var a Assignment
	err := row.Scan(&a.ID, &a.IncidentID, &a.IncidentCode, &a.ResourceID, &a.ResourceName, &a.Status, &a.AssignedBy,
		&a.AssigneeID, &a.Reason, &a.AssignedAt, &a.ReleasedAt, &a.Version)
	return a, err
}

func loadAssignment(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Assignment, error) {
	sql := `SELECT ` + assignmentCols + ` FROM assignments a JOIN incidents i ON i.id=a.incident_id
		JOIN resources rs ON rs.id=a.resource_id WHERE a.id=$1`
	if forUpdate {
		sql += ` FOR UPDATE OF a`
	}
	a, err := scanAssignment(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return a, httpx.ErrNotFound
	}
	return a, err
}

// assign allocates a resource atomically (AT-06): the resource row is locked, its availability re-checked,
// and a partial unique index guarantees at most one active assignment per resource even under races.
func (m *Module) assign(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	incidentID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req AssignRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Assignment
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		in, err := incident.Load(ctx, tx, incidentID, false)
		if err != nil {
			return err
		}
		if !slices.Contains([]string{incident.Open, incident.Active, incident.Escalated, incident.Contained}, in.Status) {
			return httpx.Conflict("INCIDENT_NOT_ACTIVE", "تخصیص منبع فقط برای حادثه باز/فعال مجاز است")
		}
		rs, err := m.load(ctx, tx, req.ResourceID, true)
		if err != nil {
			if err == httpx.ErrNotFound {
				return httpx.Validation(httpx.FieldDetail{Field: "resource_id", Reason: "not_found"})
			}
			return err
		}
		if !p.CanInOrg(auth.ResourceAllocate, rs.OrganizationID) {
			return m.Guard.Deny(ctx, string(auth.ResourceAllocate), "resource", rs.ID.String(), "out_of_org_scope", nil)
		}
		if rs.Status != "available" {
			return httpx.Conflict("RESOURCE_UNAVAILABLE", "منبع در دسترس نیست (وضعیت: "+rs.Status+")")
		}
		if req.AssigneeID != nil {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active')`, *req.AssigneeID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.Validation(httpx.FieldDetail{Field: "assignee_id", Reason: "not_found"})
			}
		}
		status := "assigned"
		if req.Proposed {
			status = "proposed"
		}
		id := uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO assignments (id, incident_id, resource_id, status, assigned_by, assignee_id, reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, incidentID, rs.ID, status, p.UserID, req.AssigneeID, req.Reason)
		if db.IsUniqueViolation(err, "assignments_one_active_per_resource") {
			return httpx.Conflict("RESOURCE_UNAVAILABLE", "منبع هم‌زمان به مأموریت دیگری تخصیص یافته است")
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE resources SET status='assigned', updated_at=now(), version=version+1
			WHERE id=$1 AND status='available'`, rs.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.Conflict("RESOURCE_UNAVAILABLE", "منبع در دسترس نیست")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO incident_events (incident_id, event_type, actor_id, payload, correlation_id)
			VALUES ($1,'resource_assigned',$2,$3,$4)`, incidentID, p.UserID,
			map[string]any{"assignment_id": id, "resource_id": rs.ID, "resource_name": rs.Name, "reason": req.Reason}, httpx.CorrelationID(ctx)); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "resource.assigned",
			Aggregate: outbox.Aggregate{Type: "resource", ID: rs.ID, Version: rs.Version + 1},
			Payload: map[string]any{"assignment_id": id, "incident_id": incidentID, "resource_id": rs.ID, "resource_type": rs.Type,
				"status": status, "assignee_id": req.AssigneeID}}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "resource.allocate",
			TargetType: "assignment", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"incident_id": incidentID, "resource_id": rs.ID}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = loadAssignment(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

func (m *Module) mine(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.AssignmentUpdate, "assignment", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	rows, err := m.Pool.Query(ctx, `SELECT `+assignmentCols+` FROM assignments a JOIN incidents i ON i.id=a.incident_id
		JOIN resources rs ON rs.id=a.resource_id WHERE a.assignee_id=$1 ORDER BY a.assigned_at DESC LIMIT 100`, p.UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Assignment{}
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return err
		}
		items = append(items, a)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return rows.Err()
}

func (m *Module) assignmentStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req AssignmentStatusRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Assignment
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		a, err := loadAssignment(ctx, tx, id, true)
		if err != nil {
			return err
		}
		rs, err := m.load(ctx, tx, a.ResourceID, true)
		if err != nil {
			return err
		}
		isAssignee := a.AssigneeID != nil && *a.AssigneeID == p.UserID && p.Has(auth.AssignmentUpdate)
		if !isAssignee && !p.CanInOrg(auth.ResourceAllocate, rs.OrganizationID) {
			return m.Guard.Deny(ctx, string(auth.AssignmentUpdate), "assignment", id.String(), "not_assignee", nil)
		}
		if a.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if !CanTransitionAssignment(a.Status, req.Status) {
			return httpx.Conflict("INVALID_TRANSITION", "انتقال وضعیت مأموریت مجاز نیست")
		}
		released := req.Status == "completed" || req.Status == "cancelled"
		if _, err := tx.Exec(ctx, `UPDATE assignments SET status=$2, version=version+1,
			released_at = CASE WHEN $3 THEN now() ELSE released_at END WHERE id=$1`, id, req.Status, released); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE resources SET status=$2, updated_at=now(), version=version+1 WHERE id=$1`,
			rs.ID, ResourceStatusFor(req.Status)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO incident_events (incident_id, event_type, actor_id, payload, correlation_id)
			VALUES ($1,'assignment_status_changed',$2,$3,$4)`, a.IncidentID, p.UserID,
			map[string]any{"assignment_id": id, "resource_id": rs.ID, "from": a.Status, "to": req.Status, "reason": req.Reason},
			httpx.CorrelationID(ctx)); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "resource.assignment_status_changed",
			Aggregate: outbox.Aggregate{Type: "resource", ID: rs.ID, Version: rs.Version + 1},
			Payload:   map[string]any{"assignment_id": id, "incident_id": a.IncidentID, "from": a.Status, "to": req.Status}}); err != nil {
			return err
		}
		if released {
			if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "resource.release",
				TargetType: "assignment", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
				Details: map[string]any{"to": req.Status}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
				return err
			}
		}
		out, err = loadAssignment(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}
