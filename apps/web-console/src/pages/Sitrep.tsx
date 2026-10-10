import { useState } from "react";
import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { fmtTime, num, t } from "../lib/format";
import { Empty, ErrorBox, Freshness, Section, useAction, usePoll } from "../components/ui";

type Counts = Record<string, number>;
export type Figures = {
  as_of: string; window_hours: number;
  reports: { new: number; awaiting_review: number; by_type: Counts };
  incidents: { active: number; new: number; by_severity: Counts; by_status: Counts };
  casualties: { total: number; by_triage: Counts; by_status: Counts; new: number };
  hospitals: { total: number; receiving: number; beds_available: number; icu_available: number; not_current: number };
  shelters: { total: number; open: number; occupancy: number; capacity: number; available_in_open: number };
  needs: { active: number; critical_active: number; remaining_by_category: Counts; met: number };
  resources: { by_status: Counts; active_assignments: number };
  damage: { red: number; yellow: number; green: number; people_trapped: number };
  alerts: { active: number; sent: number };
};
type Issued = { id: string; number: number; window_hours: number; figures: Figures; summary: string; issued_by: string; issued_at: string };

const CAS_STATUS: Record<string, string> = { on_scene: "در محل", transported: "در حال انتقال", admitted: "بستری", released: "ترخیص", deceased: "فوت" };

/** Value with the change since the previous issued report (▲/▼), when there is one. */
function V({ v, prev, bad }: { v: number; prev?: number; bad?: "up" | "down" }) {
  const d = prev === undefined ? 0 : v - prev;
  const cls = d === 0 ? "muted" : (bad === "up" && d > 0) || (bad === "down" && d < 0) ? "warn" : "ok-text";
  return <>{num(v)}{prev !== undefined && d !== 0 && <span className={`small ${cls}`}> ({d > 0 ? "▲" : "▼"}{num(Math.abs(d))})</span>}</>;
}

function Row({ label, v, prev, bad }: { label: string; v: number; prev?: number; bad?: "up" | "down" }) {
  return <tr><td>{label}</td><td><V v={v} prev={prev} bad={bad} /></td></tr>;
}

function Breakdown({ counts, label = t }: { counts: Counts; label?: (k: string) => string }) {
  const e = Object.entries(counts ?? {}).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
  return e.length ? <span className="small">{e.map(([k, n]) => `${label(k)}: ${num(n)}`).join("، ")}</span> : <span className="muted small">—</span>;
}

/** The report body; shared by the live view and issued reports so both read the same. */
export function SitrepBody({ f, prev }: { f: Figures; prev?: Figures }) {
  return (
    <div className="sitrep">
      <div className="sitrep-grid">
        <table className="table"><caption>حوادث و گزارش‌ها</caption><tbody>
          <Row label="حادثه فعال" v={f.incidents.active} prev={prev?.incidents.active} bad="up" />
          <Row label={`حادثه جدید (${num(f.window_hours)} ساعت)`} v={f.incidents.new} prev={prev?.incidents.new} bad="up" />
          <tr><td>شدت حوادث فعال</td><td><Breakdown counts={f.incidents.by_severity} /></td></tr>
          <Row label={`گزارش جدید (${num(f.window_hours)} ساعت)`} v={f.reports.new} prev={prev?.reports.new} />
          <Row label="گزارش در انتظار بررسی" v={f.reports.awaiting_review} prev={prev?.reports.awaiting_review} bad="up" />
          <tr><td>نوع گزارش‌های جدید</td><td><Breakdown counts={f.reports.by_type} /></td></tr>
        </tbody></table>
        <table className="table"><caption>مصدومان و درمان</caption><tbody>
          <Row label="کل مصدومان ثبت‌شده" v={f.casualties.total} prev={prev?.casualties.total} bad="up" />
          <tr><td>بر اساس تریاژ</td><td><Breakdown counts={f.casualties.by_triage} /></td></tr>
          <tr><td>وضعیت</td><td><Breakdown counts={f.casualties.by_status} label={(k) => CAS_STATUS[k] ?? k} /></td></tr>
          <Row label="بیمارستان پذیرنده" v={f.hospitals.receiving} prev={prev?.hospitals.receiving} bad="down" />
          <Row label="تخت خالی (مراکز پذیرنده)" v={f.hospitals.beds_available} prev={prev?.hospitals.beds_available} bad="down" />
          <Row label="ICU خالی" v={f.hospitals.icu_available} prev={prev?.hospitals.icu_available} bad="down" />
          <Row label="بیمارستان بدون گزارش به‌روز" v={f.hospitals.not_current} prev={prev?.hospitals.not_current} bad="up" />
        </tbody></table>
        <table className="table"><caption>اسکان و نیازها</caption><tbody>
          <Row label="اسکان‌یافته" v={f.shelters.occupancy} prev={prev?.shelters.occupancy} />
          <Row label="جای خالی (محل‌های باز)" v={f.shelters.available_in_open} prev={prev?.shelters.available_in_open} bad="down" />
          <tr><td>محل اسکان باز / کل</td><td>{num(f.shelters.open)} / {num(f.shelters.total)}</td></tr>
          <Row label="نیاز در انتظار تأمین" v={f.needs.active} prev={prev?.needs.active} bad="up" />
          <Row label="نیاز بحرانی در انتظار" v={f.needs.critical_active} prev={prev?.needs.critical_active} bad="up" />
          <tr><td>کمبود باقی‌مانده</td><td><Breakdown counts={f.needs.remaining_by_category} /></td></tr>
        </tbody></table>
        <table className="table"><caption>منابع، خسارت و هشدار</caption><tbody>
          <Row label="مأموریت فعال" v={f.resources.active_assignments} prev={prev?.resources.active_assignments} />
          <tr><td>وضعیت منابع</td><td><Breakdown counts={f.resources.by_status} /></td></tr>
          <Row label="ساختمان قرمز (ناایمن)" v={f.damage.red} prev={prev?.damage.red} bad="up" />
          <Row label="ساختمان زرد" v={f.damage.yellow} prev={prev?.damage.yellow} bad="up" />
          <Row label="ساختمان با احتمال افراد محبوس" v={f.damage.people_trapped} prev={prev?.damage.people_trapped} bad="up" />
          <Row label="هشدار عمومی فعال" v={f.alerts.active} prev={prev?.alerts.active} />
        </tbody></table>
      </div>
      <p className="muted small">ارقام شمارش کل سازمان‌ها و بدون داده شخصی است؛ زمان: {fmtTime(f.as_of)}. تغییرات داخل پرانتز نسبت به آخرین گزارش صادرشده است.</p>
    </div>
  );
}

