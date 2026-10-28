import { api, rangeQuery, type Result } from "./api";

/** Severities as stored: 1 trace … 6 fatal. */
export const LEVELS = ["trace", "debug", "info", "warn", "error", "fatal"] as const;
export type Level = (typeof LEVELS)[number];

export const LEVEL_LABEL: Record<Level, string> = { trace: "Trace", debug: "Debug", info: "Info", warn: "Warn", error: "Error", fatal: "Fatal" };

/** Level colors, from the theme's status tokens. */
export const LEVEL_COLOR: Record<Level, string> = {
  trace: "var(--text-3)",
  debug: "var(--series-6)",
  info: "var(--series-1)",
  warn: "var(--warning)",
  error: "var(--critical)",
  fatal: "var(--critical)",
};

export function asLevel(v: unknown): Level {
  const s = String(v ?? "").toLowerCase();
  return (LEVELS as readonly string[]).includes(s) ? (s as Level) : "info";
}

/** A list row: the columns of logListColumns on the server, as an object. */
export interface LogLine {
  id: number;
  created_at: string;
  level: Level;
  severity: number;
  service: string | null;
  source: string;
  host: string | null;
  container: string | null;
  logger: string | null;
  message: string;
  attrs: Record<string, unknown>;
  request_id: string | null;
  trace_id: string | null;
  user_id: string | null;
  code_file: string | null;
  code_line: number | null;
  pattern: string | null;
}

/** Rows of a Result as lines, attrs parsed. */
export function toLines(res: Result | undefined): LogLine[] {
  if (!res) return [];
  return res.rows.map((row) => {
    const o: Record<string, unknown> = {};
    res.columns.forEach((c, i) => (o[c] = row[i]));
    let attrs: Record<string, unknown> = {};
    if (typeof o.attrs === "string" && o.attrs) {
      try {
        attrs = JSON.parse(o.attrs);
      } catch {
        attrs = {};
      }
    }
    return { ...(o as unknown as LogLine), id: Number(o.id), level: asLevel(o.level), attrs };
  });
}

export interface LogResult extends Result {
  terms: string[];
  masked?: string[];
}

export interface FieldCount {
  name: string;
  count: number;
  column: boolean;
}

export interface FieldValue {
  value: unknown;
  count: number;
}

export interface LogView {
  id: number;
  name: string;
  query: string;
  columns: string[];
  created_by: string;
  created_at: string;
}

export interface Pattern {
  id: string;
  count: number;
  level: Level;
  bytes: number;
  sample_id: number;
  trend: number[];
  service: string | null;
  template: string;
  first_seen: string;
  first_version: string | null;
  code_file: string | null;
  code_line: number | null;
  new: boolean;
  viewed_at: string | null;
}

export interface PatternsResponse {
  patterns: Pattern[];
  total: number;
  pending: number;
  bucket: number;
  from: number;
}

export interface VolumeResponse {
  lines: number;
  bytes: number;
  services: Result;
  sources: Result;
  levels: Result;
  code: Result;
  unread: { id: string; service: string | null; template: string; level: string; code_file: string | null; code_line: number | null; lines: number; bytes: number; share: number }[];
}

export interface HiddenFailure {
  pattern: string | null;
  template: string;
  method: string;
  route: string | null;
  lines: number;
  requests: number;
  sample_id: number;
  sample_request: string;
  last: string;
  status: number;
}

export interface CompareChange {
  id: string;
  a: number;
  b: number;
  share_a: number;
  share_b: number;
  level: Level;
  kind: "new" | "gone" | "more" | "less";
  template: string;
}

export interface CompareResponse {
  a: string;
  b: string;
  lines_a: number;
  lines_b: number;
  changes: CompareChange[];
}

const enc = encodeURIComponent;

