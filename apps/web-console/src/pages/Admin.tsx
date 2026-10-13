import { useState } from "react";
import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { fmtTime, t } from "../lib/format";
import { Badge, ErrorBox, ReasonAction, Section, useAction, usePoll } from "../components/ui";
import { SessionsTable, type SessionRow } from "./Account";

type GrantRow = { grant_id: string; role: string; scope_org_id: string | null; expires_at: string | null; reason: string };
type User = {
  id: string; subject: string; display_name: string; status: string; grants: GrantRow[]; kind?: string; username?: string | null;
  national_code?: string | null; mobile?: string | null; console_access?: boolean; app_access?: boolean; locked?: boolean;
  last_login_at?: string | null; shahkar_verified_at?: string | null;
};
type UserDetail = User & {
  first_name: string; last_name: string; position_title: string; must_change_password: boolean; locked_until: string | null;
  app_enrolled_at: string | null; last_login_ip: string | null; password_changed_at: string | null; failed_since_success: number;
  allowed_cidrs: string[]; auth_methods: string[];
};
type OutboxRow = { id: string; aggregate_type: string; aggregate_id: string; event_type: string; attempts: number; last_error: string | null; dead_lettered_at: string | null; created_at: string };
type Inquiry = { id: number; national_code: string; mobile: string; matched: boolean | null; provider_code: string | null; message: string | null; context: string; requested_by: string | null; created_at: string };

export const STAFF_ROLES = ["RESPONDER", "OPERATOR", "COMMANDER", "RESOURCE_MANAGER", "GIS_ANALYST", "SECURITY_ADMIN", "SECURITY_AUDITOR"];

export function AdminPage() {
  const { can } = useSession();
  const local = useLocalMode();
  return (
    <>
      {can("user:manage") && <Users local={local} />}
      {can("shahkar:inquire") && local && <Shahkar />}
      {can("event:replay") && <Outbox />}
    </>
  );
}

/** The built-in identity provider is active when the session itself is a built-in one. */
function useLocalMode() {
  const { me } = useSession();
  return !!me?.channel;
}

/** One-time temporary password, shown above everything else with a copy button. */
function TempPassword({ value, onClose }: { value: string; onClose: () => void }) {
  const [copied, setCopied] = useState("");
  return (
    <div className="overlay" role="dialog" aria-label="رمز موقت">
      <div className="card narrow">
        <h2>رمز موقت</h2>
        <p>این رمز فقط همین یک بار نمایش داده می‌شود؛ آن را به‌صورت امن به کاربر تحویل دهید. کاربر در اولین ورود باید آن را تغییر دهد.</p>
        <p className="temppw" dir="ltr">{value}</p>
        <div className="row gap">
          <button className="btn" onClick={() => navigator.clipboard?.writeText(value).then(() => setCopied("کپی شد"), () => setCopied("کپی نشد؛ دستی یادداشت کنید"))}>کپی رمز</button>
          <span className="muted small">{copied}</span>
          <button className="btn primary" onClick={onClose}>بستم</button>
        </div>
      </div>
    </div>
  );
}

