// Package damage owns rapid post-earthquake building assessments: green (usable), yellow (restricted use),
// red (unsafe). Assessments are never edited; a re-inspection supersedes the previous one. Finding people
// trapped raises a report into the operators' review queue so it cannot be missed.
package damage

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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

var Tags = []string{"green", "yellow", "red"}

var BuildingUses = []string{"residential", "school", "hospital", "commercial", "government", "industrial", "religious", "other"}

var Observations = []string{
	"collapse_total", "collapse_partial", "leaning", "major_cracks", "column_damage", "foundation",
	"falling_hazard", "gas_leak", "fire", "water_leak", "adjacent_hazard",
}

type Request struct {
	IncidentID        *uuid.UUID `json:"incident_id"`
	Location          gis.Point  `json:"location"`
	AddressText       string     `json:"address_text"`
	BuildingUse       string     `json:"building_use"`
	Floors            *int       `json:"floors"`
	Tag               string     `json:"tag"`
	Observations      []string   `json:"observations"`
	PeopleTrapped     bool       `json:"people_trapped"`
	OccupantsEstimate *int       `json:"occupants_estimate"`
	Notes             string     `json:"notes"`
	Supersedes        *uuid.UUID `json:"supersedes"` // re-inspection of a building already assessed
}

func (req *Request) Validate(area gis.Area) error {
	var v httpx.Validator
	req.AddressText = strings.TrimSpace(req.AddressText)
	req.Notes = strings.TrimSpace(req.Notes)
	gis.ValidatePoint(&v, "location", req.Location, area)
	v.Check(utf8.RuneCountInString(req.AddressText) <= 300, "address_text", "too_long")
	v.Check(slices.Contains(BuildingUses, req.BuildingUse), "building_use", "not_allowed")
	v.Check(req.Floors == nil || (*req.Floors >= 1 && *req.Floors <= 200), "floors", "out_of_range")
	v.Check(slices.Contains(Tags, req.Tag), "tag", "not_allowed")
	seen := map[string]bool{}
	for _, o := range req.Observations {
		v.Check(slices.Contains(Observations, o), "observations", "unknown:"+o)
		v.Check(!seen[o], "observations", "duplicate:"+o)
		seen[o] = true
	}
	// A restricted or unsafe tag must say why.
	v.Check(req.Tag == "green" || len(req.Observations) > 0, "observations", "required_for_yellow_red")
	v.Check(req.Tag != "green" || !slices.Contains(req.Observations, "collapse_total"), "tag", "inconsistent_with_collapse")
	v.Check(req.OccupantsEstimate == nil || (*req.OccupantsEstimate >= 0 && *req.OccupantsEstimate <= 100000), "occupants_estimate", "out_of_range")
	v.Check(utf8.RuneCountInString(req.Notes) <= 1000, "notes", "too_long")
	return v.Err()
}

type Module struct {
	Pool  *pgxpool.Pool
	Guard *auth.Guard
	Area  gis.Area
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("POST /damage-assessments", m.create)
	r.Auth("GET /damage-assessments", m.list)
	r.Auth("GET /damage-assessments/{id}", m.get)
}

type Assessment struct {
	ID                uuid.UUID  `json:"id"`
	IncidentID        *uuid.UUID `json:"incident_id"`
	IncidentCode      *string    `json:"incident_code"`
	Location          gis.Point  `json:"location"`
	AddressText       string     `json:"address_text"`
	BuildingUse       string     `json:"building_use"`
	Floors            *int       `json:"floors"`
	Tag               string     `json:"tag"`
	Observations      []string   `json:"observations"`
	PeopleTrapped     bool       `json:"people_trapped"`
	OccupantsEstimate *int       `json:"occupants_estimate"`
	Notes             string     `json:"notes"`
	Supersedes        *uuid.UUID `json:"supersedes"`
	SupersededAt      *time.Time `json:"superseded_at"`
	ReportID          *uuid.UUID `json:"report_id"`
	AssessorName      string     `json:"assessor_name"`
	CreatedAt         time.Time  `json:"created_at"`
}

