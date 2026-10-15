-- 0010_push_sms: self-hosted push (long-poll topics), SMS outbox, last known citizen location for alert targeting.

-- One unguessable topic per app user; the app's background service long-polls it without credentials.
CREATE TABLE push_topics (
    user_id      uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    topic        text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_poll_at timestamptz
);

-- Short-lived queue of messages per topic (purged after 48 hours).
CREATE TABLE push_messages (
    id         bigserial PRIMARY KEY,
    topic      text NOT NULL,
    title      text NOT NULL,
    body       text NOT NULL,
    kind       text NOT NULL,          -- alert | assignment
    ref        text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX push_messages_topic_idx ON push_messages (topic, id);
CREATE INDEX push_messages_created_idx ON push_messages (created_at);

-- Outgoing SMS other than OTP (mission notices); sent by a background worker with retries.
CREATE TABLE sms_outbox (
    id              bigserial PRIMARY KEY,
    user_id         uuid REFERENCES users(id) ON DELETE SET NULL,
    mobile          text NOT NULL,
    message         text NOT NULL,
    kind            text NOT NULL,
    ref             text,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','sent','failed')),
    attempts        int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);
CREATE INDEX sms_outbox_due_idx ON sms_outbox (next_attempt_at) WHERE status = 'pending';

-- Approximate last position of app users (rounded to ~100 m, kept 72 hours) so public alerts can reach
-- the people inside the alert area by push and SMS.
CREATE TABLE user_locations (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    location   geography(Point, 4326) NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_locations_gix ON user_locations USING GIST (location);

-- Who an alert already reached on a fan-out channel: retries skip them, so nobody gets the same alert twice.
CREATE TABLE alert_recipients (
    alert_id uuid NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    channel  text NOT NULL,
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sent_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (alert_id, channel, user_id)
);
