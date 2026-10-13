import { useState } from "react";
import { api, ApiError, type AccessHistory } from "../lib/api";
import { useSession } from "../lib/session";
import { fmtTime, num } from "../lib/format";
import { ErrorBox, Section, useAction, usePoll } from "../components/ui";

export type SessionRow = {
  id: string; channel: string; device_id: string | null; device_name: string | null; ip: string | null; user_agent: string | null;
  created_at: string; last_seen_at: string; expires_at: string; revoked_at: string | null; revoked_reason: string | null;
};

const REVOKE_REASONS: Record<string, string> = {
  logout: "خروج", replaced: "ورود دوباره", idle_timeout: "عدم فعالیت", ip_changed: "تغییر نشانی IP", user_agent_changed: "تغییر مرورگر",
  password_changed: "تغییر رمز", password_reset: "بازنشانی رمز", role_changed: "تغییر نقش", revoked_by_admin: "ابطال توسط مدیر",
  refresh_reuse: "استفاده مجدد توکن", mobile_changed: "تغییر موبایل", account_suspended: "تعلیق حساب", account_disabled: "غیرفعال‌سازی حساب",
  console_access_removed: "حذف دسترسی کنسول", shahkar_mismatch: "عدم تطابق شاهکار",
};

export function SessionsTable({ items, current, onRevoke }: { items: SessionRow[]; current?: string | null; onRevoke?: (id: string) => void }) {
  return (
    <table className="table small">
      <thead><tr><th>کانال</th><th>دستگاه / مرورگر</th><th>IP</th><th>شروع</th><th>آخرین فعالیت</th><th>وضعیت</th><th /></tr></thead>
      <tbody>
        {items.map((s) => (
          <tr key={s.id}>
            <td>{s.channel === "console" ? "کنسول" : "اپ"}{s.id === current && <b> (همین نشست)</b>}</td>
            <td dir="ltr" className="ellipsis">{s.device_name || s.user_agent || s.device_id}</td>
            <td dir="ltr">{s.ip}</td>
            <td>{fmtTime(s.created_at)}</td>
            <td>{fmtTime(s.last_seen_at)}</td>
            <td>{s.revoked_at ? `پایان: ${REVOKE_REASONS[s.revoked_reason ?? ""] ?? s.revoked_reason}` : new Date(s.expires_at) < new Date() ? "منقضی" : "فعال"}</td>
            <td>{onRevoke && !s.revoked_at && s.id !== current && <button className="btn small danger" onClick={() => onRevoke(s.id)}>ابطال</button>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function ChangePasswordForm({ forced, onDone }: { forced?: boolean; onDone?: () => void }) {
  const { busy, error, run } = useAction();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [ok, setOk] = useState(false);
  return (
    <form className="stack" onSubmit={(e) => {
      e.preventDefault();
      void run(async () => {
        if (next !== again) throw new ApiError(0, "PASSWORD_MISMATCH", "تکرار رمز جدید یکسان نیست", "");
        await api("POST", "/auth/console/change-password", { body: { current_password: cur, new_password: next } });
        setCur(""); setNext(""); setAgain(""); setOk(true);
        onDone?.();
      });
    }}>
      {forced && <div className="banner warn">رمز عبور شما موقت یا منقضی است. پیش از هر کار دیگری آن را تغییر دهید.</div>}
      <p className="muted small">حداقل ۱۲ کاراکتر شامل حروف کوچک و بزرگ لاتین، رقم و نماد؛ بدون نام کاربری، کد ملی یا موبایل؛ متفاوت با رمزهای اخیر.</p>
      <label>رمز فعلی<input type="password" autoComplete="current-password" dir="ltr" required value={cur} onChange={(e) => setCur(e.target.value)} /></label>
      <label>رمز جدید<input type="password" autoComplete="new-password" dir="ltr" required value={next} onChange={(e) => setNext(e.target.value)} /></label>
      <label>تکرار رمز جدید<input type="password" autoComplete="new-password" dir="ltr" required value={again} onChange={(e) => setAgain(e.target.value)} /></label>
      <button className="btn primary" disabled={busy}>تغییر رمز</button>
      {ok && <div className="banner info">رمز عبور تغییر کرد. نشست‌های دیگر شما خاتمه یافت.</div>}
      <ErrorBox error={error} />
    </form>
  );
}

/** Shown instead of the console while the password must be changed. */
export function ForcedPasswordChange() {
  const { reload, logout } = useSession();
  return (
    <div className="login">
      <div className="card narrow">
        <h1>تغییر رمز عبور</h1>
        <ChangePasswordForm forced onDone={() => void reload()} />
        <button className="btn ghost small" onClick={logout}>خروج</button>
      </div>
    </div>
  );
}

export function AccountPage() {
  const { me } = useSession();
  const data = usePoll(() => api<{ items: SessionRow[]; current: string | null; access_history: AccessHistory | null }>("GET", "/me/sessions"), 30000);
  const h = data.data?.access_history;
  return (
    <>
      <Section title="حساب من">
        <dl className="kv">
          <dt>نام</dt><dd>{me?.display_name}</dd>
          <dt>نقش</dt><dd>{me?.roles.join("، ")}</dd>
          {h && <><dt>آخرین ورود</dt><dd>{fmtTime(h.last_login_at)} <span dir="ltr">{h.last_login_ip}</span></dd>
            <dt>تلاش ناموفق اخیر</dt><dd>{num(h.failed_since_last_login)}</dd></>}
        </dl>
      </Section>
      {me?.channel === "console" && <Section title="تغییر رمز عبور"><ChangePasswordForm /></Section>}
      <Section title="نشست‌ها و سوابق ورود">
        <ErrorBox error={data.error} />
        {data.data && <SessionsTable items={data.data.items} current={data.data.current} />}
      </Section>
    </>
  );
}
