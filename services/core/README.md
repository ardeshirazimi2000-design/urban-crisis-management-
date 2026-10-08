# core service

Go modular monolith. Layout:

```
cmd/api        REST API (+ optional in-process relay/notifier: RUN_RELAY, RUN_NOTIFIER)
cmd/relay      outbox → Kafka publisher
cmd/notifier   alert delivery, reconciliation of unknown states, expiry sweeper
cmd/consumer   applies report.scored.v1 (inbox, DLQ)
cmd/migrate    embedded migrations (-seed loads local demo data)
internal/      auth, audit, outbox, inbox, idempotency, report, media, incident, resource, alert, notification, gis, admin
migrations/    SQL (expand/contract only)
```

Configuration is environment-based (`internal/platform/config`). Production refuses `AUTH_MODE=dev` and
`MEDIA_ALLOW_UNSCANNED`. Tests: `go test ./...`; DB-backed acceptance tests need `TEST_DATABASE_URL` (reset on start).
