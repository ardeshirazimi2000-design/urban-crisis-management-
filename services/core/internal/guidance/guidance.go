// Package guidance owns the approved safety guidance behind the citizens' crisis assistant. Authors write
// versions; a different person approves them (four-eyes); published text is never edited, only replaced
// by a newer approved version. Citizens download the published set and match questions on their device,
// so no question ever reaches the server.
package guidance

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

var Categories = []string{"earthquake", "after_quake", "trapped", "gas", "fire", "medical", "flood", "shelter", "alerts",
	"preparedness", "family", "psychological", "utilities", "general"}

// Actions a card can offer: call an emergency number or open a part of the citizen app.
var Actions = []string{"call:110", "call:112", "call:115", "call:121", "call:122", "call:125", "call:194", "report", "shelters", "alerts"}

var slugRe = regexp.MustCompile(`^[a-z0-9-]{2,60}$`)

type Content struct {
	Title     string   `json:"title"`
	Category  string   `json:"category"`
	Keywords  []string `json:"keywords"`
	Body      string   `json:"body"`
	Actions   []string `json:"actions"`
	Emergency bool     `json:"emergency"`
}

func (c *Content) Validate() error {
	var v httpx.Validator
	c.Title, c.Body = strings.TrimSpace(c.Title), strings.TrimSpace(c.Body)
	v.Check(c.Title != "" && utf8.RuneCountInString(c.Title) <= 120, "title", "required_max_120")
	v.Check(slices.Contains(Categories, c.Category), "category", "not_allowed")
	v.Check(c.Body != "" && utf8.RuneCountInString(c.Body) <= 3000, "body", "required_max_3000")
	v.Check(len(c.Keywords) >= 1 && len(c.Keywords) <= 40, "keywords", "1_to_40")
	kw := make([]string, 0, len(c.Keywords))
	for _, k := range c.Keywords {
		k = strings.TrimSpace(k)
		v.Check(k != "" && utf8.RuneCountInString(k) <= 60, "keywords", "empty_or_too_long")
		kw = append(kw, k)
	}
	c.Keywords = kw
	if c.Actions == nil {
		c.Actions = []string{}
	}
	for _, a := range c.Actions {
		v.Check(slices.Contains(Actions, a), "actions", "not_allowed:"+a)
	}
	return v.Err()
}

type Module struct {
	Pool  *pgxpool.Pool
	Guard *auth.Guard
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("GET /guidance", m.published)
	r.Auth("GET /guidance/admin", m.admin)
	r.Auth("POST /guidance/cards", m.createCard)
	r.Auth("POST /guidance/cards/{id}/versions", m.newVersion)
	r.Auth("POST /guidance/cards/{id}/retire", m.retire)
	r.Auth("POST /guidance/versions/{id}/approve", m.approve)
	r.Auth("POST /guidance/versions/{id}/reject", m.reject)
}

type PublishedCard struct {
	Slug    string `json:"slug"`
	Version int    `json:"version"`
	Content
}

// published is what the citizen app downloads: every live card plus the knowledge-base version.
func (m *Module) published(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GuidanceReadPublic, "guidance", ""); err != nil {
		return err
	}
	rows, err := m.Pool.Query(ctx, `SELECT c.slug, v.version, v.title, v.category, v.keywords, v.body, v.actions, v.emergency
		FROM guidance_versions v JOIN guidance_cards c ON c.id = v.card_id
		WHERE v.status = 'approved' AND c.retired_at IS NULL ORDER BY v.emergency DESC, c.slug`)
	if err != nil {
		return err
	}
	defer rows.Close()
	cards := []PublishedCard{}
	for rows.Next() {
		var p PublishedCard
		if err := rows.Scan(&p.Slug, &p.Version, &p.Title, &p.Category, &p.Keywords, &p.Body, &p.Actions, &p.Emergency); err != nil {
			return err
		}
		cards = append(cards, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var kb int64
	// Publishing and withdrawing both draw from one sequence, so any change moves the version forward.
	if err := m.Pool.QueryRow(ctx, `SELECT GREATEST(COALESCE((SELECT max(published_seq) FROM guidance_versions), 0),
		COALESCE((SELECT max(retired_seq) FROM guidance_cards), 0))`).Scan(&kb); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"kb_version": kb, "cards": cards})
	return nil
}

type Version struct {
	ID           uuid.UUID  `json:"id"`
	Version      int        `json:"version"`
	Status       string     `json:"status"`
	AuthorName   string     `json:"author_name"`
	AuthorID     *uuid.UUID `json:"author_id"`
	ReviewerName string     `json:"reviewer_name"`
	ReviewReason *string    `json:"review_reason"`
	CreatedAt    time.Time  `json:"created_at"`
	ReviewedAt   *time.Time `json:"reviewed_at"`
	Content
}

type AdminCard struct {
	ID            uuid.UUID  `json:"id"`
	Slug          string     `json:"slug"`
	RetiredAt     *time.Time `json:"retired_at"`
	RetiredReason *string    `json:"retired_reason"`
	Versions      []Version  `json:"versions"` // newest first
}

