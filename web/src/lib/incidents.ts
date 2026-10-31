import { api, rangeQuery } from "./api";

/** Declared incidents (Incidents page), on top of alerts, uptime, errors and logs. */

export type Severity = "sev1" | "sev2" | "sev3" | "sev4";
export type IncidentStatus = "investigating" | "identified" | "monitoring" | "resolved";
export type RoleName = "commander" | "communications" | "operations" | "scribe" | "responder";
export type LinkKind = "alert" | "uptime" | "error" | "log" | "request" | "trace" | "chart" | "url";

export const SEVERITIES: { key: Severity; label: string; hint: string }[] = [
  { key: "sev1", label: "SEV1", hint: "Critical: the product is down or data is at risk for many users" },
  { key: "sev2", label: "SEV2", hint: "Major: a core feature is broken or badly degraded" },
  { key: "sev3", label: "SEV3", hint: "Minor: part of a feature is affected, with a workaround" },
  { key: "sev4", label: "SEV4", hint: "Low: little user impact, worth tracking" },
];

export const STATUSES: { key: IncidentStatus; label: string }[] = [
  { key: "investigating", label: "Investigating" },
  { key: "identified", label: "Identified" },
  { key: "monitoring", label: "Monitoring" },
  { key: "resolved", label: "Resolved" },
];

export const ROLES: { key: RoleName; label: string }[] = [
  { key: "commander", label: "Incident commander" },
  { key: "communications", label: "Communications lead" },
  { key: "operations", label: "Operations lead" },
  { key: "scribe", label: "Scribe" },
  { key: "responder", label: "Responder" },
];

export const LINK_KINDS: { key: LinkKind; label: string; placeholder: string }[] = [
  { key: "alert", label: "Alert rule", placeholder: "Rule id, e.g. 12" },
  { key: "uptime", label: "Uptime check", placeholder: "Check id, e.g. 3" },
  { key: "error", label: "Error issue", placeholder: "Fingerprint" },
  { key: "log", label: "Log line", placeholder: "Line id" },
  { key: "request", label: "Request", placeholder: "Request id" },
  { key: "trace", label: "Trace", placeholder: "Trace id" },
  { key: "chart", label: "Chart or dashboard", placeholder: "#/dashboards/4 or a URL" },
  { key: "url", label: "Web page", placeholder: "https://…" },
];

export const statusLabel = (s: string) => STATUSES.find((x) => x.key === s)?.label ?? s;
export const roleLabel = (r: string) => ROLES.find((x) => x.key === r)?.label ?? r;
export const sevLabel = (s: string) => s.toUpperCase();

export interface IncidentRole {
  id: number;
  role: RoleName;
  user: string;
  assigned_by: string;
  assigned_at: string;
}

export interface Incident {
  id: number;
  title: string;
  severity: Severity;
  status: IncidentStatus;
  services: string[];
  impact: string;
  source_kind?: string;
  source_ref?: string;
  on_status_page: boolean;
  created_by: string;
  started_at: string;
  declared_at: string;
  acknowledged_at?: string;
  identified_at?: string;
  mitigated_at?: string;
  resolved_at?: string;
  updated_at: string;
  commander: string;
  roles: IncidentRole[];
  tta_s: number | null;
  ttm_s: number | null;
  ttr_s: number | null;
}

export interface Delivery {
  channel_name: string;
  channel_kind: string;
  status: "pending" | "sent" | "failed";
  attempts: number;
  error?: string;
}

export interface TimelineEntry {
  id?: number;
  at: string;
  kind: string;
  actor?: string;
  body: string;
  auto: boolean;
  detail?: { link?: string; channels?: string[]; public?: boolean; status?: string; note?: string; delivery?: Delivery[]; kind?: string; ref?: string; [k: string]: unknown };
}

export interface IncidentLink {
  id: number;
  kind: LinkKind;
  ref: string;
  label: string;
  created_by: string;
  created_at: string;
}

export interface ActionItem {
  id: number;
  body: string;
  owner: string;
  done: boolean;
  created_at: string;
  done_at?: string;
}

export interface Postmortem {
  summary: string;
  impact: string;
  root_cause: string;
  timeline: string;
  lessons: string;
  drafted?: "ai" | "data";
  updated_by?: string;
  updated_at?: string;
}

export interface IncidentDetail {
  incident: Incident;
  links: IncidentLink[];
  timeline: TimelineEntry[];
  actions: ActionItem[];
  postmortem: Postmortem | null;
  can_edit: boolean;
  can_admin: boolean;
  llm: boolean;
  status_page: boolean;
}

export interface IncidentStats {
  total: number;
  active: number;
  /** Not yet resolved, whenever declared; unassigned of those have no commander. */
  open: number;
  unassigned: number;
  by_severity: Partial<Record<Severity, number>>;
  step_s: number;
  buckets: { at: string; counts: Partial<Record<Severity, number>> }[];
  mtta_s: number | null;
  mttm_s: number | null;
  mttr_s: number | null;
}

export interface IncidentSource {
  kind: string;
  ref: string;
  label: string;
  title: string;
  severity: Severity;
  services: string[];
  impact: string;
  started_at?: string;
}

