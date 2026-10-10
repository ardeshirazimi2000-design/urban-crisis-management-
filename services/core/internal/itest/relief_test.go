package itest

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type needResp struct {
	ID        uuid.UUID `json:"id"`
	Status    string    `json:"status"`
	Quantity  int       `json:"quantity"`
	Fulfilled int       `json:"fulfilled"`
	Remaining int       `json:"remaining"`
	Version   int       `json:"version"`
}

// Open needs: registered per incident, supplied in parts, never over-supplied or double-counted, and scoped.
func TestNeedsLifecycle(t *testing.T) {
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	rm := login(t, grant{Role: "RESOURCE_MANAGER"})
	gis := login(t, grant{Role: "GIS_ANALYST"})
	fireOp := login(t, grant{Role: "OPERATOR", OrgCode: "fire"})
	citizen := login(t)
	in := createIncident(t, op, "command", "open")
	path := "/incidents/" + in.ID.String() + "/needs"
	body := map[string]any{"category": "blanket", "quantity": 100, "unit": "عدد", "priority": "high", "description": "شب سرد"}

	citizen.do("POST", path, body, nil, nil, 403)
	gis.do("POST", path, body, nil, nil, 403)
	fireOp.do("POST", path, body, nil, nil, 403) // other organisation's incident
	op.do("POST", path, map[string]any{"category": "other", "quantity": 1, "unit": "x", "priority": "low"}, nil, nil, 422)

	var n needResp
	op.do("POST", path, body, nil, &n, 201)
	if n.Status != "open" || n.Remaining != 100 {
		t.Fatalf("new need: %+v", n)
	}
	var list struct {
		Items []needResp `json:"items"`
	}
	gis.do("GET", "/needs?status=active&incident_id="+in.ID.String(), nil, nil, &list, 200)
	if len(list.Items) != 1 {
		t.Fatalf("active needs: %+v", list.Items)
	}
	fireOp.do("GET", "/needs/"+n.ID.String(), nil, nil, nil, 404)

	fpath := "/needs/" + n.ID.String() + "/fulfillments"
	op.do("POST", fpath, map[string]any{"quantity": 10, "source": "انبار", "version": n.Version}, nil, nil, 403) // operator cannot supply
	rm.do("POST", fpath, map[string]any{"quantity": 40, "version": n.Version}, nil, nil, 422)                    // source required
	rm.do("POST", fpath, map[string]any{"quantity": 101, "source": "انبار", "version": n.Version}, nil, nil, 422)

	var after needResp
	rm.do("POST", fpath, map[string]any{"quantity": 40, "source": "انبار هلال‌احمر", "version": n.Version}, nil, &after, 200)
	if after.Status != "partially_met" || after.Fulfilled != 40 || after.Remaining != 60 {
		t.Fatalf("partial: %+v", after)
	}
	// A retry of the same request (same version) must not count twice.
	rm.do("POST", fpath, map[string]any{"quantity": 40, "source": "انبار هلال‌احمر", "version": n.Version}, nil, nil, 409)
	rm.do("POST", fpath, map[string]any{"quantity": 60, "source": "شهرداری", "version": after.Version}, nil, &after, 200)
	if after.Status != "met" || after.Remaining != 0 {
		t.Fatalf("met: %+v", after)
	}
	rm.do("POST", fpath, map[string]any{"quantity": 1, "source": "x", "version": after.Version}, nil, nil, 409) // closed

	var detail struct {
		Fulfillments []struct {
			Quantity int `json:"quantity"`
		} `json:"fulfillments"`
	}
	op.do("GET", "/needs/"+n.ID.String(), nil, nil, &detail, 200)
	if len(detail.Fulfillments) != 2 {
		t.Fatalf("history: %+v", detail.Fulfillments)
	}

	// Cancel needs a reason; supply history is append-only at the database level.
	var water needResp
	op.do("POST", path, map[string]any{"category": "water", "quantity": 500, "unit": "لیتر", "priority": "critical"}, nil, &water, 201)
	op.do("POST", "/needs/"+water.ID.String()+"/cancel", map[string]any{"reason": "", "version": water.Version}, nil, nil, 422)
	op.do("POST", "/needs/"+water.ID.String()+"/cancel", map[string]any{"reason": "تأمین از محل", "version": water.Version}, nil, &water, 200)
	if water.Status != "cancelled" || water.Remaining != 0 {
		t.Fatalf("cancelled: %+v", water)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE need_fulfillments SET quantity=1`); err == nil {
		t.Fatal("need_fulfillments must be append-only")
	}
	if countAudit(t, "need.fulfill", "success", n.ID.String()) != 2 {
		t.Fatal("fulfilments must be audited")
	}
}

type shelterResp struct {
	ID        uuid.UUID `json:"id"`
	Capacity  *int      `json:"capacity"`
	Occupancy int       `json:"occupancy"`
	Available *int      `json:"available"`
	Accepting bool      `json:"accepting"`
	Open      bool      `json:"open"`
	Full      bool      `json:"full"`
	Version   int       `json:"version"`
}

// Shelter occupancy never exceeds capacity, never goes negative, and each movement applies exactly once.
func TestShelterCapacity(t *testing.T) {
	rm := login(t, grant{Role: "RESOURCE_MANAGER"})
	op := login(t, grant{Role: "OPERATOR", OrgCode: "redcrescent"}) // shelter staff of the owning organisation
	otherOp := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	commander := login(t, grant{Role: "COMMANDER"})
	var res struct {
		ID uuid.UUID `json:"id"`
	}
	rm.do("POST", "/resources", map[string]any{"organization_id": orgID(t, "redcrescent"), "type": "shelter",
		"name": "اسکان آزمون", "capacity": 10, "location": map[string]any{"lat": 35.7, "lng": 51.4}}, nil, &res, 201)
	base := "/shelters/" + res.ID.String()

	var s shelterResp
	op.do("GET", base, nil, nil, &struct {
		Shelter *shelterResp `json:"shelter"`
	}{&s}, 200)
	if s.Version != 0 || s.Occupancy != 0 || !s.Open || *s.Available != 10 {
		t.Fatalf("fresh shelter: %+v", s)
	}
	commander.do("POST", base+"/occupancy", map[string]any{"admitted": 1, "version": 0}, nil, nil, 403) // read-only role
	otherOp.do("GET", base, nil, nil, nil, 404)                                                         // other organisation

	op.do("POST", base+"/occupancy", map[string]any{"admitted": 11, "version": 0}, nil, nil, 409)  // over capacity
	op.do("POST", base+"/occupancy", map[string]any{"discharged": 1, "version": 0}, nil, nil, 422) // below zero
	op.do("POST", base+"/occupancy", map[string]any{"admitted": 8, "note": "خانواده‌ها", "version": 0}, nil, &s, 200)
	if s.Occupancy != 8 || *s.Available != 2 || s.Version != 1 {
		t.Fatalf("after admission: %+v", s)
	}
	op.do("POST", base+"/occupancy", map[string]any{"admitted": 8, "version": 0}, nil, nil, 409) // retry: not applied twice
	op.do("POST", base+"/occupancy", map[string]any{"admitted": 3, "discharged": 1, "version": 1}, nil, &s, 200)
	if s.Occupancy != 10 || !s.Full || s.Open {
		t.Fatalf("full: %+v", s)
	}

	// Capacity and closing need resource:manage and a reason; capacity cannot drop below occupancy.
	op.do("POST", base+"/settings", map[string]any{"capacity": 20, "reason": "سالن دوم", "version": s.Version}, nil, nil, 403)
	rm.do("POST", base+"/settings", map[string]any{"capacity": 5, "reason": "کاهش", "version": s.Version}, nil, nil, 409)
	rm.do("POST", base+"/settings", map[string]any{"capacity": 20, "version": s.Version}, nil, nil, 422)
	rm.do("POST", base+"/settings", map[string]any{"capacity": 20, "reason": "سالن دوم", "version": s.Version}, nil, &s, 200)
	if *s.Capacity != 20 || !s.Open {
		t.Fatalf("capacity: %+v", s)
	}
	rm.do("POST", base+"/settings", map[string]any{"accepting": false, "reason": "قطع آب", "version": s.Version}, nil, &s, 200)
	op.do("POST", base+"/occupancy", map[string]any{"admitted": 1, "version": s.Version}, nil, nil, 409)  // closed
	op.do("POST", base+"/occupancy", map[string]any{"discharged": 4, "version": s.Version}, nil, &s, 200) // leaving is still allowed
	if s.Occupancy != 6 || s.Open {
		t.Fatalf("closed shelter: %+v", s)
	}

	var list struct {
		Items   []shelterResp  `json:"items"`
		Summary map[string]int `json:"summary"`
	}
	op.do("GET", "/shelters", nil, nil, &list, 200)
	if list.Summary["shelters"] < 1 || list.Summary["occupancy"] < 6 {
		t.Fatalf("summary: %+v", list.Summary)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM shelter_occupancy_log`); err == nil {
		t.Fatal("shelter_occupancy_log must be append-only")
	}
	op.do("GET", "/shelters/"+uuid.NewString(), nil, nil, nil, 404)
}
