import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Act } from "./Act";
import { ApiError } from "../lib/api";
import { ago, t } from "../lib/format";

/** Polls an async loader; keeps the last good data and exposes its age so stale data is visible. */
export function usePoll<T>(load: () => Promise<T>, intervalMs: number, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [updatedAt, setUpdatedAt] = useState<string | null>(null);
  const loadRef = useRef(load);
  loadRef.current = load;
  const reload = useCallback(async () => {
    try {
      const d = await loadRef.current();
      setData(d);
      setError(null);
      setUpdatedAt(new Date().toISOString());
    } catch (e) {
      setError(e instanceof ApiError ? e : new ApiError(0, "UNKNOWN", String(e), ""));
    }
  }, []);
  useEffect(() => {
    void reload();
    if (!intervalMs) return;
    const id = setInterval(() => void reload(), intervalMs);
    return () => clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reload, intervalMs, ...deps]);
  return { data, error, reload, updatedAt };
}

/** Runs an action, tracking busy state and the last error. */
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const run = useCallback(async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      return true;
    } catch (e) {
      setError(e instanceof ApiError ? e : new ApiError(0, "UNKNOWN", String(e), ""));
      return false;
    } finally {
      setBusy(false);
    }
  }, []);
  return { busy, error, run, setError };
}

/** Persian wording for validation details the API returns as field + reason codes. */
const REASONS: Record<string, string> = {
  "role.role:app_only_user_needs_app_role": "کاربری که فقط دسترسی اپلیکیشن دارد باید نقش «امدادگر» یا «شهروند» داشته باشد. برای نقش‌های دیگر «دسترسی کنسول» را هم فعال کنید.",
  "console_access:citizens_have_no_console": "شهروند دسترسی کنسول ندارد؛ تیک «دسترسی کنسول» را بردارید.",
  "app_access:citizen_needs_app": "برای شهروند «دسترسی اپلیکیشن» را فعال کنید.",
  "role.scope_org_id:citizen_has_no_scope": "شهروند دامنه سازمانی ندارد.",
  "console_access:console_or_app_required": "دست‌کم یکی از «دسترسی کنسول» یا «دسترسی اپلیکیشن» را انتخاب کنید.",
  "username:required_for_console": "برای دسترسی کنسول، نام کاربری لازم است.",
  "username:invalid": "نام کاربری باید با حرف کوچک لاتین شروع شود و فقط حروف کوچک لاتین، رقم، نقطه، خط تیره و زیرخط داشته باشد (۴ تا ۳۲ نویسه).",
  "national_code:invalid": "کد ملی معتبر نیست.",
  "mobile:invalid": "شماره موبایل معتبر نیست.",
  "first_name:required": "نام را وارد کنید.",
  "last_name:required": "نام خانوادگی را وارد کنید.",
  "reason:required": "دلیل را بنویسید.",
  "allowed_cidrs:invalid": "شبکه مجاز باید به شکل 192.168.1.0/24 باشد.",
  "role.expires_at:in_past": "تاریخ اعتبار نقش گذشته است.",
  "role.role:unknown": "نقش انتخاب‌شده معتبر نیست.",
};
/** Persian wording for the API's field names and validation reasons; unknown ones are shown as sent. */
const FIELD_FA: Record<string, string> = {
  "role.role": "نقش", "role.expires_at": "اعتبار نقش", national_code: "کد ملی", mobile: "موبایل", first_name: "نام",
  last_name: "نام خانوادگی", username: "نام کاربری", console_access: "نوع دسترسی", allowed_cidrs: "شبکه‌های مجاز", reason: "دلیل",
  location: "موقعیت", quantity: "مقدار", version: "نسخه", description: "شرح", title: "عنوان", body: "متن", keywords: "کلیدواژه‌ها",
};
const REASON_FA: Record<string, string> = {
  app_only_user_needs_app_role: "کاربری که فقط دسترسی اپلیکیشن دارد باید نقش «امدادگر» یا «شهروند» داشته باشد.",
  console_or_app_required: "حداقل یکی از «دسترسی کنسول» یا «دسترسی اپلیکیشن» را انتخاب کنید.",
  required_for_console: "برای دسترسی کنسول نام کاربری لازم است.",
  required: "الزامی است.", invalid: "نامعتبر است.", unknown: "مجاز نیست.", in_past: "تاریخ گذشته است.",
  too_long: "بیش از حد طولانی است.", not_allowed: "مجاز نیست.", outside_service_area: "خارج از محدوده خدمت است.",
  exceeds_remaining: "بیشتر از مقدار باقی‌مانده است.",
};
const reasonFa = (r: string) => REASON_FA[r] ?? REASON_FA[r.split(":")[0]] ?? r;

export function ErrorBox({ error }: { error: ApiError | null }) {
  if (!error) return null;
  return (
    <div className="error" role="alert">
      <strong>{error.message}</strong>
      {error.details.length > 0 && (
        <ul>
          {error.details.map((d, i) => (
            <li key={i}>
              {REASONS[`${d.field}:${d.reason}`] ?? <>{FIELD_FA[d.field] ?? <code>{d.field}</code>}: {reasonFa(d.reason)}</>}
            </li>
          ))}
        </ul>
      )}
      <small>
        کد: {error.code}
        {error.correlationId && <> · شناسه پیگیری: <code>{error.correlationId}</code></>}
      </small>
    </div>
  );
}

export function Badge({ value, kind }: { value: string | null | undefined; kind?: string }) {
  return <span className={`badge badge-${kind ?? value ?? "none"}`}>{t(value)}</span>;
}

export function Freshness({ at, label = "به‌روزرسانی" }: { at: string | null; label?: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => tick((x) => x + 1), 15000);
    return () => clearInterval(id);
  }, []);
  return <span className="muted small">{label}: {ago(at)}</span>;
}

export function Section({ title, actions, children }: { title: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      <header className="card-head">
        <h2>{title}</h2>
        <div className="row gap">{actions}</div>
      </header>
      {children}
    </section>
  );
}

/** Inline form that requires a reason before a consequential action (doc §8.1). */
export function ReasonAction({ label, onSubmit, required = true, danger, extra }: {
  label: string; required?: boolean; danger?: boolean; extra?: ReactNode;
  onSubmit: (reason: string) => Promise<unknown>;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const { busy, error, run } = useAction();
  if (!open)
    return (
      <button className={danger ? "btn danger" : "btn"} onClick={() => setOpen(true)}>
        <Act>{label}</Act>
      </button>
    );
  return (
    <form
      className="reason-form"
      onSubmit={async (e) => {
        e.preventDefault();
        if (await run(() => onSubmit(reason.trim()))) {
          setOpen(false);
          setReason("");
        }
      }}
    >
      <strong>{label}</strong>
      {extra}
      <textarea placeholder={required ? "دلیل (الزامی)" : "توضیح (اختیاری)"} value={reason} required={required}
        onChange={(e) => setReason(e.target.value)} rows={2} />
      <div className="row gap">
        <button className={danger ? "btn danger" : "btn primary"} disabled={busy || (required && !reason.trim())}>
          {busy ? "…" : <Act>ثبت</Act>}
        </button>
        <button type="button" className="btn ghost" onClick={() => setOpen(false)}><Act>انصراف</Act></button>
      </div>
      <ErrorBox error={error} />
    </form>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="muted empty">{children}</p>;
}
