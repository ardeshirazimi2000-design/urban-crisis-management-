// Package auth implements authentication (OIDC / local dev JWT) and authorization:
// deny-by-default RBAC with explicit permissions plus organizational scope (ADR: no "*" for COMMANDER).
package auth

import (
	"slices"

	"github.com/google/uuid"
)

type Permission string

const (
	ReportCreate       Permission = "report:create"
	ReportIntake       Permission = "report:intake" // record a report on behalf of a caller (phone intake)
	ReportReadOwn      Permission = "report:read_own"
	ReportRead         Permission = "report:read"
	ReportReadPrecise  Permission = "report:read_precise" // exact citizen coordinates
	ReportReview       Permission = "report:review"
	MediaRead          Permission = "media:read"
	IncidentCreate     Permission = "incident:create"
	IncidentRead       Permission = "incident:read"
	IncidentReadAssign Permission = "incident:read_assigned"
	IncidentTransition Permission = "incident:transition"
	IncidentApprove    Permission = "incident:approve" // high-impact transitions (activate, escalate, close)
	IncidentLinkReport Permission = "incident:link_report"
	GISRead            Permission = "gis:read"
	GISManage          Permission = "gis:manage"
	ResourceRead       Permission = "resource:read"
	ResourceManage     Permission = "resource:manage"
	ResourceAllocate   Permission = "resource:allocate"
	AssignmentUpdate   Permission = "assignment:update_assigned"
	NeedRead           Permission = "need:read"
	NeedCreate         Permission = "need:create" // register (and cancel, with reason) an open need for an incident
	NeedFulfill        Permission = "need:fulfill"
	ShelterRead        Permission = "shelter:read"
	ShelterReadPublic  Permission = "shelter:read_public" // nearest open shelters with free places (citizens)
	ShelterUpdate      Permission = "shelter:update"      // record admissions/discharges; capacity and closing need resource:manage
	AlertReadPublic    Permission = "alert:read_public"
	AlertDraft         Permission = "alert:draft"
	AlertApprove       Permission = "alert:approve"
	AlertDispatch      Permission = "alert:dispatch"
	AlertCancel        Permission = "alert:cancel"
	AlertReadDelivery  Permission = "alert:read_delivery"
	UserManage         Permission = "user:manage"
	RoleManage         Permission = "role:manage"
	AuditRead          Permission = "audit:read"
	EventReplay        Permission = "event:replay"
)

type Role string

const (
	RoleCitizen         Role = "CITIZEN"
	RoleResponder       Role = "RESPONDER"
	RoleOperator        Role = "OPERATOR"
	RoleCommander       Role = "COMMANDER"
	RoleResourceManager Role = "RESOURCE_MANAGER"
	RoleGISAnalyst      Role = "GIS_ANALYST"
	RoleSecurityAdmin   Role = "SECURITY_ADMIN"
)

// RolePermissions is the authoritative mapping. COMMANDER deliberately cannot manage users, roles or keys.
var RolePermissions = map[Role][]Permission{
	RoleCitizen:   {ReportCreate, ReportReadOwn, AlertReadPublic, ShelterReadPublic},
	RoleResponder: {ReportCreate, IncidentReadAssign, AssignmentUpdate, AlertReadPublic, GISRead, ShelterReadPublic},
	RoleOperator: {ReportIntake, ReportRead, ReportReadPrecise, ReportReview, MediaRead, IncidentCreate, IncidentRead,
		IncidentTransition, IncidentLinkReport, GISRead, ResourceRead, AlertDraft, AlertReadDelivery, AlertReadPublic,
		NeedRead, NeedCreate, ShelterRead, ShelterUpdate},
	RoleCommander: {ReportIntake, ReportRead, ReportReadPrecise, MediaRead, IncidentCreate, IncidentRead, IncidentTransition,
		IncidentApprove, IncidentLinkReport, GISRead, ResourceRead, ResourceAllocate, AlertDraft, AlertApprove,
		AlertDispatch, AlertCancel, AlertReadDelivery, AlertReadPublic, NeedRead, NeedCreate, NeedFulfill, ShelterRead},
	RoleResourceManager: {IncidentRead, GISRead, ResourceRead, ResourceManage, ResourceAllocate, AlertReadPublic,
		NeedRead, NeedFulfill, ShelterRead, ShelterUpdate},
	RoleGISAnalyst:    {ReportRead, IncidentRead, GISRead, GISManage, ResourceRead, AlertReadPublic, NeedRead, ShelterRead},
	RoleSecurityAdmin: {UserManage, RoleManage, AuditRead, EventReplay},
}

func IsKnownRole(r string) bool {
	_, ok := RolePermissions[Role(r)]
	return ok
}

// Grant is one role assignment; ScopeOrg nil means all organizations (explicit global grant).
type Grant struct {
	Role     Role       `json:"role"`
	ScopeOrg *uuid.UUID `json:"scope_org_id,omitempty"`
}

// Principal is the authenticated caller with resolved grants.
type Principal struct {
	UserID      uuid.UUID `json:"user_id"`
	Subject     string    `json:"subject"`
	DisplayName string    `json:"display_name"`
	Grants      []Grant   `json:"grants"`
}

// Has reports whether any grant (regardless of scope) carries the permission.
func (p *Principal) Has(perm Permission) bool {
	if p == nil {
		return false
	}
	for _, g := range p.Grants {
		if slices.Contains(RolePermissions[g.Role], perm) {
			return true
		}
	}
	return false
}

// CanInOrg reports whether the permission is held for the given organization.
func (p *Principal) CanInOrg(perm Permission, org uuid.UUID) bool {
	if p == nil {
		return false
	}
	for _, g := range p.Grants {
		if !slices.Contains(RolePermissions[g.Role], perm) {
			continue
		}
		if g.ScopeOrg == nil || *g.ScopeOrg == org {
			return true
		}
	}
	return false
}

// OrgScope returns (all=true) if the permission is held globally, otherwise the list of orgs it is held for.
func (p *Principal) OrgScope(perm Permission) (all bool, orgs []uuid.UUID) {
	if p == nil {
		return false, nil
	}
	for _, g := range p.Grants {
		if !slices.Contains(RolePermissions[g.Role], perm) {
			continue
		}
		if g.ScopeOrg == nil {
			return true, nil
		}
		orgs = append(orgs, *g.ScopeOrg)
	}
	return false, orgs
}

func (p *Principal) RoleNames() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Grants))
	for _, g := range p.Grants {
		if !slices.Contains(out, string(g.Role)) {
			out = append(out, string(g.Role))
		}
	}
	return out
}

func (p *Principal) Permissions() []Permission {
	var out []Permission
	for _, g := range p.Grants {
		for _, perm := range RolePermissions[g.Role] {
			if !slices.Contains(out, perm) {
				out = append(out, perm)
			}
		}
	}
	return out
}
