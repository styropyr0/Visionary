import { formatBytes, formatMs } from "../lib/format";
import type { NetRequest } from "../lib/uptime";

const TYPE_COLOR: Record<string, string> = {
  Document: "var(--series-1)",
  Script: "var(--series-2)",
  Stylesheet: "var(--series-3)",
  Image: "var(--series-4)",
  Fetch: "var(--series-5)",
  XHR: "var(--series-5)",
  Font: "var(--series-6)",
};

const pathOf = (url: string) => {
  try {
    const u = new URL(url);
    return { host: u.host, path: u.pathname + u.search };
  } catch {
    return { host: "", path: url };
  }
};

export function Waterfall({ requests }: { requests: NetRequest[] }) {
  const end = Math.max(1, ...requests.map((r) => r.start_ms + r.duration_ms));
  return (
    <div className="up-wf" role="table" aria-label="Requests">
      <div className="up-wf-row up-wf-head" role="row">
        <span role="columnheader">Request</span>
        <span role="columnheader">Status</span>
        <span role="columnheader">Type</span>
        <span role="columnheader" className="num">
          Size
        </span>
        <span role="columnheader" className="num">
          Time
        </span>
        <span role="columnheader">0 – {formatMs(end)}</span>
      </div>
      {requests.map((r, i) => {
        const { host, path } = pathOf(r.url);
        const bad = r.failed || (r.status ?? 0) >= 400;
        return (
          <div key={i} className={`up-wf-row ${bad ? "bad" : ""}`} role="row" title={`${r.method} ${r.url}${r.error ? `\n${r.error}` : ""}`}>
            <span className="up-wf-url mono" role="cell">
              <span>{path || "/"}</span>
              <span className="dim">{host}</span>
            </span>
            <span role="cell" className="mono">
              {r.failed ? (r.error === "net::ERR_ABORTED" ? "aborted" : "failed") : r.status || "—"}
            </span>
            <span role="cell" className="dim">
              {r.type || "Other"}
            </span>
            <span role="cell" className="num dim">
              {r.size ? formatBytes(r.size) : "—"}
            </span>
            <span role="cell" className="num">
              {formatMs(r.duration_ms)}
            </span>
            <span role="cell" className="up-wf-track">
              <span
                className="up-wf-bar"
                style={{
                  left: `${(r.start_ms / end) * 100}%`,
                  width: `max(2px, ${(r.duration_ms / end) * 100}%)`,
                  background: bad ? "var(--critical)" : TYPE_COLOR[r.type ?? ""] ?? "var(--text-3)",
                }}
              />
            </span>
          </div>
        );
      })}
    </div>
  );
}
