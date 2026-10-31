import { useState } from "react";
import { AlertTriangle, Camera, CheckCircle2, CircleDashed, Download, ListChecks, Network, Waypoints, XCircle } from "lucide-react";
import { downloadHar } from "../lib/har";
import { Waterfall } from "./Uptime.waterfall";
import { DataTable, type Column } from "../components/DataTable";
import { Drawer } from "../components/Modal";
import { Panel } from "../components/Panel";
import { ErrorNotice } from "../components/ErrorNotice";
import type { Result } from "../lib/api";
import { formatMs, toNumber } from "../lib/format";
import { formatLogTime, formatRelative } from "../lib/time";
import { traceHref } from "../lib/traces";
import { getBrowserRun, type BrowserRunSummary, type StepResult, type StepStat } from "../lib/uptime";
import { useQuery } from "../lib/useQuery";
import { StateBadge } from "./Uptime.parts";

const ms = (v: unknown) => (toNumber(v) === null ? <span className="dim">—</span> : formatMs(toNumber(v)!));
const cls = (v: unknown) => (toNumber(v) === null ? <span className="dim">—</span> : toNumber(v)!.toFixed(3));

const asResult = (columns: string[], rows: unknown[][]): Result => ({ columns, rows, truncated: false, rows_read: rows.length, duration_ms: 0, sql: "", bucket: 0, from: "", to: "" });

const RUN_KEYS = ["created_at", "ok", "duration_ms", "lcp_ms", "cls", "ttfb_ms", "failed_label", "error", "screenshot", "id", "degraded", "warning"] as const;
const runState = (ok: unknown, degraded: unknown) => (ok ? (degraded ? "degraded" : "up") : "down");

/** Runs of a browser check, newest first; a row opens the run. */
export function BrowserRunsPanel({ checkId, checkName, runs, loading, error }: { checkId: number; checkName: string; runs?: BrowserRunSummary[]; loading: boolean; error?: Error | null }) {
  const [open, setOpen] = useState<number | null>(null);
  const result = runs && asResult([...RUN_KEYS], runs.map((r) => RUN_KEYS.map((k) => r[k])));
  const cols: Column[] = [
    { key: "created_at", label: "Time", width: 190, render: (v) => <span className="mono">{formatLogTime(String(v))}</span> },
    { key: "ok", label: "Result", width: 110, render: (v, row) => <StateBadge state={runState(v, row[RUN_KEYS.indexOf("degraded")])} /> },
    { key: "duration_ms", label: "Total", align: "right", render: ms },
    { key: "lcp_ms", label: "LCP", align: "right", render: ms },
    { key: "cls", label: "CLS", align: "right", render: cls },
    { key: "ttfb_ms", label: "TTFB", align: "right", render: ms },
    { key: "failed_label", label: "Failed step", render: (v) => (v ? <span className="up-err">{String(v)}</span> : <span className="dim">—</span>) },
    { key: "error", label: "Error", render: (v, row) => (v ? <span className="up-err">{String(v)}</span> : row[RUN_KEYS.indexOf("warning")] ? <span className="up-warn">{String(row[RUN_KEYS.indexOf("warning")])}</span> : <span className="dim">—</span>) },
    { key: "screenshot", label: "", align: "right", width: 40, render: (v) => (v ? <Camera size={14} aria-label="Has a screenshot" className="dim" /> : null) },
  ];
  return (
    <>
      <Panel
        className="span-12"
        title="Runs"
        description="The latest 500 runs in this range, newest first. Click one for its steps, screenshot, console and network errors."
        result={result}
        loading={loading}
        error={error ?? undefined}
        bodyHeight={120}
        table={result && (runs!.length === 0 ? <div className="panel-state">No runs in this range.</div> : <DataTable result={result} columns={cols} maxHeight={440} sortable={false} onRowClick={(row) => setOpen(Number(row[RUN_KEYS.indexOf("id")]))} />)}
      />
      {open !== null && <RunDrawer checkId={checkId} checkName={checkName} runId={open} onClose={() => setOpen(null)} />}
    </>
  );
}

