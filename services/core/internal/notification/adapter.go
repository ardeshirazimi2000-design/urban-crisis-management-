// Package notification delivers dispatched alerts through channel adapters, recording every attempt.
// Rules (doc §11, runbook 17.3): provider acceptance != user receipt; a timeout yields "unknown" which is
// reconciled by querying the provider — never blindly re-sent; retries use exponential backoff.
package notification

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

type Message struct {
	AlertID   string
	Channel   string
	ClientRef string // stable per (alert, channel, attempt): lets providers de-duplicate retries
	Mode      string // test | operational
	Severity  string
	Text      string
	RegionWKT string
}

type Outcome string

const (
	Accepted  Outcome = "accepted"  // provider took responsibility; not proof of receipt
	Delivered Outcome = "delivered" // provider reports delivery (where supported)
	Rejected  Outcome = "rejected"  // permanent: do not retry (policy, contract, validation)
	Retryable Outcome = "retryable" // transient failure: provider definitely did not accept
	Unknown   Outcome = "unknown"   // timeout / ambiguous: must be reconciled
	NotFound  Outcome = "not_found" // reconciliation: provider never saw the message, safe to retry
)

type Result struct {
	Outcome           Outcome
	ProviderMessageID string
	Code              string
	Detail            string
}

type Adapter interface {
	Channel() string
	Send(ctx context.Context, m Message) Result
	// Query reconciles an earlier attempt by client reference.
	Query(ctx context.Context, clientRef string) Result
}

// ---------------------------------------------------------------------------
// Internal channel: operator dashboards / inter-agency feed. Persisted by the platform itself.
// ---------------------------------------------------------------------------

type InternalAdapter struct{}

func (InternalAdapter) Channel() string { return "internal" }
func (InternalAdapter) Send(_ context.Context, m Message) Result {
	return Result{Outcome: Delivered, ProviderMessageID: "internal-" + m.ClientRef, Code: "OK"}
}
func (InternalAdapter) Query(_ context.Context, ref string) Result {
	return Result{Outcome: Delivered, ProviderMessageID: "internal-" + ref, Code: "OK"}
}

// ---------------------------------------------------------------------------
// Sandbox provider: simulates a real SMS/Push gateway (idempotent by client ref, configurable faults).
// Real providers are enabled only after contract, permit and controlled tests (production gate).
// ---------------------------------------------------------------------------

type SandboxAdapter struct {
	Name        string
	FailRate    float64 // probability of a transient failure
	TimeoutRate float64 // probability of an ambiguous timeout (message may or may not be accepted)
	Latency     time.Duration

	mu   sync.Mutex
	seen map[string]string // clientRef -> provider message id
}

func NewSandbox(name string, failRate, timeoutRate float64) *SandboxAdapter {
	return &SandboxAdapter{Name: name, FailRate: failRate, TimeoutRate: timeoutRate, seen: map[string]string{}}
}

func (s *SandboxAdapter) Channel() string { return s.Name }

func (s *SandboxAdapter) Send(ctx context.Context, m Message) Result {
	if s.Latency > 0 {
		select {
		case <-ctx.Done():
			return Result{Outcome: Unknown, Code: "CTX_DONE"}
		case <-time.After(s.Latency):
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.seen[m.ClientRef]; ok { // provider-side de-duplication
		return Result{Outcome: Accepted, ProviderMessageID: id, Code: "DUPLICATE_ACCEPTED"}
	}
	r := rand.Float64()
	switch {
	case r < s.FailRate:
		return Result{Outcome: Retryable, Code: "PROVIDER_503", Detail: "sandbox transient failure"}
	case r < s.FailRate+s.TimeoutRate:
		// Ambiguous: half of simulated timeouts were actually accepted by the provider.
		if rand.Float64() < 0.5 {
			s.seen[m.ClientRef] = fmt.Sprintf("%s-%d", s.Name, time.Now().UnixNano())
		}
		return Result{Outcome: Unknown, Code: "TIMEOUT", Detail: "no response from provider"}
	}
	id := fmt.Sprintf("%s-%d", s.Name, time.Now().UnixNano())
	s.seen[m.ClientRef] = id
	return Result{Outcome: Accepted, ProviderMessageID: id, Code: "ACCEPTED"}
}

func (s *SandboxAdapter) Query(_ context.Context, ref string) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.seen[ref]; ok {
		return Result{Outcome: Accepted, ProviderMessageID: id, Code: "FOUND"}
	}
	return Result{Outcome: NotFound, Code: "NOT_FOUND"}
}

// ---------------------------------------------------------------------------
// Cell Broadcast: requires operator contract and legal permit (D-05). Always rejects in the MVP.
// ---------------------------------------------------------------------------

type CellBroadcastAdapter struct{}

func (CellBroadcastAdapter) Channel() string { return "cell_broadcast" }
func (CellBroadcastAdapter) Send(context.Context, Message) Result {
	return Result{Outcome: Rejected, Code: "NOT_CONTRACTED", Detail: "Cell Broadcast requires operator contract and permit"}
}
func (CellBroadcastAdapter) Query(context.Context, string) Result {
	return Result{Outcome: NotFound, Code: "NOT_CONTRACTED"}
}

var ErrNoAdapter = errors.New("no adapter for channel")
