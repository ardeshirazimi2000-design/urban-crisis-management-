package itest

import (
	"context"
	"testing"
)

// The situation report counts what the other modules recorded; issued reports are numbered and frozen.
func TestSitrep(t *testing.T) {
	commander := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	citizen := login(t)
	type figures struct {
		Casualties struct {
			Total    int            `json:"total"`
			ByTriage map[string]int `json:"by_triage"`
		} `json:"casualties"`
		Incidents struct {
			Active int `json:"active"`
		} `json:"incidents"`
		Needs struct {
			CriticalActive int `json:"critical_active"`
		} `json:"needs"`
	}
	citizen.do("GET", "/sitrep", nil, nil, nil, 403)
	op.do("GET", "/sitrep?window_hours=0", nil, nil, nil, 422)
	var before figures
	op.do("GET", "/sitrep?window_hours=24", nil, nil, &before, 200)

	in := createIncident(t, op, "command", "open")
	op.do("POST", "/incidents/"+in.ID.String()+"/casualties", map[string]any{"triage": "immediate"}, idem(), nil, 201)
	op.do("POST", "/incidents/"+in.ID.String()+"/needs", map[string]any{"category": "water", "quantity": 100, "unit": "لیتر", "priority": "critical"}, nil, nil, 201)
	var after figures
	op.do("GET", "/sitrep", nil, nil, &after, 200)
	if after.Casualties.Total != before.Casualties.Total+1 || after.Casualties.ByTriage["immediate"] != before.Casualties.ByTriage["immediate"]+1 ||
		after.Incidents.Active != before.Incidents.Active+1 || after.Needs.CriticalActive != before.Needs.CriticalActive+1 {
		t.Fatalf("figures did not follow: before %+v after %+v", before, after)
	}

	op.do("POST", "/sitreps", map[string]any{"window_hours": 24, "summary": "x"}, nil, nil, 403) // operators read, commanders issue
	commander.do("POST", "/sitreps", map[string]any{"window_hours": 24, "summary": " "}, nil, nil, 422)
	var s1, s2 struct {
		ID      string  `json:"id"`
		Number  int     `json:"number"`
		Figures figures `json:"figures"`
	}
	commander.do("POST", "/sitreps", map[string]any{"window_hours": 24, "summary": "وضعیت در حال کنترل؛ نیاز به آب در منطقه ۶."}, nil, &s1, 201)
	commander.do("POST", "/sitreps", map[string]any{"window_hours": 12, "summary": "گزارش دوم"}, nil, &s2, 201)
	if s2.Number != s1.Number+1 || s1.Figures.Casualties.Total != after.Casualties.Total {
		t.Fatalf("numbering/figures: %+v %+v", s1, s2)
	}
	var got struct {
		Summary string `json:"summary"`
	}
	op.do("GET", "/sitreps/"+s1.ID, nil, nil, &got, 200)
	if got.Summary == "" {
		t.Fatal("issued report not readable")
	}
	if _, err := pool.Exec(context.Background(), `UPDATE sitreps SET summary='edited'`); err == nil {
		t.Fatal("issued situation reports must be immutable")
	}
}
