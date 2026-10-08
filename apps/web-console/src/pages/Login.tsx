import { useState } from "react";
import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { ErrorBox, useAction } from "../components/ui";

// Local development login: the API's /dev/token endpoint exists only with APP_ENV=local and AUTH_MODE=dev.
// Production deployments authenticate through the organisation's OIDC provider (MFA for privileged roles).
const PRESETS = [
  { subject: "operator", name: "اپراتور مرکز فرماندهی", grants: [{ role: "OPERATOR", org_code: "command" }] },
  { subject: "commander", name: "فرمانده کشیک", grants: [{ role: "COMMANDER", org_code: "command" }, { role: "RESOURCE_MANAGER" }] },
  { subject: "commander2", name: "فرمانده جانشین", grants: [{ role: "COMMANDER", org_code: "command" }] },
  { subject: "resources", name: "مدیر منابع", grants: [{ role: "RESOURCE_MANAGER" }] },
  { subject: "gis", name: "کارشناس GIS", grants: [{ role: "GIS_ANALYST" }] },
  { subject: "fire-operator", name: "اپراتور آتش‌نشانی", grants: [{ role: "OPERATOR", org_code: "fire" }] },
  { subject: "security", name: "مدیر امنیت", grants: [{ role: "SECURITY_ADMIN" }] },
];

export function LoginPage() {
  const { login } = useSession();
  const { busy, error, run } = useAction();
  const [idx, setIdx] = useState(0);
  return (
    <div className="login">
      <div className="card narrow">
        <h1>کنسول مدیریت بحران شهری</h1>
        <p className="muted">ورود توسعه محلی (فقط APP_ENV=local). در محیط عملیاتی ورود از طریق OIDC و MFA انجام می‌شود.</p>
        <label>
          نقش نمونه
          <select value={idx} onChange={(e) => setIdx(+e.target.value)}>
            {PRESETS.map((p, i) => (
              <option key={p.subject} value={i}>{p.name}</option>
            ))}
          </select>
        </label>
        <button className="btn primary" disabled={busy}
          onClick={() => run(async () => {
            const r = await api<{ access_token: string }>("POST", "/dev/token", { body: PRESETS[idx] });
            await login(r.access_token);
          })}>
          ورود
        </button>
        <ErrorBox error={error} />
      </div>
    </div>
  );
}
