import { api, rangeQuery, type Result } from "./api";
import { useApp } from "./state";
import { useQuery, type QueryState } from "./useQuery";

export interface TraceResponse {
  trace_id: string;
  requests: Result;
  spans: Result;
  logs: Result;
  errors: Result;
}

export interface MapNode {
  id: string;
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
  /** The window right before, when asked to compare. */
  previous?: ServiceMap;
}

export interface MapHealth {
  deploys: { version: string; at: string }[];
  incidents: { id: number; title: string; severity: string }[];
  alerts: { id: number; name: string; severity: string; slo: boolean }[];
  cpu: number | null;
  memory: number | null;
  container_memory: number | null;
}

/** [bucket start in epoch seconds, calls, failed calls] */
export type EdgePoint = [number, number, number];

export interface ServiceDetail {
  name: string;
  bucket: number;
  /** t, requests, errors, p95 */
  trend: Result;
  /** route, requests, errors, p95; slowest first */
  routes: Result;
  /** fingerprint, type, message, n, last_at */
  errors: Result;
  /** version, at */
  deploys: Result;
  /** instance, last_at */
  instances: Result;
}

export interface EdgeDetail {
  bucket: number;
  /** t, calls, errors, p95 */
  series: Result;
  /** route, calls, errors, p95 */
  routes: Result;
}

export const edgeKey = (e: Pick<MapEdge, "source" | "target">) => `${e.source}>${e.target}`;

/** The name a map node id carries after its kind, as the server writes them. */
export const nodeName = (id: string) => id.slice(id.indexOf(":") + 1);

export type ErrorRole = "origin" | "downstream";

/**
 * Where failures likely start. A service that fails while none of its calls
 * fail is a likely source; one whose calls fail is likely passing those on.
 * A dependency whose calls fail is a source itself.
 */
export function errorRoles(map: ServiceMap): Map<string, ErrorRole> {
  const out = new Map<string, ErrorRole>();
  const failingOut = new Set(map.edges.filter((e) => e.errors > 0).map((e) => e.source));
  for (const n of map.nodes) {
    if (n.kind === "service") {
      if (n.errors > 0) out.set(n.id, failingOut.has(n.id) ? "downstream" : "origin");
    } else if (map.edges.some((e) => e.target === n.id && e.errors > 0)) {
      out.set(n.id, "origin");
    }
  }
  return out;
}

export interface MapFilters {
  focus: string;
  hops: number;
  minRate: number;
  failing: boolean;
  kind: string;
}

export const NO_MAP_FILTERS: MapFilters = { focus: "", hops: 1, minRate: 0, failing: false, kind: "" };

/** The part of the map the filters keep: arrows first, then the boxes they touch. */
export function filterMap(map: ServiceMap, f: MapFilters): ServiceMap {
  let edges = map.edges.filter((e) => e.per_min >= f.minRate && (!f.failing || e.errors > 0) && (!f.kind || e.kind === f.kind));
  let keep: Set<string> | null = null;
  if (f.focus) {
    keep = new Set([f.focus]);
    let frontier = [f.focus];
    for (let i = 0; i < f.hops; i++) {
      const next: string[] = [];
      for (const e of edges) {
        for (const [a, b] of [
          [e.source, e.target],
          [e.target, e.source],
        ]) {
          if (frontier.includes(a) && !keep.has(b)) {
            keep.add(b);
            next.push(b);
          }
        }
      }
      frontier = next;
    }
    const k = keep;
    edges = edges.filter((e) => k.has(e.source) && k.has(e.target));
  }
  const filtered = f.minRate > 0 || f.failing || !!f.kind;
  const touched = new Set(edges.flatMap((e) => [e.source, e.target]));
  const nodes = map.nodes.filter((n) => (keep ? keep.has(n.id) : true) && (!filtered || touched.has(n.id) || n.id === f.focus));
  const ids = new Set(nodes.map((n) => n.id));
  const previous = map.previous && { nodes: map.previous.nodes.filter((n) => ids.has(n.id)), edges: map.previous.edges.filter((e) => ids.has(e.source) && ids.has(e.target)) };
  return { nodes, edges, previous };
}

/** The change from before as a share, null when there is nothing to compare. */
export function change(now: number | null, before: number | null | undefined): number | null {
  if (now === null || before === null || before === undefined || before === 0) return null;
  return (now - before) / before;
}

export const alertOnEdgeHref = (src: string, callee: string, metric = "edge_error_rate") => `#/alerts?${new URLSearchParams({ metric, service: src, callee })}`;

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

export function traceHref(id: string): string {
  return `#/traces/${encodeURIComponent(id)}`;
}

export function traceSearchHref(f: Partial<TraceFilters>): string {
  const p = new URLSearchParams({ tab: "search" });
  for (const [k, v] of Object.entries(f)) if (v) p.set(k, v);
  return `#/traces?${p}`;
}

export function useTraceSearch(f: TraceFilters): QueryState<Result> {
  const { resolved, tick } = useApp();
  const extra: Record<string, string> = {};
  for (const [k, v] of Object.entries(f)) if (v) extra[k] = v;
  const qs = rangeQuery(resolved.from, resolved.to, extra);
  return useQuery<Result>(`traces?${qs}#${tick}`, (signal) => api<Result>(`/api/traces?${qs}`, { signal }));
}

export function useServiceMap(compare = false): QueryState<ServiceMap> {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, compare ? { compare: "1" } : {});
  return useQuery<ServiceMap>(`trace-map?${qs}#${tick}`, (signal) => api<ServiceMap>(`/api/traces/map?${qs}`, { signal }));
}

export function useMapOverlay(services: string[]): QueryState<{ services: Record<string, MapHealth> }> {
  const { resolved, tick } = useApp();
  const list = [...services].sort().join(",");
  const qs = rangeQuery(resolved.from, resolved.to, { services: list });
  return useQuery(list ? `trace-map-overlay?${qs}#${tick}` : null, (signal) => api<{ services: Record<string, MapHealth> }>(`/api/traces/map/overlay?${qs}`, { signal }));
}

export function useMapSeries(): QueryState<{ bucket: number; edges: Record<string, EdgePoint[]> }> {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to);
  return useQuery(`trace-map-series?${qs}#${tick}`, (signal) => api<{ bucket: number; edges: Record<string, EdgePoint[]> }>(`/api/traces/map/series?${qs}`, { signal }));
}

export function useServiceDetail(name: string | null): QueryState<ServiceDetail> {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, { name: name ?? "" });
  return useQuery(name === null ? null : `trace-map-service?${qs}&n=${name}#${tick}`, (signal) => api<ServiceDetail>(`/api/traces/map/service?${qs}&name=${encodeURIComponent(name ?? "")}`, { signal }));
}

export function useEdgeDetail(e: MapEdge | null): QueryState<EdgeDetail> {
  const { resolved, tick } = useApp();
  const extra: Record<string, string> = e ? { src: nodeName(e.source), kind: e.kind, dst: nodeName(e.target) } : {};
  const qs = rangeQuery(resolved.from, resolved.to, extra);
  const p = new URLSearchParams(qs);
  // rangeQuery drops empty values; the unnamed service is an empty name
  if (e) for (const [k, v] of Object.entries(extra)) p.set(k, v);
  return useQuery(e ? `trace-map-edge?${p}#${tick}` : null, (signal) => api<EdgeDetail>(`/api/traces/map/edge?${p}`, { signal }));
}

export function serviceLabel(name: unknown): string {
  const s = String(name ?? "");
  return s || "unnamed service";
}
