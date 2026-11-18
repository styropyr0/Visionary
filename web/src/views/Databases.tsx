import { useEffect, useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, CheckCircle2, CornerDownRight, Database, Lock, XCircle } from "lucide-react";
import { api, rangeQuery, type Result, type Viz } from "../lib/api";
import { formatCount, formatMs, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { records } from "../lib/telemetry";
import { formatLogTime, formatRelative } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { DataTable, type Column } from "../components/DataTable";
import { ErrorNotice } from "../components/ErrorNotice";
import { InfoTip } from "../components/InfoTip";
import { dim, Meter } from "../components/Machine";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { Select } from "../components/Select";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { AgentSteps, SetupCard, SetupHelpButton, SetupOption } from "../components/SetupCard";
import { MiniSpark } from "../components/telemetry/bits";
import { QueryViewerLink } from "../components/telemetry/QueryViewerLink";
import { KIND_COLOR } from "../components/telemetry/Waterfall";
import "../components/telemetry/telemetry.css";
import "../components/telemetry/perf.css";
import "./Containers.css";
import "./Databases.css";

/** The server the page shows lives in the hash: #/databases?host=db-box&server=main. */
function readTarget(): { host: string; server: string } {
  const p = new URLSearchParams(window.location.hash.split("?")[1] ?? "");
  return { host: p.get("host") ?? "", server: p.get("server") ?? "" };
}

function writeTarget(host: string, server: string) {
  const q = server ? `?${new URLSearchParams({ host, server })}` : "";
  history.replaceState(null, "", `#/databases${q}`);
}

type Row = Record<string, unknown>;

const LIVE_EVERY = 15_000;

function useDBData(path: string, params: Record<string, string>, extra = "") {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, params);
  const sep = path.includes("?") ? "&" : "?";
  return useQuery<Result>(`db-${path}${sep}${qs}#${tick}${extra}`, (signal) => api<Result>(`/api/databases${path ? `/${path}` : ""}${sep}${qs}`, { signal }));
}

/** Bumps every 15 s while the tab is visible, so the live tables follow the agent's samples. */
function useLiveTick(): number {
  const [n, setN] = useState(0);
  useEffect(() => {
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") setN((x) => x + 1);
    }, LIVE_EVERY);
    return () => window.clearInterval(id);
  }, []);
  return n;
}

/** Seconds as "12.4 s", "4m 05s" or "2h 03m". */
export function formatSecs(v: unknown): string {
  const s = toNumber(v);
  if (s === null) return "—";
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)} s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(Math.floor(s % 60)).padStart(2, "0")}s`;
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, "0")}m`;
}

/** Elapsed time, amber past a minute and red past five. */
function Elapsed({ value }: { value: unknown }) {
  const s = toNumber(value) ?? 0;
  const tone = s >= 300 ? "host-bad" : s >= 60 ? "host-warn" : "";
  return <span className={`db-elapsed ${tone}`}>{formatSecs(value)}</span>;
}

function engineLabel(engine: unknown, version: unknown): string {
  const v = String(version ?? "");
  if (engine === "mysql") return /mariadb/i.test(v) ? `MariaDB ${v.split("-")[0]}` : `MySQL ${v}`.trim();
  return `PostgreSQL ${v}`.trim();
}

const STATUS: Record<string, { icon: typeof CheckCircle2; label: string; tone: string }> = {
  online: { icon: CheckCircle2, label: "Reading", tone: "online" },
  stale: { icon: AlertTriangle, label: "Stale", tone: "stale" },
  error: { icon: AlertTriangle, label: "Can't read", tone: "stale" },
  offline: { icon: XCircle, label: "Agent offline", tone: "offline" },
};

function DBStatus({ status }: { status: unknown }) {
  const s = STATUS[String(status)] ?? STATUS.offline;
  const Icon = s.icon;
  return (
    <span className={`host-status ${s.tone}`}>
      <Icon size={14} aria-hidden />
      {s.label}
    </span>
  );
}

function SQLText({ text, name }: { text: unknown; name?: unknown }) {
  if (!text) return dim;
  return (
    <div className="db-sql" title={String(text)}>
      {name ? <div className="mono span-name-main">{String(name)}</div> : null}
      <div className="mono dim span-detail">{String(text)}</div>
    </div>
  );
}

