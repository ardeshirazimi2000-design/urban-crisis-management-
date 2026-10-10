import { useState } from "react";
import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { fmtTime, num } from "../lib/format";
import { Badge, Empty, ErrorBox, ReasonAction, Section, useAction, usePoll } from "../components/ui";

type Content = { title: string; category: string; keywords: string[]; body: string; actions: string[]; emergency: boolean };
type Version = Content & {
  id: string; version: number; status: string; author_name: string; author_id: string | null; reviewer_name: string;
  review_reason: string | null; created_at: string; reviewed_at: string | null;
};
type Card = { id: string; slug: string; retired_at: string | null; retired_reason: string | null; versions: Version[] };

const CATEGORY: Record<string, string> = {
  earthquake: "زلزله", after_quake: "بعد از زلزله", trapped: "محبوس شدن", gas: "گاز", fire: "آتش‌سوزی", medical: "کمک‌های اولیه",
  flood: "سیل", shelter: "اسکان", alerts: "هشدار و اخبار", preparedness: "آمادگی", family: "خانواده", psychological: "حمایت روانی",
  utilities: "آب و برق", general: "عمومی",
};
const ACTION: Record<string, string> = {
  "call:110": "تماس ۱۱۰", "call:112": "تماس ۱۱۲", "call:115": "تماس ۱۱۵", "call:121": "تماس ۱۲۱", "call:122": "تماس ۱۲۲",
  "call:125": "تماس ۱۲۵", "call:194": "تماس ۱۹۴", report: "ثبت گزارش", shelters: "محل‌های اسکان", alerts: "هشدارهای رسمی",
};
const STATUS: Record<string, string> = { pending: "در انتظار تأیید", approved: "منتشرشده", rejected: "ردشده", superseded: "نسخه قبلی" };

