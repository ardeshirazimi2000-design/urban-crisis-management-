// Base-map tiles come through the console's own server (/tiles → nginx cache → upstream tile server), so
// browsers need no direct access to the tile provider and the CSP stays same-origin. Upstream is set with
// TILE_UPSTREAM on the web container; VITE_TILE_URL overrides it entirely (e.g. an offline tile server, D-09).
export const TILE_URL = (import.meta.env.VITE_TILE_URL as string | undefined) ?? "/tiles/{z}/{x}/{y}.png";
export const TILE_ATTR = (import.meta.env.VITE_TILE_ATTRIBUTION as string | undefined) ?? "&copy; OpenStreetMap contributors";