function Users({ local }: { local: boolean }) {
  const { orgName } = useSession();
  const [q, setQ] = useState("");
  const [kind, setKind] = useState(local ? "staff" : "");
  const users = usePoll(() => api<{ items: User[] }>("GET", "/admin/users", { query: { q, kind } }), 0, [q, kind]);
  const [open, setOpen] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [temp, setTemp] = useState("");
  return (
    <Section title="کاربران و نقش‌ها" actions={local && <button className="btn primary" onClick={() => setCreating(true)}>تعریف کاربر</button>}>
      <p className="muted small">هر کاربر فقط یک نقش دارد. مدیر امنیت کاربران و تنظیمات را مدیریت می‌کند و ممیز امنیتی ممیزی را؛ هیچ‌کس نقش خود را تغییر نمی‌دهد.</p>
      <div className="row gap wrap filters">
        <input placeholder="جستجو: نام، نام کاربری، کد ملی یا موبایل" value={q} onChange={(e) => setQ(e.target.value)} />
        <select value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="">همه</option><option value="staff">کارکنان</option><option value="citizen">شهروندان</option>
        </select>
      </div>
      <ErrorBox error={users.error} />
      {creating && <CreateUser onClose={() => setCreating(false)} onCreated={(pw) => { setCreating(false); if (pw) setTemp(pw); void users.reload(); }} />}
      <table className="table">
        <thead><tr><th>کاربر</th><th>هویت</th><th>وضعیت</th><th>نقش</th><th>آخرین ورود</th></tr></thead>
        <tbody>
          {users.data?.items.map((u) => (
            <tr key={u.id} onClick={() => setOpen(u.id)} className={open === u.id ? "sel" : ""}>
              <td>{u.display_name || u.subject}<div className="muted small" dir="ltr">{u.username ?? u.subject}</div></td>
              <td className="small" dir="ltr">{u.national_code}<br />{u.mobile}</td>
              <td><Badge value={u.status} kind={u.status === "active" ? "available" : "failed"} />{u.locked && <Badge value="قفل" kind="failed" />}</td>
              <td>{u.grants.map((g) => <div key={g.grant_id} className="small">{t(g.role)} — {g.scope_org_id ? orgName(g.scope_org_id) : "همه سازمان‌ها"}{g.expires_at && <> · تا {fmtTime(g.expires_at)}</>}</div>)}</td>
              <td className="small">{fmtTime(u.last_login_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {open && (local ? <UserPanel id={open} onTemp={setTemp} onChanged={() => void users.reload()} onClose={() => setOpen(null)} />
        : <LegacyUserActions user={users.data?.items.find((u) => u.id === open)} onChanged={() => void users.reload()} />)}
      {temp && <TempPassword value={temp} onClose={() => setTemp("")} />}
    </Section>
  );
}

function RoleFields({ value, onChange }: { value: { role: string; scope: string; until: string }; onChange: (v: { role: string; scope: string; until: string }) => void }) {
  const { orgs } = useSession();
  return (
    <div className="row gap wrap">
      <label>نقش<select value={value.role} onChange={(e) => onChange({ ...value, role: e.target.value })}>{STAFF_ROLES.map((r) => <option key={r} value={r}>{t(r)}</option>)}</select></label>
      <label>دامنه سازمانی<select value={value.scope} onChange={(e) => onChange({ ...value, scope: e.target.value })}>
        <option value="">همه سازمان‌ها</option>{orgs.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}</select></label>
      <label>اعتبار تا (اختیاری)<input type="date" dir="ltr" value={value.until} onChange={(e) => onChange({ ...value, until: e.target.value })} /></label>
    </div>
  );
}

const roleBody = (r: { role: string; scope: string; until: string }) =>
  ({ role: r.role, scope_org_id: r.scope || null, expires_at: r.until ? new Date(r.until + "T23:59:59+03:30").toISOString() : null });

const cidrList = (s: string) => s.split(/[\s,،]+/).map((x) => x.trim()).filter(Boolean);

function CreateUser({ onClose, onCreated }: { onClose: () => void; onCreated: (temp: string) => void }) {
  const { busy, error, run } = useAction();
  const [f, setF] = useState({ national_code: "", mobile: "", first_name: "", last_name: "", position_title: "", username: "", console_access: true, app_access: false, allowed_cidrs: "" });
  const [role, setRole] = useState({ role: "OPERATOR", scope: "", until: "" });
  const set = (k: string, v: unknown) => setF({ ...f, [k]: v });
  return (
    <form className="card subtle stack" onSubmit={(e) => {
      e.preventDefault();
      void run(async () => {
        const r = await api<{ temporary_password: string }>("POST", "/admin/users", {
          body: { ...f, allowed_cidrs: cidrList(f.allowed_cidrs), role: roleBody(role), username: f.username || undefined },
        });
        onCreated(r.temporary_password);
      });
    }}>
      <h3>تعریف کاربر (پس از تطابق شاهکار)</h3>
      <div className="row gap wrap">
        <label>کد ملی<input dir="ltr" required value={f.national_code} onChange={(e) => set("national_code", e.target.value)} /></label>
        <label>موبایل<input dir="ltr" required value={f.mobile} onChange={(e) => set("mobile", e.target.value)} /></label>
        <label>نام<input required value={f.first_name} onChange={(e) => set("first_name", e.target.value)} /></label>
        <label>نام خانوادگی<input required value={f.last_name} onChange={(e) => set("last_name", e.target.value)} /></label>
        <label>سمت<input value={f.position_title} onChange={(e) => set("position_title", e.target.value)} /></label>
      </div>
      <div className="row gap wrap">
        <label className="chk"><input type="checkbox" checked={f.console_access} onChange={(e) => set("console_access", e.target.checked)} /> دسترسی کنسول</label>
        <label className="chk"><input type="checkbox" checked={f.app_access} onChange={(e) => set("app_access", e.target.checked)} /> دسترسی اپلیکیشن</label>
        {f.console_access && <label>نام کاربری<input dir="ltr" required value={f.username} onChange={(e) => set("username", e.target.value)} /></label>}
        {f.console_access && <label>شبکه‌های مجاز کنسول (اختیاری)<input dir="ltr" placeholder="192.168.1.0/24" value={f.allowed_cidrs} onChange={(e) => set("allowed_cidrs", e.target.value)} /></label>}
      </div>
      <RoleFields value={role} onChange={setRole} />
      <div className="row gap">
        <button className="btn primary" disabled={busy}>{busy ? "استعلام شاهکار…" : "استعلام شاهکار و تعریف"}</button>
        <button type="button" className="btn ghost" onClick={onClose}>انصراف</button>
      </div>
      <ErrorBox error={error} />
    </form>
  );
}

function UserPanel({ id, onTemp, onChanged, onClose }: { id: string; onTemp: (pw: string) => void; onChanged: () => void; onClose: () => void }) {
  const { me, can, orgName } = useSession();
  const user = usePoll(() => api<UserDetail>("GET", `/admin/users/${id}`), 0, [id]);
  const sessions = usePoll(() => can("session:manage") ? api<{ items: SessionRow[] }>("GET", `/admin/users/${id}/sessions`) : Promise.resolve({ items: [] }), 0, [id]);
  const act = useAction();
  const [role, setRole] = useState({ role: "OPERATOR", scope: "", until: "" });
  const [edit, setEdit] = useState<{ mobile: string; position_title: string; username: string; allowed_cidrs: string } | null>(null);
  const u = user.data;
  const self = id === me?.user_id;
  const done = async () => { await user.reload(); await sessions.reload(); onChanged(); };
  if (!u) return <ErrorBox error={user.error} />;
  return (
    <div className="card subtle">
      <header className="card-head">
        <h2>{u.first_name} {u.last_name} {self && <span className="muted small">(حساب خودتان)</span>}</h2>
        <button className="btn ghost small" onClick={onClose}>بستن</button>
      </header>
      <dl className="kv">
        <dt>کد ملی / موبایل</dt><dd dir="ltr">{u.national_code} · {u.mobile}</dd>
        <dt>نام کاربری</dt><dd dir="ltr">{u.username ?? "—"}</dd>
        <dt>نقش</dt><dd>{u.grants.map((g) => <span key={g.grant_id}>{t(g.role)} — {g.scope_org_id ? orgName(g.scope_org_id) : "همه سازمان‌ها"}{g.expires_at && <> · تا {fmtTime(g.expires_at)}</>} </span>)}</dd>
        <dt>وضعیت</dt><dd>{u.status}{u.locked && <> · قفل تا {fmtTime(u.locked_until)}</>}{u.must_change_password && " · رمز موقت"}</dd>
        <dt>شاهکار</dt><dd>{fmtTime(u.shahkar_verified_at)}</dd>
        <dt>آخرین ورود</dt><dd>{fmtTime(u.last_login_at)} <span dir="ltr">{u.last_login_ip}</span> · تلاش ناموفق از آن زمان: {u.failed_since_success}</dd>
        <dt>روش احراز هویت</dt><dd className="small" dir="ltr">{u.auth_methods.join(" | ")}</dd>
        {u.allowed_cidrs.length > 0 && <><dt>شبکه‌های مجاز کنسول</dt><dd dir="ltr">{u.allowed_cidrs.join(", ")}</dd></>}
      </dl>
      <ErrorBox error={act.error} />
      {!self && u.kind === "staff" && can("role:manage") && (
        <div className="stack">
          <h3>تغییر نقش</h3>
          <p className="muted small">تغییر نقش، نشست‌های فعال کاربر را خاتمه می‌دهد.</p>
          <RoleFields value={role} onChange={setRole} />
          <ReasonAction label="ثبت نقش" onSubmit={async (reason) => { await api("PUT", `/admin/users/${id}/role`, { body: { role: roleBody(role), reason } }); await done(); }} />
        </div>
      )}
      {can("user:manage") && (
        <div className="stack">
          <h3>ویرایش</h3>
          {!edit ? <button className="btn" onClick={() => setEdit({ mobile: u.mobile ?? "", position_title: u.position_title, username: u.username ?? "", allowed_cidrs: u.allowed_cidrs.join(", ") })}>ویرایش اطلاعات</button> : (
            <form className="stack" onSubmit={(e) => {
              e.preventDefault();
              void act.run(async () => {
                const r = await api<{ temporary_password: string }>("PATCH", `/admin/users/${id}`, { body: {
                  mobile: edit.mobile !== u.mobile ? edit.mobile : undefined, position_title: edit.position_title,
                  username: edit.username && edit.username !== u.username ? edit.username : undefined, allowed_cidrs: cidrList(edit.allowed_cidrs) } });
                setEdit(null);
                if (r.temporary_password) onTemp(r.temporary_password);
                await done();
              });
            }}>
              <div className="row gap wrap">
                <label>موبایل (تغییر = استعلام شاهکار)<input dir="ltr" value={edit.mobile} onChange={(e) => setEdit({ ...edit, mobile: e.target.value })} /></label>
                <label>سمت<input value={edit.position_title} onChange={(e) => setEdit({ ...edit, position_title: e.target.value })} /></label>
                {u.kind === "staff" && <label>نام کاربری<input dir="ltr" value={edit.username} onChange={(e) => setEdit({ ...edit, username: e.target.value })} /></label>}
                {u.kind === "staff" && <label>شبکه‌های مجاز کنسول<input dir="ltr" value={edit.allowed_cidrs} onChange={(e) => setEdit({ ...edit, allowed_cidrs: e.target.value })} /></label>}
              </div>
              <div className="row gap"><button className="btn primary" disabled={act.busy}>ذخیره</button><button type="button" className="btn ghost" onClick={() => setEdit(null)}>انصراف</button></div>
            </form>
          )}
          {!self && u.kind === "staff" && (
            <div className="row gap wrap">
              <label className="chk"><input type="checkbox" checked={!!u.console_access} onChange={(e) => void act.run(async () => {
                const r = await api<{ temporary_password: string }>("PATCH", `/admin/users/${id}`, { body: { console_access: e.target.checked } });
                if (r.temporary_password) onTemp(r.temporary_password);
                await done();
              })} /> دسترسی کنسول</label>
              <label className="chk"><input type="checkbox" checked={!!u.app_access} onChange={(e) => void act.run(async () => {
                await api("PATCH", `/admin/users/${id}`, { body: { app_access: e.target.checked } }); await done();
              })} /> دسترسی اپلیکیشن</label>
            </div>
          )}
        </div>
      )}
      {!self && (
        <div className="row gap wrap">
          {can("user:manage") && u.console_access && <button className="btn" disabled={act.busy} onClick={() => {
            if (!confirm("رمز فعلی کاربر باطل و یک رمز موقت جدید صادر می‌شود. ادامه می‌دهید؟")) return;
            void act.run(async () => { const r = await api<{ temporary_password: string }>("POST", `/admin/users/${id}/reset-password`); await done(); onTemp(r.temporary_password); });
          }}>بازنشانی رمز</button>}
          {can("user:manage") && u.locked && <button className="btn" onClick={() => void act.run(async () => { await api("POST", `/admin/users/${id}/unlock`); await done(); })}>بازگشایی قفل</button>}
          {can("shahkar:inquire") && <button className="btn" onClick={() => void act.run(async () => {
            const r = await api<{ matched: boolean }>("POST", `/admin/users/${id}/shahkar`);
            alert(r.matched ? "تطابق شاهکار تأیید شد." : "عدم تطابق شاهکار — حساب تعلیق شد."); await done();
          })}>استعلام مجدد شاهکار</button>}
          {can("user:manage") && ["active", "suspended", "disabled"].filter((s) => s !== u.status).map((s) => (
            <ReasonAction key={s} label={{ active: "فعال‌سازی", suspended: "تعلیق", disabled: "غیرفعال‌سازی" }[s]!} danger={s !== "active"}
              onSubmit={async (reason) => { await api("POST", `/admin/users/${id}/status`, { body: { status: s, reason } }); await done(); }} />
          ))}
        </div>
      )}
      {can("session:manage") && sessions.data && (
        <div className="stack">
          <div className="row gap"><h3>نشست‌ها</h3>{!self && <button className="btn small danger" onClick={() => void act.run(async () => { await api("DELETE", `/admin/users/${id}/sessions`); await done(); })}>ابطال همه</button>}</div>
          <SessionsTable items={sessions.data.items} current={null} onRevoke={self ? undefined : (sid) => void act.run(async () => { await api("DELETE", `/admin/users/${id}/sessions/${sid}`); await done(); })} />
        </div>
      )}
    </div>
  );
}

/** Role grants for OIDC / development deployments (no built-in identity). */
function LegacyUserActions({ user, onChanged }: { user?: User; onChanged: () => void }) {
  const { me } = useSession();
  const [g, setG] = useState({ role: "OPERATOR", scope: "", until: "" });
  const [breakGlass, setBreakGlass] = useState(false);
  if (!user || user.id === me?.user_id) return null;
  return (
    <div className="card subtle stack">
      <h3>{user.display_name}</h3>
      <RoleFields value={g} onChange={setG} />
      <label className="chk"><input type="checkbox" checked={breakGlass} onChange={(e) => setBreakGlass(e.target.checked)} /> اعطای اضطراری (حداکثر ۸ ساعت)</label>
      <div className="row gap wrap">
        <ReasonAction label={`اعطای ${t(g.role)}`} onSubmit={async (reason) => {
          await api("POST", `/admin/users/${user.id}/roles`, { body: { ...roleBody(g), reason, break_glass: breakGlass,
            expires_at: breakGlass ? new Date(Date.now() + 8 * 3600_000 - 60_000).toISOString() : roleBody(g).expires_at } });
          onChanged();
        }} />
        {user.grants.map((gr) => <ReasonAction key={gr.grant_id} label={`حذف ${t(gr.role)}`} danger onSubmit={async (reason) => {
          await api("DELETE", `/admin/users/${user.id}/roles/${gr.grant_id}`, { query: { reason } }); onChanged();
        }} />)}
        <ReasonAction label={user.status === "active" ? "تعلیق" : "فعال‌سازی"} danger={user.status === "active"} onSubmit={async (reason) => {
          await api("POST", `/admin/users/${user.id}/status`, { body: { status: user.status === "active" ? "suspended" : "active", reason } }); onChanged();
        }} />
      </div>
    </div>
  );
}

function Shahkar() {
  const [nc, setNc] = useState("");
  const [mobile, setMobile] = useState("");
  const [result, setResult] = useState<{ ok: boolean; matched: boolean; message: string } | null>(null);
  const act = useAction();
  const list = usePoll(() => api<{ items: Inquiry[] }>("GET", "/admin/shahkar/inquiries"), 0);
  return (
    <Section title="استعلام شاهکار">
      <form className="row gap wrap" onSubmit={(e) => { e.preventDefault(); void act.run(async () => { setResult(await api("POST", "/admin/shahkar/inquiry", { body: { national_code: nc, mobile } })); await list.reload(); }); }}>
        <label>کد ملی<input dir="ltr" required value={nc} onChange={(e) => setNc(e.target.value)} /></label>
        <label>موبایل<input dir="ltr" required value={mobile} onChange={(e) => setMobile(e.target.value)} /></label>
        <button className="btn" disabled={act.busy}>استعلام</button>
      </form>
      {result && <div className={result.ok && result.matched ? "banner info" : "banner warn"}>
        {!result.ok ? `سامانه شاهکار پاسخ نداد: ${result.message}` : result.matched ? "شماره موبایل متعلق به صاحب کد ملی است." : "عدم تطابق: شماره به نام این کد ملی نیست."}</div>}
      <ErrorBox error={act.error ?? list.error} />
      <table className="table small">
        <thead><tr><th>زمان</th><th>کد ملی</th><th>موبایل</th><th>نتیجه</th><th>زمینه</th><th>درخواست‌کننده</th></tr></thead>
        <tbody>{list.data?.items.map((i) => (
          <tr key={i.id}><td>{fmtTime(i.created_at)}</td><td dir="ltr">{i.national_code}</td><td dir="ltr">{i.mobile}</td>
            <td>{i.matched === null ? `خطا (${i.provider_code ?? ""})` : i.matched ? "تطابق" : "عدم تطابق"}</td><td dir="ltr">{i.context}</td><td>{i.requested_by ?? "—"}</td></tr>
        ))}</tbody>
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
