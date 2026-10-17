import type { IssueStatus, IssueVersion } from "../../lib/telemetry";
import { shortVersion } from "../../lib/telemetry";
import "./telemetry.css";

const STATUS_TEXT: Record<IssueStatus, string> = { new: "New", regressed: "Regressed", ongoing: "Ongoing" };

const STATUS_HELP: Record<IssueStatus, string> = {
  new: "Has only occurred in the version it occurs in now (or, without versions, was first seen in this range).",
  regressed: "Occurred in an older version, was absent from the release before the current one, and is back.",
  ongoing: "Has occurred across several releases.",
};

export function IssueStatusBadge({ status, version }: { status: IssueStatus; version?: string }) {
  return (
    <span className={`issue-status ${status}`} title={STATUS_HELP[status]}>
      <span className="dot" aria-hidden />
      {STATUS_TEXT[status]}
      {version && status !== "ongoing" && <span className="mono dim">in {shortVersion(version)}</span>}
    </span>
  );
}

/** A small count trend for table rows, in the muted series hue. */
export function MiniSpark({ points, width = 96, height = 22 }: { points: number[]; width?: number; height?: number }) {
  if (points.length < 2) return <span className="dim">—</span>;
  const max = Math.max(...points, 1);
  const step = width / (points.length - 1);
  const xy = points.map((v, i) => [i * step, height - 2 - (v / max) * (height - 4)] as const);
  const d = xy.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join("");
  return (
    <svg className="mini-spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden>
      <path d={`${d}L${width},${height}L0,${height}Z`} fill="var(--accent)" opacity="0.12" />
      <path d={d} fill="none" stroke="var(--accent)" strokeWidth="1.25" strokeLinejoin="round" />
    </svg>
  );
}

export function VersionPills({ versions, current, max = 4 }: { versions: IssueVersion[]; current?: string; max?: number }) {
  const known = versions.filter((v) => v.version);
  if (known.length === 0) return <span className="dim">—</span>;
  const shown = known.slice(-max);
  return (
    <span className="version-pills">
      {known.length > shown.length && <span className="pill">+{known.length - shown.length}</span>}
      {shown.map((v) => (
        <span key={v.version} className={`pill ${v.version === current ? "current" : ""}`} title={`${v.version}: ${v.occurrences} occurrences`}>
          {shortVersion(v.version)}
        </span>
      ))}
    </span>
  );
}
