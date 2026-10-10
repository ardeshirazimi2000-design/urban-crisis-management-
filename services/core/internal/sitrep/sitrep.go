// Package sitrep builds the situation report: one set of current figures across reports, incidents,
// casualties, hospitals, shelters, needs, resources, building damage and alerts. Figures are counts only
// (no personal data) and cover all organisations, so every manager reads the same picture. Issued reports
// freeze the server-computed figures with the commander's narrative and are numbered and immutable.
package sitrep

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

type Module struct {
	Pool  *pgxpool.Pool
	Guard *auth.Guard
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("GET /sitrep", m.current)
	r.Auth("POST /sitreps", m.issue)
	r.Auth("GET /sitreps", m.list)
	r.Auth("GET /sitreps/{id}", m.get)
}

// Figures is the content of a situation report. Window-based numbers ("new_*", "sent_*") count the last
// WindowHours; the rest are the state at AsOf.
type Figures struct {
	AsOf        time.Time `json:"as_of"`
	WindowHours int       `json:"window_hours"`
	Reports     struct {
		New            int            `json:"new"`
		AwaitingReview int            `json:"awaiting_review"`
		ByType         map[string]int `json:"by_type"` // new in window
	} `json:"reports"`
	Incidents struct {
		Active     int            `json:"active"`
		New        int            `json:"new"`
		BySeverity map[string]int `json:"by_severity"` // active
		ByStatus   map[string]int `json:"by_status"`   // active
	} `json:"incidents"`
	Casualties struct {
		Total       int            `json:"total"`
		ByTriage    map[string]int `json:"by_triage"`
		ByStatus    map[string]int `json:"by_status"`
		NewInWindow int            `json:"new"`
	} `json:"casualties"`
	Hospitals struct {
		Total         int `json:"total"`
		Receiving     int `json:"receiving"`
		BedsAvailable int `json:"beds_available"`
		ICUAvailable  int `json:"icu_available"`
		NotCurrent    int `json:"not_current"` // never reported or older than 6 h
	} `json:"hospitals"`
	Shelters struct {
		Total     int `json:"total"`
		Open      int `json:"open"`
		Occupancy int `json:"occupancy"`
		Capacity  int `json:"capacity"`
		Available int `json:"available_in_open"`
	} `json:"shelters"`
	Needs struct {
		Active         int            `json:"active"`
		CriticalActive int            `json:"critical_active"`
		RemainingByCat map[string]int `json:"remaining_by_category"`
		MetInWindow    int            `json:"met"`
	} `json:"needs"`
	Resources struct {
		ByStatus          map[string]int `json:"by_status"`
		ActiveAssignments int            `json:"active_assignments"`
	} `json:"resources"`
	Damage struct {
		Red           int `json:"red"`
		Yellow        int `json:"yellow"`
		Green         int `json:"green"`
		PeopleTrapped int `json:"people_trapped"`
	} `json:"damage"`
	Alerts struct {
		Active int `json:"active"`
		Sent   int `json:"sent"`
	} `json:"alerts"`
}

