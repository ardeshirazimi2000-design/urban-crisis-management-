// Package audit writes the append-only, hash-chained audit log (AFTA PP: FAU_GEN, FAU_SAR, FAU_SEL, FAU_STG).
// Each row's hash covers its content and the previous row's hash, so any edit or deletion
// (already blocked by a trigger) is detectable by Verify. Rows written since the AFTA upgrade use HMAC-SHA256
// with a secret key (FCS_COP.1), so the chain cannot be recomputed by someone with database access only.
package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

const algHMAC = "hmac-sha256"

var (
	cfgMu        sync.RWMutex
	key          []byte
	fallbackFile string
	// Disabled returns the selectable events the auditor switched off (FAU_SEL.1); nil = none.
	Disabled func(ctx context.Context) []string
	// AfterWrite is called (asynchronously, at most every 200 rows) to check the trail's capacity.
	AfterWrite func()
	written    atomic.Int64
	// Full is set while the trail exceeds its capacity and the configured action is to block activity.
	Full atomic.Bool
)

// Configure sets the HMAC key and the fallback file. Called once at start-up.
func Configure(hmacKey, fallback string) {
	cfgMu.Lock()
	key, fallbackFile = []byte(hmacKey), fallback
	cfgMu.Unlock()
}

func hmacKey() []byte {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	if len(key) == 0 {
		return []byte(devKey)
	}
	return key
}

var severityRules = []struct {
	re  *regexp.Regexp
	sev string
}{
	{regexp.MustCompile(`^(integrity\.violation|audit\.(chain_broken|full|archive)|session\.(refresh_reuse|hijack_suspected))`), "critical"},
	{regexp.MustCompile(`^(account\.locked|shahkar\.mismatch|role\.|user\.(status|password_reset|role)|settings\.|audit\.(config|export)|event\.replay|organization\.)`), "high"},
	{regexp.MustCompile(`^(user\.|session\.|password\.|export\.|alert\.(approve|dispatch|cancel)|incident\.(activate|escalate|close))`), "medium"},
	{regexp.MustCompile(`\.(login|signup|logout)|^(console|app)\.`), "low"},
}

// SeverityOf classifies an event for review and alerting.
func SeverityOf(action, outcome string) string {
	for _, r := range severityRules {
		if r.re.MatchString(action) {
			if r.sev == "low" && outcome != "success" {
				return "medium"
			}
			return r.sev
		}
	}
	if outcome == "denied" {
		return "medium"
	}
	return "info"
}

const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// chainLock serialises appends so the chain is linear. This bounds audited-write throughput,
// which is acceptable for the MVP; partitioned chains are a later option (see ADR-008).
const chainLock = 4711

type Entry struct {
	ActorID       *uuid.UUID
	ActorRoles    []string
	Action        string
	TargetType    string
	TargetID      string
	Outcome       string // success | denied | failed
	Reason        string
	Details       map[string]any
	CorrelationID string
	Severity      string // derived from Action when empty
	IP            string // taken from the request when empty
	Channel       string
	UserAgent     string
}

type Record struct {
	Seq           int64          `json:"seq"`
	ID            uuid.UUID      `json:"id"`
	ActorID       *uuid.UUID     `json:"actor_id"`
	ActorRoles    []string       `json:"actor_roles"`
	Action        string         `json:"action"`
	TargetType    string         `json:"target_type"`
	TargetID      string         `json:"target_id"`
	Outcome       string         `json:"outcome"`
	Reason        string         `json:"reason"`
	Details       map[string]any `json:"details"`
	CreatedAt     time.Time      `json:"created_at"`
	CorrelationID string         `json:"correlation_id"`
	PrevHash      string         `json:"prev_hash"`
	Hash          string         `json:"hash"`
	HashAlg       string         `json:"hash_alg"`
	Severity      string         `json:"severity"`
	IP            string         `json:"ip"`
	Channel       string         `json:"channel"`
	UserAgent     string         `json:"user_agent"`
}