func (m *Module) admin(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := auth.FromContext(ctx)
	if !p.Has(auth.GuidanceEdit) && !p.Has(auth.GuidanceApprove) {
		return m.Guard.Deny(ctx, string(auth.GuidanceEdit), "guidance", "", "missing_permission", nil)
	}
	rows, err := m.Pool.Query(ctx, `SELECT c.id, c.slug, c.retired_at, c.retired_reason, v.id, v.version, v.status,
		COALESCE(a.display_name, 'داده نمونه'), v.author_id, COALESCE(rv.display_name, ''), v.review_reason, v.created_at, v.reviewed_at,
		v.title, v.category, v.keywords, v.body, v.actions, v.emergency
		FROM guidance_cards c JOIN guidance_versions v ON v.card_id = c.id
		LEFT JOIN users a ON a.id = v.author_id LEFT JOIN users rv ON rv.id = v.reviewer_id
		ORDER BY c.retired_at NULLS FIRST, c.slug, v.version DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	cards := []*AdminCard{}
	byID := map[uuid.UUID]*AdminCard{}
	for rows.Next() {
		var c AdminCard
		var v Version
		if err := rows.Scan(&c.ID, &c.Slug, &c.RetiredAt, &c.RetiredReason, &v.ID, &v.Version, &v.Status, &v.AuthorName, &v.AuthorID,
			&v.ReviewerName, &v.ReviewReason, &v.CreatedAt, &v.ReviewedAt, &v.Title, &v.Category, &v.Keywords, &v.Body, &v.Actions, &v.Emergency); err != nil {
			return err
		}
		cur := byID[c.ID]
		if cur == nil {
			cur = &c
			byID[c.ID] = cur
			cards = append(cards, cur)
		}
		cur.Versions = append(cur.Versions, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"cards": cards, "categories": Categories, "actions": Actions})
	return nil
}

func (m *Module) createCard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GuidanceEdit, "guidance", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req struct {
		Slug string `json:"slug"`
		Content
	}
	if err := httpx.DecodeJSON(w, r, &req, 32<<10); err != nil {
		return err
	}
	req.Slug = strings.TrimSpace(req.Slug)
	if err := req.Content.Validate(); err != nil {
		return err
	}
	if !slugRe.MatchString(req.Slug) {
		return httpx.Validation(httpx.FieldDetail{Field: "slug", Reason: "lowercase_latin_digits_dash"})
	}
	cardID, versionID := uuid.New(), uuid.New()
	err := db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO guidance_cards (id, slug, created_by) VALUES ($1,$2,$3)`, cardID, req.Slug, p.UserID)
		if db.IsUniqueViolation(err, "") {
			return httpx.Conflict("SLUG_IN_USE", "این شناسه قبلاً استفاده شده است")
		}
		if err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, versionID, cardID, 1, req.Content, &p.UserID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "guidance.create",
			TargetType: "guidance_card", TargetID: cardID.String(), Outcome: "success", Details: map[string]any{"slug": req.Slug},
			CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"card_id": cardID, "version_id": versionID, "status": "pending"})
	return nil
}

func insertVersion(ctx context.Context, tx pgx.Tx, id, cardID uuid.UUID, version int, c Content, author *uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO guidance_versions (id, card_id, version, title, category, keywords, body, actions, emergency, author_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, cardID, version, c.Title, c.Category, c.Keywords, c.Body, c.Actions, c.Emergency, author)
	if db.IsUniqueViolation(err, "guidance_one_pending") {
		return httpx.Conflict("PENDING_VERSION_EXISTS", "این کارت یک نسخه در انتظار تأیید دارد؛ ابتدا آن را تأیید یا رد کنید")
	}
	return err
}