export const logsApi = {
  list: (from: Date, to: Date, q: string, extra: Record<string, string> = {}, signal?: AbortSignal) =>
    api<LogResult>(`/api/applogs?${rangeQuery(from, to, { q, ...extra })}`, { signal }),
  histogram: (from: Date, to: Date, q: string, signal?: AbortSignal) => api<Result>(`/api/applogs/histogram?${rangeQuery(from, to, { q })}`, { signal }),
  fields: (from: Date, to: Date, q: string, signal?: AbortSignal) =>
    api<{ fields: FieldCount[]; sampled: number }>(`/api/applogs/fields?${rangeQuery(from, to, { q })}`, { signal }),
  values: (from: Date, to: Date, q: string, field: string) =>
    api<{ field: string; values: FieldValue[]; total: number }>(`/api/applogs/fields?${rangeQuery(from, to, { q, field })}`),
  line: (id: number, signal?: AbortSignal) =>
    api<{ line: Record<string, unknown>; pattern?: { id: string; template: string; first_seen: string; last_seen: string; first_version: string | null; lines: number } }>(
      `/api/applogs/${id}`,
      { signal },
    ),
  context: (id: number, n = 25) => api<{ result: Result; by: string; value: unknown; id: number }>(`/api/applogs/${id}/context?n=${n}`),
  around: (params: Record<string, string>, signal?: AbortSignal) => api<Result>(`/api/applogs/around?${new URLSearchParams(params)}`, { signal }),
  views: () => api<{ views: LogView[] }>("/api/applogs/views"),
  saveView: (v: { name: string; query: string; columns: string[] }) => api<LogView>("/api/applogs/views", { method: "POST", json: v }),
  deleteView: (id: number) => api<void>(`/api/applogs/views/${id}`, { method: "DELETE" }),
  patterns: (from: Date, to: Date, q: string, signal?: AbortSignal) => api<PatternsResponse>(`/api/applogs/patterns?${rangeQuery(from, to, { q })}`, { signal }),
  volume: (from: Date, to: Date, q: string, signal?: AbortSignal) => api<VolumeResponse>(`/api/applogs/volume?${rangeQuery(from, to, { q })}`, { signal }),
  hidden: (from: Date, to: Date, q: string, signal?: AbortSignal) =>
    api<{ failures: HiddenFailure[] }>(`/api/applogs/hidden-failures?${rangeQuery(from, to, { q })}`, { signal }),
  compare: (from: Date, to: Date, a: string, b: string, service: string) =>
    api<CompareResponse>(`/api/applogs/compare?${rangeQuery(from, to, { a, b, service })}`),
  ask: (question: string) => api<{ query: string; since: string; note?: string }>("/api/applogs/ask", { method: "POST", json: { question } }),
  explain: (from: Date, to: Date, q: string) =>
    api<{ summary: string; lines: number[] }>("/api/applogs/explain", { method: "POST", json: { q, from: from.toISOString(), to: to.toISOString() } }),
  exportUrl: (from: Date, to: Date, q: string, format: "csv" | "ndjson") => `/api/applogs/export?${rangeQuery(from, to, { q, format })}`,
  tailUrl: (q: string, after?: number) => `/api/applogs/tail?${new URLSearchParams({ q, ...(after ? { after: String(after) } : {}) })}`,
};

/** Quotes a value for the search when it needs it. */
export function quoteValue(v: unknown): string {
  const s = String(v ?? "");
  return /^[A-Za-z0-9_.\-/@:]+$/.test(s) && !s.includes(":") ? s : `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

/** A field:value term, or -field:value to exclude it. */
export function fieldTerm(field: string, value: unknown, exclude = false): string {
  return `${exclude ? "-" : ""}${field}:${quoteValue(value)}`;
}

/** Adds a term to a search unless it is already there; replaces its opposite. */
export function addTerm(q: string, term: string): string {
  const opposite = term.startsWith("-") ? term.slice(1) : `-${term}`;
  const parts = q.trim() ? q.trim().split(/\s+(?=(?:[^"]*"[^"]*")*[^"]*$)/) : [];
  const kept = parts.filter((p) => p !== opposite);
  if (kept.includes(term)) return kept.join(" ");
  return [...kept, term].join(" ");
}

/** A link to the Logs page with a search, and optionally a range. */
export function logsHref(q: string, range?: { from: Date | string; to: Date | string }): string {
  const p = new URLSearchParams();
  if (q) p.set("q", q);
  if (range) {
    p.set("from", new Date(range.from).toISOString());
    p.set("to", new Date(range.to).toISOString());
  }
  const qs = p.toString();
  return `#/applogs${qs ? `?${qs}` : ""}`;
}

