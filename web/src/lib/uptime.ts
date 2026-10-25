import { api, rangeQuery, type Result } from "./api";

export interface UptimeCheck {
  id: number;
  name: string;
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
  checks: { check: UptimeCheck; state: CheckState; alert: CheckAlert }[];
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
}

export interface RunResult {
  created_at: string;
  ok: boolean;
  status: number;
  duration_ms: number;
  error?: string;
  degraded: boolean;
  retried: boolean;
}

export const listChecks = (signal?: AbortSignal) => api<CheckList>("/api/uptime/checks", { signal });

export const getSummary = (id: number, signal?: AbortSignal) => api<Summary>(`/api/uptime/checks/${id}/summary`, { signal });

export const getPanel = (id: number, panel: "latency" | "phases" | "results" | "availability", from: Date, to: Date, signal?: AbortSignal) =>
  api<Result>(`/api/uptime/checks/${id}/${panel}?${rangeQuery(from, to)}`, { signal });

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