function StateLabel({ state, wait }: { state: unknown; wait?: unknown }) {
  const s = String(state ?? "");
  const idleTx = s.startsWith("idle in transaction");
  const w = String(wait ?? "");
  const waiting = w.startsWith("Lock") || (w.startsWith("Waiting") && /lock/i.test(w));
  const tone = waiting ? "bad" : idleTx ? "warn" : "ok";
  return (
    <span className={`ctr-state ${tone}`}>
      <span className="ctr-dot" aria-hidden />
      {waiting ? "waiting on a lock" : s}
    </span>
  );
}

export function Databases() {
  const [target, setTarget] = useState(readTarget);
  const list = useDBData("", {});
  const servers = records(list.data);
  const key = (r: Row) => `${r.host}\u0000${r.server}`;
  // One server opens on its own; with more the page starts on all of them.
  const selected = servers.find((r) => r.host === target.host && r.server === target.server) ?? (servers.length === 1 ? servers[0] : undefined);
  const pick = (k: string) => {
    const r = servers.find((x) => key(x) === k);
    const t = r ? { host: String(r.host), server: String(r.server) } : { host: "", server: "" };
    setTarget(t);
    writeTarget(t.host, t.server);
  };
  const params: Record<string, string> = selected ? { host: String(selected.host), server: String(selected.server) } : {};
  const withNotes = (selected ? [selected] : servers).filter((r) => r.notes);

  return (
    <>
      <TopBar
        title="Databases"
        section="System"
        actions={
          <>
            {servers.length > 1 && (
              <Select
                label="Database"
                icon={<Database size={14} />}
                value={selected ? key(selected) : ""}
                onChange={pick}
                options={[{ value: "", label: "All databases" }, ...servers.map((r) => ({ value: key(r), label: String(r.server) }))]}
                maxWidth={240}
              />
            )}
            <TimeRangePicker />
            <RefreshControl busy={list.loading} />
          </>
        }
      />
      <div className="content">
        {list.error && !list.data && <ErrorNotice title="Could not load databases" error={list.error} />}
        {list.data && servers.length === 0 && <DatabasesSetup />}
        {servers.length > 0 && (
          <div className="grid">
            {withNotes.map((r) => (
              <div key={key(r)} className="notice span-12 db-notes">
                <AlertTriangle size={14} aria-hidden />
                <div>
                  <strong>{String(r.server)}</strong>
                  {String(r.notes)
                    .split("\n")
                    .map((n) => (
                      <div key={n}>{n}</div>
                    ))}
                </div>
              </div>
            ))}
            {servers.length > 1 && !selected && <ServerTable result={list.data!} onPick={(r) => pick(key(r))} />}
            <Tiles servers={selected ? [selected] : servers} loading={list.loading} />
            <LiveSessions params={params} many={!selected && servers.length > 1} />
            <Statements params={params} many={!selected && servers.length > 1} />
            <RecentSessions params={params} many={!selected && servers.length > 1} />
            <Charts params={params} one={!!selected} />
          </div>
        )}
      </div>
    </>
  );
}

function DatabasesSetup() {
  return (
    <SetupCard
      icon={<Database size={20} />}
      title="No databases yet"
      text="The host agent reads your PostgreSQL and MySQL servers with a read-only user: what runs now and for how long, who blocks whom, which statements take the time, and connections against the limit."
      topic="dbactivity"
    >
      <SetupOption title="1. Install the host agent" text="On any machine that reaches the database, such as the database host or a bastion.">
        <AgentSteps />
      </SetupOption>
      <SetupOption title="2. Give it the databases" text="Create a monitoring user with the grants in Help, then set VISIONARY_AGENT_DATABASES to its postgres:// or mysql:// URL and restart the agent.">
        <SetupHelpButton topic="dbactivity">Grants and settings</SetupHelpButton>
      </SetupOption>
    </SetupCard>
  );
}

