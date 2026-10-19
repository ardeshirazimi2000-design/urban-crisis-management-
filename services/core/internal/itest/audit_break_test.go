package itest

import (
	"context"
	"strconv"
	"testing"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/compliance"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
)

// A background service that signs an audit record without the configured key (the notifier before the fix)
// breaks the chain: the check names it as a key fault, reports it once, and the security auditor can accept it.
func TestAuditBreakFromUnconfiguredService(t *testing.T) {
	ctx := context.Background()
	auditor := login(t, grant{Role: "SECURITY_AUDITOR"})

	audit.Configure("", cfg.AuditFallback) // as a process that never loaded AUDIT_KEY
	err := audit.WriteStandalone(ctx, pool, audit.Entry{Action: "alert.expire", TargetType: "alert", Outcome: "success"})
	audit.Configure(cfg.AuditKey, cfg.AuditFallback)
	if err != nil {
		t.Fatal(err)
	}

	var v audit.VerifyResult
	auditor.do("GET", "/admin/audit/verify", nil, nil, &v, 200)
	if v.Valid || v.Problem != "hash_mismatch" || !v.DevKey {
		t.Fatalf("expected a dev-key hash mismatch, got %+v", v)
	}

	// Hourly checks report the same break once.
	m := &compliance.Module{Pool: pool, IntegrityKey: db.IntegrityKey}
	for i := 0; i < 3; i++ {
		if _, err := m.CheckIntegrity(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM security_notifications WHERE kind='audit' AND details->>'seq' = $1`,
		strconv.FormatInt(v.BrokenSeq, 10)).Scan(&n)
	if n != 1 {
		t.Fatalf("break reported %d times, want once", n)
	}

	// Only the auditor, only the current break, only with a reason.
	login(t, grant{Role: "SECURITY_ADMIN"}).do("POST", "/admin/audit/acknowledge-break", map[string]any{"seq": v.BrokenSeq, "reason": "x"}, nil, nil, 403)
	auditor.do("POST", "/admin/audit/acknowledge-break", map[string]any{"seq": v.BrokenSeq - 1, "reason": "x"}, nil, nil, 409)
	auditor.do("POST", "/admin/audit/acknowledge-break", map[string]any{"seq": v.BrokenSeq}, nil, nil, 422)

	var after audit.VerifyResult
	auditor.do("POST", "/admin/audit/acknowledge-break", map[string]any{"seq": v.BrokenSeq,
		"reason": "رکورد انقضای هشدار با کلید پیش‌فرض امضا شده بود؛ سرویس اعلان اصلاح شد"}, nil, &after, 200)
	if !after.Valid || after.Acknowledged != 1 {
		t.Fatalf("after acknowledgement: %+v", after)
	}
	if countAudit(t, "audit.break_acknowledged", "success", strconv.FormatInt(v.BrokenSeq, 10)) != 1 {
		t.Fatal("acknowledgement must be audited")
	}
}
