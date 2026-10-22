import { api, rangeQuery, type Result } from "./api";
import { useApp } from "./state";
import { useQuery, type QueryState } from "./useQuery";

export interface Release {
  service: string;
  version: string;
  deployed_at: string;
  instances: number;
}

export interface RegressionsResponse {
  releases: Release[];
  release: Release | null;
  before?: { from: string; to: string };
  after?: { from: string; to: string };
  statements?: Result;
}

export interface DependenciesResponse {
  hosts: Result;
  timeline: Result;
  errors: Result;
}

export interface BreakdownResponse {
  routes: Result;
  method?: string;
  route?: string;
  series?: Result;
  totals?: { requests: number; total_ms: number | null; db_ms: number | null; http_ms: number | null; other_ms: number | null; app_ms: number | null };
}

export interface ExplainFinding {
  severity: "warning" | "info";
  title: string;
  detail: string;
}

export interface ExplainRequest {
  id: number;
  created_at: string;
  target: string;
  statement: string;
  status: "pending" | "running" | "done" | "failed";
  claimed_by?: string;
  plan?: string;
  error?: string;
  finished_at?: string;
  findings?: ExplainFinding[];
}

export interface ProfilesResponse {
  profiles: Result;
  routes: { route: string; busy_ms: number }[];
}

export interface FlameNode {
  name: string;
  value: number;
  self: number;
  children?: FlameNode[];
}

export interface FlameResponse {
  root: FlameNode;
  profiles: number;
}

/** One insights endpoint over the dashboard range, refetched on refresh. */
export function useInsights<T = Result>(path: string | null, extra: Record<string, string> = {}): QueryState<T> {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, extra);
  return useQuery<T>(path === null ? null : `ins-${path}?${qs}#${tick}`, (signal) => api<T>(`/api/insights/${path}?${qs}`, { signal }));
}
