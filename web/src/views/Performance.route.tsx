import { useMemo, useState } from "react";
import type { Result, Viz } from "../lib/api";
import { formatBytes, formatCount, formatMs, formatPercent, toNumber } from "../lib/format";
import { records, useTelemetry } from "../lib/telemetry";
import { formatLogTime } from "../lib/time";
import { DataTable, type Column } from "../components/DataTable";
import { InfoTip } from "../components/InfoTip";
import { Drawer } from "../components/Modal";
import { VizView } from "../components/VizView";
import { RequestTelemetry } from "../components/telemetry/RequestTelemetry";
import { GroupRequests } from "./Performance.slow";
import "../components/telemetry/telemetry.css";

interface RouteDetailResponse {
  method: string;
  route: string;
  summary: Result;
  previous: Result;
  timeline: Result;
  statuses: Result;
  examples: RouteExample[];
}

/** A request of the route to show call by call, near one of its percentiles. */
interface RouteExample {
  label: "typical" | "slow" | "slowest";
  target_ms: number;
  log: Record<string, unknown>;
}

const EXAMPLE_LABEL: Record<RouteExample["label"], string> = { typical: "Typical", slow: "Slow (p95)", slowest: "Slowest" };
const EXAMPLE_ORDER: RouteExample["label"][] = ["typical", "slow", "slowest"];

const LATENCY: Viz = { type: "timeseries", x: "t", y: ["p50", "p95"], unit: "ms" };
const REQUESTS: Viz = { type: "bar", x: "t", y: ["requests"], unit: "count" };

function ms(v: unknown) {
  const n = toNumber(v);
  return n === null ? <span className="dim">—</span> : formatMs(n);
}

/**
 * Everything about one route in the dashboard range: its totals against the
 * previous period, latency and traffic over time, the status codes it
 * answers, one request call by call, and its slowest requests.
 */
export function RouteDetailDrawer({
  method,
  route,
  apdexMs,
  openLog,
  onOpenLog,
  onClose,
}: {
  method: string;
  route: string;
  apdexMs: number;
  openLog: number | null;
  onOpenLog: (id: number | null) => void;
  onClose: () => void;
}) {
  const q = useTelemetry<RouteDetailResponse>("routes/detail", { method, route });
  const d = q.data;
  const cur = records(d?.summary)[0];
  const prev = records(d?.previous)[0];

  return (
    <Drawer
      className="wide"
      title={
        <span className="span-name">
          <span className="method">{method}</span>
          <span className="mono">{route}</span>
        </span>
      }
      onClose={onClose}
    >
      {q.error && !d && <div className="notice error">{q.error.message}</div>}
      {!d ? (
        <div className="panel-state" style={{ height: 200 }} aria-busy />
      ) : (toNumber(cur?.requests) ?? 0) === 0 ? (
        <div className="panel-state">No requests to this route in the time range.</div>
      ) : (
        <div className="sd">
          <Stats cur={cur} prev={prev} apdexMs={apdexMs} />

          <section className="sd-section">
            <h3 className="rt-title">Over time</h3>
            <div className="sd-charts">
              <div>
                <div className="sd-chart-label">p50 and p95</div>
                <VizView result={d.timeline} viz={LATENCY} height={170} title={`${method} ${route} latency`} />
              </div>
              <div>
                <div className="sd-chart-label">Requests</div>
                <VizView result={d.timeline} viz={REQUESTS} height={170} title={`${method} ${route} requests`} />
              </div>
            </div>
          </section>

          {d.examples.length > 0 && (
            <section className="sd-section">
              <h3 className="rt-title">
                One request, call by call{" "}
                <InfoTip
                  text="A real request of this route with its calls on a timeline, picked near the route's typical (p50), p95 and slowest time. SDKs record calls for slow and failed requests only, so the typical one can be slower than the p50."
                  size={12}
                />
              </h3>
              <RouteWaterfall examples={d.examples} onOpenLog={onOpenLog} />
            </section>
          )}

          <section className="sd-section">
            <h3 className="rt-title">Status codes</h3>
            <DataTable result={d.statuses} columns={statusColumns} maxHeight={240} />
          </section>

          <section className="sd-section">
            <h3 className="rt-title">Slowest requests</h3>
            <GroupRequests method={method} route={route} excludes={[]} openLog={openLog} onOpenLog={onOpenLog} />
          </section>
        </div>
      )}
    </Drawer>
  );
}

