import { api, rangeQuery } from "./api";

export interface ApiExtract {
  var: string;
  source: "json" | "header" | "regex" | "status";
  path?: string;
}

export type AssertSource = "status" | "json" | "header" | "body" | "time";
export type AssertOp = "in" | "equals" | "not_equals" | "contains" | "not_contains" | "exists" | "not_exists" | "lt" | "lte" | "gt" | "gte" | "matches";

export interface ApiAssertion {
  source: AssertSource;
  path?: string;
  op: AssertOp;
  value?: string;
}

export interface ApiRequest {
  name?: string;
  method: string;
  url: string;
  /** Secret-looking literal values come back as MASK */
  headers?: Record<string, string>;
  body_type?: "" | "json" | "text" | "form";
  body?: string;
  timeout_ms?: number;
  extract?: ApiExtract[];
  assert?: ApiAssertion[];
}

export interface ApiConfig {
  requests: ApiRequest[];
}

export interface TcpConfig {
  host: string;
  port: number;
  send?: string;
  expect?: string;
}

export type DnsType = "A" | "AAAA" | "CNAME" | "MX" | "TXT" | "NS";

export interface DnsConfig {
  name: string;
  record_type: DnsType;
  server?: string;
  match?: "" | "contains" | "equals";
  values?: string[];
}

export interface PingConfig {
  host: string;
  packets: number;
  max_loss_pct: number;
}

export interface SslConfig {
  host: string;
  port: number;
  server_name?: string;
  warn_days: number;
  fail_days: number;
  min_tls?: "" | "1.0" | "1.1" | "1.2" | "1.3";
  ignore_hostname?: boolean;
  ignore_chain?: boolean;
}

export interface HeartbeatConfig {
  period_s?: number;
  cron?: string;
  timezone?: string;
  grace_s: number;
}

export interface ApiRequestResult {
  name: string;
  method: string;
  url: string;
  state: "passed" | "failed" | "skipped";
  status?: number;
  duration_ms?: number;
  size?: number;
  assertion?: string;
  got?: string;
  error?: string;
  excerpt?: string;
}

export interface RunDetail {
  requests?: ApiRequestResult[];
  // tcp
  address?: string;
  connect_ms?: number;
  banner?: string;
  // dns
  server?: string;
  type?: string;
  answers?: string[];
  resolve_ms?: number;
  // ping
  sent?: number;
  received?: number;
  loss_pct?: number;
  min_ms?: number | null;
  avg_ms?: number | null;
  max_ms?: number | null;
  rtts?: (number | null)[];
  method?: string;
  // ssl
  subject?: string;
  issuer?: string;
  names?: string[];
  not_before?: string;
  not_after?: string;
  days_left?: number;
  tls_version?: string;
  cipher?: string;
  chain?: string[];
  chain_valid?: boolean;
  chain_error?: string;
  hostname_valid?: boolean;
}

export interface DetailRun {
  created_at: string;
  ok: boolean;
  degraded: boolean;
  retried: boolean;
  status: number;
  duration_ms: number | null;
  error: string;
  detail: RunDetail;
  location: string;
}

export interface HeartbeatPing {
  created_at: string;
  kind: "success" | "start" | "fail";
  exit_code: number | null;
  message: string;
  duration_ms: number | null;
}

export interface HeartbeatView {
  last_ping: string;
  last_success: string;
  last_fail: string;
  last_start: string;
  next_expected: string;
  late_after: string;
  ok: boolean;
  waiting: boolean;
  reason: string;
  last_duration_ms: number | null;
  pings: HeartbeatPing[];
  path?: string;
}

export interface PingStatus {
  available: boolean;
  method?: string;
  error?: string;
  help?: string;
}

export const getDetails = (id: number, from: Date, to: Date, signal?: AbortSignal) =>
  api<{ runs: DetailRun[] }>(`/api/uptime/checks/${id}/details?${rangeQuery(from, to)}`, { signal });

export const getHeartbeat = (id: number, from: Date, to: Date, signal?: AbortSignal) =>
  api<HeartbeatView>(`/api/uptime/checks/${id}/heartbeat?${rangeQuery(from, to)}`, { signal });

export const newHeartbeatURL = (id: number) => api<{ path: string }>(`/api/uptime/checks/${id}/heartbeat/token`, { method: "POST", json: {} });

export const getPingStatus = (signal?: AbortSignal) => api<PingStatus>("/api/uptime/ping", { signal });

export const heartbeatURL = (path: string) => `${window.location.origin}${path}`;
