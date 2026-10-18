import { ArrowDownRight, ArrowRight, ArrowUpRight } from "lucide-react";
import { splitUnit } from "../lib/format";
import { InfoTip } from "./InfoTip";

interface Props {
  label: string;
  value: string;
  description?: string;
  /** Fractional change vs the previous period; null when unknown. */
  delta: number | null;
  deltaLabel: string;
  /** Whether an increase is good, bad, or neither. */
  upIs: "good" | "bad" | "neutral";
  spark?: number[];
  loading?: boolean;
  /** Replaces the delta line, e.g. "avg 12% · peak 80%". */
  footer?: string;
}

export function StatTile({ label, value, description, delta, deltaLabel, upIs, spark, loading, footer }: Props) {
  const [num, unit] = splitUnit(value);
  let tone = "";
  if (delta !== null && Math.abs(delta) >= 0.005 && upIs !== "neutral") {
    const up = delta > 0;
    tone = (up && upIs === "good") || (!up && upIs === "bad") ? "good" : "bad";
  }
  const Arrow = delta === null || Math.abs(delta) < 0.005 ? ArrowRight : delta > 0 ? ArrowUpRight : ArrowDownRight;

  return (
    <section className="panel span-2" aria-busy={loading || undefined}>
      {loading && <div className="progress" aria-hidden />}
      <div className={`stat ${loading ? "refetching" : ""}`}>
        <div className="stat-label">
          {label}
          {description && (
            <InfoTip text={description} size={12} />
          )}
        </div>
        <div className="stat-value">
          {num}
          {unit && <span className="stat-unit">{unit}</span>}
        </div>
        <div className={`stat-delta ${tone}`}>
          {footer && delta === null ? (
            <span className="vs" style={{ marginLeft: 0 }}>
              {footer}
            </span>
          ) : delta === null ? (
            <span className="vs" style={{ marginLeft: 0 }}>
              No previous data
            </span>
          ) : (
            <>
              <Arrow size={13} aria-hidden />
              <span>{formatDelta(delta)}</span>
              <span className="vs">{deltaLabel}</span>
            </>
          )}
        </div>
        {spark && spark.length > 1 && <Sparkline points={spark} />}
      </div>
    </section>
  );
}

function formatDelta(d: number): string {
  const pct = Math.abs(d * 100);
  if (pct >= 1000) return `${d > 0 ? "+" : "−"}${Math.round(pct / 100) / 10}k%`;
  return `${d > 0 ? "+" : d < 0 ? "−" : ""}${pct.toFixed(pct >= 10 ? 0 : 1)}%`;
}

/** Trend line in the de-emphasis hue, with the latest point in the accent. */
function Sparkline({ points }: { points: number[] }) {
  const w = 200;
  const h = 28;
  const max = Math.max(...points, 0);
  const min = Math.min(...points, 0);
  const span = max - min || 1;
  const xy = points.map((v, i) => [(i / (points.length - 1)) * w, h - 3 - ((v - min) / span) * (h - 6)] as const);
  const d = xy.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join("");
  const [lx, ly] = xy[xy.length - 1];
  return (
    <svg className="stat-spark" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" aria-hidden width="100%">
      <path d={`${d}L${w},${h}L0,${h}Z`} fill="var(--accent)" opacity="0.08" />
      <path d={d} fill="none" stroke="var(--text-3)" strokeWidth="1.5" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
      <circle cx={lx} cy={ly} r="2.5" fill="var(--accent)" />
    </svg>
  );
}
