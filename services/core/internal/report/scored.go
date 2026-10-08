package report

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/inbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
)

// ScoredPayload is report.scored.v1 produced by the AI assist service. It is a non-authoritative signal.
type ScoredPayload struct {
	ReportID       uuid.UUID       `json:"report_id"`
	ModelVersion   string          `json:"model_version"`
	PredictedType  *string         `json:"predicted_type"`
	TypeConfidence *float64        `json:"type_confidence"`
	UrgencySignal  *float64        `json:"urgency_signal"`
	Signals        json.RawMessage `json:"signals"`
	Status         string          `json:"status"` // ok | failed
}

const ScoredConsumer = "core.report-enrichment"

// HandleScored stores an AI score exactly once and moves the report from received to triage.
// It never changes review decisions: AI cannot accept, reject or escalate a report (ADR-004).
func HandleScored(pool *pgxpool.Pool) inbox.Handler {
	return func(ctx context.Context, env outbox.Envelope) error {
		if env.EventType != "report.scored" || env.SchemaVersion != 1 {
			return inbox.PermanentError{Err: fmt.Errorf("unsupported event %s v%d", env.EventType, env.SchemaVersion)}
		}
		var p ScoredPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.ReportID == uuid.Nil || p.ModelVersion == "" {
			return inbox.PermanentError{Err: fmt.Errorf("invalid report.scored payload")}
		}
		if len(p.Signals) == 0 {
			p.Signals = json.RawMessage(`{}`)
		}
		_, err := inbox.Process(ctx, pool, ScoredConsumer, env.EventID, func(tx pgx.Tx) error {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reports WHERE id=$1)`, p.ReportID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return inbox.PermanentError{Err: fmt.Errorf("unknown report %s", p.ReportID)}
			}
			if p.Status == "failed" {
				_, err := tx.Exec(ctx, `UPDATE reports SET enrichment_status='failed' WHERE id=$1 AND enrichment_status='pending'`, p.ReportID)
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO report_ai_scores (report_id, model_version, predicted_type, type_confidence,
				urgency_signal, signals, event_id) VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (report_id, model_version) DO NOTHING`,
				p.ReportID, p.ModelVersion, p.PredictedType, p.TypeConfidence, p.UrgencySignal, p.Signals, env.EventID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE reports SET enrichment_status='done',
				status = CASE WHEN status='received' THEN 'triage' ELSE status END,
				version = CASE WHEN status='received' THEN version+1 ELSE version END WHERE id=$1`, p.ReportID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO report_events (report_id, event_type, payload, correlation_id)
				VALUES ($1,'ai_scored',$2,$3)`, p.ReportID, map[string]any{"model_version": p.ModelVersion}, env.CorrelationID)
			return err
		})
		return err
	}
}
