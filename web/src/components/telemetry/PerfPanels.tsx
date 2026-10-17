import { useEffect, useMemo, useState, type ReactNode } from "react";
import { api, type Result } from "../../lib/api";
import { formatCount, formatMs, formatPercent, toNumber } from "../../lib/format";
import { formatRelative } from "../../lib/time";
import { shortVersion, useTelemetry, type CompareResponse, type RequestTelemetry } from "../../lib/telemetry";
import { DataTable, type Column } from "../DataTable";
import { Panel } from "../Panel";
import { Select } from "../Select";
import { KIND_COLOR } from "./Waterfall";
import "./telemetry.css";
import "./perf.css";

const NO_SPANS =
  "No spans in this range. SDKs with telemetry record each database and outbound call of requests slower than VISIONARY_SPAN_THRESHOLD_MS (100 ms by default) or answering 5xx.";

/** Opens the log entry of a request found by its request_id. */
export async function logIdFor(requestId: string): Promise<number | null> {
  const r = await api<RequestTelemetry>(`/api/telemetry/requests/${encodeURIComponent(requestId)}`);
  return toNumber(r.log_id);
}

function ms(v: unknown) {
  const n = toNumber(v);
  return n === null ? <span className="dim">—</span> : formatMs(n);
}

function ShareBar({ value, max }: { value: number; max: number }) {
  return (
    <span className="cell-bar">
      {formatMs(value)}
      <span className="track" aria-hidden>
        <span className="fill" style={{ display: "block", width: `${(100 * value) / Math.max(max, 1)}%` }} />
      </span>
    </span>
  );
}

function RouteList({ routes }: { routes: unknown }) {
  const list = Array.isArray(routes) ? (routes as string[]) : [];
  if (list.length === 0) return <span className="dim">background</span>;
  return (
    <span className="span-routes" title={list.join("\n")}>
      {list.map((r) => (
        <span key={r} className="mono">
          {r}
        </span>
      ))}
    </span>
  );
}

function spanColumns(res: Result, kind: string): Column[] {
  const ti = res.columns.indexOf("total_ms");
  const max = Math.max(1, ...res.rows.map((r) => toNumber(r[ti]) ?? 0));
  const di = res.columns.indexOf("slowest_detail");
  return [
    {
      key: "name",
      label: kind === "db" ? "Statement" : "Host",
      render: (v, row) => (
        <div className="span-name">
          <span className="wf-swatch" style={{ background: KIND_COLOR[kind] }} aria-hidden />
          <div style={{ minWidth: 0 }}>
            <div className="mono span-name-main">{String(v)}</div>
            {row[di] ? (
              <div className="mono dim span-detail" title={String(row[di])}>
                {String(row[di])}
              </div>
            ) : null}
          </div>
        </div>
      ),
    },
    { key: "calls", align: "right", unit: "count", width: 70 },
    { key: "total_ms", label: "Total time", align: "right", render: (v) => <ShareBar value={toNumber(v) ?? 0} max={max} />, width: 130 },
    { key: "avg_ms", label: "Avg", align: "right", render: ms, width: 76 },
    { key: "p95", align: "right", render: ms, width: 76 },
    { key: "max_ms", label: "Max", align: "right", render: ms, width: 76 },
    {
      key: "errors",
      align: "right",
      render: (v, row) => {
        const n = toNumber(v) ?? 0;
        const calls = toNumber(row[res.columns.indexOf("calls")]) ?? 1;
        if (n === 0) return <span className="dim">0</span>;
        return <span className={`flag ${n / calls >= 0.05 ? "bad" : "warn"}`}>{formatCount(n)}</span>;
      },
      width: 64,
    },
    { key: "routes", label: "Routes", render: (v) => <RouteList routes={v} />, width: 250 },
  ];
}

/** Span names of one kind by total time: the statements or hosts that cost the most. */
export function SpanGroupsPanel({ kind, title, description, className }: { kind: "db" | "http"; title: string; description: string; className: string }) {
  const q = useTelemetry("spans", { kind });
  return (
    <Panel
      className={className}
      title={title}
      description={description}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={160}
      table={
        q.data &&
        (q.data.rows.length === 0 ? <div className="panel-state empty-why">{NO_SPANS}</div> : <DataTable result={q.data} columns={spanColumns(q.data, kind)} maxHeight={420} />)
      }
    />
  );
}

