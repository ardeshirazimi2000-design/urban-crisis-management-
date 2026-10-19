// Package compliance implements the AFTA protection-profile functions around stored data: record-integrity
// checks (FDP_SDI.2), audit storage capacity and archiving (FAU_STG.3/4), audit export (FAU_SAR), controlled
// export of operational data (FDP_ETC) and security notifications to the responsible staff.
package compliance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/apiir"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/security"
)

type Module struct {
	Pool         *pgxpool.Pool
	Guard        *auth.Guard
	Settings     *security.Store
	APIIR        apiir.Client
	IntegrityKey string
	AuditKey     string

	mu        sync.Mutex
	lastCheck *IntegrityResult
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("GET /admin/audit/export", m.auditExport)
	r.Auth("POST /admin/audit/archive", m.auditArchive)
	r.Auth("POST /admin/audit/acknowledge-break", m.acknowledgeBreak)
	r.Auth("GET /admin/audit/status", m.auditStatus)
	r.Auth("POST /admin/integrity/check", m.integrityCheck)
	r.Auth("GET /admin/export/incidents", m.exportIncidents)
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

// Notify records a security notification and texts the security administrators and auditors (best effort).
func (m *Module) Notify(ctx context.Context, kind, severity, message string, details map[string]any) {
	b, _ := json.Marshal(details)
	if _, err := m.Pool.Exec(ctx, `INSERT INTO security_notifications (kind, severity, message, details) VALUES ($1,$2,$3,$4::jsonb)`,
		kind, severity, message, string(b)); err != nil {
		slog.ErrorContext(ctx, "security notification", "err", err.Error())
	}
	if m.APIIR == nil || (severity != "critical" && severity != "high") {
		return
	}
	rows, err := m.Pool.Query(ctx, `SELECT DISTINCT u.mobile FROM users u JOIN user_roles r ON r.user_id=u.id
		WHERE r.role_code IN ('SECURITY_ADMIN','SECURITY_AUDITOR') AND u.status='active' AND u.mobile IS NOT NULL`)
	if err != nil {
		return
	}
	var mobiles []string
	for rows.Next() {
		var mob string
		if rows.Scan(&mob) == nil {
			mobiles = append(mobiles, mob)
		}
	}
	rows.Close()
	if len(mobiles) > 0 {
		if err := m.APIIR.SendSMS(ctx, mobiles, "هشدار امنیتی سامانه مدیریت بحران: "+message); err != nil {
			slog.WarnContext(ctx, "security sms", "err", err.Error())
		}
	}
}

// ---------------------------------------------------------------------------
// Record integrity
// ---------------------------------------------------------------------------

type Violation struct {
	Type   string    `json:"record_type"`
	ID     uuid.UUID `json:"record_id"`
	Reason string    `json:"reason"` // mismatch | missing
}

type IntegrityResult struct {
	Checked    int         `json:"checked"`
	Violations []Violation `json:"violations"`
	AuditChain audit.VerifyResult
	At         time.Time `json:"at"`
}

var sealed = []struct{ table, fn string }{{"incidents", "incident_seal_data"}, {"alerts", "alert_seal_data"}}

// CheckIntegrity recomputes every seal and the audit chain; violations are recorded, audited and notified.
func (m *Module) CheckIntegrity(ctx context.Context, actor *uuid.UUID) (IntegrityResult, error) {
	res := IntegrityResult{Violations: []Violation{}, At: time.Now().UTC()}
	for _, t := range sealed {
		rows, err := m.Pool.Query(ctx, `SELECT t.id, s.mac IS NULL, s.mac IS DISTINCT FROM encode(hmac(`+t.fn+`(t), $1, 'sha256'), 'hex')
			FROM `+t.table+` t LEFT JOIN record_seals s ON s.record_type=$2 AND s.record_id=t.id`, m.IntegrityKey, t.table)
		if err != nil {
			return res, err
		}
		for rows.Next() {
			var id uuid.UUID
			var missing, bad bool
			if err := rows.Scan(&id, &missing, &bad); err != nil {
				rows.Close()
				return res, err
			}
			res.Checked++
			if missing {
				res.Violations = append(res.Violations, Violation{Type: t.table, ID: id, Reason: "missing"})
			} else if bad {
				res.Violations = append(res.Violations, Violation{Type: t.table, ID: id, Reason: "mismatch"})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, err
		}
	}
	chain, err := audit.Verify(ctx, m.Pool)
	if err != nil {
		return res, err
	}
	res.AuditChain = chain
	_, _ = m.Pool.Exec(ctx, `UPDATE record_seals SET checked_at=now(), ok=true`)
	for _, v := range res.Violations {
		_, _ = m.Pool.Exec(ctx, `UPDATE record_seals SET ok=false WHERE record_type=$1 AND record_id=$2`, v.Type, v.ID)
		_ = audit.WriteStandalone(ctx, m.Pool, audit.Entry{ActorID: actor, Action: "integrity.violation", TargetType: strings.TrimSuffix(v.Type, "s"),
			TargetID: v.ID.String(), Outcome: "failed", Details: map[string]any{"reason": v.Reason}})
	}
	if len(res.Violations) > 0 {
		m.Notify(ctx, "integrity", "critical", fmt.Sprintf("تغییر غیرمجاز در %d رکورد حادثه/هشدار تشخیص داده شد", len(res.Violations)),
			map[string]any{"violations": res.Violations})
	}
	// A break is reported (audit record, notification, SMS) once, not on every hourly check.
	var reported bool
	if !chain.Valid {
		if err := m.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM security_notifications WHERE kind='audit'
			AND details->>'seq' = $1 AND details->>'problem' = $2)`, strconv.FormatInt(chain.BrokenSeq, 10), chain.Problem).Scan(&reported); err != nil {
			return res, err
		}
	}
	if !chain.Valid && !reported {
		_ = audit.WriteStandalone(ctx, m.Pool, audit.Entry{ActorID: actor, Action: "audit.chain_broken", TargetType: "audit_log",
			TargetID: strconv.FormatInt(chain.BrokenSeq, 10), Outcome: "failed", Details: map[string]any{"problem": chain.Problem, "dev_key": chain.DevKey}})
		msg := "زنجیره ممیزی دستکاری شده است"
		if chain.DevKey {
			msg = "گسست زنجیره ممیزی: رکورد با کلید پیش‌فرض امضا شده (خطای پیکربندی سرویس، نه تغییر رکورد)"
		}
		m.Notify(ctx, "audit", "critical", msg, map[string]any{"seq": strconv.FormatInt(chain.BrokenSeq, 10), "problem": chain.Problem, "dev_key": chain.DevKey})
	}
	_ = audit.WriteStandalone(ctx, m.Pool, audit.Entry{ActorID: actor, Action: "integrity.check", TargetType: "system",
		Outcome: map[bool]string{true: "success", false: "failed"}[len(res.Violations) == 0 && chain.Valid],
		Details: map[string]any{"checked": res.Checked, "violations": len(res.Violations), "audit_chain_valid": chain.Valid}})
	m.mu.Lock()
	m.lastCheck = &res
	m.mu.Unlock()
	return res, nil
}

// SealExisting seals records that have no seal yet (records created before the integrity migration).
func (m *Module) SealExisting(ctx context.Context) (int64, error) {
	var total int64
	for _, t := range sealed {
		tag, err := m.Pool.Exec(ctx, `INSERT INTO record_seals (record_type, record_id, mac)
			SELECT $2, t.id, encode(hmac(`+t.fn+`(t), $1, 'sha256'), 'hex') FROM `+t.table+` t
			WHERE NOT EXISTS (SELECT 1 FROM record_seals s WHERE s.record_type=$2 AND s.record_id=t.id)`, m.IntegrityKey, t.table)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	if total > 0 {
		err := audit.WriteStandalone(ctx, m.Pool, audit.Entry{Action: "integrity.seal_existing", TargetType: "system", Outcome: "success",
			Channel: "cli", Details: map[string]any{"sealed": total}})
		return total, err
	}
	return 0, nil
}

func (m *Module) integrityCheck(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.IntegrityCheck, "system", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	res, err := m.CheckIntegrity(ctx, &p.UserID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// ---------------------------------------------------------------------------
// Audit capacity (FAU_STG.3 / FAU_STG.4)
// ---------------------------------------------------------------------------

// CheckAuditCapacity compares the trail size with the configured thresholds and sets audit.Full.
func (m *Module) CheckAuditCapacity(ctx context.Context) (int64, error) {
	st, err := m.Settings.Get(ctx)
	if err != nil {
		return 0, err
	}
	var rows int64
	if err := m.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&rows); err != nil {
		return 0, err
	}
	full := rows >= int64(st.AuditMaxRows)
	wasFull := audit.Full.Load()
	audit.Full.Store(full && st.AuditFullAction == "block")
	switch {
	case full && !wasFull:
		_ = audit.WriteStandalone(ctx, m.Pool, audit.Entry{Action: "audit.full", TargetType: "audit_log", Outcome: "failed",
			Details: map[string]any{"rows": rows, "max": st.AuditMaxRows, "action": st.AuditFullAction}})
		m.Notify(ctx, "audit_capacity", "critical", "ظرفیت ممیزی تکمیل شده است؛ ممیزی را بایگانی کنید", map[string]any{"rows": rows})
	case rows >= int64(st.AuditWarnRows) && !full:
		var recent bool
		_ = m.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM security_notifications WHERE kind='audit_capacity_warn' AND created_at > now() - interval '1 day')`).Scan(&recent)
		if !recent {
			m.Notify(ctx, "audit_capacity_warn", "high", fmt.Sprintf("حجم ممیزی به %d رکورد رسیده است (آستانه هشدار)", rows), map[string]any{"rows": rows})
		}
	}
	return rows, nil
}

