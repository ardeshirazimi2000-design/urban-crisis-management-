package medical

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/idempotency"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/incident"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// CapacityStaleAfter: a hospital report older than this is flagged, never shown as current.
const CapacityStaleAfter = 6 * time.Hour

type Module struct {
	Pool  *pgxpool.Pool
	Guard *auth.Guard
	Area  gis.Area
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("GET /hospitals", m.listHospitals)
	r.Auth("GET /hospitals/suggest", m.suggest)
	r.Auth("GET /hospitals/{id}", m.getHospital)
	r.Auth("POST /hospitals/{id}/capacity", m.updateCapacity)
	r.Auth("POST /incidents/{id}/casualties", m.recordCasualty)
	r.Auth("GET /incidents/{id}/casualties", m.listCasualties)
	r.Auth("GET /casualties/{id}", m.getCasualty)
	r.Auth("POST /casualties/{id}/triage", m.retriage)
	r.Auth("POST /casualties/{id}/status", m.setStatus)
}

// ---------------------------------------------------------------------------
// Hospitals
// ---------------------------------------------------------------------------

type Hospital struct {
	ID             uuid.UUID  `json:"id"` // the hospital map feature
	Name           string     `json:"name"`
	OrganizationID *uuid.UUID `json:"organization_id"`
	Location       gis.Point  `json:"location"`
	Reported       bool       `json:"reported"` // false: the hospital has never reported capacity
	BedsTotal      *int       `json:"beds_total"`
	BedsAvailable  int        `json:"beds_available"`
	ICUAvailable   int        `json:"icu_available"`
	ERStatus       string     `json:"er_status"` // open | limited | diverting | closed | unknown
	Note           string     `json:"note"`
	UpdatedAt      *time.Time `json:"updated_at"`
	Stale          bool       `json:"stale"`
	Incoming       int        `json:"incoming"` // casualties on the way (transported, not yet admitted)
	Admitted       int        `json:"admitted"`
	Version        int        `json:"version"`
}

const hospitalCols = `f.id, f.name, f.owner_org_id, ST_Y(ST_PointOnSurface(f.geom::geometry)), ST_X(ST_PointOnSurface(f.geom::geometry)),
	c.feature_id IS NOT NULL, c.beds_total, COALESCE(c.beds_available, 0), COALESCE(c.icu_available, 0),
	COALESCE(c.er_status, 'unknown'), COALESCE(c.note, ''), c.updated_at, COALESCE(c.version, 0),
	(SELECT count(*) FROM casualties x WHERE x.hospital_id = f.id AND x.status = 'transported'),
	(SELECT count(*) FROM casualties x WHERE x.hospital_id = f.id AND x.status = 'admitted')`

const hospitalFrom = ` FROM gis_features f LEFT JOIN hospital_capacity c ON c.feature_id = f.id`

func scanHospital(row pgx.Row) (Hospital, error) {
	var h Hospital
	err := row.Scan(&h.ID, &h.Name, &h.OrganizationID, &h.Location.Lat, &h.Location.Lng, &h.Reported, &h.BedsTotal,
		&h.BedsAvailable, &h.ICUAvailable, &h.ERStatus, &h.Note, &h.UpdatedAt, &h.Version, &h.Incoming, &h.Admitted)
	h.Stale = h.UpdatedAt == nil || time.Since(*h.UpdatedAt) > CapacityStaleAfter
	return h, err
}

func loadHospital(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Hospital, error) {
	sql := `SELECT ` + hospitalCols + hospitalFrom + ` WHERE f.id = $1 AND f.layer = 'hospital'`
	if forUpdate {
		sql += ` FOR UPDATE OF f` // serialises capacity changes, including a hospital's first report
	}
	h, err := scanHospital(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return h, httpx.ErrNotFound
	}
	return h, err
}

func canUpdateHospital(p *auth.Principal, h Hospital) bool {
	return h.OrganizationID != nil && p.CanInOrg(auth.HospitalUpdate, *h.OrganizationID)
}

