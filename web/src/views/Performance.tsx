import { useMemo, useState } from "react";
import { api, rangeQuery, type Meta, type Result, type StatsResponse, type Viz } from "../lib/api";
import { formatBytes, formatCount, formatMs, formatPercent, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatLogTime, previousLabel } from "../lib/time";
import { sparkPoints } from "../charts/option";
import { buildLatencyHeatmap, LATENCY_BANDS } from "../charts/heatmap";
import { Chart } from "../components/Chart";
import { DataTable, type Column } from "../components/DataTable";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { LogDetail } from "./Logs";
import { useTelemetry } from "../lib/telemetry";
import { CompareVersionsPanel, logIdFor, NPlusOnePanel, SpanGroupsPanel } from "../components/telemetry/PerfPanels";

type Q<T = Result> = { data?: T; loading: boolean; error?: Error };

function usePerf<T = Result>(name: string): Q<T> {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to);
  return useQuery<T>(`perf-${name}?${qs}#${tick}`, (signal) => api<T>(`/api/performance/${name}?${qs}`, { signal }));
}

const VIZ = {
  percentiles: { type: "timeseries", x: "t", y: ["p50", "p90", "p95", "p99"], unit: "ms" },
  apdex: { type: "timeseries", x: "t", y: ["apdex"], unit: "none" },
  routeP95: { type: "timeseries", x: "t", y: ["value"], series: "series", unit: "ms" },
  histogram: { type: "bar", x: "band", y: ["requests"], unit: "count" },
  satRequests: { type: "timeseries", x: "t", y: ["requests"], unit: "count" },
  satP95: { type: "timeseries", x: "t", y: ["p95"], unit: "ms" },
  satInFlight: { type: "timeseries", x: "t", y: ["in_flight"], unit: "count" },
  satDbWait: { type: "timeseries", x: "t", y: ["db_wait_ms"], unit: "ms" },
} satisfies Record<string, Viz>;