func countBy(ctx context.Context, q db.DBTX, sql string, args ...any) (map[string]int, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// Compute reads all figures from one consistent snapshot (a repeatable-read transaction).
func Compute(ctx context.Context, pool *pgxpool.Pool, windowHours int) (Figures, error) {
	var f Figures
	f.WindowHours = windowHours
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		w := windowHours
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&f.AsOf); err != nil {
			return err
		}
		since := `now() - make_interval(hours => $1)`
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE received_at > `+since+`),
			count(*) FILTER (WHERE status IN ('received','triage','under_review')) FROM reports`, w).
			Scan(&f.Reports.New, &f.Reports.AwaitingReview); err != nil {
			return err
		}
		if f.Reports.ByType, err = countBy(ctx, tx, `SELECT type, count(*) FROM reports WHERE received_at > `+since+` GROUP BY type`, w); err != nil {
			return err
		}
		const activeInc = `status IN ('open','active','escalated','contained')`
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE `+activeInc+`), count(*) FILTER (WHERE opened_at > `+since+`) FROM incidents`, w).
			Scan(&f.Incidents.Active, &f.Incidents.New); err != nil {
			return err
		}
		if f.Incidents.BySeverity, err = countBy(ctx, tx, `SELECT severity, count(*) FROM incidents WHERE `+activeInc+` GROUP BY severity`); err != nil {
			return err
		}
		if f.Incidents.ByStatus, err = countBy(ctx, tx, `SELECT status, count(*) FROM incidents WHERE `+activeInc+` GROUP BY status`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE created_at > `+since+`) FROM casualties`, w).
			Scan(&f.Casualties.Total, &f.Casualties.NewInWindow); err != nil {
			return err
		}
		if f.Casualties.ByTriage, err = countBy(ctx, tx, `SELECT triage, count(*) FROM casualties GROUP BY triage`); err != nil {
			return err
		}
		if f.Casualties.ByStatus, err = countBy(ctx, tx, `SELECT status, count(*) FROM casualties GROUP BY status`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE c.er_status IN ('open','limited')),
			COALESCE(sum(c.beds_available) FILTER (WHERE c.er_status IN ('open','limited')), 0),
			COALESCE(sum(c.icu_available) FILTER (WHERE c.er_status IN ('open','limited')), 0),
			count(*) FILTER (WHERE c.updated_at IS NULL OR c.updated_at < now() - interval '6 hours')
			FROM gis_features f LEFT JOIN hospital_capacity c ON c.feature_id = f.id WHERE f.layer = 'hospital'`).
			Scan(&f.Hospitals.Total, &f.Hospitals.Receiving, &f.Hospitals.BedsAvailable, &f.Hospitals.ICUAvailable, &f.Hospitals.NotCurrent); err != nil {
			return err
		}
		const open = `COALESCE(s.accepting, true) AND r.status <> 'out_of_service' AND r.capacity IS NOT NULL AND r.capacity > COALESCE(s.occupancy, 0)`
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE `+open+`),
			COALESCE(sum(COALESCE(s.occupancy, 0)), 0), COALESCE(sum(r.capacity), 0),
			COALESCE(sum(r.capacity - COALESCE(s.occupancy, 0)) FILTER (WHERE `+open+`), 0)
			FROM resources r LEFT JOIN shelter_occupancy s ON s.resource_id = r.id WHERE r.type = 'shelter'`).
			Scan(&f.Shelters.Total, &f.Shelters.Open, &f.Shelters.Occupancy, &f.Shelters.Capacity, &f.Shelters.Available); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('open','partially_met')),
			count(*) FILTER (WHERE status IN ('open','partially_met') AND priority = 'critical'),
			count(*) FILTER (WHERE status = 'met' AND closed_at > `+since+`) FROM needs`, w).
			Scan(&f.Needs.Active, &f.Needs.CriticalActive, &f.Needs.MetInWindow); err != nil {
			return err
		}
		if f.Needs.RemainingByCat, err = countBy(ctx, tx, `SELECT category, sum(quantity - fulfilled)::int FROM needs
			WHERE status IN ('open','partially_met') GROUP BY category`); err != nil {
			return err
		}
		if f.Resources.ByStatus, err = countBy(ctx, tx, `SELECT status, count(*) FROM resources WHERE type <> 'shelter' GROUP BY status`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM assignments WHERE status IN ('proposed','assigned','acknowledged','en_route','on_scene')`).
			Scan(&f.Resources.ActiveAssignments); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE tag='red'), count(*) FILTER (WHERE tag='yellow'),
			count(*) FILTER (WHERE tag='green'), count(*) FILTER (WHERE people_trapped) FROM damage_assessments WHERE superseded_at IS NULL`).
			Scan(&f.Damage.Red, &f.Damage.Yellow, &f.Damage.Green, &f.Damage.PeopleTrapped); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE mode='operational' AND status IN ('sending','sent','partially_sent') AND expires_at > now()),
			count(*) FILTER (WHERE mode='operational' AND issued_at > `+since+`) FROM alerts`, w).Scan(&f.Alerts.Active, &f.Alerts.Sent)
	})
	return f, err
}

func windowParam(r *http.Request) (int, error) {
	s := r.URL.Query().Get("window_hours")
	if s == "" {
		return 24, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 720 {
		return 0, httpx.Validation(httpx.FieldDetail{Field: "window_hours", Reason: "must_be_1_to_720"})
	}
	return n, nil
}

func (m *Module) current(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.SitrepRead, "sitrep", ""); err != nil {
		return err
	}
	n, err := windowParam(r)
	if err != nil {
		return err
	}
	f, err := Compute(ctx, m.Pool, n)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, f)
	return nil
}

type Sitrep struct {
	ID          uuid.UUID `json:"id"`
	Number      int       `json:"number"`
	WindowHours int       `json:"window_hours"`
	Figures     Figures   `json:"figures"`
	Summary     string    `json:"summary"`
	IssuedBy    string    `json:"issued_by"`
	IssuedAt    time.Time `json:"issued_at"`
}

func (m *Module) issue(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.SitrepIssue, "sitrep", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req struct {
		WindowHours int    `json:"window_hours"`
		Summary     string `json:"summary"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 16<<10); err != nil {
		return err
	}
	req.Summary = strings.TrimSpace(req.Summary)
	var v httpx.Validator
	v.Check(req.WindowHours >= 1 && req.WindowHours <= 720, "window_hours", "must_be_1_to_720")
	// An issued report always carries the commander's assessment, not just numbers.
	v.Check(req.Summary != "" && utf8.RuneCountInString(req.Summary) <= 5000, "summary", "required_max_5000")
	if err := v.Err(); err != nil {
		return err
	}
	f, err := Compute(ctx, m.Pool, req.WindowHours)
	if err != nil {
		return err
	}
	figures, err := json.Marshal(f)
	if err != nil {
		return err
	}
	id := uuid.New()
	var out Sitrep
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO sitreps (id, window_hours, figures, summary, issued_by) VALUES ($1,$2,$3,$4,$5)
			RETURNING number, issued_at`, id, req.WindowHours, figures, req.Summary, p.UserID).Scan(&out.Number, &out.IssuedAt); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "sitrep.issued",
			Aggregate: outbox.Aggregate{Type: "sitrep", ID: id, Version: 1},
			Payload:   map[string]any{"sitrep_id": id, "number": out.Number, "window_hours": req.WindowHours, "figures": f}}); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "sitrep.issue",
			TargetType: "sitrep", TargetID: id.String(), Outcome: "success", Details: map[string]any{"number": out.Number},
			CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	out.ID, out.WindowHours, out.Figures, out.Summary, out.IssuedBy = id, req.WindowHours, f, req.Summary, p.DisplayName
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

const cols = `s.id, s.number, s.window_hours, s.figures, s.summary, COALESCE(u.display_name, ''), s.issued_at`
const from = ` FROM sitreps s LEFT JOIN users u ON u.id = s.issued_by`

func scan(row pgx.Row) (Sitrep, error) {
	var s Sitrep
	var raw []byte
	if err := row.Scan(&s.ID, &s.Number, &s.WindowHours, &raw, &s.Summary, &s.IssuedBy, &s.IssuedAt); err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s.Figures)
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.SitrepRead, "sitrep", ""); err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT `+cols+from+` ORDER BY s.number DESC LIMIT 200`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Sitrep{}
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.SitrepRead, "sitrep", ""); err != nil {
		return err
	}
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	s, err := scan(m.Pool.QueryRow(ctx, `SELECT `+cols+from+` WHERE s.id = $1`, id))
	if err == pgx.ErrNoRows {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, s)
	return nil
}
