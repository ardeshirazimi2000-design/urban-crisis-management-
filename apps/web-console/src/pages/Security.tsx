import { Fragment, useState } from "react";
import { api, ApiError, download, saveBlob } from "../lib/api";
import { useSession } from "../lib/session";
import { fmtTime, num, t } from "../lib/format";
import { ErrorBox, ReasonAction, Section, useAction, usePoll } from "../components/ui";

type Setting = { key: string; title: string; value: unknown; editable: boolean; managed_by: string };
type AuditRow = {
  seq: number; actor_id: string | null; actor_roles: string[]; action: string; target_type: string; target_id: string; outcome: string;
  reason: string; created_at: string; correlation_id: string; severity: string; ip: string; channel: string; user_agent: string;
  details: Record<string, unknown>;
};
type Notification = { id: number; kind: string; severity: string; message: string; created_at: string };
type IntegrityResult = { checked: number; violations: { record_type: string; record_id: string; reason: string }[]; AuditChain: { checked: number; valid: boolean; broken_seq?: number; problem?: string }; at: string };
type AuditStatus = {
  rows: number; warn_rows: number; max_rows: number; full_action: string; blocking: boolean; last_integrity_check: IntegrityResult | null;
  archives: { id: number; first_seq: number; last_seq: number; row_count: number; digest: string; archived_by: string | null; created_at: string }[] | null;
};

export function SecurityPage() {
  const { can } = useSession();
  return (
    <>
      {(can("settings:manage") || can("audit:read")) && <Notifications />}
      {(can("settings:manage") || can("audit:configure")) && <Settings />}
      {can("integrity:check") && <Integrity />}
      {can("audit:read") && <AuditStatusCard />}
      {can("audit:read") && <Audit />}
    </>
  );
}

function Notifications() {
  const data = usePoll(() => api<{ items: Notification[] }>("GET", "/admin/notifications"), 60000);
  const items = data.data?.items ?? [];
  if (!items.length && !data.error) return null;
  return (
    <Section title="هشدارهای امنیتی">
      <ErrorBox error={data.error} />
      <ul className="plain">
        {items.slice(0, 10).map((n) => (
          <li key={n.id} className={n.severity === "critical" ? "warn" : ""}>{fmtTime(n.created_at)} — {n.message}</li>
        ))}
      </ul>
    </Section>
  );
}

function SettingValue({ s, onSave }: { s: Setting; onSave: (v: unknown, reason: string) => Promise<void> }) {
  const [draft, setDraft] = useState(() => JSON.stringify(s.value));
  const [reason, setReason] = useState("");
  const act = useAction();
  const dirty = draft !== JSON.stringify(s.value);
  let input;
  if (typeof s.value === "boolean")
    input = <select value={draft} disabled={!s.editable} onChange={(e) => setDraft(e.target.value)}><option value="true">بله</option><option value="false">خیر</option></select>;
  else if (typeof s.value === "number")
    input = <input type="number" dir="ltr" disabled={!s.editable} value={draft} onChange={(e) => setDraft(e.target.value)} />;
  else if (typeof s.value === "string" && s.key !== "access.banner")
    input = <input dir="ltr" disabled={!s.editable} value={JSON.parse(draft)} onChange={(e) => setDraft(JSON.stringify(e.target.value))} />;
  else if (s.key === "access.banner")
    input = <textarea disabled={!s.editable} value={JSON.parse(draft)} rows={2} onChange={(e) => setDraft(JSON.stringify(e.target.value))} />;
  else
    input = <input dir="ltr" disabled={!s.editable} value={draft} onChange={(e) => setDraft(e.target.value)} title="JSON" />;
  return (
    <tr>
      <td>{s.title}<div className="muted small" dir="ltr">{s.key}</div></td>
      <td>{input}</td>
      <td>{s.editable && dirty && (
        <form className="row gap" onSubmit={(e) => {
          e.preventDefault();
          void act.run(async () => {
            let v: unknown;
            try { v = JSON.parse(draft); } catch { throw new ApiError(0, "INVALID_JSON", "قالب مقدار نادرست است", ""); }
            await onSave(v, reason);
            setReason("");
          });
        }}>
          <input placeholder="دلیل" value={reason} onChange={(e) => setReason(e.target.value)} />
          <button className="btn small primary" disabled={act.busy}>ثبت</button>
          <button type="button" className="btn small ghost" onClick={() => setDraft(JSON.stringify(s.value))}>لغو</button>
        </form>)}
        {!s.editable && <span className="muted small">{s.managed_by === "audit:configure" ? "توسط ممیز" : "توسط مدیر امنیت"}</span>}
        <ErrorBox error={act.error} />
      </td>
    </tr>
  );
}

