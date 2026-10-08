import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError } from "../lib/api";
import { ago, t } from "../lib/format";

/** Polls an async loader; keeps the last good data and exposes its age so stale data is visible. */
export function usePoll<T>(load: () => Promise<T>, intervalMs: number, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [updatedAt, setUpdatedAt] = useState<string | null>(null);
  const loadRef = useRef(load);
  loadRef.current = load;
  const reload = useCallback(async () => {
    try {
      const d = await loadRef.current();
      setData(d);
      setError(null);
      setUpdatedAt(new Date().toISOString());
    } catch (e) {
      setError(e instanceof ApiError ? e : new ApiError(0, "UNKNOWN", String(e), ""));
    }
  }, []);
  useEffect(() => {
    void reload();
    if (!intervalMs) return;
    const id = setInterval(() => void reload(), intervalMs);
    return () => clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reload, intervalMs, ...deps]);
  return { data, error, reload, updatedAt };
}

/** Runs an action, tracking busy state and the last error. */
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const run = useCallback(async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      return true;
    } catch (e) {
      setError(e instanceof ApiError ? e : new ApiError(0, "UNKNOWN", String(e), ""));
      return false;
    } finally {
      setBusy(false);
    }
  }, []);
  return { busy, error, run, setError };
}

export function ErrorBox({ error }: { error: ApiError | null }) {
  if (!error) return null;
  return (
    <div className="error" role="alert">
      <strong>{error.message}</strong>
      {error.details.length > 0 && (
        <ul>
          {error.details.map((d, i) => (
            <li key={i}>
              <code>{d.field}</code>: {d.reason}
            </li>
          ))}
        </ul>
      )}
      <small>
        کد: {error.code}
        {error.correlationId && <> · شناسه پیگیری: <code>{error.correlationId}</code></>}
      </small>
    </div>
  );
}

export function Badge({ value, kind }: { value: string | null | undefined; kind?: string }) {
  return <span className={`badge badge-${kind ?? value ?? "none"}`}>{t(value)}</span>;
}

export function Freshness({ at, label = "به‌روزرسانی" }: { at: string | null; label?: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => tick((x) => x + 1), 15000);
    return () => clearInterval(id);
  }, []);
  return <span className="muted small">{label}: {ago(at)}</span>;
}

export function Section({ title, actions, children }: { title: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      <header className="card-head">
        <h2>{title}</h2>
        <div className="row gap">{actions}</div>
      </header>
      {children}
    </section>
  );
}

/** Inline form that requires a reason before a consequential action (doc §8.1). */
export function ReasonAction({ label, onSubmit, required = true, danger, extra }: {
  label: string; required?: boolean; danger?: boolean; extra?: ReactNode;
  onSubmit: (reason: string) => Promise<unknown>;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const { busy, error, run } = useAction();
  if (!open)
    return (
      <button className={danger ? "btn danger" : "btn"} onClick={() => setOpen(true)}>
        {label}
      </button>
    );
  return (
    <form
      className="reason-form"
      onSubmit={async (e) => {
        e.preventDefault();
        if (await run(() => onSubmit(reason.trim()))) {
          setOpen(false);
          setReason("");
        }
      }}
    >
      <strong>{label}</strong>
      {extra}
      <textarea placeholder={required ? "دلیل (الزامی)" : "توضیح (اختیاری)"} value={reason} required={required}
        onChange={(e) => setReason(e.target.value)} rows={2} />
      <div className="row gap">
        <button className={danger ? "btn danger" : "btn primary"} disabled={busy || (required && !reason.trim())}>
          {busy ? "…" : "ثبت"}
        </button>
        <button type="button" className="btn ghost" onClick={() => setOpen(false)}>انصراف</button>
      </div>
      <ErrorBox error={error} />
    </form>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="muted empty">{children}</p>;
}
