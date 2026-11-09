package itest

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for x := 0; x < 64; x++ {
		img.Set(x, 40, color.RGBA{255, 255, 255, 255})
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	// Bytes after the end-of-image marker (a smuggled payload) must not survive storage.
	return append(b.Bytes(), []byte("<?php system($_GET[1]); ?>")...)
}

type photoResp struct {
	MediaID    uuid.UUID  `json:"media_id"`
	SHA256     string     `json:"sha256"`
	ScanStatus string     `json:"scan_status"`
	CapturedAt *time.Time `json:"captured_at"`
	Location   *struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"location"`
	AltitudeM *float64 `json:"altitude_m"`
	AccuracyM *float64 `json:"accuracy_m"`
}

// Field photos: geotagged, rebuilt from pixels, uploaded once per client reference, attached to the assessment
// and readable by those who may read damage records.
func TestAssessmentPhotos(t *testing.T) {
	responder := login(t, grant{Role: "RESPONDER"})
	other := login(t, grant{Role: "RESPONDER"})
	rm := login(t, grant{Role: "RESOURCE_MANAGER"})
	citizen := login(t)
	pic := testJPEG(t)
	taken := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	q := url.Values{"client_ref": {"ph-" + uuid.NewString()}, "lat": {"36.05123"}, "lng": {"52.95123"}, "alt": {"1210.5"},
		"acc": {"6"}, "captured_at": {taken.Format(time.RFC3339)}}
	jpg := map[string]string{"Content-Type": "image/jpeg"}

	bad := url.Values{"lat": {"95"}, "lng": {"52"}}
	if r := responder.raw("POST", "/media?"+bad.Encode(), pic, jpg); r.Status != 422 {
		t.Fatalf("latitude out of range: %d", r.Status)
	}
	var p photoResp
	responder.do("POST", "/media?"+q.Encode(), pic, jpg, &p, 201)
	if p.ScanStatus != "clean" || p.Location == nil || p.Location.Lat != 36.05123 || p.AltitudeM == nil || *p.AltitudeM != 1210.5 ||
		p.CapturedAt == nil || !p.CapturedAt.Equal(taken) {
		t.Fatalf("photo metadata: %+v", p)
	}
	// The offline queue retries the upload: same record, nothing stored twice.
	var again photoResp
	responder.do("POST", "/media?"+q.Encode(), pic, jpg, &again, 200)
	if again.MediaID != p.MediaID {
		t.Fatal("retried upload stored a copy")
	}
	if r := responder.raw("POST", "/media?"+q.Encode(), testJPEG(t)[:200], jpg); r.Status != 422 {
		t.Fatalf("another file under the same client_ref: %d", r.Status)
	}

	body := map[string]any{"location": map[string]any{"lat": 36.05, "lng": 52.95}, "building_use": "residential", "tag": "yellow",
		"observations": []string{"major_cracks"}, "media_ids": []uuid.UUID{p.MediaID}}
	other.do("POST", "/damage-assessments", body, idem(), nil, 422) // someone else's photo
	var a damageResp
	responder.do("POST", "/damage-assessments", body, idem(), &a, 201)
	responder.do("POST", "/damage-assessments", body, idem(), nil, 422) // already attached

	var detail struct {
		Assessment struct {
			PhotoCount int         `json:"photo_count"`
			Photos     []photoResp `json:"photos"`
		} `json:"assessment"`
	}
	rm.do("GET", "/damage-assessments/"+a.ID.String(), nil, nil, &detail, 200)
	if detail.Assessment.PhotoCount != 1 || len(detail.Assessment.Photos) != 1 || detail.Assessment.Photos[0].AccuracyM == nil {
		t.Fatalf("assessment photos: %+v", detail.Assessment)
	}
	// Damage readers see the photo; a citizen does not.
	var u struct {
		URL string `json:"url"`
	}
	rm.do("GET", "/media/"+p.MediaID.String()+"/url", nil, nil, &u, 200)
	citizen.do("GET", "/media/"+p.MediaID.String()+"/url", nil, nil, nil, 404) // existence is not revealed
	resp, err := http.Get(u.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("download: %v %v", err, resp)
	}
	var stored bytes.Buffer
	stored.ReadFrom(resp.Body)
	resp.Body.Close()
	if bytes.Contains(stored.Bytes(), []byte("<?php")) {
		t.Fatal("payload after the image survived storage")
	}
	if _, err := jpeg.Decode(bytes.NewReader(stored.Bytes())); err != nil {
		t.Fatalf("stored photo is not a JPEG: %v", err)
	}
}
