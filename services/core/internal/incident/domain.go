// Package incident owns the incident lifecycle, evidence links and the append-only incident history.
package incident

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/report"
)

const (
	Draft     = "draft"
	Open      = "open"
	Active    = "active"
	Escalated = "escalated"
	Contained = "contained"
	Resolved  = "resolved"
	Closed    = "closed"
)

var Statuses = []string{Draft, Open, Active, Escalated, Contained, Resolved, Closed}
var Severities = []string{"low", "medium", "high", "critical"}
var ResponseLevels = []string{"L0", "L1", "L2", "L3", "L4"}

// Lifecycle: draft -> open -> active -> contained -> resolved -> closed; open/active -> escalated.
// Reopen paths (contained/resolved -> active, closed -> open) exist but always need approval and a reason.
var transitions = map[string][]string{
	Draft:     {Open, Closed},
	Open:      {Active, Escalated, Closed},
	Active:    {Escalated, Contained},
	Escalated: {Active, Contained},
	Contained: {Resolved, Active},
	Resolved:  {Closed, Active},
	Closed:    {Open},
}

// IsReopen reports transitions that move an incident backwards in the lifecycle.
func IsReopen(from, to string) bool {
	return (from == Contained && to == Active) || (from == Resolved && to == Active) || (from == Closed && to == Open)
}

func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }

// RequiredPermission returns the permission needed for a transition. High-impact changes
// (activation, escalation, closure and every reopening) need incident:approve (commander).
func RequiredPermission(from, to string) auth.Permission {
	if to == Active || to == Escalated || to == Closed || IsReopen(from, to) {
		return auth.IncidentApprove
	}
	return auth.IncidentTransition
}

func AllowedTargets(from string) []string { return transitions[from] }

type CreateRequest struct {
	Type          string      `json:"type"`
	Title         string      `json:"title"`
	Severity      string      `json:"severity"`
	ResponseLevel string      `json:"response_level"`
	OwnerOrgID    uuid.UUID   `json:"owner_org_id"`
	Location      *gis.Point  `json:"location"`
	ReportIDs     []uuid.UUID `json:"report_ids"`
	Status        string      `json:"status"` // draft (default) | open
	Reason        string      `json:"reason"`
}

func (req *CreateRequest) Validate(area gis.Area) error {
	var v httpx.Validator
	req.Title = strings.TrimSpace(req.Title)
	if req.Status == "" {
		req.Status = Draft
	}
	if req.ResponseLevel == "" {
		req.ResponseLevel = "L1"
	}
	v.Check(slices.Contains(report.Types, req.Type) || req.Type == "earthquake" || req.Type == "aftershock", "type", "not_allowed")
	v.Check(req.Title != "" && utf8.RuneCountInString(req.Title) <= 200, "title", "required_max_200")
	v.Check(slices.Contains(Severities, req.Severity), "severity", "not_allowed")
	v.Check(slices.Contains(ResponseLevels, req.ResponseLevel), "response_level", "not_allowed")
	v.Check(req.OwnerOrgID != uuid.Nil, "owner_org_id", "required")
	v.Check(req.Status == Draft || req.Status == Open, "status", "must_be_draft_or_open")
	v.Check(len(req.ReportIDs) <= 100, "report_ids", "too_many")
	if req.Location != nil {
		gis.ValidatePoint(&v, "location", *req.Location, area)
	}
	return v.Err()
}

type TransitionRequest struct {
	To      string `json:"to"`
	Reason  string `json:"reason"`
	Version int    `json:"version"`
}

func (req *TransitionRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(slices.Contains(Statuses, req.To), "to", "not_allowed")
	v.Check(req.Reason != "", "reason", "required")
	v.Check(utf8.RuneCountInString(req.Reason) <= 1000, "reason", "too_long")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}

type AssessmentRequest struct {
	Severity      string `json:"severity"`
	ResponseLevel string `json:"response_level"`
	Reason        string `json:"reason"`
	Version       int    `json:"version"`
}

func (req *AssessmentRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(slices.Contains(Severities, req.Severity), "severity", "not_allowed")
	v.Check(slices.Contains(ResponseLevels, req.ResponseLevel), "response_level", "not_allowed")
	v.Check(req.Reason != "", "reason", "required")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}

type LinkRequest struct {
	ReportID     uuid.UUID `json:"report_id"`
	RelationType string    `json:"relation_type"`
	Reason       string    `json:"reason"`
}

func (req *LinkRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	if req.RelationType == "" {
		req.RelationType = "evidence"
	}
	v.Check(req.ReportID != uuid.Nil, "report_id", "required")
	v.Check(slices.Contains([]string{"evidence", "origin", "related"}, req.RelationType), "relation_type", "not_allowed")
	v.Check(req.Reason != "", "reason", "required")
	return v.Err()
}
