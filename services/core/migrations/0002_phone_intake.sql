-- 0002_phone_intake: reports taken by operators over the phone.
-- Caller details are personal data: kept apart from reports, readable only with report:read_precise,
-- never copied into events or logs. Retention follows the approved policy (decision D-04).
CREATE TABLE report_contacts (
    report_id           uuid PRIMARY KEY REFERENCES reports(id),
    caller_name         text,
    caller_phone        text,
    callback_requested  boolean NOT NULL DEFAULT false,
    address_text        text,
    recorded_by         uuid NOT NULL REFERENCES users(id),
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reports_source_idx ON reports (source, received_at DESC);