export interface DeclareInput {
  title: string;
  severity: Severity;
  services: string[];
  impact: string;
  started_at?: string;
  source_kind?: string;
  source_ref?: string;
  commander?: string;
  on_status_page?: boolean;
  channels?: number[];
}

const base = (id: number) => `/api/incidents/${id}`;

export const incidentsApi = {
  list: (filters: Record<string, string>, signal?: AbortSignal) =>
    api<{ incidents: Incident[]; can_edit: boolean }>(`/api/incidents?${new URLSearchParams(Object.entries(filters).filter(([, v]) => v !== ""))}`, { signal }),
  stats: (from: Date, to: Date, signal?: AbortSignal) => api<IncidentStats>(`/api/incidents/stats?${rangeQuery(from, to)}`, { signal }),
  people: (signal?: AbortSignal) => api<{ people: string[]; me: string }>("/api/incidents/people", { signal }),
  source: (kind: string, ref: string, signal?: AbortSignal) => api<IncidentSource>(`/api/incidents/source?${new URLSearchParams({ kind, ref })}`, { signal }),
  declare: (d: DeclareInput) => api<Incident>("/api/incidents", { method: "POST", json: d }),
  get: (id: number, signal?: AbortSignal) => api<IncidentDetail>(base(id), { signal }),
  edit: (id: number, change: Partial<Pick<Incident, "title" | "severity" | "services" | "impact" | "started_at" | "on_status_page">>) =>
    api<Incident>(base(id), { method: "PUT", json: change }),
  remove: (id: number) => api<void>(base(id), { method: "DELETE" }),
  setStatus: (id: number, status: IncidentStatus, note = "") => api<Incident>(`${base(id)}/status`, { method: "POST", json: { status, note } }),
  assign: (id: number, role: RoleName, user: string) => api<Incident>(`${base(id)}/roles`, { method: "POST", json: { role, user } }),
  unassign: (id: number, roleID: number) => api<Incident>(`${base(id)}/roles/${roleID}`, { method: "DELETE" }),
  note: (id: number, body: string, link: string) => api<void>(`${base(id)}/notes`, { method: "POST", json: { body, link } }),
  link: (id: number, kind: LinkKind, ref: string, label: string) => api<{ links: IncidentLink[] }>(`${base(id)}/links`, { method: "POST", json: { kind, ref, label } }),
  unlink: (id: number, linkID: number) => api<void>(`${base(id)}/links/${linkID}`, { method: "DELETE" }),
  update: (id: number, u: { body: string; status: IncidentStatus | ""; channels: number[]; status_page: boolean }) =>
    api<{ incident: Incident; channels: string[] }>(`${base(id)}/updates`, { method: "POST", json: u }),
  draft: (id: number) => api<{ postmortem: Postmortem; actions: ActionItem[]; note: string }>(`${base(id)}/postmortem/draft`, { method: "POST", json: {} }),
  savePostmortem: (id: number, p: Postmortem) => api<Postmortem>(`${base(id)}/postmortem`, { method: "PUT", json: p }),
  markdownURL: (id: number) => `${base(id)}/postmortem.md`,
  addAction: (id: number, body: string, owner: string) => api<{ actions: ActionItem[] }>(`${base(id)}/actions`, { method: "POST", json: { body, owner } }),
  editAction: (id: number, actionID: number, change: { body?: string; owner?: string; done?: boolean }) =>
    api<{ actions: ActionItem[] }>(`${base(id)}/actions/${actionID}`, { method: "PUT", json: change }),
  removeAction: (id: number, actionID: number) => api<{ actions: ActionItem[] }>(`${base(id)}/actions/${actionID}`, { method: "DELETE" }),
};

/** A link to the declare form, filled from a source. */
export function declareHref(kind: "alert" | "uptime" | "error" | "log", ref: string | number): string {
  return `#/incidents?${new URLSearchParams({ declare: kind, ref: String(ref) })}`;
}

export const incidentHref = (id: number) => `#/incidents?id=${id}`;

/** Where a linked item lives in the dashboard, or its own address. */
export function linkHref(l: Pick<IncidentLink, "kind" | "ref">): string {
  const [id] = l.ref.split("|");
  switch (l.kind) {
    case "alert":
      return `#/alerts?rule=${encodeURIComponent(id)}`;
    case "uptime":
      return `#/uptime?check=${encodeURIComponent(id)}`;
    case "error":
      return `#/errors?fp=${encodeURIComponent(l.ref)}`;
    case "log":
      return `#/applogs?line=${encodeURIComponent(l.ref)}`;
    case "request":
      return `#/applogs?q=${encodeURIComponent(`request_id:${l.ref}`)}`;
    case "trace":
      return `#/applogs?q=${encodeURIComponent(`trace_id:${l.ref}`)}`;
  }
  return l.ref;
}

/** "4m", "1h 12m", "2d 3h"; a dash until reached. */
export function formatSpan(s: number | null | undefined): string {
  if (s === null || s === undefined) return "—";
  if (s < 60) return `${Math.round(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}