export function Performance({ meta }: { meta?: Meta }) {
  const { range } = useApp();
  const apdexMs = meta?.apdex_ms ?? 500;
  const target = meta?.slo_target ?? 99;

  const summary = usePerf<StatsResponse>("summary");
  const percentiles = usePerf("percentiles");
  const apdex = usePerf("apdex");
  const heatmap = usePerf("heatmap");
  const histogram = usePerf("histogram");
  const routes = usePerf<StatsResponse>("routes");
  const routeP95 = usePerf("route_p95");
  const slowest = usePerf("slowest");
  const users = usePerf("users");
  const saturation = usePerf("saturation");
  const calls = useTelemetry("routes");
  const [openLog, setOpenLog] = useState<number | null>(null);
  const openRequest = (requestId: string) => {
    logIdFor(requestId)
      .then((id) => id !== null && setOpenLog(id))
      .catch(() => undefined);
  };

  const all = [summary, percentiles, apdex, heatmap, histogram, routes, routeP95, slowest, users, saturation, calls];
  const busy = all.some((q) => q.loading);

  const cur = rowObject(summary.data?.current);
  const prev = rowObject(summary.data?.previous ?? undefined);
  const delta = (k: string) => {
    const a = cur?.[k];
    const b = prev?.[k];
    return a === undefined || b === undefined || b === 0 ? null : (a - b) / b;
  };
  const prevLbl = previousLabel(range);

  // Error budget: the share of requests allowed to miss the target.
  const allowed = 100 - target;
  const missed = cur && cur.requests > 0 ? 100 - (cur.good_percent ?? 100) : null;
  const budgetLeft = missed === null ? null : Math.max(0, 100 - (100 * missed) / allowed);

  const sparks = useMemo(
    () => ({
      apdex: sparkPoints(apdex.data, ["apdex"]),
      good: sparkPoints(apdex.data, ["good_percent"]),
      p95: sparkPoints(percentiles.data, ["p95"]),
      p99: sparkPoints(percentiles.data, ["p99"]),
    }),
    [apdex.data, percentiles.data],
  );

  const histResult = useMemo(() => labelBands(histogram.data), [histogram.data]);
  const regressions = useMemo(() => findRegressions(routes.data), [routes.data]);
  const routeRows = useMemo(() => withCalls(withPreviousP95(routes.data), calls.data), [routes.data, calls.data]);

  return (
    <>
      <TopBar
        title="Performance"
        section="API"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl busy={busy} />
          </>
        }
      />
      <div className="content">
        {summary.error && !summary.data && (
          <div className="notice" style={{ marginBottom: 12 }}>
            <span>Could not load performance data: {summary.error.message}</span>
          </div>
        )}
        <div className="grid">
          <StatTile
            label="Apdex"
            description={`User satisfaction from 0 to 1. A request is satisfied within ${formatMs(apdexMs)}, tolerating within ${formatMs(4 * apdexMs)}, and frustrated when slower or a server error. Set VISIONARY_APDEX_MS to change the threshold.`}
            value={cur?.apdex != null ? cur.apdex.toFixed(2) : "—"}
            delta={delta("apdex")}
            deltaLabel={prevLbl}
            upIs="good"
            spark={sparks.apdex}
            loading={summary.loading}
          />
          <StatTile
            label={`Within ${formatMs(apdexMs)}`}
            description={`Share of requests that succeeded within ${formatMs(apdexMs)}. The SLO target is ${target}% (VISIONARY_SLO_TARGET).`}
            value={cur?.good_percent != null ? formatPercent(cur.good_percent) : "—"}
            delta={delta("good_percent")}
            deltaLabel={prevLbl}
            upIs="good"
            spark={sparks.good}
            loading={summary.loading}
          />
          <StatTile
            label="Error budget left"
            description={`With a ${target}% target, ${allowed.toFixed(allowed < 1 ? 1 : 0)}% of requests may be slow or fail. This is how much of that allowance is unused in the range.`}
            value={budgetLeft === null ? "—" : formatPercent(budgetLeft)}
            delta={null}
            deltaLabel=""
            upIs="good"
            loading={summary.loading}
            footer={missed === null ? undefined : `${formatPercent(missed)} missed · target ${target}%`}
          />
          <StatTile
            label="p95 latency"
            value={cur?.p95 != null ? formatMs(cur.p95) : "—"}
            delta={delta("p95")}
            deltaLabel={prevLbl}
            upIs="bad"
            spark={sparks.p95}
            loading={summary.loading}
          />
          <StatTile
            label="p99 latency"
            description="The slowest 1% of requests take at least this long."
            value={cur?.p99 != null ? formatMs(cur.p99) : "—"}
            delta={delta("p99")}
            deltaLabel={prevLbl}
            upIs="bad"
            spark={sparks.p99}
            loading={summary.loading}
          />
          <StatTile
            label="Frustrated requests"
            description={`Requests slower than ${formatMs(4 * apdexMs)}.`}
            value={cur ? formatCount(cur.frustrated ?? 0) : "—"}
            delta={delta("frustrated")}
            deltaLabel={prevLbl}
            upIs="bad"
            loading={summary.loading}
            footer={cur?.max_ms != null ? `slowest ${formatMs(cur.max_ms)}` : undefined}
          />

          <HeatmapPanel q={heatmap} />
          <ChartPanel
            className="span-4"
            title="Latency distribution"
            description="How many requests fall in each latency band over the whole range."
            q={{ ...histogram, data: histResult }}
            viz={VIZ.histogram}
            height={280}
          />

          <ChartPanel
            className="span-8"
            title="Latency percentiles"
            description="p50 is the typical request; p99 is the slowest 1%. A widening gap means a slow tail, not a slow service."
            q={percentiles}
            viz={VIZ.percentiles}
            height={240}
          />
          <ChartPanel
            className="span-4"
            title="Apdex over time"
            description={`1.0 means every request finished within ${formatMs(apdexMs)}.`}
            q={apdex}
            viz={VIZ.apdex}
            height={240}
          />

          <ChartPanel
            className="span-12"
            title="p95 of the busiest routes"
            description="The five routes with the most traffic. A single line rising shows which endpoint degraded."
            q={routeP95}
            viz={VIZ.routeP95}
            height={240}
          />

          <Section
            title="Load and saturation"
            text="Latency next to what drives it, on the same time axis. When p95 climbs with requests or in-flight work, the service is saturating; when it climbs with database waits, the pool is the bottleneck."
          />
          <ChartPanel className="span-3" title="Requests" q={saturation} viz={VIZ.satRequests} height={150} />
          <ChartPanel className="span-3" title="p95 latency" q={saturation} viz={VIZ.satP95} height={150} />
          <ChartPanel className="span-3" title="In flight (peak)" q={saturation} viz={VIZ.satInFlight} height={150} />
          <ChartPanel className="span-3" title="Waiting for a DB connection" q={saturation} viz={VIZ.satDbWait} height={150} />

          <Panel
            className="span-12"
            title="Routes by server time"
            description="Busy time is the sum of request durations: where the server spends its time, and so where optimizing pays off most. p95 change compares with the previous period of the same length. DB time is the share of request time spent in database calls, from SDKs that measure it."
            result={routeRows}
            loading={routes.loading}
            error={routes.error}
            bodyHeight={200}
            table={routeRows && <DataTable result={routeRows} columns={routeColumns(routeRows, apdexMs)} maxHeight={460} />}
          />

          <Panel
            className="span-7"
            title="Regressions"
            description="Routes whose p95 grew by 20% or more against the previous period, with at least 20 requests in both."
            result={regressions}
            loading={routes.loading}
            error={routes.error}
            bodyHeight={160}
            table={
              regressions &&
              (regressions.rows.length === 0 ? (
                <div className="panel-state">No route got slower than in the previous period.</div>
              ) : (
                <DataTable result={regressions} columns={regressionColumns} maxHeight={320} />
              ))
            }
          />
          <Panel
            className="span-5"
            title="Users waiting the longest"
            description={`Signed-in users with the most frustrated requests (slower than ${formatMs(4 * apdexMs)} or a server error).`}
            result={users.data}
            loading={users.loading}
            error={users.error}
            bodyHeight={160}
            table={users.data && <DataTable result={users.data} columns={userColumns} maxHeight={320} />}
          />

          <Panel
            className="span-12"
            title="Slowest requests"
            description="The 25 slowest individual requests in the range. Open one to see its headers and bodies."
            result={slowest.data}
            loading={slowest.loading}
            error={slowest.error}
            bodyHeight={200}
            table={
              slowest.data && (
                <DataTable
                  result={slowest.data}
                  columns={slowestColumns}
                  maxHeight={420}
                  onRowClick={(row) => setOpenLog(toNumber(row[0]))}
                  rowKey={(row) => row[0]}
                  selectedKey={openLog ?? undefined}
                />
              )
            }
          />

          <Section
            title="Database and outbound calls"
            text="Where requests wait on other systems, from the spans the SDKs keep for slow and failed requests. Statements are grouped by their first keyword and table, outbound calls by method and host."
          />
          <SpanGroupsPanel
            className="span-12"
            kind="db"
            title="Slowest queries"
            description="Database statements by total time spent in them, with the slowest instance of each (literals replaced by ?) and the routes that run them most."
          />
          <SpanGroupsPanel className="span-12" kind="http" title="Outbound calls" description="Outbound HTTP calls by host, by total time, with the routes that make them most." />
          <NPlusOnePanel className="span-12" onOpenRequest={openRequest} />

          <Section title="Releases" text="Dashed lines on the charts mark deploys. Compare two versions to see what a release changed, route by route." />
          <CompareVersionsPanel className="span-12" />
        </div>
      </div>
      {openLog !== null && <LogDetail id={openLog} onClose={() => setOpenLog(null)} />}
    </>
  );
}

