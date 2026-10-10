package itest

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type hospitalResp struct {
	ID            uuid.UUID `json:"id"`
	BedsAvailable int       `json:"beds_available"`
	ICUAvailable  int       `json:"icu_available"`
	ERStatus      string    `json:"er_status"`
	Incoming      int       `json:"incoming"`
	Admitted      int       `json:"admitted"`
	Reported      bool      `json:"reported"`
	Version       int       `json:"version"`
}

type casualtyResp struct {
	ID      uuid.UUID `json:"id"`
	TagNo   string    `json:"tag_no"`
	Triage  string    `json:"triage"`
	Status  string    `json:"status"`
	Version int       `json:"version"`
}

func testHospital(t *testing.T, name string, lat, lng float64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO gis_features (id, layer, name, geom, owner_org_id, source, verified_at)
		VALUES ($1,'hospital',$2, ST_SetSRID(ST_MakePoint($4,$3),4326)::geography, (SELECT id FROM organizations WHERE code='ems'), 'test', now())`,
		id, name, lat, lng); err != nil {
		t.Fatal(err)
	}
	return id
}

func getHospital(t *testing.T, c *client, id uuid.UUID) hospitalResp {
	t.Helper()
	var h hospitalResp
	c.do("GET", "/hospitals/"+id.String(), nil, nil, &struct {
		Hospital *hospitalResp `json:"hospital"`
	}{&h}, 200)
	return h
}

// Triage → transport → admission → release, with hospital beds kept coherent and access scoped.
func TestCasualtiesAndHospitals(t *testing.T) {
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	emsStaff := login(t, grant{Role: "OPERATOR", OrgCode: "ems"}) // hospital coordinator
	fireOp := login(t, grant{Role: "OPERATOR", OrgCode: "fire"})
	commander := login(t, grant{Role: "COMMANDER", OrgCode: "command"}, grant{Role: "RESOURCE_MANAGER"})
	responder := login(t, grant{Role: "RESPONDER"})
	h1 := testHospital(t, "بیمارستان آزمون ۱", 36.10, 52.90)
	h2 := testHospital(t, "بیمارستان آزمون ۲", 36.11, 52.91)

	// Capacity: only the hospital's organisation reports it; first report uses version 0.
	op.do("POST", "/hospitals/"+h1.String()+"/capacity", map[string]any{"beds_available": 2, "icu_available": 1, "er_status": "open", "version": 0}, nil, nil, 403)
	emsStaff.do("POST", "/hospitals/"+h1.String()+"/capacity", map[string]any{"beds_total": 1, "beds_available": 2, "icu_available": 1, "er_status": "open", "version": 0}, nil, nil, 422)
	var h hospitalResp
	emsStaff.do("POST", "/hospitals/"+h1.String()+"/capacity", map[string]any{"beds_total": 10, "beds_available": 2, "icu_available": 1, "er_status": "open", "version": 0}, nil, &h, 200)
	emsStaff.do("POST", "/hospitals/"+h1.String()+"/capacity", map[string]any{"beds_available": 2, "icu_available": 1, "er_status": "open", "version": 0}, nil, nil, 409)
	emsStaff.do("POST", "/hospitals/"+h2.String()+"/capacity", map[string]any{"beds_available": 5, "icu_available": 0, "er_status": "diverting", "version": 0}, nil, nil, 200)

	in := createIncident(t, op, "command", "open")
	path := "/incidents/" + in.ID.String() + "/casualties"
	body := map[string]any{"triage": "immediate", "age_group": "adult", "sex": "female", "notes": "شکستگی لگن"}

	// Responders record only on incidents they are assigned to.
	responder.do("POST", path, body, idem(), nil, 403)
	var res struct {
		ID uuid.UUID `json:"id"`
	}
	commander.do("POST", "/resources", map[string]any{"organization_id": orgID(t, "ems"), "type": "ambulance", "name": "آمبولانس آزمون",
		"location": map[string]any{"lat": 36.1, "lng": 52.9}}, nil, &res, 201)
	var me struct {
		UserID uuid.UUID `json:"user_id"`
	}
	responder.do("GET", "/me", nil, nil, &me, 200)
	commander.do("POST", "/incidents/"+in.ID.String()+"/assignments", map[string]any{"resource_id": res.ID, "reason": "اعزام", "assignee_id": me.UserID}, nil, nil, 201)

	key := idem()
	var c casualtyResp
	responder.do("POST", path, body, key, &c, 201)
	r := responder.do("POST", path, body, key, nil, 201) // offline-queue retry: same casualty, not a second one
	if r.Header.Get("Idempotent-Replayed") != "true" || c.Status != "on_scene" || c.TagNo == "" {
		t.Fatalf("record: %+v replay=%q", c, r.Header.Get("Idempotent-Replayed"))
	}
	op.do("POST", path, map[string]any{"triage": "minor", "tag_no": c.TagNo}, idem(), nil, 409) // tag already used
	op.do("POST", path, map[string]any{"triage": "purple"}, idem(), nil, 422)
	fireOp.do("GET", path, nil, nil, nil, 404) // other organisation
	var list struct {
		ByTriage map[string]int `json:"by_triage"`
	}
	op.do("GET", path, nil, nil, &list, 200)
	if list.ByTriage["immediate"] != 1 {
		t.Fatalf("counts: %+v", list.ByTriage)
	}

	// Suggestion: receiving hospitals with beds; diverting h2 is excluded, ICU first for immediate.
	var sug struct {
		Items []hospitalResp `json:"items"`
	}
	responder.do("GET", "/hospitals/suggest?lat=36.1&lng=52.9&triage=immediate", nil, nil, &sug, 200)
	if len(sug.Items) != 1 || sug.Items[0].ID != h1 {
		t.Fatalf("suggest: %+v", sug.Items)
	}

	st := "/casualties/" + c.ID.String() + "/status"
	responder.do("POST", st, map[string]any{"status": "transported", "version": c.Version}, nil, nil, 422) // hospital required
	responder.do("POST", st, map[string]any{"status": "transported", "hospital_id": h2, "version": c.Version}, nil, nil, 409)
	responder.do("POST", st, map[string]any{"status": "admitted", "version": c.Version}, nil, nil, 409) // must be transported first
	responder.do("POST", st, map[string]any{"status": "transported", "hospital_id": h1, "transport_resource_id": res.ID, "version": c.Version}, nil, &c, 200)
	if h = getHospital(t, emsStaff, h1); h.Incoming != 1 || h.BedsAvailable != 2 {
		t.Fatalf("incoming: %+v", h)
	}

	// The receiving hospital's staff admit the patient (not in the incident's organisation): a bed is taken.
	emsStaff.do("POST", st, map[string]any{"status": "admitted", "version": c.Version}, nil, &c, 200)
	if h = getHospital(t, emsStaff, h1); h.BedsAvailable != 1 || h.Admitted != 1 || h.Incoming != 0 {
		t.Fatalf("after admission: %+v", h)
	}
	fireOp.do("POST", st, map[string]any{"status": "released", "version": c.Version}, nil, nil, 404)
	emsStaff.do("POST", st, map[string]any{"status": "deceased", "version": c.Version}, nil, nil, 422) // reason required
	emsStaff.do("POST", st, map[string]any{"status": "released", "version": c.Version}, nil, &c, 200)
	if h = getHospital(t, emsStaff, h1); h.BedsAvailable != 2 || h.Admitted != 0 {
		t.Fatalf("after release: %+v", h)
	}
	emsStaff.do("POST", st, map[string]any{"status": "admitted", "version": c.Version}, nil, nil, 409) // closed record

	// Re-triage needs a reason; a second casualty to a diverting hospital needs one too.
	var c2 casualtyResp
	op.do("POST", path, map[string]any{"triage": "delayed"}, idem(), &c2, 201)
	op.do("POST", "/casualties/"+c2.ID.String()+"/triage", map[string]any{"triage": "immediate", "version": c2.Version}, nil, nil, 422)
	op.do("POST", "/casualties/"+c2.ID.String()+"/triage", map[string]any{"triage": "immediate", "reason": "افت فشار", "version": c2.Version}, nil, &c2, 200)
	op.do("POST", "/casualties/"+c2.ID.String()+"/status", map[string]any{"status": "transported", "hospital_id": h2, "reason": "نزدیک‌ترین مرکز با جراح", "version": c2.Version}, nil, &c2, 200)
	if c2.Triage != "immediate" || c2.Status != "transported" {
		t.Fatalf("second casualty: %+v", c2)
	}

	var detail struct {
		Events []struct {
			EventType string `json:"event_type"`
		} `json:"events"`
	}
	op.do("GET", "/casualties/"+c.ID.String(), nil, nil, &detail, 200)
	if len(detail.Events) != 4 { // recorded, transported, admitted, released
		t.Fatalf("history: %+v", detail.Events)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM casualty_events`); err == nil {
		t.Fatal("casualty_events must be append-only")
	}
}
