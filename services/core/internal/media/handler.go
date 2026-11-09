package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// AllowedTypes maps sniffed MIME types to file extensions. The client-declared type must match the sniffed one.
var AllowedTypes = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
	"video/mp4":  "mp4",
}

const URLTTL = 5 * time.Minute

type Module struct {
	Pool           *pgxpool.Pool
	Guard          *auth.Guard
	Store          Store
	Scanner        Scanner
	MaxBytes       int64
	AllowUnscanned bool
	Limiter        *httpx.RateLimiter
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("POST /media", m.upload)
	r.Auth("GET /media/{id}/url", m.signedURL)
	if fs, ok := m.Store.(*FSStore); ok {
		r.Public("GET /media/blob", m.serveBlob(fs))
	}
}

type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type Media struct {
	ID           uuid.UUID  `json:"media_id"`
	ReportID     *uuid.UUID `json:"report_id,omitempty"`
	AssessmentID *uuid.UUID `json:"assessment_id,omitempty"`
	IncidentID   *uuid.UUID `json:"incident_id,omitempty"`
	SHA256       string     `json:"sha256"`
	ContentType  string     `json:"content_type"`
	Bytes        int64      `json:"bytes"`
	ScanStatus   string     `json:"scan_status"`
	CapturedAt   *time.Time `json:"captured_at,omitempty"`
	Location     *Point     `json:"location,omitempty"`
	AltitudeM    *float64   `json:"altitude_m,omitempty"`
	AccuracyM    *float64   `json:"accuracy_m,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

const mediaCols = `id, report_id, assessment_id, incident_id, sha256, content_type, bytes, scan_status, captured_at,
	ST_Y(location::geometry), ST_X(location::geometry), altitude_m, accuracy_m, created_at`

func scanMedia(row pgx.Row) (Media, error) {
	var md Media
	var lat, lng *float64
	err := row.Scan(&md.ID, &md.ReportID, &md.AssessmentID, &md.IncidentID, &md.SHA256, &md.ContentType, &md.Bytes, &md.ScanStatus,
		&md.CapturedAt, &lat, &lng, &md.AltitudeM, &md.AccuracyM, &md.CreatedAt)
	if lat != nil && lng != nil {
		md.Location = &Point{Lat: *lat, Lng: *lng}
	}
	return md, err
}

var clientRefRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

// capture is the optional field metadata of a photo (query parameters): where and when it was taken.
type capture struct {
	clientRef  *string
	incidentID *uuid.UUID
	capturedAt *time.Time
	lat, lng   *float64
	alt, acc   *float64
}

func parseCapture(r *http.Request, now time.Time) (capture, error) {
	q := r.URL.Query()
	var c capture
	var v httpx.Validator
	num := func(name string, lo, hi float64) *float64 {
		s := q.Get(name)
		if s == "" {
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		ok := err == nil && !math.IsNaN(f) && f >= lo && f <= hi
		v.Check(ok, name, "invalid")
		if !ok {
			return nil
		}
		return &f
	}
	if s := q.Get("client_ref"); s != "" {
		v.Check(clientRefRe.MatchString(s), "client_ref", "invalid")
		c.clientRef = &s
	}
	if s := q.Get("incident_id"); s != "" {
		id, err := uuid.Parse(s)
		v.Check(err == nil, "incident_id", "invalid")
		if err == nil {
			c.incidentID = &id
		}
	}
	if s := q.Get("captured_at"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		ok := err == nil && t.Before(now.Add(10*time.Minute)) && t.After(now.AddDate(0, 0, -30))
		v.Check(ok, "captured_at", "invalid")
		if ok {
			t = t.UTC()
			c.capturedAt = &t
		}
	}
	c.lat, c.lng = num("lat", -90, 90), num("lng", -180, 180)
	v.Check((c.lat == nil) == (c.lng == nil), "lat", "lat_and_lng_together")
	c.alt, c.acc = num("alt", -500, 9000), num("acc", 0, 100000)
	return c, v.Err()
}

// upload accepts the raw file as the request body with its Content-Type header. Field photos add where and
// when they were taken as query parameters, and a client_ref so a retried upload from the offline queue
// returns the first one instead of storing a copy.
func (m *Module) upload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ReportCreate, "media", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	declared := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if _, ok := AllowedTypes[declared]; !ok {
		return httpx.NewError(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "نوع فایل مجاز نیست")
	}
	now := time.Now().UTC()
	meta, err := parseCapture(r, now)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, m.MaxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > m.MaxBytes {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "حجم فایل بیش از حد مجاز است")
	}
	if len(data) == 0 {
		return httpx.Validation(httpx.FieldDetail{Field: "body", Reason: "empty"})
	}
	sniffed := http.DetectContentType(data)
	if sniffed != declared {
		return httpx.Validation(httpx.FieldDetail{Field: "Content-Type", Reason: "does_not_match_content"})
	}
	received := sha256.Sum256(data)
	if meta.clientRef != nil {
		if md, ok, err := m.byClientRef(ctx, p.UserID, *meta.clientRef, hex.EncodeToString(received[:])); err != nil || ok {
			if err == nil {
				httpx.JSON(w, http.StatusOK, md)
			}
			return err
		}
	}
	if m.Limiter != nil && !m.Limiter.Allow("media:"+p.UserID.String()) {
		return httpx.ErrRateLimited
	}
	if meta.incidentID != nil {
		var exists bool
		if err := m.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM incidents WHERE id=$1)`, *meta.incidentID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.Validation(httpx.FieldDetail{Field: "incident_id", Reason: "not_found"})
		}
	}
	scan, err := m.Scanner.Scan(ctx, data)
	if err != nil {
		return fmt.Errorf("scan media: %w", err)
	}
	if scan == "infected" {
		_ = m.Guard.Deny(ctx, "media.upload", "media", hex.EncodeToString(received[:]), "malware_detected", nil)
		return httpx.Validation(httpx.FieldDetail{Field: "body", Reason: "rejected_by_scanner"})
	}
	// Images are rebuilt from their pixels; what is stored is then known to be only an image.
	clean, rebuilt, err := Sanitize(data, sniffed)
	if err != nil {
		return httpx.Validation(httpx.FieldDetail{Field: "body", Reason: "unreadable_image"})
	}
	if rebuilt {
		data, scan = clean, "clean"
	}
	sum := sha256.Sum256(data)
	id := uuid.New()
	folder := "reports"
	if meta.capturedAt != nil || meta.lat != nil {
		folder = "field"
	}
	key := fmt.Sprintf("%s/%s/%s.%s", folder, now.Format("2006/01/02"), id, AllowedTypes[sniffed])
	if err := m.Store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), sniffed); err != nil {
		return fmt.Errorf("store media: %w", err)
	}
	_, err = m.Pool.Exec(ctx, `INSERT INTO report_media (id, uploaded_by, object_key, sha256, content_type, bytes, scan_status, created_at,
		client_ref, incident_id, captured_at, location, altitude_m, accuracy_m, received_sha256)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
			CASE WHEN $12::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($13, $12), 4326)::geography END, $14,$15,$16)`,
		id, p.UserID, key, hex.EncodeToString(sum[:]), sniffed, int64(len(data)), scan, now,
		meta.clientRef, meta.incidentID, meta.capturedAt, meta.lat, meta.lng, meta.alt, meta.acc, hex.EncodeToString(received[:]))
	if db.IsUniqueViolation(err, "") && meta.clientRef != nil {
		// A concurrent retry of the same upload won; answer with its record.
		if md, ok, err := m.byClientRef(ctx, p.UserID, *meta.clientRef, hex.EncodeToString(received[:])); err != nil || ok {
			if err == nil {
				httpx.JSON(w, http.StatusOK, md)
			}
			return err
		}
	}
	if err != nil {
		return err
	}
	out, err := scanMedia(m.Pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM report_media WHERE id=$1`, id))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

// byClientRef finds an earlier upload with the same client reference; different bytes under the same reference are refused.
func (m *Module) byClientRef(ctx context.Context, owner uuid.UUID, ref, receivedSHA string) (Media, bool, error) {
	var id uuid.UUID
	var prev *string
	err := m.Pool.QueryRow(ctx, `SELECT id, received_sha256 FROM report_media WHERE uploaded_by=$1 AND client_ref=$2`, owner, ref).Scan(&id, &prev)
	if err == pgx.ErrNoRows {
		return Media{}, false, nil
	}
	if err != nil {
		return Media{}, false, err
	}
	if prev == nil || *prev != receivedSHA {
		return Media{}, false, httpx.NewError(http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
			"این شناسه قبلاً برای فایل دیگری استفاده شده است")
	}
	md, err := scanMedia(m.Pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM report_media WHERE id=$1`, id))
	return md, err == nil, err
}