function HeatmapPanel({ q }: { q: Q }) {
  const tokens = useChartTokens();
  const option = useMemo(() => (q.data ? buildLatencyHeatmap(q.data, tokens) : null), [q.data, tokens]);
  const height = 280;
  return (
    <Panel
      className="span-8"
      title="Latency heatmap"
      description="Requests per interval and latency band; brighter cells hold more requests. Bands that light up above the main body are slow outliers, and a band that drifts upward is a slowdown."
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={height}
      chart={
        q.data &&
        (option ? (
          <Chart option={option} height={height} label="Latency heatmap" />
        ) : (
          <div className="panel-state" style={{ height }}>
            No data in this time range.
          </div>
        ))
      }
      table={q.data && <DataTable result={labelBands(q.data) ?? q.data} maxHeight={height} />}
    />
  );
}

function ChartPanel({ q, viz, height, title, description, className }: { q: Q; viz: Viz; height: number; title: string; description?: string; className: string }) {
  const tokens = useChartTokens();
  return (
    <Panel
      className={className}
      title={title}
      description={description}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={height}
      note={vizNote(q.data, viz, tokens)}
      chart={q.data && <VizView result={q.data} viz={viz} height={height} title={title} />}
      table={q.data && <DataTable result={q.data} maxHeight={height} defaultUnit={viz.unit} />}
    />
  );
}

