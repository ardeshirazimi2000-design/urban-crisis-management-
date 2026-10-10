import { useState } from "react";
import { Link } from "react-router-dom";
import { api, newKey, type Casualty, type Hospital } from "../lib/api";
import { useSession } from "../lib/session";
import { ago, ER_LABEL, fmtTime, num, t, TRIAGE } from "../lib/format";
import { Badge, Empty, ErrorBox, Freshness, ReasonAction, Section, useAction, usePoll } from "../components/ui";
import { Meter } from "./Needs";

const AGE: Record<string, string> = { child: "کودک", adult: "بزرگسال", elderly: "سالمند", unknown: "نامشخص" };
const SEX: Record<string, string> = { female: "زن", male: "مرد", unknown: "نامشخص" };
const STATUS: Record<string, string> = {
  on_scene: "در محل حادثه", transported: "در حال انتقال", admitted: "بستری", released: "ترخیص", deceased: "فوت",
};

function ER({ h }: { h: Pick<Hospital, "er_status"> }) {
  return <span className={`badge badge-er-${h.er_status}`}>{ER_LABEL[h.er_status] ?? h.er_status}</span>;
}

type Summary = { hospitals?: number; receiving?: number; beds_available?: number; icu_available?: number; incoming?: number; admitted?: number; not_current?: number };

