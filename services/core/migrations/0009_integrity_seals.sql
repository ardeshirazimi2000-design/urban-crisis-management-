-- 0009_integrity_seals: HMAC seals over the critical fields of incidents and alerts (AFTA PP FDP_SDI.2).
-- The application gives its sessions the key (crisis.integrity_key); a change made without the key — e.g. a
-- direct database edit — leaves the old seal in place and is reported by the integrity check.

CREATE FUNCTION incident_seal_data(i incidents) RETURNS text LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object('id', i.id, 'code', i.code, 'type', i.type, 'title', i.title, 'severity', i.severity,
        'response_level', i.response_level, 'status', i.status, 'owner_org_id', i.owner_org_id,
        'location', encode(ST_AsBinary(i.location::geometry), 'hex'), 'created_by', i.created_by,
        'opened_at', i.opened_at, 'closed_at', i.closed_at, 'version', i.version)::text
$$;

CREATE FUNCTION alert_seal_data(a alerts) RETURNS text LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object('id', a.id, 'incident_id', a.incident_id, 'mode', a.mode, 'severity', a.severity,
        'status', a.status, 'issuer_org_id', a.issuer_org_id, 'region', encode(ST_AsBinary(a.region_geom::geometry), 'hex'),
        'region_label', a.region_label, 'template_code', a.template_code, 'template_version', a.template_version,
        'params', a.params, 'rendered_text', a.rendered_text, 'channels', a.channels, 'issued_at', a.issued_at,
        'expires_at', a.expires_at, 'created_by', a.created_by, 'approved_by', a.approved_by, 'approved_at', a.approved_at,
        'cancelled_by', a.cancelled_by, 'version', a.version)::text
$$;

CREATE FUNCTION seal_record() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    k    text := current_setting('crisis.integrity_key', true);
    data text;
BEGIN
    IF k IS NULL OR k = '' THEN
        RETURN NEW;  -- unsealed change: the next integrity check reports it
    END IF;
    IF TG_TABLE_NAME = 'incidents' THEN
        data := incident_seal_data(NEW);
    ELSE
        data := alert_seal_data(NEW);
    END IF;
    INSERT INTO record_seals (record_type, record_id, mac)
    VALUES (TG_TABLE_NAME, NEW.id, encode(hmac(data, k, 'sha256'), 'hex'))
    ON CONFLICT (record_type, record_id) DO UPDATE SET mac = EXCLUDED.mac, sealed_at = now(), ok = true;
    RETURN NEW;
END $$;

CREATE TRIGGER incidents_seal AFTER INSERT OR UPDATE ON incidents FOR EACH ROW EXECUTE FUNCTION seal_record();
CREATE TRIGGER alerts_seal AFTER INSERT OR UPDATE ON alerts FOR EACH ROW EXECUTE FUNCTION seal_record();

-- Records that existed before this migration are sealed once by `migrate -seal-existing` (needs the key).
