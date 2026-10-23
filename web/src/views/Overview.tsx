import { useMemo } from "react";
import { ConnectCard } from "../components/ConnectCard";
import { useConnected } from "../lib/meta";
import { api, rangeQuery, type Result, type StatsResponse, type Viz } from "../lib/api";
import { formatCount, formatMs, formatPercent, formatValue, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatRelative, previousLabel } from "../lib/time";
import { sparkPoints } from "../charts/option";
import { Panel } from "../components/Panel";
import { StatTile } from "../components/StatTile";
import { DataTable, type Column } from "../components/DataTable";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { ErrorNotice } from "../components/ErrorNotice";

function usePanel<T = Result>(name: string) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to);
  return useQuery<T>(`${name}?${qs}#${tick}`, (signal) => api<T>(`/api/overview/${name}?${qs}`, { signal }));
}

const VIZ = {
  traffic: { type: "timeseries", x: "t", y: ["value"], series: "series", stack: true, unit: "count" },
  latency: { type: "timeseries", x: "t", y: ["p50", "p95", "p99"], unit: "ms" },
  errorRate: { type: "timeseries", x: "t", y: ["server_errors", "client_errors"], unit: "percent" },
  statusCodes: { type: "bar", x: "status", y: ["requests"], unit: "count", colorBy: "status" },
} satisfies Record<string, Viz>;

export function Overview() {
  const connected = useConnected("requests");
  const { range } = useApp();
  const stats = usePanel<StatsResponse>("stats");
  const traffic = usePanel("traffic");
  const latency = usePanel("latency");
  const errorRate = usePanel("error_rate");
  const statusCodes = usePanel("status_codes");
  const routes = usePanel("routes");
  const errors = usePanel("errors");
  const users = usePanel("users");

  const busy = [stats, traffic, latency, errorRate, statusCodes, routes, errors, users].some((q) => q.loading);

  const cur = rowObject(stats.data?.current);
  const prev = rowObject(stats.data?.previous ?? undefined);
  const rate = (o: Record<string, number> | null, k: string) => (o && o.requests > 0 ? (100 * o[k]) / o.requests : null);
  const delta = (a: number | null, b: number | null) => (a === null || b === null || b === 0 ? null : (a - b) / b);
  const prevLbl = previousLabel(range);

  const sparks = useMemo(
    () => ({
      requests: sparkPoints(traffic.data, ["value"]),
      server: sparkPoints(errorRate.data, ["server_errors"]),
      client: sparkPoints(errorRate.data, ["client_errors"]),
      p50: sparkPoints(latency.data, ["p50"]),
      p95: sparkPoints(latency.data, ["p95"]),
    }),
    [traffic.data, errorRate.data, latency.data],
  );

  const serverRate = rate(cur, "server_errors");
  const clientRate = rate(cur, "client_errors");
  const statsLoading = stats.loading;

  return (
    <>
      <TopBar
        title="Overview"
        section="API"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl busy={busy} />
          </>
        }
      />
      <div className="content">
        {connected ? (
          <>
        {stats.error && !stats.data && (
          <ErrorNotice title="Could not load stats" error={stats.error} style={{ marginBottom: 12 }} />
        )}
        <div className="grid">
          <StatTile
            label="Requests"
            value={cur ? formatCount(cur.requests) : "—"}
            delta={delta(cur?.requests ?? null, prev?.requests ?? null)}
            deltaLabel={prevLbl}
            upIs="neutral"
            spark={sparks.requests}
            loading={statsLoading}
          />
          <StatTile
            label="Server errors"
            description="Share of requests answered with a 5xx status."
            value={serverRate === null ? "—" : formatPercent(serverRate)}
            delta={delta(serverRate, rate(prev, "server_errors"))}
            deltaLabel={prevLbl}
            upIs="bad"
            spark={sparks.server}
            loading={statsLoading}
          />
          <StatTile
            label="Client errors"
            description="Share of requests answered with a 4xx status."
            value={clientRate === null ? "—" : formatPercent(clientRate)}
            delta={delta(clientRate, rate(prev, "client_errors"))}
            deltaLabel={prevLbl}
            upIs="bad"
            spark={sparks.client}
            loading={statsLoading}
          />
          <StatTile
            label="Median latency"
            description="p50 server time across all requests."
            value={cur?.p50 != null ? formatMs(cur.p50) : "—"}
            delta={delta(cur?.p50 ?? null, prev?.p50 ?? null)}
            deltaLabel={prevLbl}
            upIs="bad"
            spark={sparks.p50}
            loading={statsLoading}
          />
          <StatTile
            label="p95 latency"
            description="95% of requests finished within this time."
            value={cur?.p95 != null ? formatMs(cur.p95) : "—"}
            delta={delta(cur?.p95 ?? null, prev?.p95 ?? null)}
            deltaLabel={prevLbl}
            upIs="bad"
            spark={sparks.p95}
            loading={statsLoading}
          />
          <StatTile
            label="Active users"
            description="Distinct signed-in users who made at least one request."
            value={cur ? formatCount(cur.users) : "—"}
            delta={delta(cur?.users ?? null, prev?.users ?? null)}
            deltaLabel={prevLbl}
            upIs="good"
            loading={statsLoading}
          />

          <ChartPanel className="span-8" title="Requests by status" description="Requests per interval, stacked by HTTP status class." q={traffic} viz={VIZ.traffic} height={260} />
          <ChartPanel className="span-4" title="Status codes" description="Most frequent response codes in this range." q={statusCodes} viz={VIZ.statusCodes} height={260} />

          <ChartPanel className="span-8" title="Latency percentiles" description="Server time per interval. p99 is the slowest 1% of requests." q={latency} viz={VIZ.latency} height={260} />
          <ChartPanel className="span-4" title="Error rate" description="Percent of requests per interval that returned 5xx or 4xx." q={errorRate} viz={VIZ.errorRate} height={260} />

          <Panel
            className="span-12"
            title="Routes"
            description="Per route and method, ordered by traffic. Latency percentiles are per route."
            result={routes.data}
            loading={routes.loading}
            error={routes.error}
            bodyHeight={200}
            table={routes.data && <DataTable result={routes.data} columns={routeColumns(routes.data)} maxHeight={420} />}
          />

          <Panel
            className="span-7"
            title="Top errors"
            description="4xx and 5xx responses grouped by error and route."
            result={errors.data}
            loading={errors.loading}
            error={errors.error}
            bodyHeight={200}
            table={errors.data && <DataTable result={errors.data} columns={errorColumns} maxHeight={360} />}
          />
          <Panel
            className="span-5"
            title="Most active users"
            result={users.data}
            loading={users.loading}
            error={users.error}
            bodyHeight={200}
            table={users.data && <DataTable result={users.data} columns={userColumns} maxHeight={360} />}
          />
        </div>
          </>
        ) : (
          <ConnectCard title="Connect your API" text="Requests, errors, latency, and the busiest routes and users show up here as soon as a service with the Visionary SDK serves its first request." />
        )}
      </div>
    </>
  );
}

