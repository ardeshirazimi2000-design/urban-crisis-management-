// Package medical owns hospital capacity and casualties (field triage → transport → admission).
// Casualties carry a triage tag number, age group and sex only — no names or national IDs (decision D-04).
package medical

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// START-style triage categories: red, yellow, green, black.
var Triage = []string{"immediate", "delayed", "minor", "deceased"}

var AgeGroups = []string{"child", "adult", "elderly", "unknown"}
var Sexes = []string{"female", "male", "unknown"}
var ERStatuses = []string{"open", "limited", "diverting", "closed"}

const (
	OnScene     = "on_scene"
	Transported = "transported"
	Admitted    = "admitted"
	Released    = "released"
	Deceased    = "deceased"
)

var transitions = map[string][]string{
	OnScene:     {Transported, Released, Deceased},
	Transported: {Admitted, Released, Deceased},
	Admitted:    {Released, Deceased},
}

func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }

var tagRe = regexp.MustCompile(`^[A-Z0-9-]{1,20}$`)

type CasualtyRequest struct {
	TagNo    string     `json:"tag_no"` // number printed on the physical triage tag; generated when empty
	Triage   string     `json:"triage"`
	AgeGroup string     `json:"age_group"`
	Sex      string     `json:"sex"`
	Location *gis.Point `json:"location"`
	Notes    string     `json:"notes"`
}

func (req *CasualtyRequest) Normalize() {
	req.TagNo = strings.ToUpper(strings.TrimSpace(req.TagNo))
	req.Notes = strings.TrimSpace(req.Notes)
	if req.AgeGroup == "" {
		req.AgeGroup = "unknown"
	}
	if req.Sex == "" {
		req.Sex = "unknown"
	}
}

func (req *CasualtyRequest) Validate(area gis.Area) error {
	var v httpx.Validator
	v.Check(req.TagNo == "" || tagRe.MatchString(req.TagNo), "tag_no", "invalid")
	v.Check(slices.Contains(Triage, req.Triage), "triage", "not_allowed")
	v.Check(slices.Contains(AgeGroups, req.AgeGroup), "age_group", "not_allowed")
	v.Check(slices.Contains(Sexes, req.Sex), "sex", "not_allowed")
	v.Check(utf8.RuneCountInString(req.Notes) <= 500, "notes", "too_long")
	if req.Location != nil {
		gis.ValidatePoint(&v, "location", *req.Location, area)
	}
	return v.Err()
}

type TriageRequest struct {
	Triage  string `json:"triage"`
	Reason  string `json:"reason"`
	Version int    `json:"version"`
}

func (req *TriageRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(slices.Contains(Triage, req.Triage), "triage", "not_allowed")
	v.Check(req.Reason != "" && utf8.RuneCountInString(req.Reason) <= 500, "reason", "required")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}

type StatusRequest struct {
	Status              string     `json:"status"`
	HospitalID          *uuid.UUID `json:"hospital_id"`
	TransportResourceID *uuid.UUID `json:"transport_resource_id"`
	Reason              string     `json:"reason"`
	Version             int        `json:"version"`
}

func (req *StatusRequest) Validate() error {
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(slices.Contains([]string{Transported, Admitted, Released, Deceased}, req.Status), "status", "not_allowed")
	v.Check(req.Status != Transported || req.HospitalID != nil, "hospital_id", "required_for_transport")
	v.Check(req.Status != Deceased || req.Reason != "", "reason", "required")
	v.Check(utf8.RuneCountInString(req.Reason) <= 500, "reason", "too_long")
	v.Check(req.Version > 0, "version", "required")
	return v.Err()
}

// CapacityRequest is a hospital's own report of its beds and emergency-department status.
type CapacityRequest struct {
	BedsTotal     *int   `json:"beds_total"`
	BedsAvailable int    `json:"beds_available"`
	ICUAvailable  int    `json:"icu_available"`
	ERStatus      string `json:"er_status"`
	Note          string `json:"note"`
	Version       int    `json:"version"` // 0 for a hospital that has never reported
}

func (req *CapacityRequest) Validate() error {
	var v httpx.Validator
	req.Note = strings.TrimSpace(req.Note)
	v.Check(req.BedsTotal == nil || (*req.BedsTotal >= 0 && *req.BedsTotal <= 20000), "beds_total", "out_of_range")
	v.Check(req.BedsAvailable >= 0 && req.BedsAvailable <= 20000, "beds_available", "out_of_range")
	v.Check(req.BedsTotal == nil || req.BedsAvailable <= *req.BedsTotal, "beds_available", "exceeds_total")
	v.Check(req.ICUAvailable >= 0 && req.ICUAvailable <= 5000, "icu_available", "out_of_range")
	v.Check(slices.Contains(ERStatuses, req.ERStatus), "er_status", "not_allowed")
	v.Check(utf8.RuneCountInString(req.Note) <= 500, "note", "too_long")
	v.Check(req.Version >= 0, "version", "required")
	return v.Err()
}