export function GuidancePage() {
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const { can } = useSession();
  const d = usePoll(() => api<{ cards: Card[]; categories: string[]; actions: string[] }>("GET", "/guidance/admin"), 30000);
  const cards = d.data?.cards ?? [];
  const pending = cards.filter((c) => c.versions.some((v) => v.status === "pending")).length;
  const card = cards.find((c) => c.id === selected);

  return (
    <div className="split">
      <Section title="راهنمای شهروند (دستیار بحران)" actions={can("guidance:edit") && <button className="btn primary small" onClick={() => { setCreating(true); setSelected(null); }}>کارت جدید</button>}>
        <p className="muted small">اپ شهروند فقط متن «منتشرشده» را نشان می‌دهد. هر متن را یک نفر می‌نویسد و نفر دیگری (فرمانده) تأیید می‌کند. سؤال شهروندان روی گوشی خودشان تطبیق داده می‌شود و به سرور نمی‌آید.</p>
        {pending > 0 && <p className="warn">{num(pending)} کارت نسخه در انتظار تأیید دارد.</p>}
        <ErrorBox error={d.error} />
        {d.data && cards.length === 0 && <Empty>کارتی وجود ندارد.</Empty>}
        <table className="table">
          <thead><tr><th>عنوان</th><th>دسته</th><th>وضعیت</th><th>نسخه</th></tr></thead>
          <tbody>
            {cards.map((c) => {
              const pub = c.versions.find((v) => v.status === "approved");
              const pen = c.versions.find((v) => v.status === "pending");
              const latest = pub ?? c.versions[0];
              return (
                <tr key={c.id} className={(selected === c.id ? "sel " : "") + (c.retired_at ? "muted" : "")} onClick={() => { setSelected(c.id); setCreating(false); }}>
                  <td>{latest.emergency && <span className="badge badge-critical">اضطراری</span>} {latest.title}</td>
                  <td className="small">{CATEGORY[latest.category] ?? latest.category}</td>
                  <td>{c.retired_at ? <span className="badge">بازنشسته</span> : pub ? <span className="badge badge-sent">منتشرشده</span> : <span className="badge">منتشرنشده</span>}
                    {pen && <span className="badge badge-pending_approval"> در انتظار تأیید</span>}</td>
                  <td>{pub ? num(pub.version) : "—"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Section>
      {creating ? <CardEditor onDone={async () => { setCreating(false); await d.reload(); }} onCancel={() => setCreating(false)} />
        : card ? <CardPanel card={card} key={card.id + card.versions.length} onChanged={d.reload} />
        : <Section title="جزئیات"><Empty>یک کارت را انتخاب کنید.</Empty></Section>}
    </div>
  );
}

function ContentView({ c }: { c: Content }) {
  return (
    <div className="guidance-preview">
      <strong>{c.title}</strong>
      <div className="pre">{c.body}</div>
      <div className="muted small">کلیدواژه‌ها: {c.keywords.join("، ")}</div>
      <div className="small">دکمه‌ها: {c.actions.map((a) => ACTION[a] ?? a).join("، ") || "—"}</div>
    </div>
  );
}

function CardPanel({ card, onChanged }: { card: Card; onChanged: () => void }) {
  const { can, me } = useSession();
  const [editing, setEditing] = useState(false);
  const pub = card.versions.find((v) => v.status === "approved");
  const pen = card.versions.find((v) => v.status === "pending");
  const base = pen ?? pub ?? card.versions[0];
  const mine = pen && pen.author_id === me?.user_id;

  if (editing) return <CardEditor cardId={card.id} initial={base} onDone={async () => { setEditing(false); await onChanged(); }} onCancel={() => setEditing(false)} />;
  return (
    <Section title={<>{base.title} <span className="muted small" dir="ltr">{card.slug}</span></>}>
      {card.retired_at && <p className="warn">بازنشسته از {fmtTime(card.retired_at)}: {card.retired_reason}</p>}
      <h3>متن منتشرشده {pub && `(نسخه ${num(pub.version)})`}</h3>
      {pub ? <ContentView c={pub} /> : <Empty>هنوز منتشر نشده است.</Empty>}
      {pen && (
        <>
          <h3>نسخه {num(pen.version)} در انتظار تأیید <span className="muted small">— نویسنده: {pen.author_name} · {fmtTime(pen.created_at)}</span></h3>
          <ContentView c={pen} />
          {can("guidance:approve") && (
            <div className="actions">
              {mine ? <span className="muted small">تأیید نسخه‌ای که خودتان نوشته‌اید ممکن نیست؛ فرمانده دیگری باید تأیید کند.</span> : (
                <ReasonAction label="تأیید و انتشار" required={false} onSubmit={async (reason) => { await api("POST", `/guidance/versions/${pen.id}/approve`, { body: { reason } }); await onChanged(); }} />
              )}
              <ReasonAction label="رد" danger onSubmit={async (reason) => { await api("POST", `/guidance/versions/${pen.id}/reject`, { body: { reason } }); await onChanged(); }} />
            </div>
          )}
        </>
      )}
      {!card.retired_at && (
        <div className="actions">
          {can("guidance:edit") && !pen && <button className="btn" onClick={() => setEditing(true)}>پیشنهاد اصلاح (نسخه جدید)</button>}
          {can("guidance:approve") && pub && <ReasonAction label="بازنشسته کردن" danger onSubmit={async (reason) => { await api("POST", `/guidance/cards/${card.id}/retire`, { body: { reason } }); await onChanged(); }} />}
        </div>
      )}
      <h3>سابقه نسخه‌ها</h3>
      <ul className="timeline">
        {card.versions.map((v) => (
          <li key={v.id}><b>نسخه {num(v.version)}</b> <Badge value={v.status} kind={v.status === "approved" ? "sent" : v.status === "pending" ? "pending_approval" : "none"} />{" "}
            {STATUS[v.status]} · نویسنده {v.author_name}{v.reviewer_name && ` · بررسی: ${v.reviewer_name}`}{v.reviewed_at && ` · ${fmtTime(v.reviewed_at)}`}
            {v.review_reason && <div className="muted small">{v.review_reason}</div>}</li>
        ))}
      </ul>
    </Section>
  );
}

function CardEditor({ cardId, initial, onDone, onCancel }: { cardId?: string; initial?: Content; onDone: () => void; onCancel: () => void }) {
  const [slug, setSlug] = useState("");
  const [f, setF] = useState({
    title: initial?.title ?? "", category: initial?.category ?? "general", keywords: (initial?.keywords ?? []).join("، "),
    body: initial?.body ?? "", actions: initial?.actions ?? [] as string[], emergency: initial?.emergency ?? false,
  });
  const act = useAction();
  return (
    <Section title={cardId ? "پیشنهاد نسخه جدید" : "کارت راهنمای جدید"} actions={<button className="btn ghost small" onClick={onCancel}>بستن</button>}>
      <form onSubmit={(e) => {
        e.preventDefault();
        void act.run(async () => {
          const body = { ...f, keywords: f.keywords.split(/[،,\n]/).map((k) => k.trim()).filter(Boolean) };
          if (cardId) await api("POST", `/guidance/cards/${cardId}/versions`, { body });
          else await api("POST", "/guidance/cards", { body: { ...body, slug } });
          onDone();
        });
      }}>
        {!cardId && <label>شناسه (لاتین، مثل night-aftershock)<input dir="ltr" value={slug} onChange={(e) => setSlug(e.target.value)} required pattern="[a-z0-9-]{2,60}" /></label>}
        <label>عنوان (به شکل سؤال یا موقعیت شهروند)<input value={f.title} onChange={(e) => setF({ ...f, title: e.target.value })} required maxLength={120} /></label>
        <div className="row gap wrap">
          <label>دسته<select value={f.category} onChange={(e) => setF({ ...f, category: e.target.value })}>
            {Object.entries(CATEGORY).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></label>
          <label className="chk"><input type="checkbox" checked={f.emergency} onChange={(e) => setF({ ...f, emergency: e.target.checked })} /> وضعیت اضطراری (اول و با رنگ قرمز نمایش داده می‌شود)</label>
        </div>
        <label>کلیدواژه‌ها (با ویرگول جدا کنید؛ شکل محاوره‌ای هم بنویسید، مثل «بوی گاز، گاز میاد»)
          <textarea rows={2} value={f.keywords} onChange={(e) => setF({ ...f, keywords: e.target.value })} required /></label>
        <label>متن راهنما (کوتاه، مرحله‌به‌مرحله)<textarea rows={8} value={f.body} onChange={(e) => setF({ ...f, body: e.target.value })} required maxLength={3000} /></label>
        <fieldset className="chk-group"><legend>دکمه‌های زیر پاسخ</legend>
          <div className="chk-grid">
            {Object.entries(ACTION).map(([k, v]) => (
              <label key={k} className="chk"><input type="checkbox" checked={f.actions.includes(k)}
                onChange={(e) => setF({ ...f, actions: e.target.checked ? [...f.actions, k] : f.actions.filter((x) => x !== k) })} /> {v}</label>
            ))}
          </div>
        </fieldset>
        <button className="btn primary" disabled={act.busy}>ارسال برای تأیید</button>
        <span className="muted small"> متن تا تأیید فرد دیگر به شهروندان نمی‌رسد.</span>
      </form>
      <ErrorBox error={act.error} />
    </Section>
  );
}