function rowObject(res: Result | undefined): Record<string, number> | null {
  if (!res || res.rows.length === 0) return null;
  const out: Record<string, number> = {};
  res.columns.forEach((c, i) => {
    const n = toNumber(res.rows[0][i]);
    if (n !== null) out[c] = n;
  });
  return out;
}

/** Replaces band numbers with their labels, keeping every band in order. */
function labelBands(res: Result | undefined): Result | undefined {
  if (!res) return undefined;
  const bi = res.columns.indexOf("band");
  if (bi < 0) return res;
  if (res.columns.includes("t")) {
    return { ...res, rows: res.rows.map((r) => r.map((v, i) => (i === bi ? (LATENCY_BANDS[toNumber(v) ?? 0] ?? String(v)) : v))) };
  }
  const counts = new Map(res.rows.map((r) => [toNumber(r[bi]), r]));
  const ci = res.columns.indexOf("requests");
  return {
    ...res,
    rows: LATENCY_BANDS.map((label, i) => {
      const r = counts.get(i);
      return res.columns.map((_, j) => (j === bi ? label : j === ci ? (r ? r[ci] : 0) : r ? r[j] : null));
    }),
  };
}

const routeKey = (row: unknown[], cols: string[]) => `${row[cols.indexOf("method")]} ${row[cols.indexOf("route")]}`;

/** The current routes with the previous period's p95 and its change. */
function withPreviousP95(data: StatsResponse | undefined): Result | undefined {
  const cur = data?.current;
  if (!cur) return undefined;
  const prev = data.previous;
  const before = new Map<string, number | null>();
  if (prev) {
    const pi = prev.columns.indexOf("p95");
    for (const r of prev.rows) before.set(routeKey(r, prev.columns), toNumber(r[pi]));
  }
  const pi = cur.columns.indexOf("p95");
  return {
    ...cur,
    columns: [...cur.columns, "prev_p95", "p95_change"],
    rows: cur.rows.map((r) => {
      const was = before.get(routeKey(r, cur.columns)) ?? null;
      const now = toNumber(r[pi]);
      const change = was && now !== null ? (now - was) / was : null;
      return [...r, was, change];
    }),
  };
}

