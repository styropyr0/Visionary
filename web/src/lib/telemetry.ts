import { api, rangeQuery, type Result } from "./api";
import { useApp } from "./state";
import { useQuery, type QueryState } from "./useQuery";

export type IssueStatus = "new" | "regressed" | "ongoing";

export interface IssueVersion {
  version: string;
  first_seen: string;
  last_seen: string;
  occurrences: number;
  deployed_at?: string;
}

export interface Issue {
  fingerprint: string;
  type: string;
  message: string;
  method: string;
  route: string;
  service: string;
  occurrences: number;
  users: number;
  handled: boolean;
  first_seen: string;
  last_seen: string;
  status: IssueStatus;
  status_version?: string;
  versions: IssueVersion[];
  trend?: number[];
}

export interface IssuesResponse {
  issues: Issue[];
  counts: Partial<Record<IssueStatus, number>>;
  summary: { current: Result; previous: Result | null };
  timeline: Result;
  spark: { bucket: number; from: string };
  sql: string;
}

export interface IssueDetail {
  fingerprint: string;
  latest: Record<string, unknown>;
  first_seen: string;
  status: IssueStatus;
  status_version?: string;
  versions: IssueVersion[];
  stats: Result;
  timeline: Result;
  routes: Result;
  users: Result;
  recent: Result;
}

export interface RequestTelemetry {
  log_id: number | null;
  spans: Result;
  errors: Result;
}

export interface Release {
  service: string;
  version: string;
  deployed_at: string;
  instances: number;
  sdk: string;
  runtime: string;
}

export interface CompareResponse {
  overall: Result;
  routes: Result;
}

/** One telemetry endpoint over the dashboard range, refetched on refresh. */
export function useTelemetry<T = Result>(path: string | null, extra: Record<string, string> = {}): QueryState<T> {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, extra);
  return useQuery<T>(path === null ? null : `tel-${path}?${qs}#${tick}`, (signal) => api<T>(`/api/telemetry/${path}?${qs}`, { signal }));
}

/** A result's rows as objects keyed by column. */
export function records(res: Result | undefined): Record<string, unknown>[] {
  if (!res) return [];
  return res.rows.map((r) => {
    const o: Record<string, unknown> = {};
    res.columns.forEach((c, i) => (o[c] = r[i]));
    return o;
  });
}

/** Commit hashes read better short; other version strings stay as they are. */
export function shortVersion(v: string | null | undefined): string {
  if (!v) return "unknown";
  return /^[0-9a-f]{12,40}$/i.test(v) ? v.slice(0, 7) : v;
}

/** The Explore view with a question already asked. */
export function askAnalyst(question: string) {
  window.location.hash = `#/explore?q=${encodeURIComponent(question)}`;
}

/** Span names repeated this often in one request are flagged as N+1. */
export const N_PLUS_ONE = 10;
