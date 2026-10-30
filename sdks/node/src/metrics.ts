import { createHash } from "node:crypto";
import type { Value } from "./store/sql.js";
import { formatTs } from "./util.js";

/**
 * Custom metrics: counters, gauges and histograms the application defines.
 * Values add up in memory and the started agent writes one metrics row per
 * series every interval (15 s by default). Recording never waits for the
 * database.
 *
 *     visionary.counter("orders_placed", { region: "eu" }).add(1);
 *     visionary.gauge("queue_depth", { queue: "emails" }).set(n);
 *     visionary.histogram("checkout_ms").observe(took);
 *
 * Each distinct label set is one series; past the cap (1000 series per metric
 * name) new ones are dropped and counted, so a label holding user ids cannot
 * fill the database.
 */

const MAX_NAME = 200;
const MAX_LABELS = 20;
const MAX_KEY = 100;
const MAX_VALUE = 200;

/** Histogram bucket upper bounds unless describeMetric sets others
 * (OpenTelemetry's defaults, which suit milliseconds). */
export const DEFAULT_BUCKETS: readonly number[] = [0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000];

export type Labels = Record<string, string | number | boolean | null | undefined>;

export interface MetricOptions {
  /** "ms", "bytes", "requests"… */
  unit?: string;
  /** Histogram bucket upper bounds. */
  buckets?: number[];
}

type Kind = "counter" | "gauge" | "histogram";

class Series {
  touched = false;
  total = 0;
  value = 0;
  readonly counts: number[];
  count = 0;
  sum = 0;
  min = 0;
  max = 0;

  constructor(
    readonly name: string,
    readonly kind: Kind,
    readonly unit: string,
    readonly labels: string,
    readonly id: string,
    readonly bounds: readonly number[],
  ) {
    this.counts = kind === "histogram" ? new Array(bounds.length + 1).fill(0) : [];
  }
}

/** A counter series. Rows hold the increase over each interval. */
export class Counter {
  constructor(private readonly s?: Series) {}

  /** Adds n (ignored unless positive and finite). */
  add(n: number): void {
    const s = this.s;
    if (!s || !(n > 0) || n === Infinity) return;
    s.total += n;
    s.touched = true;
  }

  inc(): void {
    this.add(1);
  }
}

/** A gauge series. Rows hold the last value, every interval once it was set. */
export class Gauge {
  constructor(private readonly s?: Series) {}

  set(v: number): void {
    const s = this.s;
    if (!s || !Number.isFinite(v)) return;
    s.value = v;
    s.touched = true;
  }

  /** Moves the gauge by delta, from 0 when it was never set. */
  add(delta: number): void {
    const s = this.s;
    if (!s || !Number.isFinite(delta)) return;
    s.value += delta;
    s.touched = true;
  }
}

/** A histogram series. Rows hold each interval's count, sum, min, max and
 * bucket counts. */
export class Histogram {
  constructor(private readonly s?: Series) {}

  observe(v: number): void {
    const s = this.s;
    if (!s || !Number.isFinite(v)) return;
    let i = 0;
    while (i < s.bounds.length && s.bounds[i]! < v) i++;
    s.counts[i]!++;
    if (s.count === 0 || v < s.min) s.min = v;
    if (s.count === 0 || v > s.max) s.max = v;
    s.count++;
    s.sum += v;
  }
}

const series = new Map<string, Series>();
const perName = new Map<string, number>();
let dropped = new Map<string, number>();
const options = new Map<string, MetricOptions>();
let maxSeries = 1000;
let enabled = true;

/** Set by the agent from its configuration (VISIONARY_METRICS, VISIONARY_METRICS_MAX_SERIES). */
export function configureMetrics(on: boolean, max: number): void {
  enabled = on;
  if (max > 0) maxSeries = max;
}

export function metricsLimit(): number {
  return maxSeries;
}

function get(name: string, kind: Kind, labels: Labels | undefined): Series | undefined {
  if (!enabled) return undefined;
  name = cleanName(name);
  if (!name) return undefined;
  const raw = Object.entries(labels ?? {}).map(([k, v]) => [k, v === null || v === undefined ? "" : String(v)] as const);
  raw.sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
  let key = `${name}\0${kind}`;
  for (const [k, v] of raw) key += `\0${k}\x01${v}`;
  const found = series.get(key);
  if (found) return found;
  const n = perName.get(name) ?? 0;
  if (n >= maxSeries) {
    dropped.set(name, (dropped.get(name) ?? 0) + 1);
    return undefined;
  }
  const [text, id] = metricSeries(name, Object.fromEntries(raw));
  const opts = options.get(name);
  const bounds = kind === "histogram" && opts?.buckets?.length ? opts.buckets : DEFAULT_BUCKETS;
  const s = new Series(name, kind, opts?.unit ?? "", text, id, bounds);
  series.set(key, s);
  perName.set(name, n + 1);
  return s;
}