// newVersion proposes a correction; the published text stays live until the new version is approved.
func (m *Module) newVersion(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GuidanceEdit, "guidance", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	cardID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var c Content
	if err := httpx.DecodeJSON(w, r, &c, 32<<10); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	versionID := uuid.New()
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		var retired *time.Time
		var next int
		// Lock the card (serialises proposals), then number the new version.
		err := tx.QueryRow(ctx, `SELECT retired_at FROM guidance_cards WHERE id = $1 FOR UPDATE`, cardID).Scan(&retired)
		if err == pgx.ErrNoRows {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version), 0) + 1 FROM guidance_versions WHERE card_id = $1`, cardID).Scan(&next); err != nil {
			return err
		}
		if retired != nil {
			return httpx.Conflict("CARD_RETIRED", "این کارت بازنشسته شده است")
		}
		if err := insertVersion(ctx, tx, versionID, cardID, next, c, &p.UserID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "guidance.propose",
			TargetType: "guidance_card", TargetID: cardID.String(), Outcome: "success", Details: map[string]any{"version": next},
			CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"version_id": versionID, "status": "pending"})
	return nil
}

type pendingRow struct {
	cardID uuid.UUID
	slug   string
	ver    int
	author *uuid.UUID
	status string
}

func loadPending(ctx context.Context, tx pgx.Tx, id uuid.UUID) (pendingRow, error) {
	var p pendingRow
	err := tx.QueryRow(ctx, `SELECT v.card_id, c.slug, v.version, v.author_id, v.status FROM guidance_versions v
		JOIN guidance_cards c ON c.id = v.card_id WHERE v.id = $1 FOR UPDATE OF v`, id).Scan(&p.cardID, &p.slug, &p.ver, &p.author, &p.status)
	if err == pgx.ErrNoRows {
		return p, httpx.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if p.status != "pending" {
		return p, httpx.Conflict("NOT_PENDING", "این نسخه در انتظار تأیید نیست")
	}
	return p, nil
}

func (m *Module) approve(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GuidanceApprove, "guidance", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	var kb int64
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		pv, err := loadPending(ctx, tx, id)
		if err != nil {
			return err
		}
		if pv.author != nil && *pv.author == p.UserID {
			return m.Guard.Deny(ctx, string(auth.GuidanceApprove), "guidance_version", id.String(), "same_person",
				httpx.NewError(http.StatusForbidden, "FOUR_EYES_REQUIRED", "تأییدکننده باید فردی غیر از نویسنده باشد"))
		}
		if _, err := tx.Exec(ctx, `UPDATE guidance_versions SET status='superseded' WHERE card_id=$1 AND status='approved'`, pv.cardID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE guidance_versions SET status='approved', reviewer_id=$2, review_reason=$3, reviewed_at=now(),
			published_seq=nextval('guidance_publish_seq') WHERE id=$1 RETURNING published_seq`, id, p.UserID, strings.TrimSpace(req.Reason)).Scan(&kb); err != nil {
			return err
		}
		if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "guidance.published",
			Aggregate: outbox.Aggregate{Type: "guidance_card", ID: pv.cardID, Version: pv.ver},
			Payload:   map[string]any{"card_id": pv.cardID, "slug": pv.slug, "version": pv.ver, "kb_version": kb}}); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "guidance.approve",
			TargetType: "guidance_version", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"slug": pv.slug, "version": pv.ver}, CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "approved", "kb_version": kb})
	return nil
}

func (m *Module) reject(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GuidanceApprove, "guidance", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		return httpx.Validation(httpx.FieldDetail{Field: "reason", Reason: "required"})
	}
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		pv, err := loadPending(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE guidance_versions SET status='rejected', reviewer_id=$2, review_reason=$3, reviewed_at=now()
			WHERE id=$1`, id, p.UserID, req.Reason); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "guidance.reject",
			TargetType: "guidance_version", TargetID: id.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"slug": pv.slug, "version": pv.ver}, CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "rejected"})
	return nil
}

// retire takes a card out of the citizens' set (e.g. wrong or obsolete advice) without deleting its history.
func (m *Module) retire(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GuidanceApprove, "guidance", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		return httpx.Validation(httpx.FieldDetail{Field: "reason", Reason: "required"})
	}
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE guidance_cards SET retired_at=now(), retired_reason=$2,
			retired_seq=nextval('guidance_publish_seq') WHERE id=$1 AND retired_at IS NULL`, id, req.Reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.ErrNotFound
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "guidance.retire",
			TargetType: "guidance_card", TargetID: id.String(), Outcome: "success", Reason: req.Reason, CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "retired"})
	return nil
}

// SeedFile is the bundled sample guidance (seeds/guidance.fa.json).
type SeedFile struct {
	Cards []struct {
		Slug string `json:"slug"`
		Content
	} `json:"cards"`
}

// Seed loads sample cards that do not exist yet as approved version 1 (marked as samples needing expert
// review). Existing cards are never touched, so edits made in the console survive re-seeding.
func Seed(ctx context.Context, pool *pgxpool.Pool, raw []byte) (int, error) {
	var f SeedFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, err
	}
	added := 0
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		for _, c := range f.Cards {
			content := c.Content
			if err := content.Validate(); err != nil {
				return err
			}
			id := uuid.New()
			tag, err := tx.Exec(ctx, `INSERT INTO guidance_cards (id, slug) VALUES ($1,$2) ON CONFLICT (slug) DO NOTHING`, id, c.Slug)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO guidance_versions (id, card_id, version, title, category, keywords, body, actions, emergency,
				status, published_seq, review_reason, reviewed_at)
				VALUES ($1,$2,1,$3,$4,$5,$6,$7,$8,'approved', nextval('guidance_publish_seq'), 'نمونه آزمایشی؛ پیش از استفاده عملیاتی باید کارشناس تأیید کند', now())`,
				uuid.New(), id, content.Title, content.Category, content.Keywords, content.Body, content.Actions, content.Emergency); err != nil {
				return err
			}
			added++
		}
		return nil
	})
	return added, err
}
