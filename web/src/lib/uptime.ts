import { api, rangeQuery, type Result } from "./api";
import type { ApiConfig, DnsConfig, HeartbeatConfig, PingConfig, SslConfig, TcpConfig } from "./uptimeKinds";

export type CheckKind = "http" | "browser" | "playwright" | "api" | "tcp" | "dns" | "ping" | "ssl" | "heartbeat";

export interface UptimeCheck {
  id: number;
  name: string;
  kind: CheckKind;
  url: string;
  method: string;
  expect_status: string;
  body_contains: string;
  /** Secret-looking values come back as MASK; sending MASK keeps the stored value. */
  headers: Record<string, string>;
  interval_s: number;
  timeout_ms: number;
  /** 0 means no latency threshold. */
  degraded_ms: number;
  follow_redirects: boolean;
  enabled: boolean;
  on_status_page: boolean;
  status_show_url: boolean;
  created_at: string;
  updated_at: string;
  /** Browser checks only. */
  steps?: BrowserStep[];
  /** Browser and API checks; values always come back as MASK. */
  secrets?: Record<string, string>;
  final_screenshot?: boolean;
  /** Lower case, sorted. */
  tags?: string[];
  /** Playwright checks only: the test its reported runs come from. */
  playwright?: { project: string; title: string };
  /** Where it runs from: "server" and uptime location names. */
  locations: string[];
  /** With several locations: down when any, a majority or all fail. */
  location_rule: LocationRule;
  /** Break it down by location on the status page. */
  status_show_locations: boolean;
  /** The settings of the other kinds, one per kind (lib/uptimeKinds.ts). */
  api?: ApiConfig;
  tcp?: TcpConfig;
  dns?: DnsConfig;
  ping?: PingConfig;
  ssl?: SslConfig;
  heartbeat?: HeartbeatConfig;
  /** Origins that get the run's traceparent; empty means the first page's. */
  trace_origins?: string[];
  no_trace?: boolean;
  /** Performance limits of browser runs; 0 or absent is none. Over max_* fails, over warn_* is degraded. */
  max_lcp_ms?: number;
  max_cls?: number;
  max_ttfb_ms?: number;
  max_duration_ms?: number;
  warn_lcp_ms?: number;
  warn_cls?: number;
  warn_ttfb_ms?: number;
  /** The screen a browser check runs at; custom uses viewport_w × viewport_h. */
  device?: Device;
  viewport_w?: number;
  viewport_h?: number;
  /** Keep a small screenshot after every step. */
  step_screenshots?: boolean;
  files?: BrowserFile[];
}

export type Device = "desktop" | "laptop" | "tablet" | "phone" | "custom";

export const DEVICES: { value: Device; label: string; size: string }[] = [
  { value: "desktop", label: "Desktop", size: "1280 × 800" },
  { value: "laptop", label: "Laptop", size: "1440 × 900" },
  { value: "tablet", label: "Tablet", size: "768 × 1024" },
  { value: "phone", label: "Phone", size: "390 × 844, mobile, touch, 3×" },
  { value: "custom", label: "Custom", size: "" },
];

/** "Phone" or "Custom 1000 × 700", for lists. */
export function deviceLabel(c: Pick<UptimeCheck, "device" | "viewport_w" | "viewport_h">): string {
  if (c.device === "custom") return `${c.viewport_w} × ${c.viewport_h}`;
  return DEVICES.find((d) => d.value === (c.device || "desktop"))?.label ?? "Desktop";
}

export type LocationRule = "any" | "majority" | "all";

export const RULE_LABEL: Record<LocationRule, string> = { any: "any location fails", majority: "most locations fail", all: "every location fails" };

export type LocationStateName = "up" | "degraded" | "down" | "stale" | "pending" | "no_browser" | "paused";

/** How one location sees a check now. */
export interface LocationState {
  name: string;
  state: LocationStateName;
  last_at?: string;
  last_status: number;
  last_duration_ms: number;
  last_error?: string;
}

export const LOCATION_STATE_LABEL: Record<LocationStateName, string> = {
  up: "Up",
  degraded: "Degraded",
  down: "Down",
  stale: "Not reporting",
  pending: "Pending",
  no_browser: "No browser there",
  paused: "Paused",
};

