import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, newKey, type Incident, type IncidentDetail, type Report, type Resource } from "../lib/api";
import { useSession } from "../lib/session";
import { ago, fmtTime, num, REPORT_TYPES, SEVERITIES, t } from "../lib/format";
import { Badge, Empty, ErrorBox, Freshness, ReasonAction, Section, useAction, usePoll } from "../components/ui";

const STATUSES = ["", "draft", "open", "active", "escalated", "contained", "resolved", "closed"];
const LEVELS = ["L0", "L1", "L2", "L3", "L4"];
const LEVEL_LABEL: Record<string, string> = { L0: "L0 پایش", L1: "L1 بررسی", L2: "L2 آماده‌باش", L3: "L3 پاسخ", L4: "L4 گسترده" };
// Transitions that need commander approval (mirrors incident.RequiredPermission on the server).
const NEEDS_APPROVAL = (from: string, to: string) =>
  ["active", "escalated", "closed"].includes(to) || (from === "closed" && to === "open");

export function IncidentsPage() {
  const { can, orgs, me } = useSession();
  const [status, setStatus] = useState("");
  const list = usePoll(() => api<{ items: Incident[] }>("GET", "/incidents", { query: { status, limit: 100 } }), 10000, [status]);
  const [form, setForm] = useState({ type: "earthquake", title: "", severity: "high", owner_org_id: "", status: "draft", lat: "", lng: "" });
  const act = useAction();
  const defaultOrg = me?.grants.find((g) => g.scope_org_id)?.scope_org_id ?? orgs[0]?.id ?? "";

  return (
    <>
      <Section title="حوادث" actions={<Freshness at={list.updatedAt} />}>
        <label>وضعیت
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUSES.map((s) => <option key={s} value={s}>{s ? t(s) : "همه"}</option>)}
          </select>
        </label>
        <ErrorBox error={list.error} />
        {list.data?.items.length === 0 && <Empty>حادثه‌ای نیست.</Empty>}
        <table className="table">
          <thead><tr><th>کد</th><th>عنوان</th><th>وضعیت</th><th>شدت</th><th>سطح</th><th>گزارش‌ها</th><th>مأموریت فعال</th><th>آخرین تغییر</th></tr></thead>
          <tbody>
            {list.data?.items.map((i) => (
              <tr key={i.id}>
                <td><Link to={`/incidents/${i.id}`}>{i.code}</Link></td>
                <td>{i.title}</td>
                <td><Badge value={i.status} /></td>
                <td><Badge value={i.severity} /></td>
                <td>{LEVEL_LABEL[i.response_level]}</td>
                <td>{num(i.report_count)}</td>
                <td>{num(i.active_assignments)}</td>
                <td>{ago(i.updated_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      {can("incident:create") && (
        <Section title="ثبت حادثه جدید">
          <form className="grid-form" onSubmit={(e) => {
            e.preventDefault();
            void act.run(async () => {
              await api("POST", "/incidents", { idempotent: newKey(), body: {
                type: form.type, title: form.title, severity: form.severity, status: form.status,
                owner_org_id: form.owner_org_id || defaultOrg,
                location: form.lat && form.lng ? { lat: +form.lat, lng: +form.lng } : undefined } });
              setForm({ ...form, title: "" });
              await list.reload();
            });
          }}>
            <label>نوع<select value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value })}>
              {["earthquake", "aftershock", ...REPORT_TYPES].map((x) => <option key={x} value={x}>{t(x)}</option>)}</select></label>
            <label>عنوان<input value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} required maxLength={200} /></label>
            <label>شدت<select value={form.severity} onChange={(e) => setForm({ ...form, severity: e.target.value })}>
              {SEVERITIES.map((x) => <option key={x} value={x}>{t(x)}</option>)}</select></label>
            <label>سازمان مالک<select value={form.owner_org_id || defaultOrg} onChange={(e) => setForm({ ...form, owner_org_id: e.target.value })}>
              {orgs.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
            <label>وضعیت اولیه<select value={form.status} onChange={(e) => setForm({ ...form, status: e.target.value })}>
              <option value="draft">{t("draft")}</option><option value="open">{t("open")}</option></select></label>
            <label>عرض جغرافیایی<input value={form.lat} onChange={(e) => setForm({ ...form, lat: e.target.value })} inputMode="decimal" dir="ltr" /></label>
            <label>طول جغرافیایی<input value={form.lng} onChange={(e) => setForm({ ...form, lng: e.target.value })} inputMode="decimal" dir="ltr" /></label>
            <button className="btn primary" disabled={act.busy}>ثبت</button>
          </form>
          <ErrorBox error={act.error} />
        </Section>
      )}
    </>
  );
}

export function IncidentDetailPage() {
  const { id = "" } = useParams();
  const { can, orgName } = useSession();
  const d = usePoll(() => api<IncidentDetail>("GET", `/incidents/${id}`), 10000, [id]);
  const resources = usePoll(async () => (can("resource:allocate") ? api<{ items: Resource[] }>("GET", "/resources", { query: { status: "available" } }) : { items: [] }), 20000);
  const accepted = usePoll(async () => (can("incident:link_report") ? api<{ items: Report[] }>("GET", "/reports", { query: { status: "accepted", limit: 50 } }) : { items: [] }), 20000);
  const [assess, setAssess] = useState({ severity: "", level: "" });
  const [resId, setResId] = useState("");
  const [assignee, setAssignee] = useState("");
  const responders = usePoll(async () => (can("resource:allocate") ? api<{ items: { id: string; display_name: string; busy: boolean }[] }>("GET", "/responders") : { items: [] }), 30000);
  const [repId, setRepId] = useState("");
  const act = useAction();
  if (!d.data) return <Section title="حادثه"><ErrorBox error={d.error} /></Section>;
  const { incident: i, timeline, reports, assignments, alerts } = d.data;

  return (
    <>
      <Section title={<>{i.code} — {i.title} <Badge value={i.status} /></>} actions={<Freshness at={d.updatedAt} />}>
        <ErrorBox error={act.error} />
        <dl className="kv">
          <dt>نوع</dt><dd>{t(i.type)}</dd>
          <dt>شدت / سطح پاسخ</dt><dd><Badge value={i.severity} /> {LEVEL_LABEL[i.response_level]}</dd>
          <dt>سازمان مالک</dt><dd>{orgName(i.owner_org_id)}</dd>
          <dt>تشکیل</dt><dd>{fmtTime(i.opened_at)}</dd>
          <dt>نسخه</dt><dd>{i.version}</dd>
        </dl>
        <div className="actions">
          {i.allowed_transitions.map((to) => (
            (can(NEEDS_APPROVAL(i.status, to) ? "incident:approve" : "incident:transition")) && (
              <ReasonAction key={to} label={`انتقال به «${t(to)}»${NEEDS_APPROVAL(i.status, to) ? " (نیازمند فرمانده)" : ""}`}
                danger={to === "closed"}
                onSubmit={async (reason) => { await api("POST", `/incidents/${id}/transitions`, { body: { to, reason, version: i.version } }); await d.reload(); }} />
            )
          ))}
        </div>
        {can("incident:transition") && i.status !== "closed" && (
          <div className="actions">
            <label>شدت<select value={assess.severity || i.severity} onChange={(e) => setAssess({ ...assess, severity: e.target.value })}>
              {SEVERITIES.map((x) => <option key={x} value={x}>{t(x)}</option>)}</select></label>
            <label>سطح پاسخ<select value={assess.level || i.response_level} onChange={(e) => setAssess({ ...assess, level: e.target.value })}>
              {LEVELS.map((x) => <option key={x} value={x}>{LEVEL_LABEL[x]}</option>)}</select></label>
            <ReasonAction label="ثبت ارزیابی" onSubmit={async (reason) => {
              await api("POST", `/incidents/${id}/assessment`, { body: { severity: assess.severity || i.severity, response_level: assess.level || i.response_level, reason, version: i.version } });
              await d.reload();
            }} />
          </div>
        )}
      </Section>

      <div className="split">
        <Section title="مأموریت‌ها و منابع">
          {assignments.length === 0 && <Empty>منبعی تخصیص نیافته است.</Empty>}
          <table className="table">
            <thead><tr><th>منبع</th><th>نوع</th><th>وضعیت</th><th>زمان</th><th /></tr></thead>
            <tbody>
              {assignments.map((a) => (
                <tr key={a.id}>
                  <td>{a.resource_name}</td><td>{t(a.resource_type)}</td><td><Badge value={a.status} /></td><td>{ago(a.assigned_at)}</td>
                  <td>{can("resource:allocate") && !["completed", "cancelled"].includes(a.status) && (
                    <ReasonAction label="لغو و آزادسازی" danger onSubmit={async (reason) => {
                      await api("POST", `/assignments/${a.id}/status`, { body: { status: "cancelled", reason, version: a.version } });
                      await d.reload(); await resources.reload();
                    }} />)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {can("resource:allocate") && ["open", "active", "escalated", "contained"].includes(i.status) && (
            <div className="actions">
              <select value={resId} onChange={(e) => setResId(e.target.value)}>
                <option value="">منبع آماده…</option>
                {resources.data?.items.map((r) => (
                  <option key={r.id} value={r.id}>{t(r.type)} — {r.name}{r.stale ? " (موقعیت کهنه)" : ""}</option>
                ))}
              </select>
              <select value={assignee} onChange={(e) => setAssignee(e.target.value)}>
                <option value="">امدادگر مسئول (اختیاری)…</option>
                {responders.data?.items.map((u) => <option key={u.id} value={u.id}>{u.display_name}{u.busy ? " (در مأموریت)" : ""}</option>)}
              </select>
              {resId && <ReasonAction label="تخصیص" onSubmit={async (reason) => {
                await api("POST", `/incidents/${id}/assignments`, { body: { resource_id: resId, reason, assignee_id: assignee || undefined } });
                setResId(""); setAssignee(""); await d.reload(); await resources.reload();
              }} />}
            </div>
          )}
        </Section>

        <Section title="شواهد (گزارش‌های پیوندشده)">
          <ul className="list">
            {reports.map((r) => <li key={r.report_id}>{t(r.type)} <Badge value={r.relation_type} kind="none" /> — {r.reason} · {ago(r.linked_at)}</li>)}
          </ul>
          {can("incident:link_report") && i.status !== "closed" && (
            <div className="actions">
              <select value={repId} onChange={(e) => setRepId(e.target.value)}>
                <option value="">گزارش تأییدشده…</option>
                {accepted.data?.items.map((r) => <option key={r.id} value={r.id}>{t(r.type)} · {ago(r.received_at)} · {r.description.slice(0, 40)}</option>)}
              </select>
              {repId && <ReasonAction label="پیوند گزارش" onSubmit={async (reason) => {
                await api("POST", `/incidents/${id}/reports`, { body: { report_id: repId, reason } });
                setRepId(""); await d.reload(); await accepted.reload();
              }} />}
            </div>
          )}
          <h3>هشدارهای مرتبط</h3>
          <ul className="list">{alerts.map((a) => <li key={a.id}><Link to={`/alerts/${a.id}`}>{a.region_label}</Link> <Badge value={a.status} /> <Badge value={a.mode} /></li>)}</ul>
          {can("alert:draft") && <Link className="btn small" to={`/alerts/new?incident=${id}`}>پیش‌نویس هشدار برای این حادثه</Link>}
        </Section>
      </div>

      <Section title="تاریخچه ممیزی‌پذیر حادثه">
        <ul className="timeline">
          {timeline.map((e, k) => (
            <li key={k}><b>{e.event_type}</b> · {fmtTime(e.created_at)}
              {Boolean(e.payload.reason) && <> — دلیل: {String(e.payload.reason)}</>}
              {"from" in e.payload && <> ({t(String(e.payload.from))} ← {t(String(e.payload.to))})</>}
            </li>
          ))}
        </ul>
      </Section>
    </>
  );
}