// AttachToReport links caller-owned, unattached media to a new report inside tx.
func AttachToReport(ctx context.Context, tx pgx.Tx, owner, reportID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE report_media SET report_id = $1
		WHERE id = ANY($2) AND uploaded_by = $3 AND report_id IS NULL AND assessment_id IS NULL AND scan_status <> 'infected'`, reportID, ids, owner)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(ids) {
		return httpx.Validation(httpx.FieldDetail{Field: "media_ids", Reason: "unknown_or_already_attached"})
	}
	return nil
}

// AttachToAssessment links caller-owned, unattached field photos to a new building assessment inside tx.
func AttachToAssessment(ctx context.Context, tx pgx.Tx, owner, assessmentID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE report_media SET assessment_id = $1
		WHERE id = ANY($2) AND uploaded_by = $3 AND report_id IS NULL AND assessment_id IS NULL AND scan_status <> 'infected'`,
		assessmentID, ids, owner)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(ids) {
		return httpx.Validation(httpx.FieldDetail{Field: "media_ids", Reason: "unknown_or_already_attached"})
	}
	return nil
}

func list(ctx context.Context, q db.DBTX, where string, id uuid.UUID) ([]Media, error) {
	rows, err := q.Query(ctx, `SELECT `+mediaCols+` FROM report_media WHERE `+where+` = $1 ORDER BY COALESCE(captured_at, created_at)`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Media{}
	for rows.Next() {
		md, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, md)
	}
	return out, rows.Err()
}