/** A place checks run from: the server itself, or a host agent elsewhere. */
export interface UptimeLocation {
  id: number;
  name: string;
  builtin: boolean;
  key_prefix?: string;
  created_at?: string;
  last_seen_at?: string;
  version?: string;
  host?: string;
  browser: boolean;
  browser_version?: string;
  /** Check kinds its runner can run; absent for the server, which runs them all. */
  kinds?: string[];
  /** new: never polled. */
  state: "online" | "offline" | "new";
  checks: number;
}

export interface LocationList {
  locations: UptimeLocation[];
  can_admin: boolean;
  poll_s: number;
  stale_after_s: number;
}

export const listLocations = (signal?: AbortSignal) => api<LocationList>("/api/uptime/locations", { signal });

export const createLocation = (name: string) => api<{ location: UptimeLocation; secret: string }>("/api/uptime/locations", { method: "POST", json: { name } });

export const newLocationKey = (name: string) => api<{ secret: string; key_prefix: string }>(`/api/uptime/locations/${encodeURIComponent(name)}/key`, { method: "POST", json: {} });

export const deleteLocation = (name: string) => api<void>(`/api/uptime/locations/${encodeURIComponent(name)}`, { method: "DELETE" });

/** Locations in one order everywhere: the server first, then by name. */
export function locationOrder(names: string[]): string[] {
  return [...names].sort((a, b) => (a === "server" ? -1 : b === "server" ? 1 : a.localeCompare(b)));
}

/** A location's color: its slot in the order of every location, so it is the same on every check. */
export function locationColor(name: string, all: string[], series: string[]): string {
  const i = Math.max(0, locationOrder(all.includes(name) ? all : [...all, name]).indexOf(name));
  return series[i % series.length] ?? "var(--accent)";
}

export const isServerOnly = (c: Pick<UptimeCheck, "locations">) => !c.locations?.length || (c.locations.length === 1 && c.locations[0] === "server");

export type StepAction =
  | "goto"
  | "click"
  | "type"
  | "press"
  | "select"
  | "wait_selector"
  | "wait_text"
  | "wait_navigation"
  | "assert_text"
  | "assert_no_text"
  | "assert_url"
  | "assert_count"
  | "wait"
  | "script"
  | "extract"
  | "hover"
  | "drag"
  | "upload"
  | "frame"
  | "set_cookie"
  | "set_storage"
  | "grant_permission";

export interface BrowserStep {
  action: StepAction;
  selector?: string;
  text?: string;
  value?: string;
  count?: number;
  op?: "eq" | "gte" | "lte";
  ms?: number;
  label?: string;
  /** Fails the step when it takes longer. */
  max_ms?: number;
  /** The variable of extract, the cookie of set_cookie, the key of set_storage, the file of upload. */
  name?: string;
  /** What extract reads: text, value or an attribute. */
  attr?: string;
  regex?: string;
  /** Where drag drops. */
  target?: string;
  /** set_cookie: URL or domain; set_storage: local or session; grant_permission: origin. */
  scope?: string;
  permission?: "geolocation" | "notifications" | "clipboard";
  lat?: number;
  lon?: number;
}

/** A file upload steps send; data (base64) comes back empty, and sending it empty keeps the stored file. */
export interface BrowserFile {
  name: string;
  type?: string;
  size: number;
  data?: string;
}

export const MASK = "********";

export type StateName = "up" | "degraded" | "down" | "paused" | "pending";

export interface DayBar {
  day: string;
  checks: number;
  failed: number;
  degraded: number;
  uptime: number | null;
}

export interface CheckState {
  state: StateName;
  last_at?: string;
  last_status: number;
  last_duration_ms: number;
  last_error?: string;
  cert_expires_at?: string;
  incident_since?: string;
  /** A browser check waiting for a browser on the server. */
  no_browser?: boolean;
  /** Why a ping check cannot run on this server. */
  unavailable?: string;
  uptime_24h: number | null;
  days: DayBar[];
  /** Hourly average response time over the last 24h; null where no check passed. */
  spark: (number | null)[];
}