func (m *Module) auditStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.AuditRead, "audit_log", ""); err != nil {
		return err
	}
	rows, err := m.CheckAuditCapacity(ctx)
	if err != nil {
		return err
	}
	st, err := m.Settings.Get(ctx)
	if err != nil {
		return err
	}
	var archives []map[string]any
	ar, err := m.Pool.Query(ctx, `SELECT a.id, a.first_seq, a.last_seq, a.row_count, a.digest, u.display_name, a.created_at
		FROM audit_archives a LEFT JOIN users u ON u.id=a.archived_by ORDER BY a.id DESC LIMIT 20`)
	if err != nil {
		return err
	}
	for ar.Next() {
		var id, first, last, n int64
		var digest string
		var by *string
		var at time.Time
		if err := ar.Scan(&id, &first, &last, &n, &digest, &by, &at); err != nil {
			ar.Close()
			return err
		}
		archives = append(archives, map[string]any{"id": id, "first_seq": first, "last_seq": last, "row_count": n, "digest": digest,
			"archived_by": by, "created_at": at})
	}
	ar.Close()
	m.mu.Lock()
	last := m.lastCheck
	m.mu.Unlock()
	httpx.JSON(w, http.StatusOK, map[string]any{"rows": rows, "warn_rows": st.AuditWarnRows, "max_rows": st.AuditMaxRows,
		"full_action": st.AuditFullAction, "blocking": audit.Full.Load(), "archives": archives, "last_integrity_check": last})
	return nil
}

