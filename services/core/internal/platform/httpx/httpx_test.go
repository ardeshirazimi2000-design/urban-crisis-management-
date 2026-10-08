package httpx

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{T: time.Date(2026, 10, 8, 20, 0, 0, 123456000, time.UTC), ID: uuid.New()}
	got, err := DecodeCursor(c.Encode())
	if err != nil || !got.T.Equal(c.T) || got.ID != c.ID {
		t.Fatalf("round trip failed: %v %v", got, err)
	}
	if _, err := DecodeCursor("!!!"); err == nil {
		t.Fatal("garbage cursor must fail")
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	rl := NewRateLimiter(60, 2) // 1/s, burst 2
	rl.now = func() time.Time { return now }
	if !rl.Allow("a") || !rl.Allow("a") {
		t.Fatal("burst should pass")
	}
	if rl.Allow("a") {
		t.Fatal("third immediate request must be limited")
	}
	if !rl.Allow("b") {
		t.Fatal("keys are independent")
	}
	now = now.Add(1100 * time.Millisecond)
	if !rl.Allow("a") {
		t.Fatal("token should refill")
	}
}