function ServerTable({ result, onPick }: { result: Result; onPick: (r: Row) => void }) {
  const multiHost = new Set(records(result).map((r) => r.host)).size > 1;
  const col = (n: string) => result.columns.indexOf(n);
  const columns: Column[] = [
    {
      key: "server",
      label: "Database",
      render: (v, row) => (
        <span className="host-name">
          <span className="db-server-name">{String(v)}</span>
          <span className="dim">
            {engineLabel(row[col("engine")], row[col("version")])}
            {multiHost ? ` · via ${String(row[col("host")])}` : ""}
          </span>
        </span>
      ),
    },
    { key: "status", render: (v) => <DBStatus status={v} /> },
    {
      key: "connections",
      label: "Connections",
      align: "right",
      render: (v, row) => {
        const max = toNumber(row[col("max_connections")]);
        return v == null ? dim : <span>{formatCount(toNumber(v) ?? 0)}{max ? <span className="dim"> / {formatCount(max)}</span> : null}</span>;
      },
    },
    {
      key: "max_connections",
      label: "Of limit",
      align: "right",
      render: (v, row) => {
        const c = toNumber(row[col("connections")]);
        const m = toNumber(v);
        return c === null || !m ? dim : <Meter value={(100 * c) / m} />;
      },
    },
    { key: "active", label: "Active", align: "right", render: (v) => (v == null ? dim : formatCount(toNumber(v) ?? 0)) },
    { key: "idle_in_tx", label: "Idle in transaction", align: "right", render: (v) => (v == null ? dim : toNumber(v) ? <span className="host-warn">{formatCount(toNumber(v)!)}</span> : <span className="dim">0</span>) },
    { key: "waiting", label: "Waiting", align: "right", render: (v) => (v == null ? dim : toNumber(v) ? <span className="host-bad">{formatCount(toNumber(v)!)}</span> : <span className="dim">0</span>) },
    { key: "longest_query_s", label: "Longest query", align: "right", render: (v) => (v == null ? dim : <Elapsed value={v} />) },
    { key: "last_seen", label: "Reported", align: "right", render: (v) => (v ? <span className="dim">{formatRelative(String(v))}</span> : dim) },
  ];
  return (
    <section className="panel span-12">
      <header className="panel-head">
        <h3 className="panel-title">Databases</h3>
      </header>
      <DataTable result={result} columns={columns} rowKey={(r) => `${r[0]}/${r[1]}`} onRowClick={(r) => onPick(Object.fromEntries(result.columns.map((c, i) => [c, r[i]])))} />
    </section>
  );
}

function Tiles({ servers, loading }: { servers: Row[]; loading: boolean }) {
  const sum = (k: string) => servers.reduce((a, r) => a + (toNumber(r[k]) ?? 0), 0);
  const peak = (k: string) => Math.max(0, ...servers.map((r) => toNumber(r[k]) ?? 0));
  const conns = sum("connections");
  const max = sum("max_connections");
  const one = servers.length === 1;
  return (
    <>
      <div className="span-3 host-tile">
        <StatTile
          label="Connections"
          description="Client sessions now, against max_connections."
          value={max ? `${formatCount(conns)} / ${formatCount(max)}` : formatCount(conns)}
          delta={null}
          deltaLabel=""
          upIs="neutral"
          loading={loading}
          footer={max ? `${((100 * conns) / max).toFixed(0)}% of the limit` : "Limit unknown"}
        />
      </div>
      <div className="span-3 host-tile">
        <StatTile
          label="Running now"
          description="Sessions running a statement at the latest sample."
          value={formatCount(sum("active"))}
          delta={null}
          deltaLabel=""
          upIs="neutral"
          loading={loading}
          footer={`${formatCount(sum("idle_in_tx"))} idle in a transaction`}
        />
      </div>
      <div className="span-3 host-tile">
        <StatTile
          label="Waiting on locks"
          description="Sessions waiting for a lock another session holds, at the latest sample."
          value={formatCount(sum("waiting"))}
          delta={null}
          deltaLabel=""
          upIs="bad"
          loading={loading}
          footer={`${formatCount(sum("deadlocks"))} deadlocks in this range`}
        />
      </div>
      <div className="span-3 host-tile">
        <StatTile
          label="Longest query"
          description={one ? "The statement that has run longest, at the latest sample." : "The longest-running statement on any of these databases."}
          value={formatSecs(peak("longest_query_s"))}
          delta={null}
          deltaLabel=""
          upIs="bad"
          loading={loading}
          footer={`Oldest transaction ${formatSecs(peak("longest_tx_s"))}`}
        />
      </div>
    </>
  );
}

