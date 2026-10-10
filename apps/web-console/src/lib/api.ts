// Thin API client: bearer token, correlation IDs, Idempotency-Key for retryable POSTs,
// and the platform's fixed error envelope surfaced as ApiError.

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public correlationId: string,
    public details: { field: string; reason: string }[] = [],
  ) {
    super(message);
  }
}

const BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? "/api/v1";
let token: string | null = sessionStorageGet("crisis.token");

function sessionStorageGet(k: string): string | null {
  try {
    return sessionStorage.getItem(k);
  } catch {
    return null;
  }
}

export function setToken(t: string | null) {
  token = t;
  try {
    if (t) sessionStorage.setItem("crisis.token", t);
    else sessionStorage.removeItem("crisis.token");
  } catch {
    /* storage unavailable: keep in memory only */
  }
}

export const hasToken = () => token !== null;

/**
 * RFC 4122 v4 UUID. crypto.randomUUID exists only in secure contexts (HTTPS/localhost); the console is also
 * served over plain HTTP on office LANs, so fall back to crypto.getRandomValues, which works everywhere.
 */
export function uuid(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

export function newKey(): string {
  return "web-" + uuid();
}

type Opts = { body?: unknown; idempotent?: boolean | string; query?: Record<string, string | number | undefined> };

export async function api<T>(method: string, path: string, opts: Opts = {}): Promise<T> {
  const headers: Record<string, string> = { "X-Correlation-ID": uuid() };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (opts.idempotent) headers["Idempotency-Key"] = typeof opts.idempotent === "string" ? opts.idempotent : newKey();
  let url = BASE + path;
  if (opts.query) {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(opts.query)) if (v !== undefined && v !== "") q.set(k, String(v));
    const s = q.toString();
    if (s) url += "?" + s;
  }
  let res: Response;
  try {
    res = await fetch(url, { method, headers, body: opts.body === undefined ? undefined : JSON.stringify(opts.body) });
  } catch {
    throw new ApiError(0, "NETWORK", "ارتباط با سرور برقرار نیست", headers["X-Correlation-ID"]);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const e = data?.error ?? {};
    if (res.status === 401) setToken(null);
    throw new ApiError(res.status, e.code ?? "HTTP_" + res.status, e.message ?? res.statusText,
      e.correlation_id ?? res.headers.get("X-Correlation-ID") ?? "", e.details ?? []);
  }
  return data as T;
}

// ---------------------------------------------------------------------------
// Types (mirror contracts/openapi/core-api.v1.yaml)
// ---------------------------------------------------------------------------

export type Grant = { role: string; scope_org_id?: string | null };
export type Me = { user_id: string; subject: string; display_name: string; grants: Grant[]; roles: string[]; permissions: string[] };
export type Org = { id: string; code: string; name: string };
export type Loc = { lat: number; lng: number; accuracy_m: number; source?: string; precision?: string };

export type Report = {
  id: string; type: string; description: string; severity: string | null; status: string; source: string;
  occurred_at: string | null; received_at: string; location: Loc; enrichment_status: string;
  duplicate_of?: string; reviewer_id?: string; review_reason?: string; reviewed_at?: string; media_count: number; version: number;
};
export type AIScore = { model_version: string; predicted_type: string | null; type_confidence: number | null; urgency_signal: number | null; signals: Record<string, unknown>; created_at: string; note: string };
export type CallerContact = {
  caller_name: string | null; caller_phone: string | null; callback_requested: boolean;
  address_text: string | null; recorded_by: string; created_at: string;
};
export type ReportDetail = Report & {
  contact?: CallerContact;
  media: { media_id: string; content_type: string; bytes: number; scan_status: string }[];
  ai_scores: AIScore[];
  history: { event_type: string; actor_id: string | null; payload: Record<string, unknown>; created_at: string }[];
  incidents: { id: string; code: string; status: string; relation_type: string }[];
};

export type Incident = {
  id: string; code: string; type: string; title: string; severity: string; response_level: string; status: string;
  owner_org_id: string; location: { lat: number; lng: number } | null; opened_at: string; updated_at: string;
  closed_at: string | null; version: number; report_count: number; active_assignments: number; allowed_transitions: string[];
};
export type IncidentDetail = {
  incident: Incident;
  timeline: { event_type: string; actor_id: string | null; payload: Record<string, unknown>; created_at: string }[];
  reports: { report_id: string; type: string; status: string; relation_type: string; reason: string; linked_at: string }[];
  assignments: { id: string; resource_id: string; resource_name: string; resource_type: string; status: string; assigned_at: string; version: number }[];
  alerts: { id: string; status: string; mode: string; severity: string; region_label: string; expires_at: string }[];
};

export type Resource = {
  id: string; organization_id: string; type: string; name: string; status: string; capabilities: Record<string, unknown>;
  capacity: number | null; location: { lat: number; lng: number } | null; last_seen_at: string | null; stale: boolean; version: number;
};

export type Alert = {
  id: string; incident_id: string | null; mode: string; severity: string; status: string; issuer_org_id: string; issuer_name: string;
  region: unknown; region_label: string; region_area_km2: number; template_code: string; template_version: number;
  params: Record<string, string>; rendered_text: string; channels: string[]; issued_at: string | null; expires_at: string;
  created_by: string; submitted_by: string | null; approved_by: string | null; approval_reason: string | null; version: number;
  cancel_reason: string | null;
};
export type Template = { code: string; version: number; title: string; body: string; params: string[] | null };
export type Delivery = {
  alert_id: string; alert_status: string; note: string;
  channels: { channel: string; status: string; attempts: { attempt_no: number; status: string; result_code: string | null; detail: string | null; attempted_at: string | null }[] }[];
};

export type Feature = { type: "Feature"; id: string; geometry: { type: string; coordinates: unknown }; properties: Record<string, unknown> };
export type FeatureCollection = {
  type: "FeatureCollection"; features: Feature[];
  meta: { generated_at: string; layers: Record<string, { count: number; stale_count: number; note?: string }>; stale_after_seconds: number };
};

export type Need = {
  id: string; incident_id: string; incident_code: string; incident_title: string; owner_org_id: string;
  category: string; description: string; quantity: number; fulfilled: number; remaining: number; unit: string;
  priority: string; status: string; location: { lat: number; lng: number } | null; requested_by: string;
  requested_by_name: string; cancel_reason: string | null; created_at: string; updated_at: string; closed_at: string | null;
  version: number;
};
export type Fulfillment = {
  id: string; quantity: number; source: string; resource_id: string | null; resource_name: string | null; note: string;
  actor_id: string; actor_name: string; created_at: string;
};
export type Shelter = {
  id: string; organization_id: string; name: string; resource_status: string; location: { lat: number; lng: number } | null;
  capacity: number | null; occupancy: number; available: number | null; accepting: boolean; open: boolean; full: boolean;
  updated_at: string | null; version: number;
};
export type ShelterLog = {
  kind: string; admitted: number; discharged: number; occupancy_after: number; capacity_after: number | null;
  accepting_after: boolean; note: string; actor_name: string; created_at: string;
};
