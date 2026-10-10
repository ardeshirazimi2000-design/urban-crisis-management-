import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, newKey, type Incident } from "../lib/api";
import { useSession } from "../lib/session";
import { ago, BUILDING_USES, DAMAGE_TAGS, fmtTime, num, OBSERVATIONS, t } from "../lib/format";
import { Badge, Empty, ErrorBox, Freshness, Section, useAction, usePoll } from "../components/ui";
import { MapView } from "../components/MapView";
import { MapPicker, type PickedPoint } from "../components/MapPicker";

export type Assessment = {
  id: string; incident_id: string | null; incident_code: string | null; location: { lat: number; lng: number };
  address_text: string; building_use: string; floors: number | null; tag: string; observations: string[];
  people_trapped: boolean; occupants_estimate: number | null; notes: string; supersedes: string | null;
  superseded_at: string | null; report_id: string | null; assessor_name: string; created_at: string;
};

export function DamagePage() {
  const { can } = useSession();
  const [tag, setTag] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState<{ supersedes?: Assessment } | null>(null);
  const list = usePoll(() => api<{ items: Assessment[]; counts: Record<string, number>; people_trapped: number }>("GET", "/damage-assessments", { query: { tag } }), 15000, [tag]);
  const items = list.data?.items ?? [];
  const c = list.data?.counts ?? {};
  // Centre the map once on the first assessment; later polls must not move the map under the user.
  const [mapCenter, setMapCenter] = useState<[number, number] | undefined>();
  useEffect(() => { if (!mapCenter && items[0]) setMapCenter([items[0].location.lat, items[0].location.lng]); }, [mapCenter, items]);

  return (
    <div className="split">
      <Section title="ارزیابی سریع خسارت ساختمان‌ها" actions={<>
        <Freshness at={list.updatedAt} />
        {can("damage:assess") && <button className="btn primary small" onClick={() => { setCreating({}); setSelected(null); }}>ثبت ارزیابی</button>}
      </>}>
        <ErrorBox error={list.error} />
        {list.data && (
          <div className="stats">
            <div><span className="muted small">قرمز — ناایمن</span><b>{num(c.red ?? 0)}</b></div>
            <div><span className="muted small">زرد — استفاده محدود</span><b>{num(c.yellow ?? 0)}</b></div>
            <div><span className="muted small">سبز — قابل استفاده</span><b>{num(c.green ?? 0)}</b></div>
            <div className={list.data.people_trapped ? "warn" : ""}><span className="muted small">با احتمال افراد محبوس</span><b>{num(list.data.people_trapped)}</b></div>
          </div>
        )}
        <div className="row gap wrap filters">
          <label>برچسب
            <select value={tag} onChange={(e) => setTag(e.target.value)}>
              <option value="">همه</option>
              {DAMAGE_TAGS.map((x) => <option key={x} value={x}>{t(x)}</option>)}
            </select>
          </label>
        </div>
        <MapView height={300} features={items.map((a) => ({ type: "Feature", id: a.id, geometry: { type: "Point", coordinates: [a.location.lng, a.location.lat] },
          properties: { layer: "damage", tag: a.tag, building_use: a.building_use, people_trapped: a.people_trapped, assessed_at: a.created_at } }))}
          center={mapCenter}
          onSelect={(f) => setSelected(String(f.id))} />
        {list.data && items.length === 0 && <Empty>ارزیابی‌ای ثبت نشده است. ارزیاب‌ها از اپ امدادگر یا همین صفحه ثبت می‌کنند.</Empty>}
        <table className="table">
          <thead><tr><th>برچسب</th><th>کاربری</th><th>نشانی</th><th>مشاهدات</th><th>ارزیابی</th></tr></thead>
          <tbody>
            {items.map((a) => (
              <tr key={a.id} className={(selected === a.id ? "sel " : "") + (a.people_trapped ? "warn-row" : "")} onClick={() => { setSelected(a.id); setCreating(null); }}>
                <td><Badge value={a.tag} /></td>
                <td>{t(a.building_use)}{a.floors ? <span className="muted small"> · {num(a.floors)} طبقه</span> : null}</td>
                <td className="small">{a.address_text || "—"}{a.people_trapped && <div className="warn">احتمال افراد محبوس</div>}</td>
                <td className="small">{a.observations.map(t).join("، ") || "—"}</td>
                <td title={fmtTime(a.created_at)}>{ago(a.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      {creating ? <AssessmentForm supersedes={creating.supersedes} onDone={async (id) => { setCreating(null); await list.reload(); setSelected(id); }} onCancel={() => setCreating(null)} />
        : selected ? <AssessmentPanel id={selected} key={selected} onReassess={(a) => setCreating({ supersedes: a })} />
        : <Section title="جزئیات"><Empty>یک ساختمان را از جدول یا نقشه انتخاب کنید.</Empty></Section>}
    </div>
  );
}

function AssessmentPanel({ id, onReassess }: { id: string; onReassess: (a: Assessment) => void }) {
  const { can } = useSession();
  const d = usePoll(() => api<{ assessment: Assessment; history: Assessment[] }>("GET", `/damage-assessments/${id}`), 30000, [id]);
  if (!d.data) return <Section title="جزئیات"><ErrorBox error={d.error} /></Section>;
  const { assessment: a, history } = d.data;
  return (
    <Section title={<>{t(a.building_use)} <Badge value={a.tag} /></>}>
      {a.superseded_at && <p className="warn">این ارزیابی با بازدید جدیدتر جایگزین شده است ({ago(a.superseded_at)}).</p>}
      <dl className="kv">
        <dt>نشانی</dt><dd>{a.address_text || "—"}</dd>
        <dt>مختصات</dt><dd dir="ltr" style={{ textAlign: "end" }}>{a.location.lat.toFixed(5)}, {a.location.lng.toFixed(5)}</dd>
        <dt>طبقات / ساکنان</dt><dd>{a.floors ? num(a.floors) : "—"} / {a.occupants_estimate !== null ? num(a.occupants_estimate) : "—"}</dd>
        <dt>مشاهدات</dt><dd>{a.observations.map(t).join("، ") || "—"}</dd>
        <dt>افراد محبوس</dt><dd>{a.people_trapped ? <span className="warn">احتمال دارد{a.report_id && <> — گزارش در <Link to="/reports">صف گزارش‌ها</Link></>}</span> : "خیر"}</dd>
        <dt>حادثه</dt><dd>{a.incident_id ? <Link to={`/incidents/${a.incident_id}`}>{a.incident_code}</Link> : "—"}</dd>
        <dt>ارزیاب</dt><dd>{a.assessor_name} · {fmtTime(a.created_at)}</dd>
        {a.notes && <><dt>یادداشت</dt><dd className="pre">{a.notes}</dd></>}
      </dl>
      {can("damage:assess") && !a.superseded_at && (
        <div className="actions"><button className="btn" onClick={() => onReassess(a)}>بازدید مجدد (ارزیابی جدید)</button></div>
      )}
      <h3>سابقه بازدیدهای این ساختمان</h3>
      <ul className="timeline">
        {history.map((h) => <li key={h.id}><Badge value={h.tag} /> {h.assessor_name} · {fmtTime(h.created_at)}{h.id === a.id && " (همین)"}</li>)}
      </ul>
    </Section>
  );
}

function AssessmentForm({ supersedes, onDone, onCancel }: { supersedes?: Assessment; onDone: (id: string) => void; onCancel: () => void }) {
  const incidents = usePoll(() => api<{ items: Incident[] }>("GET", "/incidents", { query: { active: "true" } }), 0);
  const [key] = useState(newKey);
  const [loc, setLoc] = useState<PickedPoint | null>(supersedes ? supersedes.location : null);
  const [f, setF] = useState({
    tag: "", building_use: supersedes?.building_use ?? "residential", floors: supersedes?.floors ? String(supersedes.floors) : "",
    address_text: supersedes?.address_text ?? "", observations: [] as string[], people_trapped: false, occupants: "", notes: "",
    incident_id: supersedes?.incident_id ?? "",
  });
  const act = useAction();
  const needObs = f.tag === "yellow" || f.tag === "red";
  return (
    <Section title={supersedes ? "بازدید مجدد ساختمان" : "ثبت ارزیابی ساختمان"} actions={<button className="btn ghost small" onClick={onCancel}>بستن</button>}>
      <form onSubmit={(e) => {
        e.preventDefault();
        void act.run(async () => {
          const a = await api<Assessment>("POST", "/damage-assessments", { idempotent: key, body: {
            location: loc, building_use: f.building_use, tag: f.tag, address_text: f.address_text, observations: f.observations,
            people_trapped: f.people_trapped, notes: f.notes, floors: f.floors ? Number(f.floors) : undefined,
            occupants_estimate: f.occupants ? Number(f.occupants) : undefined, incident_id: f.incident_id || undefined,
            supersedes: supersedes?.id } });
          onDone(a.id);
        });
      }}>
        <div className="triage-pick">
          {["green", "yellow", "red"].map((x) => (
            <button type="button" key={x} className={`btn badge-${x}${f.tag === x ? " on" : ""}`} onClick={() => setF({ ...f, tag: x })}>{t(x)}</button>
          ))}
        </div>
        <label>موقعیت ساختمان (روی نقشه کلیک کنید) *</label>
        <MapPicker value={loc} accuracyM={15} onPick={setLoc} height={220} />
        <div className="row gap wrap">
          <label>کاربری<select value={f.building_use} onChange={(e) => setF({ ...f, building_use: e.target.value })}>
            {BUILDING_USES.map((x) => <option key={x} value={x}>{t(x)}</option>)}</select></label>
          <label>طبقات<input type="number" min={1} max={200} value={f.floors} onChange={(e) => setF({ ...f, floors: e.target.value })} style={{ width: 80 }} /></label>
          <label>ساکنان (تخمین)<input type="number" min={0} value={f.occupants} onChange={(e) => setF({ ...f, occupants: e.target.value })} style={{ width: 90 }} /></label>
          <label>حادثه<select value={f.incident_id} onChange={(e) => setF({ ...f, incident_id: e.target.value })}>
            <option value="">—</option>
            {incidents.data?.items.map((i) => <option key={i.id} value={i.id}>{i.code} — {i.title}</option>)}</select></label>
        </div>
        <label>نشانی<input value={f.address_text} onChange={(e) => setF({ ...f, address_text: e.target.value })} maxLength={300} /></label>
        <fieldset className="chk-group"><legend>مشاهدات {needObs && "(برای زرد و قرمز حداقل یک مورد)"}</legend>
          <div className="chk-grid">
            {OBSERVATIONS.map((o) => (
              <label key={o} className="chk"><input type="checkbox" checked={f.observations.includes(o)}
                onChange={(e) => setF({ ...f, observations: e.target.checked ? [...f.observations, o] : f.observations.filter((x) => x !== o) })} /> {t(o)}</label>
            ))}
          </div>
        </fieldset>
        <label className="chk warn"><input type="checkbox" checked={f.people_trapped} onChange={(e) => setF({ ...f, people_trapped: e.target.checked })} />
          احتمال افراد محبوس (یک گزارش فوری در صف گزارش‌ها ثبت می‌شود)</label>
        <label>یادداشت<textarea rows={2} value={f.notes} onChange={(e) => setF({ ...f, notes: e.target.value })} maxLength={1000} /></label>
        <button className="btn primary" disabled={act.busy || !f.tag || !loc || (needObs && f.observations.length === 0)}>ثبت ارزیابی</button>
      </form>
      <ErrorBox error={act.error} />
    </Section>
  );
}
