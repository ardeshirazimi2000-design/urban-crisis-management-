-- 0004_medical: hospital capacity (on the reference "hospital" map features) and casualties with field triage.
-- Casualties carry no names or IDs: only the triage tag number written on the physical tag, an age group and
-- sex, so the record can follow a patient without holding identifying data (identification is decision D-04).

CREATE TABLE hospital_capacity (
    feature_id     uuid PRIMARY KEY REFERENCES gis_features(id),
    beds_total     integer CHECK (beds_total >= 0),
    beds_available integer NOT NULL DEFAULT 0 CHECK (beds_available >= 0),
    icu_available  integer NOT NULL DEFAULT 0 CHECK (icu_available >= 0),
    er_status      text NOT NULL DEFAULT 'open' CHECK (er_status IN ('open','limited','diverting','closed')),
    note           text NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid REFERENCES users(id),
    version        integer NOT NULL DEFAULT 1,
    CHECK (beds_total IS NULL OR beds_available <= beds_total)
);

CREATE TABLE hospital_capacity_log (
    id             bigserial PRIMARY KEY,
    feature_id     uuid NOT NULL REFERENCES gis_features(id),
    beds_total     integer,
    beds_available integer NOT NULL,
    icu_available  integer NOT NULL,
    er_status      text NOT NULL,
    note           text NOT NULL DEFAULT '',
    actor_id       uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hospital_capacity_log_idx ON hospital_capacity_log (feature_id, id DESC);
CREATE TRIGGER hospital_capacity_log_append_only BEFORE UPDATE OR DELETE ON hospital_capacity_log
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE SEQUENCE casualty_tag_seq;

CREATE TABLE casualties (
    id                    uuid PRIMARY KEY,
    tag_no                text NOT NULL UNIQUE,
    incident_id           uuid NOT NULL REFERENCES incidents(id),
    triage                text NOT NULL CHECK (triage IN ('immediate','delayed','minor','deceased')),
    status                text NOT NULL DEFAULT 'on_scene'
                          CHECK (status IN ('on_scene','transported','admitted','released','deceased')),
    age_group             text NOT NULL DEFAULT 'unknown' CHECK (age_group IN ('child','adult','elderly','unknown')),
    sex                   text NOT NULL DEFAULT 'unknown' CHECK (sex IN ('female','male','unknown')),
    location              geography(Point, 4326),
    hospital_id           uuid REFERENCES gis_features(id),
    transport_resource_id uuid REFERENCES resources(id),
    notes                 text NOT NULL DEFAULT '',
    recorded_by           uuid NOT NULL REFERENCES users(id),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    version               integer NOT NULL DEFAULT 1
);
CREATE INDEX casualties_incident_idx ON casualties (incident_id, created_at DESC);
CREATE INDEX casualties_hospital_idx ON casualties (hospital_id, status);

CREATE TABLE casualty_events (
    id           bigserial PRIMARY KEY,
    casualty_id  uuid NOT NULL REFERENCES casualties(id),
    event_type   text NOT NULL,
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    actor_id     uuid NOT NULL REFERENCES users(id),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX casualty_events_idx ON casualty_events (casualty_id, id);
CREATE TRIGGER casualty_events_append_only BEFORE UPDATE OR DELETE ON casualty_events
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
