-- 0006_sitrep: issued situation reports. The figures are computed by the server at issue time (never taken
-- from the client) and frozen with the commander's narrative; issued reports cannot be changed.
CREATE SEQUENCE sitrep_no_seq;
CREATE TABLE sitreps (
    id           uuid PRIMARY KEY,
    number       integer NOT NULL UNIQUE DEFAULT nextval('sitrep_no_seq'),
    window_hours integer NOT NULL CHECK (window_hours BETWEEN 1 AND 720),
    figures      jsonb NOT NULL,
    summary      text NOT NULL,
    issued_by    uuid NOT NULL REFERENCES users(id),
    issued_at    timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER sitreps_append_only BEFORE UPDATE OR DELETE ON sitreps
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