function sessionColumns(res: Result, many: boolean, live: boolean): Column[] {
  const col = (n: string) => res.columns.indexOf(n);
  return [
    { key: "pid", label: "PID", width: 70, render: (v) => <span className="mono">{String(v)}</span> },
    ...(many ? [{ key: "server", label: "Database", render: (v: unknown) => <span className="mono">{String(v)}</span> }] : []),
    { key: "state", render: (v, row) => <StateLabel state={v} wait={row[col("wait_event")]} /> },
    { key: "elapsed_s", label: live ? "Running for" : "Ran for at least", align: "right", width: 110, render: (v) => <Elapsed value={v} /> },
    { key: "query", label: "Statement", render: (v, row) => <SQLText text={v} name={row[col("name")]} /> },
    {
      key: "username",
      label: "User",
      render: (v, row) => (
        <span className="db-who">
          <span>{v ? String(v) : "—"}</span>
          {row[col("db_name")] ? <span className="dim">{String(row[col("db_name")])}</span> : null}
        </span>
      ),
    },
    {
      key: "client",
      label: "Client",
      render: (v, row) => (
        <span className="db-who">
          <span className="mono">{v ? String(v) : "local"}</span>
          {row[col("app")] ? <span className="dim">{String(row[col("app")])}</span> : null}
        </span>
      ),
    },
    { key: "wait_event", label: "Waits on", render: (v) => (v ? <span className="mono db-wait">{String(v)}</span> : dim) },
    { key: "blocked_by", label: "Blocked by", render: (v) => (v ? <span className="mono host-bad">{String(v).split(",").join(", ")}</span> : dim) },
    { key: "tx_age_s", label: "Transaction", align: "right", render: (v) => (v == null ? dim : formatSecs(v)) },
    ...(live ? [] : [{ key: "seen_at", label: "Last seen", align: "right" as const, render: (v: unknown) => <span className="mono dim">{formatLogTime(String(v))}</span> }]),
  ];
}

function LiveSessions({ params, many }: { params: Record<string, string>; many: boolean }) {
  const live = useLiveTick();
  const q = useDBData("sessions?live=1", params, `#${live}`);
  const rows = records(q.data);
  return (
    <>
      <Section title="Running now" text="The sessions running a statement, sitting idle inside a transaction, or holding a lock another waits for, at the agent's latest sample. It refreshes every 15 seconds. Values in statements are replaced with ?." />
      <BlockingTree rows={rows} many={many} />
      <Panel
        className="span-12"
        title="Live sessions"
        result={q.data}
        loading={q.loading}
        error={q.error}
        table={
          q.data &&
          (rows.length === 0 ? (
            <div className="panel-state">Nothing running at the latest sample.</div>
          ) : (
            <DataTable result={q.data} columns={sessionColumns(q.data, many, true)} maxHeight={520} rowKey={(r) => `${r[0]}/${r[1]}/${r[2]}`} />
          ))
        }
      />
    </>
  );
}

interface TreeNode {
  row: Row;
  children: TreeNode[];
}

/** Who blocks whom: each root holds a lock others wait for and waits on no one. */
export function blockingForest(rows: Row[]): TreeNode[] {
  const id = (r: Row) => `${r.host}\u0000${r.server}\u0000${r.pid}`;
  const byID = new Map(rows.map((r) => [id(r), r]));
  const kids = new Map<string, Row[]>();
  const blocked = new Set<string>();
  for (const r of rows) {
    for (const p of String(r.blocked_by ?? "").split(",").filter(Boolean)) {
      const parent = `${r.host}\u0000${r.server}\u0000${p}`;
      if (!byID.has(parent)) byID.set(parent, { host: r.host, server: r.server, pid: Number(p), state: "not in the sample" });
      kids.set(parent, [...(kids.get(parent) ?? []), r]);
      blocked.add(id(r));
    }
  }
  const build = (r: Row, seen: Set<string>): TreeNode => {
    const k = id(r);
    const next = new Set(seen).add(k);
    return { row: r, children: (kids.get(k) ?? []).filter((c) => !next.has(id(c))).map((c) => build(c, next)) };
  };
  const roots = [...kids.keys()].filter((k) => !blocked.has(k)).map((k) => byID.get(k)!);
  // Sessions only blocking each other (a deadlock in the making) have no root.
  if (roots.length === 0 && kids.size > 0) roots.push(byID.get([...kids.keys()][0])!);
  return roots.map((r) => build(r, new Set()));
}