func (m *Module) listHospitals(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.HospitalRead, "hospital", ""); err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+hospitalCols+hospitalFrom+` WHERE f.layer = 'hospital' ORDER BY f.name LIMIT 1000`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Hospital{}
	sum := map[string]int{}
	for rows.Next() {
		h, err := scanHospital(rows)
		if err != nil {
			return err
		}
		items = append(items, h)
		sum["hospitals"]++
		if h.ERStatus == "open" || h.ERStatus == "limited" {
			sum["receiving"]++
			sum["beds_available"] += h.BedsAvailable
			sum["icu_available"] += h.ICUAvailable
		}
		if !h.Reported || h.Stale {
			sum["not_current"]++
		}
		sum["incoming"] += h.Incoming
		sum["admitted"] += h.Admitted
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "summary": sum, "stale_after_seconds": int(CapacityStaleAfter.Seconds())})
	return nil
}

type CapacityLog struct {
	BedsTotal     *int      `json:"beds_total"`
	BedsAvailable int       `json:"beds_available"`
	ICUAvailable  int       `json:"icu_available"`
	ERStatus      string    `json:"er_status"`
	Note          string    `json:"note"`
	ActorName     string    `json:"actor_name"`
	CreatedAt     time.Time `json:"created_at"`
}

func (m *Module) getHospital(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.HospitalRead, "hospital", ""); err != nil {
		return err
	}
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	h, err := loadHospital(ctx, m.Pool, id, false)
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT l.beds_total, l.beds_available, l.icu_available, l.er_status, l.note,
		COALESCE(u.display_name, ''), l.created_at FROM hospital_capacity_log l LEFT JOIN users u ON u.id = l.actor_id
		WHERE l.feature_id = $1 ORDER BY l.id DESC LIMIT 100`, id)
	if err != nil {
		return err
	}
	logs := []CapacityLog{}
	for rows.Next() {
		var l CapacityLog
		if err := rows.Scan(&l.BedsTotal, &l.BedsAvailable, &l.ICUAvailable, &l.ERStatus, &l.Note, &l.ActorName, &l.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		logs = append(logs, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Patients are listed only for the hospital's own staff and roles allowed to read casualties.
	p := auth.FromContext(ctx)
	patients := []Casualty{}
	if canUpdateHospital(p, h) || p.Has(auth.CasualtyRead) {
		all, orgs := p.OrgScope(auth.CasualtyRead)
		if canUpdateHospital(p, h) {
			all = true
		}
		crows, err := m.Pool.Query(ctx, `SELECT `+casualtyCols+casualtyFrom+`
			WHERE x.hospital_id = $1 AND x.status IN ('transported','admitted') AND ($2 OR i.owner_org_id = ANY($3))
			ORDER BY x.status DESC, x.updated_at DESC LIMIT 500`, id, all, orgs)
		if err != nil {
			return err
		}
		for crows.Next() {
			c, err := scanCasualty(crows)
			if err != nil {
				crows.Close()
				return err
			}
			patients = append(patients, c)
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			return err
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"hospital": h, "log": logs, "patients": patients, "can_update": canUpdateHospital(p, h)})
	return nil
}

func (m *Module) updateCapacity(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req CapacityRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Hospital
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		h, err := loadHospital(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !canUpdateHospital(p, h) {
			return m.Guard.Deny(ctx, string(auth.HospitalUpdate), "hospital", id.String(), "out_of_org_scope", nil)
		}
		if h.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		total := req.BedsTotal
		if total == nil {
			total = h.BedsTotal
		}
		if total != nil && req.BedsAvailable > *total {
			return httpx.Validation(httpx.FieldDetail{Field: "beds_available", Reason: "exceeds_total"})
		}
		if err := writeCapacity(ctx, tx, h.ID, total, req.BedsAvailable, req.ICUAvailable, req.ERStatus, req.Note, h.Version, "report"); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "hospital.capacity",
			TargetType: "hospital", TargetID: id.String(), Outcome: "success",
			Details:       map[string]any{"beds_available": req.BedsAvailable, "icu_available": req.ICUAvailable, "er_status": req.ERStatus},
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = loadHospital(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

// writeCapacity upserts the hospital's current figures, appends them to the log and emits an event.
func writeCapacity(ctx context.Context, tx pgx.Tx, id uuid.UUID, total *int, beds, icu int, er, note string, version int, cause string) error {
	p := auth.FromContext(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO hospital_capacity (feature_id, beds_total, beds_available, icu_available, er_status, note, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (feature_id) DO UPDATE SET beds_total=EXCLUDED.beds_total, beds_available=EXCLUDED.beds_available,
		  icu_available=EXCLUDED.icu_available, er_status=EXCLUDED.er_status, note=EXCLUDED.note, updated_by=EXCLUDED.updated_by,
		  updated_at=now(), version=hospital_capacity.version+1`, id, total, beds, icu, er, note, p.UserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hospital_capacity_log (feature_id, beds_total, beds_available, icu_available, er_status, note, actor_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, total, beds, icu, er, note, p.UserID); err != nil {
		return err
	}
	_, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "hospital.capacity_changed",
		Aggregate: outbox.Aggregate{Type: "hospital", ID: id, Version: version + 1},
		Payload: map[string]any{"hospital_id": id, "cause": cause, "beds_total": total, "beds_available": beds,
			"icu_available": icu, "er_status": er}})
	return err
}

