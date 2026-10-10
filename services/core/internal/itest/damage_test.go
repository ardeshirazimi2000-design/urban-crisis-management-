package itest

import (
	"testing"

	"github.com/google/uuid"
)

type damageResp struct {
	ID       uuid.UUID  `json:"id"`
	Tag      string     `json:"tag"`
	ReportID *uuid.UUID `json:"report_id"`
}

// Rapid assessment: tags need reasons, re-inspections supersede (never edit), trapped people reach the queue.
func TestDamageAssessment(t *testing.T) {
	responder := login(t, grant{Role: "RESPONDER"})
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	rm := login(t, grant{Role: "RESOURCE_MANAGER"})
	citizen := login(t)
	at := map[string]any{"lat": 36.05, "lng": 52.95}
	body := map[string]any{"location": at, "building_use": "school", "floors": 3, "tag": "red",
		"observations": []string{"collapse_partial", "column_damage"}, "people_trapped": true, "address_text": "مدرسه آزمون"}

	citizen.do("POST", "/damage-assessments", body, idem(), nil, 403)
	rm.do("POST", "/damage-assessments", body, idem(), nil, 403) // read-only role
	responder.do("POST", "/damage-assessments", map[string]any{"location": at, "building_use": "school", "tag": "yellow"}, idem(), nil, 422)
	responder.do("POST", "/damage-assessments", map[string]any{"location": at, "building_use": "school", "tag": "green",
		"observations": []string{"collapse_total"}}, idem(), nil, 422)

	key := idem()
	var red damageResp
	responder.do("POST", "/damage-assessments", body, key, &red, 201)
	responder.do("POST", "/damage-assessments", body, key, nil, 201) // offline retry: replayed, not duplicated
	if red.ReportID == nil {
		t.Fatal("trapped people must raise a report into the review queue")
	}
	var rep struct {
		Source string `json:"source"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	op.do("GET", "/reports/"+red.ReportID.String(), nil, nil, &rep, 200)
	if rep.Source != "damage_assessment" || rep.Type != "trapped_people" || rep.Status == "accepted" {
		t.Fatalf("raised report: %+v", rep)
	}

	// Re-inspection supersedes; the old assessment can be superseded only once.
	var yellow damageResp
	op.do("POST", "/damage-assessments", map[string]any{"location": at, "building_use": "school", "tag": "yellow",
		"observations": []string{"major_cracks"}, "supersedes": red.ID}, idem(), &yellow, 201)
	op.do("POST", "/damage-assessments", map[string]any{"location": at, "building_use": "school", "tag": "green",
		"supersedes": red.ID}, idem(), nil, 409)

	var list struct {
		Items []damageResp `json:"items"`
	}
	rm.do("GET", "/damage-assessments?include_superseded=false&limit=2000", nil, nil, &list, 200)
	for _, a := range list.Items {
		if a.ID == red.ID {
			t.Fatal("superseded assessment listed as current")
		}
	}
	var detail struct {
		History []damageResp `json:"history"`
	}
	rm.do("GET", "/damage-assessments/"+red.ID.String(), nil, nil, &detail, 200)
	if len(detail.History) != 2 || detail.History[0].Tag != "red" || detail.History[1].Tag != "yellow" {
		t.Fatalf("history: %+v", detail.History)
	}
	var fc struct {
		Meta struct {
			Layers map[string]struct {
				Count int `json:"count"`
			} `json:"layers"`
		} `json:"meta"`
	}
	op.do("GET", "/gis/features?layers=damage", nil, nil, &fc, 200)
	if fc.Meta.Layers["damage"].Count < 1 {
		t.Fatalf("damage layer: %+v", fc.Meta.Layers)
	}
}
