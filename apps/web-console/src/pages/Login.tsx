import { useEffect, useState } from "react";
import { api, ApiError, type AccessHistory, type Tokens } from "../lib/api";
import { useSession } from "../lib/session";
import { ErrorBox, useAction } from "../components/ui";
import { fmtTime, num } from "../lib/format";
import { Brand } from "../components/Brand";

type Banner = { banner: string; help: string };
type Challenge = { challenge_id: string; mobile_masked: string; expires_in: number; resend_after: number };

/**
 * Sign-in. With the built-in identity provider (AUTH_MODE=local, detected through /auth/banner) the console asks
 * for username and password, then the SMS code sent to the Shahkar-verified mobile. Otherwise the local
 * development sign-in (/dev/token, APP_ENV=local only) is shown.
 */
export function LoginPage() {
  const { endedReason } = useSession();
  const [mode, setMode] = useState<"loading" | "local" | "dev">("loading");
  const [banner, setBanner] = useState<Banner | null>(null);
  const [failure, setFailure] = useState<ApiError | null>(null);
  useEffect(() => {
    api<Banner>("GET", "/auth/banner")
      .then((b) => {
        setBanner(b);
        setMode("local");
      })
      .catch((e) => {
        // Only a missing endpoint means development sign-in; anything else (HTTPS required, server down, a CDN
        // error page) is shown.
        if (e instanceof ApiError && e.status === 404) setMode("dev");
        else setFailure(e instanceof ApiError ? e : new ApiError(0, "UNEXPECTED", String(e), ""));
      });
  }, []);
  if (failure)
    return (
      <div className="login">
        <div className="card narrow">
          <Brand size="lg" />
          <ErrorBox error={failure} />
          {failure.code === "HTTPS_REQUIRED" && <p className="muted small">نشانی را با <span dir="ltr">https://</span> باز کنید.</p>}
          <button className="btn" onClick={() => location.reload()}>تلاش دوباره</button>
        </div>
      </div>
    );
  if (mode === "loading") return <div className="center muted">در حال بارگذاری…</div>;
  return (
    <div className="login">
      <div className="card narrow">
        <Brand size="lg" />
        {banner && <div className="banner warn" role="note">{banner.banner}</div>}
        {endedReason && <div className="banner info">{endedReason}</div>}
        {mode === "local" ? <LocalLogin help={banner?.help ?? ""} /> : <DevLogin />}
      </div>
    </div>
  );
}

function LocalLogin({ help }: { help: string }) {
  const { loginSession } = useSession();
  const { busy, error, run } = useAction();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const [code, setCode] = useState("");
  const [showHelp, setShowHelp] = useState(false);

  if (challenge)
    return (
      <form className="stack" onSubmit={(e) => {
        e.preventDefault();
        void run(async () => {
          const r = await api<{ tokens: Tokens; access_history: AccessHistory }>("POST", "/auth/console/verify",
            { body: { challenge_id: challenge.challenge_id, code: code.trim() } });
          await loginSession(r.tokens, r.access_history);
        });
      }}>
        <p>کد تأیید به شماره <span dir="ltr">{challenge.mobile_masked}</span> ارسال شد ({num(challenge.expires_in)} ثانیه اعتبار).</p>
        <label>
          کد تأیید
          <input className="otp" inputMode="numeric" autoComplete="one-time-code" dir="ltr" maxLength={8} autoFocus required
            value={code} onChange={(e) => setCode(e.target.value)} />
        </label>
        <button type="submit" className="btn primary" disabled={busy}>ورود</button>
        <button type="button" className="btn ghost" onClick={() => { setChallenge(null); setCode(""); }}>بازگشت</button>
        <ErrorBox error={error} />
      </form>
    );
  return (
    <form className="stack" onSubmit={(e) => {
      e.preventDefault();
      void run(async () => {
        const r = await api<{ challenge: Challenge }>("POST", "/auth/console/login", { body: { username: username.trim(), password } });
        setPassword("");
        setChallenge(r.challenge);
      });
    }}>
      <label>
        نام کاربری
        <input name="username" autoComplete="username" dir="ltr" autoCapitalize="none" spellCheck={false} required autoFocus
          value={username} onChange={(e) => setUsername(e.target.value)} />
      </label>
      <label>
        رمز عبور
        <input name="password" type="password" autoComplete="current-password" dir="ltr" required
          value={password} onChange={(e) => setPassword(e.target.value)} />
      </label>
      <button type="submit" className="btn primary" disabled={busy}>ادامه</button>
      <button type="button" className="btn ghost small" onClick={() => setShowHelp(!showHelp)}>راهنمای ورود</button>
      {showHelp && <p className="muted small">{help}</p>}
      <ErrorBox error={error} />
    </form>
  );
}

/** Shown once after signing in: when the account was last used and how many attempts failed since. */
export function AccessHistoryBanner() {
  const { history, dismissHistory } = useSession();
  if (!history) return null;
  const failed = history.failed_since_last_login;
  return (
    <div className={failed > 0 ? "banner warn" : "banner info"} role="status">
      آخرین ورود موفق: {history.last_login_at ? <>{fmtTime(history.last_login_at)} از <span dir="ltr">{history.last_login_ip}</span></> : "اولین ورود"}
      {" · "}تلاش‌های ناموفق از آخرین ورود: {num(failed)}
      {failed > 0 && history.last_failed_login_at && <> (آخرین: {fmtTime(history.last_failed_login_at)}) — اگر این تلاش‌ها از شما نبوده، به مدیر امنیت اطلاع دهید.</>}
      <button className="btn ghost small" onClick={dismissHistory}>بستن</button>
    </div>
  );
}

// Local development sign-in: the API's /dev/token endpoint exists only with APP_ENV=local and AUTH_MODE=dev.
const PRESETS = [
  { subject: "operator", name: "اپراتور مرکز فرماندهی", grants: [{ role: "OPERATOR", org_code: "command" }] },
  { subject: "commander", name: "فرمانده کشیک", grants: [{ role: "COMMANDER", org_code: "command" }, { role: "RESOURCE_MANAGER" }] },
  { subject: "commander2", name: "فرمانده جانشین", grants: [{ role: "COMMANDER", org_code: "command" }] },
  { subject: "resources", name: "مدیر منابع", grants: [{ role: "RESOURCE_MANAGER" }] },
  { subject: "gis", name: "کارشناس GIS", grants: [{ role: "GIS_ANALYST" }] },
  { subject: "fire-operator", name: "اپراتور آتش‌نشانی", grants: [{ role: "OPERATOR", org_code: "fire" }] },
  { subject: "ems-coordinator", name: "هماهنگ‌کننده درمان اورژانس", grants: [{ role: "OPERATOR", org_code: "ems" }] },
  { subject: "shelter-staff", name: "مسئول اسکان هلال‌احمر", grants: [{ role: "OPERATOR", org_code: "redcrescent" }] },
  { subject: "security", name: "مدیر امنیت", grants: [{ role: "SECURITY_ADMIN" }] },
  { subject: "auditor", name: "ممیز امنیتی", grants: [{ role: "SECURITY_AUDITOR" }] },
];

function DevLogin() {
  const { login } = useSession();
  const { busy, error, run } = useAction();
  const [idx, setIdx] = useState(0);
  const [code, setCode] = useState("");
  return (
    <form className="stack" onSubmit={(e) => {
      e.preventDefault();
      void run(async () => {
        if (!code.trim()) throw new ApiError(0, "ACCESS_CODE_REQUIRED", "کد دسترسی کارکنان را وارد کنید.", "");
        const r = await api<{ access_token: string }>("POST", "/dev/token", { body: { ...PRESETS[idx], access_code: code || undefined } });
        await login(r.access_token);
      });
    }}>
      <p className="muted">ورود آزمایشی (فقط APP_ENV=local). در محیط عملیاتی ورود با نام کاربری، رمز عبور و کد پیامکی (AUTH_MODE=local) انجام می‌شود.</p>
      <label>
        نقش نمونه
        <select value={idx} onChange={(e) => setIdx(+e.target.value)}>
          {PRESETS.map((p, i) => (
            <option key={p.subject} value={i}>{p.name}</option>
          ))}
        </select>
      </label>
      <label>
        کد دسترسی کارکنان <span className="muted small">(در پایان نصب سرور نمایش داده می‌شود)</span>
        <input type="text" name="access-code" autoComplete="off" autoCapitalize="none" autoCorrect="off" spellCheck={false}
          dir="ltr" value={code} onChange={(e) => setCode(e.target.value)} />
      </label>
      <button type="submit" className="btn primary" disabled={busy}>ورود</button>
      <ErrorBox error={error} />
    </form>
  );
}
