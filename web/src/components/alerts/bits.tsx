import { AlertOctagon, BellOff, CheckCircle2, CircleDashed, Clock3, Info, PauseCircle, TriangleAlert } from "lucide-react";
import type { Severity, SeriesStateName } from "../../lib/alerts";

const STATE: Record<string, { label: string; cls: string; Icon: typeof CheckCircle2 }> = {
  firing: { label: "Firing", cls: "firing", Icon: AlertOctagon },
  pending: { label: "Pending", cls: "pending", Icon: Clock3 },
  resolved: { label: "Resolved", cls: "ok", Icon: CheckCircle2 },
  ok: { label: "OK", cls: "ok", Icon: CheckCircle2 },
  "": { label: "Not evaluated", cls: "idle", Icon: CircleDashed },
  disabled: { label: "Disabled", cls: "idle", Icon: PauseCircle },
  muted: { label: "Muted", cls: "idle", Icon: BellOff },
};

/** State with icon and label, so meaning never rests on color alone. */
export function StateBadge({ state }: { state: SeriesStateName | "disabled" | "muted" }) {
  const s = STATE[state] ?? STATE[""];
  return (
    <span className={`al-state ${s.cls}`}>
      <s.Icon size={13} aria-hidden />
      {s.label}
    </span>
  );
}

export function SeverityTag({ severity }: { severity: Severity }) {
  const Icon = severity === "critical" ? AlertOctagon : severity === "warning" ? TriangleAlert : Info;
  return (
    <span className={`al-sev ${severity}`}>
      <Icon size={12} aria-hidden />
      {severity}
    </span>
  );
}

/** Recent evaluations as a small line; the threshold, when given, is dashed. */
export function Sparkline({ points, threshold, width = 120, height = 28 }: { points: [number, number][]; threshold?: number | null; width?: number; height?: number }) {
  if (points.length < 2) return <span className="al-spark-empty">—</span>;
  const vs = points.map((p) => p[1]);
  const all = threshold != null ? [...vs, threshold] : vs;
  const lo = Math.min(...all, 0);
  const hi = Math.max(...all);
  const span = hi - lo || 1;
  const t0 = points[0][0];
  const t1 = points[points.length - 1][0];
  const x = (t: number) => ((t - t0) / (t1 - t0 || 1)) * width;
  const y = (v: number) => height - 2 - ((v - lo) / span) * (height - 4);
  const d = points.map((p, i) => `${i ? "L" : "M"}${x(p[0]).toFixed(1)},${y(p[1]).toFixed(1)}`).join("");
  return (
    <svg className="al-spark" viewBox={`0 0 ${width} ${height}`} width={width} height={height} aria-hidden>
      {threshold != null && <line x1={0} x2={width} y1={y(threshold)} y2={y(threshold)} className="al-spark-th" />}
      <path d={d} className="al-spark-line" />
    </svg>
  );
}

export function timeAgo(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 172800) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
