-- 0012_assessment_photos: geotagged field photos for building assessments, idempotent upload from the offline queue,
-- and the hash of the bytes as received (stored images are re-encoded to strip hidden content and metadata).
ALTER TABLE report_media
    ADD COLUMN assessment_id   uuid REFERENCES damage_assessments(id),
    ADD COLUMN incident_id     uuid REFERENCES incidents(id),
    ADD COLUMN client_ref      text,
    ADD COLUMN captured_at     timestamptz,
    ADD COLUMN location        geography(Point, 4326),
    ADD COLUMN altitude_m      double precision,
    ADD COLUMN accuracy_m      double precision,
    ADD COLUMN received_sha256 text,
    ADD CONSTRAINT report_media_single_owner CHECK (report_id IS NULL OR assessment_id IS NULL);

CREATE UNIQUE INDEX report_media_client_ref_idx ON report_media (uploaded_by, client_ref) WHERE client_ref IS NOT NULL;
CREATE INDEX report_media_assessment_idx ON report_media (assessment_id) WHERE assessment_id IS NOT NULL;
