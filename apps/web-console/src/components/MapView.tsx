import { useEffect, useMemo } from "react";
import { CircleMarker, GeoJSON, MapContainer, Popup, TileLayer, Tooltip, useMap } from "react-leaflet";
import type { Feature } from "../lib/api";
import { TILE_ATTR, TILE_URL } from "../lib/tiles";
import { ago, t } from "../lib/format";


const COLORS: Record<string, string> = {
  reports: "#d97706", incidents: "#dc2626", resources: "#2563eb", hospital: "#059669", fire_station: "#b91c1c",
  shelter: "#7c3aed", assembly_point: "#0891b2", road_closure: "#111827", hazard: "#ea580c", impact_areas: "#dc2626",
};

export const TEHRAN: [number, number] = [35.7, 51.4];

/** Cities with sample data, for the map's quick jump. */
export const CITIES: { name: string; center: [number, number]; zoom: number }[] = [
  { name: "تهران", center: TEHRAN, zoom: 12 },
  { name: "سنندج", center: [35.3145, 46.9923], zoom: 13 },
];

/** MapContainer reads center/zoom only once; this follows later changes (e.g. a city switch). */
function Recenter({ center, zoom }: { center: [number, number]; zoom: number }) {
  const map = useMap();
  useEffect(() => { map.setView(center, zoom); }, [map, center[0], center[1], zoom]);
  return null;
}

export function MapView({ features, height = 520, onSelect, center = TEHRAN, zoom = 12 }: {
  features: Feature[]; height?: number; center?: [number, number]; zoom?: number;
  onSelect?: (f: Feature) => void;
}) {
  const points = useMemo(() => features.filter((f) => f.geometry.type === "Point"), [features]);
  const shapes = useMemo(() => features.filter((f) => f.geometry.type !== "Point"), [features]);
  return (
    <div style={{ height }} className="map" dir="ltr">
      <MapContainer center={center} zoom={zoom} style={{ height: "100%" }} preferCanvas>
        <Recenter center={center} zoom={zoom} />
        <TileLayer url={TILE_URL} attribution={TILE_ATTR} />
        {shapes.map((f) => {
          const layer = String(f.properties.layer);
          return (
            <GeoJSON key={f.id} data={f as never}
              style={{ color: COLORS[layer] ?? "#444", weight: layer === "road_closure" ? 5 : 2,
                dashArray: f.properties.stale || f.properties.estimate ? "6 6" : undefined, fillOpacity: 0.08 }}>
              <Tooltip sticky>
                <div dir="rtl">
                  <b>{t(layer)}</b> {String(f.properties.name ?? "")}
                  {f.properties.estimate ? <div>برآورد با عدم قطعیت؛ تأییدنشده</div> : null}
                  {f.properties.stale ? <div className="warn">داده کهنه — آخرین تأیید: {ago(f.properties.verified_at as string)}</div> : null}
                </div>
              </Tooltip>
            </GeoJSON>
          );
        })}
        {points.map((f) => {
          const [lng, lat] = f.geometry.coordinates as [number, number];
          const layer = String(f.properties.layer);
          const stale = Boolean(f.properties.stale);
          const approx = f.properties.precision === "approximate";
          return (
            <CircleMarker key={f.id} center={[lat, lng]} radius={layer === "incidents" ? 10 : 7}
              pathOptions={{ color: COLORS[layer] ?? "#444", fillOpacity: stale ? 0.15 : 0.7, dashArray: stale || approx ? "3 3" : undefined }}
              eventHandlers={{ click: () => onSelect?.(f) }}>
              <Popup>
                <div dir="rtl" className="popup">
                  <b>{t(layer)}</b>: {String(f.properties.name ?? f.properties.code ?? t(String(f.properties.report_type ?? f.properties.resource_type ?? "")))}
                  {f.properties.status ? <div>وضعیت: {t(String(f.properties.status))}</div> : null}
                  {f.properties.received_at ? <div>دریافت: {ago(String(f.properties.received_at))}</div> : null}
                  {f.properties.last_seen_at !== undefined ? <div>آخرین موقعیت: {ago(f.properties.last_seen_at as string | null)}</div> : null}
                  {approx ? <div className="muted">موقعیت تقریبی (حریم خصوصی)</div> : null}
                  {layer === "reports" && !f.properties.verified ? <div className="warn">تأییدنشده</div> : null}
                  {stale ? <div className="warn">داده کهنه — نباید زنده تلقی شود</div> : null}
                </div>
              </Popup>
            </CircleMarker>
          );
        })}
      </MapContainer>
    </div>
  );
}

export function Legend({ layers }: { layers: Record<string, { count: number; stale_count: number; note?: string }> }) {
  return (
    <div className="legend">
      {Object.entries(layers).map(([k, v]) => (
        <span key={k} className="legend-item">
          <i style={{ background: COLORS[k] ?? "#444" }} /> {t(k)}: {v.note === "no_permission" ? "بدون مجوز" : v.count}
          {v.stale_count > 0 && <em className="warn"> ({v.stale_count} کهنه)</em>}
        </span>
      ))}
    </div>
  );
}
