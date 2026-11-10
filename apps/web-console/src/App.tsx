import { useEffect, useState } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { PageErrorBoundary } from "./components/ErrorBoundary";
import { useSession } from "./lib/session";
import { t } from "./lib/format";
import { SideNav } from "./components/SideNav";
import { Brand } from "./components/Brand";
import { LogOut, PanelRightClose, PanelRightOpen, UserRound } from "lucide-react";
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

const NAV_KEY = "crisis.nav.collapsed";

/** The sidebar folds into an icon rail; the choice is remembered per browser. */
function useCollapsedNav(): [boolean, () => void] {
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem(NAV_KEY) === "1";
    } catch {
      return false;
    }
  });
  const toggle = () => setCollapsed((c) => {
    try {
      localStorage.setItem(NAV_KEY, c ? "0" : "1");
    } catch {
      /* per-browser convenience only */
    }
    return !c;
  });
  return [collapsed, toggle];
}

export function App() {
  const { me, loading, can, logout } = useSession();
  const online = useOnline();
  const { pathname } = useLocation();
  const [collapsed, toggleCollapsed] = useCollapsedNav();
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
    <div className={`shell${collapsed ? " collapsed" : ""}`}>
      <aside className="side">
        <div className="side-head">
          <Brand compact={collapsed} />
          <button type="button" className="btn ghost icon side-toggle" onClick={toggleCollapsed} aria-expanded={!collapsed}
            title={collapsed ? "باز کردن منو" : "جمع کردن منو"} aria-label={collapsed ? "باز کردن منو" : "جمع کردن منو"}>
            {collapsed ? <PanelRightOpen size={18} aria-hidden="true" /> : <PanelRightClose size={18} aria-hidden="true" />}
          </button>
        </div>
        <SideNav items={nav} collapsed={collapsed} />
        <div className="side-foot">
          {collapsed ? (
            <div className="side-user" title={`${me.display_name || me.subject} — ${me.roles.map(t).join("، ")}`}><UserRound size={20} aria-hidden="true" /></div>
          ) : (
            <>
              <div>{me.display_name || me.subject}</div>
              <div className="muted small">{me.roles.map(t).join("، ")}</div>
            </>
          )}
          <button className={`btn danger${collapsed ? " icon" : ""}`} onClick={logout} title="خروج از سامانه" aria-label="خروج از سامانه">
            <LogOut size={16} aria-hidden="true" />{!collapsed && " خروج از سامانه"}
          </button>
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
