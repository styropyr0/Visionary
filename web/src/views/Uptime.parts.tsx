import { AlertTriangle, CheckCircle2, Clock, PauseCircle, XCircle } from "lucide-react";
import { formatMs } from "../lib/format";
import { dayLevel, formatUptime, STATE_LABEL, type DayBar, type StateName } from "../lib/uptime";

const STATE_ICON = { up: CheckCircle2, degraded: AlertTriangle, down: XCircle, paused: PauseCircle, pending: Clock };

/** Current state as icon + word; color is never the only cue. */
export function StateBadge({ state, large }: { state: StateName; large?: boolean }) {
  const Icon = STATE_ICON[state];
  return (
    <span className={`up-state ${state} ${large ? "large" : ""}`}>
      <Icon size={large ? 16 : 14} aria-hidden />
      {STATE_LABEL[state]}
    </span>
  );
}

function dayTitle(b: DayBar): string {
  const d = new Date(`${b.day}T00:00:00Z`).toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
  if (b.checks === 0) return `${d}: no data`;
  const parts = [`${d}: ${formatUptime(b.uptime)} up`, `${b.checks.toLocaleString()} checks`];
  if (b.failed) parts.push(`${b.failed.toLocaleString()} failed`);
  if (b.degraded) parts.push(`${b.degraded.toLocaleString()} slow`);
  return parts.join(" · ");
}

/** One bar per UTC day, statuspage style; the tooltip carries the numbers. */
export function DayBars({ days, height = 24 }: { days: DayBar[]; height?: number }) {
  return (
    <div className="up-bars" style={{ height }} role="img" aria-label={`Daily uptime, last ${days.length} days`}>
      {days.map((b) => (
        <span key={b.day} className={dayLevel(b)} title={dayTitle(b)} />
      ))}
    </div>
  );
}

export function BarsLegend() {
  return (
    <div className="up-legend">
      <span>
        <i className="ok" /> No downtime
      </span>
      <span>
        <i className="minor" /> Under 1% down
      </span>
      <span>
        <i className="major" /> 1% or more down
      </span>
      <span>
        <i className="none" /> No data
      </span>
    </div>
  );
}

/** Response time over 24h; gaps where no check passed. */
export function Spark({ points }: { points: (number | null)[] }) {
  const w = 120;
  const h = 26;
  const vals = points.filter((p): p is number => p !== null);
  if (vals.length < 2) return <span className="dim">—</span>;
  const max = Math.max(...vals);
  const min = Math.min(...vals);
  const span = max - min || 1;
  const x = (i: number) => (i / (points.length - 1)) * w;
  const y = (v: number) => h - 3 - ((v - min) / span) * (h - 6);
  let d = "";
  let pen = false;
  points.forEach((p, i) => {
    if (p === null) {
      pen = false;
      return;
    }
    d += `${pen ? "L" : "M"}${x(i).toFixed(1)},${y(p).toFixed(1)}`;
    pen = true;
  });
  const lastIdx = points.length - 1 - [...points].reverse().findIndex((p) => p !== null);
  const last = points[lastIdx]!;
  return (
    <svg className="up-spark" viewBox={`0 0 ${w} ${h}`} width={w} height={h} aria-label={`Response time, last 24h: ${formatMs(min)} to ${formatMs(max)}`} role="img">
      <path d={d} fill="none" stroke="var(--text-3)" strokeWidth="1.5" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
      <circle cx={x(lastIdx)} cy={y(last)} r="2.5" fill="var(--accent)" />
    </svg>
  );
}
