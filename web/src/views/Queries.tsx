import { Fragment, useMemo, useState } from "react";
import { ConnectCard } from "../components/ConnectCard";
import { useConnected } from "../lib/meta";
import { ArrowRight, ChevronDown, ChevronRight } from "lucide-react";
import type { Result } from "../lib/api";
import { formatCount, formatMs, formatPercent, titleCase, toNumber } from "../lib/format";
import { useInsights, type RegressionsResponse } from "../lib/insights";
import { load, save } from "../lib/state";
import { records } from "../lib/telemetry";
import { formatLogTime, formatRelative } from "../lib/time";
import { DataTable, type Column } from "../components/DataTable";
import { InfoTip } from "../components/InfoTip";
import { Panel } from "../components/Panel";
import { Select } from "../components/Select";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { logIdFor } from "../components/telemetry/PerfPanels";
import { SpanDetailDrawer } from "../components/telemetry/SpanDetail";
import { KIND_COLOR } from "../components/telemetry/Waterfall";
import { LogDetail } from "./Logs";
import "../components/telemetry/telemetry.css";
import "../components/telemetry/perf.css";
import "./Insights.css";
import { HelpLink } from "../components/Help";

type Tab = "catalogue" | "depth" | "regressions" | "graphql";

const TABS: { key: Tab; label: string }[] = [
  { key: "catalogue", label: "Catalogue" },
  { key: "depth", label: "Per route" },
  { key: "regressions", label: "After a deploy" },
  { key: "graphql", label: "GraphQL" },
];

const TAB_KEY = "va.queries.tab";

/** What a span drawer shows: one statement of a name, or the whole name. */
interface Open {
  kind: string;
  name: string;
  detail?: string;
}

function ms(v: unknown) {
  const n = toNumber(v);
  return n === null ? <span className="dim">—</span> : formatMs(n);
}

/**
 * The database from the queries' side: every statement and its cost, how
 * many queries each route makes and how deep they sit, what a deploy changed,
 * and GraphQL operations with the queries their resolvers ran.
 */
export function Queries() {
  const connected = useConnected("requests");
  // "#/queries?tab=depth" opens a tab directly; otherwise the last one used.
  const [tab, setTabState] = useState<Tab>(() => {
    const fromLink = new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("tab") as Tab | null;
    const t = fromLink ?? load<Tab>(TAB_KEY, "catalogue");
    return TABS.some((x) => x.key === t) ? t : "catalogue";
  });
  const setTab = (t: Tab) => {
    setTabState(t);
    save(TAB_KEY, t);
  };
  const [open, setOpen] = useState<Open | null>(null);
  const [openLog, setOpenLog] = useState<number | null>(null);
  const openRequest = (requestId: string) => {
    logIdFor(requestId)
      .then((id) => id !== null && setOpenLog(id))
      .catch(() => undefined);
  };

  return (
    <>
      <TopBar
        title="Queries"
        section="API"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl />
          </>
        }
      />
      <div className="content">
        {connected ? (
          <>
        <nav className="tabs" role="tablist" aria-label="Queries">
          {TABS.map((t) => (
            <button type="button" key={t.key} role="tab" aria-selected={tab === t.key} className={tab === t.key ? "on" : ""} onClick={() => setTab(t.key)}>
              {t.label}
            </button>
          ))}
        </nav>
        <div className="grid" style={{ marginTop: 12 }}>
          {tab === "catalogue" && <Catalogue onOpen={setOpen} />}
          {tab === "depth" && <Depth />}
          {tab === "regressions" && <Regressions onOpen={setOpen} />}
          {tab === "graphql" && <GraphQL onOpen={setOpen} />}
        </div>
          </>
        ) : (
          <ConnectCard title="Connect your API to see its queries" text="Every statement your services run against their databases, how often and for how long, with the requests that ran them and their query plans." />
        )}
      </div>
      {open && <SpanDetailDrawer kind={open.kind} name={open.name} detail={open.detail} onClose={() => setOpen(null)} onOpenRequest={openRequest} />}
      {openLog !== null && <LogDetail id={openLog} onClose={() => setOpenLog(null)} />}
    </>
  );
}

