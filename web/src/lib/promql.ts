import { ApiError, api, rangeQuery, type Result } from "./api";
import { useApp } from "./state";
import { useQuery } from "./useQuery";

/** The Prometheus API's base path; Grafana's data source URL is the server's origin followed by /api/prom. */
export const PROM_API = "/api/prom/api/v1";

export interface PromMeta {
  type: string;
  help: string;
  unit: string;
}

interface PromEnvelope<T> {
  status: string;
  data: T;
}

/** Where a query error points, from the server's 400 answer. */
export function errorPosition(err: Error | undefined): number | null {
  if (err instanceof ApiError && typeof err.data?.position === "number") return err.data.position;
  return null;
}

/** A PromQL query charted over the app's time range, one point per bucket. */
export function usePromChart(query: string) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, { query });
  return useQuery<Result>(query.trim() ? `promql?${qs}#${tick}` : null, (signal) => api<Result>(`/api/metrics/promql?${qs}`, { signal }));
}

const DAY = 24 * 3600 * 1000;

function metaRange(): string {
  const now = Date.now();
  return `start=${Math.floor((now - DAY) / 1000)}&end=${Math.floor(now / 1000)}`;
}

/** Metric names over the last day with their type and help, for completion. */
export function usePromNames() {
  // Names change slowly: refetch by the hour.
  const hour = Math.floor(Date.now() / 3_600_000);
  return useQuery<Record<string, PromMeta[]>>(`prom-meta-${hour}`, async (signal) => {
    const res = await api<PromEnvelope<Record<string, PromMeta[]>>>(`${PROM_API}/metadata?${metaRange()}`, { signal });
    return res.data;
  });
}

const labelCache = new Map<string, Promise<string[]>>();

/** Label names of a metric, or the values of one label; cached for the page's life. */
export function promLabels(metric: string, label?: string): Promise<string[]> {
  const key = `${metric}\n${label ?? ""}`;
  let p = labelCache.get(key);
  if (!p) {
    const match = `match[]=${encodeURIComponent(metric ? metric : '{__name__=~".+"}')}`;
    const path = label ? `${PROM_API}/label/${encodeURIComponent(label)}/values` : `${PROM_API}/labels`;
    p = api<PromEnvelope<string[]>>(`${path}?${match}&${metaRange()}`)
      .then((r) => r.data)
      .catch(() => {
        labelCache.delete(key);
        return [];
      });
    labelCache.set(key, p);
  }
  return p;
}

export const PROM_FUNCTIONS = [
  "rate",
  "irate",
  "increase",
  "delta",
  "idelta",
  "avg_over_time",
  "min_over_time",
  "max_over_time",
  "sum_over_time",
  "count_over_time",
  "last_over_time",
  "histogram_quantile",
  "abs",
  "ceil",
  "floor",
  "round",
  "clamp_min",
  "clamp_max",
  "clamp",
  "scalar",
  "vector",
  "absent",
  "sort",
  "sort_desc",
  "time",
];

export const PROM_AGGREGATIONS = ["sum", "avg", "min", "max", "count", "topk", "bottomk", "quantile", "stddev", "stdvar", "group"];

export const PROM_KEYWORDS = ["by", "without", "on", "ignoring", "group_left", "group_right", "bool", "offset", "and", "or", "unless"];

/** What the cursor is completing: a name, a label inside {…}, or a label's value. */
export type Completion =
  | { kind: "name"; prefix: string; from: number }
  | { kind: "label"; metric: string; prefix: string; from: number }
  | { kind: "value"; metric: string; label: string; prefix: string; from: number };

export function completionAt(text: string, cursor: number): Completion | null {
  const before = text.slice(0, cursor);
  const open = before.lastIndexOf("{");
  if (open > before.lastIndexOf("}")) {
    const metric = /([A-Za-z_:][\w:]*)\s*$/.exec(before.slice(0, open))?.[1] ?? "";
    const inside = before.slice(open + 1);
    const value = /([A-Za-z_]\w*)\s*(=~|!~|!=|=)\s*"([^"]*)$/.exec(inside);
    if (value) return { kind: "value", metric, label: value[1], prefix: value[3], from: cursor - value[3].length };
    const label = /(?:^|,)\s*([A-Za-z_]\w*)?$/.exec(inside);
    if (label) {
      const prefix = label[1] ?? "";
      return { kind: "label", metric, prefix, from: cursor - prefix.length };
    }
    return null;
  }
  if (before.lastIndexOf("[") > before.lastIndexOf("]") || (before.match(/"/g)?.length ?? 0) % 2 === 1) return null;
  const word = /[A-Za-z_:][\w:]*$/.exec(before);
  if (!word) return null;
  return { kind: "name", prefix: word[0], from: cursor - word[0].length };
}

/** The line holding a character position, and the column within it. */
export function lineAt(text: string, pos: number): { line: string; col: number; number: number } {
  const start = text.lastIndexOf("\n", Math.max(pos - 1, 0)) + 1;
  const end = text.indexOf("\n", pos);
  return { line: text.slice(start, end < 0 ? undefined : end), col: Math.min(pos, text.length) - start, number: text.slice(0, start).split("\n").length };
}
