// Package resource owns resources (units, vehicles, shelters, equipment) and atomic assignment to incidents.
package resource

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

var Types = []string{"ambulance", "fire_truck", "rescue_team", "police_unit", "shelter", "equipment", "medical_team"}

// Assignment lifecycle: proposed -> assigned -> acknowledged -> en_route -> on_scene -> completed; any active -> cancelled.
var assignmentTransitions = map[string][]string{
	"proposed":     {"assigned", "cancelled"},
	"assigned":     {"acknowledged", "en_route", "cancelled"},
	"acknowledged": {"en_route", "cancelled"},
	"en_route":     {"on_scene", "cancelled"},
	"on_scene":     {"completed", "cancelled"},
}

var ActiveAssignmentStatuses = []string{"proposed", "assigned", "acknowledged", "en_route", "on_scene"}

func CanTransitionAssignment(from, to string) bool { return slices.Contains(assignmentTransitions[from], to) }

// ResourceStatusFor maps an assignment status to the resource's operational status.
func ResourceStatusFor(assignmentStatus string) string {
	switch assignmentStatus {
	case "en_route":
		return "en_route"
	case "on_scene":
		return "on_scene"
	case "completed", "cancelled":
		return "available"
	default:
		return "assigned"
	}
}

type CreateRequest struct {
	OrganizationID uuid.UUID      `json:"organization_id"`
	Type           string         `json:"type"`
	Name           string         `json:"name"`
	Capabilities   map[string]any `json:"capabilities"`
	Capacity       *int           `json:"capacity"`
	Location       *gis.Point     `json:"location"`
}

func (req *CreateRequest) Validate(area gis.Area) error {
	var v httpx.Validator
	req.Name = strings.TrimSpace(req.Name)
	v.Check(req.OrganizationID != uuid.Nil, "organization_id", "required")
	v.Check(slices.Contains(Types, req.Type), "type", "not_allowed")
	v.Check(req.Name != "" && utf8.RuneCountInString(req.Name) <= 120, "name", "required_max_120")
	v.Check(req.Capacity == nil || (*req.Capacity >= 0 && *req.Capacity <= 100000), "capacity", "out_of_range")
	v.Check(len(req.Capabilities) <= 50, "capabilities", "too_many")
	if req.Location != nil {
		gis.ValidatePoint(&v, "location", *req.Location, area)
	}
	return v.Err()
}

type AssignRequest struct {
	ResourceID uuid.UUID  `json:"resource_id"`
	AssigneeID *uuid.UUID `json:"assignee_id"`
	Reason     string     `json:"reason"`
	Proposed   bool       `json:"proposed"`
}

func (req *AssignRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(req.ResourceID != uuid.Nil, "resource_id", "required")
	v.Check(req.Reason != "" && utf8.RuneCountInString(req.Reason) <= 1000, "reason", "required")
	return v.Err()
}

type AssignmentStatusRequest struct {
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Version int    `json:"version"`
}

func (req *AssignmentStatusRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(slices.Contains([]string{"assigned", "acknowledged", "en_route", "on_scene", "completed", "cancelled"}, req.Status), "status", "not_allowed")
	// Cancelling a mission (and releasing the resource) always needs a reason.
	v.Check(req.Status != "cancelled" || req.Reason != "", "reason", "required")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}