function ChartPanel({
  q,
  viz,
  height,
  title,
  description,
  className,
}: {
  q: { data?: Result; loading: boolean; error?: Error };
  viz: Viz;
  height: number;
  title: string;
  description?: string;
  className: string;
}) {
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

function Status({ code }: { code: unknown }) {
  const n = toNumber(code) ?? 0;
  return (
    <span className={`status s${Math.floor(n / 100)}`}>
      <span className="dot" aria-hidden />
      {String(code)}
    </span>
  );
}

/** The number carries the value; color only adds emphasis past 1% and 5%. */
function RateCell({ value }: { value: unknown }) {
  const n = toNumber(value);
  if (n === null) return <span className="dim">—</span>;
  const cls = n >= 5 ? "bad" : n >= 1 ? "warn" : "";
  return <span className={`flag ${cls}`}>{n === 0 ? <span className="dim">0%</span> : formatPercent(n)}</span>;
}

function routeColumns(res: Result): Column[] {
  const ri = res.columns.indexOf("requests");
  const max = Math.max(1, ...res.rows.map((r) => toNumber(r[ri]) ?? 0));
  return [
    { key: "method", render: (v) => <span className="method">{String(v)}</span>, width: 70 },
    { key: "route", render: (v) => <span className="mono">{String(v)}</span> },
    {
      key: "requests",
      align: "right",
      render: (v) => {
        const n = toNumber(v) ?? 0;
        return (
          <span className="cell-bar">
            {formatCount(n)}
            <span className="track" aria-hidden>
              <span className="fill" style={{ display: "block", width: `${(100 * n) / max}%` }} />
            </span>
          </span>
        );
      },
    },
    { key: "error_rate", label: "5xx rate", align: "right", render: (v) => <RateCell value={v} /> },
    { key: "client_error_rate", label: "4xx rate", align: "right", render: (v) => <RateCell value={v} /> },
    { key: "avg_ms", label: "Avg", align: "right", unit: "ms" },
    { key: "p50_ms", label: "p50", align: "right", unit: "ms" },
    { key: "p95_ms", label: "p95", align: "right", unit: "ms" },
  ];
}

const errorColumns: Column[] = [
  { key: "status", render: (v) => <Status code={v} />, width: 70 },
  { key: "error", render: (v) => <span className="mono">{String(v)}</span> },
  { key: "route", render: (v) => <span className="mono dim">{String(v)}</span> },
  { key: "occurrences", align: "right", unit: "count" },
  { key: "last_seen", label: "Last seen", align: "right", render: (v) => <span className="dim">{formatRelative(String(v))}</span> },
];

const userColumns: Column[] = [
  {
    key: "handle",
    label: "User",
    render: (v, row) => (
      <span>
        {v ? `@${String(v)}` : <span className="dim">#{String(row[0])}</span>}
      </span>
    ),
  },
  { key: "requests", align: "right", unit: "count" },
  { key: "errors", align: "right", render: (v) => formatValue(v, "count") },
  { key: "avg_ms", label: "Avg", align: "right", unit: "ms" },
  { key: "last_seen", label: "Last seen", align: "right", render: (v) => <span className="dim">{formatRelative(String(v))}</span> },
];