/** How a check is alerted on. */
export interface CheckAlert {
  /** custom: its own rule; off: its own rule, disabled; default: the rule for every check; none: nothing alerts on it. */
  mode: "custom" | "off" | "default" | "none";
  rule_id?: number;
  rule_name?: string;
  /** Names of the rule's channels. */
  channels: string[];
  channel_ids: number[];
  failures: number;
  severity?: string;
  firing: boolean;
}

export interface CheckAlertInput {
  mode: "default" | "custom" | "off";
  channels?: number[];
  failures?: number;
  severity?: string;
}

export interface CheckList {
  /** locations is null for checks that run from the server alone. */
  checks: { check: UptimeCheck; state: CheckState; alert: CheckAlert; locations?: LocationState[] | null }[];
  can_edit: boolean;
}

/** Whether anyone hears when the check goes down. */
export function alertGap(a: CheckAlert | undefined): string {
  if (!a || a.mode === "none") return "No alert rule covers this check";
  if (a.mode === "off") return "Alerts are off for this check";
  if (a.channels.length === 0) return `“${a.rule_name}” has no channels, so nobody is notified`;
  return "";
}

export type Window = "24h" | "7d" | "30d" | "90d";

export interface Summary {
  check: UptimeCheck;
  state: CheckState;
  uptime: Record<Window, number | null>;
  checks_run: Record<Window, number>;
  latency: { avg_ms: number | null; p50: number | null; p95: number | null };
  locations?: LocationState[] | null;
  can_edit: boolean;
}

export interface Incident {
  started_at: string;
  ended_at: string | null;
  duration_s: number;
  failures: number;
  status: number;
  error?: string;
  ongoing: boolean;
  /** Locations that failed during it, for checks that run from more than the server. */
  locations?: string[];
}

export interface RunResult {
  created_at: string;
  ok: boolean;
  status: number;
  duration_ms: number;
  error?: string;
  degraded: boolean;
  retried: boolean;
  lcp_ms?: number;
  cls?: number;
}

export interface BrowserStatus {
  available: boolean;
  path?: string;
  source?: "setting" | "env" | "found";
  version?: string;
  error?: string;
  help?: string;
  workers: number;
}

export interface StepResult {
  action: StepAction;
  label: string;
  status: "passed" | "failed" | "skipped";
  duration_ms: number;
  error?: string;
  /** What the step found, such as an extracted value. */
  detail?: string;
}

export interface BrowserRunSummary {
  id: number;
  created_at: string;
  ok: boolean;
  failed_step: number;
  failed_label: string;
  duration_ms: number | null;
  error: string;
  lcp_ms: number | null;
  cls: number | null;
  ttfb_ms: number | null;
  retried: boolean;
  screenshot: boolean;
  final_screenshot: boolean;
  steps: number;
  degraded: boolean;
  /** Which "warn above" values a degraded run went over. */
  warning: string;
}

export interface StepStat {
  label: string;
  runs: number;
  failed: number;
  avg_ms: number;
  p95_ms: number;
}

export interface BrowserRunDetail {
  id: number;
  created_at: string;
  ok: boolean;
  failed_step: number;
  steps: StepResult[];
  console: string[];
  network: { url: string; status?: number; error?: string; type?: string }[];
  /** data: URLs, empty when none was taken. */
  screenshot: string;
  final_screenshot: string;
  duration_ms: number | null;
  error: string;
  lcp_ms: number | null;
  cls: number | null;
  ttfb_ms: number | null;
  status: number;
  retried: boolean;
  /** The run's trace id, and whether a backend recorded anything under it. */
  trace_id: string;
  trace_found: boolean;
  degraded: boolean;
  warning: string;
  /** Every request of the run, timed from its start; empty for older runs. */
  requests: NetRequest[];
  /** data: URLs after each step, aligned with steps; "" where none was kept. */
  step_shots: string[];
}

export interface NetRequest {
  url: string;
  method: string;
  status?: number;
  type?: string;
  mime?: string;
  start_ms: number;
  duration_ms: number;
  size?: number;
  failed?: boolean;
  error?: string;
}

export const getBrowserStatus = (signal?: AbortSignal) => api<BrowserStatus>("/api/uptime/browser", { signal });

/** Steps converted from a Chrome DevTools Recorder export, and the recorded steps that have no equivalent. */
export interface RecorderImport {
  title: string;
  steps: BrowserStep[];
  device?: Device;
  viewport_w?: number;
  viewport_h?: number;
  unsupported: string[];
}