export function SitrepPage() {
  const { can } = useSession();
  const [hours, setHours] = useState(24);
  const [view, setView] = useState<string | null>(null);
  const [summary, setSummary] = useState("");
  const live = usePoll(() => api<Figures>("GET", "/sitrep", { query: { window_hours: hours } }), 30000, [hours]);
  const issued = usePoll(() => api<{ items: Issued[] }>("GET", "/sitreps"), 60000);
  const act = useAction();
  const items = issued.data?.items ?? [];
  const shown = view ? items.find((s) => s.id === view) : undefined;
  const prevOf = (s?: Issued) => (s ? items.find((x) => x.number < s.number)?.figures : items[0]?.figures);

  return (
    <div className="split">
      <Section title={shown ? `گزارش وضعیت شماره ${num(shown.number)}` : "گزارش وضعیت (لحظه‌ای)"} actions={<>
        {!shown && <Freshness at={live.updatedAt} />}
        {shown && <button className="btn small" onClick={() => setView(null)}>بازگشت به وضعیت لحظه‌ای</button>}
        <button className="btn small" onClick={() => window.print()}>چاپ</button>
      </>}>
        {shown ? (
          <>
            <p className="small">صادرکننده: {shown.issued_by} · {fmtTime(shown.issued_at)} · بازه {num(shown.window_hours)} ساعت</p>
            <div className="sitrep-summary pre">{shown.summary}</div>
            <SitrepBody f={shown.figures} prev={prevOf(shown)} />
          </>
        ) : (
          <>
            <div className="row gap wrap filters no-print">
              <label>بازه شمارش رخدادهای جدید
                <select value={hours} onChange={(e) => setHours(+e.target.value)}>
                  {[6, 12, 24, 72].map((h) => <option key={h} value={h}>{num(h)} ساعت</option>)}
                </select>
              </label>
            </div>
            <ErrorBox error={live.error} />
            {live.data && <SitrepBody f={live.data} prev={items[0]?.figures} />}
            {can("sitrep:issue") && live.data && (
              <form className="actions no-print" onSubmit={(e) => {
                e.preventDefault();
                void act.run(async () => {
                  const s = await api<Issued>("POST", "/sitreps", { body: { window_hours: hours, summary } });
                  setSummary(""); await issued.reload(); setView(s.id);
                });
              }}>
                <label style={{ flex: "1 1 100%" }}>ارزیابی فرمانده (الزامی): وضعیت کلی، اولویت‌های دوره بعد، درخواست‌ها
                  <textarea rows={4} value={summary} onChange={(e) => setSummary(e.target.value)} maxLength={5000} required />
                </label>
                <button className="btn primary" disabled={act.busy || !summary.trim()}>صدور گزارش وضعیت شماره {num((items[0]?.number ?? 0) + 1)}</button>
                <span className="muted small">ارقام همان لحظه صدور توسط سرور ثبت و قفل می‌شود و قابل ویرایش نیست.</span>
              </form>
            )}
            <ErrorBox error={act.error} />
          </>
        )}
      </Section>
      <Section title="گزارش‌های صادرشده">
        <ErrorBox error={issued.error} />
        {issued.data && items.length === 0 && <Empty>هنوز گزارشی صادر نشده است.</Empty>}
        <ul className="list">
          {items.map((s) => (
            <li key={s.id}>
              <button className={`btn small ${view === s.id ? "primary" : "ghost"}`} onClick={() => setView(s.id)}>شماره {num(s.number)}</button>
              {" "}{fmtTime(s.issued_at)} · {s.issued_by}
              <div className="muted small">{s.summary.slice(0, 90)}{s.summary.length > 90 ? "…" : ""}</div>
            </li>
          ))}
        </ul>
      </Section>
    </div>
  );
}
