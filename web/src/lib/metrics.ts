import { api, rangeQuery, type Result, type Unit } from "./api";
import { useApp } from "./state";
import { useQuery } from "./useQuery";

export type MetricKind = "counter" | "gauge" | "histogram";

export interface MetricName {
  name: string;
  kind: MetricKind;
  unit: string;
  series: number;
  rows: number;
  /** Counters: the total in range; gauges: the latest reading; histograms: the mean. */
  value: number | null;
  last_seen: string;
  /** New series dropped over the per-name cap since the server started. */
  dropped?: number;
}

export interface MetricNames {
  metrics: MetricName[];
  services: string[];
}

export interface MetricLabels {
  name: string;
  kind: MetricKind | "";
  unit: string;
  series: number;
  labels: { key: string; values: string[]; series: number }[];
  truncated: boolean;
}

/** What to chart; the same fields are a dashboard panel's params and a custom_metric rule's query. */
export interface MetricQuery {
  name: string;
  agg?: string;
  /** A label key, or ":service" / ":instance". */
  group?: string;
  labels?: Record<string, string>;
}

export const AGG_LABEL: Record<string, string> = {
  sum: "Sum",
  avg: "Average",
  max: "Max",
  min: "Min",
  rate: "Rate per second",
  count: "Count",
  p50: "p50",
  p95: "p95",
  p99: "p99",
};

/** The aggregations that make sense for a kind, the default first. */
export function aggsFor(kind: MetricKind | ""): string[] {
  switch (kind) {
    case "counter":
      return ["sum", "rate", "avg", "max", "min"];
    case "histogram":
      return ["p95", "p50", "p99", "avg", "max", "min", "count", "rate", "sum"];
  }
  return ["avg", "sum", "max", "min"];
}

/** A metric's unit as a chart unit; anything else is drawn as a plain number. */
export function chartUnit(unit: string, agg: string): Unit {
  if (agg === "count" || agg === "rate") return agg === "count" ? "count" : "none";
  const u = unit.toLowerCase();
  if (u === "ms" || u === "milliseconds") return "ms";
  if (u === "by" || u === "bytes" || u === "byte") return "bytes";
  if (u === "%" || u === "percent") return "percent";
  return "none";
}

/** The URL params of /api/metrics/series and of a dashboard panel. */
export function queryParams(q: MetricQuery, service: string): Record<string, string> {
  const p: Record<string, string> = { name: q.name };
  if (q.agg) p.agg = q.agg;
  if (q.group) p.group = q.group;
  if (q.labels && Object.keys(q.labels).length) p.labels = JSON.stringify(q.labels);
  if (service) p.service = service;
  return p;
}

export function useMetricNames(service: string) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, service ? { service } : {});
  return useQuery<MetricNames>(`metric-names?${qs}#${tick}`, (signal) => api<MetricNames>(`/api/metrics/names?${qs}`, { signal }));
}

export function useMetricLabels(name: string | null, service: string) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, { name: name ?? "", ...(service ? { service } : {}) });
  return useQuery<MetricLabels>(name ? `metric-labels?${qs}#${tick}` : null, (signal) => api<MetricLabels>(`/api/metrics/labels?${qs}`, { signal }));
}

export function useMetricSeries(q: MetricQuery | null, service: string) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, q ? queryParams(q, service) : {});
  return useQuery<Result>(q ? `metric-series?${qs}#${tick}` : null, (signal) => api<Result>(`/api/metrics/series?${qs}`, { signal }));
}

/** Labels as typed in a filter box: region=eu, status=200. */
export function parseLabelText(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of text.split(",")) {
    const i = part.indexOf("=");
    if (i <= 0) continue;
    const k = part.slice(0, i).trim();
    if (k) out[k] = part.slice(i + 1).trim();
  }
  return out;
}

export function labelText(labels: Record<string, string> | undefined): string {
  return Object.entries(labels ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join(", ");
}
