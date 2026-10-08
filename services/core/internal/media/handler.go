package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
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

type Media struct {
	ID          uuid.UUID  `json:"media_id"`
	ReportID    *uuid.UUID `json:"report_id,omitempty"`
	SHA256      string     `json:"sha256"`
	ContentType string     `json:"content_type"`
	Bytes       int64      `json:"bytes"`
	ScanStatus  string     `json:"scan_status"`
	CreatedAt   time.Time  `json:"created_at"`
}

// upload accepts the raw file as the request body with its Content-Type header.
func (m *Module) upload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.ReportCreate, "media", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	if m.Limiter != nil && !m.Limiter.Allow("media:"+p.UserID.String()) {
		return httpx.ErrRateLimited
	}
	declared := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if _, ok := AllowedTypes[declared]; !ok {
		return httpx.NewError(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "نوع فایل مجاز نیست")
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
	sum := sha256.Sum256(data)
	scan, err := m.Scanner.Scan(ctx, data)
	if err != nil {
		return fmt.Errorf("scan media: %w", err)
	}
	if scan == "infected" {
		_ = m.Guard.Deny(ctx, "media.upload", "media", hex.EncodeToString(sum[:]), "malware_detected", nil)
		return httpx.Validation(httpx.FieldDetail{Field: "body", Reason: "rejected_by_scanner"})
	}
	id := uuid.New()
	now := time.Now().UTC()
	key := fmt.Sprintf("reports/%s/%s.%s", now.Format("2006/01/02"), id, AllowedTypes[sniffed])
	if err := m.Store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), sniffed); err != nil {
		return fmt.Errorf("store media: %w", err)
	}
	out := Media{ID: id, SHA256: hex.EncodeToString(sum[:]), ContentType: sniffed, Bytes: int64(len(data)), ScanStatus: scan, CreatedAt: now}
	_, err = m.Pool.Exec(ctx, `INSERT INTO report_media (id, uploaded_by, object_key, sha256, content_type, bytes, scan_status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, p.UserID, key, out.SHA256, sniffed, out.Bytes, scan, now)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, out)
	return nil
}

// AttachToReport links caller-owned, unattached media to a new report inside tx.
func AttachToReport(ctx context.Context, tx pgx.Tx, owner, reportID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE report_media SET report_id = $1
		WHERE id = ANY($2) AND uploaded_by = $3 AND report_id IS NULL AND scan_status <> 'infected'`, reportID, ids, owner)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(ids) {
		return httpx.Validation(httpx.FieldDetail{Field: "media_ids", Reason: "unknown_or_already_attached"})
	}
	return nil
}

func ListForReport(ctx context.Context, q db.DBTX, reportID uuid.UUID) ([]Media, error) {
	rows, err := q.Query(ctx, `SELECT id, report_id, sha256, content_type, bytes, scan_status, created_at
		FROM report_media WHERE report_id = $1 ORDER BY created_at`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Media{}
	for rows.Next() {
		var md Media
		if err := rows.Scan(&md.ID, &md.ReportID, &md.SHA256, &md.ContentType, &md.Bytes, &md.ScanStatus, &md.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, md)
	}
	return out, rows.Err()
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
	err = m.Pool.QueryRow(ctx, `SELECT object_key, scan_status, uploaded_by FROM report_media WHERE id=$1`, id).Scan(&key, &scan, &uploader)
	if err == pgx.ErrNoRows {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	if uploader != p.UserID && !p.Has(auth.MediaRead) {
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
