package outbox

import (
	"testing"
	"time"
)

func TestBackoffBounds(t *testing.T) {
	for attempt := 1; attempt <= 20; attempt++ {
		d := Backoff(attempt)
		if d <= 0 || d > 5*time.Minute {
			t.Fatalf("attempt %d: backoff %v out of bounds", attempt, d)
		}
	}
	if Backoff(1) > time.Second {
		t.Fatal("first retry should be quick")
	}
}

func TestTopic(t *testing.T) {
	if got := Topic("crisis.", "report.created", 1); got != "crisis.report.created.v1" {
		t.Fatal(got)
	}
}
