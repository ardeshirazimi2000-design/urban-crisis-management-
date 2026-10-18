import { useEffect, useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { Activity, ChevronDown, HeartPulse, Megaphone, Settings, Truck, type LucideIcon } from "lucide-react";
import { NAV_ICONS } from "./icons";

export type NavItem = { to: string; label: string; show: boolean };
type Group = { id: string; title: string; icon: LucideIcon; items: string[] };

/** The console's sections, in the order of the response workflow. */
const GROUPS: Group[] = [
  { id: "ops", title: "عملیات و فرماندهی", icon: Activity, items: ["/", "/sitrep", "/reports", "/incidents"] },
  { id: "resources", title: "منابع و امداد", icon: Truck, items: ["/resources", "/needs", "/shelters"] },
  { id: "care", title: "درمان و خسارت", icon: HeartPulse, items: ["/hospitals", "/damage"] },
  { id: "public", title: "اطلاع‌رسانی عمومی", icon: Megaphone, items: ["/alerts", "/guidance"] },
  { id: "system", title: "مدیریت سامانه", icon: Settings, items: ["/admin", "/security", "/account"] },
];

const KEY = "crisis.nav.open";

function isActive(to: string, pathname: string) {
  return to === "/" ? pathname === "/" : pathname === to || pathname.startsWith(to + "/");
}

/**
 * Cascading sidebar: sections fold open and closed; the section of the current page always opens. Sections the
 * user has no page in are hidden. The open sections are remembered per browser.
 */
export function SideNav({ items }: { items: NavItem[] }) {
  const { pathname } = useLocation();
  const byPath = new Map(items.filter((i) => i.show).map((i) => [i.to, i]));
  const groups = GROUPS.map((g) => ({ ...g, entries: g.items.map((p) => byPath.get(p)).filter((x): x is NavItem => !!x) }))
    .filter((g) => g.entries.length > 0);
  const current = groups.find((g) => g.entries.some((e) => isActive(e.to, pathname)))?.id;

  const [open, setOpen] = useState<Set<string>>(() => {
    try {
      const saved = localStorage.getItem(KEY);
      if (saved) return new Set(JSON.parse(saved) as string[]);
    } catch {
      /* storage unavailable: start with the current section only */
    }
    return new Set(current ? [current] : ["ops"]);
  });
  useEffect(() => {
    if (current && !open.has(current)) setOpen((o) => new Set(o).add(current));
  }, [current]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    try {
      localStorage.setItem(KEY, JSON.stringify([...open]));
    } catch {
      /* per-browser convenience only */
    }
  }, [open]);

  const toggle = (id: string) => setOpen((o) => {
    const n = new Set(o);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    return n;
  });

  // A single section (e.g. the security auditor) needs no folding.
  if (groups.length === 1) {
    return <nav className="sidenav">{groups[0].entries.map((e) => <Link key={e.to} item={e} />)}</nav>;
  }
  return (
    <nav className="sidenav">
      {groups.map((g) => {
        const isOpen = open.has(g.id);
        const GroupIcon = g.icon;
        return (
          <div key={g.id} className={`nav-group${isOpen ? " open" : ""}${g.id === current ? " current" : ""}`}>
            <button type="button" className="nav-group-head" aria-expanded={isOpen} aria-controls={`nav-${g.id}`} onClick={() => toggle(g.id)}>
              <GroupIcon size={18} strokeWidth={1.75} aria-hidden="true" />
              <span>{g.title}</span>
              <ChevronDown size={16} className="nav-chevron" aria-hidden="true" />
            </button>
            {isOpen && (
              <div className="nav-group-items" id={`nav-${g.id}`}>
                {g.entries.map((e) => <Link key={e.to} item={e} />)}
              </div>
            )}
          </div>
        );
      })}
    </nav>
  );
}

function Link({ item }: { item: NavItem }) {
  const Icon = NAV_ICONS[item.to];
  return (
    <NavLink to={item.to} end={item.to === "/"}>
      {Icon && <Icon size={17} strokeWidth={1.75} aria-hidden="true" />}{item.label}
    </NavLink>
  );
}