// ---------------------------------------------------------------- catalogue

function Catalogue({ onOpen }: { onOpen: (o: Open) => void }) {
  const q = useInsights("queries", { kind: "db" });
  const columns = useMemo(() => (q.data ? catalogueColumns(q.data) : []), [q.data]);
  return (
    <Panel
      className="span-12"
      title="Every statement"
      description="Each distinct statement in the range, with values replaced by ?, sorted by the total time spent in it: a 5 ms query run 10,000 times costs more than one slow query. First and last seen are within the range; New means it did not run in the previous period. Open one for its timeline, routes, slowest calls and query plan."
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={200}
      table={
        q.data &&
        (q.data.rows.length === 0 ? (
          <div className="panel-state empty-why">
            <div>No database calls in this range.</div>
            <HelpLink topic="db">How to record queries</HelpLink>
          </div>
        ) : (
          <DataTable
            result={q.data}
            columns={columns}
            maxHeight={640}
            onRowClick={(row) => onOpen({ kind: "db", name: String(row[1]), detail: String(row[0]) })}
            rowKey={(row) => row[0]}
          />
        ))
      }
    />
  );
}

function catalogueColumns(res: Result): Column[] {
  const ti = res.columns.indexOf("total_ms");
  const max = Math.max(1, ...res.rows.map((r) => toNumber(r[ti]) ?? 0));
  const col = (name: string) => res.columns.indexOf(name);
  return [
    {
      key: "detail",
      label: "Statement",
      render: (v, row) => (
        <div className="span-name">
          <span className="wf-swatch" style={{ background: KIND_COLOR.db }} aria-hidden />
          <div style={{ minWidth: 0 }}>
            <div className="mono span-name-main">
              {String(row[col("name")])}
              {row[col("new")] === true && <span className="pill q-new">New</span>}
            </div>
            <div className="mono dim span-detail" title={String(v)}>
              {String(v)}
            </div>
          </div>
        </div>
      ),
    },
    { key: "calls", align: "right", unit: "count", width: 70 },
    { key: "per_min", label: "Per min", align: "right", render: (v) => formatCount(toNumber(v) ?? 0), width: 70 },
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
    { key: "p50", align: "right", render: ms, width: 70 },
    { key: "p95", align: "right", render: ms, width: 70 },
    { key: "max_ms", label: "Max", align: "right", render: ms, width: 70 },
    { key: "avg_rows", label: "Rows", align: "right", render: (v) => (v == null ? <span className="dim">—</span> : formatCount(toNumber(v) ?? 0)), width: 60 },
    {
      key: "errors",
      align: "right",
      width: 60,
      render: (v) => ((toNumber(v) ?? 0) > 0 ? <span className="flag bad">{formatCount(toNumber(v) ?? 0)}</span> : <span className="dim">0</span>),
    },
    {
      key: "routes",
      render: (v) => {
        const list = Array.isArray(v) ? (v as string[]) : [];
        return list.length === 0 ? (
          <span className="dim">background</span>
        ) : (
          <span className="span-routes" title={list.join("\n")}>
            {list.map((r) => (
              <span key={r} className="mono">
                {r}
              </span>
            ))}
          </span>
        );
      },
      width: 220,
    },
    {
      key: "last_seen",
      label: "Seen",
      width: 110,
      render: (v, row) => (
        <span className="dim" title={`First ${formatLogTime(String(row[col("first_seen")]))}\nLast ${formatLogTime(String(v))}`}>
          {formatRelative(String(v))}
        </span>
      ),
    },
  ];
}

// ---------------------------------------------------------------- depth

function Depth() {
  const q = useInsights("depth");
  return (
    <Panel
      className="span-12"
      title="Queries and depth per route"
      description="How many queries one request of each route makes (median, 95th percentile and most), how much of its time is database time, and how deep its calls sit. Depth counts the custom and GraphQL spans a query runs inside: 1 is a query made straight from the handler, 2 one inside a custom span, and so on; parallel calls are not nested. Without custom spans every route is at 1. Up and down arrows compare the 95th percentile of queries with the previous version seen in the range."
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={200}
      table={
        q.data &&
        (q.data.rows.length === 0 ? (
          <div className="panel-state empty-why">No requests with database calls in this range.</div>
        ) : (
          <DataTable result={q.data} columns={depthColumns(q.data)} maxHeight={640} />
        ))
      }
    />
  );
}