type Suggestion struct {
	Hospital
	DistanceM float64 `json:"distance_m"`
}

// suggest lists receiving hospitals nearest to a point; for "immediate" patients those with ICU beds first.
func (m *Module) suggest(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.HospitalRead, "hospital", ""); err != nil {
		return err
	}
	q := r.URL.Query()
	lat, e1 := strconv.ParseFloat(q.Get("lat"), 64)
	lng, e2 := strconv.ParseFloat(q.Get("lng"), 64)
	if e1 != nil || e2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return httpx.Validation(httpx.FieldDetail{Field: "lat,lng", Reason: "invalid"})
	}
	triage, err := httpx.OneOf(r, "triage", "immediate", "delayed", "minor")
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+hospitalCols+`,
		ST_Distance(f.geom, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography)`+hospitalFrom+`
		WHERE f.layer = 'hospital' AND c.er_status IN ('open','limited') AND c.beds_available > 0
		  AND ST_DWithin(f.geom, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography, 100000)
		ORDER BY ($3 = 'immediate' AND c.icu_available > 0) DESC, 16 LIMIT 5`, lng, lat, triage)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Suggestion{}
	for rows.Next() {
		var s Suggestion
		var lat2, lng2 float64
		err := rows.Scan(&s.ID, &s.Name, &s.OrganizationID, &lat2, &lng2, &s.Reported, &s.BedsTotal, &s.BedsAvailable,
			&s.ICUAvailable, &s.ERStatus, &s.Note, &s.UpdatedAt, &s.Version, &s.Incoming, &s.Admitted, &s.DistanceM)
		if err != nil {
			return err
		}
		s.Location = gis.Point{Lat: lat2, Lng: lng2}
		s.Stale = s.UpdatedAt == nil || time.Since(*s.UpdatedAt) > CapacityStaleAfter
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items,
		"note": "پیشنهاد بر اساس آخرین ظرفیت اعلام‌شده است؛ پیش از انتقال با بیمارستان هماهنگ کنید."})
	return nil
}

// ---------------------------------------------------------------------------
// Casualties
// ---------------------------------------------------------------------------

