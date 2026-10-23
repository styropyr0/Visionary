import type { ReactNode } from "react";
import { AlertTriangle, CheckCircle2, XCircle } from "lucide-react";
import { formatPercent, titleCase, toNumber } from "../lib/format";
import "./Machine.css";

/** Pieces shared by the machine sections of System and the Containers view. */

export type HostStatus = "online" | "stale" | "offline";

const STATUS_ICON = { online: CheckCircle2, stale: AlertTriangle, offline: XCircle };
const STATUS_LABEL = { online: "Online", stale: "Stale", offline: "Offline" };

/** Status as icon + word; color is never the only cue. */
export function StatusBadge({ status, large }: { status: HostStatus; large?: boolean }) {
  const Icon = STATUS_ICON[status] ?? XCircle;
  return (
    <span className={`host-status ${status} ${large ? "large" : ""}`}>
      <Icon size={large ? 16 : 14} aria-hidden />
      {STATUS_LABEL[status] ?? titleCase(status)}
    </span>
  );
}

export function formatUptime(sec: number): string {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export const dim = <span className="dim">—</span>;
export const pct = (v: unknown) => (toNumber(v) === null ? dim : formatPercent(toNumber(v)!));

/** A percent with a small bar, red past 90 and amber past 75. */
export function Meter({ value }: { value: unknown }) {
  const n = toNumber(value);
  if (n === null) return dim;
  const level = n >= 90 ? "bad" : n >= 75 ? "warn" : "ok";
  return (
    <span className="host-meter">
      <span className="host-meter-bar" aria-hidden>
        <span className={level} style={{ width: `${Math.min(100, Math.max(0, n))}%` }} />
      </span>
      {formatPercent(n)}
    </span>
  );
}

export interface Fact {
  label: string;
  value: ReactNode;
  /** A second, quieter line under the value. */
  sub?: ReactNode;
  /** Exact value on hover, e.g. a full timestamp. */
  title?: string;
  mono?: boolean;
  tone?: "warn" | "bad";
}

/**
 * The head of a machine or container: what it is and its state on top, then
 * its facts as label-over-value cells that wrap on narrow screens.
 */
export function HeadCard({ icon, title, subtitle, badge, meta, facts }: { icon: ReactNode; title: string; subtitle: ReactNode; badge: ReactNode; meta?: ReactNode; facts: Fact[] }) {
  return (
    <section className="panel span-12 head-card">
      <div className="head-top">
        <div className="head-icon" aria-hidden>
          {icon}
        </div>
        <div className="head-text">
          <h2 className="head-title">{title}</h2>
          <div className="head-sub">{subtitle}</div>
        </div>
        <div className="head-state">
          {badge}
          {meta && <div className="head-meta">{meta}</div>}
        </div>
      </div>
      <dl className="head-facts">
        {facts.map((f) => (
          <div key={f.label} className={`head-fact ${f.tone ?? ""}`} title={f.title}>
            <dt>{f.label}</dt>
            <dd className={f.mono ? "mono" : undefined}>{f.value}</dd>
            {f.sub && <dd className="head-fact-sub">{f.sub}</dd>}
          </div>
        ))}
      </dl>
    </section>
  );
}

