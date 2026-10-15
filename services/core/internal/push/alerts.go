package push

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/apiir"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/notification"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
)

// AlertAdapter delivers a dispatched public alert to the app users last seen inside the alert area, by push
// (Channel "push") or by SMS through api.ir (Channel "sms"). Recipients already reached are recorded in
// alert_recipients, so a retried attempt never sends the same alert to a person twice.
//
// Test-mode alerts go only to users whose mobile is listed in TestMobiles, never to the public.
type AlertAdapter struct {
	Pool          *pgxpool.Pool
	Name          string // push | sms
	SMS           apiir.Client
	MaxRecipients int // per attempt; 0 = 5000
	TestMobiles   []string
}

const smsBatch = 100

func (a *AlertAdapter) Channel() string { return a.Name }

type recipient struct {
	userID uuid.UUID
	mobile string
	topic  string
}

func (a *AlertAdapter) recipients(ctx context.Context, alertID uuid.UUID, operational bool) ([]recipient, error) {
	limit := a.MaxRecipients
	if limit <= 0 {
		limit = 5000
	}
	need := `pt.topic IS NOT NULL`
	if a.Name == "sms" {
		need = `u.mobile IS NOT NULL AND u.mobile <> ''`
	}
	test := a.TestMobiles
	if test == nil {
		test = []string{}
	}
	rows, err := a.Pool.Query(ctx, `SELECT u.id, COALESCE(u.mobile, ''), COALESCE(pt.topic, '')
		FROM alerts al
		JOIN user_locations ul ON ST_Covers(al.region_geom, ul.location)
		JOIN users u ON u.id = ul.user_id AND u.status = 'active'
		LEFT JOIN push_topics pt ON pt.user_id = u.id
		WHERE al.id = $1 AND ul.updated_at > now() - interval '72 hours' AND `+need+`
		  AND ($3 OR u.mobile = ANY($4))
		  AND NOT EXISTS (SELECT 1 FROM alert_recipients r WHERE r.alert_id = al.id AND r.channel = $2 AND r.user_id = u.id)
		ORDER BY ul.updated_at DESC LIMIT $5`, alertID, a.Name, operational, test, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (recipient, error) {
		var x recipient
		err := r.Scan(&x.userID, &x.mobile, &x.topic)
		return x, err
	})
}

func (a *AlertAdapter) Send(ctx context.Context, m notification.Message) notification.Result {
	alertID, err := uuid.Parse(m.AlertID)
	if err != nil {
		return notification.Result{Outcome: notification.Rejected, Code: "BAD_ALERT_ID", Detail: err.Error()}
	}
	list, err := a.recipients(ctx, alertID, m.Mode == "operational")
	if err != nil {
		return notification.Result{Outcome: notification.Retryable, Code: "DB", Detail: err.Error()}
	}
	text := m.Text
	if m.Mode == "test" && !strings.Contains(text, "آزمایش") {
		text = "[آزمایشی] " + text
	}
	if a.Name == "push" {
		err := db.WithTx(ctx, a.Pool, func(tx pgx.Tx) error {
			for _, r := range list {
				if _, err := tx.Exec(ctx, `INSERT INTO push_messages (topic, title, body, kind, ref) VALUES ($1, $2, $3, 'alert', $4)`,
					r.topic, alertTitle(m.Severity), text, m.AlertID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `SELECT pg_notify('push', $1)`, r.topic); err != nil {
					return err
				}
				if err := markSent(ctx, tx, alertID, a.Name, r.userID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return notification.Result{Outcome: notification.Retryable, Code: "DB", Detail: err.Error()}
		}
		return notification.Result{Outcome: notification.Accepted, ProviderMessageID: m.ClientRef, Code: fmt.Sprintf("PUSH_%d", len(list)),
			Detail: fmt.Sprintf("%d دستگاه در محدوده", len(list))}
	}

	if a.SMS == nil {
		return notification.Result{Outcome: notification.Rejected, Code: "NO_SMS_PROVIDER"}
	}
	sent := 0
	for chunk := range slices.Chunk(list, smsBatch) {
		mobiles := make([]string, len(chunk))
		for i, r := range chunk {
			mobiles[i] = r.mobile
		}
		if err := a.SMS.SendSMS(ctx, mobiles, "همراه پاد — "+text); err != nil {
			// Those already sent are recorded; the retry continues with the rest.
			return notification.Result{Outcome: notification.Retryable, Code: "SMS_ERROR",
				Detail: fmt.Sprintf("%d of %d sent: %s", sent, len(list), err.Error())}
		}
		err := db.WithTx(ctx, a.Pool, func(tx pgx.Tx) error {
			for _, r := range chunk {
				if err := markSent(ctx, tx, alertID, a.Name, r.userID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return notification.Result{Outcome: notification.Unknown, Code: "DB", Detail: err.Error()}
		}
		sent += len(chunk)
	}
	return notification.Result{Outcome: notification.Accepted, ProviderMessageID: m.ClientRef, Code: fmt.Sprintf("SMS_%d", sent),
		Detail: fmt.Sprintf("%d پیامک به شماره‌های داخل محدوده", sent)}
}

// Query reconciles an attempt: once anybody is recorded as reached the attempt counts as accepted; otherwise
// nothing went out and the worker may retry safely.
func (a *AlertAdapter) Query(ctx context.Context, ref string) notification.Result {
	id, err := uuid.Parse(strings.SplitN(ref, ":", 2)[0])
	if err != nil {
		return notification.Result{Outcome: notification.Rejected, Code: "BAD_REF"}
	}
	var n int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM alert_recipients WHERE alert_id=$1 AND channel=$2`, id, a.Name).Scan(&n); err != nil {
		return notification.Result{Outcome: notification.Unknown, Code: "DB", Detail: err.Error()}
	}
	if n > 0 {
		return notification.Result{Outcome: notification.Accepted, ProviderMessageID: ref, Code: fmt.Sprintf("REACHED_%d", n)}
	}
	return notification.Result{Outcome: notification.NotFound}
}

func markSent(ctx context.Context, tx pgx.Tx, alertID uuid.UUID, channel string, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO alert_recipients (alert_id, channel, user_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		alertID, channel, userID)
	return err
}

func alertTitle(severity string) string {
	switch severity {
	case "emergency":
		return "هشدار فوری"
	case "warning":
		return "هشدار"
	default:
		return "اطلاع‌رسانی"
	}
}
