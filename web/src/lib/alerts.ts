import { api, rangeQuery, type Result } from "./api";
import { formatCount, formatMs, formatPercent } from "./format";

export type AlertUnit = "ms" | "percent" | "rpm" | "score" | "minutes" | "count" | "burn" | "gb" | "mb" | "days" | "none";
export type Severity = "info" | "warning" | "critical";
export type RuleKind = "threshold" | "anomaly" | "slo";
export type SeriesStateName = "ok" | "pending" | "firing" | "resolved" | "";

export type MetricGroup = "request" | "system" | "liveness" | "uptime" | "errors" | "frontend" | "dependency" | "security" | "container" | "logs";

export const GROUP_LABEL: Record<MetricGroup, string> = {
  request: "Requests",
  system: "System",
  liveness: "Liveness",
  uptime: "Uptime",
  errors: "Errors",
  frontend: "Frontend",
  dependency: "Dependencies",
  security: "Security",
  container: "Containers",
  logs: "Logs",
};

export interface MetricDef {
  id: string;
  label: string;
  group: MetricGroup;
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
  /** Current offset of the digest zone from UTC, in minutes. */
  digest_offset_min: number;
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
  /** security_events rules: finding kind, minimum severity, source (api or host) and host. */
  event_kind?: string;
  event_severity?: string;
  source?: string;
  host?: string;
  /** Container rules: the container's name. */
  container?: string;
  /** Frontend rules: the client app. */
  app?: string;
}

export type NoDataPolicy = "" | "alerting" | "keep";

export interface EscalationStep {
  after: string;
  channels: number[];
}

export interface RuleSpec {
  metric: string;
  sql?: string;
  /** The Logs search of log_matches and log_absent rules. */
  query?: string;
  scope: Scope;
  op?: string;
  threshold: number;
  window: string;
  for: string;
  repeat?: string;
  sensitivity?: string;
  direction?: string;
  target?: number;
  /** What a window without data means; empty is OK. */
  no_data?: NoDataPolicy;
  /** How long the condition must stay clear before a firing series resolves. */
  clear_for?: string;
  /** Threshold rules: the value a reading must pass to count as clear. */
  clear_threshold?: number | null;
  /** Absent means resolved notices are sent. */
  notify_resolved?: boolean;
  mention?: string;
  title?: string;
  message?: string;
  escalation?: EscalationStep[];
  depends_on?: number[];
  labels?: Record<string, string>;
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
  ack?: { by: string; at: string; note?: string };
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
    /** Firing series someone acknowledged. */
    acked: number;
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
  /** Why nothing was sent, when nothing was. */
  suppressed: string;
  deliveries: Delivery[];
}

export interface Delivery {
  id: number;
  event_id: number;
  channel_id: number;
  channel_name: string;
  channel_kind: ChannelKind;
  event: string;
  status: "pending" | "sent" | "failed";
  attempts: number;
  error?: string;
  created_at: string;
  sent_at?: string;
  next_at?: string;
}

export type ChannelKind = "slack" | "discord" | "teams" | "google_chat" | "webhook" | "email" | "pagerduty" | "opsgenie" | "telegram" | "sms";

export interface Channel {
  id: number;
  name: string;
  kind: ChannelKind;
  url_masked?: string;
  has_secret: boolean;
  secret_hint?: string;
  to?: string[];
  chat_id?: string;
  account_sid?: string;
  from?: string;
  region?: string;
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
  chat_id?: string;
  account_sid?: string;
  from?: string;
  region?: string;
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
    slowest_routes: { route: string; requests: number; p95_ms: number; previous_p95_ms: number | null }[] | null;
    incidents: { rule: string; severity: Severity; count: number }[] | null;
    releases: { version: string; service: string; first_seen: string }[] | null;
    days: { day: string; requests: number; errors_5xx: number }[] | null;
    regressed_routes: DigestChange[] | null;
    improved_routes: DigestChange[] | null;
    busiest_routes: { route: string; requests: number; previous_requests: number }[] | null;
    errors?: {
      total: number;
      previous_total: number;
      new_issues: number;
      top: { type: string; message: string; route: string; count: number; new: boolean }[] | null;
    };
    uptime: { check: string; availability: number; previous_availability: number | null; failures: number; avg_ms: number }[] | null;
    machines: { instance: string; avg_cpu: number | null; peak_cpu: number | null; peak_memory: number | null; peak_disk: number | null }[] | null;
    containers?: {
      restarts: number;
      oom_kills: number;
      exits: number;
      unhealthy: number;
      worst: { container: string; restarts: number; oom_kills: number; unhealthy: number }[] | null;
    };
    web_vitals: { name: string; p75: number; previous_p75: number | null; rating: "good" | "needs_improvement" | "poor" }[] | null;
    security?: {
      findings_by_severity: Record<string, number>;
      findings_by_kind: { kind: string; count: number }[] | null;
      top_ips: { ip: string; findings: number; kinds: string }[] | null;
      failed_logins: number;
      previous_failed_logins: number;
      hosts_with_pending_updates: { host: string; pending: number }[] | null;
    };
  };
}