/** The counter `name` with `labels`. Works before init(); keep the handle to
 * skip the lookup. */
export function counter(name: string, labels?: Labels): Counter {
  return new Counter(get(name, "counter", labels));
}

export function gauge(name: string, labels?: Labels): Gauge {
  return new Gauge(get(name, "gauge", labels));
}

export function histogram(name: string, labels?: Labels): Histogram {
  return new Histogram(get(name, "histogram", labels));
}

/** Sets a metric's unit and histogram buckets. Call it before the metric's
 * first use; series created earlier keep theirs. */
export function describeMetric(name: string, opts: MetricOptions): void {
  name = cleanName(name);
  if (!name) return;
  const o: MetricOptions = { unit: opts.unit };
  if (opts.buckets?.length) {
    o.buckets = [...new Set(opts.buckets.filter((b) => Number.isFinite(b)))].sort((a, b) => a - b);
  }
  options.set(name, o);
}

/**
 * Takes each series' interval as metrics rows (without service and
 * instance): counters and histograms that moved, and every gauge ever set.
 * Dropped series counts come back by metric name.
 */
export function collectMetrics(at: number): { rows: Value[][]; dropped: Map<string, number> } {
  const out: { id: string; row: Value[] }[] = [];
  const ts = formatTs(at);
  for (const s of series.values()) {
    const row: Value[] = [ts, s.name, s.kind, s.unit || null, s.labels, s.id];
    if (s.kind === "counter") {
      if (s.total <= 0) continue;
      row.push(s.total, null, null, null, null, null);
      s.total = 0;
    } else if (s.kind === "gauge") {
      if (!s.touched) continue;
      row.push(s.value, null, null, null, null, null);
    } else {
      if (s.count === 0) continue;
      row.push(s.sum / s.count, s.count, s.sum, s.min, s.max, JSON.stringify({ bounds: s.bounds, counts: s.counts }));
      s.count = s.sum = s.min = s.max = 0;
      s.counts.fill(0);
    }
    out.push({ id: s.id, row });
  }
  const d = dropped;
  if (d.size > 0) dropped = new Map();
  out.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  return { rows: out.map((o) => o.row), dropped: d };
}

/**
 * The canonical labels text of a series and its id: labels as a JSON object
 * with keys in byte order, and the first 16 hex digits of SHA-256 over name,
 * a newline and that text. Empty keys are dropped, keys and values are cut to
 * 100 and 200 bytes, and only the first 20 keys are kept.
 */
export function metricSeries(name: string, labels: Record<string, string>): [string, string] {
  const clean = new Map<string, string>();
  for (const [k, v] of Object.entries(labels)) {
    const key = cutBytes(k.trim(), MAX_KEY);
    if (key) clean.set(key, cutBytes(v, MAX_VALUE));
  }
  const keys = [...clean.keys()].sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b))).slice(0, MAX_LABELS);
  const text = "{" + keys.map((k) => jsonString(k) + ":" + jsonString(clean.get(k)!)).join(",") + "}";
  const id = createHash("sha256").update(name + "\n" + text, "utf8").digest("hex").slice(0, 16);
  return [text, id];
}

/** Escapes only what JSON requires, so every SDK produces the same bytes. */
function jsonString(s: string): string {
  let out = '"';
  for (const ch of s) {
    const c = ch.charCodeAt(0);
    if (ch === '"' || ch === "\\") out += "\\" + ch;
    else if (c < 0x20) out += "\\u00" + c.toString(16).padStart(2, "0");
    else out += ch;
  }
  return out + '"';
}

/** Cuts to n UTF-8 bytes on a character boundary; lone surrogates become U+FFFD. */
function cutBytes(s: string, n: number): string {
  const b = Buffer.from(s, "utf8");
  if (b.length <= n) return b.toString("utf8");
  let end = n;
  while (end > 0 && (b[end]! & 0xc0) === 0x80) end--;
  return b.subarray(0, end).toString("utf8");
}

function cleanName(name: string): string {
  return cutBytes(String(name ?? "").trim(), MAX_NAME);
}

/** Forgets every series (tests). */
export function resetMetrics(): void {
  series.clear();
  perName.clear();
  dropped = new Map();
  options.clear();
  maxSeries = 1000;
  enabled = true;
}