/** Adds each route's DB time share and calls per request, when the SDK reports them. */
function withCalls(rows: Result | undefined, calls: Result | undefined): Result | undefined {
  if (!rows) return undefined;
  const by = new Map<string, unknown[]>();
  if (calls) for (const r of calls.rows) by.set(routeKey(r, calls.columns), r);
  const si = calls?.columns.indexOf("db_share") ?? -1;
  const ci = calls?.columns.indexOf("db_calls") ?? -1;
  return {
    ...rows,
    columns: [...rows.columns, "db_share", "db_calls"],
    rows: rows.rows.map((r) => {
      const c = by.get(routeKey(r, rows.columns));
      return [...r, c && si >= 0 ? c[si] : null, c && ci >= 0 ? c[ci] : null];
    }),
  };
}

function findRegressions(data: StatsResponse | undefined): Result | undefined {
  const cur = data?.current;
  if (!cur) return undefined;
  const prev = data.previous;
  const cols = ["method", "route", "requests", "prev_p95", "p95", "p95_change", "error_rate"];
  if (!prev) return { ...cur, columns: cols, rows: [] };
  const at = (res: Result, r: unknown[], c: string) => toNumber(r[res.columns.indexOf(c)]);
  const before = new Map(prev.rows.map((r) => [routeKey(r, prev.columns), r]));
  const rows: unknown[][] = [];
  for (const r of cur.rows) {
    const p = before.get(routeKey(r, cur.columns));
    if (!p) continue;
    const n = at(cur, r, "requests") ?? 0;
    const pn = at(prev, p, "requests") ?? 0;
    const now = at(cur, r, "p95");
    const was = at(prev, p, "p95");
    if (n < 20 || pn < 20 || now === null || !was) continue;
    const change = (now - was) / was;
    if (change < 0.2) continue;
    rows.push([r[cur.columns.indexOf("method")], r[cur.columns.indexOf("route")], n, was, now, change, at(cur, r, "error_rate")]);
  }
  rows.sort((a, b) => (b[5] as number) - (a[5] as number));
  return { ...cur, columns: cols, rows };
}

function ApdexCell({ value }: { value: unknown }) {
  const n = toNumber(value);
  if (n === null) return <span className="dim">—</span>;
  const cls = n < 0.7 ? "bad" : n < 0.85 ? "warn" : "";
  return <span className={`flag ${cls}`}>{n.toFixed(2)}</span>;
}

function RateCell({ value }: { value: unknown }) {
  const n = toNumber(value);
  if (n === null) return <span className="dim">—</span>;
  const cls = n >= 5 ? "bad" : n >= 1 ? "warn" : "";
  return <span className={`flag ${cls}`}>{n === 0 ? <span className="dim">0%</span> : formatPercent(n)}</span>;
}

/** Change as a signed percent; slower is flagged, faster is dimmed. */
function ChangeCell({ value }: { value: unknown }) {
  const n = toNumber(value);
  if (n === null) return <span className="dim">new</span>;
  const pct = n * 100;
  const text = `${pct > 0 ? "+" : pct < 0 ? "−" : ""}${Math.abs(pct).toFixed(Math.abs(pct) >= 10 ? 0 : 1)}%`;
  const cls = pct >= 50 ? "bad" : pct >= 20 ? "warn" : "";
  return <span className={`flag ${cls}`}>{pct <= 0 ? <span className="dim">{text}</span> : text}</span>;
}

function msCell(v: unknown) {
  const n = toNumber(v);
  return n === null ? <span className="dim">—</span> : formatMs(n);
}

