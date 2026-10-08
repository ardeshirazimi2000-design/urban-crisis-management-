import { useState } from "react";
import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { fmtTime, t } from "../lib/format";
import { Badge, ErrorBox, ReasonAction, Section, useAction, usePoll } from "../components/ui";

type User = { id: string; subject: string; display_name: string; status: string; grants: { grant_id: string; role: string; scope_org_id: string | null; expires_at: string | null; reason: string }[] };
type AuditRow = { seq: number; actor_id: string | null; actor_roles: string[]; action: string; target_type: string; target_id: string; outcome: string; reason: string; created_at: string; correlation_id: string };
type OutboxRow = { id: string; aggregate_type: string; aggregate_id: string; event_type: string; attempts: number; last_error: string | null; dead_lettered_at: string | null; created_at: string };

const ROLES = ["CITIZEN", "RESPONDER", "OPERATOR", "COMMANDER", "RESOURCE_MANAGER", "GIS_ANALYST", "SECURITY_ADMIN"];

export function AdminPage() {
  const { can } = useSession();
  return (
    <>
      {can("user:manage") && <Users />}
      {can("audit:read") && <Audit />}
      {can("event:replay") && <Outbox />}
    </>
  );
}

function Users() {
  const { orgs, orgName, me } = useSession();
  const [q, setQ] = useState("");
  const users = usePoll(() => api<{ items: User[] }>("GET", "/admin/users", { query: { q } }), 0, [q]);
  const [g, setG] = useState({ role: "OPERATOR", scope: "", hours: "" , breakGlass: false });
  return (
    <Section title="کاربران و نقش‌ها">
      <p className="muted small">مدیریت نقش از فرماندهی عملیاتی جداست. اعطای اضطراری (break-glass) حداکثر ۸ ساعت، با دلیل و ثبت ممیزی است.</p>
      <input placeholder="جستجو" value={q} onChange={(e) => setQ(e.target.value)} />
      <ErrorBox error={users.error} />
      <div className="row gap wrap filters">
        <label>نقش<select value={g.role} onChange={(e) => setG({ ...g, role: e.target.value })}>{ROLES.map((r) => <option key={r} value={r}>{t(r)}</option>)}</select></label>
        <label>دامنه سازمانی<select value={g.scope} onChange={(e) => setG({ ...g, scope: e.target.value })}>
          <option value="">همه سازمان‌ها</option>{orgs.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
        <label>انقضا (ساعت)<input value={g.hours} onChange={(e) => setG({ ...g, hours: e.target.value })} dir="ltr" size={4} /></label>
        <label className="chk"><input type="checkbox" checked={g.breakGlass} onChange={(e) => setG({ ...g, breakGlass: e.target.checked })} /> اضطراری</label>
      </div>
      <table className="table">
        <thead><tr><th>کاربر</th><th>وضعیت</th><th>نقش‌ها</th><th /></tr></thead>
        <tbody>
          {users.data?.items.map((u) => (
            <tr key={u.id}>
              <td>{u.display_name || u.subject}<div className="muted small" dir="ltr">{u.subject}</div></td>
              <td><Badge value={u.status} kind={u.status === "active" ? "available" : "failed"} /></td>
              <td>{u.grants.map((gr) => (
                <div key={gr.grant_id} className="small">
                  {t(gr.role)} — {gr.scope_org_id ? orgName(gr.scope_org_id) : "همه"}{gr.expires_at && <> · تا {fmtTime(gr.expires_at)}</>}
                  {u.id !== me?.user_id && <ReasonAction label="حذف" danger onSubmit={async (reason) => {
                    await api("DELETE", `/admin/users/${u.id}/roles/${gr.grant_id}`, { query: { reason } }); await users.reload();
                  }} />}
                </div>))}</td>
              <td>{u.id !== me?.user_id && <>
                <ReasonAction label={`اعطای ${t(g.role)}`} onSubmit={async (reason) => {
                  await api("POST", `/admin/users/${u.id}/roles`, { body: { role: g.role, scope_org_id: g.scope || null, reason, break_glass: g.breakGlass,
                    expires_at: g.hours ? new Date(Date.now() + +g.hours * 3600_000).toISOString() : null } });
                  await users.reload();
                }} />
                <ReasonAction label={u.status === "active" ? "تعلیق" : "فعال‌سازی"} danger={u.status === "active"} onSubmit={async (reason) => {
                  await api("POST", `/admin/users/${u.id}/status`, { body: { status: u.status === "active" ? "suspended" : "active", reason } }); await users.reload();
                }} />
              </>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  );
}

function Audit() {
  const [outcome, setOutcome] = useState("");
  const rows = usePoll(() => api<{ items: AuditRow[] }>("GET", "/admin/audit", { query: { outcome, limit: 200 } }), 15000, [outcome]);
  const verify = useAction();
  const [result, setResult] = useState<{ checked: number; valid: boolean; broken_seq?: number; problem?: string } | null>(null);
  return (
    <Section title="لاگ ممیزی (زنجیره هش، فقط‌افزودنی)" actions={
      <button className="btn small" onClick={() => verify.run(async () => setResult(await api("GET", "/admin/audit/verify")))}>بررسی یکپارچگی</button>}>
      {result && <div className={result.valid ? "banner info" : "banner warn"}>
        {result.valid ? `زنجیره سالم است (${result.checked} رکورد)` : `گسست در رکورد ${result.broken_seq}: ${result.problem}`}</div>}
      <ErrorBox error={verify.error ?? rows.error} />
      <label>نتیجه<select value={outcome} onChange={(e) => setOutcome(e.target.value)}>
        <option value="">همه</option><option value="success">موفق</option><option value="denied">رد دسترسی</option><option value="failed">ناموفق</option></select></label>
      <table className="table small">
        <thead><tr><th>#</th><th>زمان</th><th>عمل</th><th>هدف</th><th>نتیجه</th><th>نقش</th><th>دلیل</th><th>پیگیری</th></tr></thead>
        <tbody>
          {rows.data?.items.map((r) => (
            <tr key={r.seq} className={r.outcome === "denied" ? "warn-row" : ""}>
              <td>{r.seq}</td><td>{fmtTime(r.created_at)}</td><td dir="ltr">{r.action}</td><td dir="ltr">{r.target_type}:{r.target_id.slice(0, 8)}</td>
              <td>{r.outcome}</td><td>{r.actor_roles.map(t).join("، ")}</td><td>{r.reason}</td><td dir="ltr"><code>{r.correlation_id.slice(0, 8)}</code></td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  );
}

function Outbox() {
  const [state, setState] = useState("dead");
  const data = usePoll(() => api<{ items: OutboxRow[]; pending: number; dead: number; oldest_pending_seconds: number | null }>("GET", "/admin/outbox", { query: { state } }), 10000, [state]);
  return (
    <Section title="صف رویداد (Outbox) و بازپخش">
      {data.data && <p>در انتظار انتشار: {data.data.pending} · DLQ: {data.data.dead} · قدیمی‌ترین: {data.data.oldest_pending_seconds ? Math.round(data.data.oldest_pending_seconds) + " ثانیه" : "—"}</p>}
      <label>وضعیت<select value={state} onChange={(e) => setState(e.target.value)}>
        <option value="dead">DLQ</option><option value="pending">در انتظار</option><option value="published">منتشرشده</option></select></label>
      <ErrorBox error={data.error} />
      <table className="table small">
        <thead><tr><th>رویداد</th><th>Aggregate</th><th>تلاش</th><th>خطا</th><th /></tr></thead>
        <tbody>
          {data.data?.items.map((o) => (
            <tr key={o.id}>
              <td dir="ltr">{o.event_type}</td><td dir="ltr">{o.aggregate_type}:{o.aggregate_id.slice(0, 8)}</td><td>{o.attempts}</td>
              <td className="small">{o.last_error}</td>
              <td><ReasonAction label="بازپخش" onSubmit={async (reason) => { await api("POST", "/admin/outbox/replay", { body: { event_ids: [o.id], reason } }); await data.reload(); }} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  );
}
