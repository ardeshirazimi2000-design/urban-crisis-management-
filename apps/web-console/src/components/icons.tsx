// Line icons (Lucide, MIT) bundled with the console; no icon CDN is contacted at runtime.
import {
  Ambulance, Biohazard, BookOpen, Building2, CircleAlert, Clock, CloudOff, Construction, Droplets, FileBarChart,
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
};

/** An icon followed by the label; the icon is decorative (the text carries the meaning). */
export function TypeLabel({ type, label }: { type: string; label: string }) {
  const Icon = TYPE_ICONS[type] ?? CircleAlert;
  return <span className="with-icon"><Icon size={16} strokeWidth={1.75} aria-hidden="true" />{label}</span>;
}

export { Clock, CloudOff, Inbox, Siren, TriangleAlert };
