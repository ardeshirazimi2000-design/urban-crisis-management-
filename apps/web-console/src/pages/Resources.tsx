import { useState } from "react";
import { api, type Resource } from "../lib/api";
import { useSession } from "../lib/session";
import { ago, num, RESOURCE_TYPES, t } from "../lib/format";
import { Badge, Empty, ErrorBox, Freshness, ReasonAction, Section, useAction, usePoll } from "../components/ui";

export function ResourcesPage() {
  const { can, orgs, orgName } = useSession();
  const [type, setType] = useState("");
  const [status, setStatus] = useState("");
  const list = usePoll(() => api<{ items: Resource[]; stale_after_seconds: number }>("GET", "/resources", { query: { type, status } }), 15000, [type, status]);
  const [form, setForm] = useState({ organization_id: "", type: "ambulance", name: "", lat: "", lng: "", capacity: "" });
  const act = useAction();
  const items = list.data?.items ?? [];
  const counts = RESOURCE_TYPES.map((rt) => ({ rt, avail: items.filter((r) => r.type === rt && r.status === "available").length, all: items.filter((r) => r.type === rt).length }));

  return (
    <>
      <div className="kpis">
        {counts.filter((c) => c.all > 0).map((c) => (
          <div key={c.rt} className={c.avail === 0 ? "kpi warn" : "kpi"}><b>{num(c.avail)} / {num(c.all)}</b><span>{t(c.rt)} آماده</span></div>
        ))}
      </div>
      <Section title="منابع عملیاتی" actions={<Freshness at={list.updatedAt} />}>
        <div className="row gap wrap filters">
          <label>نوع<select value={type} onChange={(e) => setType(e.target.value)}>
            <option value="">همه</option>{RESOURCE_TYPES.map((x) => <option key={x} value={x}>{t(x)}</option>)}</select></label>
          <label>وضعیت<select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">همه</option>{["available", "assigned", "en_route", "on_scene", "out_of_service"].map((x) => <option key={x} value={x}>{t(x)}</option>)}</select></label>
        </div>
        <ErrorBox error={list.error} />
        {items.length === 0 && <Empty>منبعی در دامنه دسترسی شما نیست.</Empty>}
        <table className="table">
          <thead><tr><th>نام</th><th>نوع</th><th>سازمان</th><th>وضعیت</th><th>ظرفیت</th><th>آخرین موقعیت</th><th /></tr></thead>
          <tbody>
            {items.map((r) => (
              <tr key={r.id}>
                <td>{r.name}</td><td>{t(r.type)}</td><td>{orgName(r.organization_id)}</td><td><Badge value={r.status} /></td>
                <td>{num(r.capacity)}</td>
                <td>{r.stale ? <span className="warn">کهنه — {ago(r.last_seen_at)}</span> : ago(r.last_seen_at)}</td>
                <td>{can("resource:manage") && ["available", "out_of_service"].includes(r.status) && (
                  <ReasonAction label={r.status === "available" ? "خارج از خدمت" : "بازگشت به خدمت"}
                    onSubmit={async (reason) => {
                      await api("POST", `/resources/${r.id}/status`, { body: { status: r.status === "available" ? "out_of_service" : "available", reason, version: r.version } });
                      await list.reload();
                    }} />)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="muted small">موقعیت قدیمی‌تر از {num((list.data?.stale_after_seconds ?? 600) / 60)} دقیقه «کهنه» نمایش داده می‌شود و نباید موقعیت زنده تلقی شود.</p>
      </Section>
      {can("resource:manage") && (
        <Section title="ثبت منبع">
          <form className="grid-form" onSubmit={(e) => {
            e.preventDefault();
            void act.run(async () => {
              await api("POST", "/resources", { body: { organization_id: form.organization_id || orgs[0]?.id, type: form.type, name: form.name,
                capacity: form.capacity ? +form.capacity : undefined, location: form.lat && form.lng ? { lat: +form.lat, lng: +form.lng } : undefined } });
              setForm({ ...form, name: "" });
              await list.reload();
            });
          }}>
            <label>سازمان<select value={form.organization_id} onChange={(e) => setForm({ ...form, organization_id: e.target.value })}>
              {orgs.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
            <label>نوع<select value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value })}>
              {RESOURCE_TYPES.map((x) => <option key={x} value={x}>{t(x)}</option>)}</select></label>
            <label>نام<input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required /></label>
            <label>ظرفیت<input value={form.capacity} onChange={(e) => setForm({ ...form, capacity: e.target.value })} inputMode="numeric" /></label>
            <label>عرض<input value={form.lat} onChange={(e) => setForm({ ...form, lat: e.target.value })} dir="ltr" /></label>
            <label>طول<input value={form.lng} onChange={(e) => setForm({ ...form, lng: e.target.value })} dir="ltr" /></label>
            <button className="btn primary" disabled={act.busy}>ثبت</button>
          </form>
          <ErrorBox error={act.error} />
        </Section>
      )}
    </>
  );
}
