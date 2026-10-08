// Package admin contains identity administration (separated from operational command), audit access,
// event replay and the local-only development token endpoint.
package admin

import (
	"net/http"
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

// MaxBreakGlass bounds emergency (time-boxed) grants.
const MaxBreakGlass = 8 * time.Hour

// MaxReplayEvents bounds a single replay request.
const MaxReplayEvents = 1000

type Module struct {
	Pool          *pgxpool.Pool
	Guard         *auth.Guard
	Resolver      *auth.Resolver
	ReplayLimiter *httpx.RateLimiter
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("GET /me", m.me)
	r.Auth("GET /organizations", m.organizations)
	r.Auth("POST /admin/organizations", m.createOrganization)
	r.Auth("GET /admin/users", m.users)
	r.Auth("POST /admin/users/{id}/roles", m.grant)
	r.Auth("DELETE /admin/users/{id}/roles/{grant}", m.revoke)
	r.Auth("POST /admin/users/{id}/status", m.userStatus)
	r.Auth("GET /admin/audit", m.auditList)
	r.Auth("GET /admin/audit/verify", m.auditVerify)
	r.Auth("GET /admin/outbox", m.outboxList)
	r.Auth("POST /admin/outbox/replay", m.replay)
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	httpx.JSON(w, http.StatusOK, map[string]any{"user_id": p.UserID, "subject": p.Subject, "display_name": p.DisplayName,
		"grants": p.Grants, "roles": p.RoleNames(), "permissions": p.Permissions()})
	return nil
}

func (m *Module) organizations(w http.ResponseWriter, r *http.Request) error {
	rows, err := m.Pool.Query(r.Context(), `SELECT id, code, name FROM organizations ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var code, name string
		if err := rows.Scan(&id, &code, &name); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "code": code, "name": name})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return rows.Err()
}

func (m *Module) createOrganization(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.UserManage, "organization", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	var v httpx.Validator
	v.Check(len(req.Code) >= 2 && len(req.Code) <= 40, "code", "length_2_to_40")
	v.Check(strings.TrimSpace(req.Name) != "", "name", "required")
	if err := v.Err(); err != nil {
		return err
	}
	var id uuid.UUID
	err := db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO organizations (code, name) VALUES ($1,$2) RETURNING id`, req.Code, req.Name).Scan(&id)
		if db.IsUniqueViolation(err, "") {
			return httpx.Conflict("DUPLICATE_CODE", "کد سازمان تکراری است")
		}
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "organization.create",
			TargetType: "organization", TargetID: id.String(), Outcome: "success", CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "code": req.Code, "name": req.Name})
	return nil
}

func (m *Module) users(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.UserManage, "user", ""); err != nil {
		return err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := m.Pool.Query(ctx, `SELECT u.id, u.subject_id, u.display_name, u.status, u.created_at,
		COALESCE(json_agg(json_build_object('grant_id', ur.id, 'role', ur.role_code, 'scope_org_id', ur.scope_org_id,
		  'expires_at', ur.expires_at, 'reason', ur.reason)) FILTER (WHERE ur.id IS NOT NULL), '[]')
		FROM users u LEFT JOIN user_roles ur ON ur.user_id = u.id AND (ur.expires_at IS NULL OR ur.expires_at > now())
		WHERE $1 = '' OR u.display_name ILIKE '%' || $1 || '%' OR u.subject_id ILIKE '%' || $1 || '%'
		GROUP BY u.id ORDER BY u.created_at DESC LIMIT 200`, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var sub, name, status string
		var created time.Time
		var grants []map[string]any
		if err := rows.Scan(&id, &sub, &name, &status, &created, &grants); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "subject": sub, "display_name": name, "status": status,
			"created_at": created, "grants": grants})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return rows.Err()
}

