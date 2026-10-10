import { Circle, CircleMarker, MapContainer, TileLayer, useMapEvents } from "react-leaflet";
import { TILE_ATTR, TILE_URL } from "../lib/tiles";
import { TEHRAN } from "./MapView";

export type PickedPoint = { lat: number; lng: number };

function ClickHandler({ onPick }: { onPick: (p: PickedPoint) => void }) {
  useMapEvents({ click: (e) => onPick({ lat: +e.latlng.lat.toFixed(6), lng: +e.latlng.lng.toFixed(6) }) });
  return null;
}

/** Click-to-place location picker; the dashed circle shows the estimated accuracy. */
export function MapPicker({ value, accuracyM, onPick, height = 300, center = TEHRAN }: {
  value: PickedPoint | null; accuracyM: number; onPick: (p: PickedPoint) => void; height?: number; center?: [number, number];
}) {
  return (
    <div style={{ height }} className="map picker" dir="ltr" data-testid="map-picker">
      <MapContainer center={value ? [value.lat, value.lng] : center} zoom={12} style={{ height: "100%" }}>
        <TileLayer url={TILE_URL} attribution={TILE_ATTR} />
        <ClickHandler onPick={onPick} />
        {value && (
          <>
            <Circle center={[value.lat, value.lng]} radius={accuracyM} pathOptions={{ color: "#d97706", dashArray: "6 6", fillOpacity: 0.08 }} />
            <CircleMarker center={[value.lat, value.lng]} radius={8} pathOptions={{ color: "#d97706", fillOpacity: 0.9 }} />
          </>
        )}
      </MapContainer>
    </div>
  );
}
