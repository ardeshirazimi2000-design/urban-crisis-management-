import { useState } from "react";
import { api, type FeatureCollection, type Incident, type Report } from "../lib/api";
import { useSession } from "../lib/session";
import { num, t } from "../lib/format";
import { ErrorBox, Freshness, Section, usePoll } from "../components/ui";
import { CITIES, Legend, MapView } from "../components/MapView";

const ALL_LAYERS = ["reports", "incidents", "resources", "impact_areas", "hospital", "fire_station", "shelter", "assembly_point", "road_closure", "hazard"];

export function DashboardPage() {
  const { can } = useSession();
  const [layers, setLayers] = useState<string[]>(ALL_LAYERS);
  const [hours, setHours] = useState(24);
  const [city, setCity] = useState(() => {
    try { return Number(localStorage.getItem("crisis.city") ?? 0) || 0; } catch { return 0; }
  });
  const pickCity = (i: number) => { setCity(i); try { localStorage.setItem("crisis.city", String(i)); } catch { /* per-viewer convenience only */ } };
  const view = CITIES[city] ?? CITIES[0];
  const map = usePoll(() => api<FeatureCollection>("GET", "/gis/features", { query: { layers: layers.join(","), since_hours: hours } }), 15000, [layers.join(), hours]);
  const queue = usePoll(async () => (can("report:read") ? api<{ items: Report[] }>("GET", "/reports", { query: { limit: 200 } }) : { items: [] }), 15000);
  const incidents = usePoll(async () => (can("incident:read") ? api<{ items: Incident[] }>("GET", "/incidents", { query: { active: "true" } }) : { items: [] }), 15000);

  const open = queue.data?.items.filter((r) => ["received", "triage", "under_review"].includes(r.status)) ?? [];
  const oldest = open.reduce<string | null>((o, r) => (!o || r.received_at < o ? r.received_at : o), null);
  const stale = Object.values(map.data?.meta.layers ?? {}).reduce((s, l) => s + l.stale_count, 0);

  return (
    <>
      <div className="kpis">
        <div className="kpi"><b>{num(open.length)}</b><span>گزارش در انتظار تصمیم</span></div>
        <div className="kpi"><b>{oldest ? <Freshness at={oldest} label="قدیمی‌ترین" /> : "—"}</b><span>سن صف بررسی</span></div>
        <div className="kpi"><b>{num(incidents.data?.items.length ?? 0)}</b><span>حادثه باز</span></div>
        <div className="kpi"><b>{num(incidents.data?.items.filter((i) => i.severity === "critical").length ?? 0)}</b><span>حادثه بحرانی</span></div>
        <div className={stale ? "kpi warn" : "kpi"}><b>{num(stale)}</b><span>عارضه با داده کهنه</span></div>
      </div>
      <Section title="تصویر عملیاتی مشترک" actions={<Freshness at={map.updatedAt} />}>
        <div className="row gap wrap filters">
          {ALL_LAYERS.map((l) => (
            <label key={l} className="chk">
              <input type="checkbox" checked={layers.includes(l)}
                onChange={(e) => setLayers(e.target.checked ? [...layers, l] : layers.filter((x) => x !== l))} />
              {t(l)}
            </label>
          ))}
          <label>شهر
            <select value={city} onChange={(e) => pickCity(+e.target.value)}>
              {CITIES.map((c, i) => <option key={c.name} value={i}>{c.name}</option>)}
            </select>
          </label>
          <label>بازه زمانی
            <select value={hours} onChange={(e) => setHours(+e.target.value)}>
              <option value={6}>۶ ساعت</option><option value={24}>۲۴ ساعت</option><option value={72}>۷۲ ساعت</option>
            </select>
          </label>
        </div>
        <ErrorBox error={map.error} />
        {map.error && map.data && <p className="warn">نمایش آخرین داده معتبر؛ به‌روزرسانی ناموفق بود.</p>}
        <MapView features={map.data?.features ?? []} center={view.center} zoom={view.zoom} />
        {map.data && <Legend layers={map.data.meta.layers} />}
        <p className="muted small">خطوط نقطه‌چین: داده کهنه، موقعیت تقریبی یا برآورد تأییدنشده. محدوده اثر صرفاً برآورد اولیه است.</p>
      </Section>
    </>
  );
}
