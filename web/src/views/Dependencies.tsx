import { useMemo, useState } from "react";
import type { Result, Viz } from "../lib/api";
import { formatCount, formatMs, formatPercent, toNumber } from "../lib/format";
import { useInsights, type DependenciesResponse } from "../lib/insights";
import { records } from "../lib/telemetry";
import { DataTable, type Column } from "../components/DataTable";
import { ErrorNotice } from "../components/ErrorNotice";
import { Panel } from "../components/Panel";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView } from "../components/VizView";
import { logIdFor } from "../components/telemetry/PerfPanels";
import { SpanDetailDrawer } from "../components/telemetry/SpanDetail";
import { KIND_COLOR } from "../components/telemetry/Waterfall";
import { LogDetail } from "./Logs";
import "../components/telemetry/perf.css";
import "./Insights.css";
import { HelpLink } from "../components/Help";

const P95: Viz = { type: "timeseries", x: "t", y: ["value"], series: "series", unit: "ms" };
const CALLS: Viz = { type: "timeseries", x: "t", y: ["calls", "errors"], unit: "count" };

function ms(v: unknown) {
  const n = toNumber(v);
  return n === null ? <span className="dim">—</span> : formatMs(n);
}

/** "+12%" style change of a value against the previous period, worse in red. */
function Change({ now, before, worseUp = true }: { now: unknown; before: unknown; worseUp?: boolean }) {
  const a = toNumber(now);
  const b = toNumber(before);
  if (a === null || b === null || b === 0) return null;
  const pct = ((a - b) / b) * 100;
  if (Math.abs(pct) < 10) return null;
  const worse = worseUp ? pct > 0 : pct < 0;
  return <span className={`q-change ${worse ? "up" : "down"}`}>{`${pct > 0 ? "+" : ""}${pct.toFixed(0)}%`}</span>;
}

/**
 * Third-party APIs and other services this one calls, each treated like a
 * route of its own: rate, errors, latency spread and their change since the
 * previous period, and which routes depend on them.
 */