/** One of the route's example requests as a waterfall, as the request view shows it. */
function RouteWaterfall({ examples, onOpenLog }: { examples: RouteExample[]; onOpenLog: (id: number | null) => void }) {
  const sorted = useMemo(() => [...examples].sort((a, b) => EXAMPLE_ORDER.indexOf(a.label) - EXAMPLE_ORDER.indexOf(b.label)), [examples]);
  const [pick, setPick] = useState<RouteExample["label"]>(() => (sorted.some((e) => e.label === "slow") ? "slow" : sorted[0].label));
  const ex = sorted.find((e) => e.label === pick) ?? sorted[0];
  const log = ex.log;
  const logId = toNumber(log.id);
  return (
    <div className="rw">
      <div className="rw-bar">
        {sorted.length > 1 && (
          <div className="segmented" role="radiogroup" aria-label="Example request">
            {sorted.map((e) => (
              <button type="button" key={e.label} role="radio" aria-checked={e.label === ex.label} className={e.label === ex.label ? "on" : ""} onClick={() => setPick(e.label)}>
                {EXAMPLE_LABEL[e.label]}
              </button>
            ))}
          </div>
        )}
        <span className="dim rw-meta">
          <span className="mono">{String(log.path ?? "")}</span> · {formatMs(toNumber(log.duration_ms) ?? 0)} · {String(log.status)} · {formatLogTime(String(log.created_at))}
        </span>
        {logId !== null && (
          <button type="button" className="btn ghost sm" onClick={() => onOpenLog(logId)}>
            Open request
          </button>
        )}
      </div>
      <RequestTelemetry key={String(log.request_id)} rec={log} />
    </div>
  );
}

function Stats({ cur, prev, apdexMs }: { cur: Record<string, unknown>; prev?: Record<string, unknown>; apdexMs: number }) {
  // upIs says whether a rise is good news, which colours the change.
  const change = (key: string, upIs: "good" | "bad") => {
    const a = toNumber(cur[key]);
    const b = toNumber(prev?.[key]);
    if (a === null || b === null || b === 0) return null;
    const pct = (100 * (a - b)) / b;
    if (Math.abs(pct) < 0.5) return null;
    const worse = upIs === "bad" ? pct > 0 : pct < 0;
    return <span className={`sd-change ${worse ? "up" : "down"}`}>{`${pct > 0 ? "+" : ""}${pct.toFixed(0)}% vs before`}</span>;
  };
  const n = (key: string) => toNumber(cur[key]);
  const dbShare = n("db_share");
  const tiles: { label: string; value: string; change?: ReturnType<typeof change>; tip?: string }[] = [
    { label: "Requests", value: formatCount(n("requests") ?? 0), change: change("requests", "good") },
    { label: "Busy time", value: formatMs(n("busy_ms") ?? 0), change: change("busy_ms", "bad"), tip: "The sum of this route's request durations: how much server time it took." },
    { label: "p50", value: formatMs(n("p50") ?? 0), change: change("p50", "bad") },
    { label: "p95", value: formatMs(n("p95") ?? 0), change: change("p95", "bad") },
    { label: "p99", value: formatMs(n("p99") ?? 0), change: change("p99", "bad") },
    { label: "Max", value: formatMs(n("max_ms") ?? 0) },
    { label: "5xx", value: formatPercent(n("error_rate") ?? 0), change: change("error_rate", "bad") },
    { label: "4xx", value: formatPercent(n("client_error_rate") ?? 0) },
    { label: `Apdex (${formatMs(apdexMs)})`, value: n("apdex") === null ? "—" : n("apdex")!.toFixed(2), change: change("apdex", "good") },
    {
      label: "DB time",
      value: dbShare === null ? "—" : `${formatPercent(Math.min(100, dbShare))}${n("db_calls") !== null ? ` · ${formatCount(n("db_calls")!)} calls` : ""}`,
      tip: "Share of request time spent in database calls, and database calls per request on average, from SDKs that measure it.",
    },
    { label: "Avg size", value: n("avg_response_bytes") === null ? "—" : formatBytes(n("avg_response_bytes")!) },
  ];
  return (
    <div className="sd-stats">
      {tiles.map((t) => (
        <div key={t.label} className="rt-total">
          <div className="rt-total-label">
            {t.label}
            {t.tip && <InfoTip text={t.tip} size={12} />}
          </div>
          <div className="rt-total-value">{t.value}</div>
          {t.change}
        </div>
      ))}
    </div>
  );
}

const statusColumns: Column[] = [
  {
    key: "status",
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
  { key: "requests", align: "right", unit: "count" },
  { key: "avg_ms", label: "Avg time", align: "right", render: ms },
];