function BlockingTree({ rows, many }: { rows: Row[]; many: boolean }) {
  const forest = useMemo(() => blockingForest(rows), [rows]);
  if (forest.length === 0) return null;
  const node = (n: TreeNode, depth: number): ReactNode => (
    <li key={`${n.row.server}/${n.row.pid}`}>
      <div className="db-tree-row" style={{ paddingLeft: depth * 22 }}>
        {depth > 0 ? <CornerDownRight size={14} className="db-tree-icon" aria-hidden /> : <Lock size={14} className="db-tree-icon root" aria-hidden />}
        <div className="db-tree-body">
          <div className="db-tree-head">
            <span className="mono">pid {String(n.row.pid)}</span>
            {many && <span className="dim">{String(n.row.server)}</span>}
            <StateLabel state={n.row.state} wait={depth > 0 ? n.row.wait_event : undefined} />
            {n.row.elapsed_s != null && <Elapsed value={n.row.elapsed_s} />}
            {n.row.username ? <span className="dim">{String(n.row.username)}</span> : null}
            {depth > 0 && n.row.lock_mode ? (
              <span className="dim">
                wants {String(n.row.lock_mode)}
                {n.row.lock_object ? ` on ${String(n.row.lock_object)}` : n.row.lock_type ? ` (${String(n.row.lock_type)})` : ""}
              </span>
            ) : null}
          </div>
          {n.row.query ? <div className="mono dim db-tree-sql">{String(n.row.query)}</div> : null}
        </div>
      </div>
      {n.children.length > 0 && <ul>{n.children.map((c) => node(c, depth + 1))}</ul>}
    </li>
  );
  return (
    <section className="panel span-12">
      <header className="panel-head">
        <h3 className="panel-title">Blocking</h3>
        <InfoTip text="Each session with a lock icon holds what the sessions under it wait for. When it commits, rolls back or ends, the rest can go on." />
      </header>
      <ul className="db-tree">{forest.map((n) => node(n, 0))}</ul>
    </section>
  );
}

function RecentSessions({ params, many }: { params: Record<string, string>; many: boolean }) {
  const q = useDBData("sessions", params);
  return (
    <>
      <Section title="Long-running in this range" text="Statements and transactions that ran across at least one sample, longest first, one row per execution. Shorter statements count only in the statement stats above." />
      <Panel
        className="span-12"
        title="Longest executions"
        result={q.data}
        loading={q.loading}
        error={q.error}
        table={
          q.data &&
          (q.data.rows.length === 0 ? (
            <div className="panel-state">No statement ran across a sample in this range.</div>
          ) : (
            <DataTable result={q.data} columns={sessionColumns(q.data, many, false)} maxHeight={480} />
          ))
        }
      />
    </>
  );
}

function Statements({ params, many }: { params: Record<string, string>; many: boolean }) {
  const q = useDBData("statements", params);
  const columns = useMemo(() => (q.data ? statementColumns(q.data, many) : []), [q.data, many]);
  return (
    <>
      <Section
        title="Statements"
        text="What each normalized statement did in the range, from pg_stat_statements or performance_schema, read once per agent report. Sorted by total time; the trend is its time per interval. Max is the slowest call since the server's statistics were last reset."
      />
      <Panel
        className="span-12"
        title="Top statements by time"
        result={q.data}
        loading={q.loading}
        error={q.error}
        table={
          q.data &&
          (q.data.rows.length === 0 ? (
            <div className="panel-state">No statement stats in this range. A note above says why when the server does not allow them.</div>
          ) : (
            <DataTable result={q.data} columns={columns} maxHeight={640} rowKey={(r) => `${r[0]}/${r[1]}`} />
          ))
        }
      />
    </>
  );
}

