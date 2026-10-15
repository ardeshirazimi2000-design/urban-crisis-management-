import { useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { PageErrorBoundary } from "./components/ErrorBoundary";
import { useSession } from "./lib/session";
import { t } from "./lib/format";
import { NAV_ICONS } from "./components/icons";
import { Brand } from "./components/Brand";
import { LogOut } from "lucide-react";
import { AccessHistoryBanner, LoginPage } from "./pages/Login";
import { AccountPage, ForcedPasswordChange } from "./pages/Account";
import { SecurityPage } from "./pages/Security";
import { DashboardPage } from "./pages/Dashboard";
import { ReportsPage } from "./pages/Reports";
import { IncidentsPage, IncidentDetailPage } from "./pages/Incidents";
import { ResourcesPage } from "./pages/Resources";
import { AlertsPage, AlertDetailPage, NewAlertPage } from "./pages/Alerts";
import { AdminPage } from "./pages/Admin";
import { NeedsPage } from "./pages/Needs";
import { SheltersPage } from "./pages/Shelters";
import { HospitalsPage } from "./pages/Medical";
import { DamagePage } from "./pages/Damage";
import { SitrepPage } from "./pages/Sitrep";
import { GuidancePage } from "./pages/Guidance";

function useOnline() {
  const [online, setOnline] = useState(navigator.onLine);
  useEffect(() => {
    const on = () => setOnline(true), off = () => setOnline(false);
    window.addEventListener("online", on);
    window.addEventListener("offline", off);
    return () => { window.removeEventListener("online", on); window.removeEventListener("offline", off); };
  }, []);
  return online;
}

export function App() {
  const { me, loading, can, logout } = useSession();
  const online = useOnline();
  const { pathname } = useLocation();
  if (loading) return <div className="center muted">در حال بارگذاری…</div>;
  if (!me) return <LoginPage />;
  if (me.password_change_required) return <ForcedPasswordChange />;
  const nav = [
    { to: "/", label: "تصویر عملیاتی", show: can("gis:read") },
    { to: "/sitrep", label: "گزارش وضعیت", show: can("sitrep:read") },
    { to: "/reports", label: "صف گزارش‌ها", show: can("report:read") },
    { to: "/incidents", label: "حوادث", show: can("incident:read") },
    { to: "/resources", label: "منابع", show: can("resource:read") },
    { to: "/needs", label: "نیازهای باز", show: can("need:read") },
    { to: "/shelters", label: "اسکان اضطراری", show: can("shelter:read") },
    { to: "/hospitals", label: "بیمارستان‌ها", show: can("hospital:read") },
    { to: "/damage", label: "ارزیابی خسارت", show: can("damage:read") },
    { to: "/guidance", label: "راهنمای شهروند", show: can("guidance:edit") || can("guidance:approve") },
    { to: "/alerts", label: "هشدارها", show: can("alert:draft") || can("alert:approve") || can("alert:read_delivery") },
    { to: "/admin", label: "کاربران و نقش‌ها", show: can("user:manage") || can("event:replay") },
    { to: "/security", label: "امنیت و ممیزی", show: can("settings:manage") || can("audit:read") || can("audit:configure") },
    { to: "/account", label: "حساب من", show: !!me.channel },
  ].filter((n) => n.show);
  return (
    <div className="shell">
      <aside className="side">
        <Brand />
        <nav>
          {nav.map((n) => {
            const Icon = NAV_ICONS[n.to];
            return (
              <NavLink key={n.to} to={n.to} end={n.to === "/"}>
                {Icon && <Icon size={18} strokeWidth={1.75} aria-hidden="true" />}{n.label}
              </NavLink>
            );
          })}
        </nav>
        <div className="side-foot">
          <div>{me.display_name || me.subject}</div>
          <div className="muted small">{me.roles.map(t).join("، ")}</div>
          <button className="btn danger" onClick={logout}><LogOut size={16} aria-hidden="true" /> خروج از سامانه</button>
        </div>
      </aside>
      <main className="main">
        {!online && <div className="banner warn">اتصال شبکه قطع است؛ داده‌های نمایش‌داده‌شده ممکن است کهنه باشند.</div>}
        <AccessHistoryBanner />
        <PageErrorBoundary key={pathname}>
        <Routes>
          <Route path="/" element={can("gis:read") ? <DashboardPage /> : <Navigate to={nav[0]?.to ?? "/admin"} />} />
          <Route path="/reports" element={<ReportsPage />} />
          <Route path="/incidents" element={<IncidentsPage />} />
          <Route path="/incidents/:id" element={<IncidentDetailPage />} />
          <Route path="/resources" element={<ResourcesPage />} />
          <Route path="/needs" element={<NeedsPage />} />
          <Route path="/shelters" element={<SheltersPage />} />
          <Route path="/hospitals" element={<HospitalsPage />} />
          <Route path="/damage" element={<DamagePage />} />
          <Route path="/sitrep" element={<SitrepPage />} />
          <Route path="/guidance" element={<GuidancePage />} />
          <Route path="/alerts" element={<AlertsPage />} />
          <Route path="/alerts/new" element={<NewAlertPage />} />
          <Route path="/alerts/:id" element={<AlertDetailPage />} />
          <Route path="/admin" element={<AdminPage />} />
          <Route path="/security" element={<SecurityPage />} />
          <Route path="/account" element={<AccountPage />} />
          <Route path="*" element={<Navigate to="/" />} />
        </Routes>
        </PageErrorBoundary>
      </main>
    </div>
  );
}
