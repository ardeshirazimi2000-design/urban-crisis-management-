-- 0011_audit_acknowledged_breaks: a break in the audit chain that the security auditor examined and accepted
-- (e.g. a record a background service signed with the wrong key). Verification steps over it only while the
-- record still has the hash it had when it was acknowledged, so a later change is still detected.
CREATE TABLE audit_acknowledged_breaks (
    seq             bigint PRIMARY KEY,
    problem         text NOT NULL,
    row_hash        text NOT NULL,
    reason          text NOT NULL,
    acknowledged_by uuid NOT NULL REFERENCES users(id),
    acknowledged_at timestamptz NOT NULL DEFAULT now()
);