function depthColumns(res: Result): Column[] {
  const col = (name: string) => res.columns.indexOf(name);
  const shareBar = (v: unknown) => {
    const n = toNumber(v);
    if (n === null) return <span className="dim">—</span>;
    return (
      <span className="cell-bar">
        {formatPercent(n)}
        <span className="track" aria-hidden>
          <span className="fill" style={{ display: "block", width: `${Math.min(100, n)}%` }} />
        </span>
      </span>
    );
  };
  return [
    {
      key: "route",
      render: (v, row) => (
        <span>
          <span className="method">{String(row[col("method")])}</span> <span className="mono">{String(v)}</span>
        </span>
      ),
    },
    { key: "requests", align: "right", unit: "count", width: 80 },
    { key: "p50_queries", label: "Queries p50", align: "right", unit: "count", width: 90 },
    {
      key: "p95_queries",
      label: "p95",
      align: "right",
      width: 110,
      render: (v, row) => {
        const now = toNumber(v) ?? 0;
        const before = toNumber(row[col("prev_p95_queries")]);
        const prevVersion = row[col("prev_version")];
        const loop = now >= 10;
        let change = null;
        if (before !== null && before !== now) {
          const up = now > before;
          change = (
            <span className={`q-change ${up ? "up" : "down"}`} title={`${formatCount(before)} in ${String(prevVersion)}, ${formatCount(now)} in ${String(row[col("version")])}`}>
              {up ? "▲" : "▼"} {formatCount(before)}
            </span>
          );
        }
        return (
          <span className="q-cell">
            {change}
            <span className={loop ? "flag warn" : undefined}>{formatCount(now)}</span>
          </span>
        );
      },
    },
    { key: "max_queries", label: "Most", align: "right", unit: "count", width: 64 },
    { key: "db_share", label: "DB time", align: "right", render: shareBar, width: 120 },
    { key: "p95_db_share", label: "DB time p95", align: "right", render: shareBar, width: 120 },
    {
      key: "p95_depth",
      label: "Depth p95",
      align: "right",
      width: 90,
      render: (v, row) => {
        const n = toNumber(v);
        if (n === null) return <span className="dim">—</span>;
        return <span title={`Deepest ${formatCount(toNumber(row[col("max_depth")]) ?? 0)}, from ${formatCount(toNumber(row[col("depth_sampled")]) ?? 0)} recent requests`}>{formatCount(n)}</span>;
      },
    },
    { key: "avg_http", label: "HTTP calls", align: "right", render: (v) => formatCount(toNumber(v) ?? 0), width: 80 },
  ];
}

// ---------------------------------------------------------------- regressions

const STATUS_LABEL: Record<string, string> = { slower: "Slower", faster: "Faster", new: "New", gone: "Gone", steady: "Steady" };
const HOURS = [
  { value: "1", label: "1 hour each side" },
  { value: "6", label: "6 hours each side" },
  { value: "24", label: "24 hours each side" },
  { value: "72", label: "3 days each side" },
];