type grantRequest struct {
	Role       string     `json:"role"`
	ScopeOrgID *uuid.UUID `json:"scope_org_id"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Reason     string     `json:"reason"`
	BreakGlass bool       `json:"break_glass"`
}

// grant assigns a role. Break-glass grants are time-boxed, require a reason, and raise a high-priority audit event.
func (m *Module) grant(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.RoleManage, "user", r.PathValue("id")); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	userID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req grantRequest
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	var v httpx.Validator
	req.Reason = strings.TrimSpace(req.Reason)
	v.Check(auth.IsKnownRole(req.Role), "role", "unknown")
	v.Check(req.Reason != "", "reason", "required")
	v.Check(req.ExpiresAt == nil || req.ExpiresAt.After(time.Now()), "expires_at", "in_past")
	if req.BreakGlass {
		v.Check(req.ExpiresAt != nil && req.ExpiresAt.Before(time.Now().Add(MaxBreakGlass+time.Minute)), "expires_at", "break_glass_requires_expiry_within_8h")
	}
	// Separation of duties: nobody grants themselves roles.
	v.Check(userID != p.UserID, "id", "cannot_modify_own_roles")
	if err := v.Err(); err != nil {
		return err
	}
	var grantID uuid.UUID
	var subject string
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT subject_id FROM users WHERE id=$1`, userID).Scan(&subject); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.ErrNotFound
			}
			return err
		}
		err := tx.QueryRow(ctx, `INSERT INTO user_roles (user_id, role_code, scope_org_id, granted_by, expires_at, reason)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, userID, req.Role, req.ScopeOrgID, p.UserID, req.ExpiresAt, req.Reason).Scan(&grantID)
		if db.IsUniqueViolation(err, "") {
			return httpx.Conflict("ALREADY_GRANTED", "این نقش قبلاً با همین دامنه اعطا شده است")
		}
		if err != nil {
			return err
		}
		action := "role.grant"
		if req.BreakGlass {
			action = "role.break_glass_grant"
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: action, TargetType: "user",
			TargetID: userID.String(), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"role": req.Role, "scope_org_id": req.ScopeOrgID, "expires_at": req.ExpiresAt, "grant_id": grantID},
			CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	m.Resolver.Invalidate(subject)
	httpx.JSON(w, http.StatusCreated, map[string]any{"grant_id": grantID})
	return nil
}

func (m *Module) revoke(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.RoleManage, "user", r.PathValue("id")); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	userID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	grantID, err := httpx.PathUUID(r, "grant")
	if err != nil {
		return err
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if reason == "" {
		return httpx.Validation(httpx.FieldDetail{Field: "reason", Reason: "required"})
	}
	var subject, role string
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `DELETE FROM user_roles ur USING users u WHERE ur.id=$1 AND ur.user_id=$2 AND u.id=ur.user_id
			RETURNING u.subject_id, ur.role_code`, grantID, userID).Scan(&subject, &role)
		if err == pgx.ErrNoRows {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "role.revoke", TargetType: "user",
			TargetID: userID.String(), Outcome: "success", Reason: reason, Details: map[string]any{"role": role, "grant_id": grantID},
			CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	m.Resolver.Invalidate(subject)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) userStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.UserManage, "user", r.PathValue("id")); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	userID, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	var v httpx.Validator
	v.Check(req.Status == "active" || req.Status == "suspended", "status", "not_allowed")
	v.Check(strings.TrimSpace(req.Reason) != "", "reason", "required")
	v.Check(userID != p.UserID, "id", "cannot_modify_self")
	if err := v.Err(); err != nil {
		return err
	}
	var subject string
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `UPDATE users SET status=$2 WHERE id=$1 RETURNING subject_id`, userID, req.Status).Scan(&subject); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.ErrNotFound
			}
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "user.status", TargetType: "user",
			TargetID: userID.String(), Outcome: "success", Reason: req.Reason, Details: map[string]any{"status": req.Status},
			CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	m.Resolver.Invalidate(subject)
	httpx.JSON(w, http.StatusOK, map[string]any{"id": userID, "status": req.Status})
	return nil
}

func (m *Module) auditList(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.AuditRead, "audit_log", ""); err != nil {
		return err
	}
	q := r.URL.Query()
	limit, err := httpx.PageSize(r, 100, 500)
	if err != nil {
		return err
	}
	f := audit.Filter{TargetType: q.Get("target_type"), TargetID: q.Get("target_id"), Outcome: q.Get("outcome"), Limit: limit}
	if s := q.Get("actor_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return httpx.Validation(httpx.FieldDetail{Field: "actor_id", Reason: "invalid"})
		}
		f.ActorID = &id
	}
	if s := q.Get("before_seq"); s != "" {
		if f.BeforeSeq, err = strconv.ParseInt(s, 10, 64); err != nil {
			return httpx.Validation(httpx.FieldDetail{Field: "before_seq", Reason: "invalid"})
		}
	}
	items, err := audit.List(ctx, m.Pool, f)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (m *Module) auditVerify(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.AuditRead, "audit_log", ""); err != nil {
		return err
	}
	res, err := audit.Verify(ctx, m.Pool)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (m *Module) outboxList(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.EventReplay, "outbox", ""); err != nil {
		return err
	}
	state, err := httpx.OneOf(r, "state", "pending", "dead", "published")
	if err != nil {
		return err
	}
	if state == "" {
		state = "dead"
	}
	rows, err := m.Pool.Query(ctx, `SELECT id, aggregate_type, aggregate_id, event_type, schema_version, created_at, published_at,
		attempts, last_error, dead_lettered_at FROM outbox_events
		WHERE CASE $1 WHEN 'dead' THEN dead_lettered_at IS NOT NULL
		              WHEN 'pending' THEN published_at IS NULL AND dead_lettered_at IS NULL
		              ELSE published_at IS NOT NULL END
		ORDER BY seq DESC LIMIT 200`, state)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, agg uuid.UUID
		var aggType, typ string
		var ver, attempts int
		var created time.Time
		var published, dead *time.Time
		var lastErr *string
		if err := rows.Scan(&id, &aggType, &agg, &typ, &ver, &created, &published, &attempts, &lastErr, &dead); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "aggregate_type": aggType, "aggregate_id": agg, "event_type": typ,
			"schema_version": ver, "created_at": created, "published_at": published, "attempts": attempts, "last_error": lastErr,
			"dead_lettered_at": dead})
	}
	var pending, dead int
	var oldest *float64
	if err := m.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE published_at IS NULL AND dead_lettered_at IS NULL),
		count(*) FILTER (WHERE dead_lettered_at IS NOT NULL),
		EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE published_at IS NULL AND dead_lettered_at IS NULL))
		FROM outbox_events`).Scan(&pending, &dead, &oldest); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "pending": pending, "dead": dead, "oldest_pending_seconds": oldest})
	return rows.Err()
}

