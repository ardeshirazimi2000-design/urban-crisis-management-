-- 0008_afta_identity: native identity (Shahkar + SMS OTP), sessions and security management required by the
-- AFTA protection profile for network-based applications (v1.1). Additive: OIDC/dev users keep working.

-- ---------------------------------------------------------------------------
-- Users: identity attributes, credentials and authentication-failure state
-- ---------------------------------------------------------------------------
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check CHECK (status IN ('active','suspended','disabled'));
ALTER TABLE users
    ADD COLUMN kind                 text NOT NULL DEFAULT 'citizen' CHECK (kind IN ('staff','citizen')),
    ADD COLUMN national_code        text UNIQUE,
    ADD COLUMN mobile               text,
    ADD COLUMN first_name           text NOT NULL DEFAULT '',
    ADD COLUMN last_name            text NOT NULL DEFAULT '',
    ADD COLUMN position_title       text NOT NULL DEFAULT '',
    ADD COLUMN username             text UNIQUE,
    ADD COLUMN password_hash        text,
    ADD COLUMN password_changed_at  timestamptz,
    ADD COLUMN must_change_password boolean NOT NULL DEFAULT false,
    ADD COLUMN console_access       boolean NOT NULL DEFAULT false,
    ADD COLUMN app_access           boolean NOT NULL DEFAULT false,
    ADD COLUMN shahkar_verified_at  timestamptz,
    ADD COLUMN app_enrolled_at      timestamptz,
    ADD COLUMN failed_logins        int NOT NULL DEFAULT 0,
    ADD COLUMN failed_since_success int NOT NULL DEFAULT 0,
    ADD COLUMN lock_count           int NOT NULL DEFAULT 0,
    ADD COLUMN locked_until         timestamptz,
    ADD COLUMN last_login_at        timestamptz,
    ADD COLUMN last_login_ip        text,
    ADD COLUMN last_failed_login_at timestamptz,
    ADD COLUMN allowed_cidrs        text[] NOT NULL DEFAULT '{}',
    ADD COLUMN created_by           uuid REFERENCES users(id),
    ADD COLUMN updated_at           timestamptz NOT NULL DEFAULT now();
CREATE INDEX users_mobile_idx ON users (mobile);

CREATE TABLE password_history (
    id             bigserial PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    password_hash  text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_history_user_idx ON password_history (user_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- One-time-code challenges (SMS OTP via api.ir)
-- ---------------------------------------------------------------------------
CREATE TABLE auth_challenges (
    id             uuid PRIMARY KEY,
    user_id        uuid REFERENCES users(id) ON DELETE CASCADE,
    channel        text NOT NULL CHECK (channel IN ('console','app')),
    purpose        text NOT NULL,
    mobile         text NOT NULL,
    national_code  text,
    device_id      text,
    shahkar_ok     boolean NOT NULL DEFAULT false,
    code_hash      text NOT NULL,
    attempts       int NOT NULL DEFAULT 0,
    ip             text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    consumed_at    timestamptz
);
CREATE INDEX auth_challenges_mobile_idx ON auth_challenges (mobile, created_at DESC);

-- ---------------------------------------------------------------------------
-- Sessions (refresh-token rotation, idle timeout, binding) and registered app devices
-- ---------------------------------------------------------------------------
CREATE TABLE sessions (
    id                  uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel             text NOT NULL CHECK (channel IN ('console','app')),
    refresh_hash        text NOT NULL,
    prev_refresh_hash   text,
    device_id           text,
    ip                  text,
    bound_ip            text,
    user_agent          text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz NOT NULL,
    revoked_at          timestamptz,
    revoked_reason      text
);
CREATE INDEX sessions_user_idx ON sessions (user_id, channel) WHERE revoked_at IS NULL;

CREATE TABLE user_devices (
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id      text NOT NULL,
    secret_hash    text,
    name           text NOT NULL DEFAULT '',
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device_id)
);

CREATE TABLE shahkar_inquiries (
    id             bigserial PRIMARY KEY,
    national_code  text NOT NULL,
    mobile         text NOT NULL,
    matched        boolean,
    provider_code  text,
    message        text,
    context        text NOT NULL,
    user_id        uuid REFERENCES users(id) ON DELETE SET NULL,
    requested_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    duration_ms    int,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX shahkar_inquiries_nc_idx ON shahkar_inquiries (national_code, created_at DESC);

-- ---------------------------------------------------------------------------
-- Security settings managed at run time (FMT_SMF.1 / FMT_MTD.1)
-- ---------------------------------------------------------------------------
CREATE TABLE security_settings (
    key         text PRIMARY KEY,
    value       jsonb NOT NULL,
    updated_by  uuid REFERENCES users(id),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE security_notifications (
    id          bigserial PRIMARY KEY,
    kind        text NOT NULL,
    severity    text NOT NULL,
    message     text NOT NULL,
    details     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    acknowledged_by uuid REFERENCES users(id),
    acknowledged_at timestamptz
);

-- ---------------------------------------------------------------------------
-- Roles: separation of duties between identity administration and audit review
-- ---------------------------------------------------------------------------
INSERT INTO roles (code, description) VALUES
  ('SECURITY_AUDITOR', 'ممیز امنیتی: مشاهده، بررسی صحت، خروجی و پیکربندی ممیزی')
ON CONFLICT (code) DO NOTHING;
UPDATE roles SET description = 'مدیر امنیت: کاربران، نقش‌ها، نشست‌ها، تنظیمات امنیتی و replay' WHERE code = 'SECURITY_ADMIN';

-- ---------------------------------------------------------------------------
-- Audit: keyed hash (HMAC-SHA256) for new rows, severity, archive anchors
-- ---------------------------------------------------------------------------
ALTER TABLE audit_log
    ADD COLUMN hash_alg    text NOT NULL DEFAULT 'sha256',
    ADD COLUMN severity    text NOT NULL DEFAULT 'info',
    ADD COLUMN ip          text NOT NULL DEFAULT '',
    ADD COLUMN channel     text NOT NULL DEFAULT '',
    ADD COLUMN user_agent  text NOT NULL DEFAULT '';
CREATE INDEX audit_log_action_idx ON audit_log (action, created_at);
CREATE INDEX audit_log_created_idx ON audit_log (created_at);

CREATE TABLE audit_archives (
    id          bigserial PRIMARY KEY,
    first_seq   bigint NOT NULL,
    last_seq    bigint NOT NULL,
    last_hash   text NOT NULL,
    row_count   bigint NOT NULL,
    digest      text NOT NULL,
    archived_by uuid REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Rows may only be removed by a recorded archive operation (transaction-local flag set by the application).
CREATE OR REPLACE FUNCTION audit_log_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('crisis.audit_archive', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only';
END $$;
DROP TRIGGER audit_log_append_only ON audit_log;
CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_guard();

-- ---------------------------------------------------------------------------
-- Integrity seals of critical records (FDP_SDI.2)
-- ---------------------------------------------------------------------------
CREATE TABLE record_seals (
    record_type  text NOT NULL,
    record_id    uuid NOT NULL,
    mac          text NOT NULL,
    sealed_at    timestamptz NOT NULL DEFAULT now(),
    ok           boolean NOT NULL DEFAULT true,
    checked_at   timestamptz,
    PRIMARY KEY (record_type, record_id)
);
