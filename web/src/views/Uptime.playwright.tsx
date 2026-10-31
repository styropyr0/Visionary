import { useState } from "react";
import { AlertTriangle, Camera, CheckCircle2, Download, ExternalLink, FileArchive, ListChecks, Paperclip, XCircle } from "lucide-react";
import { DataTable, type Column } from "../components/DataTable";
import { Drawer } from "../components/Modal";
import { Panel } from "../components/Panel";
import { Select } from "../components/Select";
import { ErrorNotice } from "../components/ErrorNotice";
import type { Result } from "../lib/api";
import { shortSha } from "../lib/ci";
import { formatMs, toNumber } from "../lib/format";
import { formatLogTime, formatRelative } from "../lib/time";
import type { CheckInput } from "../lib/uptime";
import { getPlaywrightRun, PLAYWRIGHT_INTERVALS, traceURL, type PlaywrightRunSummary } from "../lib/uptimePlaywright";
import { useQuery } from "../lib/useQuery";
import { StateBadge } from "./Uptime.parts";

const ms = (v: unknown) => (toNumber(v) === null ? <span className="dim">—</span> : formatMs(toNumber(v)!));

const asResult = (columns: string[], rows: unknown[][]): Result => ({ columns, rows, truncated: false, rows_read: rows.length, duration_ms: 0, sql: "", bucket: 0, from: "", to: "" });

export const BLANK_PLAYWRIGHT: Partial<CheckInput> = {
  kind: "playwright",
  url: "",
  interval_s: 3600,
  timeout_ms: 0,
  playwright: { project: "", title: "" },
};

export function everyLabel(s: number): string {
  if (s % 86400 === 0) return `${s / 86400}d`;
  if (s % 3600 === 0) return `${s / 3600}h`;
  return `${Math.round(s / 60)}m`;
}

export function PlaywrightFields({ form, onChange }: { form: CheckInput; onChange: (patch: Partial<CheckInput>) => void }) {
  const pw = form.playwright ?? { project: "", title: "" };
  const options = PLAYWRIGHT_INTERVALS.map((s) => ({ value: String(s), label: `Every ${everyLabel(s)}` }));
  if (!PLAYWRIGHT_INTERVALS.includes(form.interval_s)) options.push({ value: String(form.interval_s), label: `Every ${everyLabel(form.interval_s)}` });
  return (
    <>
      <div className="up-row">
        <label className="field" style={{ flex: 1 }}>
          <span>Project</span>
          <input className="input mono" value={pw.project} onChange={(e) => onChange({ playwright: { ...pw, project: e.target.value } })} placeholder="chromium" maxLength={200} />
          <small>Its name in playwright.config; empty without projects.</small>
        </label>
        <div className="field" style={{ flex: 1 }}>
          <span>Expect a run</span>
          <Select label="Expected interval" value={String(form.interval_s)} align="left" onChange={(v) => onChange({ interval_s: Number(v) })} options={options} />
          <small>Without a run for longer, the check is stale and counts as down.</small>
        </div>
      </div>
      <label className="field">
        <span>Test</span>
        <input className="input mono" value={pw.title} onChange={(e) => onChange({ playwright: { ...pw, title: e.target.value } })} placeholder="checkout.spec.ts › Checkout › pays by card" maxLength={1000} required />
        <small>The file and titles as Playwright lists them, joined by ›. Runs reported for this project and test land on this check.</small>
      </label>
      <label className="field">
        <span>URL</span>
        <input className="input mono" value={form.url} onChange={(e) => onChange({ url: e.target.value })} placeholder="Optional: the page the test covers" />
      </label>
    </>
  );
}

const RUN_KEYS = ["created_at", "ok", "duration_ms", "retry", "browser", "failed_step", "error", "commit", "screenshot", "id"] as const;

export function PlaywrightRunsPanel({ checkId, runs, loading, error }: { checkId: number; runs?: PlaywrightRunSummary[]; loading: boolean; error?: Error | null }) {
  const [open, setOpen] = useState<number | null>(null);
  const result = runs && asResult([...RUN_KEYS], runs.map((r) => RUN_KEYS.map((k) => r[k])));
  const cols: Column[] = [
    { key: "created_at", label: "Reported", width: 190, render: (v) => <span className="mono">{formatLogTime(String(v))}</span> },
    { key: "ok", label: "Result", width: 110, render: (v) => <StateBadge state={v ? "up" : "down"} /> },
    { key: "duration_ms", label: "Duration", align: "right", render: ms },
    { key: "retry", label: "Retries", align: "right", render: (v) => (toNumber(v) ? String(v) : <span className="dim">0</span>) },
    { key: "browser", label: "Browser", render: (v) => (v ? String(v) : <span className="dim">—</span>) },
    { key: "failed_step", label: "Failed step", render: (v) => (v ? <span className="up-err">{String(v)}</span> : <span className="dim">—</span>) },
    { key: "error", label: "Error", render: (v) => (v ? <span className="up-err">{String(v)}</span> : <span className="dim">—</span>) },
    { key: "commit", label: "Commit", render: (v) => (v ? <span className="mono">{shortSha(String(v))}</span> : <span className="dim">—</span>) },
    { key: "screenshot", label: "", align: "right", width: 40, render: (v) => (v ? <Camera size={14} aria-label="Has a screenshot" className="dim" /> : null) },
  ];
  return (
    <>
      <Panel
        className="span-12"
        title="Runs"
        description="Runs your Playwright tests reported in this range, newest first, after their retries. Click one for its steps, screenshot and trace."
        result={result}
        loading={loading}
        error={error ?? undefined}
        bodyHeight={120}
        table={
          result &&
          (runs!.length === 0 ? (
            <div className="panel-state">No runs reported in this range.</div>
          ) : (
            <DataTable result={result} columns={cols} maxHeight={440} sortable={false} onRowClick={(row) => setOpen(Number(row[RUN_KEYS.indexOf("id")]))} />
          ))
        }
      />
      {open !== null && <PlaywrightRunDrawer checkId={checkId} runId={open} onClose={() => setOpen(null)} />}
    </>
  );
}

