// Persian labels and time formatting. Times are stored in UTC and always shown with an explicit zone.

const dtf = new Intl.DateTimeFormat("fa-IR-u-ca-persian", {
  timeZone: "Asia/Tehran", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit",
});

export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  return dtf.format(new Date(iso)) + " (تهران)";
}

/** Relative age such as "۵ دقیقه پیش"; used to make data freshness visible. */
export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "نامشخص";
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  const nf = new Intl.NumberFormat("fa-IR");
  if (s < 60) return `${nf.format(s)} ثانیه پیش`;
  if (s < 3600) return `${nf.format(Math.floor(s / 60))} دقیقه پیش`;
  if (s < 86400) return `${nf.format(Math.floor(s / 3600))} ساعت پیش`;
  return `${nf.format(Math.floor(s / 86400))} روز پیش`;
}

export const num = (n: number | null | undefined, digits = 0) =>
  n === null || n === undefined ? "—" : new Intl.NumberFormat("fa-IR", { maximumFractionDigits: digits }).format(n);

export const L: Record<string, string> = {
  // report types
  structural_damage: "آسیب سازه‌ای", building_collapse: "ریزش ساختمان", trapped_people: "افراد محبوس", injury: "مصدومیت",
  fire: "آتش‌سوزی", gas_leak: "نشت گاز", road_blocked: "انسداد مسیر", flooding: "آب‌گرفتگی", power_outage: "قطع برق",
  water_outage: "قطع آب", landslide: "رانش زمین", hazmat: "مواد خطرناک", other: "سایر", earthquake: "زلزله", aftershock: "پس‌لرزه",
  // report source
  phone: "تماس تلفنی", citizen_app: "اپ شهروند",
  // location source
  gps: "GPS", network: "شبکه", manual: "تعیین دستی روی نقشه",
  // report status
  received: "دریافت‌شده", triage: "تریاژ", under_review: "در حال بررسی", accepted: "تأییدشده", rejected: "ردشده",
  duplicate: "تکراری", linked_to_incident: "پیوند به حادثه",
  // incident status
  draft: "پیش‌نویس", open: "باز", active: "فعال", escalated: "تشدیدشده", contained: "مهارشده", resolved: "پایان‌یافته", closed: "بسته",
  // severities
  low: "کم", medium: "متوسط", high: "زیاد", critical: "بحرانی",
  advisory: "توصیه", watch: "آماده‌باش", warning: "هشدار", emergency: "اضطراری",
  // alert status
  pending_approval: "در انتظار تأیید", approved: "تأییدشده", sending: "در حال ارسال", partially_sent: "ارسال ناقص", sent: "ارسال‌شده",
  failed: "ناموفق", cancelled: "لغوشده", expired: "منقضی",
  // attempt status
  pending: "در صف", in_flight: "در حال ارسال", delivered: "تحویل‌شده (گزارش ارائه‌دهنده)", unknown: "نامشخص (در حال تطبیق)",
  superseded: "متوقف‌شده", not_started: "شروع‌نشده", done: "انجام‌شده", skipped: "ردشده از تحلیل",
  // resources
  ambulance: "آمبولانس", fire_truck: "خودرو آتش‌نشانی", rescue_team: "تیم امداد و نجات", police_unit: "واحد انتظامی",
  shelter: "اسکان اضطراری", equipment: "تجهیزات", medical_team: "تیم درمانی",
  available: "آماده", assigned: "تخصیص‌یافته", en_route: "در مسیر", on_scene: "در محل", out_of_service: "خارج از خدمت",
  proposed: "پیشنهادی", acknowledged: "تأیید دریافت", completed: "تکمیل‌شده",
  // modes / channels
  test: "آزمایشی", operational: "عملیاتی", internal: "داخلی", push: "اعلان اپ", sms: "پیامک", cell_broadcast: "Cell Broadcast",
  // layers
  reports: "گزارش‌ها", incidents: "حوادث", resources: "منابع", impact_areas: "محدوده اثر (برآورد)", hospital: "بیمارستان",
  fire_station: "ایستگاه آتش‌نشانی", road_closure: "انسداد مسیر", hazard: "خطر", assembly_point: "نقطه تجمع",
  // needs
  heavy_equipment: "ماشین‌آلات سنگین", shelter_space: "جای اسکان", water: "آب آشامیدنی", food: "غذا", blanket: "پتو",
  tent: "چادر", medicine: "دارو", partially_met: "تأمین ناقص", met: "تأمین‌شده",
  // triage and casualty status
  immediate: "فوری (قرمز)", delayed: "تأخیری (زرد)", minor: "سرپایی (سبز)", deceased: "فوت‌شده (سیاه)",
  transported: "در حال انتقال", admitted: "بستری", released: "ترخیص",
  child: "کودک", adult: "بزرگسال", elderly: "سالمند", female: "زن", male: "مرد",
  // damage assessment
  damage: "ارزیابی خسارت ساختمان", damage_assessment: "ارزیابی خسارت", green: "سبز (قابل استفاده)", yellow: "زرد (استفاده محدود)",
  red: "قرمز (ناایمن)", residential: "مسکونی", school: "مدرسه", commercial: "تجاری", government: "اداری", industrial: "صنعتی",
  religious: "مذهبی", collapse_total: "ریزش کامل", collapse_partial: "ریزش بخشی", leaning: "کج‌شدگی ساختمان",
  major_cracks: "ترک‌های عمده", column_damage: "آسیب ستون/تیر", foundation: "آسیب پی", falling_hazard: "خطر سقوط اجزا (نما، دودکش)",
  water_leak: "نشت آب", adjacent_hazard: "خطر از ساختمان مجاور",
  // roles
  CITIZEN: "شهروند", RESPONDER: "امدادگر", OPERATOR: "اپراتور", COMMANDER: "فرمانده", RESOURCE_MANAGER: "مدیر منابع",
  GIS_ANALYST: "کارشناس GIS", SECURITY_ADMIN: "مدیر امنیت",
};

