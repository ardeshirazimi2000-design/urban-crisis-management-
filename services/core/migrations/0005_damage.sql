-- 0005_damage: rapid post-earthquake building assessment (green = usable, yellow = restricted, red = unsafe).
-- An assessment is never edited: a re-inspection is a new row that supersedes the previous one, so the
-- history of every building's tag stays visible.
CREATE TABLE damage_assessments (
    id                 uuid PRIMARY KEY,
    incident_id        uuid REFERENCES incidents(id),
    location           geography(Point, 4326) NOT NULL,
    address_text       text NOT NULL DEFAULT '',
    building_use       text NOT NULL CHECK (building_use IN
                         ('residential','school','hospital','commercial','government','industrial','religious','other')),
    floors             integer CHECK (floors BETWEEN 1 AND 200),
    tag                text NOT NULL CHECK (tag IN ('green','yellow','red')),
    observations       text[] NOT NULL DEFAULT '{}',
    people_trapped     boolean NOT NULL DEFAULT false,
    occupants_estimate integer CHECK (occupants_estimate >= 0),
    notes              text NOT NULL DEFAULT '',
    supersedes         uuid UNIQUE REFERENCES damage_assessments(id),
    superseded_at      timestamptz,
    report_id          uuid REFERENCES reports(id),  -- review-queue report raised for trapped people
    assessor_id        uuid NOT NULL REFERENCES users(id),
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX damage_assessments_geom_gix ON damage_assessments USING GIST (location);
CREATE INDEX damage_assessments_current_idx ON damage_assessments (tag, created_at DESC) WHERE superseded_at IS NULL;
CREATE INDEX damage_assessments_incident_idx ON damage_assessments (incident_id);