func ListForReport(ctx context.Context, q db.DBTX, reportID uuid.UUID) ([]Media, error) {
	return list(ctx, q, "report_id", reportID)
}

func ListForAssessment(ctx context.Context, q db.DBTX, assessmentID uuid.UUID) ([]Media, error) {
	return list(ctx, q, "assessment_id", assessmentID)
}

func (m *Module) signedURL(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var key, scan string
	var uploader uuid.UUID
	var assessment *uuid.UUID
	err = m.Pool.QueryRow(ctx, `SELECT object_key, scan_status, uploaded_by, assessment_id FROM report_media WHERE id=$1`, id).
		Scan(&key, &scan, &uploader, &assessment)
	if err == pgx.ErrNoRows {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	// Building-assessment photos are part of the damage record, readable by whoever may read assessments.
	if uploader != p.UserID && !p.Has(auth.MediaRead) && !(assessment != nil && p.Has(auth.DamageRead)) {
		return m.Guard.Deny(ctx, string(auth.MediaRead), "media", id.String(), "not_owner", httpx.ErrNotFound)
	}
	if scan != "clean" && !(scan == "skipped" && m.AllowUnscanned) {
		return httpx.Conflict("MEDIA_NOT_CLEARED", "رسانه هنوز از اسکن امنیتی عبور نکرده است")
	}
	u, err := m.Store.SignedURL(ctx, key, URLTTL)
	if err != nil {
		return err
	}
	if err := db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "media.url_issued",
			TargetType: "media", TargetID: id.String(), Outcome: "success", CorrelationID: httpx.CorrelationID(ctx)})
	}); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"url": u, "expires_in_seconds": int(URLTTL.Seconds())})
	return nil
}

func (m *Module) serveBlob(fs *FSStore) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		q := r.URL.Query()
		key := q.Get("k")
		if !fs.VerifySignature(key, q.Get("exp"), q.Get("sig")) {
			return httpx.ErrForbidden
		}
		var ct string
		if err := m.Pool.QueryRow(r.Context(), `SELECT content_type FROM report_media WHERE object_key=$1`, key).Scan(&ct); err != nil {
			return httpx.ErrNotFound
		}
		f, err := fs.Open(r.Context(), key)
		if err != nil {
			return httpx.ErrNotFound
		}
		defer f.Close()
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Cache-Control", "private, max-age=60")
		_, err = io.Copy(w, f)
		return err
	}
}
