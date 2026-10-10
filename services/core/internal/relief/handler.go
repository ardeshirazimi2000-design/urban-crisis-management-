package relief

import (
	"context"
	"net/http"
	"slices"
	"strconv"
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
	Pool  *pgxpool.Pool
	Guard *auth.Guard
	Area  gis.Area
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("POST /incidents/{id}/needs", m.createNeed)
	r.Auth("GET /needs", m.listNeeds)
	r.Auth("GET /needs/{id}", m.getNeed)
	r.Auth("POST /needs/{id}/fulfillments", m.fulfill)
	r.Auth("POST /needs/{id}/cancel", m.cancelNeed)
	r.Auth("GET /shelters", m.listShelters)
	r.Auth("GET /shelters/public", m.publicShelters)
	r.Auth("GET /shelters/{id}", m.getShelter)
	r.Auth("POST /shelters/{id}/occupancy", m.movement)
	r.Auth("POST /shelters/{id}/settings", m.settings)
}

// ---------------------------------------------------------------------------
// Needs
// ---------------------------------------------------------------------------

type Need struct {
	ID            uuid.UUID  `json:"id"`
	IncidentID    uuid.UUID  `json:"incident_id"`
	IncidentCode  string     `json:"incident_code"`
	IncidentTitle string     `json:"incident_title"`
	OwnerOrgID    uuid.UUID  `json:"owner_org_id"`
	Category      string     `json:"category"`
	Description   string     `json:"description"`
	Quantity      int        `json:"quantity"`
	Fulfilled     int        `json:"fulfilled"`
	Remaining     int        `json:"remaining"`
	Unit          string     `json:"unit"`
	Priority      string     `json:"priority"`
	Status        string     `json:"status"`
	Location      *gis.Point `json:"location"`
	RequestedBy   uuid.UUID  `json:"requested_by"`
	RequesterName string     `json:"requested_by_name"`
	CancelReason  *string    `json:"cancel_reason"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ClosedAt      *time.Time `json:"closed_at"`
	Version       int        `json:"version"`
}

const needCols = `n.id, n.incident_id, i.code, i.title, i.owner_org_id, n.category, n.description, n.quantity, n.fulfilled,
	n.unit, n.priority, n.status, ST_Y(n.location::geometry), ST_X(n.location::geometry), n.requested_by,
	COALESCE(u.display_name, ''), n.cancel_reason, n.created_at, n.updated_at, n.closed_at, n.version`

const needFrom = ` FROM needs n JOIN incidents i ON i.id = n.incident_id LEFT JOIN users u ON u.id = n.requested_by`

func scanNeed(row pgx.Row) (Need, error) {
	var n Need
	var lat, lng *float64
	err := row.Scan(&n.ID, &n.IncidentID, &n.IncidentCode, &n.IncidentTitle, &n.OwnerOrgID, &n.Category, &n.Description,
		&n.Quantity, &n.Fulfilled, &n.Unit, &n.Priority, &n.Status, &lat, &lng, &n.RequestedBy, &n.RequesterName,
		&n.CancelReason, &n.CreatedAt, &n.UpdatedAt, &n.ClosedAt, &n.Version)
	if lat != nil && lng != nil {
		n.Location = &gis.Point{Lat: *lat, Lng: *lng}
	}
	n.Remaining = n.Quantity - n.Fulfilled
	if n.Status == NeedCancelled {
		n.Remaining = 0
	}
	return n, err
}

func loadNeed(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Need, error) {
	sql := `SELECT ` + needCols + needFrom + ` WHERE n.id=$1`
	if forUpdate {
		sql += ` FOR UPDATE OF n`
	}
	n, err := scanNeed(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return n, httpx.ErrNotFound
	}
	return n, err
}

// loadScoped loads a need and hides it (404) from callers without perm in the owning incident's organisation.
func (m *Module) loadScoped(ctx context.Context, q db.DBTX, id uuid.UUID, perm auth.Permission, forUpdate bool) (Need, error) {
	n, err := loadNeed(ctx, q, id, forUpdate)
	if err != nil {
		return n, err
	}
	p := auth.FromContext(ctx)
	if !p.CanInOrg(perm, n.OwnerOrgID) {
		if p.CanInOrg(auth.NeedRead, n.OwnerOrgID) {
			return n, m.Guard.Deny(ctx, string(perm), "need", id.String(), "missing_permission", nil)
		}
		return n, m.Guard.Deny(ctx, string(perm), "need", id.String(), "out_of_org_scope", httpx.ErrNotFound)
	}
	return n, nil
}

func (m *Module) createNeed(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	incidentID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req NeedRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := req.Validate(m.Area); err != nil {
		return err
	}
	var out Need
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		in, err := incident.Load(ctx, tx, incidentID, false)
		if err != nil {
			return err
		}
		if !p.CanInOrg(auth.NeedCreate, in.OwnerOrgID) {
			return m.Guard.Deny(ctx, string(auth.NeedCreate), "incident", incidentID.String(), "out_of_org_scope", nil)
		}
		if !slices.Contains([]string{incident.Open, incident.Active, incident.Escalated, incident.Contained}, in.Status) {
			return httpx.Conflict("INCIDENT_NOT_ACTIVE", "ثبت نیاز فقط برای حادثه باز یا فعال مجاز است")
		}
		id := uuid.New()
		var lat, lng *float64
		if req.Location != nil {
			lat, lng = &req.Location.Lat, &req.Location.Lng
		}
		if _, err := tx.Exec(ctx, `INSERT INTO needs (id, incident_id, category, description, quantity, unit, priority, location, requested_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,
			CASE WHEN $8::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($9, $8), 4326)::geography END, $10)`,
			id, incidentID, req.Category, req.Description, req.Quantity, req.Unit, req.Priority, lat, lng, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO incident_events (incident_id, event_type, actor_id, payload, correlation_id)
			VALUES ($1,'need_created',$2,$3,$4)`, incidentID, p.UserID, map[string]any{"need_id": id, "category": req.Category,
			"quantity": req.Quantity, "unit": req.Unit, "priority": req.Priority}, httpx.CorrelationID(ctx)); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "need.created",
			Aggregate: outbox.Aggregate{Type: "need", ID: id, Version: 1},
			Payload: map[string]any{"need_id": id, "incident_id": incidentID, "category": req.Category, "quantity": req.Quantity,
				"unit": req.Unit, "priority": req.Priority}}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "need.create",
			TargetType: "need", TargetID: id.String(), Outcome: "success",
			Details:       map[string]any{"incident_id": incidentID, "category": req.Category, "quantity": req.Quantity},
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = loadNeed(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

func (m *Module) listNeeds(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	all, orgs := p.OrgScope(auth.NeedRead)
	if !all && len(orgs) == 0 {
		return m.Guard.Deny(ctx, string(auth.NeedRead), "need", "", "missing_permission", nil)
	}
	// "active" = open + partially_met (what still has to be supplied).
	status, err := httpx.OneOf(r, "status", "active", NeedOpen, NeedPartiallyMet, NeedMet, NeedCancelled)
	if err != nil {
		return err
	}
	category, err := httpx.OneOf(r, "category", NeedCategories...)
	if err != nil {
		return err
	}
	var incidentID *uuid.UUID
	if s := r.URL.Query().Get("incident_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return httpx.Validation(httpx.FieldDetail{Field: "incident_id", Reason: "invalid_uuid"})
		}
		incidentID = &id
	}
	limit, err := httpx.PageSize(r, 200, 500)
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+needCols+needFrom+`
		WHERE ($1 OR i.owner_org_id = ANY($2))
		  AND ($3 = '' OR n.status = $3 OR ($3 = 'active' AND n.status IN ('open','partially_met')))
		  AND ($4 = '' OR n.category = $4)
		  AND ($5::uuid IS NULL OR n.incident_id = $5)
		ORDER BY (n.status IN ('open','partially_met')) DESC,
		  array_position(ARRAY['critical','high','medium','low'], n.priority), n.created_at
		LIMIT $6`, all, orgs, status, category, incidentID, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Need{}
	for rows.Next() {
		n, err := scanNeed(rows)
		if err != nil {
			return err
		}
		items = append(items, n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

type Fulfillment struct {
	ID           uuid.UUID  `json:"id"`
	Quantity     int        `json:"quantity"`
	Source       string     `json:"source"`
	ResourceID   *uuid.UUID `json:"resource_id"`
	ResourceName *string    `json:"resource_name"`
	Note         string     `json:"note"`
	ActorID      uuid.UUID  `json:"actor_id"`
	ActorName    string     `json:"actor_name"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (m *Module) getNeed(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	n, err := m.loadScoped(ctx, m.Pool, id, auth.NeedRead, false)
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT f.id, f.quantity, f.source, f.resource_id, rs.name, f.note, f.actor_id,
		COALESCE(u.display_name, ''), f.created_at
		FROM need_fulfillments f LEFT JOIN resources rs ON rs.id = f.resource_id LEFT JOIN users u ON u.id = f.actor_id
		WHERE f.need_id = $1 ORDER BY f.created_at`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	history := []Fulfillment{}
	for rows.Next() {
		var f Fulfillment
		if err := rows.Scan(&f.ID, &f.Quantity, &f.Source, &f.ResourceID, &f.ResourceName, &f.Note, &f.ActorID, &f.ActorName, &f.CreatedAt); err != nil {
			return err
		}
		history = append(history, f)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"need": n, "fulfillments": history})
	return nil
}

// fulfill records a (partial) supply against a need. The need's version makes a retried request fail with
// VERSION_CONFLICT instead of counting the same delivery twice; supply beyond what is missing is rejected.
func (m *Module) fulfill(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req FulfillRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Need
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		n, err := m.loadScoped(ctx, tx, id, auth.NeedFulfill, true)
		if err != nil {
			return err
		}
		if n.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if !slices.Contains(ActiveNeedStatuses, n.Status) {
			return httpx.Conflict("NEED_CLOSED", "این نیاز بسته شده است (وضعیت: "+n.Status+")")
		}
		if req.Quantity > n.Remaining {
			return httpx.Validation(httpx.FieldDetail{Field: "quantity", Reason: "exceeds_remaining"})
		}
		if req.ResourceID != nil {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM resources WHERE id=$1)`, *req.ResourceID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.Validation(httpx.FieldDetail{Field: "resource_id", Reason: "not_found"})
			}
		}
		fid := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO need_fulfillments (id, need_id, quantity, source, resource_id, note, actor_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, fid, id, req.Quantity, req.Source, req.ResourceID, req.Note, p.UserID); err != nil {
			return err
		}
		fulfilled := n.Fulfilled + req.Quantity
		status := StatusFor(fulfilled, n.Quantity)
		if _, err := tx.Exec(ctx, `UPDATE needs SET fulfilled=$2, status=$3, updated_at=now(), version=version+1,
			closed_at = CASE WHEN $3 = 'met' THEN now() ELSE NULL END WHERE id=$1`, id, fulfilled, status); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO incident_events (incident_id, event_type, actor_id, payload, correlation_id)
			VALUES ($1,'need_fulfilled',$2,$3,$4)`, n.IncidentID, p.UserID, map[string]any{"need_id": id, "category": n.Category,
			"quantity": req.Quantity, "unit": n.Unit, "source": req.Source, "status": status}, httpx.CorrelationID(ctx)); err != nil {
			return err
		}
		if err := m.needChanged(ctx, tx, n, status, fulfilled, "fulfilled"); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "need.fulfill",
			TargetType: "need", TargetID: id.String(), Outcome: "success",
			Details:       map[string]any{"quantity": req.Quantity, "source": req.Source, "fulfillment_id": fid, "status": status},
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = loadNeed(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

func (m *Module) cancelNeed(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req ReasonRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Need
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		n, err := m.loadScoped(ctx, tx, id, auth.NeedCreate, true)
		if err != nil {
			return err
		}
		if n.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if !slices.Contains(ActiveNeedStatuses, n.Status) {
			return httpx.Conflict("NEED_CLOSED", "این نیاز بسته شده است (وضعیت: "+n.Status+")")
		}
		if _, err := tx.Exec(ctx, `UPDATE needs SET status='cancelled', cancel_reason=$2, closed_at=now(), updated_at=now(),
			version=version+1 WHERE id=$1`, id, req.Reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO incident_events (incident_id, event_type, actor_id, payload, correlation_id)
			VALUES ($1,'need_cancelled',$2,$3,$4)`, n.IncidentID, p.UserID, map[string]any{"need_id": id, "category": n.Category,
			"reason": req.Reason}, httpx.CorrelationID(ctx)); err != nil {
			return err
		}
		if err := m.needChanged(ctx, tx, n, NeedCancelled, n.Fulfilled, "cancelled"); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "need.cancel",
			TargetType: "need", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = loadNeed(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

func (m *Module) needChanged(ctx context.Context, tx pgx.Tx, n Need, status string, fulfilled int, change string) error {
	_, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "need.status_changed",
		Aggregate: outbox.Aggregate{Type: "need", ID: n.ID, Version: n.Version + 1},
		Payload: map[string]any{"need_id": n.ID, "incident_id": n.IncidentID, "category": n.Category, "change": change,
			"from": n.Status, "status": status, "quantity": n.Quantity, "fulfilled": fulfilled}})
	return err
}

// ---------------------------------------------------------------------------
// Shelters
// ---------------------------------------------------------------------------

type Shelter struct {
	ID             uuid.UUID  `json:"id"` // the shelter resource
	OrganizationID uuid.UUID  `json:"organization_id"`
	Name           string     `json:"name"`
	ResourceStatus string     `json:"resource_status"`
	Location       *gis.Point `json:"location"`
	Capacity       *int       `json:"capacity"`
	Occupancy      int        `json:"occupancy"`
	Available      *int       `json:"available"`
	Accepting      bool       `json:"accepting"`
	// Open = accepting, in service and not full: the only state in which people should be sent there.
	Open      bool       `json:"open"`
	Full      bool       `json:"full"`
	UpdatedAt *time.Time `json:"updated_at"`
	Version   int        `json:"version"`
}

const shelterCols = `r.id, r.organization_id, r.name, r.status, ST_Y(r.location::geometry), ST_X(r.location::geometry), r.capacity,
	COALESCE(s.occupancy, 0), COALESCE(s.accepting, true), s.updated_at, COALESCE(s.version, 0)`

const shelterFrom = ` FROM resources r LEFT JOIN shelter_occupancy s ON s.resource_id = r.id`

func scanShelter(row pgx.Row) (Shelter, error) {
	var s Shelter
	var lat, lng *float64
	err := row.Scan(&s.ID, &s.OrganizationID, &s.Name, &s.ResourceStatus, &lat, &lng, &s.Capacity, &s.Occupancy,
		&s.Accepting, &s.UpdatedAt, &s.Version)
	if lat != nil && lng != nil {
		s.Location = &gis.Point{Lat: *lat, Lng: *lng}
	}
	if s.Capacity != nil {
		avail := max(*s.Capacity-s.Occupancy, 0)
		s.Available = &avail
		s.Full = avail == 0
	}
	s.Open = s.Accepting && s.ResourceStatus != "out_of_service" && s.Capacity != nil && !s.Full
	return s, err
}

func (m *Module) loadShelter(ctx context.Context, q db.DBTX, id uuid.UUID, perm auth.Permission, forUpdate bool) (Shelter, error) {
	sql := `SELECT ` + shelterCols + shelterFrom + ` WHERE r.id=$1 AND r.type='shelter'`
	if forUpdate {
		sql += ` FOR UPDATE OF r` // serialises all changes to one shelter, including its first occupancy row
	}
	s, err := scanShelter(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return s, httpx.ErrNotFound
	}
	if err != nil {
		return s, err
	}
	p := auth.FromContext(ctx)
	if !p.CanInOrg(perm, s.OrganizationID) {
		if p.CanInOrg(auth.ShelterRead, s.OrganizationID) {
			return s, m.Guard.Deny(ctx, string(perm), "shelter", id.String(), "missing_permission", nil)
		}
		return s, m.Guard.Deny(ctx, string(perm), "shelter", id.String(), "out_of_org_scope", httpx.ErrNotFound)
	}
	return s, nil
}

func (m *Module) listShelters(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	all, orgs := p.OrgScope(auth.ShelterRead)
	if !all && len(orgs) == 0 {
		return m.Guard.Deny(ctx, string(auth.ShelterRead), "shelter", "", "missing_permission", nil)
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+shelterCols+shelterFrom+`
		WHERE r.type='shelter' AND ($1 OR r.organization_id = ANY($2)) ORDER BY r.name LIMIT 1000`, all, orgs)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Shelter{}
	var capacity, occupancy, available, open int
	for rows.Next() {
		s, err := scanShelter(rows)
		if err != nil {
			return err
		}
		items = append(items, s)
		occupancy += s.Occupancy
		if s.Capacity != nil {
			capacity += *s.Capacity
		}
		if s.Open {
			open++
			available += *s.Available
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "summary": map[string]int{
		"shelters": len(items), "open": open, "capacity": capacity, "occupancy": occupancy, "available_in_open": available}})
	return nil
}

// PublicShelter is what citizens see: only shelters that can take people now, nearest first. Counts are
// aggregate (no personal data); the caller's position is used for the query only and never stored or logged.
type PublicShelter struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Organization string     `json:"organization"`
	Location     gis.Point  `json:"location"`
	DistanceM    float64    `json:"distance_m"`
	Available    int        `json:"available"`
	Capacity     int        `json:"capacity"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

const publicShelterRadiusM = 50_000

func (m *Module) publicShelters(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ShelterReadPublic, "shelter", ""); err != nil {
		return err
	}
	q := r.URL.Query()
	lat, e1 := strconv.ParseFloat(q.Get("lat"), 64)
	lng, e2 := strconv.ParseFloat(q.Get("lng"), 64)
	if e1 != nil || e2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return httpx.Validation(httpx.FieldDetail{Field: "lat,lng", Reason: "invalid"})
	}
	rows, err := m.Pool.Query(ctx, `SELECT r.id, r.name, o.name, ST_Y(r.location::geometry), ST_X(r.location::geometry),
		ST_Distance(r.location, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography),
		r.capacity - COALESCE(s.occupancy, 0), r.capacity, s.updated_at
		FROM resources r JOIN organizations o ON o.id = r.organization_id
		LEFT JOIN shelter_occupancy s ON s.resource_id = r.id
		WHERE r.type = 'shelter' AND r.status <> 'out_of_service' AND r.location IS NOT NULL AND r.capacity IS NOT NULL
		  AND COALESCE(s.accepting, true) AND r.capacity > COALESCE(s.occupancy, 0)
		  AND ST_DWithin(r.location, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography, $3)
		ORDER BY 6 LIMIT 5`, lng, lat, publicShelterRadiusM)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []PublicShelter{}
	for rows.Next() {
		var s PublicShelter
		if err := rows.Scan(&s.ID, &s.Name, &s.Organization, &s.Location.Lat, &s.Location.Lng, &s.DistanceM,
			&s.Available, &s.Capacity, &s.UpdatedAt); err != nil {
			return err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "radius_m": publicShelterRadiusM,
		"note": "جای خالی لحظه‌ای است و تضمین نمی‌شود؛ پیش از حرکت در صورت امکان با ۱۱۲ یا محل اسکان هماهنگ کنید."})
	return nil
}

type LogEntry struct {
	Kind           string    `json:"kind"`
	Admitted       int       `json:"admitted"`
	Discharged     int       `json:"discharged"`
	OccupancyAfter int       `json:"occupancy_after"`
	CapacityAfter  *int      `json:"capacity_after"`
	AcceptingAfter bool      `json:"accepting_after"`
	Note           string    `json:"note"`
	ActorName      string    `json:"actor_name"`
	CreatedAt      time.Time `json:"created_at"`
}

func (m *Module) getShelter(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	s, err := m.loadShelter(ctx, m.Pool, id, auth.ShelterRead, false)
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT l.kind, l.admitted, l.discharged, l.occupancy_after, l.capacity_after, l.accepting_after,
		l.note, COALESCE(u.display_name, ''), l.created_at
		FROM shelter_occupancy_log l LEFT JOIN users u ON u.id = l.actor_id
		WHERE l.resource_id = $1 ORDER BY l.id DESC LIMIT 100`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	log := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.Kind, &e.Admitted, &e.Discharged, &e.OccupancyAfter, &e.CapacityAfter, &e.AcceptingAfter,
			&e.Note, &e.ActorName, &e.CreatedAt); err != nil {
			return err
		}
		log = append(log, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shelter": s, "log": log})
	return nil
}

// movement records admissions and departures. Admissions are refused when the shelter is closed or would
// exceed its capacity; departures cannot take occupancy below zero.
func (m *Module) movement(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req MovementRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Shelter
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		s, err := m.loadShelter(ctx, tx, id, auth.ShelterUpdate, true)
		if err != nil {
			return err
		}
		if s.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		after := s.Occupancy + req.Admitted - req.Discharged
		if after < 0 {
			return httpx.Validation(httpx.FieldDetail{Field: "discharged", Reason: "exceeds_occupancy"})
		}
		if req.Admitted > 0 {
			if !s.Accepting || s.ResourceStatus == "out_of_service" {
				return httpx.Conflict("SHELTER_NOT_ACCEPTING", "این محل اسکان پذیرش نمی‌کند")
			}
			if s.Capacity == nil {
				return httpx.Conflict("SHELTER_CAPACITY_UNKNOWN", "ظرفیت این محل اسکان ثبت نشده است؛ ابتدا ظرفیت را تعیین کنید")
			}
			if after > *s.Capacity {
				return httpx.Conflict("SHELTER_CAPACITY_EXCEEDED",
					"ظرفیت کافی نیست؛ فقط "+itoa(*s.Capacity-s.Occupancy)+" جای خالی مانده است")
			}
		}
		if err := m.writeShelter(ctx, tx, s, after, s.Capacity, s.Accepting, "movement", req.Admitted, req.Discharged, req.Note); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "shelter.movement",
			TargetType: "shelter", TargetID: id.String(), Outcome: "success",
			Details:       map[string]any{"admitted": req.Admitted, "discharged": req.Discharged, "occupancy": after},
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = m.loadShelter(ctx, tx, id, auth.ShelterRead, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

// settings changes capacity and/or opens/closes a shelter for admissions (resource:manage, with reason).
func (m *Module) settings(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req SettingsRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Shelter
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		s, err := m.loadShelter(ctx, tx, id, auth.ResourceManage, true)
		if err != nil {
			return err
		}
		if s.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		capacity, accepting := s.Capacity, s.Accepting
		if req.Capacity != nil {
			if *req.Capacity < s.Occupancy {
				return httpx.Conflict("CAPACITY_BELOW_OCCUPANCY",
					"ظرفیت نمی‌تواند کمتر از تعداد فعلی اسکان‌یافتگان ("+itoa(s.Occupancy)+") باشد")
			}
			capacity = req.Capacity
			if _, err := tx.Exec(ctx, `UPDATE resources SET capacity=$2, updated_at=now(), version=version+1 WHERE id=$1`, id, *capacity); err != nil {
				return err
			}
		}
		if req.Accepting != nil {
			accepting = *req.Accepting
		}
		if err := m.writeShelter(ctx, tx, s, s.Occupancy, capacity, accepting, "settings", 0, 0, req.Reason); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "shelter.settings",
			TargetType: "shelter", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details:       map[string]any{"capacity_from": s.Capacity, "capacity_to": capacity, "accepting_from": s.Accepting, "accepting_to": accepting},
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = m.loadShelter(ctx, tx, id, auth.ShelterRead, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

func (m *Module) writeShelter(ctx context.Context, tx pgx.Tx, s Shelter, occupancy int, capacity *int, accepting bool,
	kind string, admitted, discharged int, note string) error {
	p := auth.FromContext(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO shelter_occupancy (resource_id, occupancy, accepting, updated_by)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (resource_id) DO UPDATE SET occupancy=EXCLUDED.occupancy, accepting=EXCLUDED.accepting,
		  updated_by=EXCLUDED.updated_by, updated_at=now(), version=shelter_occupancy.version+1`,
		s.ID, occupancy, accepting, p.UserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO shelter_occupancy_log (resource_id, kind, admitted, discharged, occupancy_after,
		capacity_after, accepting_after, note, actor_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		s.ID, kind, admitted, discharged, occupancy, capacity, accepting, note, p.UserID); err != nil {
		return err
	}
	_, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "shelter.occupancy_changed",
		Aggregate: outbox.Aggregate{Type: "shelter", ID: s.ID, Version: s.Version + 1},
		Payload: map[string]any{"shelter_id": s.ID, "kind": kind, "admitted": admitted, "discharged": discharged,
			"occupancy": occupancy, "capacity": capacity, "accepting": accepting}})
	return err
}

func itoa(n int) string {
	const digits = "۰۱۲۳۴۵۶۷۸۹"
	if n == 0 {
		return "۰"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var out []rune
	d := []rune(digits)
	for n > 0 {
		out = append([]rune{d[n%10]}, out...)
		n /= 10
	}
	if neg {
		out = append([]rune{'-'}, out...)
	}
	return string(out)
}