function statementColumns(res: Result, many: boolean): Column[] {
  const col = (n: string) => res.columns.indexOf(n);
  const ti = col("total_ms");
  const max = Math.max(1, ...res.rows.map((r) => toNumber(r[ti]) ?? 0));
  const ms = (v: unknown) => (toNumber(v) === null ? dim : formatMs(toNumber(v)!));
  return [
    {
      key: "query",
      label: "Statement",
      render: (v, row) => (
        <div className="span-name">
          <span className="wf-swatch" style={{ background: KIND_COLOR.db }} aria-hidden />
          <div style={{ minWidth: 0 }}>
            <SQLText text={v ?? `(text not available) ${String(row[col("query_id")])}`} name={row[col("name")]} />
            {many && <div className="dim db-sub">{String(row[col("server")])}</div>}
          </div>
        </div>
      ),
    },
    { key: "calls", align: "right", unit: "count", width: 70 },
    { key: "per_min", label: "Per min", align: "right", width: 70, render: (v) => formatCount(toNumber(v) ?? 0) },
    {
      key: "total_ms",
      label: "Total time",
      align: "right",
      width: 130,
      render: (v) => (
        <span className="cell-bar">
          {formatMs(toNumber(v) ?? 0)}
          <span className="track" aria-hidden>
            <span className="fill" style={{ display: "block", width: `${(100 * (toNumber(v) ?? 0)) / max}%` }} />
          </span>
        </span>
      ),
    },
    { key: "mean_ms", label: "Mean", align: "right", width: 80, render: ms },
    { key: "max_ms", label: "Max", align: "right", width: 80, render: ms },
    { key: "row_count", label: "Rows", align: "right", width: 80, render: (v) => (v == null ? dim : formatCount(toNumber(v) ?? 0)) },
    { key: "share", label: "Share", align: "right", width: 70, render: (v) => (v == null ? dim : `${toNumber(v)!.toFixed(1)}%`) },
    { key: "trend", label: "Trend", width: 110, render: (v) => <MiniSpark points={Array.isArray(v) ? v.map((x) => Number(x) || 0) : []} /> },
    {
      key: "query_id",
      label: "",
      width: 170,
      render: (_v, row) => (row[col("query")] && row[col("name")] ? <QueryViewerLink name={String(row[col("name")])} detail={String(row[col("query")])} /> : null),
    },
  ];
}

function Charts({ params, one }: { params: Record<string, string>; one: boolean }) {
  const ts = (y: string[], unit: Viz["unit"]): Viz => (one ? { type: "timeseries", x: "t", y, unit } : { type: "timeseries", x: "t", y: ["value"], series: "series", unit });
  return (
    <>
      <Section title="Over time" text={one ? "From the agent's samples, every 15 seconds by default." : "One line per database. Pick one at the top for its detail."} />
      <DBChart path="connections" params={params} title="Connections" description={one ? "Client sessions, those running a statement and those idle in a transaction, against max_connections." : "Client sessions per database."} viz={ts(["connections", "active", "idle_in_transaction", "max_connections"], "count")} span="span-6" />
      <DBChart path="longest" params={params} title="Longest running" description="The longest-running statement, the oldest open transaction and the longest idle in one, per interval." viz={ts(["longest_query", "oldest_transaction", "longest_idle_in_transaction"], "ms")} span="span-6" />
      <DBChart path="waiting" params={params} title="Lock waits" description="Most sessions waiting on a lock at once, and deadlocks." viz={ts(["waiting_on_locks", "deadlocks"], "count")} span="span-6" />
      <DBChart path="transactions" params={params} title="Transactions" description="Commits and rollbacks per second (on MySQL, the storage engine's commits)." viz={ts(["commits", "rollbacks"], "count")} span="span-6" />
      <DBChart path="statement_time" params={params} title="Statement time" description="Time spent running statements per interval, from the statement stats." viz={ts(["total_ms"], "ms")} span="span-6" />
      <DBChart path="statement_calls" params={params} title="Statements per second" description="Statement calls per second, from the statement stats." viz={ts(["calls"], "count")} span="span-6" />
    </>
  );
}

function DBChart({ path, params, title, description, viz, span }: { path: string; params: Record<string, string>; title: string; description: string; viz: Viz; span: string }) {
  const tokens = useChartTokens();
  const q = useDBData(`panels/${path}`, params);
  return (
    <Panel
      className={span}
      title={title}
      description={description}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={220}
      note={vizNote(q.data, viz, tokens)}
      chart={q.data && <VizView result={q.data} viz={viz} height={220} title={title} />}
      table={q.data && <DataTable result={q.data} maxHeight={220} defaultUnit={viz.unit} />}
      dashboard={{ kind: "builtin", ref: `databases/panels/${path}`, viz, params, title: params.server ? `${params.server} · ${title}` : undefined }}
    />
  );
}
