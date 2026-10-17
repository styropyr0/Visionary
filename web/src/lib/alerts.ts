import { api, rangeQuery, type Result } from "./api";
import { formatCount, formatMs, formatPercent } from "./format";

export type AlertUnit = "ms" | "percent" | "rpm" | "score" | "minutes" | "count" | "burn" | "none";
export type Severity = "info" | "warning" | "critical";
export type RuleKind = "threshold" | "anomaly" | "slo";
export type SeriesStateName = "ok" | "pending" | "firing" | "resolved" | "";

export interface MetricDef {
  id: string;
  label: string;
  group: "request" | "system" | "liveness" | "uptime";
  unit: AlertUnit;
  description: string;
  scopes: string[];
  anomaly: boolean;
  op: string;
  threshold: number;
  up_is_bad: boolean;
}

export interface AlertsMeta {
  metrics: MetricDef[];
  apdex_ms: number;
  slo_target: number;
  llm: boolean;
  smtp_configured: boolean;
  smtp_from: string;
  digest_tz: string;
  interval_s: number;
  routes: string[];
  instances: string[];
  services: string[];
  can_edit: boolean;
}

export interface Scope {
  route?: string;
  method?: string;
  instance?: string;
  service?: string;
  check?: string;
}

export interface RuleSpec {
  metric: string;
  sql?: string;
  scope: Scope;
  op?: string;
  threshold: number;
  window: string;
  for: string;
  repeat?: string;
  sensitivity?: string;
  direction?: string;
  target?: number;
}

export interface AlertRule {
  id: number;
  name: string;
  kind: RuleKind;
  severity: Severity;
  channels: number[];
  runbook: string;
  enabled: boolean;
  muted_until: string;
  spec: RuleSpec;
  created_at: string;
  updated_at: string;
}

export interface SeriesState {
  key: string;
  label: string;
  state: SeriesStateName;
  since: string;
  evaluated_at?: string;
  value: number | null;
  threshold: number | null;
  message: string;
  recent: [number, number][] | null;
}

export interface RuleView extends AlertRule {
  status: {
    state: SeriesStateName;
    since?: string;
    value: number | null;
    threshold: number | null;
    message: string;
    evaluated_at?: string;
    error?: string;
    firing: number;
    series: SeriesState[];
    recent: [number, number][];
  };
}

export interface AlertEvent {
  id: number;
  created_at: string;
  rule_id: number;
  rule_name: string;
  kind: RuleKind;
  severity: Severity;
  state: "firing" | "resolved";
  series_key: string;
  value: number | null;
  threshold: number | null;
  message: string;
  explanation: string;
  notified: boolean;
}

export type ChannelKind = "slack" | "discord" | "webhook" | "email";

export interface Channel {
  id: number;
  name: string;
  kind: ChannelKind;
  url_masked?: string;
  has_secret: boolean;
  to?: string[];
  digest_enabled: boolean;
  digest_weekday: number;
  digest_hour: number;
  digest_sent_at: string;
  created_at: string;
}

export interface ChannelInput {
  name: string;
  kind: ChannelKind;
  url?: string;
  secret?: string;
  clear_secret?: boolean;
  to?: string[];
  digest_enabled: boolean;
  digest_weekday: number;
  digest_hour: number;
}

export interface SLOStatus {
  target: number;
  burn: Record<string, number>;
  requests: Record<string, number>;
  total_30d: number;
  bad_30d: number;
  budget_remaining: number | null;
  breach: boolean;
  pair?: string;
  value: number;
  threshold: number;
  budget_at: string;
}

export interface SLOItem {
  rule: RuleView;
  status: SLOStatus | null;
  error?: string;
}

export interface Observation {
  key: string;
  label: string;
  value: number | null;
  threshold: number | null;
  breach: boolean;
  message: string;
}

export interface Baseline {
  source: "seasonal" | "24h";
  median: number;
  points: number;
  weeks?: number;
  score: number;
  cutoff: number;
  upper: number;
  lower: number;
  current: number | null;
}

export interface PreviewResponse {
  unit: AlertUnit;
  result: Result | null;
  threshold: number | null;
  lower?: number | null;
  baseline?: Baseline;
  slo?: SLOStatus;
  current: Observation[];
  note?: string;
}

export interface DigestPreview {
  lines: string[];
  time_zone: string;
  llm: boolean;
  data: {
    from: string;
    to: string;
    this_week: DigestWeek;
    previous_week: DigestWeek;
    slowest_routes: { route: string; requests: number; p95_ms: number; previous_p95_ms: number | null }[];
    incidents: { rule: string; severity: Severity; count: number }[] | null;
    releases: { version: string; service: string; first_seen: string }[] | null;
  };
}

export interface DigestWeek {
  requests: number;
  p95_ms: number | null;
  error_rate: number | null;
  incidents: number;
}

export interface RuleInput {
  name: string;
  kind: RuleKind;
  severity: Severity;
  channels: number[];
  runbook: string;
  enabled: boolean;
  spec: RuleSpec;
}