// ---------------------------------------------------------------------------
// Audit export and archive
// ---------------------------------------------------------------------------

func parseTime(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return &t, nil
		}
	}
	return nil, httpx.Validation(httpx.FieldDetail{Field: "date", Reason: "invalid"})
}

// digest is an HMAC over the exported rows so a recipient holding the key can verify the file.
func (m *Module) digest(b []byte) string {
	h := hmac.New(sha256.New, []byte(m.AuditKey))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

var csvHeader = []string{"seq", "created_at", "severity", "actor_id", "actor_roles", "action", "target_type", "target_id", "outcome",
	"reason", "ip", "channel", "user_agent", "correlation_id", "details", "hash"}

func csvRow(rec audit.Record) []string {
	actor := ""
	if rec.ActorID != nil {
		actor = rec.ActorID.String()
	}
	details, _ := json.Marshal(rec.Details)
	return []string{strconv.FormatInt(rec.Seq, 10), rec.CreatedAt.UTC().Format(time.RFC3339Nano), rec.Severity, actor,
		strings.Join(rec.ActorRoles, " "), rec.Action, rec.TargetType, rec.TargetID, rec.Outcome, rec.Reason, rec.IP, rec.Channel,
		rec.UserAgent, rec.CorrelationID, string(details), rec.Hash}
}

func (m *Module) render(w http.ResponseWriter, format, name string, records []audit.Record, meta map[string]any) error {
	var body []byte
	if format == "csv" {
		var sb strings.Builder
		sb.WriteString("\ufeff") // Excel opens UTF-8 Persian text correctly with a BOM
		cw := csv.NewWriter(&sb)
		_ = cw.Write(csvHeader)
		for _, rec := range records {
			_ = cw.Write(csvRow(rec))
		}
		cw.Flush()
		body = []byte(sb.String())
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	} else {
		meta["records"] = records
		body, _ = json.Marshal(meta)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.`+format+`"`)
	w.Header().Set("X-Export-Digest", "hmac-sha256="+m.digest(body))
	w.Header().Set("Access-Control-Expose-Headers", "X-Export-Digest, X-Correlation-ID")
	_, err := w.Write(body)
	return err
}

func (m *Module) auditExport(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.AuditRead, "audit_log", ""); err != nil {
		return err
	}
	format, err := httpx.OneOf(r, "format", "csv", "json")
	if err != nil {
		return err
	}
	if format == "" {
		format = "csv"
	}
	q := r.URL.Query()
	from, err := parseTime(q.Get("from"))
	if err != nil {
		return err
	}
	to, err := parseTime(q.Get("to"))
	if err != nil {
		return err
	}
	records, err := audit.List(ctx, m.Pool, audit.Filter{Action: q.Get("action"), Severity: q.Get("severity"), Outcome: q.Get("outcome"),
		From: from, To: to, Ascending: true, Limit: 100000})
	if err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	if err := audit.WriteStandalone(ctx, m.Pool, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "audit.export",
		TargetType: "audit_log", Outcome: "success", Details: map[string]any{"format": format, "rows": len(records), "from": from, "to": to}}); err != nil {
		return err
	}
	return m.render(w, format, "audit-"+time.Now().UTC().Format("20060102-150405"), records,
		map[string]any{"exported_at": time.Now().UTC(), "exported_by": p.UserID, "rows": len(records)})
}