const cols = `d.id, d.incident_id, i.code, ST_Y(d.location::geometry), ST_X(d.location::geometry), d.address_text, d.building_use,
	d.floors, d.tag, d.observations, d.people_trapped, d.occupants_estimate, d.notes, d.supersedes, d.superseded_at, d.report_id,
	COALESCE(u.display_name, ''), d.created_at`

const from = ` FROM damage_assessments d LEFT JOIN incidents i ON i.id = d.incident_id LEFT JOIN users u ON u.id = d.assessor_id`

func scan(row pgx.Row) (Assessment, error) {
	var a Assessment
	err := row.Scan(&a.ID, &a.IncidentID, &a.IncidentCode, &a.Location.Lat, &a.Location.Lng, &a.AddressText, &a.BuildingUse,
		&a.Floors, &a.Tag, &a.Observations, &a.PeopleTrapped, &a.OccupantsEstimate, &a.Notes, &a.Supersedes, &a.SupersededAt,
		&a.ReportID, &a.AssessorName, &a.CreatedAt)
	return a, err
}

func load(ctx context.Context, q db.DBTX, id uuid.UUID) (Assessment, error) {
	a, err := scan(q.QueryRow(ctx, `SELECT `+cols+from+` WHERE d.id = $1`, id))
	if err == pgx.ErrNoRows {
		return a, httpx.ErrNotFound
	}
	return a, err
}

