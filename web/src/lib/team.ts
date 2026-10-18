import { useEffect, useState } from "react";
import { api, type SessionInfo, type Viz } from "./api";
import type { RangeKey } from "./time";

export type Role = "viewer" | "editor" | "admin";
export type Permission = "view" | "edit" | "admin";

export interface TeamSession extends SessionInfo {
  role: Role;
  permissions: Record<Permission, boolean>;
}

export const ROLE_LABEL: Record<Role, string> = { viewer: "Viewer", editor: "Editor", admin: "Admin" };

export const ROLE_INFO: Record<Role, string> = {
  viewer: "Reads dashboards, runs the analyst and read-only queries.",
  editor: "Also creates and edits saved panels, dashboards, alerts and checks.",
  admin: "Also manages settings: API keys, role mapping, the audit log and notification channels.",
};

// One request per page load; every view shares it. Sign-in and sign-out reload
// the session through refreshSession.
let sessionPromise: Promise<TeamSession> | null = null;
const listeners = new Set<(s: TeamSession | null) => void>();

function loadSession(): Promise<TeamSession> {
  if (!sessionPromise) {
    sessionPromise = api<TeamSession>("/api/session");
    sessionPromise.catch(() => {
      sessionPromise = null;
    });
  }
  return sessionPromise;
}

export function refreshSession() {
  sessionPromise = null;
  loadSession()
    .then((s) => listeners.forEach((l) => l(s)))
    .catch(() => listeners.forEach((l) => l(null)));
}

/** The signed-in user's role. Until it loads, nothing is allowed. */
export function useSession(): { session: TeamSession | null; can: (p: Permission) => boolean } {
  const [session, setSession] = useState<TeamSession | null>(null);
  useEffect(() => {
    let live = true;
    loadSession()
      .then((s) => live && setSession(s))
      .catch(() => undefined);
    const l = (s: TeamSession | null) => live && setSession(s);
    listeners.add(l);
    return () => {
      live = false;
      listeners.delete(l);
    };
  }, []);
  return { session, can: (p) => Boolean(session?.permissions?.[p]) };
}

// ---------- Settings ----------

export interface RoleEntry {
  provider_role: string;
  role: Role;
  source: "map" | "allowed";
}

export interface SignIn {
  created_at: string;
  user_id: string;
  user_name: string;
  role: string;
  action: string;
  ip: string;
}

export interface Members {
  mode: "otp" | "password" | "shared" | "none";
  provider: string;
  mapping: RoleEntry[];
  viewer_password: boolean;
  me: TeamSession;
  sign_ins?: SignIn[];
}

export type KeyScope = "read" | "server" | "client";

export const SCOPE_INFO: Record<KeyScope, { label: string; detail: string }> = {
  read: { label: "Read", detail: "Calls the HTTP API with viewer rights (GET, queries, the analyst). Send it as Authorization: Bearer <key>." },
  server: { label: "Server ingest", detail: "SDKs and OTLP exporters sending from your servers. Keep it secret." },
  client: { label: "Client ingest", detail: "Browser and mobile apps. It ships inside the app, so treat it as public: it can only send events." },
};

export interface ApiKey {
  id: number;
  name: string;
  scope: KeyScope;
  prefix: string;
  created_by: string;
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  revoked_at: string | null;
}

export interface AuditEntry {
  id: number;
  created_at: string;
  user_id: string;
  user_name: string;
  role: string;
  action: string;
  target: string;
  detail: unknown;
  ip: string;
}

export interface AuditPage {
  entries: AuditEntry[];
  retention_days: number;
  more: boolean;
}

export interface About {
  version: string;
  /** The Go release this Visionary server was built with. */
  go_version: string;
  database: string;
  auth_mode: string;
  features: { name: string; on: boolean; detail: string }[];
  /** Instances that sent system metrics in the last week, and their runtime. */
  services?: { service: string; instance: string; sdk: string; runtime: string; last_seen: string }[];
}

// ---------- Dashboards ----------

export type VarName = "instance" | "route" | "service";
export const VAR_NAMES: VarName[] = ["instance", "route", "service"];