function Regressions({ onOpen }: { onOpen: (o: Open) => void }) {
  const [pick, setPick] = useState("");
  const [hours, setHours] = useState("24");
  const [service, version] = pick ? (JSON.parse(pick) as [string, string]) : ["", ""];
  const q = useInsights<RegressionsResponse>("regressions", { kind: "db", hours, ...(pick ? { service, version } : {}) });
  const d = q.data;
  const options = (d?.releases ?? []).map((r) => ({
    value: JSON.stringify([r.service, r.version]),
    label: `${r.service ? r.service + " " : ""}${r.version.slice(0, 12)} · ${formatLogTime(r.deployed_at)}`,
  }));
  const current = d?.release ? JSON.stringify([d.release.service, d.release.version]) : "";

  return (
    <Panel
      className="span-12"
      title="Statements before and after a deploy"
      description="Each statement's latency in equal windows before and after the deploy. Slower and Faster need at least 5 calls on each side and a change of 25% and 1 ms or more; New and Gone are statements that only ran on one side."
      result={d?.statements}
      loading={q.loading}
      error={q.error}
      bodyHeight={200}
      extraActions={
        options.length > 0 && (
          <>
            <Select label="Deploy" value={current} options={options} onChange={setPick} maxWidth={320} />
            <Select label="Window" value={hours} options={HOURS} onChange={setHours} />
          </>
        )
      }
      table={
        d &&
        (!d.release || !d.statements ? (
          <div className="panel-state empty-why">
            <div>No deploys recorded yet.</div>
            <HelpLink topic="release">How to record deploys</HelpLink>
          </div>
        ) : (
          <>
            {d.before && d.after && (
              <div className="q-windows dim">
                Before {formatLogTime(d.before.from)} – {formatLogTime(d.before.to)} <ArrowRight size={12} /> after {formatLogTime(d.after.from)} – {formatLogTime(d.after.to)}
              </div>
            )}
            <DataTable
              result={d.statements}
              columns={regressionColumns}
              maxHeight={600}
              sortable={false}
              onRowClick={(row) => onOpen({ kind: "db", name: String(row[0]) })}
              rowKey={(row) => row[0]}
            />
          </>
        ))
      }
    />
  );
}

const regressionColumns: Column[] = [
  {
    key: "name",
    label: "Statement",
    render: (v) => (
      <span className="span-name">
        <span className="wf-swatch" style={{ background: KIND_COLOR.db }} aria-hidden />
        <span className="mono">{String(v)}</span>
      </span>
    ),
  },
  { key: "status", width: 80, render: (v) => <span className={`q-status ${String(v)}`}>{STATUS_LABEL[String(v)] ?? titleCase(String(v))}</span> },
  {
    key: "p95_after",
    label: "p95 before → after",
    align: "right",
    width: 170,
    render: (v, row) => (
      <span>
        {ms(row[5])} <span className="dim">→</span> {ms(v)}
      </span>
    ),
  },
  {
    key: "change",
    align: "right",
    width: 80,
    render: (v) => {
      const n = toNumber(v);
      if (n === null) return <span className="dim">—</span>;
      return <span className={n >= 25 ? "flag bad" : n <= -25 ? "q-good" : undefined}>{`${n > 0 ? "+" : ""}${n.toFixed(0)}%`}</span>;
    },
  },
  {
    key: "calls_after",
    label: "Calls before → after",
    align: "right",
    width: 150,
    render: (v, row) => `${formatCount(toNumber(row[1]) ?? 0)} → ${formatCount(toNumber(v) ?? 0)}`,
  },
  {
    key: "errors_after",
    label: "Errors",
    align: "right",
    width: 90,
    render: (v, row) => {
      const b = toNumber(row[8]) ?? 0;
      const a = toNumber(v) ?? 0;
      if (a === 0 && b === 0) return <span className="dim">0</span>;
      return <span className={a > b ? "flag bad" : undefined}>{`${formatCount(b)} → ${formatCount(a)}`}</span>;
    },
  },
];

// ---------------------------------------------------------------- GraphQL

interface Statement {
  statement: string;
  calls: number;
  per_operation: number;
  total_ms: number;
}