/** Routes where one call repeats per item: N+1 queries and call loops. */
export function NPlusOnePanel({ className, onOpenRequest }: { className: string; onOpenRequest: (requestId: string) => void }) {
  const q = useTelemetry("nplusone");
  const columns: Column[] = [
    {
      key: "route",
      render: (v, row) => (
        <span>
          <span className="method">{String(row[0])}</span> <span className="mono">{String(v)}</span>
        </span>
      ),
    },
    {
      key: "name",
      label: "Repeated call",
      render: (v, row) => (
        <span className="span-name">
          <span className="wf-swatch" style={{ background: KIND_COLOR[String(row[3])] ?? KIND_COLOR.custom }} aria-hidden />
          <span className="mono">{String(v)}</span>
        </span>
      ),
    },
    { key: "avg_calls", label: "Calls per request", align: "right", render: (v) => <span className="flag warn">×{formatCount(toNumber(v) ?? 0)}</span>, width: 120 },
    { key: "max_calls", label: "Max", align: "right", render: (v) => `×${formatCount(toNumber(v) ?? 0)}`, width: 64 },
    {
      key: "flagged",
      label: "Requests",
      align: "right",
      render: (v, row) => (
        <span title="Traced requests with the call repeated 10 or more times, of all traced requests of the route that made it">
          {formatCount(toNumber(v) ?? 0)} <span className="dim">of {formatCount(toNumber(row[4]) ?? 0)}</span>
        </span>
      ),
      width: 100,
    },
    { key: "avg_ms", label: "Time per request", align: "right", render: ms, width: 120 },
    { key: "time_share", label: "Of request time", align: "right", render: (v) => (toNumber(v) === null ? <span className="dim">—</span> : formatPercent(toNumber(v)!)), width: 110 },
    {
      key: "example_request",
      label: "",
      align: "right",
      render: (v) =>
        v ? (
          <button
            className="btn sm ghost"
            onClick={(e) => {
              e.stopPropagation();
              onOpenRequest(String(v));
            }}
          >
            Example
          </button>
        ) : null,
      width: 90,
    },
  ];
  return (
    <Panel
      className={className}
      title="N+1 suspects"
      description="Routes where one statement or host is called 10 or more times in a single request: usually a query per item in a loop. Batch them (an IN list, a join, a bulk endpoint). Counted over requests that kept spans, which are the slow and failed ones."
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={120}
      table={
        q.data &&
        (q.data.rows.length === 0 ? (
          <div className="panel-state empty-why">No call repeats 10 or more times in any traced request in this range.</div>
        ) : (
          <DataTable result={q.data} columns={columns} maxHeight={320} />
        ))
      }
    />
  );
}

// ---------- Compare versions ----------

/** b against a, as a signed percent; worse is flagged. */
function Change({ a, b, worseUp = true, unit }: { a: unknown; b: unknown; worseUp?: boolean; unit: "ms" | "percent" | "count" }) {
  const x = toNumber(a);
  const y = toNumber(b);
  const fmt = (n: number | null) => (n === null ? "—" : unit === "ms" ? formatMs(n) : unit === "percent" ? formatPercent(n) : formatCount(n));
  let change: ReactNode = null;
  if (x !== null && y !== null) {
    if (unit === "percent") {
      const pts = y - x;
      if (Math.abs(pts) >= 0.05) change = <span className={`flag ${(pts > 0) === worseUp ? (Math.abs(pts) >= 1 ? "bad" : "warn") : "dim"}`}>{`${pts > 0 ? "+" : "−"}${Math.abs(pts).toFixed(1)} pt`}</span>;
    } else if (x > 0) {
      const pct = (100 * (y - x)) / x;
      const worse = (pct > 0) === worseUp;
      if (Math.abs(pct) >= 1)
        change = <span className={`flag ${worse ? (Math.abs(pct) >= 50 ? "bad" : Math.abs(pct) >= 20 ? "warn" : "") : "dim"}`}>{`${pct > 0 ? "+" : "−"}${Math.abs(pct).toFixed(Math.abs(pct) >= 10 ? 0 : 1)}%`}</span>;
    }
  }
  return (
    <span className="cmp-cell">
      <span className="dim">{fmt(x)}</span>
      <span className="cmp-arrow" aria-label="to">
        →
      </span>
      <span>{fmt(y)}</span>
      <span className="cmp-change">{change}</span>
    </span>
  );
}