export interface DashPanel {
  id: string;
  kind: "saved" | "builtin";
  /** Saved panel id, or "system/cpu" style built-in name. */
  ref: string;
  title?: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface DashVar {
  name: VarName;
  default?: string;
}

export type DashTime = { kind: "relative"; key: RangeKey } | { kind: "absolute"; from: string; to: string };

export interface DashboardLayout {
  panels: DashPanel[];
  variables: DashVar[];
  time?: DashTime;
}

export interface Dashboard {
  id: number;
  name: string;
  description: string;
  layout: DashboardLayout;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export function saveDashboard(d: Pick<Dashboard, "name" | "description" | "layout"> & { id?: number }): Promise<Dashboard> {
  const body = { name: d.name, description: d.description, layout: d.layout };
  return d.id ? api<Dashboard>(`/api/dashboards/${d.id}`, { method: "PUT", json: body }) : api<Dashboard>("/api/dashboards", { method: "POST", json: body });
}

export function newPanelId(): string {
  return Math.random().toString(36).slice(2, 10);
}

/** First free spot at the bottom of the grid. */
export function placeAtBottom(panels: DashPanel[], w: number, h: number): Pick<DashPanel, "x" | "y" | "w" | "h"> {
  const y = panels.reduce((m, p) => Math.max(m, p.y + p.h), 0);
  return { x: 0, y, w, h };
}

/** Built-in panels any dashboard can show, by name. */
export interface Builtin {
  ref: string;
  group: "API overview" | "Performance" | "System";
  title: string;
  description: string;
  viz: Viz;
}

const ts = (y: string[], unit: Viz["unit"], extra: Partial<Viz> = {}): Viz => ({ type: "timeseries", x: "t", y, unit, ...extra });

export const BUILTINS: Builtin[] = [
  { ref: "overview/traffic", group: "API overview", title: "Requests by status", description: "Requests per interval, stacked by HTTP status class.", viz: ts(["value"], "count", { series: "series", stack: true }) },
  { ref: "overview/latency", group: "API overview", title: "Latency percentiles", description: "Server time per interval. p99 is the slowest 1% of requests.", viz: ts(["p50", "p95", "p99"], "ms") },
  { ref: "overview/error_rate", group: "API overview", title: "Error rate", description: "Percent of requests per interval that returned 5xx or 4xx.", viz: ts(["server_errors", "client_errors"], "percent") },
  { ref: "overview/status_codes", group: "API overview", title: "Status codes", description: "Most frequent response codes in this range.", viz: { type: "bar", x: "status", y: ["requests"], unit: "count", colorBy: "status" } },
  { ref: "overview/routes", group: "API overview", title: "Routes", description: "Per route and method, ordered by traffic.", viz: { type: "table" } },
  { ref: "overview/errors", group: "API overview", title: "Top errors", description: "4xx and 5xx responses grouped by error and route.", viz: { type: "table" } },
  { ref: "overview/users", group: "API overview", title: "Most active users", description: "Users by request count.", viz: { type: "table" } },
  { ref: "performance/percentiles", group: "Performance", title: "Latency percentiles (p50–p99)", description: "p50, p90, p95 and p99 per interval.", viz: ts(["p50", "p90", "p95", "p99"], "ms") },
  { ref: "performance/apdex", group: "Performance", title: "Apdex", description: "Satisfied plus half the tolerating requests, per interval.", viz: ts(["apdex"], "none") },
  { ref: "performance/histogram", group: "Performance", title: "Latency distribution", description: "Requests per latency band.", viz: { type: "bar", x: "band", y: ["requests"], unit: "count" } },
  { ref: "performance/slowest", group: "Performance", title: "Slowest requests", description: "The slowest single requests in the range.", viz: { type: "table" } },
  { ref: "performance/saturation", group: "Performance", title: "Requests and p95", description: "Requests and p95 latency per interval.", viz: ts(["p95"], "ms") },
  { ref: "system/cpu", group: "System", title: "Host CPU", description: "Whole-machine CPU use: average and peak per interval.", viz: ts(["avg_cpu", "max_cpu"], "percent") },
  { ref: "system/cpu_breakdown", group: "System", title: "CPU breakdown", description: "User, system, iowait and steal time.", viz: ts(["user_time", "system_time", "iowait", "steal"], "percent", { stack: true }) },
  { ref: "system/process_cpu", group: "System", title: "Process CPU", description: "This process alone, as a percent of one core.", viz: ts(["avg_process_cpu", "max_process_cpu"], "percent") },
  { ref: "system/load_per_core", group: "System", title: "Load per core", description: "Load average divided by cores.", viz: ts(["load_per_core", "peak_load_per_core"], "none") },
  { ref: "system/memory", group: "System", title: "Host memory used", description: "Percent of host memory in use.", viz: ts(["host_memory"], "percent") },
  { ref: "system/memory_detail", group: "System", title: "Host memory", description: "Memory in use, available and held as page cache.", viz: ts(["used", "available", "cached"], "bytes") },
  { ref: "system/process_memory", group: "System", title: "Process memory", description: "Resident memory and runtime heap in use.", viz: ts(["rss_bytes", "heap_bytes"], "bytes") },
  { ref: "system/swap", group: "System", title: "Swap", description: "Swap in use against the total.", viz: ts(["swap_used", "swap_total"], "bytes") },
  { ref: "system/disk_space", group: "System", title: "Disk space", description: "Space and inodes used.", viz: ts(["space_used", "inodes_used"], "percent") },
  { ref: "system/disk_throughput", group: "System", title: "Disk read/write speed", description: "Disk reads and writes per second.", viz: ts(["read_speed", "write_speed"], "bytes_per_s") },
  { ref: "system/disk_iops", group: "System", title: "Disk operations", description: "Read and write operations per second.", viz: ts(["read_ops", "write_ops"], "count") },
  { ref: "system/disk_latency", group: "System", title: "Disk latency", description: "Average time per disk operation.", viz: ts(["avg_await", "peak_await"], "ms") },
  { ref: "system/network", group: "System", title: "Network throughput", description: "Bytes received and sent per second.", viz: ts(["received_bytes", "sent_bytes"], "bytes_per_s") },
  { ref: "system/tcp", group: "System", title: "TCP connections", description: "Connections in the ESTABLISHED state.", viz: ts(["established"], "count") },
  { ref: "system/goroutines", group: "System", title: "Concurrency", description: "Goroutines or live threads, and OS threads.", viz: ts(["goroutines", "threads"], "count") },
  { ref: "system/gc", group: "System", title: "GC pause", description: "Total garbage-collection pause per interval.", viz: ts(["gc_pause_ms"], "ms") },
  { ref: "system/db_pool", group: "System", title: "Database connections", description: "Connections in use and open, against the pool limit.", viz: ts(["in_use", "open", "pool_limit"], "count") },
  { ref: "system/db_wait", group: "System", title: "Waiting for a connection", description: "Time requests spent waiting for a database connection.", viz: ts(["acquire_wait_ms"], "ms") },
  { ref: "system/throughput", group: "System", title: "Throughput", description: "Requests per second and the most in flight at once.", viz: ts(["avg_rps", "peak_rps", "max_in_flight"], "count") },
  { ref: "system/instances", group: "System", title: "Instances", description: "Every instance reporting system metrics.", viz: { type: "table" } },
];

export const BUILTIN_BY_REF = new Map(BUILTINS.map((b) => [b.ref, b]));

/** Hash link to a dashboard with its range and variables. */
export function dashboardLink(id: number, from?: Date, to?: Date, vars: Partial<Record<VarName, string>> = {}): string {
  const p = new URLSearchParams();
  if (from && to) {
    p.set("from", from.toISOString());
    p.set("to", to.toISOString());
  }
  for (const [k, v] of Object.entries(vars)) if (v) p.set(`var.${k}`, v);
  const qs = p.toString();
  return `#/dashboards/${id}${qs ? `?${qs}` : ""}`;
}

/** Reads #/dashboards/<id>?... (or #/dashboards?id=<id>&...). */
export function parseDashboardHash(hash: string): { id: number | null; from?: string; to?: string; vars: Partial<Record<VarName, string>>; tv: boolean } {
  const raw = hash.replace(/^#\/?/, "");
  const [path, query = ""] = raw.split("?");
  const p = new URLSearchParams(query);
  const seg = path.split("/")[1];
  const id = Number(seg ?? p.get("id"));
  const vars: Partial<Record<VarName, string>> = {};
  for (const n of VAR_NAMES) {
    const v = p.get(`var.${n}`);
    if (v) vars[n] = v;
  }
  return {
    id: Number.isInteger(id) && id > 0 ? id : null,
    from: p.get("from") ?? undefined,
    to: p.get("to") ?? undefined,
    vars,
    tv: p.get("tv") === "1",
  };
}
