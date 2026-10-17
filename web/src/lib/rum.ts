import { api, rangeQuery, type Result, type StatsResponse } from "./api";
import { formatMs, toNumber } from "./format";
import { useApp } from "./state";
import { useQuery } from "./useQuery";

export type RumApp = "" | "web" | "android" | "ios";
export type Rating = "good" | "needs-improvement" | "poor";

export interface VitalInfo {
  name: "LCP" | "INP" | "CLS" | "FCP" | "TTFB";
  label: string;
  help: string;
  good: number;
  poor: number;
}

/** Google's thresholds: good up to `good`, poor above `poor`. */
export const VITALS: VitalInfo[] = [
  { name: "LCP", label: "Largest Contentful Paint", help: "When the main content has rendered.", good: 2500, poor: 4000 },
  { name: "INP", label: "Interaction to Next Paint", help: "How quickly the page responds to clicks, taps and keys.", good: 200, poor: 500 },
  { name: "CLS", label: "Cumulative Layout Shift", help: "How much the layout jumps while loading (unitless).", good: 0.1, poor: 0.25 },
  { name: "FCP", label: "First Contentful Paint", help: "When the first text or image appears.", good: 1800, poor: 3000 },
  { name: "TTFB", label: "Time to First Byte", help: "Server response time plus network, as the browser saw it.", good: 800, poor: 1800 },
];

export const RATING_LABEL: Record<Rating, string> = {
  good: "Good",
  "needs-improvement": "Needs improvement",
  poor: "Poor",
};

export function rate(name: string, value: number | null): Rating | null {
  const v = VITALS.find((x) => x.name === name);
  if (!v || value === null) return null;
  return value <= v.good ? "good" : value <= v.poor ? "needs-improvement" : "poor";
}

export function formatVital(name: string, value: number | null): string {
  if (value === null) return "—";
  return name === "CLS" ? value.toFixed(value < 0.01 ? 3 : 2) : formatMs(value);
}

/** A panel under /api/rum, following the time range, refresh and app filter. */
export function useRum<T = Result>(panel: string, app: RumApp, extra: Record<string, string> = {}) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, { app, ...extra });
  return useQuery<T>(`rum-${panel}?${qs}#${tick}`, (signal) => api<T>(`/api/rum/${panel}?${qs}`, { signal }));
}

export function useRumError(fingerprint: string | null) {
  const { resolved } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to);
  return useQuery<Result>(fingerprint ? `rum-error-${fingerprint}?${qs}` : null, (signal) =>
    api<Result>(`/api/rum/errors/${fingerprint}?${qs}`, { signal }),
  );
}

/** The first row of a result as name → number. */
export function firstRow(res: Result | undefined | null): Record<string, number> | null {
  if (!res || res.rows.length === 0) return null;
  const out: Record<string, number> = {};
  res.columns.forEach((c, i) => {
    const n = toNumber(res.rows[0][i]);
    if (n !== null) out[c] = n;
  });
  return out;
}

export interface VitalSummary {
  samples: number;
  p75: number | null;
  good: number;
  needs: number;
  poor: number;
}

/** The vitals panel keyed by vital name. */
export function vitalsByName(res: Result | undefined | null): Map<string, VitalSummary> {
  const out = new Map<string, VitalSummary>();
  if (!res) return out;
  const at = (r: unknown[], c: string) => toNumber(r[res.columns.indexOf(c)]);
  for (const r of res.rows) {
    out.set(String(r[res.columns.indexOf("name")]), {
      samples: at(r, "samples") ?? 0,
      p75: at(r, "p75"),
      good: at(r, "good") ?? 0,
      needs: at(r, "needs_improvement") ?? 0,
      poor: at(r, "poor") ?? 0,
    });
  }
  return out;
}

/** Keeps only the long-format rows whose series passes keep. */
export function filterSeries(res: Result | undefined, keep: (series: string) => boolean): Result | undefined {
  if (!res) return undefined;
  const si = res.columns.indexOf("series");
  return { ...res, rows: res.rows.filter((r) => keep(String(r[si]))) };
}

/** "https://app.example.com/trips/:id" → "/trips/:id", keeping the host when pages span several. */
export function pagePath(url: string): string {
  try {
    const u = new URL(url);
    return u.pathname || "/";
  } catch {
    return url;
  }
}

export type { StatsResponse };
