package incident

import (
	"testing"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
)

func TestTransitions(t *testing.T) {
	ok := [][2]string{{Draft, Open}, {Open, Active}, {Active, Escalated}, {Escalated, Contained}, {Contained, Resolved}, {Resolved, Closed}, {Closed, Open}}
	for _, tr := range ok {
		if !CanTransition(tr[0], tr[1]) {
			t.Errorf("%s->%s should be allowed", tr[0], tr[1])
		}
	}
	bad := [][2]string{{Draft, Active}, {Closed, Active}, {Open, Resolved}, {Draft, Escalated}}
	for _, tr := range bad {
		if CanTransition(tr[0], tr[1]) {
			t.Errorf("%s->%s should be rejected", tr[0], tr[1])
		}
	}
}

func TestHighImpactTransitionsNeedApproval(t *testing.T) {
	cases := map[[2]string]auth.Permission{
		{Draft, Open}:         auth.IncidentTransition,
		{Open, Active}:        auth.IncidentApprove,
		{Active, Escalated}:   auth.IncidentApprove,
		{Active, Contained}:   auth.IncidentTransition,
		{Resolved, Closed}:    auth.IncidentApprove,
		{Closed, Open}:        auth.IncidentApprove, // reopen
		{Contained, Active}:   auth.IncidentApprove, // reopen
		{Contained, Resolved}: auth.IncidentTransition,
	}
	for tr, want := range cases {
		if got := RequiredPermission(tr[0], tr[1]); got != want {
			t.Errorf("%s->%s: want %s got %s", tr[0], tr[1], want, got)
		}
	}
}

func TestTransitionNeedsReason(t *testing.T) {
	r := TransitionRequest{To: Active, Version: 1}
	if r.Validate() == nil {
		t.Fatal("reason is mandatory")
	}
}