var trappedLabels = map[string]string{
	"residential": "مسکونی", "school": "مدرسه", "hospital": "بیمارستان", "commercial": "تجاری", "government": "اداری",
	"industrial": "صنعتی", "religious": "مذهبی", "other": "سایر",
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.DamageAssess, "damage", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	key, err := idempotency.Key(r) // field inspectors submit through an offline queue
	if err != nil {
		return err
	}
	var req Request
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	if req.Observations == nil {
		req.Observations = []string{}
	}
	if err := req.Validate(m.Area); err != nil {
		return err
	}
	const scope = "POST /damage-assessments"
	var out Assessment
	var replay *idempotency.Stored
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		replay, err = idempotency.Begin(ctx, tx, p.UserID, scope, key, idempotency.Hash(req))
		if err != nil || replay != nil {
			return err
		}
		if req.IncidentID != nil {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM incidents WHERE id=$1)`, *req.IncidentID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.Validation(httpx.FieldDetail{Field: "incident_id", Reason: "not_found"})
			}
		}
		if req.Supersedes != nil {
			var supersededAt *time.Time
			err := tx.QueryRow(ctx, `SELECT superseded_at FROM damage_assessments WHERE id=$1 FOR UPDATE`, *req.Supersedes).Scan(&supersededAt)
			if err == pgx.ErrNoRows {
				return httpx.Validation(httpx.FieldDetail{Field: "supersedes", Reason: "not_found"})
			}
			if err != nil {
				return err
			}
			if supersededAt != nil {
				return httpx.Conflict("ALREADY_REASSESSED", "این ارزیابی قبلاً با ارزیابی جدیدتری جایگزین شده است")
			}
			if _, err := tx.Exec(ctx, `UPDATE damage_assessments SET superseded_at = now() WHERE id=$1`, *req.Supersedes); err != nil {
				return err
			}
		}
		id := uuid.New()
		var reportID *uuid.UUID
		if req.PeopleTrapped {
			desc := "ارزیابی سریع ساختمان: احتمال وجود افراد محبوس. کاربری: " + trappedLabels[req.BuildingUse]
			if req.AddressText != "" {
				desc += " — " + req.AddressText
			}
			rid, err := report.RaiseFromSystem(ctx, tx, p.UserID, "damage_assessment", report.CreateRequest{
				Type: "trapped_people", Description: desc,
				Location: gis.Location{Lat: req.Location.Lat, Lng: req.Location.Lng, AccuracyM: 30, Source: "gps"}})
			if err != nil {
				return err
			}
			reportID = &rid
		}
		if _, err := tx.Exec(ctx, `INSERT INTO damage_assessments (id, incident_id, location, address_text, building_use, floors, tag,
			observations, people_trapped, occupants_estimate, notes, supersedes, report_id, assessor_id)
			VALUES ($1,$2, ST_SetSRID(ST_MakePoint($4, $3), 4326)::geography, $5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			id, req.IncidentID, req.Location.Lat, req.Location.Lng, req.AddressText, req.BuildingUse, req.Floors, req.Tag,
			req.Observations, req.PeopleTrapped, req.OccupantsEstimate, req.Notes, req.Supersedes, reportID, p.UserID); err != nil {
			return err
		}
		if req.IncidentID != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO incident_events (incident_id, event_type, actor_id, payload, correlation_id)
				VALUES ($1,'building_assessed',$2,$3,$4)`, *req.IncidentID, p.UserID, map[string]any{"assessment_id": id, "tag": req.Tag,
				"people_trapped": req.PeopleTrapped}, httpx.CorrelationID(ctx)); err != nil {
				return err
			}
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "damage.assessed",
			Aggregate: outbox.Aggregate{Type: "damage_assessment", ID: id, Version: 1},
			Payload: map[string]any{"assessment_id": id, "incident_id": req.IncidentID, "tag": req.Tag, "building_use": req.BuildingUse,
				"observations": req.Observations, "people_trapped": req.PeopleTrapped, "supersedes": req.Supersedes,
				"location": req.Location}}); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "damage.assess",
			TargetType: "damage_assessment", TargetID: id.String(), Outcome: "success",
			Details:       map[string]any{"tag": req.Tag, "supersedes": req.Supersedes, "people_trapped": req.PeopleTrapped},
			CorrelationID: httpx.CorrelationID(ctx)}); err != nil {
			return err
		}
		out, err = load(ctx, tx, id)
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

// list returns current assessments (the latest per building) by default, with counts per tag.
func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.DamageRead, "damage", ""); err != nil {
		return err
	}
	tag, err := httpx.OneOf(r, "tag", Tags...)
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
	all := r.URL.Query().Get("include_superseded") == "true"
	limit, err := httpx.PageSize(r, 500, 2000)
	if err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+cols+from+`
		WHERE ($1 OR d.superseded_at IS NULL) AND ($2 = '' OR d.tag = $2) AND ($3::uuid IS NULL OR d.incident_id = $3)
		ORDER BY array_position(ARRAY['red','yellow','green'], d.tag), d.created_at DESC LIMIT $4`, all, tag, incidentID, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Assessment{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return err
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var green, yellow, red, trapped int
	if err := m.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE tag='green'), count(*) FILTER (WHERE tag='yellow'),
		count(*) FILTER (WHERE tag='red'), count(*) FILTER (WHERE people_trapped)
		FROM damage_assessments WHERE superseded_at IS NULL AND ($1::uuid IS NULL OR incident_id = $1)`, incidentID).
		Scan(&green, &yellow, &red, &trapped); err != nil {
		return err
	}
	counts := map[string]int{"green": green, "yellow": yellow, "red": red}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "counts": counts, "people_trapped": trapped})
	return nil
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.DamageRead, "damage", ""); err != nil {
		return err
	}
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	a, err := load(ctx, m.Pool, id)
	if err != nil {
		return err
	}
	// The building's full chain: earlier inspections this one replaced, and any later one that replaced it.
	rows, err := m.Pool.Query(ctx, `WITH RECURSIVE back AS (
			SELECT id, supersedes FROM damage_assessments WHERE id = $1
			UNION ALL SELECT d.id, d.supersedes FROM damage_assessments d JOIN back b ON d.id = b.supersedes
		), fwd AS (
			SELECT id FROM damage_assessments WHERE id = $1
			UNION ALL SELECT d.id FROM damage_assessments d JOIN fwd f ON d.supersedes = f.id
		)
		SELECT `+cols+from+` WHERE d.id IN (SELECT id FROM back UNION SELECT id FROM fwd) ORDER BY d.created_at`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	history := []Assessment{}
	for rows.Next() {
		h, err := scan(rows)
		if err != nil {
			return err
		}
		history = append(history, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"assessment": a, "history": history})
	return nil
}
