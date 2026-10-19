import type { ReactNode } from "react";
import { AlertTriangle, CheckCircle2, HardDrive, KeyRound, XCircle } from "lucide-react";
import { formatPercent, toNumber } from "../lib/format";
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
      {STATUS_LABEL[status] ?? status}
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

/** How to install the host agent, with this server's URL filled in. */
export function InstallAgent({ title, children }: { title: string; children: ReactNode }) {
  const url = window.location.origin;
  const systemd = `curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \\
  | sudo VISIONARY_URL=${url} VISIONARY_KEY=<server key> sh`;
  return (
    <section className="panel span-12">
      <div className="empty host-install">
        <div className="empty-icon">
          <HardDrive size={20} />
        </div>
        <h3>{title}</h3>
        <p>{children}</p>
        <ol className="host-steps">
          <li>
            <a href="#/settings?tab=keys">
              <KeyRound size={13} /> Create an API key
            </a>{" "}
            with the <b>server</b> scope in Settings (or use one from <code className="mono">VISIONARY_INGEST_KEYS</code>).
          </li>
          <li>
            Install the agent on the machine as a systemd service:
            <pre className="code mono">{systemd}</pre>
          </li>
        </ol>
        <p className="dim">Its first report arrives within a minute.</p>
      </div>
    </section>
  );
}
