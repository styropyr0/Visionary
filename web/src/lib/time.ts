export type RangeKey = "15m" | "1h" | "6h" | "24h" | "7d" | "30d";

export type TimeRange = { kind: "relative"; key: RangeKey } | { kind: "absolute"; from: string; to: string };

export const PRESETS: { key: RangeKey; label: string; ms: number }[] = [
  { key: "15m", label: "Last 15 minutes", ms: 15 * 60_000 },
  { key: "1h", label: "Last 1 hour", ms: 3_600_000 },
  { key: "6h", label: "Last 6 hours", ms: 6 * 3_600_000 },
  { key: "24h", label: "Last 24 hours", ms: 24 * 3_600_000 },
  { key: "7d", label: "Last 7 days", ms: 7 * 86_400_000 },
  { key: "30d", label: "Last 30 days", ms: 30 * 86_400_000 },
];

export const DEFAULT_RANGE: TimeRange = { kind: "relative", key: "24h" };

export interface Resolved {
  from: Date;
  to: Date;
}

export function resolveRange(r: TimeRange, now = new Date()): Resolved {
  if (r.kind === "absolute") {
    return { from: new Date(r.from), to: new Date(r.to) };
  }
  const preset = PRESETS.find((p) => p.key === r.key) ?? PRESETS[3];
  return { from: new Date(now.getTime() - preset.ms), to: now };
}

export function rangeLabel(r: TimeRange): string {
  if (r.kind === "relative") {
    return PRESETS.find((p) => p.key === r.key)?.label ?? "Custom";
  }
  return `${formatDateTime(new Date(r.from))} – ${formatDateTime(new Date(r.to))}`;
}

/** Previous-period label for stat deltas, e.g. "vs previous 24h". */
export function previousLabel(r: TimeRange): string {
  if (r.kind === "relative") return `vs previous ${r.key}`;
  return "vs previous period";
}

// How times are shown, from Settings → Preferences: this device's zone or
// UTC, and the clock. Set once before the first render (see main.tsx); a
// change reloads the page so every time is drawn again.
let utc = false;
let hour12: boolean | undefined;

export function setTimeDisplay(o: { utc?: boolean; clock?: "auto" | "12" | "24" }) {
  utc = !!o.utc;
  hour12 = o.clock === "12" ? true : o.clock === "24" ? false : undefined;
  formats.clear();
}

/** Times are shown in UTC (charts read this too). */
export function showsUTC(): boolean {
  return utc;
}

/** The zone times are shown in, for the analyst's questions. */
export function displayZone(): string {
  return utc ? "UTC" : Intl.DateTimeFormat().resolvedOptions().timeZone;
}

const formats = new Map<string, Intl.DateTimeFormat>();

/**
 * Every date the app shows goes through here, so the zone and clock
 * preferences apply everywhere. An explicit timeZone or hour12 in opts wins.
 */
export function dateFormat(opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = JSON.stringify(opts);
  let f = formats.get(key);
  if (!f) {
    const shown: Intl.DateTimeFormatOptions = { ...opts };
    if (utc && !opts.timeZone) shown.timeZone = "UTC";
    if (hour12 !== undefined && opts.hour && opts.hour12 === undefined) shown.hour12 = hour12;
    f = upperDayPeriod(new Intl.DateTimeFormat(undefined, shown));
    formats.set(key, f);
  }
  return f;
}

/** Some locales write "2:15 pm"; the app always shows AM and PM. */
function upperDayPeriod(f: Intl.DateTimeFormat): Intl.DateTimeFormat {
  const parts = (d?: Date | number) => f.formatToParts(d).map((p) => (p.type === "dayPeriod" ? { ...p, value: p.value.toUpperCase() } : p));
  // Intl methods only accept a real formatter as `this`, so the rest are bound to f.
  return {
    format: (d?: Date | number) => parts(d).map((p) => p.value).join(""),
    formatToParts: parts,
    formatRange: f.formatRange.bind(f),
    formatRangeToParts: f.formatRangeToParts.bind(f),
    resolvedOptions: f.resolvedOptions.bind(f),
  };
}

export function formatDateTime(d: Date): string {
  return dateFormat({ month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(d);
}

export function formatFull(d: Date): string {
  return dateFormat({ year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(d);
}

export function formatLogTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const ms = String(d.getMilliseconds()).padStart(3, "0");
  // Log lines default to a 24-hour clock, which lines up in a column.
  const f = dateFormat({ month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: hour12 ?? false });
  return `${f.format(d)}.${ms}`;
}

export function formatRelative(iso: string): string {
  const d = new Date(iso);
  const diff = Date.now() - d.getTime();
  if (Number.isNaN(diff)) return iso;
  const s = Math.round(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}

/** Value for <input type="datetime-local"> in local time. */
export function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const ISO_TIME = /^\d{4}-\d\d-\d\dT\d\d:\d\d(:\d\d(\.\d+)?)?(Z|[+-]\d\d:?\d\d)$/;


/**
 * Hover text for a cell: a stored timestamp becomes the viewer's full local
 * time with its zone; anything else is shown as it is.
 */
export function cellTitle(v: string): string {
  if (!ISO_TIME.test(v)) return v;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? v : dateFormat({ year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit", timeZoneName: "short" }).format(d);
}