function routeColumns(res: Result, apdexMs: number): Column[] {
  const si = res.columns.indexOf("time_share");
  const max = Math.max(1, ...res.rows.map((r) => toNumber(r[si]) ?? 0));
  return [
    { key: "method", render: (v) => <span className="method">{String(v)}</span>, width: 70 },
    { key: "route", render: (v) => <span className="mono">{String(v)}</span> },
    { key: "requests", align: "right", unit: "count" },
    {
      key: "time_share",
      label: "Share of server time",
      align: "right",
      render: (v) => {
        const n = toNumber(v) ?? 0;
        return (
          <span className="cell-bar">
            {formatPercent(n)}
            <span className="track" aria-hidden>
              <span className="fill" style={{ display: "block", width: `${(100 * n) / max}%` }} />
            </span>
          </span>
        );
      },
    },
    { key: "busy_seconds", label: "Busy", align: "right", render: (v) => msCell((toNumber(v) ?? 0) * 1000) },
    {
      key: "db_share",
      label: "DB time",
      align: "right",
      render: (v, row) => {
        const n = toNumber(v);
        if (n === null) return <span className="dim">—</span>;
        const calls = toNumber(row[res.columns.indexOf("db_calls")]);
        return <span title={calls === null ? undefined : `${calls} database calls per request on average`}>{formatPercent(Math.min(100, n))}</span>;
      },
    },
    { key: "db_calls", label: "DB calls", align: "right", render: (v) => (toNumber(v) === null ? <span className="dim">—</span> : formatCount(toNumber(v)!)) },
    { key: "p50", align: "right", render: msCell },
    { key: "p95", align: "right", render: msCell },
    { key: "p95_change", label: "p95 change", align: "right", render: (v) => <ChangeCell value={v} /> },
    { key: "p99", align: "right", render: msCell },
    { key: "max_ms", label: "Max", align: "right", render: msCell },
    { key: "error_rate", label: "5xx", align: "right", render: (v) => <RateCell value={v} /> },
    { key: "apdex", label: `Apdex (${formatMs(apdexMs)})`, align: "right", render: (v) => <ApdexCell value={v} /> },
    { key: "avg_response_bytes", label: "Avg size", align: "right", render: (v) => (toNumber(v) === null ? <span className="dim">—</span> : formatBytes(toNumber(v)!)) },
  ];
}

const regressionColumns: Column[] = [
  { key: "method", render: (v) => <span className="method">{String(v)}</span>, width: 70 },
  { key: "route", render: (v) => <span className="mono">{String(v)}</span> },
  { key: "requests", align: "right", unit: "count" },
  { key: "prev_p95", label: "p95 before", align: "right", render: msCell },
  { key: "p95", label: "p95 now", align: "right", render: msCell },
  { key: "p95_change", label: "Change", align: "right", render: (v) => <ChangeCell value={v} /> },
  { key: "error_rate", label: "5xx", align: "right", render: (v) => <RateCell value={v} /> },
];

const userColumns: Column[] = [
  {
    key: "handle",
    label: "User",
    render: (v, row) => <span>{v ? `@${String(v)}` : <span className="dim">#{String(row[0])}</span>}</span>,
  },
  { key: "requests", align: "right", unit: "count" },
  { key: "frustrated", align: "right", unit: "count" },
  { key: "apdex", align: "right", render: (v) => <ApdexCell value={v} /> },
  { key: "slowest_ms", label: "Slowest", align: "right", render: msCell },
];

const slowestColumns: Column[] = [
  { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
  { key: "method", render: (v) => <span className="method">{String(v)}</span>, width: 70 },
  { key: "route", render: (v, row) => <span className="mono" title={String(row[4])}>{String(v)}</span> },
  {
    key: "status",
    align: "right",
    render: (v) => {
      const n = toNumber(v) ?? 0;
      return (
        <span className={`status s${Math.floor(n / 100)}`}>
          <span className="dot" aria-hidden />
          {String(v)}
        </span>
      );
    },
  },
  { key: "duration_ms", label: "Duration", align: "right", render: msCell },
  { key: "user_handle", label: "User", render: (v, row) => (v ? `@${String(v)}` : row[8] != null ? <span className="dim">#{String(row[8])}</span> : <span className="dim">—</span>) },
  { key: "response_size", label: "Size", align: "right", render: (v) => formatBytes(toNumber(v) ?? 0) },
];