export interface DigestChange {
  route: string;
  requests: number;
  p95_ms: number;
  previous_p95_ms: number;
  change_pct: number;
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

export interface SilenceMatchers {
  rule_ids?: number[];
  severities?: Severity[];
  labels?: Record<string, string>;
  series?: string;
}

export interface SilenceSchedule {
  kind: "once" | "weekly";
  starts_at?: string;
  ends_at?: string;
  days?: number[];
  from?: string;
  to?: string;
  tz?: string;
}

export interface Silence {
  id: number;
  name: string;
  matchers: SilenceMatchers;
  schedule: SilenceSchedule;
  enabled: boolean;
  created_by: string;
  created_at: string;
  state: "active" | "scheduled" | "ended" | "disabled";
}

export interface SilenceInput {
  name: string;
  matchers: SilenceMatchers;
  schedule: SilenceSchedule;
  enabled: boolean;
}

export interface HistoryEntry {
  id: number;
  rule_id: number;
  at: string;
  actor: string;
  action: string;
  changes: string[];
  rule: AlertRule | null;
}

export interface AlertsSummary {
  firing_rules: number;
  firing_series: number;
  unacked: number;
  checks_down: number;
  down: string[];
}

export interface ImportResult {
  created: number;
  skipped: number;
  silences: number;
  warnings: string[];
}

export type BulkAction = "enable" | "disable" | "mute" | "unmute" | "delete";

export const alertsApi = {
  meta: (signal?: AbortSignal) => api<AlertsMeta>("/api/alerts/meta", { signal }),
  rules: (signal?: AbortSignal) => api<{ rules: RuleView[] }>("/api/alerts/rules", { signal }),
  createRule: (r: RuleInput) => api<AlertRule>("/api/alerts/rules", { method: "POST", json: r }),
  updateRule: (id: number, r: RuleInput) => api<AlertRule>(`/api/alerts/rules/${id}`, { method: "PUT", json: r }),
  deleteRule: (id: number) => api<void>(`/api/alerts/rules/${id}`, { method: "DELETE" }),
  mute: (id: number, minutes: number) => api<{ muted_until: string }>(`/api/alerts/rules/${id}/mute`, { method: "POST", json: minutes > 0 ? { minutes } : {} }),
  evaluate: (id: number) => api<RuleView>(`/api/alerts/rules/${id}/evaluate`, { method: "POST", json: {} }),
  /** Acknowledges one series, or every firing series without seriesKey. */
  ack: (id: number, note = "", seriesKey?: string) =>
    api<{ series: string[]; acked_by: string }>(`/api/alerts/rules/${id}/ack`, { method: "POST", json: seriesKey === undefined ? { note } : { note, series_key: seriesKey } }),
  unack: (id: number, seriesKey?: string) =>
    api<{ series: string[] }>(`/api/alerts/rules/${id}/unack`, { method: "POST", json: seriesKey === undefined ? {} : { series_key: seriesKey } }),
  history: (id: number, signal?: AbortSignal) => api<{ history: HistoryEntry[] }>(`/api/alerts/rules/${id}/history`, { signal }),
  bulk: (ids: number[], action: BulkAction, minutes = 0) => api<{ changed: number }>("/api/alerts/rules/bulk", { method: "POST", json: { ids, action, minutes } }),
  exportURL: "/api/alerts/export",
  import: (file: unknown) => api<ImportResult>("/api/alerts/import", { method: "POST", json: file }),
  summary: (signal?: AbortSignal) => api<AlertsSummary>("/api/alerts/summary", { signal }),
  silences: (signal?: AbortSignal) => api<{ silences: Silence[] }>("/api/alerts/silences", { signal }),
  createSilence: (s: SilenceInput) => api<Silence>("/api/alerts/silences", { method: "POST", json: s }),
  updateSilence: (id: number, s: SilenceInput) => api<Silence>(`/api/alerts/silences/${id}`, { method: "PUT", json: s }),
  deleteSilence: (id: number) => api<void>(`/api/alerts/silences/${id}`, { method: "DELETE" }),
  preview: (r: RuleInput, signal?: AbortSignal) => api<PreviewResponse>("/api/alerts/preview", { method: "POST", json: r, signal }),
  events: (from: Date, to: Date, filters: Record<string, string>, signal?: AbortSignal) =>
    api<{ events: AlertEvent[]; next: string }>(`/api/alerts/events?${rangeQuery(from, to, filters)}`, { signal }),
  eventsCSVURL: (from: Date, to: Date, filters: Record<string, string>) => `/api/alerts/events.csv?${rangeQuery(from, to, filters)}`,
  slos: (signal?: AbortSignal) => api<{ slos: SLOItem[]; apdex_ms: number }>("/api/alerts/slos", { signal }),
  channels: (signal?: AbortSignal) => api<{ channels: Channel[] }>("/api/alerts/channels", { signal }),
  createChannel: (c: ChannelInput) => api<Channel>("/api/alerts/channels", { method: "POST", json: c }),
  updateChannel: (id: number, c: ChannelInput) => api<Channel>(`/api/alerts/channels/${id}`, { method: "PUT", json: c }),
  deleteChannel: (id: number) => api<void>(`/api/alerts/channels/${id}`, { method: "DELETE" }),
  testChannel: (id: number) => api<{ ok: boolean; error?: string }>(`/api/alerts/channels/${id}/test`, { method: "POST", json: {} }),
  sendDigest: (id: number) => api<{ ok: boolean; error?: string }>(`/api/alerts/channels/${id}/digest`, { method: "POST", json: {} }),
  digest: (signal?: AbortSignal) => api<DigestPreview>("/api/alerts/digest", { signal }),
  digestSummary: () => api<{ summary: string }>("/api/alerts/digest/summary", { method: "POST", json: {} }),
  /** The email version, served sandboxed for an iframe. */
  digestEmailURL: "/api/alerts/digest/email",
  digestPdfURL: "/api/alerts/digest/pdf",
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
    case "gb":
      return `${v.toFixed(1)} GB`;
    case "mb":
      return `${formatCount(Math.round(v))} MB`;
    case "days":
      return `${v.toFixed(1)} days`;
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
  const where = [sc.method && sc.route ? `${sc.method} ${sc.route}` : sc.route || sc.method, sc.service && `service ${sc.service}`, sc.instance, sc.check && `check ${sc.check}`,
    sc.event_kind && sc.event_kind.replace(/_/g, " "), sc.event_severity && `${sc.event_severity} and up`, sc.source && `from ${sc.source}`, sc.host && `host ${sc.host}`,
    sc.container && `container ${sc.container}`, sc.app && `app ${sc.app}`]
    .filter(Boolean)
    .join(", ");
  const scoped = where ? ` · ${where}` : "";
  if (rule.kind === "slo") return `${rule.spec.target}% within the Apdex threshold over 30 days${scoped}`;
  if (rule.kind === "anomaly") return `${m?.label ?? rule.spec.metric} unusual for this time of week, ${rule.spec.sensitivity ?? "medium"} sensitivity, ${rule.spec.window} window${scoped}`;
  const label = rule.spec.metric === "custom" ? "Custom SQL" : (m?.label ?? rule.spec.metric);
  const unit = ruleUnit(rule, metrics);
  const win = rule.spec.metric === "instance_silent" || rule.spec.metric === "uptime_cert_days" ? "" : ` over ${rule.spec.window}`;
  const hold = rule.spec.for && rule.spec.for !== "0s" && rule.spec.for !== "0" ? ` for ${rule.spec.for}` : "";
  return `${label} ${rule.spec.op} ${formatAlertValue(rule.spec.threshold, unit)}${win}${hold}${scoped}`;
}

export function isMuted(r: AlertRule, now = Date.now()): boolean {
  return !!r.muted_until && Date.parse(r.muted_until) > now;
}

/** Labels of a rule, as `key=value` pairs. */
export function labelPairs(labels: Record<string, string> | undefined): string[] {
  return Object.entries(labels ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}=${v}`);
}

/** Why an event was not sent, in a sentence. */
export function suppressedText(reason: string): string {
  if (!reason) return "";
  if (reason === "muted") return "Not sent: the rule was muted.";
  if (reason.startsWith("silence: ")) return `Not sent: held back by the silence “${reason.slice(9)}”.`;
  if (reason.startsWith("depends on: ")) return `Not sent: “${reason.slice(12)}”, which this rule depends on, was firing.`;
  if (reason === "resolved notices are off") return "Not sent: this rule does not notify when it resolves.";
  if (reason === "its firing was not sent") return "Not sent, because its firing was not sent either.";
  return `Not sent: ${reason}.`;
}

/** Starting points for new rules, shown above the form. */
export interface RulePreset {
  id: string;
  group: string;
  label: string;
  name: string;
  severity: Severity;
  kind?: RuleKind;
  spec: RuleSpec;
  runbook?: string;
}

const th = (metric: string, op: string, threshold: number, window: string, hold: string, extra: Partial<RuleSpec> = {}): RuleSpec => ({
  metric,
  scope: {},
  op,
  threshold,
  window,
  for: hold,
  repeat: "",
  ...extra,
});

const anomaly = (metric: string, direction: string, sensitivity = "medium", window = "15m"): RuleSpec => ({
  metric,
  scope: {},
  threshold: 0,
  window,
  for: "10m",
  sensitivity,
  direction,
});

const containerEvent = (kind: string, threshold: number, window: string): RuleSpec => ({ metric: "container_events", scope: { event_kind: kind }, op: ">=", threshold, window, for: "0s", repeat: "" });

const finding = (scope: Scope, threshold: number, window: string, repeat = ""): RuleSpec => ({ metric: "security_events", scope, op: ">=", threshold, window, for: "0s", repeat });

export const RULE_PRESETS: RulePreset[] = [
  // Requests
  { id: "5xx", group: "Requests", label: "High 5xx error rate", name: "5xx error rate above 2%", severity: "critical", spec: th("error_rate_5xx", ">", 2, "5m", "2m", { clear_for: "5m" }) },
  { id: "5xx_spike", group: "Requests", label: "Unusual rise in 5xx errors", name: "5xx errors unusually high", severity: "warning", kind: "anomaly", spec: anomaly("error_rate_5xx", "up") },
  { id: "p95", group: "Requests", label: "Slow responses (p95 above 1 s)", name: "p95 latency above 1 s", severity: "warning", spec: th("latency_p95", ">", 1000, "5m", "5m", { clear_threshold: 800 }) },
  { id: "p99", group: "Requests", label: "Very slow tail (p99 above 2.5 s)", name: "p99 latency above 2.5 s", severity: "warning", spec: th("latency_p99", ">", 2500, "10m", "5m") },
  { id: "latency_anomaly", group: "Requests", label: "Latency unusual for this time of week", name: "p95 latency unusually high", severity: "warning", kind: "anomaly", spec: anomaly("latency_p95", "up") },
  { id: "apdex", group: "Requests", label: "Apdex below 0.85", name: "Apdex below 0.85", severity: "warning", spec: th("apdex", "<", 0.85, "10m", "5m") },
  { id: "traffic_drop", group: "Requests", label: "Traffic dropped unusually", name: "Traffic unusually low", severity: "critical", kind: "anomaly", spec: anomaly("requests_per_min", "down", "medium", "15m") },
  { id: "traffic_spike", group: "Requests", label: "Traffic spike", name: "Traffic unusually high", severity: "info", kind: "anomaly", spec: anomaly("requests_per_min", "up", "low", "15m") },
  { id: "no_traffic", group: "Requests", label: "No traffic at all", name: "No requests in 10 minutes", severity: "critical", spec: th("request_count", "<", 1, "10m", "0s", { no_data: "alerting" }) },
  { id: "4xx", group: "Requests", label: "Many client errors (4xx above 10%)", name: "4xx error rate above 10%", severity: "info", spec: th("error_rate_4xx", ">", 10, "10m", "5m") },
  { id: "429", group: "Requests", label: "Clients being rate limited", name: "Over 5% of requests rate limited", severity: "warning", spec: th("rate_limited", ">", 5, "10m", "5m") },
  { id: "frustrated", group: "Requests", label: "Frustrated users (slow or failing)", name: "Over 10% of requests frustrated", severity: "warning", spec: th("frustrated_requests", ">", 10, "10m", "5m") },
  { id: "n_plus_one", group: "Requests", label: "Database calls per request jumped (N+1)", name: "Over 20 database calls per request", severity: "warning", spec: th("db_calls_avg", ">", 20, "15m", "5m") },
  { id: "slo", group: "Requests", label: "SLO error budget burning fast", name: "SLO 99.9% burning", severity: "critical", kind: "slo", spec: { metric: "slo", scope: {}, threshold: 0, window: "", for: "0s", target: 99.9 } },

  // Errors
  { id: "exceptions_spike", group: "Errors", label: "Exceptions spiking", name: "Exceptions unusually high", severity: "warning", kind: "anomaly", spec: anomaly("exceptions", "up") },
  { id: "unhandled", group: "Errors", label: "Unhandled exception (crash)", name: "Unhandled exception", severity: "critical", spec: th("unhandled_exceptions", ">", 0, "5m", "0s", { repeat: "1h" }) },
  { id: "new_errors", group: "Errors", label: "New type of error appeared", name: "New error type", severity: "warning", spec: th("new_error_types", ">", 0, "10m", "0s") },

  // System
  { id: "instance_down", group: "System", label: "Instance stopped reporting", name: "Instance stopped reporting", severity: "critical", spec: th("instance_silent", ">", 5, "5m", "0s", { repeat: "1h" }) },
  { id: "cpu", group: "System", label: "Host CPU above 85%", name: "Host CPU above 85%", severity: "warning", spec: th("host_cpu", ">", 85, "10m", "10m", { clear_threshold: 75 }) },
  { id: "memory", group: "System", label: "Memory above 90%", name: "Memory above 90%", severity: "warning", spec: th("memory_percent", ">", 90, "5m", "10m") },
  { id: "disk", group: "System", label: "Disk almost full (above 90%)", name: "Disk above 90%", severity: "critical", spec: th("disk_used_percent", ">", 90, "5m", "0s", { repeat: "6h" }) },
  { id: "disk_free", group: "System", label: "Less than 5 GB free", name: "Less than 5 GB of disk free", severity: "critical", spec: th("disk_free_gb", "<", 5, "5m", "0s", { repeat: "6h" }) },
  { id: "inodes", group: "System", label: "Running out of inodes", name: "Inodes above 90%", severity: "warning", spec: th("disk_inodes_percent", ">", 90, "15m", "0s") },
  { id: "load", group: "System", label: "Load per core above 1.5", name: "Load per core above 1.5", severity: "warning", spec: th("load_per_core", ">", 1.5, "10m", "10m") },
  { id: "swap", group: "System", label: "Swapping (swap above 50%)", name: "Swap above 50%", severity: "warning", spec: th("swap_used_percent", ">", 50, "10m", "10m") },
  { id: "iowait", group: "System", label: "CPU waiting on disk (I/O wait above 20%)", name: "I/O wait above 20%", severity: "warning", spec: th("cpu_iowait", ">", 20, "10m", "10m") },
  { id: "steal", group: "System", label: "Noisy neighbour (CPU steal above 10%)", name: "CPU steal above 10%", severity: "info", spec: th("cpu_steal", ">", 10, "15m", "15m") },
  { id: "fds", group: "System", label: "File descriptors near the limit", name: "File descriptors above 80% of the limit", severity: "warning", spec: th("fd_usage_percent", ">", 80, "5m", "5m") },
  { id: "pool", group: "System", label: "Database pool nearly exhausted", name: "DB pool above 90% in use", severity: "warning", spec: th("db_pool_usage", ">", 90, "5m", "5m") },
  { id: "pool_waits", group: "System", label: "Requests waiting for a DB connection", name: "DB pool waits", severity: "warning", spec: th("db_pool_waits", ">", 10, "5m", "5m") },
  { id: "goroutines", group: "System", label: "Goroutine leak", name: "Goroutines above 10,000", severity: "warning", spec: th("goroutines", ">", 10000, "15m", "15m") },
  { id: "heap", group: "System", label: "Heap growing past 1 GB", name: "Heap above 1 GB", severity: "warning", spec: th("heap_inuse_mb", ">", 1024, "15m", "15m") },

  // Uptime
  { id: "uptime_down", group: "Uptime", label: "Uptime check down", name: "Uptime check down", severity: "critical", spec: th("uptime_failing", ">=", 2, "30m", "0s") },
  { id: "uptime_slow", group: "Uptime", label: "Uptime check slow (above 2 s)", name: "Uptime check slower than 2 s", severity: "warning", spec: th("uptime_latency", ">", 2000, "15m", "10m") },
  { id: "uptime_availability", group: "Uptime", label: "Availability below 99% over an hour", name: "Availability below 99%", severity: "warning", spec: th("uptime_availability", "<", 99, "1h", "0s") },
  { id: "cert", group: "Uptime", label: "TLS certificate expires within 14 days", name: "TLS certificate expiring", severity: "warning", spec: th("uptime_cert_days", "<", 14, "1h", "0s", { repeat: "24h" }) },

  // Frontend
  { id: "lcp", group: "Frontend", label: "Poor LCP (p75 above 4 s)", name: "LCP poor", severity: "warning", spec: th("web_lcp", ">", 4000, "30m", "15m") },
  { id: "inp", group: "Frontend", label: "Poor INP (p75 above 500 ms)", name: "INP poor", severity: "warning", spec: th("web_inp", ">", 500, "30m", "15m") },
  { id: "cls", group: "Frontend", label: "Poor CLS (p75 above 0.25)", name: "CLS poor", severity: "info", spec: th("web_cls", ">", 0.25, "30m", "15m") },
  { id: "client_errors", group: "Frontend", label: "Client errors spiking", name: "Client errors unusually high", severity: "warning", kind: "anomaly", spec: anomaly("client_errors", "up") },
  { id: "client_api", group: "Frontend", label: "API calls failing in browsers and apps", name: "Client API failures above 5%", severity: "critical", spec: th("client_api_failures", ">", 5, "10m", "5m") },

  // Dependencies
  { id: "db_slow", group: "Dependencies", label: "Slow database queries (p95 above 500 ms)", name: "Database queries slow", severity: "warning", spec: th("db_query_p95", ">", 500, "10m", "5m") },
  { id: "http_slow", group: "Dependencies", label: "Slow calls to other services", name: "Outbound HTTP slow", severity: "warning", spec: th("outbound_http_p95", ">", 2000, "10m", "5m") },
  { id: "dep_errors", group: "Dependencies", label: "Calls to dependencies failing", name: "Dependency calls failing", severity: "critical", spec: th("dependency_errors", ">", 5, "5m", "2m") },

  // Security
  { id: "bruteforce", group: "Security", label: "Brute-force attempt", name: "Brute-force sign-in attempt", severity: "critical", spec: finding({ event_kind: "auth_bruteforce" }, 1, "10m") },
  { id: "stuffing", group: "Security", label: "Credential stuffing", name: "Credential stuffing", severity: "critical", spec: finding({ event_kind: "credential_stuffing" }, 1, "10m") },
  { id: "scanner", group: "Security", label: "Scanner probing the API", name: "Scanner probing the API", severity: "warning", spec: finding({ event_kind: "scanner" }, 1, "15m") },
  { id: "critical", group: "Security", label: "Any critical finding", name: "Critical security finding", severity: "critical", spec: finding({ event_severity: "critical" }, 1, "10m") },
  { id: "failed_logins", group: "Security", label: "Failed logins spike", name: "Failed logins above 50 in 5 minutes", severity: "warning", spec: th("failed_logins", ">", 50, "5m", "0s") },
  { id: "ssh", group: "Security", label: "SSH brute force on a host", name: "SSH brute force", severity: "warning", spec: finding({ event_kind: "ssh_failed", source: "host" }, 20, "10m") },
  { id: "port", group: "Security", label: "New open port on a host", name: "New open port on a host", severity: "warning", spec: finding({ event_kind: "port_opened", source: "host" }, 1, "5m") },
  { id: "updates", group: "Security", label: "Pending security updates", name: "Pending security updates", severity: "info", spec: finding({ event_kind: "updates_pending", source: "host" }, 1, "1h", "24h") },

  // Containers
  { id: "ctr_restarts", group: "Containers", label: "Container restart loop", name: "Container restarting repeatedly", severity: "critical", spec: containerEvent("restarted", 3, "10m") },
  { id: "ctr_oom", group: "Containers", label: "Container killed out of memory", name: "Container out-of-memory kill", severity: "critical", spec: containerEvent("oom_killed", 1, "10m") },
  { id: "ctr_unhealthy", group: "Containers", label: "Container health check failing", name: "Container unhealthy", severity: "warning", spec: containerEvent("unhealthy", 1, "5m") },
  { id: "ctr_memory", group: "Containers", label: "Container near its memory limit", name: "Container memory above 90% of its limit", severity: "warning", spec: th("container_memory", ">", 90, "5m", "5m") },
  { id: "ctr_throttle", group: "Containers", label: "Container CPU throttled", name: "Container CPU throttled over 25%", severity: "warning", spec: th("container_throttled", ">", 25, "10m", "10m") },
];
