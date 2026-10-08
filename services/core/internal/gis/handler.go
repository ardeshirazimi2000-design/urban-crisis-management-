package gis

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/audit"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

var ReferenceLayers = []string{"hospital", "fire_station", "shelter", "road_closure", "hazard", "assembly_point"}
var DynamicLayers = []string{"reports", "incidents", "resources", "impact_areas"}

const ImpactAlgorithm = "radial-buffer-v1"

type Module struct {
	Pool               *pgxpool.Pool
	Guard              *auth.Guard
	Area               Area
	StaleAfter         time.Duration // reference data not re-verified within this window is flagged stale
	ResourceStaleAfter time.Duration
}

func (m *Module) Routes(r *httpx.Router) {
	r.Auth("GET /gis/features", m.features)
	r.Auth("POST /gis/features", m.createFeature)
	r.Auth("POST /gis/features/{id}/verify", m.verifyFeature)
	r.Auth("POST /gis/impact-area", m.impactArea)
	r.Auth("GET /gis/impact-areas", m.listImpactAreas)
}

type Feature struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties map[string]any  `json:"properties"`
}

type layerMeta struct {
	Count      int    `json:"count"`
	StaleCount int    `json:"stale_count"`
	Note       string `json:"note,omitempty"`
}

func parseBBox(s string) (*BBox, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return nil, httpx.Validation(httpx.FieldDetail{Field: "bbox", Reason: "expected_minLng,minLat,maxLng,maxLat"})
	}
	var v [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, httpx.Validation(httpx.FieldDetail{Field: "bbox", Reason: "invalid_number"})
		}
		v[i] = f
	}
	if v[0] >= v[2] || v[1] >= v[3] || (v[2]-v[0])*(v[3]-v[1]) > 25 {
		return nil, httpx.Validation(httpx.FieldDetail{Field: "bbox", Reason: "invalid_or_too_large"})
	}
	return &BBox{MinLng: v[0], MinLat: v[1], MaxLng: v[2], MaxLat: v[3]}, nil
}

