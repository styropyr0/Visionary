import { api, rangeQuery, type Result } from "./api";

export type CheckKind = "http" | "browser";

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
  /** Browser checks only; values always come back as MASK. */
  secrets?: Record<string, string>;
  final_screenshot?: boolean;
  /** Where it runs from: "server" and uptime location names. */
  locations: string[];
  /** With several locations: down when any, a majority or all fail. */
  location_rule: LocationRule;
  /** Break it down by location on the status page. */
  status_show_locations: boolean;
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
  | "script";

export interface BrowserStep {
  action: StepAction;
  selector?: string;
  text?: string;
  value?: string;
  count?: number;
  op?: "eq" | "gte" | "lte";
  ms?: number;
  label?: string;
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
}

export const getBrowserStatus = (signal?: AbortSignal) => api<BrowserStatus>("/api/uptime/browser", { signal });

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
