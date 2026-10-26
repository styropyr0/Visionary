import { Select } from "./Select";

/** Spans offered, in seconds; each picker shows the ones within its bounds. */
const PRESETS = [10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800, 259200, 604800, 1209600, 2592000];

const UNITS: Record<string, number> = { ms: 0.001, s: 1, m: 60, h: 3600, d: 86400 };

/** Seconds in a duration such as 30s, 5m, 1h30m, 12h0m0s or 1d; 0 when empty or unreadable. */
export function spanSeconds(v: string | undefined): number {
  let total = 0;
  for (const [, n, unit] of (v ?? "").trim().matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h|d)/g)) total += Number(n) * UNITS[unit];
  return total;
}

/** The duration as the server reads it: 45s, 5m or 12h (Go durations have no days). */
function spanValue(sec: number): string {
  if (sec % 3600 === 0) return `${sec / 3600}h`;
  if (sec % 60 === 0) return `${sec / 60}m`;
  return `${sec}s`;
}

/** "1 hour 30 minutes", "7 days". */
function spanLabel(sec: number): string {
  const parts: string[] = [];
  for (const [n, unit] of [
    [86400, "day"],
    [3600, "hour"],
    [60, "minute"],
    [1, "second"],
  ] as [number, string][]) {
    const k = Math.floor(sec / n);
    if (k > 0) parts.push(`${k} ${unit}${k === 1 ? "" : "s"}`);
    sec -= k * n;
  }
  return parts.join(" ") || "0 seconds";
}

interface Props {
  value: string | undefined;
  onChange: (value: string) => void;
  label: string;
  /** Bounds in seconds; max defaults to 30 days. */
  min?: number;
  max?: number;
  /** Offers "" (no duration) under this label, such as "Never" or "At once". */
  empty?: string;
  maxWidth?: number;
  align?: "left" | "right";
  disabled?: boolean;
}

/** A dropdown of durations, so nobody has to type 5m or 12h. A stored value outside the presets stays listed. */
export function DurationSelect({ value, onChange, label, min = 0, max = 0, empty, maxWidth, align, disabled }: Props) {
  const sec = spanSeconds(value);
  const current = sec > 0 ? spanValue(sec) : "";
  const secs = PRESETS.filter((p) => p >= min && p <= (max || 2592000));
  if (sec > 0 && !secs.includes(sec)) secs.push(sec);
  secs.sort((a, b) => a - b);
  const options = secs.map((p) => ({ value: spanValue(p), label: spanLabel(p) }));
  if (empty !== undefined) options.unshift({ value: "", label: empty });
  return <Select label={label} value={current} options={options} onChange={onChange} maxWidth={maxWidth} align={align} disabled={disabled} />;
}
