import { useRef, useState } from "react";
import { api, newKey } from "../lib/api";
import { num, REPORT_TYPES, t } from "../lib/format";
import { ErrorBox, Section, useAction } from "../components/ui";
import { MapPicker, type PickedPoint } from "../components/MapPicker";

const ACCURACY = [50, 200, 500, 1000, 2000];
const OCCURRED = [
  { label: "همین حالا", minutes: 0 },
  { label: "حدود ۵ دقیقه پیش", minutes: 5 },
  { label: "حدود ۱۵ دقیقه پیش", minutes: 15 },
  { label: "حدود ۳۰ دقیقه پیش", minutes: 30 },
  { label: "حدود ۱ ساعت پیش", minutes: 60 },
  { label: "نامشخص", minutes: -1 },
];

const empty = { type: "", description: "", address: "", callerName: "", callerPhone: "", callback: false, accuracy: 200, occurred: 0 };

/**
 * Phone intake: the operator records a caller's report. It enters the normal review queue (not auto-accepted).
 * Caller details are personal data: stored separately and visible only to roles allowed to see exact locations.
 */
export function PhoneReportForm({ onCreated, onClose }: { onCreated: (id: string) => void; onClose: () => void }) {
  const [f, setF] = useState(empty);
  const [point, setPoint] = useState<PickedPoint | null>(null);
  const [done, setDone] = useState<{ id: string; dups: number } | null>(null);
  // One Idempotency-Key per filled form: a double click or retry after a timeout creates one report only.
  const key = useRef(newKey());
  const act = useAction();

  const submit = () =>
    act.run(async () => {
      const occurred = OCCURRED[f.occurred];
      const r = await api<{ report_id: string; possible_duplicates: number }>("POST", "/reports/phone", {
        idempotent: key.current,
        body: {
          type: f.type, description: f.description.trim(),
          location: { lat: point!.lat, lng: point!.lng, accuracy_m: f.accuracy },
          occurred_at: occurred.minutes >= 0 ? new Date(Date.now() - occurred.minutes * 60_000).toISOString() : undefined,
          caller_name: f.callerName.trim(), caller_phone: f.callerPhone.trim(), callback_requested: f.callback,
          address_text: f.address.trim(),
        },
      });
      setDone({ id: r.report_id, dups: r.possible_duplicates });
      key.current = newKey();
      setF(empty);
      setPoint(null);
      onCreated(r.report_id);
    });

  return (
    <Section title="ثبت گزارش تماس تلفنی" actions={<button className="btn ghost small" onClick={onClose}>بستن</button>}>
      {done && (
        <div className="banner info" role="status">
          گزارش ثبت شد و در صف بررسی قرار گرفت.
          {done.dups > 0 && <strong className="warn"> {num(done.dups)} گزارش مشابه در نزدیکی وجود دارد؛ احتمال تکرار را بررسی کنید.</strong>}
        </div>
      )}
      <form className="phone-form" onSubmit={(e) => { e.preventDefault(); if (point && f.type) void submit(); }}>
        <div className="grid-form">
          <label>نوع رخداد *
            <select value={f.type} onChange={(e) => setF({ ...f, type: e.target.value })} required name="type">
              <option value="">انتخاب کنید…</option>
              {REPORT_TYPES.map((x) => <option key={x} value={x}>{t(x)}</option>)}
            </select>
          </label>
          <label>زمان رخداد (به گفته تماس‌گیرنده)
            <select value={f.occurred} onChange={(e) => setF({ ...f, occurred: +e.target.value })}>
              {OCCURRED.map((o, i) => <option key={i} value={i}>{o.label}</option>)}
            </select>
          </label>
        </div>
        <label className="full">شرح (آنچه تماس‌گیرنده گفت؛ تعداد افراد، وضعیت، خطرها)
          <textarea rows={3} maxLength={2000} value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} name="description" />
        </label>
        <label className="full">نشانی گفته‌شده
          <input value={f.address} maxLength={300} onChange={(e) => setF({ ...f, address: e.target.value })} name="address"
            placeholder="مثلاً: خیابان …، کوچه …، پلاک …" />
        </label>

        <div className="row gap wrap">
          <strong>موقعیت روی نقشه *</strong>
          <span className="muted small">بر اساس نشانی روی نقشه کلیک کنید.</span>
          <label>دقت تخمینی
            <select value={f.accuracy} onChange={(e) => setF({ ...f, accuracy: +e.target.value })}>
              {ACCURACY.map((a) => <option key={a} value={a}>حدود {num(a)} متر</option>)}
            </select>
          </label>
        </div>
        <MapPicker value={point} accuracyM={f.accuracy} onPick={setPoint} />
        <div className="row gap wrap small">
          <label>عرض جغرافیایی
            <input dir="ltr" inputMode="decimal" size={10} value={point?.lat ?? ""} name="lat"
              onChange={(e) => setPoint({ lat: +e.target.value, lng: point?.lng ?? 51.39 })} />
          </label>
          <label>طول جغرافیایی
            <input dir="ltr" inputMode="decimal" size={10} value={point?.lng ?? ""} name="lng"
              onChange={(e) => setPoint({ lat: point?.lat ?? 35.7, lng: +e.target.value })} />
          </label>
          {!point && <span className="warn">موقعیت هنوز مشخص نشده است.</span>}
        </div>

        <fieldset className="caller">
          <legend>تماس‌گیرنده (اطلاعات شخصی؛ فقط برای نقش‌های مجاز قابل مشاهده)</legend>
          <div className="grid-form">
            <label>نام<input value={f.callerName} maxLength={100} onChange={(e) => setF({ ...f, callerName: e.target.value })} name="caller_name" /></label>
            <label>شماره تماس<input dir="ltr" inputMode="tel" value={f.callerPhone} onChange={(e) => setF({ ...f, callerPhone: e.target.value })} name="caller_phone" /></label>
            <label className="chk"><input type="checkbox" checked={f.callback} onChange={(e) => setF({ ...f, callback: e.target.checked })} name="callback" /> نیاز به تماس مجدد</label>
          </div>
        </fieldset>

        <ErrorBox error={act.error} />
        <button className="btn primary" disabled={act.busy || !point || !f.type}>{act.busy ? "در حال ثبت…" : "ثبت گزارش"}</button>
      </form>
    </Section>
  );
}