type Casualty struct {
	ID            uuid.UUID  `json:"id"`
	TagNo         string     `json:"tag_no"`
	IncidentID    uuid.UUID  `json:"incident_id"`
	IncidentCode  string     `json:"incident_code"`
	Triage        string     `json:"triage"`
	Status        string     `json:"status"`
	AgeGroup      string     `json:"age_group"`
	Sex           string     `json:"sex"`
	Location      *gis.Point `json:"location"`
	HospitalID    *uuid.UUID `json:"hospital_id"`
	HospitalName  *string    `json:"hospital_name"`
	TransportID   *uuid.UUID `json:"transport_resource_id"`
	TransportName *string    `json:"transport_resource_name"`
	Notes         string     `json:"notes"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Version       int        `json:"version"`
	ownerOrg      uuid.UUID
}

const casualtyCols = `x.id, x.tag_no, x.incident_id, i.code, i.owner_org_id, x.triage, x.status, x.age_group, x.sex,
	ST_Y(x.location::geometry), ST_X(x.location::geometry), x.hospital_id, h.name, x.transport_resource_id, rs.name,
	x.notes, x.created_at, x.updated_at, x.version`

const casualtyFrom = ` FROM casualties x JOIN incidents i ON i.id = x.incident_id
	LEFT JOIN gis_features h ON h.id = x.hospital_id LEFT JOIN resources rs ON rs.id = x.transport_resource_id`

func scanCasualty(row pgx.Row) (Casualty, error) {
	var c Casualty
	var lat, lng *float64
	err := row.Scan(&c.ID, &c.TagNo, &c.IncidentID, &c.IncidentCode, &c.ownerOrg, &c.Triage, &c.Status, &c.AgeGroup, &c.Sex,
		&lat, &lng, &c.HospitalID, &c.HospitalName, &c.TransportID, &c.TransportName, &c.Notes, &c.CreatedAt, &c.UpdatedAt, &c.Version)
	if lat != nil && lng != nil {
		c.Location = &gis.Point{Lat: *lat, Lng: *lng}
	}
	return c, err
}

func loadCasualty(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Casualty, error) {
	sql := `SELECT ` + casualtyCols + casualtyFrom + ` WHERE x.id = $1`
	if forUpdate {
		sql += ` FOR UPDATE OF x`
	}
	c, err := scanCasualty(q.QueryRow(ctx, sql, id))
	if err == pgx.ErrNoRows {
		return c, httpx.ErrNotFound
	}
	return c, err
}

// onAssignment: a responder works casualties only on incidents they hold an active mission for.
func onAssignment(ctx context.Context, q db.DBTX, user, incidentID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assignments WHERE incident_id=$1 AND assignee_id=$2
		AND status IN ('assigned','acknowledged','en_route','on_scene'))`, incidentID, user).Scan(&ok)
	return ok, err
}

func (m *Module) canRecord(ctx context.Context, q db.DBTX, owner, incidentID uuid.UUID) (bool, error) {
	p := auth.FromContext(ctx)
	if p.CanInOrg(auth.CasualtyRecord, owner) {
		return true, nil
	}
	if !p.Has(auth.CasualtyRecordAsgn) {
		return false, nil
	}
	return onAssignment(ctx, q, p.UserID, incidentID)
}

func (m *Module) canRead(ctx context.Context, q db.DBTX, owner, incidentID uuid.UUID) (bool, error) {
	if auth.FromContext(ctx).CanInOrg(auth.CasualtyRead, owner) {
		return true, nil
	}
	return m.canRecord(ctx, q, owner, incidentID)
}