// auditArchive removes the oldest part of the trail after handing it to the auditor as a file. The chain
// continues from a recorded anchor, so Verify still covers what remains.
// acknowledgeBreak lets the security auditor accept the current break in the audit chain after examining it
// (e.g. a record a background service signed without the configured key). The record's hash is pinned: if it is
// changed later, verification fails again.
func (m *Module) acknowledgeBreak(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.AuditConfigure, "audit_log", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req struct {
		Seq    int64  `json:"seq"`
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		return httpx.Validation(httpx.FieldDetail{Field: "reason", Reason: "required"})
	}
	chain, err := audit.Verify(ctx, m.Pool)
	if err != nil {
		return err
	}
	if chain.Valid || chain.BrokenSeq != req.Seq {
		return httpx.Conflict("NOT_CURRENT_BREAK", "این رکورد گسست فعلی زنجیره ممیزی نیست")
	}
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO audit_acknowledged_breaks (seq, problem, row_hash, reason, acknowledged_by)
			VALUES ($1,$2,$3,$4,$5)`, req.Seq, chain.Problem, chain.RowHash, req.Reason, p.UserID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "audit.break_acknowledged",
			TargetType: "audit_log", TargetID: strconv.FormatInt(req.Seq, 10), Outcome: "success", Reason: req.Reason,
			Details: map[string]any{"problem": chain.Problem, "dev_key": chain.DevKey}})
	})
	if db.IsUniqueViolation(err, "audit_acknowledged_breaks_pkey") {
		return httpx.Conflict("ALREADY_ACKNOWLEDGED", "این گسست قبلاً تأیید شده است")
	}
	if err != nil {
		return err
	}
	m.Notify(ctx, "audit", "high", fmt.Sprintf("گسست زنجیره ممیزی در رکورد %d توسط ممیز امنیتی بررسی و تأیید شد", req.Seq),
		map[string]any{"acknowledged_seq": req.Seq, "reason": req.Reason})
	after, err := audit.Verify(ctx, m.Pool)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, after)
	return nil
}

func (m *Module) auditArchive(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.AuditConfigure, "audit_log", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req struct {
		Before string `json:"before"` // archive records created before this date
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req, 4<<10); err != nil {
		return err
	}
	before, err := parseTime(req.Before)
	if err != nil || before == nil {
		return httpx.Validation(httpx.FieldDetail{Field: "before", Reason: "required_date"})
	}
	if strings.TrimSpace(req.Reason) == "" {
		return httpx.Validation(httpx.FieldDetail{Field: "reason", Reason: "required"})
	}
	if chain, err := audit.Verify(ctx, m.Pool); err != nil {
		return err
	} else if !chain.Valid {
		return httpx.Conflict("AUDIT_CHAIN_BROKEN", "زنجیره ممیزی معتبر نیست؛ پیش از بایگانی بررسی شود")
	}
	var records []audit.Record
	var body []byte
	err = db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4711)`); err != nil {
			return err
		}
		var lastSeq int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(seq), 0) FROM audit_log WHERE created_at < $1`, before).Scan(&lastSeq); err != nil {
			return err
		}
		if lastSeq == 0 {
			return httpx.Validation(httpx.FieldDetail{Field: "before", Reason: "nothing_to_archive"})
		}
		var err error
		records, err = audit.ListTx(ctx, tx, lastSeq)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return httpx.Validation(httpx.FieldDetail{Field: "before", Reason: "nothing_to_archive"})
		}
		meta := map[string]any{"archived_at": time.Now().UTC(), "archived_by": p.UserID, "reason": req.Reason, "rows": len(records)}
		meta["records"] = records
		body, _ = json.Marshal(meta)
		last := records[len(records)-1]
		if _, err := tx.Exec(ctx, `INSERT INTO audit_archives (first_seq, last_seq, last_hash, row_count, digest, archived_by)
			VALUES ($1,$2,$3,$4,$5,$6)`, records[0].Seq, last.Seq, last.Hash, len(records), m.digest(body), p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL crisis.audit_archive = 'on'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE seq <= $1`, last.Seq); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL crisis.audit_archive = 'off'`); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "audit.archive", TargetType: "audit_log",
			Outcome: "success", Reason: req.Reason, Details: map[string]any{"first_seq": records[0].Seq, "last_seq": last.Seq, "rows": len(records)}})
	})
	if err != nil {
		return err
	}
	if _, err := m.CheckAuditCapacity(ctx); err != nil {
		slog.WarnContext(ctx, "audit capacity", "err", err.Error())
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit-archive-`+time.Now().UTC().Format("20060102-150405")+`.json"`)
	w.Header().Set("X-Export-Digest", "hmac-sha256="+m.digest(body))
	w.Header().Set("Access-Control-Expose-Headers", "X-Export-Digest, X-Correlation-ID")
	_, err = w.Write(body)
	return err
}

