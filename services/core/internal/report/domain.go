// Package report owns citizen/field report intake, triage and review.
// It never issues public alerts; evidence is never silently overwritten (changes are appended as events).
package report

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

var Types = []string{
	"structural_damage", "building_collapse", "trapped_people", "injury", "fire", "gas_leak",
	"road_blocked", "flooding", "power_outage", "water_outage", "landslide", "hazmat", "other",
}

var Severities = []string{"low", "medium", "high", "critical"}

const (
	StatusReceived    = "received"
	StatusTriage      = "triage"
	StatusUnderReview = "under_review"
	StatusAccepted    = "accepted"
	StatusRejected    = "rejected"
	StatusDuplicate   = "duplicate"
	StatusLinked      = "linked_to_incident"
)

var Statuses = []string{StatusReceived, StatusTriage, StatusUnderReview, StatusAccepted, StatusRejected, StatusDuplicate, StatusLinked}

// transitions encodes the report lifecycle:
// received -> triage -> under_review -> (accepted | rejected | duplicate); accepted -> linked_to_incident.
// Review is allowed directly from received/triage so that an AI outage never blocks the manual path (AT-04).
var transitions = map[string][]string{
	StatusReceived:    {StatusTriage, StatusUnderReview, StatusAccepted, StatusRejected, StatusDuplicate},
	StatusTriage:      {StatusUnderReview, StatusAccepted, StatusRejected, StatusDuplicate},
	StatusUnderReview: {StatusAccepted, StatusRejected, StatusDuplicate},
	StatusAccepted:    {StatusLinked},
	StatusLinked:      {StatusLinked},
}

func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }

const (
	MaxDescriptionRunes = 2000
	MaxMedia            = 5
	MaxClockSkew        = 5 * time.Minute
	MaxReportAge        = 7 * 24 * time.Hour
)

type CreateRequest struct {
	Type        string       `json:"type"`
	Description string       `json:"description"`
	OccurredAt  *time.Time   `json:"occurred_at"`
	Location    gis.Location `json:"location"`
	MediaIDs    []uuid.UUID  `json:"media_ids"`
}

func (req *CreateRequest) Normalize() {
	req.Description = strings.TrimSpace(req.Description)
	if req.Location.Source == "" {
		req.Location.Source = "gps"
	}
}

// Validate enforces the intake contract (type allowlist, text length, coordinates within the service area,
// accuracy, plausible claimed time, bounded media).
func (req *CreateRequest) Validate(area gis.Area, now time.Time) error {
	var v httpx.Validator
	v.Check(slices.Contains(Types, req.Type), "type", "not_allowed")
	v.Check(utf8.ValidString(req.Description), "description", "invalid_utf8")
	v.Check(utf8.RuneCountInString(req.Description) <= MaxDescriptionRunes, "description", "too_long")
	gis.ValidateLocation(&v, "location", req.Location, area)
	if req.OccurredAt != nil {
		v.Check(!req.OccurredAt.After(now.Add(MaxClockSkew)), "occurred_at", "in_future")
		v.Check(req.OccurredAt.After(now.Add(-MaxReportAge)), "occurred_at", "too_old")
	}
	v.Check(len(req.MediaIDs) <= MaxMedia, "media_ids", "too_many")
	seen := map[uuid.UUID]bool{}
	for _, id := range req.MediaIDs {
		v.Check(!seen[id], "media_ids", "duplicate_id")
		seen[id] = true
	}
	return v.Err()
}

type ReviewRequest struct {
	Decision    string     `json:"decision"` // accepted | rejected | duplicate
	Reason      string     `json:"reason"`
	Severity    string     `json:"severity"`
	DuplicateOf *uuid.UUID `json:"duplicate_of"`
	Version     int        `json:"version"`
}

func (req *ReviewRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(slices.Contains([]string{StatusAccepted, StatusRejected, StatusDuplicate}, req.Decision), "decision", "not_allowed")
	// Rejection and duplicate marking always require a reason and are attributed to the actor.
	if req.Decision == StatusRejected || req.Decision == StatusDuplicate {
		v.Check(req.Reason != "", "reason", "required")
	}
	v.Check(utf8.RuneCountInString(req.Reason) <= 1000, "reason", "too_long")
	v.Check(req.Severity == "" || slices.Contains(Severities, req.Severity), "severity", "not_allowed")
	v.Check(req.Decision != StatusDuplicate || req.DuplicateOf != nil, "duplicate_of", "required")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}
