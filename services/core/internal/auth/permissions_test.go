package auth

import (
	"testing"

	"github.com/google/uuid"
)

func TestCommanderIsNotSecurityAdmin(t *testing.T) {
	p := &Principal{Grants: []Grant{{Role: RoleCommander}}}
	for _, perm := range []Permission{UserManage, RoleManage, AuditRead, EventReplay} {
		if p.Has(perm) {
			t.Errorf("COMMANDER must not hold %s", perm)
		}
	}
	for _, perm := range []Permission{IncidentApprove, AlertApprove, ResourceAllocate} {
		if !p.Has(perm) {
			t.Errorf("COMMANDER should hold %s", perm)
		}
	}
}

func TestSecurityAdminHasNoOperationalPowers(t *testing.T) {
	p := &Principal{Grants: []Grant{{Role: RoleSecurityAdmin}}}
	for _, perm := range []Permission{AlertApprove, AlertDispatch, IncidentApprove, ReportRead} {
		if p.Has(perm) {
			t.Errorf("SECURITY_ADMIN must not hold %s", perm)
		}
	}
}

func TestOrgScope(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	p := &Principal{Grants: []Grant{{Role: RoleOperator, ScopeOrg: &a}}}
	if !p.CanInOrg(IncidentRead, a) {
		t.Fatal("expected access to own org")
	}
	if p.CanInOrg(IncidentRead, b) {
		t.Fatal("must deny other org (deny-by-default)")
	}
	all, orgs := p.OrgScope(IncidentRead)
	if all || len(orgs) != 1 || orgs[0] != a {
		t.Fatalf("unexpected scope all=%v orgs=%v", all, orgs)
	}
	global := &Principal{Grants: []Grant{{Role: RoleOperator}}}
	if all, _ := global.OrgScope(IncidentRead); !all {
		t.Fatal("nil scope must mean all organizations")
	}
}

func TestNilPrincipalDenied(t *testing.T) {
	var p *Principal
	if p.Has(ReportCreate) || p.CanInOrg(ReportCreate, uuid.New()) {
		t.Fatal("nil principal must be denied")
	}
}
