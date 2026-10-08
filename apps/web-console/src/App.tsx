import { useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { useSession } from "./lib/session";
import { t } from "./lib/format";
import { LoginPage } from "./pages/Login";
import { DashboardPage } from "./pages/Dashboard";
import { ReportsPage } from "./pages/Reports";
import { IncidentsPage, IncidentDetailPage } from "./pages/Incidents";
import { ResourcesPage } from "./pages/Resources";
import { AlertsPage, AlertDetailPage, NewAlertPage } from "./pages/Alerts";
import { AdminPage } from "./pages/Admin";

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
  if (loading) return <div className="center muted">در حال بارگذاری…</div>;
  if (!me) return <LoginPage />;
  const nav = [
    { to: "/", label: "تصویر عملیاتی", show: can("gis:read") },
    { to: "/reports", label: "صف گزارش‌ها", show: can("report:read") },
    { to: "/incidents", label: "حوادث", show: can("incident:read") },
    { to: "/resources", label: "منابع", show: can("resource:read") },
    { to: "/alerts", label: "هشدارها", show: can("alert:draft") || can("alert:approve") || can("alert:read_delivery") },
    { to: "/admin", label: "هویت و ممیزی", show: can("user:manage") || can("audit:read") },
  ].filter((n) => n.show);
  return (
    <div className="shell">
      <aside className="side">
        <div className="brand">مدیریت بحران شهری</div>
        <nav>
          {nav.map((n) => (
            <NavLink key={n.to} to={n.to} end={n.to === "/"}>{n.label}</NavLink>
          ))}
        </nav>
        <div className="side-foot">
          <div>{me.display_name || me.subject}</div>
          <div className="muted small">{me.roles.map(t).join("، ")}</div>
          <button className="btn ghost small" onClick={logout}>خروج</button>
        </div>
      </aside>
      <main className="main">
        {!online && <div className="banner warn">اتصال شبکه قطع است؛ داده‌های نمایش‌داده‌شده ممکن است کهنه باشند.</div>}
        <Routes>
          <Route path="/" element={can("gis:read") ? <DashboardPage /> : <Navigate to={nav[0]?.to ?? "/admin"} />} />
          <Route path="/reports" element={<ReportsPage />} />
          <Route path="/incidents" element={<IncidentsPage />} />
          <Route path="/incidents/:id" element={<IncidentDetailPage />} />
          <Route path="/resources" element={<ResourcesPage />} />
          <Route path="/alerts" element={<AlertsPage />} />
          <Route path="/alerts/new" element={<NewAlertPage />} />
          <Route path="/alerts/:id" element={<AlertDetailPage />} />
          <Route path="/admin" element={<AdminPage />} />
          <Route path="*" element={<Navigate to="/" />} />
        </Routes>
      </main>
    </div>
  );
}
