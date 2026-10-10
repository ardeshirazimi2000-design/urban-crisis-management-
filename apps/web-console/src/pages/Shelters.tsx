import { useState } from "react";
import { api, type Shelter, type ShelterLog } from "../lib/api";
import { useSession } from "../lib/session";
import { ago, fmtTime, num } from "../lib/format";
import { Empty, ErrorBox, Freshness, Section, useAction, usePoll } from "../components/ui";
import { Meter } from "./Needs";

type Summary = { shelters: number; open: number; capacity: number; occupancy: number; available_in_open: number };

function ShelterState({ s }: { s: Shelter }) {
  if (s.resource_status === "out_of_service") return <span className="badge badge-failed">خارج از خدمت</span>;
  if (!s.accepting) return <span className="badge badge-rejected">پذیرش بسته</span>;
  if (s.capacity === null) return <span className="badge badge-unknown">ظرفیت نامشخص</span>;
  if (s.full) return <span className="badge badge-high">تکمیل</span>;
  return <span className="badge badge-available">پذیرش باز</span>;
}

export function SheltersPage() {
  const { orgName } = useSession();
  const [selected, setSelected] = useState<string | null>(null);
  const list = usePoll(() => api<{ items: Shelter[]; summary: Summary }>("GET", "/shelters"), 15000);
  const sum = list.data?.summary;

  return (
    <div className="split">
      <Section title="ظرفیت اسکان اضطراری" actions={<Freshness at={list.updatedAt} />}>
        <ErrorBox error={list.error} />
        {sum && (
          <div className="stats">
            <div><span className="muted small">محل‌های اسکان</span><b>{num(sum.shelters)}</b></div>
            <div><span className="muted small">پذیرش باز</span><b>{num(sum.open)}</b></div>
            <div><span className="muted small">اسکان‌یافته</span><b>{num(sum.occupancy)}</b></div>
            <div><span className="muted small">ظرفیت کل</span><b>{num(sum.capacity)}</b></div>
            <div><span className="muted small">جای خالی (محل‌های باز)</span><b>{num(sum.available_in_open)}</b></div>
          </div>
        )}
        {list.data && list.data.items.length === 0 && <Empty>محل اسکانی در محدوده دسترسی شما ثبت نشده است (منابع از نوع «اسکان اضطراری»).</Empty>}
        <table className="table">
          <thead><tr><th>محل اسکان</th><th>سازمان</th><th>پرشدگی</th><th>جای خالی</th><th>وضعیت</th><th>آخرین تغییر</th></tr></thead>
          <tbody>
            {list.data?.items.map((s) => (
              <tr key={s.id} className={selected === s.id ? "sel" : ""} onClick={() => setSelected(s.id)}>
                <td>{s.name}</td>
                <td className="small">{orgName(s.organization_id)}</td>
                <td>
                  {s.capacity !== null && <Meter value={s.occupancy} max={s.capacity} invert />}
                  <span className="small">{num(s.occupancy)} از {s.capacity === null ? "؟" : num(s.capacity)} نفر</span>
                </td>
                <td>{s.available === null ? "—" : num(s.available)}</td>
                <td><ShelterState s={s} /></td>
                <td>{s.updated_at ? ago(s.updated_at) : "بدون ثبت"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      {selected ? <ShelterPanel id={selected} key={selected} onChanged={list.reload} />
        : <Section title="جزئیات محل اسکان"><Empty>یک محل اسکان را انتخاب کنید.</Empty></Section>}
    </div>
  );
}

function ShelterPanel({ id, onChanged }: { id: string; onChanged: () => void }) {
  const { can, orgName } = useSession();
  const d = usePoll(() => api<{ shelter: Shelter; log: ShelterLog[] }>("GET", `/shelters/${id}`), 15000, [id]);
  const [admitted, setAdmitted] = useState("");
  const [discharged, setDischarged] = useState("");
  const [note, setNote] = useState("");
  const [capacity, setCapacity] = useState("");
  const [reason, setReason] = useState("");
  const move = useAction();
  const settings = useAction();
  if (!d.data) return <Section title="جزئیات محل اسکان"><ErrorBox error={d.error} /></Section>;
  const { shelter: s, log } = d.data;
  const refresh = async () => { await d.reload(); onChanged(); };
  const saveSettings = (body: Record<string, unknown>) => settings.run(async () => {
    await api("POST", `/shelters/${id}/settings`, { body: { ...body, reason, version: s.version } });
    setReason(""); setCapacity(""); await refresh();
  });

  return (
    <Section title={<>{s.name} <ShelterState s={s} /></>} actions={<Freshness at={d.updatedAt} />}>
      <dl className="kv">
        <dt>سازمان</dt><dd>{orgName(s.organization_id)}</dd>
        <dt>اسکان‌یافته</dt><dd><b>{num(s.occupancy)}</b> نفر</dd>
        <dt>ظرفیت</dt><dd>{s.capacity === null ? "ثبت نشده" : `${num(s.capacity)} نفر`}</dd>
        <dt>جای خالی</dt><dd>{s.available === null ? "—" : `${num(s.available)} نفر`}</dd>
        <dt>آخرین تغییر</dt><dd>{s.updated_at ? fmtTime(s.updated_at) : "بدون ثبت"}</dd>
      </dl>
      {s.capacity !== null && <Meter value={s.occupancy} max={s.capacity} invert />}

      {can("shelter:update") && (
        <form className="actions inline-form" onSubmit={(e) => {
          e.preventDefault();
          void move.run(async () => {
            await api("POST", `/shelters/${id}/occupancy`, {
              body: { admitted: Number(admitted || 0), discharged: Number(discharged || 0), note, version: s.version } });
            setAdmitted(""); setDischarged(""); setNote(""); await refresh();
          });
        }}>
          <label>پذیرش (نفر)
            <input type="number" min={0} max={s.available ?? undefined} value={admitted} onChange={(e) => setAdmitted(e.target.value)} />
          </label>
          <label>خروج (نفر)
            <input type="number" min={0} max={s.occupancy} value={discharged} onChange={(e) => setDischarged(e.target.value)} />
          </label>
          <label>توضیح
            <input value={note} onChange={(e) => setNote(e.target.value)} maxLength={500} />
          </label>
          <button className="btn primary" disabled={move.busy || (!Number(admitted) && !Number(discharged))}>ثبت جابه‌جایی</button>
        </form>
      )}
      <ErrorBox error={move.error} />

      {can("resource:manage") && (
        <form className="actions inline-form" onSubmit={(e) => { e.preventDefault(); void saveSettings({ capacity: Number(capacity) }); }}>
          <label>ظرفیت جدید
            <input type="number" min={s.occupancy} value={capacity} onChange={(e) => setCapacity(e.target.value)} />
          </label>
          <label>دلیل (الزامی)
            <input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={1000} />
          </label>
          <button className="btn" disabled={settings.busy || !capacity || !reason.trim()}>تغییر ظرفیت</button>
          <button type="button" className={s.accepting ? "btn danger" : "btn"} disabled={settings.busy || !reason.trim()}
            onClick={() => void saveSettings({ accepting: !s.accepting })}>
            {s.accepting ? "بستن پذیرش" : "باز کردن پذیرش"}
          </button>
        </form>
      )}
      <ErrorBox error={settings.error} />

      <h3>سابقه</h3>
      {log.length === 0 && <Empty>تغییری ثبت نشده است.</Empty>}
      <ul className="timeline">
        {log.map((l, k) => (
          <li key={k}>
            {l.kind === "movement"
              ? <><b>{l.admitted > 0 && `+${num(l.admitted)} پذیرش`}{l.admitted > 0 && l.discharged > 0 && "، "}{l.discharged > 0 && `−${num(l.discharged)} خروج`}</b> ← {num(l.occupancy_after)} نفر</>
              : <><b>تغییر تنظیمات</b>: ظرفیت {l.capacity_after === null ? "—" : num(l.capacity_after)}، پذیرش {l.accepting_after ? "باز" : "بسته"}</>}
            {" "}· {l.actor_name} · {fmtTime(l.created_at)}
            {l.note && <div className="muted small">{l.note}</div>}
          </li>
        ))}
      </ul>
    </Section>
  );
}
