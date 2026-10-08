// Package audit writes the append-only, hash-chained audit log.
// Each row's hash covers its content and the previous row's hash, so any edit or deletion
// (already blocked by a trigger) is detectable by Verify.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func computeHash(r Record) (string, error) {
	details, err := canonicalJSON(r.Details)
	if err != nil {
		return "", err
	}
	actor := ""
	if r.ActorID != nil {
		actor = r.ActorID.String()
	}
	roles, _ := json.Marshal(r.ActorRoles)
	h := sha256.New()
	for _, part := range []string{
		r.PrevHash, r.ID.String(), actor, string(roles), r.Action, r.TargetType, r.TargetID, r.Outcome,
		r.Reason, string(details), r.CreatedAt.UTC().Format(time.RFC3339Nano), r.CorrelationID,
	} {
		h.Write([]byte(part))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Write appends an entry inside the caller's transaction (so the audit row commits atomically with the change).
func Write(ctx context.Context, tx pgx.Tx, e Entry) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, chainLock); err != nil {
		return fmt.Errorf("audit lock: %w", err)
	}
	prev := genesisHash
	err := tx.QueryRow(ctx, `SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("audit prev hash: %w", err)
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	if e.ActorRoles == nil {
		e.ActorRoles = []string{}
	}
	rec := Record{
		ID: uuid.New(), ActorID: e.ActorID, ActorRoles: e.ActorRoles, Action: e.Action, TargetType: e.TargetType,
		TargetID: e.TargetID, Outcome: e.Outcome, Reason: e.Reason, Details: e.Details,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond), CorrelationID: e.CorrelationID, PrevHash: prev,
	}
	details, err := canonicalJSON(rec.Details)
	if err != nil {
		return err
	}
	if rec.Hash, err = computeHash(rec); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (id, actor_id, actor_roles, action, target_type, target_id, outcome,
		reason, details, created_at, correlation_id, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		rec.ID, rec.ActorID, rec.ActorRoles, rec.Action, rec.TargetType, rec.TargetID, rec.Outcome, rec.Reason,
		details, rec.CreatedAt, rec.CorrelationID, rec.PrevHash, rec.Hash)
	if err != nil {
		return fmt.Errorf("audit insert: %w", err)
	}
	return nil
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
	created_at, correlation_id, prev_hash, hash`

func scan(row pgx.Row) (Record, error) {
	var r Record
	var details string
	if err := row.Scan(&r.Seq, &r.ID, &r.ActorID, &r.ActorRoles, &r.Action, &r.TargetType, &r.TargetID, &r.Outcome,
		&r.Reason, &details, &r.CreatedAt, &r.CorrelationID, &r.PrevHash, &r.Hash); err != nil {
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
	BeforeSeq  int64
	Limit      int
}

func List(ctx context.Context, pool *pgxpool.Pool, f Filter) ([]Record, error) {
	rows, err := pool.Query(ctx, `SELECT `+selectCols+` FROM audit_log
		WHERE ($1 = '' OR target_type = $1) AND ($2 = '' OR target_id = $2)
		  AND ($3::uuid IS NULL OR actor_id = $3) AND ($4 = '' OR outcome = $4)
		  AND ($5 = 0 OR seq < $5)
		ORDER BY seq DESC LIMIT $6`, f.TargetType, f.TargetID, f.ActorID, f.Outcome, f.BeforeSeq, f.Limit)
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
}

// Verify walks the whole chain and recomputes every hash.
func Verify(ctx context.Context, pool *pgxpool.Pool) (VerifyResult, error) {
	rows, err := pool.Query(ctx, `SELECT `+selectCols+` FROM audit_log ORDER BY seq`)
	if err != nil {
		return VerifyResult{}, err
	}
	defer rows.Close()
	res := VerifyResult{Valid: true}
	prev := genesisHash
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return res, err
		}
		res.Checked++
		if r.PrevHash != prev {
			return VerifyResult{Checked: res.Checked, BrokenSeq: r.Seq, Problem: "prev_hash_mismatch"}, nil
		}
		h, err := computeHash(r)
		if err != nil {
			return res, err
		}
		if h != r.Hash {
			return VerifyResult{Checked: res.Checked, BrokenSeq: r.Seq, Problem: "hash_mismatch"}, nil
		}
		prev = r.Hash
	}
	return res, rows.Err()
}