export const t = (k: string | null | undefined) => (k ? L[k] ?? k : "—");

// Delivery-attempt wording differs from report wording: provider acceptance is not user receipt.
const ATTEMPT: Record<string, string> = { accepted: "پذیرفته‌شده توسط ارائه‌دهنده", rejected: "ردشده توسط ارائه‌دهنده" };
export const tAttempt = (k: string) => ATTEMPT[k] ?? t(k);

export const REPORT_TYPES = ["structural_damage", "building_collapse", "trapped_people", "injury", "fire", "gas_leak",
  "road_blocked", "flooding", "power_outage", "water_outage", "landslide", "hazmat", "other"];
export const SEVERITIES = ["low", "medium", "high", "critical"];
export const DAMAGE_TAGS = ["red", "yellow", "green"];
export const BUILDING_USES = ["residential", "school", "hospital", "commercial", "government", "industrial", "religious", "other"];
export const OBSERVATIONS = ["collapse_total", "collapse_partial", "leaning", "major_cracks", "column_damage", "foundation",
  "falling_hazard", "gas_leak", "fire", "water_leak", "adjacent_hazard"];
export const TRIAGE = ["immediate", "delayed", "minor", "deceased"];
/** ER status words differ from generic open/closed. */
export const ER_LABEL: Record<string, string> = {
  open: "پذیرش", limited: "پذیرش محدود", diverting: "عدم پذیرش (ارجاع به دیگر مراکز)", closed: "بسته", unknown: "گزارش نشده",
};
export const NEED_CATEGORIES = ["rescue_team", "ambulance", "fire_truck", "medical_team", "heavy_equipment", "shelter_space",
  "water", "food", "blanket", "tent", "medicine", "other"];
/** Suggested unit per need category (editable in the form). */
export const NEED_UNITS: Record<string, string> = {
  rescue_team: "تیم", ambulance: "دستگاه", fire_truck: "دستگاه", medical_team: "تیم", heavy_equipment: "دستگاه",
  shelter_space: "نفر", water: "لیتر", food: "بسته", blanket: "عدد", tent: "عدد", medicine: "بسته", other: "واحد",
};
export const RESOURCE_TYPES = ["ambulance", "fire_truck", "rescue_team", "police_unit", "shelter", "equipment", "medical_team"];

/** Approximates a circle as a closed GeoJSON polygon (used for alert target regions). */
export function circlePolygon(lat: number, lng: number, radiusM: number, steps = 32) {
  const coords: [number, number][] = [];
  const dLat = radiusM / 111_320;
  const dLng = radiusM / (111_320 * Math.cos((lat * Math.PI) / 180));
  for (let i = 0; i < steps; i++) {
    const a = (2 * Math.PI * i) / steps;
    coords.push([+(lng + dLng * Math.cos(a)).toFixed(6), +(lat + dLat * Math.sin(a)).toFixed(6)]);
  }
  coords.push(coords[0]);
  return { type: "Polygon", coordinates: [coords] };
}
