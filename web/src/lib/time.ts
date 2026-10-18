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

const dtFmt = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

export function formatDateTime(d: Date): string {
  return dtFmt.format(d);
}

const fullFmt = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});

export function formatFull(d: Date): string {
  return fullFmt.format(d);
}

const logFmt = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

export function formatLogTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const ms = String(d.getMilliseconds()).padStart(3, "0");
  return `${logFmt.format(d)}.${ms}`;
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

const zoneFmt = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  timeZoneName: "short",
});

/**
 * Hover text for a cell: a stored timestamp becomes the viewer's full local
 * time with its zone; anything else is shown as it is.
 */
export function cellTitle(v: string): string {
  if (!ISO_TIME.test(v)) return v;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? v : zoneFmt.format(d);
}