func (m *Module) recordCasualty(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	incidentID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	key, err := idempotency.Key(r) // responders record from the field through an offline queue
	if err != nil {
		return err
	}
	var req CasualtyRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	req.Normalize()
	if err := req.Validate(m.Area); err != nil {
		return err
	}
	scope := "POST /incidents/{id}/casualties"
	var out Casualty
	var replay *idempotency.Stored
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		in, err := incident.Load(ctx, tx, incidentID, false)
		if err != nil {
			return err
		}
		ok, err := m.canRecord(ctx, tx, in.OwnerOrgID, incidentID)
		if err != nil {
			return err
		}
		if !ok {
			return m.Guard.Deny(ctx, string(auth.CasualtyRecord), "incident", incidentID.String(), "not_in_scope_or_assignment", nil)
		}
		replay, err = idempotency.Begin(ctx, tx, p.UserID, scope, key, idempotency.Hash(map[string]any{"i": incidentID, "r": req}))
		if err != nil || replay != nil {
			return err
		}
		if in.Status == incident.Closed {
			return httpx.Conflict("INCIDENT_CLOSED", "حادثه بسته شده است")
		}
		tag := req.TagNo
		if tag == "" {
			var n int64
			if err := tx.QueryRow(ctx, `SELECT nextval('casualty_tag_seq')`).Scan(&n); err != nil {
				return err
			}
			tag = "T-" + leftPad(strconv.FormatInt(n, 10), 5)
		}
		status := OnScene
		if req.Triage == "deceased" {
			status = Deceased
		}
		id := uuid.New()
		var lat, lng *float64
		if req.Location != nil {
			lat, lng = &req.Location.Lat, &req.Location.Lng
		}
		_, err = tx.Exec(ctx, `INSERT INTO casualties (id, tag_no, incident_id, triage, status, age_group, sex, location, notes, recorded_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7, CASE WHEN $8::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($9, $8), 4326)::geography END, $10, $11)`,
			id, tag, incidentID, req.Triage, status, req.AgeGroup, req.Sex, lat, lng, req.Notes, p.UserID)
		if db.IsUniqueViolation(err, "casualties_tag_no_key") {
			return httpx.Conflict("TAG_IN_USE", "این شماره برچسب قبلاً ثبت شده است")
		}
		if err != nil {
			return err
		}
		if err := m.casualtyEvent(ctx, tx, id, "recorded", map[string]any{"triage": req.Triage, "status": status}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO incident_events (incident_id, event_type, actor_id, payload, correlation_id)
			VALUES ($1,'casualty_recorded',$2,$3,$4)`, incidentID, p.UserID, map[string]any{"casualty_id": id, "tag_no": tag,
			"triage": req.Triage}, httpx.CorrelationID(ctx)); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "casualty.recorded",
			Aggregate: outbox.Aggregate{Type: "casualty", ID: id, Version: 1},
			Payload: map[string]any{"casualty_id": id, "incident_id": incidentID, "triage": req.Triage, "status": status,
				"age_group": req.AgeGroup}}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "casualty.record",
			TargetType: "casualty", TargetID: id.String(), Outcome: "success",
			Details: map[string]any{"incident_id": incidentID, "triage": req.Triage}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = loadCasualty(ctx, tx, id, false)
		if err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, p.UserID, scope, key, http.StatusCreated, out)
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

func leftPad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}

func (m *Module) listCasualties(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	incidentID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	in, err := incident.Load(ctx, m.Pool, incidentID, false)
	if err != nil {
		return err
	}
	ok, err := m.canRead(ctx, m.Pool, in.OwnerOrgID, incidentID)
	if err != nil {
		return err
	}
	if !ok {
		return m.Guard.Deny(ctx, string(auth.CasualtyRead), "incident", incidentID.String(), "out_of_org_scope", httpx.ErrNotFound)
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+casualtyCols+casualtyFrom+` WHERE x.incident_id = $1
		ORDER BY array_position(ARRAY['immediate','delayed','minor','deceased'], x.triage), x.created_at LIMIT 2000`, incidentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Casualty{}
	byTriage := map[string]int{}
	byStatus := map[string]int{}
	for rows.Next() {
		c, err := scanCasualty(rows)
		if err != nil {
			return err
		}
		items = append(items, c)
		byTriage[c.Triage]++
		byStatus[c.Status]++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "by_triage": byTriage, "by_status": byStatus})
	return nil
}

type CasualtyEvent struct {
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	ActorName string         `json:"actor_name"`
	CreatedAt time.Time      `json:"created_at"`
}

func (m *Module) getCasualty(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	c, err := m.loadVisible(ctx, m.Pool, id, false)
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT e.event_type, e.payload, COALESCE(u.display_name, ''), e.created_at
		FROM casualty_events e LEFT JOIN users u ON u.id = e.actor_id WHERE e.casualty_id = $1 ORDER BY e.id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	events := []CasualtyEvent{}
	for rows.Next() {
		var e CasualtyEvent
		if err := rows.Scan(&e.EventType, &e.Payload, &e.ActorName, &e.CreatedAt); err != nil {
			return err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"casualty": c, "events": events})
	return nil
}

// loadVisible loads a casualty for a caller who may read it: incident scope, an assignment on the incident,
// or staff of the hospital the patient was taken to.
func (m *Module) loadVisible(ctx context.Context, q db.DBTX, id uuid.UUID, forUpdate bool) (Casualty, error) {
	c, err := loadCasualty(ctx, q, id, forUpdate)
	if err != nil {
		return c, err
	}
	ok, err := m.canRead(ctx, q, c.ownerOrg, c.IncidentID)
	if err != nil {
		return c, err
	}
	if !ok && c.HospitalID != nil {
		ok, err = m.isHospitalStaff(ctx, q, *c.HospitalID)
		if err != nil {
			return c, err
		}
	}
	if !ok {
		return c, m.Guard.Deny(ctx, string(auth.CasualtyRead), "casualty", id.String(), "out_of_scope", httpx.ErrNotFound)
	}
	return c, nil
}

func (m *Module) isHospitalStaff(ctx context.Context, q db.DBTX, hospitalID uuid.UUID) (bool, error) {
	var org *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT owner_org_id FROM gis_features WHERE id=$1`, hospitalID).Scan(&org); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return org != nil && auth.FromContext(ctx).CanInOrg(auth.HospitalUpdate, *org), nil
}

