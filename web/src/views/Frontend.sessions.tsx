import { useMemo, useState } from "react";
import { Globe, Server } from "lucide-react";
import { api, rangeQuery, type Result } from "../lib/api";
import { formatCount, formatMs, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatLogTime, formatRelative } from "../lib/time";
import { Drawer } from "../components/Modal";
import { ErrorNotice } from "../components/ErrorNotice";
import { useSession } from "../lib/team";
import { asLevel } from "../lib/applogs";
import { LevelBadge, LogLineDrawer } from "./AppLogs";
import { LogDetail } from "./Logs";

interface TimelineItem {
  at: string;
  kind: string;
  side: "client" | "server";
  name?: string;
  message?: string;
  level?: string;
  url?: string;
  status?: number;
  duration_ms?: number;
  request_id?: string;
  log_id?: number;
  service?: string;
}

interface Timeline {
  session: Record<string, string>;
  items: TimelineItem[];
}

const KIND_LABEL: Record<string, string> = {
  page_load: "Page",
  screen: "Screen",
  api: "API call",
  error: "Error",
  web_vital: "Web Vital",
  custom: "Event",
  log: "Log",
  request: "Request",
  server_log: "Server log",
};

export function SessionsPanel({ app }: { app: string }) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, { app });
  const list = useQuery(`rum-sessions:${qs}:${tick}`, (signal) => api<Result>(`/api/rum/session-list?${qs}`, { signal }));
  const [open, setOpen] = useState<string | null>(null);
  const rows = useMemo(() => (list.data ? list.data.rows.map((r) => Object.fromEntries(list.data!.columns.map((c, i) => [c, r[i]]))) : []), [list.data]);
  return (
    <section className="panel span-12">
      <header className="panel-head">
        <h3 className="panel-title">Recent sessions</h3>
        <span className="dim fe-sessions-note">Open one for everything it did, with the server's requests and logs for its API calls.</span>
      </header>
      <div className="panel-body">
        {list.error ? (
          <ErrorNotice title="Could not load sessions" error={list.error} />
        ) : !list.data ? (
          <div className="panel-state">Loading…</div>
        ) : rows.length === 0 ? (
          <div className="panel-state">No sessions in this range.</div>
        ) : (
          <div className="table-wrap" style={{ maxHeight: 360 }}>
            <table className="data">
              <thead>
                <tr>
                  <th>Session</th>
                  <th>User</th>
                  <th>Device</th>
                  <th className="num">Events</th>
                  <th className="num">API calls</th>
                  <th className="num">Errors</th>
                  <th>Last active</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={String(r.session_id)} className="clickable" onClick={() => setOpen(String(r.session_id))}>
                    <td className="mono">{String(r.session_id).slice(0, 12)}</td>
                    <td className="mono">{r.user_id ? String(r.user_id) : <span className="dim">—</span>}</td>
                    <td>{String(r.device ?? "")}</td>
                    <td className="num">{formatCount(toNumber(r.events) ?? 0)}</td>
                    <td className="num">
                      {formatCount(toNumber(r.api_calls) ?? 0)}
                      {(toNumber(r.failed_calls) ?? 0) > 0 && <span className="fe-bad"> · {formatCount(toNumber(r.failed_calls) ?? 0)} failed</span>}
                    </td>
                    <td className="num">{(toNumber(r.errors) ?? 0) > 0 ? <span className="fe-bad">{formatCount(toNumber(r.errors) ?? 0)}</span> : 0}</td>
                    <td title={formatFull(new Date(String(r.last)))}>{formatRelative(String(r.last))}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {open && <SessionDrawer id={open} onClose={() => setOpen(null)} />}
    </section>
  );
}

function SessionDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const q = useQuery(`rum-session:${id}`, (signal) => api<Timeline>(`/api/rum/session/${encodeURIComponent(id)}`, { signal }));
  const { can } = useSession();
  const [line, setLine] = useState<number | null>(null);
  const [request, setRequest] = useState<number | null>(null);
  if (line !== null) return <LogLineDrawer id={line} canEdit={can("edit")} onClose={() => setLine(null)} />;
  if (request !== null) return <LogDetail id={request} onClose={() => setRequest(null)} />;
  const start = q.data ? new Date(q.data.session.started).getTime() : 0;
  return (
    <Drawer onClose={onClose} title={<span className="mono">Session {id.slice(0, 12)}</span>} className="wide">
      {q.error && <ErrorNotice title="Could not load the session" error={q.error} />}
      {!q.data && !q.error && <div className="panel-state">Loading…</div>}
      {q.data && (
        <>
          <dl className="kv">
            <dt>User</dt>
            <dd className="mono">{q.data.session.user_id ?? "Anonymous"}</dd>
            <dt>Device</dt>
            <dd>{[q.data.session.device, q.data.session.os].filter(Boolean).join(" · ") || "—"}</dd>
            <dt>Started</dt>
            <dd>{formatFull(new Date(q.data.session.started))}</dd>
          </dl>
          <ol className="fe-timeline">
            {q.data.items.map((it, i) => {
              const clickable = (it.kind === "request" && it.log_id) || ((it.kind === "log" || it.kind === "server_log") && it.log_id);
              const failed = (it.status ?? 0) >= 500 || it.kind === "error" || it.level === "error" || it.level === "fatal";
              return (
                <li
                  key={i}
                  className={`fe-tl-item side-${it.side} ${failed ? "bad" : ""} ${clickable ? "clickable" : ""}`}
                  onClick={() => {
                    if (!it.log_id) return;
                    if (it.kind === "request") setRequest(Number(it.log_id));
                    else if (it.kind === "log" || it.kind === "server_log") setLine(Number(it.log_id));
                  }}
                >
                  <span className="mono dim fe-tl-at" title={formatLogTime(it.at)}>
                    +{formatMs(Math.max(0, new Date(it.at).getTime() - start))}
                  </span>
                  <span className="fe-tl-side" title={it.side === "client" ? "In the app" : `On the server${it.service ? ` (${it.service})` : ""}`}>
                    {it.side === "client" ? <Globe size={13} /> : <Server size={13} />}
                  </span>
                  <span className="fe-tl-kind">{KIND_LABEL[it.kind] ?? it.kind}</span>
                  <span className="fe-tl-body">
                    {it.level && <LevelBadge level={asLevel(it.level)} />}
                    <span className="mono">{it.name || it.message || it.url}</span>
                    {it.name && it.message && <span className="dim"> {it.message}</span>}
                    {it.status !== undefined && it.status !== null && <span className={`fe-tl-status ${failed ? "fe-bad" : "dim"}`}> {String(it.status)}</span>}
                    {it.duration_ms !== undefined && it.duration_ms !== null && <span className="dim"> · {formatMs(Number(it.duration_ms))}</span>}
                  </span>
                </li>
              );
            })}
          </ol>
        </>
      )}
    </Drawer>
  );
}
