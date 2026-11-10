import {
  Ambulance, Archive, ArrowLeft, ArrowRight, Ban, BedDouble, Check, CheckCheck, ChevronDown, ChevronUp, CircleHelp, Copy, DoorClosed,
  DoorOpen, Download, FileJson, FilePen, FilePlus2, FileSpreadsheet, Fingerprint, Gauge, Hand, Hospital, Image, KeyRound, Link2,
  List, LockOpen, LogIn, LogOut, Megaphone, PauseCircle, Pencil, PhoneCall, PlayCircle, Plus, Printer, RefreshCcw, RotateCw,
  Save, Send, ShieldCheck, ShieldPlus, ShieldMinus, UserPlus, Wrench, X, type LucideIcon,
} from "lucide-react";

/**
 * Icon for a button by what it does, so the same action looks the same on every page. Rules are matched in
 * order against the button's (Persian) label; the first match wins.
 */
const RULES: [RegExp, LucideIcon][] = [
  [/^ورود/, LogIn],
  [/^خروج( از سامانه)?$/, LogOut],
  [/^(انصراف|لغو|بستن|بستم)$/, X],
  [/^بستن پذیرش/, DoorClosed],
  [/^باز کردن پذیرش/, DoorOpen],
  [/^بستن ماتریس/, ChevronUp],
  [/^نمایش ماتریس/, ChevronDown],
  [/^بازگشت به خدمت/, Wrench],
  [/^خارج از خدمت/, PauseCircle],
  [/^بازگشت/, ArrowRight],
  [/^ادامه/, ArrowLeft],
  [/(تلاش دوباره|بارگذاری دوباره)/, RotateCw],
  [/^کپی/, Copy],
  [/^چاپ/, Printer],
  [/CSV/, FileSpreadsheet],
  [/JSON/, FileJson],
  [/^خروجی/, Download],
  [/شاهکار|^استعلام/, Fingerprint],
  [/^بررسی زنجیره/, Link2],
  [/^بررسی صحت/, ShieldCheck],
  [/^برداشتن برای بررسی/, Hand],
  [/رمز/, KeyRound],
  [/^بازگشایی قفل/, LockOpen],
  [/^(ابطال|حذف)/, Ban],
  [/^اعطای/, ShieldPlus],
  [/^(تعلیق|غیرفعال‌سازی)/, ShieldMinus],
  [/^فعال‌سازی/, PlayCircle],
  [/^بازنشسته/, Archive],
  [/^(ویرایش|پیشنهاد اصلاح)/, Pencil],
  [/^بازدید مجدد/, RefreshCcw],
  [/^تعریف کاربر/, UserPlus],
  [/^صدور اصلاحیه/, FilePen],
  [/^پیش‌نویس/, FilePlus2],
  [/جدید$/, Plus],
  [/^ثبت گزارش تلفنی/, PhoneCall],
  [/^ارسال برای تأیید/, Send],
  [/^ارسال$/, Megaphone],
  [/^تأیید/, CheckCheck],
  [/^انتقال به/, ArrowLeft],
  [/^انتقال/, Ambulance],
  [/^پذیرش/, BedDouble],
  [/ظرفیت بیمارستان/, Hospital],
  [/ظرفیت/, Gauge],
  [/^همه /, List],
  [/^راهنما/, CircleHelp],
  [/^(ذخیره|صدور)/, Save],
  [/^ثبت/, Check],
  [/^(تصویر|عکس)|image\//, Image],
];

export function actionIcon(label: string): LucideIcon | undefined {
  return RULES.find(([re]) => re.test(label.trim()))?.[1];
}

/** A button label with its action icon. */
export function Act({ children }: { children: string }) {
  const Icon = actionIcon(children);
  return <>{Icon && <Icon size={16} strokeWidth={2} aria-hidden="true" className="act-ico" />}{children}</>;
}