function Settings() {
  const data = usePoll(() => api<{ items: Setting[]; selectable_events: string[] }>("GET", "/admin/settings"), 0);
  return (
    <Section title="تنظیمات امنیتی">
      <p className="muted small">تنظیمات ممیزی (audit.*) فقط توسط ممیز امنیتی و بقیه فقط توسط مدیر امنیت تغییر می‌کنند؛ هر تغییر با مقدار قبل و بعد ممیزی می‌شود.
        فهرست‌ها و ساعت‌ها به قالب JSON هستند، مثلاً <code dir="ltr">["192.168.1.0/24"]</code> یا <code dir="ltr">{'{"from":"07:00","to":"19:00"}'}</code>.
        رویدادهای قابل غیرفعال‌سازی: <code dir="ltr">{data.data?.selectable_events.join(", ")}</code></p>
      <ErrorBox error={data.error} />
      <table className="table">
        <tbody>{data.data?.items.map((s) => <SettingValue key={s.key + JSON.stringify(s.value)} s={s} onSave={async (value, reason) => {
          await api("PUT", `/admin/settings/${s.key}`, { body: { value, reason } }); await data.reload();
        }} />)}</tbody>
      </table>
    </Section>
  );
}

function Integrity() {
  const act = useAction();
  const [res, setRes] = useState<IntegrityResult | null>(null);
  return (
    <Section title="صحت داده‌ها" actions={<button className="btn small" disabled={act.busy} onClick={() => void act.run(async () => setRes(await api("POST", "/admin/integrity/check")))}>بررسی صحت</button>}>
      <p className="muted small">حوادث و هشدارها با مهر HMAC محافظت می‌شوند و زنجیره ممیزی بازمحاسبه می‌شود. بررسی خودکار هم به‌طور دوره‌ای انجام می‌شود.</p>
      <ErrorBox error={act.error} />
      {res && <IntegritySummary r={res} />}
    </Section>
  );
}

function IntegritySummary({ r }: { r: IntegrityResult }) {
  const ok = r.violations.length === 0 && r.AuditChain.valid;
  return (
    <div className={ok ? "banner info" : "banner warn"}>
      {fmtTime(r.at)}: {num(r.checked)} رکورد بررسی شد؛ {r.violations.length ? `${num(r.violations.length)} مورد تغییر غیرمجاز` : "بدون تغییر غیرمجاز"}.
      زنجیره ممیزی: {r.AuditChain.valid ? `سالم (${num(r.AuditChain.checked)} رکورد)` : `گسست در رکورد ${r.AuditChain.broken_seq} (${r.AuditChain.problem})`}
      {r.violations.length > 0 && <ul>{r.violations.slice(0, 20).map((v) => <li key={v.record_id} dir="ltr">{v.record_type}:{v.record_id} ({v.reason})</li>)}</ul>}
    </div>
  );
}

function AuditStatusCard() {
  const { can } = useSession();
  const st = usePoll(() => api<AuditStatus>("GET", "/admin/audit/status"), 60000);
  const act = useAction();
  const [before, setBefore] = useState("");
  const s = st.data;
  return (
    <Section title="ظرفیت و بایگانی ممیزی">
      <ErrorBox error={st.error ?? act.error} />
      {s && <>
        {s.blocking && <div className="banner warn">ظرفیت ممیزی تکمیل است و فعالیت کاربران غیرمدیر متوقف شده؛ ممیزی را بایگانی کنید.</div>}
        <p>{num(s.rows)} رکورد · آستانه هشدار {num(s.warn_rows)} · ظرفیت {num(s.max_rows)} · اقدام هنگام پر شدن: {s.full_action === "block" ? "توقف" : "فقط هشدار"}</p>
        {s.last_integrity_check && <IntegritySummary r={s.last_integrity_check} />}
        {can("audit:configure") && (
          <div className="row gap wrap">
            <label>بایگانی رکوردهای پیش از<input type="date" dir="ltr" value={before} onChange={(e) => setBefore(e.target.value)} /></label>
            {before && <ReasonAction label="بایگانی و دریافت فایل" danger onSubmit={async (reason) => {
              const f = await download("POST", "/admin/audit/archive", { body: { before, reason } });
              saveBlob(f.blob, f.name);
              alert(`فایل بایگانی ذخیره شد. چکیده HMAC برای نگهداری:\n${f.digest}`);
              await st.reload();
            }} />}
          </div>
        )}
        {s.archives && s.archives.length > 0 && (
          <table className="table small">
            <thead><tr><th>زمان</th><th>بازه</th><th>تعداد</th><th>توسط</th><th>چکیده</th></tr></thead>
            <tbody>{s.archives.map((a) => <tr key={a.id}><td>{fmtTime(a.created_at)}</td><td dir="ltr">{a.first_seq}–{a.last_seq}</td>
              <td>{num(a.row_count)}</td><td>{a.archived_by}</td><td dir="ltr"><code>{a.digest.slice(0, 16)}…</code></td></tr>)}</tbody>
          </table>
        )}
      </>}
    </Section>
  );
}