export const alertsApi = {
  meta: (signal?: AbortSignal) => api<AlertsMeta>("/api/alerts/meta", { signal }),
  rules: (signal?: AbortSignal) => api<{ rules: RuleView[] }>("/api/alerts/rules", { signal }),
  createRule: (r: RuleInput) => api<AlertRule>("/api/alerts/rules", { method: "POST", json: r }),
  updateRule: (id: number, r: RuleInput) => api<AlertRule>(`/api/alerts/rules/${id}`, { method: "PUT", json: r }),
  deleteRule: (id: number) => api<void>(`/api/alerts/rules/${id}`, { method: "DELETE" }),
  mute: (id: number, minutes: number) => api<{ muted_until: string }>(`/api/alerts/rules/${id}/mute`, { method: "POST", json: minutes > 0 ? { minutes } : {} }),
  evaluate: (id: number) => api<RuleView>(`/api/alerts/rules/${id}/evaluate`, { method: "POST", json: {} }),
  preview: (r: RuleInput, signal?: AbortSignal) => api<PreviewResponse>("/api/alerts/preview", { method: "POST", json: r, signal }),
  events: (from: Date, to: Date, filters: Record<string, string>, signal?: AbortSignal) =>
    api<{ events: AlertEvent[] }>(`/api/alerts/events?${rangeQuery(from, to, filters)}`, { signal }),
  slos: (signal?: AbortSignal) => api<{ slos: SLOItem[]; apdex_ms: number }>("/api/alerts/slos", { signal }),
  channels: (signal?: AbortSignal) => api<{ channels: Channel[] }>("/api/alerts/channels", { signal }),
  createChannel: (c: ChannelInput) => api<Channel>("/api/alerts/channels", { method: "POST", json: c }),
  updateChannel: (id: number, c: ChannelInput) => api<Channel>(`/api/alerts/channels/${id}`, { method: "PUT", json: c }),
  deleteChannel: (id: number) => api<void>(`/api/alerts/channels/${id}`, { method: "DELETE" }),
  testChannel: (id: number) => api<{ ok: boolean; error?: string }>(`/api/alerts/channels/${id}/test`, { method: "POST", json: {} }),
  sendDigest: (id: number) => api<{ ok: boolean; error?: string }>(`/api/alerts/channels/${id}/digest`, { method: "POST", json: {} }),
  digest: (signal?: AbortSignal) => api<DigestPreview>("/api/alerts/digest", { signal }),
};

export function formatAlertValue(v: number | null | undefined, unit: AlertUnit): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "—";
  switch (unit) {
    case "ms":
      return formatMs(v);
    case "percent":
      return formatPercent(v);
    case "rpm":
      return `${formatCount(Math.round(v * 10) / 10)}/min`;
    case "score":
      return v.toFixed(2);
    case "minutes":
      return `${Math.round(v)} min`;
    case "burn":
      return `${v.toFixed(1)}×`;
    case "count":
      return formatCount(Math.round(v));
    default:
      return formatCount(Math.round(v * 100) / 100, false);
  }
}

/** The chart unit closest to an alert unit, for axis labels. */
export function axisUnit(u: AlertUnit): "ms" | "percent" | "count" | "none" {
  if (u === "ms" || u === "percent" || u === "count") return u;
  return "none";
}

export const OPS = [">", "<", ">=", "<="];
export const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

export function ruleUnit(rule: { kind: RuleKind; spec: RuleSpec }, metrics: MetricDef[]): AlertUnit {
  if (rule.kind === "slo") return "burn";
  if (rule.spec.metric === "custom") return "none";
  return metrics.find((m) => m.id === rule.spec.metric)?.unit ?? "none";
}

export function describeCondition(rule: AlertRule, metrics: MetricDef[]): string {
  const m = metrics.find((x) => x.id === rule.spec.metric);
  const sc = rule.spec.scope ?? {};
  const where = [sc.method && sc.route ? `${sc.method} ${sc.route}` : sc.route || sc.method, sc.service && `service ${sc.service}`, sc.instance, sc.check && `check ${sc.check}`]
    .filter(Boolean)
    .join(", ");
  const scoped = where ? ` · ${where}` : "";
  if (rule.kind === "slo") return `${rule.spec.target}% within the Apdex threshold over 30 days${scoped}`;
  if (rule.kind === "anomaly") return `${m?.label ?? rule.spec.metric} unusual for this time of week, ${rule.spec.sensitivity ?? "medium"} sensitivity, ${rule.spec.window} window${scoped}`;
  const label = rule.spec.metric === "custom" ? "Custom SQL" : (m?.label ?? rule.spec.metric);
  const unit = ruleUnit(rule, metrics);
  const win = rule.spec.metric === "instance_silent" ? "" : ` over ${rule.spec.window}`;
  const hold = rule.spec.for && rule.spec.for !== "0s" && rule.spec.for !== "0" ? ` for ${rule.spec.for}` : "";
  return `${label} ${rule.spec.op} ${formatAlertValue(rule.spec.threshold, unit)}${win}${hold}${scoped}`;
}

export function isMuted(r: AlertRule, now = Date.now()): boolean {
  return !!r.muted_until && Date.parse(r.muted_until) > now;
}