/** How each step did across the runs in range. */
export function StepStatsPanel({ steps, loading, error }: { steps?: StepStat[]; loading: boolean; error?: Error | null }) {
  const result = steps && asResult(["n", "label", "runs", "failed", "avg_ms", "p95_ms"], steps.map((s, i) => [i + 1, s.label, s.runs, s.failed, s.runs > s.failed ? s.avg_ms : null, s.runs > s.failed ? s.p95_ms : null]));
  return (
    <Panel
      className="span-12"
      title="Steps"
      description="Each step across the runs in this range: how often it ran and failed, and how long it took when it passed. Later steps run less often when an earlier one fails."
      result={result}
      loading={loading}
      error={error ?? undefined}
      bodyHeight={80}
      table={
        result &&
        (steps!.length === 0 ? (
          <div className="panel-state">No runs in this range.</div>
        ) : (
          <DataTable
            result={result}
            sortable={false}
            maxHeight={360}
            columns={[
              { key: "n", label: "#", width: 40, align: "right" },
              { key: "label", label: "Step" },
              { key: "runs", label: "Runs", align: "right", unit: "count" },
              { key: "failed", label: "Failed", align: "right", render: (v) => (toNumber(v) ? <span className="up-err">{String(v)}</span> : <span className="dim">0</span>) },
              { key: "avg_ms", label: "Average", align: "right", render: ms },
              { key: "p95_ms", label: "p95", align: "right", render: ms },
            ]}
          />
        ))
      }
    />
  );
}

/** The latest run at a glance, in place of the certificate tile. */
export function LastRunTile({ run }: { run?: BrowserRunSummary }) {
  return (
    <section className="panel span-2">
      <div className="stat">
        <div className="stat-label">Last run</div>
        <div className="stat-value">
          {!run ? "—" : run.ok ? run.steps : run.failed_step + 1}
          {run && <span className="stat-unit">{run.ok ? (run.steps === 1 ? "step passed" : "steps passed") : "failed at step"}</span>}
        </div>
        <div className="stat-delta">
          <span className="vs" style={{ marginLeft: 0 }}>
            {!run ? "No runs in this range" : run.ok ? formatRelative(run.created_at) : run.failed_label}
          </span>
        </div>
      </div>
    </section>
  );
}

const STEP_ICON = { passed: CheckCircle2, failed: XCircle, skipped: CircleDashed };