// features returns a GeoJSON FeatureCollection of the requested layers within a bbox. Every feature carries
// its freshness (updated/verified time and a stale flag) so the map never presents old data as live.
func (m *Module) features(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GISRead, "gis", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	bbox, err := parseBBox(r.URL.Query().Get("bbox"))
	if err != nil {
		return err
	}
	if bbox == nil {
		bbox = &BBox{MinLng: m.Area.MinLng, MinLat: m.Area.MinLat, MaxLng: m.Area.MaxLng, MaxLat: m.Area.MaxLat}
	}
	layers := strings.Split(r.URL.Query().Get("layers"), ",")
	if r.URL.Query().Get("layers") == "" {
		layers = append(append([]string{}, DynamicLayers...), ReferenceLayers...)
	}
	for _, l := range layers {
		if !slices.Contains(ReferenceLayers, l) && !slices.Contains(DynamicLayers, l) {
			return httpx.Validation(httpx.FieldDetail{Field: "layers", Reason: "unknown_layer:" + l})
		}
	}
	sinceHours := 72
	if s := r.URL.Query().Get("since_hours"); s != "" {
		if sinceHours, err = strconv.Atoi(s); err != nil || sinceHours < 1 || sinceHours > 24*30 {
			return httpx.Validation(httpx.FieldDetail{Field: "since_hours", Reason: "must_be_1_to_720"})
		}
	}
	env := []any{bbox.MinLng, bbox.MinLat, bbox.MaxLng, bbox.MaxLat}
	features := []Feature{}
	meta := map[string]*layerMeta{}

	add := func(layer string, f Feature, stale bool) {
		lm := meta[layer]
		if lm == nil {
			lm = &layerMeta{}
			meta[layer] = lm
		}
		lm.Count++
		if stale {
			lm.StaleCount++
		}
		f.Properties["layer"] = layer
		f.Properties["stale"] = stale
		features = append(features, f)
	}

	var refLayers []string
	for _, l := range layers {
		switch {
		case l == "reports":
			if !p.Has(auth.ReportRead) {
				meta[l] = &layerMeta{Note: "no_permission"}
				continue
			}
			precise := p.Has(auth.ReportReadPrecise)
			rows, err := m.Pool.Query(ctx, `SELECT id, type, status, severity, received_at, accuracy_m,
				ST_Y(location::geometry), ST_X(location::geometry) FROM reports
				WHERE location && ST_MakeEnvelope($1,$2,$3,$4,4326)::geography AND received_at > now() - make_interval(hours => $5)
				  AND status NOT IN ('rejected','duplicate') ORDER BY received_at DESC LIMIT 2000`, append(env, sinceHours)...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id uuid.UUID
				var typ, st string
				var sev *string
				var recv time.Time
				var acc, lat, lng float64
				if err := rows.Scan(&id, &typ, &st, &sev, &recv, &acc, &lat, &lng); err != nil {
					rows.Close()
					return err
				}
				pt := Point{Lat: lat, Lng: lng}
				precision := "exact"
				if !precise {
					pt, precision, acc = Approximate(pt), "approximate", max(acc, 1100)
				}
				add(l, Feature{Type: "Feature", ID: "report:" + id.String(), Geometry: pointJSON(pt),
					Properties: map[string]any{"id": id, "report_type": typ, "status": st, "severity": sev, "received_at": recv,
						"accuracy_m": acc, "precision": precision, "verified": st == "accepted" || st == "linked_to_incident"}}, false)
			}
			rows.Close()
		case l == "incidents":
			all, orgs := p.OrgScope(auth.IncidentRead)
			if !all && len(orgs) == 0 {
				meta[l] = &layerMeta{Note: "no_permission"}
				continue
			}
			rows, err := m.Pool.Query(ctx, `SELECT id, code, type, title, status, severity, response_level, updated_at,
				ST_Y(location::geometry), ST_X(location::geometry) FROM incidents
				WHERE location IS NOT NULL AND location && ST_MakeEnvelope($1,$2,$3,$4,4326)::geography
				  AND status <> 'closed' AND ($5 OR owner_org_id = ANY($6)) LIMIT 1000`, append(env, all, orgs)...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id uuid.UUID
				var code, typ, title, st, sev, lvl string
				var upd time.Time
				var lat, lng float64
				if err := rows.Scan(&id, &code, &typ, &title, &st, &sev, &lvl, &upd, &lat, &lng); err != nil {
					rows.Close()
					return err
				}
				add(l, Feature{Type: "Feature", ID: "incident:" + id.String(), Geometry: pointJSON(Point{Lat: lat, Lng: lng}),
					Properties: map[string]any{"id": id, "code": code, "incident_type": typ, "title": title, "status": st,
						"severity": sev, "response_level": lvl, "updated_at": upd}}, false)
			}
			rows.Close()
		case l == "resources":
			all, orgs := p.OrgScope(auth.ResourceRead)
			if !all && len(orgs) == 0 {
				meta[l] = &layerMeta{Note: "no_permission"}
				continue
			}
			rows, err := m.Pool.Query(ctx, `SELECT id, type, name, status, last_seen_at, ST_Y(location::geometry), ST_X(location::geometry)
				FROM resources WHERE location IS NOT NULL AND location && ST_MakeEnvelope($1,$2,$3,$4,4326)::geography
				  AND ($5 OR organization_id = ANY($6)) LIMIT 2000`, append(env, all, orgs)...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id uuid.UUID
				var typ, name, st string
				var seen *time.Time
				var lat, lng float64
				if err := rows.Scan(&id, &typ, &name, &st, &seen, &lat, &lng); err != nil {
					rows.Close()
					return err
				}
				stale := typ != "shelter" && (seen == nil || time.Since(*seen) > m.ResourceStaleAfter)
				add(l, Feature{Type: "Feature", ID: "resource:" + id.String(), Geometry: pointJSON(Point{Lat: lat, Lng: lng}),
					Properties: map[string]any{"id": id, "resource_type": typ, "name": name, "status": st, "last_seen_at": seen}}, stale)
			}
			rows.Close()
		case l == "impact_areas":
			rows, err := m.Pool.Query(ctx, `SELECT id, incident_id, ST_AsGeoJSON(geom)::text, source, algorithm_version, uncertainty_m, created_at
				FROM impact_areas WHERE geom && ST_MakeEnvelope($1,$2,$3,$4,4326)::geography AND created_at > now() - make_interval(hours => $5)
				ORDER BY created_at DESC LIMIT 200`, append(env, sinceHours)...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id uuid.UUID
				var inc *uuid.UUID
				var geom, src, alg string
				var unc float64
				var created time.Time
				if err := rows.Scan(&id, &inc, &geom, &src, &alg, &unc, &created); err != nil {
					rows.Close()
					return err
				}
				add(l, Feature{Type: "Feature", ID: "impact:" + id.String(), Geometry: json.RawMessage(geom),
					Properties: map[string]any{"id": id, "incident_id": inc, "source": src, "algorithm_version": alg,
						"uncertainty_m": unc, "created_at": created, "estimate": true}}, false)
			}
			rows.Close()
		default:
			refLayers = append(refLayers, l)
		}
	}
	if len(refLayers) > 0 {
		rows, err := m.Pool.Query(ctx, `SELECT id, layer, name, ST_AsGeoJSON(geom)::text, properties, source, verified_at, valid_until, updated_at
			FROM gis_features WHERE layer = ANY($5) AND geom && ST_MakeEnvelope($1,$2,$3,$4,4326)::geography
			  AND (valid_until IS NULL OR valid_until > now()) LIMIT 5000`, append(env, refLayers)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			var layer, name, geom, src string
			var props map[string]any
			var verified, validUntil *time.Time
			var upd time.Time
			if err := rows.Scan(&id, &layer, &name, &geom, &props, &src, &verified, &validUntil, &upd); err != nil {
				rows.Close()
				return err
			}
			if props == nil {
				props = map[string]any{}
			}
			props["id"], props["name"], props["source"], props["verified_at"], props["valid_until"], props["updated_at"] =
				id, name, src, verified, validUntil, upd
			stale := verified == nil || time.Since(*verified) > m.StaleAfter
			add(layer, Feature{Type: "Feature", ID: layer + ":" + id.String(), Geometry: json.RawMessage(geom), Properties: props}, stale)
		}
		rows.Close()
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"type": "FeatureCollection", "features": features,
		"meta": map[string]any{"generated_at": time.Now().UTC(), "bbox": env, "layers": meta,
			"stale_after_seconds": int(m.StaleAfter.Seconds()), "crs": "EPSG:4326"},
	})
	return nil
}

