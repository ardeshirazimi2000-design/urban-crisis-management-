import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, newKey, type Incident, type Report, type ReportDetail } from "../lib/api";
import { useSession } from "../lib/session";
import { ago, fmtTime, num, REPORT_TYPES, SEVERITIES, t } from "../lib/format";
import { Badge, Empty, ErrorBox, Freshness, ReasonAction, Section, useAction, usePoll } from "../components/ui";
import { MapView } from "../components/MapView";

const QUEUE_STATUSES = ["", "received", "triage", "under_review", "accepted", "rejected", "duplicate", "linked_to_incident"];

export function ReportsPage() {
  const [status, setStatus] = useState("received");
  const [type, setType] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const list = usePoll(() => api<{ items: Report[]; next_cursor?: string }>("GET", "/reports", { query: { status, type, limit: 100 } }), 10000, [status, type]);

  return (
    <div className="split">
      <Section title="صف بررسی گزارش‌ها" actions={<Freshness at={list.updatedAt} />}>
        <div className="row gap wrap filters">
          <label>وضعیت
            <select value={status} onChange={(e) => setStatus(e.target.value)}>
              {QUEUE_STATUSES.map((s) => <option key={s} value={s}>{s ? t(s) : "همه"}</option>)}
            </select>
          </label>
          <label>نوع
            <select value={type} onChange={(e) => setType(e.target.value)}>
              <option value="">همه</option>
              {REPORT_TYPES.map((s) => <option key={s} value={s}>{t(s)}</option>)}
            </select>
          </label>
        </div>
        <ErrorBox error={list.error} />
        {list.data?.items.length === 0 && <Empty>گزارشی در این وضعیت نیست.</Empty>}
        <table className="table">
          <thead><tr><th>نوع</th><th>وضعیت</th><th>شدت</th><th>دریافت</th><th>دقت مکان</th><th>رسانه</th><th>AI</th></tr></thead>
          <tbody>
            {list.data?.items.map((r) => (
              <tr key={r.id} className={selected === r.id ? "sel" : ""} onClick={() => setSelected(r.id)}>
                <td>{t(r.type)}</td>
                <td><Badge value={r.status} /></td>
                <td>{r.severity ? <Badge value={r.severity} /> : "—"}</td>
                <td title={fmtTime(r.received_at)}>{ago(r.received_at)}</td>
                <td>{num(r.location.accuracy_m)} م</td>
                <td>{num(r.media_count)}</td>
                <td>{r.enrichment_status === "pending" ? <span className="muted">در انتظار</span> : t(r.enrichment_status)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      {selected ? <ReportPanel id={selected} onChanged={list.reload} key={selected} /> : <Section title="جزئیات"><Empty>یک گزارش را انتخاب کنید.</Empty></Section>}
    </div>
  );
}

function ReportPanel({ id, onChanged }: { id: string; onChanged: () => void }) {
  const { can, orgs, me } = useSession();
  const nav = useNavigate();
  const d = usePoll(() => api<ReportDetail>("GET", `/reports/${id}`), 15000, [id]);
  const related = usePoll(() => api<{ items: (Report & { distance_m: number; same_type: boolean })[] }>("GET", `/reports/${id}/related`), 0, [id]);
  const act = useAction();
  const [severity, setSeverity] = useState("");
  const [dupOf, setDupOf] = useState("");
  const r = d.data;
  if (!r) return <Section title="جزئیات"><ErrorBox error={d.error} /></Section>;

  const refresh = async () => { await d.reload(); onChanged(); };
  const review = (decision: string) => async (reason: string) => {
    await api("POST", `/reports/${id}/review`, { body: { decision, reason, version: r.version, severity, duplicate_of: decision === "duplicate" ? dupOf : undefined } });
    await refresh();
  };
  const reviewable = ["received", "triage", "under_review"].includes(r.status);
  const myOrg = me?.grants.find((g) => g.scope_org_id)?.scope_org_id ?? orgs.find((o) => o.code === "command")?.id;

  return (
    <Section title={<>گزارش {t(r.type)} <Badge value={r.status} /></>} actions={<Freshness at={d.updatedAt} />}>
      <ErrorBox error={act.error} />
      <dl className="kv">
        <dt>شرح</dt><dd className="pre">{r.description || "—"}</dd>
        <dt>زمان ادعایی رخداد</dt><dd>{fmtTime(r.occurred_at)} <span className="muted small">(ساعت دستگاه؛ قطعی نیست)</span></dd>
        <dt>زمان دریافت</dt><dd>{fmtTime(r.received_at)}</dd>
        <dt>موقعیت</dt><dd>{r.location.lat.toFixed(5)}, {r.location.lng.toFixed(5)} — دقت {num(r.location.accuracy_m)} متر ({r.location.precision === "exact" ? "دقیق" : "تقریبی"}، منبع: {r.location.source})</dd>
        {r.review_reason && <><dt>دلیل تصمیم</dt><dd>{r.review_reason}</dd></>}
      </dl>
      <MapView height={220} center={[r.location.lat, r.location.lng]} zoom={15}
        features={[{ type: "Feature", id: r.id, geometry: { type: "Point", coordinates: [r.location.lng, r.location.lat] }, properties: { layer: "reports", status: r.status, report_type: r.type } }]} />

      {r.ai_scores.length > 0 && (
        <div className="aux">
          <strong>سیگنال کمکی هوش مصنوعی</strong> <span className="muted small">— جایگزین قضاوت انسانی نیست</span>
          {r.ai_scores.map((s) => (
            <div key={s.model_version} className="small">
              مدل {s.model_version}: نوع پیشنهادی {t(s.predicted_type)} (سهم شواهد {num((s.type_confidence ?? 0) * 100)}٪)،
              سیگنال فوریت {num((s.urgency_signal ?? 0) * 100)}٪
              {Boolean((s.signals as Record<string, unknown>).type_disagreement) && <span className="warn"> · با نوع اعلامی شهروند متفاوت است</span>}
            </div>
          ))}
        </div>
      )}

      {r.media.length > 0 && (
        <div className="row gap wrap">
          {r.media.map((m) => (
            <button key={m.media_id} className="btn small" onClick={() => act.run(async () => {
              const u = await api<{ url: string }>("GET", `/media/${m.media_id}/url`);
              window.open(u.url, "_blank", "noopener");
            })}>
              {m.content_type} ({num(m.bytes / 1024)}KB) — اسکن: {m.scan_status}
            </button>
          ))}
        </div>
      )}

      {can("report:review") && reviewable && (
        <div className="actions">
          {r.status !== "under_review" && (
            <button className="btn" onClick={() => act.run(async () => { await api("POST", `/reports/${id}/claim`); await refresh(); })}>
              برداشتن برای بررسی
            </button>
          )}
          <label>شدت
            <select value={severity} onChange={(e) => setSeverity(e.target.value)}>
              <option value="">—</option>
              {SEVERITIES.map((s) => <option key={s} value={s}>{t(s)}</option>)}
            </select>
          </label>
          <ReasonAction label="تأیید گزارش" required={false} onSubmit={review("accepted")} />
          <ReasonAction label="رد گزارش" danger onSubmit={review("rejected")} />
          <ReasonAction label="علامت تکراری" onSubmit={review("duplicate")}
            extra={<select value={dupOf} onChange={(e) => setDupOf(e.target.value)} required>
              <option value="">گزارش اصلی…</option>
              {related.data?.items.map((x) => <option key={x.id} value={x.id}>{t(x.type)} · {num(x.distance_m)} م · {ago(x.received_at)}</option>)}
            </select>} />
        </div>
      )}

      {can("incident:create") && r.status === "accepted" && (
        <div className="actions">
          <ReasonAction label="تشکیل حادثه از این گزارش" onSubmit={async (reason) => {
            const inc = await api<Incident>("POST", "/incidents", { idempotent: newKey(), body: {
              type: r.type, title: `${t(r.type)} — ${r.location.lat.toFixed(3)},${r.location.lng.toFixed(3)}`, severity: r.severity ?? "medium",
              owner_org_id: myOrg, location: { lat: r.location.lat, lng: r.location.lng }, report_ids: [r.id], status: "open", reason } });
            nav(`/incidents/${inc.id}`);
          }} />
        </div>
      )}

      <h3>گزارش‌های نزدیک (۳۰۰ متر، ±۶ ساعت)</h3>
      {related.data?.items.length === 0 && <Empty>موردی یافت نشد.</Empty>}
      <ul className="list">
        {related.data?.items.map((x) => (
          <li key={x.id}>{t(x.type)} <Badge value={x.status} /> · {num(x.distance_m)} متر · {ago(x.received_at)} {x.same_type && <em>(هم‌نوع)</em>}</li>
        ))}
      </ul>

      {r.incidents.length > 0 && (<><h3>حوادث مرتبط</h3><ul className="list">{r.incidents.map((i) => <li key={i.id}><a href={`/incidents/${i.id}`}>{i.code}</a> <Badge value={i.status} /></li>)}</ul></>)}

      <h3>تاریخچه</h3>
      <ul className="timeline">
        {r.history.map((h, i) => <li key={i}><b>{h.event_type}</b> · {fmtTime(h.created_at)} <code className="small" dir="ltr" style={{ display: "block" }}>{JSON.stringify(h.payload)}</code></li>)}
      </ul>
    </Section>
  );
}