export function Dependencies() {
  const q = useInsights<DependenciesResponse>("dependencies");
  const [open, setOpen] = useState<string | null>(null);
  const [openLog, setOpenLog] = useState<number | null>(null);
  const hosts = useMemo(() => records(q.data?.hosts), [q.data]);
  const totals = useMemo(() => {
    let calls = 0;
    let errors = 0;
    let perMin = 0;
    let slowest: Record<string, unknown> | undefined;
    for (const h of hosts) {
      calls += toNumber(h.calls) ?? 0;
      errors += toNumber(h.errors) ?? 0;
      perMin += toNumber(h.per_min) ?? 0;
      if (!slowest || (toNumber(h.p95) ?? 0) > (toNumber(slowest.p95) ?? 0)) slowest = h;
    }
    return { calls, errors, perMin, slowest };
  }, [hosts]);
  const columns = useMemo(() => (q.data ? hostColumns(q.data.hosts) : []), [q.data]);
  const openRequest = (requestId: string) => {
    logIdFor(requestId)
      .then((id) => id !== null && setOpenLog(id))
      .catch(() => undefined);
  };

  return (
    <>
      <TopBar
        title="Dependencies"
        section="API"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl busy={q.loading} />
          </>
        }
      />
      <div className="content">
        {q.error && !q.data && <ErrorNotice title="Could not load dependencies" error={q.error} style={{ marginBottom: 12 }} />}
        <div className="grid">
          <StatTile label="Outbound calls" value={formatCount(totals.calls)} footer={`${formatCount(totals.perMin)} per minute`} delta={null} deltaLabel="" upIs="neutral" loading={q.loading} />
          <StatTile
            label="Failed"
            description="Calls that failed to connect or answered 5xx."
            value={totals.calls ? formatPercent((100 * totals.errors) / totals.calls) : "—"}
            footer={`${formatCount(totals.errors)} calls`}
            delta={null}
            deltaLabel=""
            upIs="neutral"
            loading={q.loading}
          />
          <StatTile label="Hosts" value={formatCount(hosts.length)} delta={null} deltaLabel="" upIs="neutral" loading={q.loading} />
          <StatTile
            label="Slowest host (p95)"
            value={totals.slowest ? formatMs(toNumber(totals.slowest.p95) ?? 0) : "—"}
            footer={totals.slowest ? String(totals.slowest.host ?? totals.slowest.name) : undefined}
            delta={null}
            deltaLabel=""
            upIs="neutral"
            loading={q.loading}
          />

          <Panel className="span-6" title="p95 by host" description="The 95th percentile of each of the six busiest hosts, over time." result={q.data?.timeline} loading={q.loading} bodyHeight={220} chart={q.data && <VizView result={q.data.timeline} viz={P95} height={220} title="p95 by host" />} table={q.data && <DataTable result={q.data.timeline} maxHeight={220} />} />
          <Panel className="span-6" title="Calls and failures" description="Outbound calls and the ones that failed, over time." result={q.data?.errors} loading={q.loading} bodyHeight={220} chart={q.data && <VizView result={q.data.errors} viz={CALLS} height={220} title="Outbound calls and failures" />} table={q.data && <DataTable result={q.data.errors} maxHeight={220} />} />

          <Panel
            className="span-12"
            title="Hosts"
            description="Every host called, by total time spent waiting on it. Changes compare with the previous period of the same length. Open one for its timeline, distribution, URLs, routes and slowest calls."
            result={q.data?.hosts}
            loading={q.loading}
            error={q.error}
            bodyHeight={200}
            table={
              q.data &&
              (hosts.length === 0 ? (
                <div className="panel-state empty-why">
                  <div>No outbound calls in this range.</div>
                  <HelpLink topic="http">How to record outbound calls</HelpLink>
                </div>
              ) : (
                <DataTable result={q.data.hosts} columns={columns} maxHeight={560} onRowClick={(row) => setOpen(String(row[0]))} rowKey={(row) => row[0]} selectedKey={open ?? undefined} />
              ))
            }
          />
        </div>
      </div>
      {open !== null && <SpanDetailDrawer kind="http" name={open} onClose={() => setOpen(null)} onOpenRequest={openRequest} />}
      {openLog !== null && <LogDetail id={openLog} onClose={() => setOpenLog(null)} />}
    </>
  );
}

function hostColumns(res: Result): Column[] {
  const col = (name: string) => res.columns.indexOf(name);
  const ti = col("total_ms");
  const max = Math.max(1, ...res.rows.map((r) => toNumber(r[ti]) ?? 0));
  return [
    {
      key: "name",
      label: "Host",
      render: (v) => (
        <span className="span-name">
          <span className="wf-swatch" style={{ background: KIND_COLOR.http }} aria-hidden />
          <span className="mono">{String(v)}</span>
        </span>
      ),
    },
    { key: "calls", align: "right", unit: "count", width: 70 },
    { key: "per_min", label: "Per min", align: "right", render: (v) => formatCount(toNumber(v) ?? 0), width: 70 },
    {
      key: "error_rate",
      label: "Failed",
      align: "right",
      width: 110,
      render: (v, row) => {
        const n = toNumber(v) ?? 0;
        return (
          <span className="q-cell">
            <Change now={v} before={row[col("prev_error_rate")]} />
            <span className={n >= 5 ? "flag bad" : n > 0 ? "flag warn" : "dim"}>{formatPercent(n)}</span>
          </span>
        );
      },
    },
    { key: "p50", align: "right", render: ms, width: 70 },
    {
      key: "p95",
      align: "right",
      width: 120,
      render: (v, row) => (
        <span className="q-cell">
          <Change now={v} before={row[col("prev_p95")]} />
          {ms(v)}
        </span>
      ),
    },
    { key: "p99", align: "right", render: ms, width: 70 },
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
      width: 230,
    },
  ];
}
