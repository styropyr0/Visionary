import { api, rangeQuery, type Result } from "./api";
import { useApp } from "./state";
import { useQuery, type QueryState } from "./useQuery";

/** One trace: its requests and calls in every service, its log lines and errors. */
export interface TraceResponse {
  trace_id: string;
  requests: Result;
  spans: Result;
  logs: Result;
  errors: Result;
}

export interface MapNode {
  id: string;
  /** "service", or the dependency's kind: db, http, cache, queue. */
  kind: string;
  name: string;
  requests: number;
  errors: number;
  p95: number | null;
}

export interface MapEdge {
  source: string;
  target: string;
  kind: string;
  calls: number;
  per_min: number;
  errors: number;
  error_rate: number;
  p95: number | null;
}

export interface ServiceMap {
  nodes: MapNode[];
  edges: MapEdge[];
}

/** What Search narrows traces by; empty strings are unset. */
export interface TraceFilters {
  service: string;
  route: string;
  span: string;
  min_ms: string;
  errors: string;
}

export const NO_FILTERS: TraceFilters = { service: "", route: "", span: "", min_ms: "", errors: "" };

export const tracesApi = {
  trace: (id: string, signal?: AbortSignal) => api<TraceResponse>(`/api/traces/${encodeURIComponent(id)}`, { signal }),
};

/** The address of one trace. */
export function traceHref(id: string): string {
  return `#/traces/${encodeURIComponent(id)}`;
}

/** The trace search with filters, from the service map or elsewhere. */
export function traceSearchHref(f: Partial<TraceFilters>): string {
  const p = new URLSearchParams({ tab: "search" });
  for (const [k, v] of Object.entries(f)) if (v) p.set(k, v);
  return `#/traces?${p}`;
}

/** Trace search over the dashboard range. */
export function useTraceSearch(f: TraceFilters): QueryState<Result> {
  const { resolved, tick } = useApp();
  const extra: Record<string, string> = {};
  for (const [k, v] of Object.entries(f)) if (v) extra[k] = v;
  const qs = rangeQuery(resolved.from, resolved.to, extra);
  return useQuery<Result>(`traces?${qs}#${tick}`, (signal) => api<Result>(`/api/traces?${qs}`, { signal }));
}

export function useServiceMap(): QueryState<ServiceMap> {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to);
  return useQuery<ServiceMap>(`trace-map?${qs}#${tick}`, (signal) => api<ServiceMap>(`/api/traces/map?${qs}`, { signal }));
}

export function serviceLabel(name: unknown): string {
  const s = String(name ?? "");
  return s || "unnamed service";
}