function compareColumns(res: Result): Column[] {
  const at = (row: unknown[], c: string) => row[res.columns.indexOf(c)];
  const pair = (key: string, label: string, unit: "ms" | "percent" | "count", worseUp = true): Column => ({
    key: `b_${key}`,
    label,
    align: "right",
    render: (_v, row) => <Change a={at(row, `a_${key}`)} b={at(row, `b_${key}`)} unit={unit} worseUp={worseUp} />,
  });
  return [
    {
      key: "route",
      render: (v, row) => (
        <span>
          <span className="method">{String(at(row, "method"))}</span> <span className="mono">{String(v)}</span>
        </span>
      ),
    },
    {
      key: "b_requests",
      label: "Requests",
      align: "right",
      render: (_v, row) => (
        <span>
          <span className="dim">{formatCount(toNumber(at(row, "a_requests")) ?? 0)}</span> / {formatCount(toNumber(at(row, "b_requests")) ?? 0)}
        </span>
      ),
      width: 110,
    },
    pair("p95", "p95", "ms"),
    pair("error_rate", "5xx rate", "percent"),
    pair("db_ms", "DB time / request", "ms"),
    pair("db_calls", "DB calls / request", "count"),
  ];
}

interface VersionRow {
  version: string;
  service: string;
  requests: number;
  first: string;
}

/** Two versions side by side: overall and per route, over the dashboard range. */
export function CompareVersionsPanel({ className }: { className: string }) {
  const versions = useTelemetry("versions");
  const list = useMemo<VersionRow[]>(
    () =>
      (versions.data?.rows ?? []).map((r) => ({
        version: String(r[0]),
        service: String(r[1] ?? ""),
        requests: toNumber(r[2]) ?? 0,
        first: String(r[3]),
      })),
    [versions.data],
  );
  const [a, setA] = useState("");
  const [b, setB] = useState("");

  // Default: the newest version against the one before it in the same service.
  useEffect(() => {
    if (list.length < 2 || (list.some((v) => v.version === a) && list.some((v) => v.version === b))) return;
    const newest = list[0];
    const before = list.slice(1).find((v) => v.service === newest.service) ?? list[1];
    setB(newest.version);
    setA(before.version);
  }, [list, a, b]);

  const cmp = useTelemetry<CompareResponse>(a && b && a !== b ? "compare" : null, { a, b });
  const overall = cmp.data?.overall;
  const routes = cmp.data?.routes;
  const options = list.map((v) => ({ value: v.version, label: `${shortVersion(v.version)}${v.service ? ` · ${v.service}` : ""} · ${formatRelative(v.first)}` }));
  const row = overall?.rows[0];
  const at = (c: string) => (row && overall ? row[overall.columns.indexOf(c)] : null);
  const pickers = (
    <div className="cmp-pickers">
      <Select label="Baseline version" align="left" value={a} onChange={setA} options={options} maxWidth={320} />
      <span className="dim">compared with</span>
      <Select label="Compared version" align="left" value={b} onChange={setB} options={options} maxWidth={320} />
    </div>
  );

  return (
    <Panel
      className={className}
      title="Compare versions"
      description="Latency, server errors and database work per route for two versions, over the selected range. The first version is the baseline."
      result={routes ?? versions.data}
      loading={versions.loading || cmp.loading}
      error={versions.error ?? cmp.error}
      bodyHeight={160}
      table={
        versions.data &&
        (list.length < 2 ? (
          <div className="panel-state empty-why">
            {list.length === 0
              ? "No request in this range carries a version. Set VISIONARY_RELEASE (or deploy from a platform that sets a commit variable) and the SDKs tag every request with it."
              : "Only one version served requests in this range. Widen the range to include an earlier version."}
          </div>
        ) : a === b ? (
          <div className="cmp">
            {pickers}
            <div className="panel-state">Pick two different versions.</div>
          </div>
        ) : !routes ? (
          <div className="cmp">{pickers}</div>
        ) : (
          <div className="cmp">
            {pickers}
            <div className="cmp-summary">
              <div>
                <div className="cmp-label">All routes, p50</div>
                <Change a={at("a_p50")} b={at("b_p50")} unit="ms" />
              </div>
              <div>
                <div className="cmp-label">p95</div>
                <Change a={at("a_p95")} b={at("b_p95")} unit="ms" />
              </div>
              <div>
                <div className="cmp-label">5xx rate</div>
                <Change a={at("a_error_rate")} b={at("b_error_rate")} unit="percent" />
              </div>
              <div>
                <div className="cmp-label">DB time / request</div>
                <Change a={at("a_db_ms")} b={at("b_db_ms")} unit="ms" />
              </div>
              <div>
                <div className="cmp-label">DB calls / request</div>
                <Change a={at("a_db_calls")} b={at("b_db_calls")} unit="count" />
              </div>
            </div>
            <DataTable result={routes} columns={compareColumns(routes)} maxHeight={380} sortable={false} />
          </div>
        ))
      }
    />
  );
}
