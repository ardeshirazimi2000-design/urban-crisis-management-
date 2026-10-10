import { useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { api, newKey, type Alert, type Delivery, type Template } from "../lib/api";
import { useSession } from "../lib/session";
import { ago, circlePolygon, fmtTime, num, t, tAttempt } from "../lib/format";
import { Badge, Empty, ErrorBox, Freshness, ReasonAction, Section, useAction, usePoll } from "../components/ui";
import { MapView } from "../components/MapView";

export function AlertsPage() {
  const { can } = useSession();
  const [status, setStatus] = useState("");
  const list = usePoll(() => api<{ items: Alert[] }>("GET", "/alerts", { query: { status } }), 10000, [status]);
  return (
    <Section title="هشدارهای عمومی" actions={<>
      <Freshness at={list.updatedAt} />
      {can("alert:draft") && <Link className="btn primary small" to="/alerts/new">پیش‌نویس جدید</Link>}
    </>}>
      <label>وضعیت<select value={status} onChange={(e) => setStatus(e.target.value)}>
        <option value="">همه</option>
        {["draft", "pending_approval", "approved", "sending", "partially_sent", "sent", "failed", "cancelled", "expired"].map((s) => <option key={s} value={s}>{t(s)}</option>)}
      </select></label>
      <ErrorBox error={list.error} />
      {list.data?.items.length === 0 && <Empty>هشداری نیست.</Empty>}
      <table className="table">
        <thead><tr><th>محدوده</th><th>نوع پیام</th><th>شدت</th><th>وضعیت</th><th>کانال‌ها</th><th>انقضا</th></tr></thead>
        <tbody>
          {list.data?.items.map((a) => (
            <tr key={a.id}>
              <td><Link to={`/alerts/${a.id}`}>{a.region_label}</Link></td>
              <td><Badge value={a.mode} /></td><td><Badge value={a.severity} /></td><td><Badge value={a.status} /></td>
              <td>{a.channels.map(t).join("، ")}</td><td>{fmtTime(a.expires_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  );
}

/** Template parameter names (API keys) shown in Persian. */
const PARAM_LABEL: Record<string, string> = {
  instructions: "دستورالعمل برای مردم", shelter: "نام و نشانی محل اسکان", road: "نام مسیر",
};

export function NewAlertPage() {
  const { orgs, me } = useSession();
  const [sp] = useSearchParams();
  const nav = useNavigate();
  const templates = usePoll(() => api<{ items: Template[]; channel_max_runes: Record<string, number> }>("GET", "/alerts/templates"), 0);
  const defaultOrg = me?.grants.find((g) => g.scope_org_id)?.scope_org_id ?? orgs[0]?.id ?? "";
  const [f, setF] = useState({ mode: "test", severity: "warning", issuer_org_id: "", template: "earthquake_aftershock_advisory:1",
    region_label: "", lat: "35.70", lng: "51.40", radius_km: "3", hours: "6", channels: ["internal", "push"] as string[] });
  const [params, setParams] = useState<Record<string, string>>({});
  // One key per form instance: double-submits create exactly one draft.
  const key = useRef(newKey());
  const act = useAction();
  const tpl = templates.data?.items.find((x) => `${x.code}:${x.version}` === f.template);
  const region = useMemo(() => circlePolygon(+f.lat, +f.lng, +f.radius_km * 1000), [f.lat, f.lng, f.radius_km]);

  return (
    <Section title="پیش‌نویس هشدار">
      <p className="muted">ایجاد پیش‌نویس هیچ پیامی ارسال نمی‌کند. ارسال فقط پس از تأیید فردی غیر از تهیه‌کننده ممکن است.</p>
      <form className="grid-form" onSubmit={(e) => {
        e.preventDefault();
        void act.run(async () => {
          const [code, ver] = f.template.split(":");
          const a = await api<Alert>("POST", "/alerts", { idempotent: key.current, body: {
            incident_id: sp.get("incident") || undefined, mode: f.mode, severity: f.severity,
            issuer_org_id: f.issuer_org_id || defaultOrg, region, region_label: f.region_label,
            template_code: code, template_version: +ver, params, channels: f.channels,
            expires_at: new Date(Date.now() + +f.hours * 3600_000).toISOString() } });
          nav(`/alerts/${a.id}`);
        });
      }}>
        <label>نوع پیام<select value={f.mode} onChange={(e) => setF({ ...f, mode: e.target.value })}>
          <option value="test">آزمایشی</option><option value="operational">عملیاتی</option></select></label>
        <label>شدت<select value={f.severity} onChange={(e) => setF({ ...f, severity: e.target.value })}>
          {["advisory", "watch", "warning", "emergency"].map((x) => <option key={x} value={x}>{t(x)}</option>)}</select></label>
        <label>صادرکننده<select value={f.issuer_org_id || defaultOrg} onChange={(e) => setF({ ...f, issuer_org_id: e.target.value })}>
          {orgs.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
        <label>قالب<select value={f.template} onChange={(e) => { setF({ ...f, template: e.target.value }); setParams({}); }}>
          {templates.data?.items.map((x) => <option key={`${x.code}:${x.version}`} value={`${x.code}:${x.version}`}>{x.title} (نسخه {x.version})</option>)}</select></label>
        {tpl?.params?.map((p) => (
          <label key={p}>{PARAM_LABEL[p] ?? p}<input value={params[p] ?? ""} onChange={(e) => setParams({ ...params, [p]: e.target.value })} required maxLength={300} /></label>
        ))}
        <label>نام محدوده<input value={f.region_label} onChange={(e) => setF({ ...f, region_label: e.target.value })} required maxLength={120} /></label>
        <label>مرکز (عرض)<input value={f.lat} onChange={(e) => setF({ ...f, lat: e.target.value })} dir="ltr" /></label>
        <label>مرکز (طول)<input value={f.lng} onChange={(e) => setF({ ...f, lng: e.target.value })} dir="ltr" /></label>
        <label>شعاع (کیلومتر)<input value={f.radius_km} onChange={(e) => setF({ ...f, radius_km: e.target.value })} dir="ltr" /></label>
        <label>اعتبار (ساعت)<input value={f.hours} onChange={(e) => setF({ ...f, hours: e.target.value })} dir="ltr" /></label>
        <fieldset className="chk-group"><legend>کانال‌ها</legend>
          {["internal", "push", "sms", "cell_broadcast"].map((c) => (
            <label key={c} className="chk"><input type="checkbox" checked={f.channels.includes(c)}
              onChange={(e) => setF({ ...f, channels: e.target.checked ? [...f.channels, c] : f.channels.filter((x) => x !== c) })} />
              {t(c)} {templates.data && <span className="muted small">(حداکثر {num(templates.data.channel_max_runes[c])} نویسه)</span>}
            </label>
          ))}
        </fieldset>
        <button className="btn primary" disabled={act.busy || f.channels.length === 0}>ذخیره پیش‌نویس</button>
      </form>
      <ErrorBox error={act.error} />
      <MapView height={300} center={[+f.lat || 35.7, +f.lng || 51.4]} zoom={11}
        features={[{ type: "Feature", id: "preview", geometry: region as never, properties: { layer: "impact_areas", name: f.region_label } }]} />
    </Section>
  );
}

type Preview = { rendered_text: string; is_test: boolean; channels: Record<string, { runes: number; max_runes: number; note: string }>;
  region: { label: string; area_km2: number; centroid: { lat: number; lng: number }; geojson: unknown }; issuer: string; expires_at: string;
  template: { code: string; version: number } };

export function AlertDetailPage() {
  const { id = "" } = useParams();
  const { can, me } = useSession();
  const a = usePoll(() => api<Alert>("GET", `/alerts/${id}`), 5000, [id]);
  const p = usePoll(() => api<Preview>("GET", `/alerts/${id}/preview`), 0, [id]);
  const delivery = usePoll(async () => (can("alert:read_delivery") ? api<Delivery>("GET", `/alerts/${id}/delivery`) : null), 5000, [id]);
  const dispatchKey = useRef(newKey());
  const act = useAction();
  const al = a.data;
  if (!al) return <Section title="هشدار"><ErrorBox error={a.error} /></Section>;
  const isAuthor = al.created_by === me?.user_id || al.submitted_by === me?.user_id;

  return (
    <>
      <Section title={<>هشدار «{al.region_label}» <Badge value={al.status} /> <Badge value={al.mode} /></>} actions={<Freshness at={a.updatedAt} />}>
        {al.mode === "test" && <div className="banner info">این پیام آزمایشی است و با برچسب «آزمایشی» ارسال می‌شود.</div>}
        {al.mode === "operational" && <div className="banner warn">پیام عملیاتی — به شهروندان واقعی ارسال می‌شود.</div>}
        <ErrorBox error={act.error} />
        {p.data && (
          <div className="preview">
            <h3>پیش‌نمایش دقیق متن ارسالی</h3>
            <blockquote>{p.data.rendered_text}</blockquote>
            <ul className="list small">
              {Object.entries(p.data.channels).map(([c, v]) => (
                <li key={c}>{t(c)}: {num(v.runes)}/{num(v.max_runes)} نویسه {v.note && <span className="muted">— {v.note}</span>}</li>
              ))}
            </ul>
            <p className="small">صادرکننده: {p.data.issuer} · قالب {p.data.template.code} نسخه {p.data.template.version} · مساحت محدوده {num(p.data.region.area_km2, 1)} km² · انقضا {fmtTime(p.data.expires_at)}</p>
            <MapView height={260} center={[p.data.region.centroid.lat, p.data.region.centroid.lng]} zoom={11}
              features={[{ type: "Feature", id: "r", geometry: p.data.region.geojson as never, properties: { layer: "impact_areas", name: al.region_label } }]} />
          </div>
        )}
        <dl className="kv">
          <dt>تأییدکننده</dt><dd>{al.approved_by ? `${al.approved_by.slice(0, 8)} — ${al.approval_reason ?? ""}` : "—"}</dd>
          <dt>صدور</dt><dd>{fmtTime(al.issued_at)}</dd>
          {al.cancel_reason && <><dt>دلیل لغو</dt><dd>{al.cancel_reason}</dd></>}
        </dl>
        <div className="actions">
          {al.status === "draft" && can("alert:draft") && (
            <button className="btn" disabled={act.busy} onClick={() => act.run(async () => { await api("POST", `/alerts/${id}/submit`, { body: { version: al.version } }); await a.reload(); })}>
              ارسال برای تأیید
            </button>
          )}
          {al.status === "pending_approval" && can("alert:approve") && (
            isAuthor ? <span className="muted">تأیید باید توسط فردی غیر از تهیه‌کننده انجام شود (اصل چهارچشم).</span> :
            <ReasonAction label="تأیید هشدار" onSubmit={async (reason) => { await api("POST", `/alerts/${id}/approve`, { body: { reason, version: al.version } }); await a.reload(); }} />
          )}
          {al.status === "approved" && can("alert:dispatch") && (
            <button className="btn primary" disabled={act.busy} onClick={() => {
              if (!window.confirm(al.mode === "operational" ? "پیام عملیاتی به شهروندان ارسال شود؟" : "پیام آزمایشی ارسال شود؟")) return;
              void act.run(async () => { await api("POST", `/alerts/${id}/dispatch`, { idempotent: dispatchKey.current }); await a.reload(); await delivery.reload(); });
            }}>ارسال</button>
          )}
          {["draft", "pending_approval", "approved", "sending"].includes(al.status) && (can("alert:cancel") || (al.status === "draft" && can("alert:draft"))) && (
            <ReasonAction label="لغو" danger onSubmit={async (reason) => { await api("POST", `/alerts/${id}/cancel`, { body: { reason, version: al.version } }); await a.reload(); }} />
          )}
          {["sent", "partially_sent"].includes(al.status) && can("alert:draft") && (
            <Link className="btn" to={`/alerts/new${al.incident_id ? `?incident=${al.incident_id}` : ""}`}>صدور اصلاحیه</Link>
          )}
        </div>
      </Section>
      {delivery.data && (
        <Section title="وضعیت تحویل به تفکیک کانال" actions={<Freshness at={delivery.updatedAt} />}>
          <p className="muted small">{delivery.data.note}</p>
          <table className="table">
            <thead><tr><th>کانال</th><th>وضعیت</th><th>تلاش‌ها</th></tr></thead>
            <tbody>
              {delivery.data.channels.map((c) => (
                <tr key={c.channel}>
                  <td>{t(c.channel)}</td><td><span className={`badge badge-${c.status}`}>{tAttempt(c.status)}</span></td>
                  <td className="small">{c.attempts.map((x) => `#${x.attempt_no} ${tAttempt(x.status)}${x.result_code ? ` (${x.result_code})` : ""} ${x.attempted_at ? ago(x.attempted_at) : ""}`).join(" · ")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}
    </>
  );
}