function GraphQL({ onOpen }: { onOpen: (o: Open) => void }) {
  const q = useInsights("graphql");
  const [open, setOpen] = useState<Set<string>>(new Set());
  const rows = useMemo(() => records(q.data), [q.data]);
  const toggle = (k: string) =>
    setOpen((o) => {
      const n = new Set(o);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });

  return (
    <Panel
      className="span-12"
      title="GraphQL operations"
      description={`Each operation with its latency, its depth and field count measured from the document, and the database statements its requests ran. Deep is ${8} levels or more, wide ${100} fields or more, and N+1 is 10 or more queries per call: usually a resolver that queries once per item. Expand one for its document and statements.`}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={200}
      table={
        q.data &&
        (rows.length === 0 ? (
          <div className="panel-state empty-why">
            <div>No GraphQL operations in this range.</div>
            <HelpLink topic="graphql">How to record GraphQL operations</HelpLink>
          </div>
        ) : (
          <div className="table-wrap" style={{ maxHeight: 640 }}>
            <table className="data q-gql">
              <thead>
                <tr>
                  <th style={{ width: 28 }} aria-label="Expand" />
                  <th>Operation</th>
                  <th className="num" style={{ width: 70 }}>Calls</th>
                  <th className="num" style={{ width: 70 }}>p50</th>
                  <th className="num" style={{ width: 70 }}>p95</th>
                  <th className="num" style={{ width: 60 }}>Errors</th>
                  <th className="num" style={{ width: 64 }}>Depth</th>
                  <th className="num" style={{ width: 64 }}>Fields</th>
                  <th className="num" style={{ width: 110 }}>
                    Queries / call <InfoTip text="Database statements its requests ran, per call of the operation." size={12} />
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => {
                  const name = String(r.name);
                  const isOpen = open.has(name);
                  const flags = Array.isArray(r.flags) ? (r.flags as string[]) : [];
                  const statements = (Array.isArray(r.statements) ? r.statements : []) as Statement[];
                  return (
                    <Fragment key={name}>
                      <tr className={`clickable ${isOpen ? "selected" : ""}`} onClick={() => toggle(name)} aria-expanded={isOpen}>
                        <td className="dim">{isOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}</td>
                        <td>
                          <span className="span-name">
                            <span className="wf-swatch" style={{ background: KIND_COLOR.graphql }} aria-hidden />
                            <span className="mono">{name}</span>
                            {flags.map((f) => (
                              <span key={f} className="pill q-flag">
                                {f === "n+1" ? "N+1" : f}
                              </span>
                            ))}
                          </span>
                        </td>
                        <td className="num">{formatCount(toNumber(r.calls) ?? 0)}</td>
                        <td className="num">{ms(r.p50)}</td>
                        <td className="num">{ms(r.p95)}</td>
                        <td className="num">{(toNumber(r.errors) ?? 0) > 0 ? <span className="flag bad">{formatCount(toNumber(r.errors) ?? 0)}</span> : <span className="dim">0</span>}</td>
                        <td className="num">
                          <span className={flags.includes("deep") ? "flag warn" : undefined}>{formatCount(toNumber(r.depth) ?? 0)}</span>
                        </td>
                        <td className="num">
                          <span className={flags.includes("wide") ? "flag warn" : undefined}>{formatCount(toNumber(r.fields) ?? 0)}</span>
                        </td>
                        <td className="num">
                          <span className={flags.includes("n+1") ? "flag warn" : undefined}>{formatCount(toNumber(r.queries_per_call) ?? 0)}</span>
                        </td>
                      </tr>
                      {isOpen && (
                        <tr className="slow-group-body">
                          <td colSpan={9}>
                            <div className="q-gql-body">
                              <div>
                                <div className="rt-title">Document</div>
                                <pre className="code mono wf-sql">{String(r.document ?? "")}</pre>
                              </div>
                              <div>
                                <div className="rt-title">Statements its requests ran</div>
                                {statements.length === 0 ? (
                                  <p className="dim">No database calls.</p>
                                ) : (
                                  <ul className="q-gql-stmts">
                                    {statements.map((st) => (
                                      <li key={st.statement}>
                                        <button type="button" className="linkish mono" onClick={() => onOpen({ kind: "db", name: st.statement })}>
                                          {st.statement}
                                        </button>
                                        <span className="dim">
                                          ×{formatCount(st.per_operation)} per call · {formatMs(toNumber(st.total_ms) ?? 0)}
                                        </span>
                                      </li>
                                    ))}
                                  </ul>
                                )}
                                <button type="button" className="btn sm" style={{ marginTop: 8 }} onClick={() => onOpen({ kind: "graphql", name })}>
                                  Open the operation
                                </button>
                              </div>
                            </div>
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        ))
      }
    />
  );
}