// ---------------------------------------------------------------------------
// Controlled export of operational data (FDP_ETC.2): permission, scope, row limit, security metadata, audit
// ---------------------------------------------------------------------------

func (m *Module) exportIncidents(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.DataExport, "incident", ""); err != nil {
		return err
	}
	st, err := m.Settings.Get(ctx)
	if err != nil {
		return err
	}
	if !st.ExportEnabled {
		return httpx.NewError(http.StatusForbidden, "EXPORT_DISABLED", "خروجی گرفتن از داده‌ها غیرفعال است")
	}
	format, err := httpx.OneOf(r, "format", "csv", "json")
	if err != nil {
		return err
	}
	if format == "" {
		format = "csv"
	}
	p := auth.FromContext(ctx)
	all, orgs := p.OrgScope(auth.DataExport)
	rows, err := m.Pool.Query(ctx, `SELECT i.id, i.code, i.type, i.title, i.severity, i.response_level, i.status, o.name, i.opened_at,
		i.closed_at, i.updated_at, COALESCE(s.ok, false) FROM incidents i JOIN organizations o ON o.id=i.owner_org_id
		LEFT JOIN record_seals s ON s.record_type='incidents' AND s.record_id=i.id
		WHERE $1 OR i.owner_org_id = ANY($2) ORDER BY i.opened_at DESC LIMIT $3`, all, orgs, st.ExportMaxRows)
	if err != nil {
		return err
	}
	type row struct {
		ID                                                 uuid.UUID
		Code, Type, Title, Severity, Level, Status, OrgNam string
		Opened, Updated                                    time.Time
		Closed                                             *time.Time
		Sealed                                             bool
	}
	var items []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Code, &x.Type, &x.Title, &x.Severity, &x.Level, &x.Status, &x.OrgNam, &x.Opened, &x.Closed, &x.Updated, &x.Sealed); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	rows.Close()
	if err := audit.WriteStandalone(ctx, m.Pool, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "export.incidents",
		TargetType: "incident", Outcome: "success", Details: map[string]any{"format": format, "rows": len(items)}}); err != nil {
		return err
	}
	exported := time.Now().UTC()
	meta := map[string]any{"classification": "محرمانه — فقط برای استفاده سازمانی", "exported_at": exported, "exported_by": p.DisplayName,
		"exported_by_id": p.UserID, "rows": len(items)}
	var body []byte
	if format == "csv" {
		var sb strings.Builder
		sb.WriteString("\ufeff")
		cw := csv.NewWriter(&sb)
		_ = cw.Write([]string{"# " + meta["classification"].(string), "exported_at=" + exported.Format(time.RFC3339), "by=" + p.DisplayName})
		_ = cw.Write([]string{"id", "code", "type", "title", "severity", "response_level", "status", "organization", "opened_at", "closed_at",
			"updated_at", "integrity_ok"})
		for _, x := range items {
			closed := ""
			if x.Closed != nil {
				closed = x.Closed.UTC().Format(time.RFC3339)
			}
			_ = cw.Write([]string{x.ID.String(), x.Code, x.Type, x.Title, x.Severity, x.Level, x.Status, x.OrgNam,
				x.Opened.UTC().Format(time.RFC3339), closed, x.Updated.UTC().Format(time.RFC3339), strconv.FormatBool(x.Sealed)})
		}
		cw.Flush()
		body = []byte(sb.String())
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	} else {
		meta["records"] = items
		body, _ = json.Marshal(meta)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.Header().Set("Content-Disposition", `attachment; filename="incidents-`+exported.Format("20060102-150405")+`.`+format+`"`)
	w.Header().Set("X-Export-Digest", "hmac-sha256="+m.digest(body))
	w.Header().Set("Access-Control-Expose-Headers", "X-Export-Digest, X-Correlation-ID")
	_, err = w.Write(body)
	return err
}

// Run performs the periodic integrity and capacity checks until ctx ends.
func (m *Module) Run(ctx context.Context) {
	audit.AfterWrite = func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := m.CheckAuditCapacity(c); err != nil {
			slog.Warn("audit capacity", "err", err.Error())
		}
	}
	if _, err := m.CheckAuditCapacity(ctx); err != nil {
		slog.WarnContext(ctx, "audit capacity", "err", err.Error())
	}
	next := time.Now().Add(time.Minute)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if now.Before(next) {
				continue
			}
			if _, err := m.CheckIntegrity(ctx, nil); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "integrity check", "err", err.Error())
			}
			minutes := 60
			if st, err := m.Settings.Get(ctx); err == nil {
				minutes = st.IntegrityCheckMin
			}
			next = now.Add(time.Duration(minutes) * time.Minute)
		}
	}
}