export function HospitalsPage() {
  const [selected, setSelected] = useState<string | null>(null);
  const list = usePoll(() => api<{ items: Hospital[]; summary: Summary }>("GET", "/hospitals"), 15000);
  const s = list.data?.summary ?? {};
  return (
    <div className="split">
      <Section title="ظرفیت بیمارستان‌ها" actions={<Freshness at={list.updatedAt} />}>
        <ErrorBox error={list.error} />
        {list.data && (
          <div className="stats">
            <div><span className="muted small">بیمارستان پذیرنده</span><b>{num(s.receiving ?? 0)} از {num(s.hospitals ?? 0)}</b></div>
            <div><span className="muted small">تخت خالی</span><b>{num(s.beds_available ?? 0)}</b></div>
            <div><span className="muted small">تخت ویژه (ICU) خالی</span><b>{num(s.icu_available ?? 0)}</b></div>
            <div><span className="muted small">مصدوم در راه</span><b>{num(s.incoming ?? 0)}</b></div>
            <div><span className="muted small">بستری</span><b>{num(s.admitted ?? 0)}</b></div>
            <div className={s.not_current ? "warn" : ""}><span className="muted small">بدون گزارش به‌روز</span><b>{num(s.not_current ?? 0)}</b></div>
          </div>
        )}
        <table className="table">
          <thead><tr><th>بیمارستان</th><th>اورژانس</th><th>تخت خالی</th><th>ICU</th><th>در راه</th><th>آخرین گزارش</th></tr></thead>
          <tbody>
            {list.data?.items.map((h) => (
              <tr key={h.id} className={(selected === h.id ? "sel " : "") + (h.stale ? "warn-row" : "")} onClick={() => setSelected(h.id)}>
                <td>{h.name}</td>
                <td><ER h={h} /></td>
                <td>{h.reported ? <>{num(h.beds_available)}{h.beds_total !== null && <span className="muted small"> از {num(h.beds_total)}</span>}</> : "—"}</td>
                <td>{h.reported ? num(h.icu_available) : "—"}</td>
                <td>{num(h.incoming)}</td>
                <td>{h.updated_at ? <>{ago(h.updated_at)}{h.stale && <span className="warn small"> (کهنه)</span>}</> : "هرگز"}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="muted small">گزارش قدیمی‌تر از ۶ ساعت «کهنه» علامت می‌خورد و نباید وضعیت فعلی فرض شود. بیمارستان‌ها از لایه «بیمارستان» نقشه می‌آیند.</p>
      </Section>
      {selected ? <HospitalPanel id={selected} key={selected} onChanged={list.reload} />
        : <Section title="جزئیات بیمارستان"><Empty>یک بیمارستان را انتخاب کنید.</Empty></Section>}
    </div>
  );
}

function HospitalPanel({ id, onChanged }: { id: string; onChanged: () => void }) {
  const d = usePoll(() => api<{ hospital: Hospital; log: { beds_total: number | null; beds_available: number; icu_available: number; er_status: string; note: string; actor_name: string; created_at: string }[]; patients: Casualty[]; can_update: boolean }>("GET", `/hospitals/${id}`), 15000, [id]);
  const [form, setForm] = useState<{ total: string; beds: string; icu: string; er: string; note: string } | null>(null);
  const act = useAction();
  if (!d.data) return <Section title="جزئیات بیمارستان"><ErrorBox error={d.error} /></Section>;
  const { hospital: h, log, patients, can_update } = d.data;
  const f = form ?? { total: h.beds_total === null ? "" : String(h.beds_total), beds: String(h.beds_available), icu: String(h.icu_available), er: h.er_status === "unknown" ? "open" : h.er_status, note: "" };
  const refresh = async () => { await d.reload(); onChanged(); };

  return (
    <Section title={<>{h.name} <ER h={h} /></>} actions={<Freshness at={d.updatedAt} />}>
      <dl className="kv">
        <dt>تخت خالی</dt><dd>{h.reported ? `${num(h.beds_available)}${h.beds_total !== null ? ` از ${num(h.beds_total)}` : ""}` : "گزارش نشده"}</dd>
        <dt>ICU خالی</dt><dd>{h.reported ? num(h.icu_available) : "—"}</dd>
        <dt>در راه / بستری</dt><dd>{num(h.incoming)} / {num(h.admitted)}</dd>
        <dt>آخرین گزارش</dt><dd>{h.updated_at ? fmtTime(h.updated_at) : "هرگز"}{h.stale && <span className="warn"> — کهنه</span>}</dd>
        {h.note && <><dt>یادداشت</dt><dd>{h.note}</dd></>}
      </dl>
      {h.beds_total !== null && <Meter value={h.beds_total - h.beds_available} max={h.beds_total} invert />}

      {can_update && (
        <form className="actions inline-form" onSubmit={(e) => {
          e.preventDefault();
          void act.run(async () => {
            await api("POST", `/hospitals/${id}/capacity`, { body: {
              beds_total: f.total === "" ? undefined : Number(f.total), beds_available: Number(f.beds), icu_available: Number(f.icu),
              er_status: f.er, note: f.note, version: h.version } });
            setForm(null); await refresh();
          });
        }}>
          <label>کل تخت‌ها<input type="number" min={0} value={f.total} onChange={(e) => setForm({ ...f, total: e.target.value })} /></label>
          <label>تخت خالی<input type="number" min={0} value={f.beds} onChange={(e) => setForm({ ...f, beds: e.target.value })} required /></label>
          <label>ICU خالی<input type="number" min={0} value={f.icu} onChange={(e) => setForm({ ...f, icu: e.target.value })} required /></label>
          <label>وضعیت اورژانس
            <select value={f.er} onChange={(e) => setForm({ ...f, er: e.target.value })}>
              {["open", "limited", "diverting", "closed"].map((x) => <option key={x} value={x}>{ER_LABEL[x]}</option>)}
            </select>
          </label>
          <label>یادداشت<input value={f.note} onChange={(e) => setForm({ ...f, note: e.target.value })} maxLength={500} /></label>
          <button className="btn primary" disabled={act.busy}>ثبت گزارش ظرفیت</button>
        </form>
      )}
      <ErrorBox error={act.error} />

      {(can_update || patients.length > 0) && <h3>بیماران در راه و بستری</h3>}
      {can_update && patients.length === 0 && <Empty>بیماری در راه یا بستری نیست.</Empty>}
      {patients.length > 0 && (
        <table className="table">
          <thead><tr><th>برچسب</th><th>تریاژ</th><th>وضعیت</th><th>حادثه</th><th /></tr></thead>
          <tbody>
            {patients.map((c) => (
              <tr key={c.id}>
                <td dir="ltr" className="nowrap">{c.tag_no}</td><td><Badge value={c.triage} /></td><td>{STATUS[c.status]}</td><td className="small">{c.incident_code}</td>
                <td>{can_update && <CasualtyActions c={c} onDone={refresh} hospitalView />}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h3>سابقه گزارش‌ها</h3>
      {log.length === 0 && <Empty>گزارشی ثبت نشده است.</Empty>}
      <ul className="timeline">
        {log.map((l, k) => (
          <li key={k}><b>{ER_LABEL[l.er_status]}</b> · تخت خالی {num(l.beds_available)}، ICU {num(l.icu_available)} · {l.actor_name} · {fmtTime(l.created_at)}
            {l.note && <div className="muted small">{l.note}</div>}</li>
        ))}
      </ul>
    </Section>
  );
}

/** Status actions for one casualty: transport (with suggested hospitals), admit, release, death, re-triage. */
function CasualtyActions({ c, onDone, near, hospitalView }: { c: Casualty; onDone: () => Promise<void> | void; near?: { lat: number; lng: number } | null; hospitalView?: boolean }) {
  const [open, setOpen] = useState(false);
  const [hospital, setHospital] = useState("");
  const act = useAction();
  const at = c.location ?? near;
  const sug = usePoll(async () => (open && at ? api<{ items: Hospital[]; note: string }>("GET", "/hospitals/suggest",
    { query: { lat: at.lat, lng: at.lng, triage: c.triage === "deceased" ? "minor" : c.triage } }) : { items: [], note: "" }), 0, [open]);
  const all = usePoll(async () => (open ? api<{ items: Hospital[] }>("GET", "/hospitals") : { items: [] }), 0, [open]);
  const status = (s: string, extra: Record<string, unknown> = {}) => api("POST", `/casualties/${c.id}/status`, { body: { status: s, version: c.version, ...extra } });

  return (
    <div className="row gap wrap">
      {c.status === "on_scene" && !hospitalView && (
        open ? (
          <div className="reason-form">
            <strong>انتقال به بیمارستان</strong>
            {sug.data && sug.data.items.length > 0 && <div className="muted small">پیشنهاد (نزدیک‌ترین با تخت خالی): {sug.data.items.map((h) => `${h.name} (${num((h.distance_m ?? 0) / 1000, 1)} کم)`).join("، ")}</div>}
            <select value={hospital} onChange={(e) => setHospital(e.target.value)}>
              <option value="">انتخاب بیمارستان…</option>
              {(sug.data?.items ?? []).map((h) => <option key={"s" + h.id} value={h.id}>★ {h.name} — {num(h.beds_available)} تخت</option>)}
              {(all.data?.items ?? []).filter((h) => !sug.data?.items.some((x) => x.id === h.id)).map((h) =>
                <option key={h.id} value={h.id}>{h.name} — {ER_LABEL[h.er_status]}</option>)}
            </select>
            <ReasonAction label="انتقال" required={false} onSubmit={async (reason) => { await status("transported", { hospital_id: hospital, reason }); setOpen(false); await onDone(); }} />
            <button className="btn ghost small" onClick={() => setOpen(false)}>انصراف</button>
            <ErrorBox error={act.error} />
          </div>
        ) : <button className="btn small" onClick={() => setOpen(true)}>انتقال</button>
      )}
      {c.status === "transported" && <button className="btn small" disabled={act.busy} onClick={() => act.run(async () => { await status("admitted"); await onDone(); })}>پذیرش و بستری</button>}
      {["on_scene", "transported", "admitted"].includes(c.status) && (
        <>
          <ReasonAction label="ترخیص" required={false} onSubmit={async (reason) => { await status("released", { reason }); await onDone(); }} />
          <ReasonAction label="ثبت فوت" danger onSubmit={async (reason) => { await status("deceased", { reason }); await onDone(); }} />
        </>
      )}
      <ErrorBox error={act.error} />
    </div>
  );
}

/** Casualties of one incident (incident page): counts by triage colour, list, field-triage form. */
export function IncidentCasualties({ incidentId, location, active }: { incidentId: string; location: { lat: number; lng: number } | null; active: boolean }) {
  const { can } = useSession();
  const list = usePoll(() => api<{ items: Casualty[]; by_triage: Record<string, number>; by_status: Record<string, number> }>("GET", `/incidents/${incidentId}/casualties`), 15000, [incidentId]);
  const [f, setF] = useState({ triage: "", age_group: "unknown", sex: "unknown", tag_no: "", notes: "" });
  const [retriage, setRetriage] = useState<Record<string, string>>({});
  const act = useAction();
  if (list.error?.status === 404 || list.error?.status === 403) return null; // not in this caller's scope
  const items = list.data?.items ?? [];

  return (
    <Section title="مصدومان" actions={<Link className="btn small ghost" to="/hospitals">ظرفیت بیمارستان‌ها</Link>}>
      <ErrorBox error={list.error} />
      <div className="row gap wrap">
        {TRIAGE.map((x) => <span key={x} className={`badge badge-${x}`}>{t(x)}: {num(list.data?.by_triage[x] ?? 0)}</span>)}
        <span className="muted small">در حال انتقال {num(list.data?.by_status.transported ?? 0)} · بستری {num(list.data?.by_status.admitted ?? 0)}</span>
      </div>
      {list.data && items.length === 0 && <Empty>مصدومی ثبت نشده است.</Empty>}
      {items.length > 0 && (
        <table className="table">
          <thead><tr><th>برچسب</th><th>تریاژ</th><th>سن / جنس</th><th>وضعیت</th><th>بیمارستان</th><th /></tr></thead>
          <tbody>
            {items.map((c) => (
              <tr key={c.id}>
                <td dir="ltr" className="nowrap">{c.tag_no}</td>
                <td><Badge value={c.triage} /></td>
                <td className="small">{AGE[c.age_group]} / {SEX[c.sex]}</td>
                <td>{STATUS[c.status]}<div className="muted small">{ago(c.updated_at)}</div></td>
                <td className="small">{c.hospital_name ?? "—"}</td>
                <td>
                  {can("casualty:record") && <CasualtyActions c={c} near={location} onDone={list.reload} />}
                  {can("casualty:record") && ["on_scene", "transported", "admitted"].includes(c.status) && (
                    <ReasonAction label="تغییر تریاژ" onSubmit={async (reason) => {
                      await api("POST", `/casualties/${c.id}/triage`, { body: { triage: retriage[c.id] || "immediate", reason, version: c.version } });
                      await list.reload();
                    }} extra={<select value={retriage[c.id] ?? ""} onChange={(e) => setRetriage({ ...retriage, [c.id]: e.target.value })} required>
                      <option value="">تریاژ جدید…</option>
                      {TRIAGE.filter((x) => x !== c.triage).map((x) => <option key={x} value={x}>{t(x)}</option>)}
                    </select>} />
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {can("casualty:record") && active && (
        <form className="actions" onSubmit={(e) => {
          e.preventDefault();
          void act.run(async () => {
            await api("POST", `/incidents/${incidentId}/casualties`, { idempotent: newKey(), body: { ...f, location: location ?? undefined } });
            setF({ triage: "", age_group: "unknown", sex: "unknown", tag_no: "", notes: "" }); await list.reload();
          });
        }}>
          <div className="triage-pick">
            {TRIAGE.map((x) => (
              <button type="button" key={x} className={`btn badge-${x}${f.triage === x ? " on" : ""}`} onClick={() => setF({ ...f, triage: x })}>{t(x)}</button>
            ))}
          </div>
          <label>سن<select value={f.age_group} onChange={(e) => setF({ ...f, age_group: e.target.value })}>
            {Object.entries(AGE).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></label>
          <label>جنس<select value={f.sex} onChange={(e) => setF({ ...f, sex: e.target.value })}>
            {Object.entries(SEX).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></label>
          <label>شماره برچسب (اختیاری)<input value={f.tag_no} onChange={(e) => setF({ ...f, tag_no: e.target.value })} dir="ltr" maxLength={20} style={{ width: 110 }} /></label>
          <label>یادداشت پزشکی<input value={f.notes} onChange={(e) => setF({ ...f, notes: e.target.value })} maxLength={500} /></label>
          <button className="btn primary" disabled={act.busy || !f.triage}>ثبت مصدوم</button>
          <span className="muted small">نام و کد ملی ثبت نمی‌شود؛ شماره برچسب تریاژ بیمار را دنبال می‌کند.</span>
        </form>
      )}
      <ErrorBox error={act.error} />
    </Section>
  );
}