export const importRecording = (json: string) =>
  api<RecorderImport>("/api/uptime/browser/recorder", { method: "POST", body: json, headers: { "Content-Type": "application/json" } });

export const getBrowserRuns = (id: number, from: Date, to: Date, signal?: AbortSignal) =>
  api<{ runs: BrowserRunSummary[]; steps: StepStat[] }>(`/api/uptime/checks/${id}/runs?${rangeQuery(from, to)}`, { signal });

export const getBrowserRun = (id: number, run: number, signal?: AbortSignal) => api<BrowserRunDetail>(`/api/uptime/checks/${id}/runs/${run}`, { signal });

export const listChecks = (signal?: AbortSignal) => api<CheckList>("/api/uptime/checks", { signal });

export const getSummary = (id: number, signal?: AbortSignal) => api<Summary>(`/api/uptime/checks/${id}/summary`, { signal });

export type PanelName = "latency" | "phases" | "results" | "availability" | "vitals" | "locations" | "location_latency" | "location_availability";

/** location narrows results to one location. */
export const getPanel = (id: number, panel: PanelName, from: Date, to: Date, signal?: AbortSignal, location?: string) =>
  api<Result>(`/api/uptime/checks/${id}/${panel}?${rangeQuery(from, to)}${location ? `&location=${encodeURIComponent(location)}` : ""}`, { signal });

export const getIncidents = (id: number, from: Date, to: Date, signal?: AbortSignal) =>
  api<{ incidents: Incident[]; result: Result }>(`/api/uptime/checks/${id}/incidents?${rangeQuery(from, to)}`, { signal });

export type CheckInput = Omit<UptimeCheck, "id" | "created_at" | "updated_at">;

/** Saves a check; alert, when given, sets how it is alerted on in the same step. */
export function saveCheck(input: CheckInput & { alert?: CheckAlertInput }, id?: number) {
  return id
    ? api<{ check: UptimeCheck; alert_error?: string }>(`/api/uptime/checks/${id}`, { method: "PUT", json: input })
    : api<{ check: UptimeCheck; alert_error?: string }>("/api/uptime/checks", { method: "POST", json: input });
}

export const getCheckAlert = (id: number, signal?: AbortSignal) => api<{ alert: CheckAlert }>(`/api/uptime/checks/${id}/alert`, { signal });

export const setCheckAlert = (id: number, input: CheckAlertInput) => api<{ alert: CheckAlert }>(`/api/uptime/checks/${id}/alert`, { method: "PUT", json: input });

export const deleteCheck = (id: number) => api<void>(`/api/uptime/checks/${id}`, { method: "DELETE" });

export const setEnabled = (id: number, enabled: boolean) =>
  api<{ check: UptimeCheck }>(`/api/uptime/checks/${id}/enabled`, { method: "POST", json: { enabled } });

export const runCheck = (id: number) => api<{ result: RunResult }>(`/api/uptime/checks/${id}/run`, { method: "POST", json: {} });

export const STATE_LABEL: Record<StateName, string> = {
  up: "Up",
  degraded: "Degraded",
  down: "Down",
  paused: "Paused",
  pending: "Pending",
};

/** A day's bar color: any failure is at least minor; under 99% is major. */
export function dayLevel(b: DayBar): "ok" | "minor" | "major" | "none" {
  if (b.checks === 0) return "none";
  if (b.failed === 0) return "ok";
  return (b.uptime ?? 0) >= 99 ? "minor" : "major";
}

export function formatUptime(p: number | null | undefined): string {
  if (p === null || p === undefined) return "—";
  if (p === 100) return "100%";
  return `${p.toFixed(p >= 99 ? 3 : 2).replace(/0+$/, "").replace(/\.$/, "")}%`;
}

export function formatDuration(sec: number): string {
  if (sec < 60) return `${Math.max(0, Math.round(sec))}s`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m}m ${Math.round(sec % 60)}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

export function formatInterval(s: number): string {
  return s % 3600 === 0 ? `${s / 3600}h` : s % 60 === 0 ? `${s / 60}m` : `${s}s`;
}

export const INTERVALS = [30, 60, 120, 300, 600, 900, 1800, 3600];
