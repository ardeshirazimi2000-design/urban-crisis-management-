-- 0001_init: core operational schema for the urban crisis platform (MVP).
-- PostgreSQL + PostGIS is the source of truth (ADR-002). All timestamps are UTC (timestamptz).
-- Migrations are additive; destructive changes ship in a separate release (expand/contract).

CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------------------
-- Identity
-- ---------------------------------------------------------------------------
CREATE TABLE organizations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code        text NOT NULL UNIQUE,
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_id    text NOT NULL UNIQUE,          -- OIDC "sub"
    display_name  text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE roles (
    code         text PRIMARY KEY,
    description  text NOT NULL DEFAULT ''
);

-- scope_org_id NULL means "all organizations" and must be granted explicitly.
CREATE TABLE user_roles (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_code     text NOT NULL REFERENCES roles(code),
    scope_org_id  uuid REFERENCES organizations(id),
    granted_by    uuid REFERENCES users(id),
    granted_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz,                   -- break-glass grants are time-boxed
    reason        text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX user_roles_uniq ON user_roles (user_id, role_code, COALESCE(scope_org_id, '00000000-0000-0000-0000-000000000000'::uuid));

INSERT INTO roles (code, description) VALUES
  ('CITIZEN',          'شهروند: ارسال گزارش و دریافت هشدار رسمی'),
  ('RESPONDER',        'امدادگر: دریافت مأموریت و ثبت وضعیت میدانی'),
  ('OPERATOR',         'اپراتور: بررسی، ادغام و ارجاع گزارش'),
  ('COMMANDER',        'فرمانده: تعیین سطح پاسخ و تأیید تصمیم‌های پراثر'),
  ('RESOURCE_MANAGER', 'مدیر منابع: نیرو، خودرو، تجهیزات و پناهگاه'),
  ('GIS_ANALYST',      'کارشناس GIS: لایه‌ها، انسداد و محدوده اثر'),
  ('SECURITY_ADMIN',   'مدیر امنیت: هویت، نقش، ممیزی و replay');

-- ---------------------------------------------------------------------------
-- Reports
-- ---------------------------------------------------------------------------
CREATE TABLE reports (
    id                 uuid PRIMARY KEY,
    reporter_ref       uuid NOT NULL REFERENCES users(id),
    source             text NOT NULL DEFAULT 'citizen_app',
    type               text NOT NULL,
    description        text NOT NULL DEFAULT '',
    severity           text CHECK (severity IN ('low','medium','high','critical')),
    status             text NOT NULL DEFAULT 'received'
                       CHECK (status IN ('received','triage','under_review','accepted','rejected','duplicate','linked_to_incident')),
    occurred_at        timestamptz,              -- claimed by the client; device clocks are not trusted
    received_at        timestamptz NOT NULL DEFAULT now(),
    location           geography(Point, 4326) NOT NULL,
    accuracy_m         double precision NOT NULL,
    location_source    text NOT NULL DEFAULT 'gps',
    enrichment_status  text NOT NULL DEFAULT 'pending' CHECK (enrichment_status IN ('pending','done','failed','skipped')),
    duplicate_of       uuid REFERENCES reports(id),
    reviewer_id        uuid REFERENCES users(id),
    review_reason      text,
    reviewed_at        timestamptz,
    version            integer NOT NULL DEFAULT 1
);
CREATE INDEX reports_location_gix ON reports USING GIST (location);
CREATE INDEX reports_status_received_idx ON reports (status, received_at DESC);
CREATE INDEX reports_reporter_idx ON reports (reporter_ref, received_at DESC);

CREATE TABLE report_media (
    id            uuid PRIMARY KEY,
    report_id     uuid REFERENCES reports(id),
    uploaded_by   uuid NOT NULL REFERENCES users(id),
    object_key    text NOT NULL UNIQUE,          -- never a public URL
    sha256        text NOT NULL,
    content_type  text NOT NULL,
    bytes         bigint NOT NULL CHECK (bytes > 0),
    scan_status   text NOT NULL DEFAULT 'pending' CHECK (scan_status IN ('pending','clean','infected','skipped')),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX report_media_report_idx ON report_media (report_id);

-- AI output is a non-authoritative auxiliary signal (ADR-004); versioned for rollback.
CREATE TABLE report_ai_scores (
    report_id        uuid NOT NULL REFERENCES reports(id),
    model_version    text NOT NULL,
    predicted_type   text,
    type_confidence  double precision,
    urgency_signal   double precision,
    signals          jsonb NOT NULL DEFAULT '{}'::jsonb,
    event_id         uuid NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (report_id, model_version)
);

CREATE TABLE report_events (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id       uuid NOT NULL REFERENCES reports(id),
    event_type      text NOT NULL,
    actor_id        uuid REFERENCES users(id),
    payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    correlation_id  text NOT NULL DEFAULT ''
);
CREATE INDEX report_events_report_idx ON report_events (report_id, created_at);

-- ---------------------------------------------------------------------------
-- Incidents
-- ---------------------------------------------------------------------------
CREATE SEQUENCE incident_code_seq;

CREATE TABLE incidents (
    id              uuid PRIMARY KEY,
    code            text NOT NULL UNIQUE,
    type            text NOT NULL,
    title           text NOT NULL,
    severity        text NOT NULL CHECK (severity IN ('low','medium','high','critical')),
    response_level  text NOT NULL DEFAULT 'L1' CHECK (response_level IN ('L0','L1','L2','L3','L4')),
    status          text NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft','open','active','escalated','contained','resolved','closed')),
    owner_org_id    uuid NOT NULL REFERENCES organizations(id),
    location        geography(Point, 4326),
    created_by      uuid NOT NULL REFERENCES users(id),
    opened_at       timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    closed_at       timestamptz,
    version         integer NOT NULL DEFAULT 1
);
CREATE INDEX incidents_status_updated_idx ON incidents (status, updated_at DESC);
CREATE INDEX incidents_location_gix ON incidents USING GIST (location);
CREATE INDEX incidents_owner_idx ON incidents (owner_org_id);

CREATE TABLE incident_reports (
    incident_id    uuid NOT NULL REFERENCES incidents(id),
    report_id      uuid NOT NULL REFERENCES reports(id),
    relation_type  text NOT NULL DEFAULT 'evidence' CHECK (relation_type IN ('evidence','origin','related')),
    reason         text NOT NULL,
    linked_by      uuid NOT NULL REFERENCES users(id),
    linked_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (incident_id, report_id)
);

CREATE TABLE incident_events (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id     uuid NOT NULL REFERENCES incidents(id),
    event_type      text NOT NULL,
    actor_id        uuid REFERENCES users(id),
    payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    correlation_id  text NOT NULL DEFAULT ''
);
CREATE INDEX incident_events_incident_idx ON incident_events (incident_id, created_at);

-- ---------------------------------------------------------------------------
-- Resources & assignments
-- ---------------------------------------------------------------------------
CREATE TABLE resources (
    id               uuid PRIMARY KEY,
    organization_id  uuid NOT NULL REFERENCES organizations(id),
    type             text NOT NULL CHECK (type IN ('ambulance','fire_truck','rescue_team','police_unit','shelter','equipment','medical_team')),
    name             text NOT NULL,
    status           text NOT NULL DEFAULT 'available'
                     CHECK (status IN ('available','assigned','en_route','on_scene','out_of_service')),
    capabilities     jsonb NOT NULL DEFAULT '{}'::jsonb,
    capacity         integer,
    location         geography(Point, 4326),
    last_seen_at     timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    version          integer NOT NULL DEFAULT 1
);
CREATE INDEX resources_location_gix ON resources USING GIST (location);
CREATE INDEX resources_status_type_idx ON resources (status, type);

CREATE TABLE assignments (
    id            uuid PRIMARY KEY,
    incident_id   uuid NOT NULL REFERENCES incidents(id),
    resource_id   uuid NOT NULL REFERENCES resources(id),
    status        text NOT NULL DEFAULT 'assigned'
                  CHECK (status IN ('proposed','assigned','acknowledged','en_route','on_scene','completed','cancelled')),
    assigned_by   uuid NOT NULL REFERENCES users(id),
    assignee_id   uuid REFERENCES users(id),        -- responder who carries out the mission
    reason        text NOT NULL,
    assigned_at   timestamptz NOT NULL DEFAULT now(),
    released_at   timestamptz,
    version       integer NOT NULL DEFAULT 1
);
-- AT-06: at most one active assignment per resource, enforced by the database as the last line of defence.
CREATE UNIQUE INDEX assignments_one_active_per_resource
    ON assignments (resource_id) WHERE status IN ('proposed','assigned','acknowledged','en_route','on_scene');
CREATE INDEX assignments_incident_idx ON assignments (incident_id);
CREATE INDEX assignments_assignee_idx ON assignments (assignee_id);

-- ---------------------------------------------------------------------------
-- Alerts & notifications
-- ---------------------------------------------------------------------------
CREATE TABLE alerts (
    id                 uuid PRIMARY KEY,
    incident_id        uuid REFERENCES incidents(id),
    mode               text NOT NULL CHECK (mode IN ('test','operational')),
    severity           text NOT NULL CHECK (severity IN ('advisory','watch','warning','emergency')),
    status             text NOT NULL DEFAULT 'draft'
                       CHECK (status IN ('draft','pending_approval','approved','sending','partially_sent','sent','failed','cancelled','expired')),
    issuer_org_id      uuid NOT NULL REFERENCES organizations(id),
    region_geom        geography(MultiPolygon, 4326) NOT NULL,
    region_label       text NOT NULL,
    template_code      text NOT NULL,
    template_version   integer NOT NULL,
    params             jsonb NOT NULL DEFAULT '{}'::jsonb,
    rendered_text      text NOT NULL,
    channels           text[] NOT NULL,
    issued_at          timestamptz,
    expires_at         timestamptz NOT NULL,
    created_by         uuid NOT NULL REFERENCES users(id),
    submitted_by       uuid REFERENCES users(id),
    approved_by        uuid REFERENCES users(id),
    approval_reason    text,
    approved_at        timestamptz,
    cancelled_by       uuid REFERENCES users(id),
    cancel_reason      text,
    idempotency_key    text NOT NULL UNIQUE,
    dispatch_key       text UNIQUE,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    version            integer NOT NULL DEFAULT 1
);
CREATE INDEX alerts_status_idx ON alerts (status, updated_at DESC);
CREATE INDEX alerts_region_gix ON alerts USING GIST (region_geom);

CREATE TABLE notification_attempts (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_id             uuid NOT NULL REFERENCES alerts(id),
    channel              text NOT NULL,
    attempt_no           integer NOT NULL,
    status               text NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending','in_flight','accepted','delivered','rejected','failed','unknown','superseded')),
    provider_message_id  text,
    result_code          text,
    detail               text,
    next_attempt_at      timestamptz NOT NULL DEFAULT now(),
    attempted_at         timestamptz,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (alert_id, channel, attempt_no)
);
CREATE INDEX notification_attempts_alert_idx ON notification_attempts (alert_id, channel, attempted_at);
CREATE INDEX notification_attempts_pending_idx ON notification_attempts (next_attempt_at) WHERE status IN ('pending','unknown');

-- ---------------------------------------------------------------------------
-- GIS reference layers & impact areas
-- ---------------------------------------------------------------------------
CREATE TABLE gis_features (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    layer         text NOT NULL CHECK (layer IN ('hospital','fire_station','shelter','road_closure','hazard','assembly_point')),
    name          text NOT NULL,
    geom          geography(Geometry, 4326) NOT NULL,
    properties    jsonb NOT NULL DEFAULT '{}'::jsonb,
    owner_org_id  uuid REFERENCES organizations(id),
    source        text NOT NULL,
    verified_at   timestamptz,                   -- last confirmation; drives the stale badge
    valid_until   timestamptz,
    created_by    uuid REFERENCES users(id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gis_features_geom_gix ON gis_features USING GIST (geom);
CREATE INDEX gis_features_layer_idx ON gis_features (layer);

CREATE TABLE impact_areas (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id        uuid REFERENCES incidents(id),
    geom               geography(Polygon, 4326) NOT NULL,
    source             text NOT NULL,
    algorithm_version  text NOT NULL,
    uncertainty_m      double precision NOT NULL,
    inputs             jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by         uuid NOT NULL REFERENCES users(id),
    created_at         timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Reliability: idempotency, outbox, inbox
-- ---------------------------------------------------------------------------
CREATE TABLE idempotency_keys (
    principal_id     uuid NOT NULL,
    scope            text NOT NULL,
    key              text NOT NULL,
    request_hash     text NOT NULL,
    response_status  integer,
    response_body    jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, scope, key)
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

CREATE TABLE outbox_events (
    seq               bigserial UNIQUE,      -- commit-independent ordering; per-aggregate order is preserved by the relay
    id                uuid PRIMARY KEY,
    aggregate_type    text NOT NULL,
    aggregate_id      uuid NOT NULL,
    aggregate_version integer,
    event_type        text NOT NULL,
    schema_version    integer NOT NULL,
    payload           jsonb NOT NULL,       -- full envelope as published
    created_at        timestamptz NOT NULL DEFAULT now(),
    published_at      timestamptz,
    attempts          integer NOT NULL DEFAULT 0,
    next_attempt_at   timestamptz NOT NULL DEFAULT now(),
    last_error        text,
    dead_lettered_at  timestamptz
);
CREATE INDEX outbox_unpublished_idx ON outbox_events (next_attempt_at)
    WHERE published_at IS NULL AND dead_lettered_at IS NULL;
CREATE INDEX outbox_pending_aggregate_idx ON outbox_events (aggregate_id, seq)
    WHERE published_at IS NULL AND dead_lettered_at IS NULL;
CREATE INDEX outbox_aggregate_idx ON outbox_events (aggregate_type, aggregate_id, seq);

CREATE TABLE inbox_events (
    consumer      text NOT NULL,
    event_id      uuid NOT NULL,
    processed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

-- ---------------------------------------------------------------------------
-- Audit log: append-only, hash-chained (tamper-evident)
-- ---------------------------------------------------------------------------
CREATE TABLE audit_log (
    seq             bigserial PRIMARY KEY,
    id              uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    actor_id        uuid,
    actor_roles     text[] NOT NULL DEFAULT '{}',
    action          text NOT NULL,
    target_type     text NOT NULL,
    target_id       text NOT NULL DEFAULT '',
    outcome         text NOT NULL CHECK (outcome IN ('success','denied','failed')),
    reason          text NOT NULL DEFAULT '',
    details         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    correlation_id  text NOT NULL DEFAULT '',
    prev_hash       text NOT NULL,
    hash            text NOT NULL
);
CREATE INDEX audit_log_target_idx ON audit_log (target_type, target_id);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, created_at);

CREATE FUNCTION forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END $$;

CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER incident_events_append_only BEFORE UPDATE OR DELETE ON incident_events
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER report_events_append_only BEFORE UPDATE OR DELETE ON report_events
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