export function PlaywrightLastTile({ run, intervalS }: { run?: PlaywrightRunSummary; intervalS: number }) {
  return (
    <section className="panel span-2">
      <div className="stat">
        <div className="stat-label">Last run</div>
        <div className="stat-value">{run ? (run.ok ? "Passed" : "Failed") : "—"}</div>
        <div className="stat-delta">
          <span className="vs" style={{ marginLeft: 0 }}>
            {run ? `${formatRelative(run.created_at)}${run.browser ? ` · ${run.browser}` : ""}` : `expected every ${everyLabel(intervalS)}`}
          </span>
        </div>
      </div>
    </section>
  );
}

function PlaywrightRunDrawer({ checkId, runId, onClose }: { checkId: number; runId: number; onClose: () => void }) {
  const q = useQuery(`up-pw-run-${checkId}-${runId}`, (signal) => getPlaywrightRun(checkId, runId, signal));
  const r = q.data;
  return (
    <Drawer onClose={onClose} title={r ? `Run at ${formatLogTime(r.created_at)}` : "Run"} className="wide">
      {q.error && <ErrorNotice title="Could not load the run" error={q.error} />}
      {!r && !q.error && <div className="panel-state">Loading…</div>}
      {r && (
        <div className="up-run">
          <dl className="kv">
            <dt>Result</dt>
            <dd>
              <StateBadge state={r.ok ? "up" : "down"} />
              {r.outcome === "flaky" && <span className="dim"> · flaky, passed on retry {r.retry}</span>}
              {r.outcome !== "flaky" && r.retry > 0 && <span className="dim"> · retry {r.retry}</span>}
            </dd>
            <dt>Duration</dt>
            <dd>{ms(r.duration_ms)}</dd>
            {(r.project || r.browser) && (
              <>
                <dt>Project</dt>
                <dd>{[r.project, r.browser].filter(Boolean).join(" · ")}</dd>
              </>
            )}
            {(r.release || r.commit) && (
              <>
                <dt>Release</dt>
                <dd className="mono">{[r.release, r.commit && shortSha(r.commit)].filter(Boolean).join(" · ")}</dd>
              </>
            )}
            {r.trace_id && (
              <>
                <dt>Trace</dt>
                <dd>
                  <a className="mono" href={`#/traces/${r.trace_id}`}>
                    {r.trace_id}
                  </a>
                </dd>
              </>
            )}
          </dl>
          {!r.ok && r.error && (
            <div className="notice up-coverage" role="alert">
              <AlertTriangle size={16} aria-hidden />
              <div className="up-coverage-text">
                <strong>Failed</strong>
                <span className="up-pw-error mono">{r.error}</span>
              </div>
            </div>
          )}

          <h4 className="up-run-h">
            <ListChecks size={14} aria-hidden /> Steps
          </h4>
          {r.steps.length === 0 ? (
            <div className="dim">The test reported no steps.</div>
          ) : (
            <ol className="up-run-steps">
              {r.steps.map((s, i) => {
                const Icon = s.error ? XCircle : CheckCircle2;
                return (
                  <li key={i} className={`up-run-step ${s.error ? "failed" : "passed"}`} style={s.depth ? { paddingLeft: 16 * s.depth } : undefined}>
                    <Icon size={14} aria-label={s.error ? "failed" : "passed"} />
                    <div className="up-run-step-text">
                      <span>{s.title}</span>
                      {s.error && <span className="up-err">{s.error}</span>}
                    </div>
                    <span className="num dim">{formatMs(s.duration_ms)}</span>
                  </li>
                );
              })}
            </ol>
          )}

          {r.screenshot && (
            <>
              <h4 className="up-run-h">
                <Camera size={14} aria-hidden /> Screenshot
              </h4>
              <a href={r.screenshot} target="_blank" rel="noreferrer" className="up-shot">
                <img src={r.screenshot} alt="The page the test left" />
              </a>
            </>
          )}

          <h4 className="up-run-h">
            <Paperclip size={14} aria-hidden /> Attachments
          </h4>
          {r.attachments.length === 0 ? (
            <div className="dim">None.</div>
          ) : (
            <ul className="up-run-list">
              {r.attachments.map((a, i) => (
                <li key={i}>
                  <span className="mono">{a.name}</span>
                  <span className="dim">
                    {" "}
                    · {a.content_type || "file"}
                    {a.size > 0 && ` · ${formatSize(a.size)}`}
                    {!a.stored && " · not uploaded"}
                  </span>
                </li>
              ))}
            </ul>
          )}
          {r.trace && (
            <div className="up-pw-trace">
              <a className="btn sm" href={traceURL(checkId, r.id)} download>
                <Download size={14} />
                Download trace
              </a>
              <a className="btn sm ghost" href="https://trace.playwright.dev" target="_blank" rel="noreferrer">
                <FileArchive size={14} />
                Open the trace viewer
                <ExternalLink size={12} />
              </a>
            </div>
          )}
        </div>
      )}
    </Drawer>
  );
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