const SEVERITY: Record<string, string> = { critical: "بحرانی", high: "زیاد", medium: "متوسط", low: "کم", info: "اطلاعات" };

function Audit() {
  const [f, setF] = useState({ outcome: "", severity: "", action: "", ip: "", from: "", to: "", sort: "desc" });
  const rows = usePoll(() => api<{ items: AuditRow[] }>("GET", "/admin/audit", { query: { ...f, limit: 200 } }), 15000, [JSON.stringify(f)]);
  const verify = useAction();
  const [result, setResult] = useState<{ checked: number; valid: boolean; broken_seq?: number; problem?: string } | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const exportAs = (format: string) => void verify.run(async () => {
    const file = await download("GET", "/admin/audit/export", { query: { format, from: f.from, to: f.to, action: f.action, severity: f.severity, outcome: f.outcome } });
    saveBlob(file.blob, file.name);
  });
  const set = (k: string, v: string) => setF({ ...f, [k]: v });
  return (
    <Section title="لاگ ممیزی (زنجیره HMAC، فقط‌افزودنی)" actions={<>
      <button className="btn small" onClick={() => verify.run(async () => setResult(await api("GET", "/admin/audit/verify")))}>بررسی زنجیره</button>
      <button className="btn small" onClick={() => exportAs("csv")}>خروجی CSV</button>
      <button className="btn small" onClick={() => exportAs("json")}>خروجی JSON</button>
    </>}>
      {result && <div className={result.valid ? "banner info" : "banner warn"}>
        {result.valid ? `زنجیره سالم است (${num(result.checked)} رکورد)` : `گسست در رکورد ${result.broken_seq}: ${result.problem}`}</div>}
      <ErrorBox error={verify.error ?? rows.error} />
      <div className="row gap wrap filters">
        <label>نتیجه<select value={f.outcome} onChange={(e) => set("outcome", e.target.value)}>
          <option value="">همه</option><option value="success">موفق</option><option value="denied">رد</option><option value="failed">ناموفق</option></select></label>
        <label>شدت<select value={f.severity} onChange={(e) => set("severity", e.target.value)}>
          <option value="">همه</option>{Object.entries(SEVERITY).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></label>
        <label>رویداد (پیشوند)<input dir="ltr" value={f.action} onChange={(e) => set("action", e.target.value)} placeholder="console." /></label>
        <label>IP<input dir="ltr" value={f.ip} onChange={(e) => set("ip", e.target.value)} /></label>
        <label>از<input type="date" dir="ltr" value={f.from} onChange={(e) => set("from", e.target.value)} /></label>
        <label>تا<input type="date" dir="ltr" value={f.to} onChange={(e) => set("to", e.target.value)} /></label>
        <label>ترتیب<select value={f.sort} onChange={(e) => set("sort", e.target.value)}><option value="desc">جدیدترین</option><option value="asc">قدیمی‌ترین</option></select></label>
      </div>
      <table className="table small">
        <thead><tr><th>#</th><th>زمان</th><th>شدت</th><th>رویداد</th><th>هدف</th><th>نتیجه</th><th>نقش</th><th>IP / کانال</th></tr></thead>
        <tbody>
          {rows.data?.items.map((r) => (
            <Fragment key={r.seq}>
              <tr className={r.outcome !== "success" ? "warn-row" : ""} onClick={() => setOpen(open === r.seq ? null : r.seq)}>
                <td>{r.seq}</td><td>{fmtTime(r.created_at)}</td><td>{SEVERITY[r.severity] ?? r.severity}</td><td dir="ltr">{r.action}</td>
                <td dir="ltr">{r.target_type}:{r.target_id.slice(0, 8)}</td><td>{r.outcome}</td><td>{r.actor_roles.map(t).join("، ")}</td>
                <td dir="ltr">{r.ip} {r.channel}</td>
              </tr>
              {open === r.seq && <tr><td colSpan={8}>
                <dl className="kv small">
                  <dt>کاربر</dt><dd dir="ltr">{r.actor_id ?? "—"}</dd><dt>دلیل</dt><dd>{r.reason || "—"}</dd>
                  <dt>مرورگر/برنامه</dt><dd dir="ltr">{r.user_agent || "—"}</dd><dt>شناسه پیگیری</dt><dd dir="ltr">{r.correlation_id}</dd>
                  <dt>جزئیات</dt><dd dir="ltr"><code>{JSON.stringify(r.details)}</code></dd>
                </dl>
              </td></tr>}
            </Fragment>
          ))}
        </tbody>
      </table>
    </Section>
  );
}
