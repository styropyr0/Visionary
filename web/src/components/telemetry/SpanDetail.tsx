import { useMemo, useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react";
import type { Result, Viz } from "../../lib/api";
import { formatCount, formatMs, formatPercent, toNumber } from "../../lib/format";
import { formatLogTime } from "../../lib/time";
import { N_PLUS_ONE, records, useTelemetry } from "../../lib/telemetry";
import { DataTable, type Column } from "../DataTable";
import { InfoTip } from "../InfoTip";
import { CopyButton, Drawer } from "../Modal";
import { VizView } from "../VizView";
import { QueryPlan } from "./QueryPlan";
import { KIND_COLOR } from "./Waterfall";
import "./telemetry.css";

export interface SpanDetailResponse {
  kind: string;
  name: string;
  summary: Result;
  previous: Result;
  timeline: Result;
  bands: Result;
  variants: Result;
  routes: Result;
  slowest: Result;
  errors: Result;
}

const TIMELINE: Viz = { type: "timeseries", x: "t", y: ["p95", "avg_ms"], unit: "ms" };
const CALLS: Viz = { type: "bar", x: "t", y: ["calls"], unit: "count" };
const BANDS: Viz = { type: "bar", x: "band", y: ["calls"], unit: "count" };

function ms(v: unknown) {
  const n = toNumber(v);
  return n === null ? <span className="dim">—</span> : formatMs(n);
}

/**
 * Everything about one statement (db) or host (http) in the dashboard range:
 * how it behaves over time and across calls, which statements or URLs it
 * covers, which routes make it and how often per request, and its slowest
 * calls, each opening the request it belongs to.
 */
export function SpanDetailDrawer({
  kind,
  name,
  detail,
  onClose,
  onOpenRequest,
}: {
  kind: string;
  name: string;
  /** One statement (or URL) of the name only, as the query catalogue lists them. */
  detail?: string;
  onClose: () => void;
  onOpenRequest: (requestId: string) => void;
}) {
  const q = useTelemetry<SpanDetailResponse>("spans/detail", detail === undefined ? { kind, name } : { kind, name, detail });
  const d = q.data;
  const cur = records(d?.summary)[0];
  const prev = records(d?.previous)[0];
  const isDB = kind === "db";

  return (
    <Drawer
      className="wide"
      title={
        <span className="span-name">
          <span className="wf-swatch" style={{ background: KIND_COLOR[kind] ?? KIND_COLOR.custom }} aria-hidden />
          <span className="mono">{name}</span>
        </span>
      }
      onClose={onClose}
    >
      {q.error && !d && <div className="notice error">{q.error.message}</div>}
      {!d ? (
        <div className="panel-state" style={{ height: 200 }} aria-busy />
      ) : (toNumber(cur?.calls) ?? 0) === 0 ? (
        <div className="panel-state">No calls in this time range.</div>
      ) : (
        <div className="sd">
          <Stats cur={cur} prev={prev} isDB={isDB} />

          <section className="sd-section">
            <h3 className="rt-title">Over time</h3>
            <div className="sd-charts">
              <div>
                <div className="sd-chart-label">p95 and average</div>
                <VizView result={d.timeline} viz={TIMELINE} height={170} title={`${name} latency`} />
              </div>
              <div>
                <div className="sd-chart-label">Calls</div>
                <VizView result={d.timeline} viz={CALLS} height={170} title={`${name} calls`} />
              </div>
            </div>
          </section>

          <section className="sd-section">
            <h3 className="rt-title">
              Distribution{" "}
              <InfoTip text="How many calls fell in each latency band. Two humps usually mean two paths: a cache hit and a miss, an indexed and an unindexed lookup, or a warm and a cold connection." size={12} />
            </h3>
            <VizView result={d.bands} viz={BANDS} height={150} title={`${name} latency distribution`} />
          </section>

          <section className="sd-section">
            <h3 className="rt-title">{isDB ? (detail === undefined ? "Statements" : "Statement") : detail === undefined ? "URLs" : "URL"}</h3>
            <Variants result={d.variants} isDB={isDB} openFirst={detail !== undefined} />
          </section>

          <section className="sd-section">
            <h3 className="rt-title">
              Routes{" "}
              <InfoTip text={`The routes that make this call. Calls per request above ${N_PLUS_ONE - 1} usually mean a loop (an N+1). Share is this call's part of the route's total time.`} size={12} />
            </h3>
            <DataTable result={d.routes} columns={routeColumns} maxHeight={280} />
          </section>

          <section className="sd-section">
            <h3 className="rt-title">Slowest calls</h3>
            <DataTable
              result={d.slowest}
              columns={slowestColumns(isDB)}
              maxHeight={320}
              onRowClick={(row) => {
                const rid = row[d.slowest.columns.indexOf("request_id")];
                if (rid) onOpenRequest(String(rid));
              }}
            />
          </section>

          {d.errors.rows.length > 0 && (
            <section className="sd-section">
              <h3 className="rt-title">Errors</h3>
              <ul className="rt-errors">
                {records(d.errors).map((e) => (
                  <li key={String(e.error)} className="sd-error">
                    <AlertTriangle size={13} aria-hidden />
                    <span className="mono sd-error-msg">{String(e.error) || "error"}</span>
                    <span className="pill">×{formatCount(toNumber(e.calls) ?? 0)}</span>
                    <span className="dim">{formatLogTime(String(e.last_seen))}</span>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </div>
      )}
    </Drawer>
  );
}

function Stats({ cur, prev, isDB }: { cur: Record<string, unknown>; prev?: Record<string, unknown>; isDB: boolean }) {
  const calls = toNumber(cur.calls) ?? 0;
  const errors = toNumber(cur.errors) ?? 0;
  const change = (key: string) => {
    const a = toNumber(cur[key]);
    const b = toNumber(prev?.[key]);
    if (a === null || b === null || b === 0) return null;
    const pct = (100 * (a - b)) / b;
    if (Math.abs(pct) < 0.5) return null;
    return <span className={`sd-change ${pct > 0 ? "up" : "down"}`}>{`${pct > 0 ? "+" : ""}${pct.toFixed(0)}% vs before`}</span>;
  };
  const tiles: { label: string; value: string; key?: string; tip?: string }[] = [
    { label: "Calls", value: formatCount(calls), key: "calls" },
    { label: "Total time", value: formatMs(toNumber(cur.total_ms) ?? 0), key: "total_ms" },
    { label: "p50", value: formatMs(toNumber(cur.p50) ?? 0), key: "p50" },
    { label: "p95", value: formatMs(toNumber(cur.p95) ?? 0), key: "p95" },
    { label: "p99", value: formatMs(toNumber(cur.p99) ?? 0), key: "p99" },
    { label: "Max", value: formatMs(toNumber(cur.max_ms) ?? 0) },
    { label: "Errors", value: errors ? `${formatCount(errors)} (${formatPercent((100 * errors) / Math.max(calls, 1))})` : "0" },
    {
      label: "Requests",
      value: formatCount(toNumber(cur.requests) ?? 0),
      tip: (toNumber(cur.background) ?? 0) > 0 ? `${formatCount(toNumber(cur.background) ?? 0)} more calls ran outside any request (jobs, startup).` : undefined,
    },
  ];
  if (isDB && cur.avg_rows != null) tiles.push({ label: "Rows", value: `${formatCount(toNumber(cur.avg_rows) ?? 0)} avg · ${formatCount(toNumber(cur.max_rows) ?? 0)} max` });
  return (
    <div className="sd-stats">
      {tiles.map((t) => (
        <div key={t.label} className="rt-total">
          <div className="rt-total-label">
            {t.label}
            {t.tip && <InfoTip text={t.tip} size={12} />}
          </div>
          <div className="rt-total-value">{t.value}</div>
          {t.key && change(t.key)}
        </div>
      ))}
    </div>
  );
}

/** The statements (or URLs) under the name, each expandable to its full text. */
function Variants({ result, isDB, openFirst }: { result: Result; isDB: boolean; openFirst: boolean }) {
  const [open, setOpen] = useState<Set<number>>(() => new Set(openFirst ? [0] : []));
  const rows = useMemo(() => records(result), [result]);
  const max = Math.max(1, ...rows.map((r) => toNumber(r.total_ms) ?? 0));
  return (
    <div className="sd-variants">
      {rows.map((r, i) => {
        const isOpen = open.has(i);
        const total = toNumber(r.total_ms) ?? 0;
        const errors = toNumber(r.errors) ?? 0;
        return (
          <div key={i} className={`sd-variant ${isOpen ? "open" : ""}`}>
            <button
              type="button"
              className="sd-variant-head"
              aria-expanded={isOpen}
              onClick={() =>
                setOpen((o) => {
                  const n = new Set(o);
                  if (n.has(i)) n.delete(i);
                  else n.add(i);
                  return n;
                })
              }
            >
              {isOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
              <span className="mono sd-variant-text">{String(r.detail) || "(no detail)"}</span>
              <span className="sd-variant-stats">
                <span>{formatCount(toNumber(r.calls) ?? 0)}×</span>
                <span>p95 {ms(r.p95)}</span>
                {errors > 0 && <span className="flag bad">{formatCount(errors)} failed</span>}
                <span className="cell-bar sd-variant-total">
                  {formatMs(total)}
                  <span className="track" aria-hidden>
                    <span className="fill" style={{ display: "block", width: `${(100 * total) / max}%` }} />
                  </span>
                </span>
              </span>
            </button>
            {isOpen && (
              <div className="sd-variant-body">
                <div className="sd-variant-meta dim">
                  avg {ms(r.avg_ms)} · max {ms(r.max_ms)}
                  {isDB && r.avg_rows != null && <> · {formatCount(toNumber(r.avg_rows) ?? 0)} rows avg</>}
                  {r.target ? <> · {String(r.target)}</> : null}
                  <span style={{ marginLeft: "auto" }}>
                    <CopyButton text={String(r.detail)} />
                  </span>
                </div>
                <pre className="code mono wf-sql">{String(r.detail)}</pre>
                {isDB && r.detail && r.target ? <QueryPlan target={String(r.target)} statement={String(r.detail)} /> : null}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

const routeColumns: Column[] = [
  {
    key: "route",
    render: (v, row) => (
      <span>
        <span className="method">{String(row[0])}</span> <span className="mono">{String(v)}</span>
      </span>
    ),
  },
  { key: "requests", align: "right", unit: "count", width: 80 },
  {
    key: "calls_per_request",
    label: "Per request",
    align: "right",
    width: 100,
    render: (v, row) => {
      const avg = toNumber(v) ?? 0;
      const most = toNumber(row[5]) ?? 0;
      const loop = most >= N_PLUS_ONE;
      return (
        <span className={loop ? "flag warn" : undefined} title={`Up to ${formatCount(most)} in one request`}>
          ×{formatCount(avg)}
        </span>
      );
    },
  },
  { key: "total_ms", label: "Time", align: "right", render: ms, width: 80 },
  { key: "time_share", label: "Share", align: "right", render: (v) => (v == null ? <span className="dim">—</span> : formatPercent(toNumber(v) ?? 0)), width: 70 },
];

function slowestColumns(isDB: boolean): Column[] {
  const cols: Column[] = [
    { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
    { key: "duration_ms", label: "Duration", align: "right", render: ms, width: 84 },
    { key: "route", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">background</span>) },
    { key: "offset_ms", label: "At", align: "right", render: (v) => (v == null ? <span className="dim">—</span> : `+${formatMs(toNumber(v) ?? 0)}`), width: 76 },
  ];
  if (isDB) cols.push({ key: "row_count", label: "Rows", align: "right", unit: "count", width: 64 });
  cols.push({
    key: "status",
    width: 64,
    render: (v, row) =>
      v === "error" ? (
        <span className="flag bad" title={String(row[3] ?? "error")}>
          <AlertTriangle size={12} /> error
        </span>
      ) : (
        <span className="dim">ok</span>
      ),
  });
  return cols;
}
