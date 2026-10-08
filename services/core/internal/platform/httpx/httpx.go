// Package httpx provides the HTTP conventions shared by every module:
// a fixed error envelope, correlation IDs, strict JSON decoding and cursor pagination.
package httpx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Error is a client-visible error. Internal details never leave the process.
type Error struct {
	Status  int           `json:"-"`
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details []FieldDetail `json:"details,omitempty"`
}

type FieldDetail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func NewError(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Message: msg} }

var (
	ErrUnauthenticated = NewError(http.StatusUnauthorized, "UNAUTHENTICATED", "احراز هویت لازم است")
	ErrForbidden       = NewError(http.StatusForbidden, "FORBIDDEN", "دسترسی مجاز نیست")
	ErrNotFound        = NewError(http.StatusNotFound, "NOT_FOUND", "مورد یافت نشد")
	ErrRateLimited     = NewError(http.StatusTooManyRequests, "RATE_LIMITED", "تعداد درخواست بیش از حد مجاز است")
	ErrVersionConflict = NewError(http.StatusConflict, "VERSION_CONFLICT", "رکورد توسط شخص دیگری تغییر کرده است؛ دوباره بارگذاری کنید")
)

func Validation(details ...FieldDetail) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_ERROR", Message: "درخواست معتبر نیست", Details: details}
}

func Conflict(code, msg string) *Error { return NewError(http.StatusConflict, code, msg) }

// Validator accumulates field errors.
type Validator struct{ details []FieldDetail }

func (v *Validator) Check(ok bool, field, reason string) {
	if !ok {
		v.details = append(v.details, FieldDetail{Field: field, Reason: reason})
	}
}

func (v *Validator) Err() error {
	if len(v.details) == 0 {
		return nil
	}
	return Validation(v.details...)
}

// ---------------------------------------------------------------------------
// Context: correlation id
// ---------------------------------------------------------------------------

type ctxKey int

const correlationKey ctxKey = 1

var correlationRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,64}$`)

func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey, id)
}

func CorrelationID(ctx context.Context) string {
	if v, ok := ctx.Value(correlationKey).(string); ok {
		return v
	}
	return ""
}

// ---------------------------------------------------------------------------
// Handler adapter & responses
// ---------------------------------------------------------------------------

type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Handle adapts an error-returning handler, rendering errors in the standard envelope.
func Handle(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var he *Error
	if !errors.As(err, &he) {
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			he = NewError(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "حجم درخواست بیش از حد مجاز است")
		case errors.Is(err, context.Canceled):
			return
		default:
			slog.ErrorContext(r.Context(), "internal error", "err", err.Error(), "correlation_id", CorrelationID(r.Context()))
			he = NewError(http.StatusInternalServerError, "INTERNAL", "خطای داخلی؛ با شناسه پیگیری تماس بگیرید")
		}
	}
	body := map[string]any{"error": map[string]any{
		"code": he.Code, "message": he.Message, "correlation_id": CorrelationID(r.Context()), "details": he.Details,
	}}
	JSON(w, he.Status, body)
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON strictly decodes a bounded JSON body; unknown fields are rejected.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return NewError(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type باید application/json باشد")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			return Validation(FieldDetail{Field: ute.Field, Reason: "invalid_type"})
		}
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return Validation(FieldDetail{Field: strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`), Reason: "unknown_field"})
		}
		return NewError(http.StatusBadRequest, "MALFORMED_JSON", "بدنه JSON نامعتبر است")
	}
	if dec.More() {
		return NewError(http.StatusBadRequest, "MALFORMED_JSON", "بدنه JSON نامعتبر است")
	}
	return nil
}

func PathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// Cursor pagination (keyset on (time, id) descending)
// ---------------------------------------------------------------------------

type Cursor struct {
	T  time.Time
	ID uuid.UUID
}

func (c Cursor) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.T.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, Validation(FieldDetail{Field: "cursor", Reason: "invalid"})
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return nil, Validation(FieldDetail{Field: "cursor", Reason: "invalid"})
	}
	t, err1 := time.Parse(time.RFC3339Nano, parts[0])
	id, err2 := uuid.Parse(parts[1])
	if err1 != nil || err2 != nil {
		return nil, Validation(FieldDetail{Field: "cursor", Reason: "invalid"})
	}
	return &Cursor{T: t, ID: id}, nil
}

func PageSize(r *http.Request, def, max int) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > max {
		return 0, Validation(FieldDetail{Field: "limit", Reason: fmt.Sprintf("must_be_1_to_%d", max)})
	}
	return n, nil
}

// OneOf validates an optional query parameter against an allowlist.
func OneOf(r *http.Request, name string, allowed ...string) (string, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return "", nil
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", Validation(FieldDetail{Field: name, Reason: "not_allowed"})
}

func NewCorrelationID() string { return uuid.NewString() }
