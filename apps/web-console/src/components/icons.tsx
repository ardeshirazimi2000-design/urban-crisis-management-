// Line icons (Lucide, MIT) bundled with the console; no icon CDN is contacted at runtime.
import type { ReactNode } from "react";
import {
  Ambulance, BedDouble, Church, DoorOpen, Factory, Gauge, HeartPulse, Landmark, School, ShieldAlert, ShieldX, Store, UserCheck, Biohazard, BookOpen, Building2, CircleAlert, Clock, CloudOff, Construction, Droplets, FileBarChart,
  Flame, HardHat, Hospital, House, Inbox, LifeBuoy, Map, Megaphone, Mountain, PackageOpen, PersonStanding, ShieldCheck,
  ShieldHalf, Siren, Stethoscope, Tent, TriangleAlert, Truck, UserRound, Users, Waves, Wind, Wrench, Zap,
  type LucideIcon,
} from "lucide-react";

export type { LucideIcon };

/** Sidebar entries by route. */
export const NAV_ICONS: Record<string, LucideIcon> = {
  "/": Map, "/sitrep": FileBarChart, "/reports": Inbox, "/incidents": Siren, "/resources": Truck, "/needs": PackageOpen,
  "/shelters": Tent, "/hospitals": Hospital, "/damage": Building2, "/guidance": BookOpen, "/alerts": Megaphone,
  "/admin": Users, "/security": ShieldCheck, "/account": UserRound,
};

/** Report/incident and resource types. */
const TYPE_ICONS: Record<string, LucideIcon> = {
  structural_damage: Building2, building_collapse: Construction, trapped_people: PersonStanding, injury: Stethoscope,
  fire: Flame, gas_leak: Wind, road_blocked: TriangleAlert, flooding: Waves, power_outage: Zap, water_outage: Droplets,
  landslide: Mountain, hazmat: Biohazard, earthquake: Mountain, aftershock: Mountain, other: CircleAlert,
  ambulance: Ambulance, fire_truck: Truck, rescue_team: LifeBuoy, police_unit: ShieldHalf, shelter: House,
  equipment: Wrench, medical_team: HardHat,
  // Building uses (damage assessment)
  residential: House, school: School, hospital: Hospital, commercial: Store, government: Landmark, industrial: Factory,
  religious: Church,
};

/** Building safety placards: the icon reinforces the colour so the tag is never colour-only. */
const TAG_ICONS: Record<string, { icon: LucideIcon; tone: string }> = {
  red: { icon: ShieldX, tone: "tag-red" }, yellow: { icon: ShieldAlert, tone: "tag-yellow" }, green: { icon: ShieldCheck, tone: "tag-green" },
};

export function TagLabel({ tag, label }: { tag: string; label: string }) {
  const m = TAG_ICONS[tag];
  if (!m) return <>{label}</>;
  const Icon = m.icon;
  return <span className={`with-icon ${m.tone}`}><Icon size={16} strokeWidth={1.9} aria-hidden="true" />{label}</span>;
}

/** Summary figure with an icon tile, as on the dashboard. */
export function Stat({ icon: Icon, label, value, tone }: { icon: LucideIcon; label: string; value: ReactNode; tone?: "warn" | "red" | "yellow" | "green" }) {
  return (
    <div className={`stat${tone ? " stat-" + tone : ""}`}>
      <span className="stat-icon"><Icon size={20} strokeWidth={1.6} aria-hidden="true" /></span>
      <span className="stat-body"><span className="muted small">{label}</span><b>{value}</b></span>
    </div>
  );
}

/** Map layers that have no per-feature type. */
const LAYER_ICONS: Record<string, LucideIcon> = {
  reports: Inbox, incidents: Siren, resources: Truck, damage: Building2, hospital: Hospital, fire_station: Flame,
  shelter: Tent, assembly_point: Users, hazard: TriangleAlert, road_closure: Construction, impact_areas: CircleAlert,
};

/** The icon for a feature's type, falling back to its layer. */
export function iconFor(type: string | undefined, layer?: string): LucideIcon {
  return (type && TYPE_ICONS[type]) || (layer && LAYER_ICONS[layer]) || CircleAlert;
}

/** An icon followed by the label; the icon is decorative (the text carries the meaning). */
export function TypeLabel({ type, label }: { type: string; label: string }) {
  const Icon = iconFor(type);
  return <span className="with-icon"><Icon size={16} strokeWidth={1.75} aria-hidden="true" />{label}</span>;
}

export { BedDouble, Clock, CloudOff, DoorOpen, Gauge, HeartPulse, Hospital, Inbox, ShieldAlert, ShieldCheck, ShieldX, Siren, Tent, TriangleAlert, UserCheck, Users, PersonStanding, Ambulance };
