import { useState } from "react";
import { Link } from "react-router-dom";
import { api, type Fulfillment, type Need } from "../lib/api";
import { useSession } from "../lib/session";
import { ago, fmtTime, num, NEED_CATEGORIES, NEED_UNITS, SEVERITIES, t } from "../lib/format";
import { Badge, Empty, ErrorBox, Freshness, ReasonAction, Section, useAction, usePoll } from "../components/ui";

const STATUSES = ["active", "", "open", "partially_met", "met", "cancelled"];
const statusLabel = (s: string) => (s === "active" ? "در انتظار تأمین" : s ? t(s) : "همه");

/** Supply progress; colour shows how much is still missing. */
export function Meter({ value, max, invert }: { value: number; max: number; invert?: boolean }) {
  const pct = max > 0 ? Math.min(100, Math.round((value / max) * 100)) : 0;
  const level = invert ? (pct >= 100 ? "danger" : pct >= 85 ? "warn" : "ok") : pct >= 100 ? "ok" : pct > 0 ? "warn" : "danger";
  return <div className={`meter ${level}`} title={`${num(pct)}٪`}><span style={{ width: `${pct}%` }} /></div>;
}

export function NeedsPage() {
  const [status, setStatus] = useState("active");
  const [category, setCategory] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const list = usePoll(() => api<{ items: Need[] }>("GET", "/needs", { query: { status, category } }), 15000, [status, category]);
  const items = list.data?.items ?? [];
  const critical = items.filter((n) => ["open", "partially_met"].includes(n.status) && n.priority === "critical").length;

  return (
    <div className="split">
      <Section title="نیازهای باز" actions={<Freshness at={list.updatedAt} />}>
        <div className="row gap wrap filters">
          <label>وضعیت
            <select value={status} onChange={(e) => setStatus(e.target.value)}>
              {STATUSES.map((s) => <option key={s} value={s}>{statusLabel(s)}</option>)}
            </select>
          </label>
          <label>نوع نیاز
            <select value={category} onChange={(e) => setCategory(e.target.value)}>
              <option value="">همه</option>
              {NEED_CATEGORIES.map((c) => <option key={c} value={c}>{t(c)}</option>)}
            </select>
          </label>
          {critical > 0 && <span className="warn">{num(critical)} نیاز بحرانی تأمین‌نشده</span>}
        </div>
        <ErrorBox error={list.error} />
        {list.data && items.length === 0 && <Empty>نیازی با این فیلتر ثبت نشده است. نیازها از صفحه هر حادثه ثبت می‌شوند.</Empty>}
        <table className="table">
          <thead><tr><th>اولویت</th><th>نیاز</th><th>حادثه</th><th>تأمین</th><th>باقی‌مانده</th><th>وضعیت</th><th>ثبت</th></tr></thead>
          <tbody>
            {items.map((n) => (
              <tr key={n.id} className={selected === n.id ? "sel" : ""} onClick={() => setSelected(n.id)}>
                <td><Badge value={n.priority} /></td>
                <td>{t(n.category)}{n.description && <div className="muted small">{n.description.slice(0, 60)}</div>}</td>
                <td className="small">{n.incident_code}</td>
                <td><Meter value={n.fulfilled} max={n.quantity} /><span className="small">{num(n.fulfilled)} از {num(n.quantity)} {n.unit}</span></td>
                <td>{num(n.remaining)} {n.unit}</td>
                <td><Badge value={n.status} /></td>
                <td title={fmtTime(n.created_at)}>{ago(n.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      {selected ? <NeedPanel id={selected} key={selected} onChanged={list.reload} />
        : <Section title="جزئیات نیاز"><Empty>یک نیاز را انتخاب کنید.</Empty></Section>}
    </div>
  );
}

function NeedPanel({ id, onChanged }: { id: string; onChanged: () => void }) {
  const { can } = useSession();
  const d = usePoll(() => api<{ need: Need; fulfillments: Fulfillment[] }>("GET", `/needs/${id}`), 15000, [id]);
  const [qty, setQty] = useState("");
  const [source, setSource] = useState("");
  const [note, setNote] = useState("");
  const act = useAction();
  if (!d.data) return <Section title="جزئیات نیاز"><ErrorBox error={d.error} /></Section>;
  const { need: n, fulfillments } = d.data;
  const active = ["open", "partially_met"].includes(n.status);
  const refresh = async () => { await d.reload(); onChanged(); };

  return (
    <Section title={<>{t(n.category)} <Badge value={n.status} /></>} actions={<Freshness at={d.updatedAt} />}>
      <dl className="kv">
        <dt>حادثه</dt><dd><Link to={`/incidents/${n.incident_id}`}>{n.incident_code}</Link> — {n.incident_title}</dd>
        <dt>اولویت</dt><dd><Badge value={n.priority} /></dd>
        <dt>مقدار</dt><dd>{num(n.quantity)} {n.unit} — تأمین‌شده {num(n.fulfilled)}، باقی‌مانده <b>{num(n.remaining)}</b></dd>
        <dt>شرح</dt><dd className="pre">{n.description || "—"}</dd>
        <dt>ثبت</dt><dd>{n.requested_by_name || "—"} · {fmtTime(n.created_at)}</dd>
        {n.cancel_reason && <><dt>دلیل لغو</dt><dd>{n.cancel_reason}</dd></>}
      </dl>
      <Meter value={n.fulfilled} max={n.quantity} />

      {active && can("need:fulfill") && (
        <form className="actions inline-form" onSubmit={(e) => {
          e.preventDefault();
          void act.run(async () => {
            await api("POST", `/needs/${id}/fulfillments`, { body: { quantity: Number(qty), source, note, version: n.version } });
            setQty(""); setNote(""); await refresh();
          });
        }}>
          <label>مقدار تأمین ({n.unit})
            <input type="number" min={1} max={n.remaining} value={qty} onChange={(e) => setQty(e.target.value)} required />
          </label>
          <label>تأمین‌کننده (سازمان، انبار یا واحد)
            <input value={source} onChange={(e) => setSource(e.target.value)} required maxLength={200} />
          </label>
          <label>توضیح
            <input value={note} onChange={(e) => setNote(e.target.value)} maxLength={1000} />
          </label>
          <button className="btn primary" disabled={act.busy || !qty || !source.trim()}>ثبت تأمین</button>
        </form>
      )}
      <ErrorBox error={act.error} />
      {active && can("need:create") && (
        <div className="actions">
          <ReasonAction label="لغو نیاز" danger onSubmit={async (reason) => {
            await api("POST", `/needs/${id}/cancel`, { body: { reason, version: n.version } });
            await refresh();
          }} />
        </div>
      )}

      <h3>سابقه تأمین</h3>
      {fulfillments.length === 0 && <Empty>هنوز چیزی تأمین نشده است.</Empty>}
      <ul className="timeline">
        {fulfillments.map((f) => (
          <li key={f.id}><b>{num(f.quantity)} {n.unit}</b> از «{f.source}» · {f.actor_name} · {fmtTime(f.created_at)}
            {f.note && <div className="muted small">{f.note}</div>}</li>
        ))}
      </ul>
    </Section>
  );
}

/** Needs of one incident, with the form to register a new one (incident page). */
export function IncidentNeeds({ incidentId, ownerActive }: { incidentId: string; ownerActive: boolean }) {
  const { can } = useSession();
  const list = usePoll(() => api<{ items: Need[] }>("GET", "/needs", { query: { incident_id: incidentId } }), 15000, [incidentId]);
  const [form, setForm] = useState({ category: "rescue_team", quantity: "", unit: NEED_UNITS.rescue_team, priority: "high", description: "" });
  const act = useAction();
  const items = list.data?.items ?? [];

  return (
    <Section title="نیازهای حادثه" actions={<Link className="btn small ghost" to="/needs">همه نیازها</Link>}>
      <ErrorBox error={list.error} />
      {list.data && items.length === 0 && <Empty>نیازی ثبت نشده است.</Empty>}
      <table className="table">
        <thead><tr><th>نیاز</th><th>اولویت</th><th>تأمین</th><th>وضعیت</th></tr></thead>
        <tbody>
          {items.map((n) => (
            <tr key={n.id}>
              <td>{t(n.category)}{n.description && <div className="muted small">{n.description.slice(0, 50)}</div>}</td>
              <td><Badge value={n.priority} /></td>
              <td><Meter value={n.fulfilled} max={n.quantity} /><span className="small">{num(n.fulfilled)} از {num(n.quantity)} {n.unit}</span></td>
              <td><Badge value={n.status} /></td>
            </tr>
          ))}
        </tbody>
      </table>
      {can("need:create") && ownerActive && (
        <form className="actions inline-form" onSubmit={(e) => {
          e.preventDefault();
          void act.run(async () => {
            await api("POST", `/incidents/${incidentId}/needs`, { body: { ...form, quantity: Number(form.quantity) } });
            setForm({ ...form, quantity: "", description: "" });
            await list.reload();
          });
        }}>
          <label>نوع نیاز
            <select value={form.category} onChange={(e) => setForm({ ...form, category: e.target.value, unit: NEED_UNITS[e.target.value] ?? form.unit })}>
              {NEED_CATEGORIES.map((c) => <option key={c} value={c}>{t(c)}</option>)}
            </select>
          </label>
          <label>مقدار
            <input type="number" min={1} value={form.quantity} onChange={(e) => setForm({ ...form, quantity: e.target.value })} required />
          </label>
          <label>واحد
            <input value={form.unit} onChange={(e) => setForm({ ...form, unit: e.target.value })} required maxLength={30} style={{ width: 80 }} />
          </label>
          <label>اولویت
            <select value={form.priority} onChange={(e) => setForm({ ...form, priority: e.target.value })}>
              {SEVERITIES.map((s) => <option key={s} value={s}>{t(s)}</option>)}
            </select>
          </label>
          <label>شرح {form.category === "other" ? "(الزامی)" : ""}
            <input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} maxLength={1000}
              required={form.category === "other"} />
          </label>
          <button className="btn primary" disabled={act.busy || !form.quantity}>ثبت نیاز</button>
        </form>
      )}
      <ErrorBox error={act.error} />
    </Section>
  );
}
