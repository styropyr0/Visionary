import type { Unit } from "./api";

const compactFmt = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });
const intFmt = new Intl.NumberFormat("en");
const smallFmt = new Intl.NumberFormat("en", { maximumSignificantDigits: 2 });

// Ticks below 1 (load per core, small rates) keep two significant digits; compact
// formatting would round them all to 0.
const tick = (n: number) => (Math.abs(n) < 1 ? smallFmt.format(n) : compactFmt.format(n));

export function toNumber(v: unknown): number | null {
  if (typeof v === "number") return Number.isFinite(v) ? v : null;
  if (typeof v === "string" && v.trim() !== "") {
    const n = Number(v);
    return Number.isFinite(n) ? n : null;
  }
  return null;
}

export function formatCount(n: number, compact = true): string {
  if (compact && Math.abs(n) >= 10_000) return compactFmt.format(n);
  if (Number.isInteger(n)) return intFmt.format(n);
  return n.toFixed(Math.abs(n) < 10 ? 2 : 1);
}

export function formatMs(n: number): string {
  if (Math.abs(n) >= 1000) return `${(n / 1000).toFixed(Math.abs(n) >= 10_000 ? 1 : 2)} s`;
  if (Math.abs(n) >= 100) return `${Math.round(n)} ms`;
  if (Math.abs(n) >= 10) return `${n.toFixed(1)} ms`;
  return `${n.toFixed(2)} ms`;
}

export function formatPercent(n: number): string {
  return `${n.toFixed(Math.abs(n) >= 10 ? 1 : 2)}%`;
}

export function formatBytes(n: number): string {
  const units = ["B", "KB", "MB", "GB"];
  let i = 0;
  let v = n;
  while (Math.abs(v) >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function formatValue(v: unknown, unit: Unit = "none"): string {
  const n = toNumber(v);
  if (n === null) return v === null || v === undefined ? "—" : String(v);
  switch (unit) {
    case "ms":
      return formatMs(n);
    case "percent":
      return formatPercent(n);
    case "bytes":
      return formatBytes(n);
    case "count":
      return formatCount(n);
    default:
      return formatCount(n, false);
  }
}

/** Axis ticks: always compact, so labels stay narrow. */
export function formatAxis(n: number, unit: Unit = "none"): string {
  switch (unit) {
    case "ms":
      return n >= 1000 ? `${compactFmt.format(n / 1000)}s` : `${tick(n)}ms`;
    case "percent":
      return `${tick(n)}%`;
    case "bytes":
      return formatBytes(n);
    default:
      return tick(n);
  }
}

/** Split a formatted value into number and unit so tiles can style the unit. */
export function splitUnit(text: string): [string, string] {
  const m = text.match(/^(-?[\d.,]+[KMBT]?)\s*(.*)$/);
  return m ? [m[1], m[2]] : [text, ""];
}

/** Guess a column's unit from its name, for tables and LLM results. */
export function unitForColumn(name: string, fallback: Unit = "none"): Unit {
  const n = name.toLowerCase();
  if (/(^|_)(ms|latency|duration|p\d{2}|avg_ms|p50|p95|p99)($|_)/.test(n) || n.endsWith("_ms")) return "ms";
  if (/(rate|pct|percent|share)($|_)/.test(n)) return "percent";
  if (/(size|bytes)($|_)/.test(n)) return "bytes";
  if (/(count|requests|errors|occurrences|users|hits|total|calls|^n$)/.test(n)) return "count";
  return fallback;
}

export function isTimeColumn(name: string): boolean {
  return /(^|_)(created_at|last_seen|first_seen|time|timestamp|at)$/.test(name.toLowerCase());
}

export function humanize(col: string): string {
  const special: Record<string, string> = {
    p50: "p50",
    p90: "p90",
    p95: "p95",
    p99: "p99",
    p50_ms: "p50",
    p95_ms: "p95",
    p99_ms: "p99",
    max_ms: "Max",
    avg_ms: "Avg",
    error_rate: "5xx rate",
    client_error_rate: "4xx rate",
    server_errors: "5xx",
    client_errors: "4xx",
    user_id: "User ID",
    t: "Time",
  };
  if (special[col]) return special[col];
  const s = col.replace(/_/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}