func pointJSON(p Point) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"Point","coordinates":[%f,%f]}`, p.Lng, p.Lat))
}

type featureRequest struct {
	Layer      string          `json:"layer"`
	Name       string          `json:"name"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties map[string]any  `json:"properties"`
	OwnerOrgID *uuid.UUID      `json:"owner_org_id"`
	Source     string          `json:"source"`
	ValidUntil *time.Time      `json:"valid_until"`
}

func (m *Module) createFeature(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GISManage, "gis_feature", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req featureRequest
	if err := httpx.DecodeJSON(w, r, &req, 512<<10); err != nil {
		return err
	}
	var v httpx.Validator
	v.Check(slices.Contains(ReferenceLayers, req.Layer), "layer", "not_allowed")
	v.Check(strings.TrimSpace(req.Name) != "", "name", "required")
	v.Check(strings.TrimSpace(req.Source) != "", "source", "required")
	v.Check(len(req.Geometry) > 0, "geometry", "required")
	v.Check(req.ValidUntil == nil || req.ValidUntil.After(time.Now()), "valid_until", "in_past")
	if err := v.Err(); err != nil {
		return err
	}
	if req.Properties == nil {
		req.Properties = map[string]any{}
	}
	var id uuid.UUID
	err := db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		var valid, inArea bool
		err := tx.QueryRow(ctx, `WITH g AS (SELECT ST_SetSRID(ST_GeomFromGeoJSON($1), 4326) AS g)
			SELECT ST_IsValid(g), ST_Intersects(g, ST_MakeEnvelope($2,$3,$4,$5,4326)) FROM g`,
			string(req.Geometry), m.Area.MinLng, m.Area.MinLat, m.Area.MaxLng, m.Area.MaxLat).Scan(&valid, &inArea)
		if err != nil || !valid {
			return httpx.Validation(httpx.FieldDetail{Field: "geometry", Reason: "invalid_geometry"})
		}
		if !inArea {
			return httpx.Validation(httpx.FieldDetail{Field: "geometry", Reason: "outside_service_area"})
		}
		if err := tx.QueryRow(ctx, `INSERT INTO gis_features (layer, name, geom, properties, owner_org_id, source, verified_at, valid_until, created_by)
			VALUES ($1,$2, ST_SetSRID(ST_GeomFromGeoJSON($3), 4326)::geography, $4,$5,$6, now(), $7, $8) RETURNING id`,
			req.Layer, req.Name, string(req.Geometry), req.Properties, req.OwnerOrgID, req.Source, req.ValidUntil, p.UserID).Scan(&id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "gis.feature_create",
			TargetType: "gis_feature", TargetID: id.String(), Outcome: "success", Details: map[string]any{"layer": req.Layer},
			CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "layer": req.Layer})
	return nil
}

