// Package gis owns spatial reference layers, impact areas and the map feature API.
// It also provides the shared coordinate validation used by other modules (WGS84 / EPSG:4326 input).
package gis

import (
	"context"
	"encoding/json"
	"math"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/db"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/httpx"
)

// Point is a WGS84 coordinate in decimal degrees.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Location is a point with its claimed accuracy and provenance.
type Location struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	AccuracyM float64 `json:"accuracy_m"`
	Source    string  `json:"source,omitempty"` // gps | network | manual
	Precision string  `json:"precision,omitempty"`
}

// Area is the configured service area; reports outside it are rejected (AT-10).
type Area struct{ MinLat, MaxLat, MinLng, MaxLng float64 }

func AreaFromConfig(c config.Config) Area {
	return Area{MinLat: c.AreaMinLat, MaxLat: c.AreaMaxLat, MinLng: c.AreaMinLng, MaxLng: c.AreaMaxLng}
}

func (a Area) Contains(p Point) bool {
	return p.Lat >= a.MinLat && p.Lat <= a.MaxLat && p.Lng >= a.MinLng && p.Lng <= a.MaxLng
}

func validFloat(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// ValidatePoint checks WGS84 ranges and the service area.
func ValidatePoint(v *httpx.Validator, field string, p Point, area Area) {
	okLat := validFloat(p.Lat) && p.Lat >= -90 && p.Lat <= 90
	okLng := validFloat(p.Lng) && p.Lng >= -180 && p.Lng <= 180
	v.Check(okLat, field+".lat", "out_of_range")
	v.Check(okLng, field+".lng", "out_of_range")
	if okLat && okLng {
		v.Check(area.Contains(p), field, "outside_service_area")
	}
}

// ValidateLocation also checks accuracy (meters) and the location source.
func ValidateLocation(v *httpx.Validator, field string, l Location, area Area) {
	ValidatePoint(v, field, Point{Lat: l.Lat, Lng: l.Lng}, area)
	v.Check(validFloat(l.AccuracyM) && l.AccuracyM > 0 && l.AccuracyM <= 5000, field+".accuracy_m", "must_be_between_0_and_5000")
	v.Check(l.Source == "" || l.Source == "gps" || l.Source == "network" || l.Source == "manual", field+".source", "not_allowed")
}

// Approximate coarsens a coordinate (~1.1 km grid) for callers without precise-location permission.
func Approximate(p Point) Point {
	return Point{Lat: math.Round(p.Lat*100) / 100, Lng: math.Round(p.Lng*100) / 100}
}

// PointSQL is the PostGIS expression for a geography point from ($lng, $lat) parameters.
const PointSQL = "ST_SetSRID(ST_MakePoint(%s, %s), 4326)::geography"

// BBox is minLng,minLat,maxLng,maxLat.
type BBox struct{ MinLng, MinLat, MaxLng, MaxLat float64 }

// ValidateGeoJSONArea checks that a GeoJSON (Multi)Polygon is valid, closed and of bounded size, using PostGIS.
// It returns the area in km².
func ValidateGeoJSONArea(ctx context.Context, q db.DBTX, field string, geom json.RawMessage, maxKm2 float64) (float64, error) {
	var typ struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(geom, &typ); err != nil || (typ.Type != "Polygon" && typ.Type != "MultiPolygon") {
		return 0, httpx.Validation(httpx.FieldDetail{Field: field, Reason: "must_be_geojson_polygon_or_multipolygon"})
	}
	var valid bool
	var areaKm2 float64
	err := q.QueryRow(ctx, `WITH g AS (SELECT ST_SetSRID(ST_GeomFromGeoJSON($1), 4326) AS g)
		SELECT ST_IsValid(g), ST_Area(g::geography) / 1e6 FROM g`, string(geom)).Scan(&valid, &areaKm2)
	if err != nil {
		return 0, httpx.Validation(httpx.FieldDetail{Field: field, Reason: "invalid_geometry"})
	}
	if !valid {
		return 0, httpx.Validation(httpx.FieldDetail{Field: field, Reason: "invalid_geometry"})
	}
	if areaKm2 <= 0 || areaKm2 > maxKm2 {
		return 0, httpx.Validation(httpx.FieldDetail{Field: field, Reason: "area_out_of_bounds"})
	}
	return areaKm2, nil
}