/** The longest literal run of a template, to match its lines by text. */
export function templateLiteral(template: string): string {
  return template
    .split("<*>")
    .map((s) => s.trim())
    .reduce((a, b) => (b.length > a.length ? b : a), "");
}

// Settings → Logs.

export interface LogOverride {
  service: string;
  route: string;
  user: string;
  level: string;
  until: string;
}

export interface LogDrop {
  service: string;
  logger: string;
  contains: string;
  level: string;
}

export interface ParseRule {
  name: string;
  service: string;
  source: string;
  pattern: string;
}

export interface LogSettings {
  enabled: boolean;
  level: string;
  buffer: boolean;
  buffer_level: string;
  slow_ms: number;
  rate: number;
  overrides: LogOverride[];
  drops: LogDrop[];
  server: {
    rate: number;
    redact: string[];
    rules: ParseRule[];
    syslog: string;
    syslog_service: string;
    access: { hidden_services: string[]; masked_fields: string[] };
  };
}

export interface LogSettingsResponse {
  settings: LogSettings;
  expired: boolean[];
  syslog: { listening: string; error: string };
  can_admin: boolean;
  can_edit: boolean;
}

export const logSettingsApi = {
  get: () => api<LogSettingsResponse>("/api/settings/logs"),
  put: (s: LogSettings) => api<LogSettingsResponse>("/api/settings/logs", { method: "PUT", json: s }),
  addOverride: (o: Partial<LogOverride> & { level: string; minutes: number }) =>
    api<LogSettingsResponse>("/api/settings/logs/overrides", { method: "POST", json: o }),
  deleteOverride: (i: number) => api<LogSettingsResponse>(`/api/settings/logs/overrides/${i}`, { method: "DELETE" }),
  addDrop: (d: Partial<LogDrop>) => api<LogSettingsResponse>("/api/settings/logs/drops", { method: "POST", json: d }),
  deleteDrop: (i: number) => api<LogSettingsResponse>(`/api/settings/logs/drops/${i}`, { method: "DELETE" }),
  testRule: (pattern: string, sample: string) =>
    api<{ matched: boolean; fields: Record<string, unknown>; message: string; level?: string; time?: string }>("/api/settings/logs/rules/test", {
      method: "POST",
      json: { pattern, sample },
    }),
};

export interface ArchiveInfo {
  configured: boolean;
  bucket: string;
  endpoint: string;
  prefix: string;
  last_run?: string;
  last_error: string;
  hours: Result | [];
}

export const archiveApi = {
  get: () => api<ArchiveInfo>("/api/applogs/archives"),
  rehydrate: (from: Date, to: Date) =>
    api<{ lines: number; hours: { hour: string; lines: number; skipped?: string }[] }>("/api/applogs/archives/rehydrate", {
      method: "POST",
      json: { from: from.toISOString().replace(/\.\d{3}Z$/, "Z"), to: to.toISOString().replace(/\.\d{3}Z$/, "Z") },
    }),
};

/** Creates an alert rule from a search: "alert me when this happens again". */
export function alertFromSearch(name: string, query: string, service?: string) {
  return api<{ id: number }>("/api/alerts/rules", {
    method: "POST",
    json: {
      name,
      kind: "threshold",
      severity: "warning",
      channels: [],
      enabled: true,
      spec: { metric: "log_matches", query, op: ">", threshold: 0, window: "5m", for: "0m", scope: service ? { service } : {} },
    },
  });
}

export function encodeQ(q: string): string {
  return enc(q);
}