func (m *Module) verifyFeature(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := httpx.PathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := m.Guard.Require(ctx, auth.GISManage, "gis_feature", id.String()); err != nil {
		return err
	}
	tag, err := m.Pool.Exec(ctx, `UPDATE gis_features SET verified_at=now(), updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.ErrNotFound
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "verified_at": time.Now().UTC()})
	return nil
}

type impactRequest struct {
	IncidentID   *uuid.UUID `json:"incident_id"`
	Center       Point      `json:"center"`
	RadiusM      float64    `json:"radius_m"`
	UncertaintyM float64    `json:"uncertainty_m"`
	Source       string     `json:"source"`
}

// impactArea stores an *estimated* impact area as a simple radial buffer. It is labelled with its algorithm,
// source and uncertainty and must never be presented as a verified damage footprint.
func (m *Module) impactArea(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GISManage, "impact_area", ""); err != nil {
		return err
	}
	p := auth.FromContext(ctx)
	var req impactRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		return err
	}
	var v httpx.Validator
	ValidatePoint(&v, "center", req.Center, m.Area)
	v.Check(req.RadiusM >= 50 && req.RadiusM <= 100000, "radius_m", "must_be_50_to_100000")
	v.Check(req.UncertaintyM >= 0 && req.UncertaintyM <= 100000, "uncertainty_m", "out_of_range")
	v.Check(strings.TrimSpace(req.Source) != "", "source", "required")
	if err := v.Err(); err != nil {
		return err
	}
	var id uuid.UUID
	var geom string
	err := db.WithTx(ctx, m.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO impact_areas (incident_id, geom, source, algorithm_version, uncertainty_m, inputs, created_by)
			VALUES ($1, ST_Buffer(ST_SetSRID(ST_MakePoint($2,$3),4326)::geography, $4, 'quad_segs=16'), $5, $6, $7, $8, $9)
			RETURNING id, ST_AsGeoJSON(geom)::text`,
			req.IncidentID, req.Center.Lng, req.Center.Lat, req.RadiusM, req.Source, ImpactAlgorithm, req.UncertaintyM,
			map[string]any{"center": req.Center, "radius_m": req.RadiusM}, p.UserID).Scan(&id, &geom)
		if err != nil {
			if db.IsUniqueViolation(err, "") {
				return err
			}
			return fmt.Errorf("insert impact area: %w", err)
		}
		return audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorRoles: p.RoleNames(), Action: "gis.impact_area",
			TargetType: "impact_area", TargetID: id.String(), Outcome: "success",
			Details: map[string]any{"radius_m": req.RadiusM, "source": req.Source}, CorrelationID: httpx.CorrelationID(ctx)})
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "geometry": json.RawMessage(geom), "algorithm_version": ImpactAlgorithm,
		"uncertainty_m": req.UncertaintyM, "source": req.Source, "estimate": true,
		"note": "برآورد اولیه با عدم قطعیت؛ جایگزین ارزیابی میدانی نیست"})
	return nil
}

func (m *Module) listImpactAreas(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.Guard.Require(ctx, auth.GISRead, "impact_area", ""); err != nil {
		return err
	}
	var incident *uuid.UUID
	if s := r.URL.Query().Get("incident_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return httpx.Validation(httpx.FieldDetail{Field: "incident_id", Reason: "invalid"})
		}
		incident = &id
	}
	items, err := ListImpactAreas(ctx, m.Pool, incident)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func ListImpactAreas(ctx context.Context, q db.DBTX, incident *uuid.UUID) ([]map[string]any, error) {
	rows, err := q.Query(ctx, `SELECT id, incident_id, ST_AsGeoJSON(geom)::text, source, algorithm_version, uncertainty_m, created_at
		FROM impact_areas WHERE ($1::uuid IS NULL OR incident_id = $1) ORDER BY created_at DESC LIMIT 100`, incident)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var inc *uuid.UUID
		var geom, src, alg string
		var unc float64
		var created time.Time
		if err := rows.Scan(&id, &inc, &geom, &src, &alg, &unc, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "incident_id": inc, "geometry": json.RawMessage(geom), "source": src,
			"algorithm_version": alg, "uncertainty_m": unc, "created_at": created, "estimate": true})
	}
	return out, rows.Err()
}