func (m *Module) retriage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req TriageRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Casualty
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		c, err := m.loadVisible(ctx, tx, id, true)
		if err != nil {
			return err
		}
		ok, err := m.canRecord(ctx, tx, c.ownerOrg, c.IncidentID)
		if err != nil {
			return err
		}
		if !ok {
			ok, err = m.isHospitalStaff(ctx, tx, uuidOr(c.HospitalID))
			if err != nil {
				return err
			}
		}
		if !ok {
			return m.Guard.Deny(ctx, string(auth.CasualtyRecord), "casualty", id.String(), "not_in_scope", nil)
		}
		if c.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if c.Status == Released || c.Status == Deceased {
			return httpx.Conflict("CASUALTY_CLOSED", "پرونده این مصدوم بسته است")
		}
		if req.Triage == c.Triage {
			return httpx.Validation(httpx.FieldDetail{Field: "triage", Reason: "unchanged"})
		}
		status := c.Status
		if req.Triage == "deceased" {
			if err := m.leaveBed(ctx, tx, c); err != nil {
				return err
			}
			status = Deceased
		}
		if _, err := tx.Exec(ctx, `UPDATE casualties SET triage=$2, status=$3, updated_at=now(), version=version+1 WHERE id=$1`,
			id, req.Triage, status); err != nil {
			return err
		}
		if err := m.casualtyEvent(ctx, tx, id, "retriaged", map[string]any{"from": c.Triage, "to": req.Triage, "reason": req.Reason}); err != nil {
			return err
		}
		if err := m.statusEvent(ctx, tx, c, status, req.Triage); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "casualty.retriage",
			TargetType: "casualty", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"from": c.Triage, "to": req.Triage}, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = loadCasualty(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

func uuidOr(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

// setStatus moves a casualty along on_scene → transported → admitted → released / deceased.
// Transport needs a receiving hospital (not closed; a diverting one only with a stated reason).
// Admission takes a bed from the hospital's count and leaving gives it back, so the board stays coherent.
func (m *Module) setStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req StatusRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	var out Casualty
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		c, err := m.loadVisible(ctx, tx, id, true)
		if err != nil {
			return err
		}
		ok, err := m.canRecord(ctx, tx, c.ownerOrg, c.IncidentID)
		if err != nil {
			return err
		}
		// Hospital staff may admit, release or record death for patients brought to their hospital.
		if !ok && c.HospitalID != nil && req.Status != Transported {
			ok, err = m.isHospitalStaff(ctx, tx, *c.HospitalID)
			if err != nil {
				return err
			}
		}
		if !ok {
			return m.Guard.Deny(ctx, string(auth.CasualtyRecord), "casualty", id.String(), "not_in_scope", nil)
		}
		if c.Version != req.Version {
			return httpx.ErrVersionConflict
		}
		if !CanTransition(c.Status, req.Status) {
			return httpx.Conflict("INVALID_TRANSITION", "این تغییر وضعیت مجاز نیست ("+c.Status+" ← "+req.Status+")")
		}
		hospital, transport := c.HospitalID, c.TransportID
		if req.Status == Transported {
			h, err := loadHospital(ctx, tx, *req.HospitalID, true)
			if err != nil {
				if err == httpx.ErrNotFound {
					return httpx.Validation(httpx.FieldDetail{Field: "hospital_id", Reason: "not_found"})
				}
				return err
			}
			switch h.ERStatus {
			case "closed":
				return httpx.Conflict("HOSPITAL_CLOSED", "اورژانس این بیمارستان بسته است")
			case "diverting":
				if req.Reason == "" {
					return httpx.Conflict("HOSPITAL_DIVERTING", "این بیمارستان بیمار جدید نمی‌پذیرد؛ فقط با ذکر دلیل می‌توان انتقال داد")
				}
			}
			hospital = req.HospitalID
			if req.TransportResourceID != nil {
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM resources WHERE id=$1)`, *req.TransportResourceID).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return httpx.Validation(httpx.FieldDetail{Field: "transport_resource_id", Reason: "not_found"})
				}
				transport = req.TransportResourceID
			}
		}
		if req.Status == Admitted && c.HospitalID != nil {
			if err := m.adjustBeds(ctx, tx, *c.HospitalID, -1, "پذیرش مصدوم "+c.TagNo); err != nil {
				return err
			}
		}
		if c.Status == Admitted {
			if err := m.leaveBed(ctx, tx, c); err != nil {
				return err
			}
		}
		triage := c.Triage
		if req.Status == Deceased {
			triage = "deceased"
		}
		if _, err := tx.Exec(ctx, `UPDATE casualties SET status=$2, triage=$3, hospital_id=$4, transport_resource_id=$5,
			updated_at=now(), version=version+1 WHERE id=$1`, id, req.Status, triage, hospital, transport); err != nil {
			return err
		}
		payload := map[string]any{"from": c.Status, "to": req.Status, "reason": req.Reason}
		if hospital != nil {
			payload["hospital_id"] = *hospital
		}
		if err := m.casualtyEvent(ctx, tx, id, "status", payload); err != nil {
			return err
		}
		c.HospitalID = hospital
		if err := m.statusEvent(ctx, tx, c, req.Status, triage); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "casualty.status",
			TargetType: "casualty", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: payload, CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = loadCasualty(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

// leaveBed returns the bed of an admitted patient who leaves (released or deceased).
func (m *Module) leaveBed(ctx context.Context, tx pgx.Tx, c Casualty) error {
	if c.Status != Admitted || c.HospitalID == nil {
		return nil
	}
	return m.adjustBeds(ctx, tx, *c.HospitalID, +1, "خروج بیمار "+c.TagNo)
}

// adjustBeds moves the reported free-bed count by delta within [0, total]. Hospitals that never reported
// capacity are left alone: there is no count to keep coherent.
func (m *Module) adjustBeds(ctx context.Context, tx pgx.Tx, hospitalID uuid.UUID, delta int, note string) error {
	h, err := loadHospital(ctx, tx, hospitalID, true)
	if err != nil || !h.Reported {
		return err
	}
	beds := h.BedsAvailable + delta
	if beds < 0 {
		beds = 0
	}
	if h.BedsTotal != nil && beds > *h.BedsTotal {
		beds = *h.BedsTotal
	}
	if beds == h.BedsAvailable {
		return nil
	}
	return writeCapacity(ctx, tx, h.ID, h.BedsTotal, beds, h.ICUAvailable, h.ERStatus, "خودکار: "+note, h.Version, "casualty")
}

func (m *Module) casualtyEvent(ctx context.Context, tx pgx.Tx, id uuid.UUID, typ string, payload map[string]any) error {
	_, err := tx.Exec(ctx, `INSERT INTO casualty_events (casualty_id, event_type, payload, actor_id) VALUES ($1,$2,$3,$4)`,
		id, typ, payload, auth.FromContext(ctx).UserID)
	return err
}

func (m *Module) statusEvent(ctx context.Context, tx pgx.Tx, c Casualty, status, triage string) error {
	_, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "casualty.status_changed",
		Aggregate: outbox.Aggregate{Type: "casualty", ID: c.ID, Version: c.Version + 1},
		Payload: map[string]any{"casualty_id": c.ID, "incident_id": c.IncidentID, "from": c.Status, "status": status,
			"triage": triage, "hospital_id": c.HospitalID}})
	return err
}
