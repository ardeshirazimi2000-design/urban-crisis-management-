-- 0003_needs_shelters: open needs (requests for resources/supplies per incident) and shelter occupancy.
-- Quantities change only through append-only records (fulfilments, occupancy log) under optimistic locking,
-- so a retried request can never be counted twice.

CREATE TABLE needs (
    id            uuid PRIMARY KEY,
    incident_id   uuid NOT NULL REFERENCES incidents(id),
    category      text NOT NULL,
    description   text NOT NULL DEFAULT '',
    quantity      integer NOT NULL CHECK (quantity > 0),
    fulfilled     integer NOT NULL DEFAULT 0 CHECK (fulfilled >= 0 AND fulfilled <= quantity),
    unit          text NOT NULL,
    priority      text NOT NULL CHECK (priority IN ('low','medium','high','critical')),
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open','partially_met','met','cancelled')),
    location      geography(Point, 4326),
    requested_by  uuid NOT NULL REFERENCES users(id),
    cancel_reason text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    closed_at     timestamptz,
    version       integer NOT NULL DEFAULT 1
);
CREATE INDEX needs_status_idx ON needs (status, created_at DESC);
CREATE INDEX needs_incident_idx ON needs (incident_id, created_at DESC);

CREATE TABLE need_fulfillments (
    id          uuid PRIMARY KEY,
    need_id     uuid NOT NULL REFERENCES needs(id),
    quantity    integer NOT NULL CHECK (quantity > 0),
    source      text NOT NULL,
    resource_id uuid REFERENCES resources(id),
    note        text NOT NULL DEFAULT '',
    actor_id    uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX need_fulfillments_need_idx ON need_fulfillments (need_id, created_at);
CREATE TRIGGER need_fulfillments_append_only BEFORE UPDATE OR DELETE ON need_fulfillments
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Live occupancy of shelter resources (resources.type = 'shelter'; capacity stays in resources.capacity).
-- A shelter without a row here has occupancy 0, is accepting, and version 0.
CREATE TABLE shelter_occupancy (
    resource_id uuid PRIMARY KEY REFERENCES resources(id),
    occupancy   integer NOT NULL DEFAULT 0 CHECK (occupancy >= 0),
    accepting   boolean NOT NULL DEFAULT true,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users(id),
    version     integer NOT NULL DEFAULT 1
);

CREATE TABLE shelter_occupancy_log (
    id              bigserial PRIMARY KEY,
    resource_id     uuid NOT NULL REFERENCES resources(id),
    kind            text NOT NULL CHECK (kind IN ('movement','settings')),
    admitted        integer NOT NULL DEFAULT 0 CHECK (admitted >= 0),
    discharged      integer NOT NULL DEFAULT 0 CHECK (discharged >= 0),
    occupancy_after integer NOT NULL,
    capacity_after  integer,
    accepting_after boolean NOT NULL,
    note            text NOT NULL DEFAULT '',
    actor_id        uuid NOT NULL REFERENCES users(id),
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX shelter_occupancy_log_idx ON shelter_occupancy_log (resource_id, id DESC);
CREATE TRIGGER shelter_occupancy_log_append_only BEFORE UPDATE OR DELETE ON shelter_occupancy_log
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
