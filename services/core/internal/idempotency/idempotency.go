// Package idempotency implements Idempotency-Key semantics for retryable POSTs (AT-02):
// the same key with the same request returns the original response; the same key with a different
// request is rejected. The key row is written in the same transaction as the domain change.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

var keyRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

// Key extracts and validates the Idempotency-Key header.
func Key(r *http.Request) (string, error) {
	k := r.Header.Get("Idempotency-Key")
	if k == "" {
		return "", httpx.NewError(http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "سرآیند Idempotency-Key الزامی است")
	}
	if !keyRe.MatchString(k) {
		return "", httpx.Validation(httpx.FieldDetail{Field: "Idempotency-Key", Reason: "invalid_format"})
	}
	return k, nil
}

func Hash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Stored is a previously recorded response.
type Stored struct {
	Status int
	Body   json.RawMessage
}

// Begin claims (principal, scope, key) inside tx. If another transaction already completed the same key,
// it returns the stored response (replay != nil). Concurrent duplicates block on the unique index
// until the first transaction commits, then see its result.
func Begin(ctx context.Context, tx pgx.Tx, principal uuid.UUID, scope, key, reqHash string) (replay *Stored, err error) {
	tag, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (principal_id, scope, key, request_hash)
		VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, principal, scope, key, reqHash)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 1 {
		return nil, nil
	}
	var (
		storedHash string
		status     *int
		body       []byte
	)
	if err := tx.QueryRow(ctx, `SELECT request_hash, response_status, response_body FROM idempotency_keys
		WHERE principal_id=$1 AND scope=$2 AND key=$3`, principal, scope, key).Scan(&storedHash, &status, &body); err != nil {
		return nil, err
	}
	if storedHash != reqHash {
		return nil, httpx.NewError(http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
			"این Idempotency-Key قبلاً با درخواست متفاوتی استفاده شده است")
	}
	if status == nil {
		return nil, httpx.Conflict("IDEMPOTENCY_IN_PROGRESS", "درخواست قبلی با همین کلید هنوز در حال پردازش است")
	}
	return &Stored{Status: *status, Body: body}, nil
}

// Complete stores the response for future replays (same transaction as Begin).
func Complete(ctx context.Context, tx pgx.Tx, principal uuid.UUID, scope, key string, status int, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE idempotency_keys SET response_status=$4, response_body=$5
		WHERE principal_id=$1 AND scope=$2 AND key=$3`, principal, scope, key, status, b)
	return err
}

// Write sends a replayed response.
func (s *Stored) Write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(s.Status)
	_, _ = w.Write(s.Body)
}
