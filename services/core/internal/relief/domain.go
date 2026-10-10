// Package relief owns open needs (what an incident still lacks: teams, vehicles, supplies, shelter places)
// and shelter occupancy. Quantities only move through append-only records under optimistic locking,
// so retries and concurrent updates can never double-count or overfill.
package relief

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

var NeedCategories = []string{
	"rescue_team", "ambulance", "fire_truck", "medical_team", "heavy_equipment",
	"shelter_space", "water", "food", "blanket", "tent", "medicine", "other",
}

var Priorities = []string{"low", "medium", "high", "critical"}

const (
	NeedOpen           = "open"
	NeedPartiallyMet   = "partially_met"
	NeedMet            = "met"
	NeedCancelled      = "cancelled"
	maxQuantity        = 1_000_000
	maxShelterMovement = 10_000
)

// ActiveNeedStatuses are needs still waiting for (more) supply.
var ActiveNeedStatuses = []string{NeedOpen, NeedPartiallyMet}

// StatusFor derives a need's status from its fulfilled quantity.
func StatusFor(fulfilled, quantity int) string {
	switch {
	case fulfilled >= quantity:
		return NeedMet
	case fulfilled > 0:
		return NeedPartiallyMet
	default:
		return NeedOpen
	}
}

type NeedRequest struct {
	Category    string     `json:"category"`
	Description string     `json:"description"`
	Quantity    int        `json:"quantity"`
	Unit        string     `json:"unit"`
	Priority    string     `json:"priority"`
	Location    *gis.Point `json:"location"`
}

func (req *NeedRequest) Validate(area gis.Area) error {
	var v httpx.Validator
	req.Description = strings.TrimSpace(req.Description)
	req.Unit = strings.TrimSpace(req.Unit)
	v.Check(slices.Contains(NeedCategories, req.Category), "category", "not_allowed")
	v.Check(utf8.RuneCountInString(req.Description) <= 1000, "description", "too_long")
	v.Check(req.Category != "other" || req.Description != "", "description", "required_for_other")
	v.Check(req.Quantity > 0 && req.Quantity <= maxQuantity, "quantity", "out_of_range")
	v.Check(req.Unit != "" && utf8.RuneCountInString(req.Unit) <= 30, "unit", "required_max_30")
	v.Check(slices.Contains(Priorities, req.Priority), "priority", "not_allowed")
	if req.Location != nil {
		gis.ValidatePoint(&v, "location", *req.Location, area)
	}
	return v.Err()
}

type FulfillRequest struct {
	Quantity   int        `json:"quantity"`
	Source     string     `json:"source"`
	ResourceID *uuid.UUID `json:"resource_id"`
	Note       string     `json:"note"`
	Version    int        `json:"version"`
}

func (req *FulfillRequest) Validate() error {
	var v httpx.Validator
	req.Source = strings.TrimSpace(req.Source)
	req.Note = strings.TrimSpace(req.Note)
	v.Check(req.Quantity > 0 && req.Quantity <= maxQuantity, "quantity", "out_of_range")
	// Who supplied it must always be on record (organisation, depot or unit).
	v.Check(req.Source != "" && utf8.RuneCountInString(req.Source) <= 200, "source", "required_max_200")
	v.Check(utf8.RuneCountInString(req.Note) <= 1000, "note", "too_long")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}

type ReasonRequest struct {
	Reason  string `json:"reason"`
	Version int    `json:"version"`
}

func (req *ReasonRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(req.Reason != "" && utf8.RuneCountInString(req.Reason) <= 1000, "reason", "required")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}

// MovementRequest records people admitted to / leaving a shelter. Version is the shelter's version
// (0 for a shelter that has never been updated), which makes each movement apply at most once.
type MovementRequest struct {
	Admitted   int    `json:"admitted"`
	Discharged int    `json:"discharged"`
	Note       string `json:"note"`
	Version    int    `json:"version"`
}

func (req *MovementRequest) Validate() error {
	var v httpx.Validator
	req.Note = strings.TrimSpace(req.Note)
	v.Check(req.Admitted >= 0 && req.Admitted <= maxShelterMovement, "admitted", "out_of_range")
	v.Check(req.Discharged >= 0 && req.Discharged <= maxShelterMovement, "discharged", "out_of_range")
	v.Check(req.Admitted+req.Discharged > 0, "admitted", "nothing_to_record")
	v.Check(utf8.RuneCountInString(req.Note) <= 500, "note", "too_long")
	v.Check(req.Version >= 0, "version", "required")
	return v.Err()
}

// SettingsRequest changes a shelter's capacity and/or whether it accepts new people. Always needs a reason.
type SettingsRequest struct {
	Capacity  *int   `json:"capacity"`
	Accepting *bool  `json:"accepting"`
	Reason    string `json:"reason"`
	Version   int    `json:"version"`
}

func (req *SettingsRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(req.Capacity != nil || req.Accepting != nil, "capacity", "nothing_to_change")
	v.Check(req.Capacity == nil || (*req.Capacity >= 0 && *req.Capacity <= 100000), "capacity", "out_of_range")
	v.Check(req.Reason != "" && utf8.RuneCountInString(req.Reason) <= 1000, "reason", "required")
	v.Check(req.Version >= 0, "version", "required")
	return v.Err()
}