type replayRequest struct {
	EventIDs    []uuid.UUID `json:"event_ids"`
	AggregateID *uuid.UUID  `json:"aggregate_id"`
	Reason      string      `json:"reason"`
}

// replay re-queues dead-lettered or already-published events. It is permissioned, bounded, rate-limited and audited.
// Consumers are idempotent (inbox), so replaying a published event has no duplicate effect.
func (m *Module) replay(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.EventReplay, "outbox", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	if m.ReplayLimiter != nil && !m.ReplayLimiter.Allow("replay:"+p.UserID.String()) {
		return httpx.ErrRateLimited
	}
	var req replayRequest
	if err := httpx.DecodeJSON(w, r, &req, 64<<10); err != nil {
		return err
	}
	var v httpx.Validator
	v.Check(strings.TrimSpace(req.Reason) != "", "reason", "required")
	v.Check((len(req.EventIDs) > 0) != (req.AggregateID != nil), "event_ids", "exactly_one_of_event_ids_or_aggregate_id")
	v.Check(len(req.EventIDs) <= MaxReplayEvents, "event_ids", "too_many")
	if err := v.Err(); err != nil {
		return err
	}
	var n int64
	err := db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE outbox_events SET published_at=NULL, dead_lettered_at=NULL, attempts=0, next_attempt_at=now(), last_error=NULL
			WHERE id IN (SELECT id FROM outbox_events WHERE (id = ANY($1) OR aggregate_id = $2) ORDER BY seq LIMIT $3)`,
			req.EventIDs, req.AggregateID, MaxReplayEvents)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "event.replay", TargetType: "outbox",
			TargetID: "", Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"count": n, "aggregate_id": req.AggregateID, "event_ids": len(req.EventIDs)}, CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"requeued": n})
	return nil
}
