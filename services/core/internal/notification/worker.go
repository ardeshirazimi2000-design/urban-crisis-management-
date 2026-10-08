package notification

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/alert"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/outbox"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
)

type Worker struct {
	Pool        *pgxpool.Pool
	Adapters    map[string]Adapter
	MaxAttempts int
	SendTimeout time.Duration
	BatchSize   int
	// InFlightTimeout: attempts stuck in_flight (worker crashed mid-send) become unknown and are reconciled.
	InFlightTimeout time.Duration
	ReconcileAfter  time.Duration
}

func (w *Worker) defaults() {
	if w.MaxAttempts == 0 {
		w.MaxAttempts = 5
	}
	if w.SendTimeout == 0 {
		w.SendTimeout = 10 * time.Second
	}
	if w.BatchSize == 0 {
		w.BatchSize = 20
	}
	if w.InFlightTimeout == 0 {
		w.InFlightTimeout = 2 * time.Minute
	}
	if w.ReconcileAfter == 0 {
		w.ReconcileAfter = 15 * time.Second
	}
}

func (w *Worker) Run(ctx context.Context) error {
	w.defaults()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	sweep := time.NewTicker(30 * time.Second)
	defer sweep.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("notification iteration failed", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-sweep.C:
			if n, err := alert.ExpireDue(ctx, w.Pool); err != nil {
				slog.Error("expire sweep failed", "err", err.Error())
			} else if n > 0 {
				slog.Info("alerts expired", "count", n)
			}
		case <-tick.C:
		}
	}
}

type claimed struct {
	id        uuid.UUID
	alertID   uuid.UUID
	channel   string
	attemptNo int
	status    string // pending | unknown
	mode      string
	severity  string
	text      string
}

func clientRef(alertID uuid.UUID, channel string, attemptNo int) string {
	return fmt.Sprintf("%s:%s:%d", alertID, channel, attemptNo)
}

// RunOnce processes due pending attempts (send) and unknown attempts (reconcile). Returns attempts handled.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	w.defaults()
	// Recover attempts orphaned by a crash between claim and result.
	if _, err := w.Pool.Exec(ctx, `UPDATE notification_attempts SET status='unknown', detail='worker lost during send',
		next_attempt_at=now(), updated_at=now() WHERE status='in_flight' AND attempted_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(w.InFlightTimeout.Seconds()))); err != nil {
		return 0, err
	}
	var batch []claimed
	err := db.WithTx(ctx, w.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT na.id, na.alert_id, na.channel, na.attempt_no, na.status, a.mode, a.severity, a.rendered_text
			FROM notification_attempts na JOIN alerts a ON a.id = na.alert_id
			WHERE na.status IN ('pending','unknown') AND na.next_attempt_at <= now()
			ORDER BY na.next_attempt_at LIMIT $1 FOR UPDATE OF na SKIP LOCKED`, w.BatchSize)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c claimed
			if err := rows.Scan(&c.id, &c.alertID, &c.channel, &c.attemptNo, &c.status, &c.mode, &c.severity, &c.text); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, c)
		}
		rows.Close()
		for _, c := range batch {
			if c.status == "pending" {
				if _, err := tx.Exec(ctx, `UPDATE notification_attempts SET status='in_flight', attempted_at=now(), updated_at=now() WHERE id=$1`, c.id); err != nil {
					return err
				}
			} else { // unknown: push next reconciliation out so concurrent workers don't double-query
				if _, err := tx.Exec(ctx, `UPDATE notification_attempts SET next_attempt_at=now() + $2::interval, updated_at=now() WHERE id=$1`,
					c.id, fmt.Sprintf("%d seconds", int(w.ReconcileAfter.Seconds()))); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, c := range batch {
		if err := w.handle(ctx, c); err != nil {
			slog.Error("notification attempt handling failed", "attempt_id", c.id.String(), "err", err.Error())
		}
	}
	return len(batch), nil
}

func (w *Worker) handle(ctx context.Context, c claimed) error {
	ad, ok := w.Adapters[c.channel]
	ref := clientRef(c.alertID, c.channel, c.attemptNo)
	var res Result
	switch {
	case !ok:
		res = Result{Outcome: Rejected, Code: "NO_ADAPTER", Detail: ErrNoAdapter.Error()}
	case c.status == "pending":
		sctx, cancel := context.WithTimeout(ctx, w.SendTimeout)
		res = ad.Send(sctx, Message{AlertID: c.alertID.String(), Channel: c.channel, ClientRef: ref, Mode: c.mode,
			Severity: c.severity, Text: c.text})
		cancel()
	default:
		sctx, cancel := context.WithTimeout(ctx, w.SendTimeout)
		res = ad.Query(sctx, ref)
		cancel()
	}
	return db.WithTx(ctx, w.Pool, func(tx pgx.Tx) error { return w.record(ctx, tx, c, res) })
}

func (w *Worker) record(ctx context.Context, tx pgx.Tx, c claimed, res Result) error {
	var status string
	retry := false
	switch res.Outcome {
	case Accepted:
		status = "accepted"
	case Delivered:
		status = "delivered"
	case Rejected:
		status = "rejected"
	case Unknown:
		status = "unknown"
	case Retryable:
		status, retry = "failed", true
	case NotFound:
		// Reconciliation proved the provider never received it: safe to retry with a new attempt.
		status, retry = "failed", true
		res.Detail = "reconciled: provider has no record"
	default:
		status = "unknown"
	}
	var alertStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM alerts WHERE id=$1`, c.alertID).Scan(&alertStatus); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE notification_attempts SET status=$2, provider_message_id=COALESCE(NULLIF($3,''), provider_message_id),
		result_code=$4, detail=$5, updated_at=now(), next_attempt_at = CASE WHEN $2='unknown' THEN now() + $6::interval ELSE next_attempt_at END
		WHERE id=$1`, c.id, status, res.ProviderMessageID, res.Code, res.Detail,
		fmt.Sprintf("%d seconds", int(w.ReconcileAfter.Seconds()))); err != nil {
		return err
	}
	if retry && c.attemptNo < w.MaxAttempts && alertStatus == alert.Sending {
		delay := outbox.Backoff(c.attemptNo)
		if _, err := tx.Exec(ctx, `INSERT INTO notification_attempts (alert_id, channel, attempt_no, next_attempt_at)
			VALUES ($1,$2,$3, now() + $4::interval) ON CONFLICT DO NOTHING`,
			c.alertID, c.channel, c.attemptNo+1, fmt.Sprintf("%d milliseconds", delay.Milliseconds())); err != nil {
			return err
		}
	}
	if _, err := outbox.Enqueue(ctx, tx, outbox.Event{Type: "notification.status_changed",
		Aggregate: outbox.Aggregate{Type: "alert", ID: c.alertID},
		Payload: map[string]any{"alert_id": c.alertID, "channel": c.channel, "attempt_no": c.attemptNo, "status": status,
			"result_code": res.Code}}); err != nil {
		return err
	}
	_, err := alert.RecomputeStatus(ctx, tx, c.alertID)
	return err
}