/** One run: which step failed and why, what the page looked like, and what it logged. */
export function RunDrawer({ checkId, checkName, runId, onClose }: { checkId: number; checkName: string; runId: number; onClose: () => void }) {
  const q = useQuery(`up-run-${checkId}-${runId}`, (signal) => getBrowserRun(checkId, runId, signal));
  const r = q.data;
  const [shot, setShot] = useState<number | null>(null);
  return (
    <Drawer onClose={onClose} title={r ? `Run at ${formatLogTime(r.created_at)}` : "Run"} className="wide">
      {q.error && <ErrorNotice title="Could not load the run" error={q.error} />}
      {!r && !q.error && <div className="panel-state">Loading…</div>}
      {r && (
        <div className="up-run">
          <dl className="kv">
            <dt>Result</dt>
            <dd>
              <StateBadge state={runState(r.ok, r.degraded)} />
              {r.retried && <span className="dim"> · after a retry</span>}
            </dd>
            <dt>Total</dt>
            <dd>{ms(r.duration_ms)}</dd>
            <dt>Web Vitals</dt>
            <dd>
              LCP {ms(r.lcp_ms)} · CLS {cls(r.cls)} · TTFB {ms(r.ttfb_ms)}
            </dd>
            {r.status > 0 && (
              <>
                <dt>Last page</dt>
                <dd>HTTP {r.status}</dd>
              </>
            )}
            {r.trace_id && (
              <>
                <dt>Trace</dt>
                <dd>
                  {r.trace_found ? (
                    <a className="btn sm" href={traceHref(r.trace_id)}>
                      <Waypoints size={14} />
                      Open trace
                    </a>
                  ) : (
                    <span className="dim" title={r.trace_id}>
                      No backend recorded this run's trace
                    </span>
                  )}
                </dd>
              </>
            )}
          </dl>
          {!r.ok && r.error && (
            <div className="notice up-coverage" role="alert">
              <AlertTriangle size={16} aria-hidden />
              <div className="up-coverage-text">
                <strong>Failed</strong>
                <span>{r.error}</span>
              </div>
            </div>
          )}
          {r.ok && r.warning && (
            <div className="notice up-coverage" role="status">
              <AlertTriangle size={16} aria-hidden />
              <div className="up-coverage-text">
                <strong>Degraded</strong>
                <span>{r.warning}</span>
              </div>
            </div>
          )}

          <h4 className="up-run-h">
            <ListChecks size={14} aria-hidden /> Steps
          </h4>
          <ol className="up-run-steps">
            {r.steps.map((s: StepResult, i) => {
              const Icon = STEP_ICON[s.status];
              const img = r.step_shots?.[i];
              return (
                <li key={i} className={`up-run-step ${s.status}`}>
                  <Icon size={14} aria-label={s.status} />
                  <div className="up-run-step-text">
                    <span>
                      {i + 1}. {s.label}
                    </span>
                    {s.error && <span className="up-err">{s.error}</span>}
                    {img && shot === i && (
                      <a href={img} target="_blank" rel="noreferrer" className="up-shot up-step-shot">
                        <img src={img} alt={`The page after step ${i + 1}`} />
                      </a>
                    )}
                  </div>
                  {img && (
                    <button type="button" className={`icon-btn ${shot === i ? "on" : ""}`} aria-label={shot === i ? "Hide the screenshot" : "Show the screenshot"} title={shot === i ? "Hide the screenshot" : "Screenshot after this step"} onClick={() => setShot(shot === i ? null : i)}>
                      <Camera size={14} />
                    </button>
                  )}
                  <span className="num dim">{s.status === "skipped" ? "skipped" : formatMs(s.duration_ms)}</span>
                </li>
              );
            })}
          </ol>

          {(r.screenshot || r.final_screenshot) && (
            <>
              <h4 className="up-run-h">
                <Camera size={14} aria-hidden /> {r.screenshot ? "When it failed" : "Last page"}
              </h4>
              <a href={r.screenshot || r.final_screenshot} target="_blank" rel="noreferrer" className="up-shot">
                <img src={r.screenshot || r.final_screenshot} alt={r.screenshot ? "The page when the step failed" : "The last page of the run"} />
              </a>
            </>
          )}

          {r.requests?.length > 0 && (
            <>
              <div className="up-run-h">
                <Network size={14} aria-hidden /> Requests
                <span className="dim" style={{ fontWeight: 400 }}>
                  {r.requests.length}
                </span>
                <span style={{ flex: 1 }} />
                <button type="button" className="btn sm" onClick={() => downloadHar(r, checkName)} title="HAR 1.2 of these requests, for a browser's network panel or a HAR viewer">
                  <Download size={14} />
                  Download HAR
                </button>
              </div>
              <Waterfall requests={r.requests} />
            </>
          )}

          <h4 className="up-run-h">Console errors</h4>
          {r.console.length === 0 ? (
            <div className="dim">None.</div>
          ) : (
            <ul className="up-run-list mono">
              {r.console.map((c, i) => (
                <li key={i}>{c}</li>
              ))}
            </ul>
          )}

          <h4 className="up-run-h">Failed requests</h4>
          {r.network.length === 0 ? (
            <div className="dim">None.</div>
          ) : (
            <ul className="up-run-list mono">
              {r.network.map((n, i) => (
                <li key={i}>
                  <span className="up-err">{n.status ? `HTTP ${n.status}` : n.error}</span> {n.url}
                  {n.type && <span className="dim"> · {n.type}</span>}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Drawer>
  );
}
