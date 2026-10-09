package report

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/gis"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

var tehran = gis.Area{MinLat: 34.8, MaxLat: 36.3, MinLng: 50.3, MaxLng: 53.2}

func valid() CreateRequest {
	return CreateRequest{Type: "fire", Description: "آتش‌سوزی", Location: gis.Location{Lat: 35.7, Lng: 51.4, AccuracyM: 20}}
}

func reasons(err error) []string {
	he, ok := err.(*httpx.Error)
	if !ok {
		return nil
	}
	var out []string
	for _, d := range he.Details {
		out = append(out, d.Field+":"+d.Reason)
	}
	return out
}

func TestCreateValidation(t *testing.T) {
	now := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	old := now.Add(-8 * 24 * time.Hour)
	dup := uuid.New()
	cases := map[string]struct {
		mut  func(*CreateRequest)
		want string
	}{
		"ok":               {func(*CreateRequest) {}, ""},
		"unknown type":     {func(r *CreateRequest) { r.Type = "alien" }, "type:not_allowed"},
		"outside area":     {func(r *CreateRequest) { r.Location.Lat, r.Location.Lng = 48.85, 2.35 }, "location:outside_service_area"},
		"lat out of range": {func(r *CreateRequest) { r.Location.Lat = 123 }, "location.lat:out_of_range"},
		"accuracy zero":    {func(r *CreateRequest) { r.Location.AccuracyM = 0 }, "location.accuracy_m:must_be_between_0_and_5000"},
		"future":           {func(r *CreateRequest) { r.OccurredAt = &future }, "occurred_at:in_future"},
		"too old":          {func(r *CreateRequest) { r.OccurredAt = &old }, "occurred_at:too_old"},
		"too long":         {func(r *CreateRequest) { r.Description = strings.Repeat("ا", 2001) }, "description:too_long"},
		"dup media":        {func(r *CreateRequest) { r.MediaIDs = []uuid.UUID{dup, dup} }, "media_ids:duplicate_id"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := valid()
			tc.mut(&r)
			err := r.Validate(tehran, now)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", reasons(err))
				}
				return
			}
			got := reasons(err)
			found := false
			for _, g := range got {
				found = found || g == tc.want
			}
			if !found {
				t.Fatalf("want %s, got %v", tc.want, got)
			}
		})
	}
}

func TestLifecycle(t *testing.T) {
	if !CanTransition(StatusReceived, StatusAccepted) {
		t.Error("manual review must work without AI triage (AT-04)")
	}
	if CanTransition(StatusRejected, StatusAccepted) {
		t.Error("rejected is terminal")
	}
	if CanTransition(StatusReceived, StatusLinked) {
		t.Error("only accepted reports can be linked")
	}
}

func TestReviewRequiresReasonForRejection(t *testing.T) {
	r := ReviewRequest{Decision: StatusRejected, Version: 1}
	if err := r.Validate(); err == nil {
		t.Fatal("reject without reason must fail")
	}
	r.Reason = "گزارش جعلی"
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	d := ReviewRequest{Decision: StatusDuplicate, Reason: "x", Version: 1}
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate needs duplicate_of")
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{"۰۹۱۲ ۱۲۳-۴۵۶۷": "09121234567", "+98 912 123 4567": "+989121234567", "٠٢١-٨٨٨٨": "0218888", "abc": ""}
	for in, want := range cases {
		if got := NormalizePhone(in); got != want {
			t.Errorf("%q: want %q got %q", in, want, got)
		}
	}
}

func TestPhoneValidation(t *testing.T) {
	now := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	base := func() PhoneRequest { return PhoneRequest{CreateRequest: valid()} }
	r := base()
	r.Normalize()
	if err := r.Validate(tehran, now); err != nil || r.Location.Source != "manual" {
		t.Fatalf("valid phone report rejected: %v (source %s)", reasons(err), r.Location.Source)
	}
	r = base()
	r.CallbackRequested = true
	r.Normalize()
	if !contains(reasons(r.Validate(tehran, now)), "caller_phone:required_for_callback") {
		t.Fatal("callback without phone must fail")
	}
	r = base()
	r.CallerPhone = "12"
	r.Location.Lat = 10
	r.Normalize()
	got := reasons(r.Validate(tehran, now))
	if !contains(got, "caller_phone:invalid") || !contains(got, "location:outside_service_area") {
		t.Fatalf("expected both phone and location errors, got %v", got)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