// canonicalJSON round-trips through a generic value so key order and number formatting are stable
// between the value hashed at write time and the jsonb value read back at verification time.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

func computeHash(r Record) (string, error) { return hashWith(r, hmacKey()) }

func hashWith(r Record, k []byte) (string, error) {
	details, err := canonicalJSON(r.Details)
	if err != nil {
		return "", err
	}
	actor := ""
	if r.ActorID != nil {
		actor = r.ActorID.String()
	}
	roles, _ := json.Marshal(r.ActorRoles)
	parts := []string{
		r.PrevHash, r.ID.String(), actor, string(roles), r.Action, r.TargetType, r.TargetID, r.Outcome,
		r.Reason, string(details), r.CreatedAt.UTC().Format(time.RFC3339Nano), r.CorrelationID,
	}
	h := sha256.New()
	if r.HashAlg == algHMAC {
		h = hmac.New(sha256.New, k)
		parts = append(parts, r.Severity, r.IP, r.Channel, r.UserAgent)
	}
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Write appends an entry inside the caller's transaction (so the audit row commits atomically with the change).
// If the entry cannot be stored it is copied to the fallback file and the error is returned, so the operation
// itself fails (FPT_FLS.1) while the event is not lost (FRU_FLT.1).
func Write(ctx context.Context, tx pgx.Tx, e Entry) error {
	if Disabled != nil && slices.Contains(Disabled(ctx), e.Action) {
		return nil
	}
	if err := write(ctx, tx, e); err != nil {
		writeFallback(e, err)
		return err
	}
	if written.Add(1)%200 == 0 && AfterWrite != nil {
		go AfterWrite()
	}
	return nil
}

func write(ctx context.Context, tx pgx.Tx, e Entry) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, chainLock); err != nil {
		return fmt.Errorf("audit lock: %w", err)
	}
	prev := genesisHash
	err := tx.QueryRow(ctx, `SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	if err == pgx.ErrNoRows {
		// After an archive of the whole trail the chain continues from the archive's anchor.
		err = tx.QueryRow(ctx, `SELECT last_hash FROM audit_archives ORDER BY id DESC LIMIT 1`).Scan(&prev)
		if err == pgx.ErrNoRows {
			prev, err = genesisHash, nil
		}
	}
	if err != nil {
		return fmt.Errorf("audit prev hash: %w", err)
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	if e.ActorRoles == nil {
		e.ActorRoles = []string{}
	}
	ri := httpx.Info(ctx)
	if e.IP == "" {
		e.IP = ri.IP
	}
	if e.UserAgent == "" {
		e.UserAgent = ri.UserAgent
	}
	if e.Channel == "" {
		e.Channel = ri.Channel
	}
	if e.Severity == "" {
		e.Severity = SeverityOf(e.Action, e.Outcome)
	}
	if e.CorrelationID == "" {
		e.CorrelationID = httpx.CorrelationID(ctx)
	}
	rec := Record{
		ID: uuid.New(), ActorID: e.ActorID, ActorRoles: e.ActorRoles, Action: e.Action, TargetType: e.TargetType,
		TargetID: e.TargetID, Outcome: e.Outcome, Reason: e.Reason, Details: e.Details,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond), CorrelationID: e.CorrelationID, PrevHash: prev,
		HashAlg: algHMAC, Severity: e.Severity, IP: e.IP, Channel: e.Channel, UserAgent: e.UserAgent,
	}
	details, err := canonicalJSON(rec.Details)
	if err != nil {
		return err
	}
	if rec.Hash, err = computeHash(rec); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (id, actor_id, actor_roles, action, target_type, target_id, outcome,
		reason, details, created_at, correlation_id, prev_hash, hash, hash_alg, severity, ip, channel, user_agent)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		rec.ID, rec.ActorID, rec.ActorRoles, rec.Action, rec.TargetType, rec.TargetID, rec.Outcome, rec.Reason,
		details, rec.CreatedAt, rec.CorrelationID, rec.PrevHash, rec.Hash, rec.HashAlg, rec.Severity, rec.IP, rec.Channel, rec.UserAgent)
	if err != nil {
		return fmt.Errorf("audit insert: %w", err)
	}
	return nil
}

func writeFallback(e Entry, cause error) {
	slog.Error("audit write failed", "err", cause.Error(), "action", e.Action)
	cfgMu.RLock()
	path := fallbackFile
	cfgMu.RUnlock()
	if path == "" {
		return
	}
	line, _ := json.Marshal(map[string]any{"at": time.Now().UTC(), "entry": e, "error": cause.Error()})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Error("audit fallback write failed", "err", err.Error())
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// WriteStandalone appends an entry in its own transaction (e.g. denied-access events where no domain change happens).
func WriteStandalone(ctx context.Context, pool *pgxpool.Pool, e Entry) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := Write(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const selectCols = `seq, id, actor_id, actor_roles, action, target_type, target_id, outcome, reason, details::text,
	created_at, correlation_id, prev_hash, hash, hash_alg, severity, ip, channel, user_agent`

func scan(row pgx.Row) (Record, error) {
	var r Record
	var details string
	if err := row.Scan(&r.Seq, &r.ID, &r.ActorID, &r.ActorRoles, &r.Action, &r.TargetType, &r.TargetID, &r.Outcome,
		&r.Reason, &details, &r.CreatedAt, &r.CorrelationID, &r.PrevHash, &r.Hash, &r.HashAlg, &r.Severity, &r.IP,
		&r.Channel, &r.UserAgent); err != nil {
		return r, err
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(details)))
	dec.UseNumber()
	if err := dec.Decode(&r.Details); err != nil {
		return r, err
	}
	return r, nil
}

type Filter struct {
	TargetType string
	TargetID   string
	ActorID    *uuid.UUID
	Outcome    string
	Action     string // prefix match
	Severity   string
	IP         string
	From, To   *time.Time
	BeforeSeq  int64
	Ascending  bool
	Limit      int
}

func List(ctx context.Context, pool *pgxpool.Pool, f Filter) ([]Record, error) {
	order := "DESC"
	if f.Ascending {
		order = "ASC"
	}
	rows, err := pool.Query(ctx, `SELECT `+selectCols+` FROM audit_log
		WHERE ($1 = '' OR target_type = $1) AND ($2 = '' OR target_id = $2)
		  AND ($3::uuid IS NULL OR actor_id = $3) AND ($4 = '' OR outcome = $4)
		  AND ($5 = 0 OR seq < $5) AND ($7 = '' OR action LIKE $7 || '%') AND ($8 = '' OR severity = $8)
		  AND ($9 = '' OR ip = $9) AND ($10::timestamptz IS NULL OR created_at >= $10)
		  AND ($11::timestamptz IS NULL OR created_at < $11)
		ORDER BY seq `+order+` LIMIT $6`, f.TargetType, f.TargetID, f.ActorID, f.Outcome, f.BeforeSeq, f.Limit,
		f.Action, f.Severity, f.IP, f.From, f.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListTx returns the records up to and including seq, oldest first (inside tx, for archiving).
func ListTx(ctx context.Context, tx pgx.Tx, upToSeq int64) ([]Record, error) {
	rows, err := tx.Query(ctx, `SELECT `+selectCols+` FROM audit_log WHERE seq <= $1 ORDER BY seq`, upToSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type VerifyResult struct {
	Checked   int64  `json:"checked"`
	Valid     bool   `json:"valid"`
	BrokenSeq int64  `json:"broken_seq,omitempty"`
	Problem   string `json:"problem,omitempty"`
	// RowHash is the stored hash of the broken record (what an acknowledgement pins).
	RowHash string `json:"row_hash,omitempty"`
	// DevKey: the broken record verifies with the built-in development key, i.e. a process wrote it without
	// the configured AUDIT_KEY — a configuration fault, not an edit of the record.
	DevKey bool `json:"dev_key,omitempty"`
	// Acknowledged counts breaks the security auditor examined and accepted (audit_acknowledged_breaks).
	Acknowledged int `json:"acknowledged,omitempty"`
}

const devKey = "dev-only-audit-key-change-me-0123456789"

// verifiesWithDevKey reports whether r was signed with the development key while a real key is configured.
func verifiesWithDevKey(r Record) bool {
	cfgMu.RLock()
	configured := len(key) > 0 && string(key) != devKey
	cfgMu.RUnlock()
	if !configured || r.HashAlg != algHMAC {
		return false
	}
	h, err := hashWith(r, []byte(devKey))
	return err == nil && h == r.Hash
}

// Verify walks the whole chain (from the last archive anchor, if any) and recomputes every hash.
func Verify(ctx context.Context, pool *pgxpool.Pool) (VerifyResult, error) {
	prev := genesisHash
	var anchorSeq int64
	err := pool.QueryRow(ctx, `SELECT last_hash, last_seq FROM audit_archives ORDER BY id DESC LIMIT 1`).Scan(&prev, &anchorSeq)
	if err != nil && err != pgx.ErrNoRows {
		return VerifyResult{}, err
	}
	type ack struct{ problem, hash string }
	acks := map[int64]ack{}
	ar, err := pool.Query(ctx, `SELECT seq, problem, row_hash FROM audit_acknowledged_breaks`)
	if err != nil {
		return VerifyResult{}, err
	}
	for ar.Next() {
		var seq int64
		var a ack
		if err := ar.Scan(&seq, &a.problem, &a.hash); err != nil {
			ar.Close()
			return VerifyResult{}, err
		}
		acks[seq] = a
	}
	ar.Close()
	res := VerifyResult{Valid: true}
	// accepted: the record is an acknowledged break with exactly this problem and unchanged since.
	accepted := func(r Record, problem string) bool {
		a, ok := acks[r.Seq]
		if ok && a.problem == problem && a.hash == r.Hash {
			res.Acknowledged++
			return true
		}
		return false
	}
	broken := func(r Record, problem string) VerifyResult {
		return VerifyResult{Checked: res.Checked, BrokenSeq: r.Seq, Problem: problem, RowHash: r.Hash,
			DevKey: problem == "hash_mismatch" && verifiesWithDevKey(r), Acknowledged: res.Acknowledged}
	}
	keyed := false
	for {
		rows, err := pool.Query(ctx, `SELECT `+selectCols+` FROM audit_log WHERE seq > $1 ORDER BY seq LIMIT 5000`, anchorSeq)
		if err != nil {
			return VerifyResult{}, err
		}
		n := 0
		for rows.Next() {
			r, err := scan(rows)
			if err != nil {
				rows.Close()
				return res, err
			}
			n++
			res.Checked++
			anchorSeq = r.Seq
			if r.PrevHash != prev && !accepted(r, "prev_hash_mismatch") {
				rows.Close()
				return broken(r, "prev_hash_mismatch"), nil
			}
			// Unkeyed rows only exist before the upgrade; one after a keyed row is a forged downgrade.
			if r.HashAlg == algHMAC {
				keyed = true
			} else if keyed && !accepted(r, "unkeyed_after_keyed") {
				rows.Close()
				return broken(r, "unkeyed_after_keyed"), nil
			}
			h, err := computeHash(r)
			if err != nil {
				rows.Close()
				return res, err
			}
			if h != r.Hash && !accepted(r, "hash_mismatch") {
				rows.Close()
				return broken(r, "hash_mismatch"), nil
			}
			prev = r.Hash
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, err
		}
		if n == 0 {
			return res, nil
		}
	}
}
