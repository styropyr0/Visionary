import { useState } from "react";
import { ArrowLeft, ChevronDown, ChevronRight, ExternalLink, GitBranch, GitCommitHorizontal, Rocket, UserRound } from "lucide-react";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatCount, formatMs, formatPercent } from "../lib/format";
import { formatDateTime, formatRelative } from "../lib/time";
import { ciApi, formatDuration, outcomeOf, shortSha, type CIDeploy, type CIJob, type CIRun, type CITest, type Traffic } from "../lib/ci";
import { RefreshControl, TopBar } from "../components/Toolbar";
import { StatTile } from "../components/StatTile";
import { ErrorNotice } from "../components/ErrorNotice";
import { Outcome, TestName } from "./CI.parts";
import { CommitChecks } from "./Uptime.runs";

export function CIRunDetail({ id }: { id: number }) {
  const { tick } = useApp();
  const q = useQuery(`ci-run?${id}#${tick}`, (signal) => ciApi.run(id, signal));
  const d = q.data;
  const run = d?.run;
  const notFound = q.error?.message.toLowerCase().includes("not found");
  return (
    <>
      <TopBar
        title={run ? `${run.workflow || "Run"}${run.number ? ` #${run.number}` : ""}` : "Run"}
        section="CI"
        actions={
          <>
            <RefreshControl busy={q.loading} />
            {run?.url && (
              <a className="btn" href={run.url} target="_blank" rel="noreferrer">
                <ExternalLink size={14} />
                Open in {run.provider === "github" ? "GitHub" : "CI"}
              </a>
            )}
          </>
        }
      />
      <div className="content">
        <a className="ci-back" href="#/ci">
          <ArrowLeft size={14} />
          All runs
        </a>
        {notFound && <div className="notice">This run is no longer stored.</div>}
        {q.error && !notFound && !d && <ErrorNotice title="Could not load the run" error={q.error} />}
        {!d && !q.error && <div className="panel-state" style={{ height: 240 }} />}
        {d && run && (
          <>
            <RunHead run={run} />
            <div className="grid ci-tiles">
              <StatTile label="Duration" value={run.status === "completed" ? formatDuration(run.duration_ms) : "running"} delta={null} deltaLabel="" upIs="neutral" footer={run.finished_at ? `finished ${formatRelative(run.finished_at)}` : "so far"} />
              <StatTile label="Queue" value={formatDuration(run.queue_ms)} description="From the run being queued until its first job started on a runner." delta={null} deltaLabel="" upIs="neutral" footer="waiting for a runner" />
              <StatTile label="Jobs" value={String(d.jobs.length)} delta={null} deltaLabel="" upIs="neutral" footer={`${d.jobs.filter((j) => ["failure", "timed_out"].includes(j.conclusion)).length} failed`} />
              <StatTile label="Tests" value={formatCount(d.tests.tests)} delta={null} deltaLabel="" upIs="neutral" footer={d.tests.tests ? `${d.tests.passed} passed, ${d.tests.skipped} skipped` : "none uploaded"} />
              <StatTile label="Failed tests" value={formatCount(d.tests.failed)} delta={null} deltaLabel="" upIs="bad" footer={d.failed.some((t) => t.flaky) ? `${d.failed.filter((t) => t.flaky).length} known flaky` : "in this run"} />
              <StatTile label="Deploys" value={String(d.deploys.length)} description="Releases whose version names this commit." delta={null} deltaLabel="" upIs="neutral" footer={d.deploys[0] ? `${d.deploys[0].service} ${formatRelative(d.deploys[0].deployed_at)}` : "not deployed yet"} />
            </div>
            <JobTimeline run={run} jobs={d.jobs} />
            {d.deploys.length > 0 && <Deploys deploys={d.deploys} />}
            <CommitChecks batches={d.checks ?? []} />
            <Tests failed={d.failed} slowest={d.slowest} total={d.tests.tests} />
          </>
        )}
      </div>
    </>
  );
}

function RunHead({ run }: { run: CIRun }) {
  return (
    <section className="panel ci-head">
      <div className="ci-head-top">
        <Outcome outcome={outcomeOf(run)} />
        <h2>{run.commit_message || `${run.workflow} #${run.number}`}</h2>
      </div>
      <div className="ci-head-meta dim">
        <span>
          <GitBranch size={13} aria-hidden /> <span className="mono">{run.branch || "—"}</span>
        </span>
        {run.commit && (
          <span title={run.commit}>
            <GitCommitHorizontal size={13} aria-hidden /> <span className="mono">{shortSha(run.commit)}</span>
          </span>
        )}
        {run.actor && (
          <span>
            <UserRound size={13} aria-hidden /> {run.actor}
          </span>
        )}
        {run.event && <span>on {run.event.replace(/_/g, " ")}</span>}
        {run.repo && <span>{run.repo}</span>}
        {run.service && <span>service {run.service}</span>}
        {run.attempt > 1 && <span>attempt {run.attempt}</span>}
        <span title={formatDateTime(new Date(run.created_at))}>queued {formatRelative(run.created_at)}</span>
      </div>
    </section>
  );
}

const ts = (v: string | undefined) => (v ? Date.parse(v) : NaN);

function JobTimeline({ run, jobs }: { run: CIRun; jobs: CIJob[] }) {
  const [open, setOpen] = useState<Record<number, boolean>>({});
  const now = Date.now();
  const starts = [ts(run.created_at), ...jobs.map((j) => ts(j.created_at))].filter((n) => !Number.isNaN(n));
  const ends = [ts(run.finished_at), ...jobs.map((j) => ts(j.finished_at) || (j.status === "completed" ? NaN : now))].filter((n) => !Number.isNaN(n));
  const t0 = Math.min(...starts);
  const t1 = Math.max(t0 + 1000, ...ends);
  const pos = (t: number) => `${(100 * (t - t0)) / (t1 - t0)}%`;
  const width = (a: number, b: number) => `${Math.max(0.4, (100 * (b - a)) / (t1 - t0))}%`;
  if (jobs.length === 0) {
    return (
      <section className="panel ci-section">
        <h3 className="ci-section-title">Jobs</h3>
        <div className="panel-state">No jobs reported for this run. GitHub sends them with the Workflow jobs webhook event; other CI systems list them in the run they post.</div>
      </section>
    );
  }
  return (
    <section className="panel ci-section">
      <h3 className="ci-section-title">Jobs</h3>
      <div className="ci-axis dim" aria-hidden>
        <span>{formatDateTime(new Date(t0))}</span>
        <span>{formatDuration(t1 - t0)}</span>
      </div>
      <ul className="ci-gantt">
        {jobs.map((j) => {
          const created = ts(j.created_at);
          const started = ts(j.started_at);
          const finished = ts(j.finished_at) || (j.status === "completed" ? started : now);
          const outcome = outcomeOf(j);
          const expanded = !!open[j.id];
          return (
            <li key={j.id}>
              <button type="button" className="ci-gantt-row" aria-expanded={expanded} onClick={() => setOpen({ ...open, [j.id]: !expanded })}>
                <span className="ci-gantt-label">
                  {j.steps.length > 0 ? expanded ? <ChevronDown size={13} aria-hidden /> : <ChevronRight size={13} aria-hidden /> : <span className="ci-gantt-pad" />}
                  <Outcome outcome={outcome} compact />
                  <span className="ci-gantt-name" title={j.name}>{j.name}</span>
                </span>
                <span className="ci-gantt-track">
                  {!Number.isNaN(created) && !Number.isNaN(started) && started > created && (
                    <span className="ci-bar queue" style={{ left: pos(created), width: width(created, started) }} title={`Queued ${formatDuration(started - created)}`} />
                  )}
                  {!Number.isNaN(started) && (
                    <span className={`ci-bar ${outcome}`} style={{ left: pos(started), width: width(started, finished) }} title={`${formatDuration(finished - started)}${j.runner ? ` on ${j.runner}` : ""}`} />
                  )}
                </span>
                <span className="ci-gantt-time num">
                  {formatDuration(j.duration_ms ?? (Number.isNaN(started) ? null : finished - started))}
                  {j.queue_ms ? <span className="dim"> +{formatDuration(j.queue_ms)} queued</span> : null}
                </span>
              </button>
              {expanded && (
                <ul className="ci-steps">
                  {j.steps.map((s, i) => {
                    const a = ts(s.started_at);
                    const b = ts(s.finished_at) || a;
                    return (
                      <li key={i} className="ci-gantt-row step">
                        <span className="ci-gantt-label">
                          <span className="ci-gantt-pad" />
                          <Outcome outcome={s.conclusion || "skipped"} compact />
                          <span className="ci-gantt-name" title={s.name}>{s.name}</span>
                        </span>
                        <span className="ci-gantt-track">
                          {!Number.isNaN(a) && <span className={`ci-bar ${s.conclusion || "skipped"}`} style={{ left: pos(a), width: width(a, b) }} />}
                        </span>
                        <span className="ci-gantt-time num">{Number.isNaN(a) ? "—" : formatDuration(b - a)}</span>
                      </li>
                    );
                  })}
                </ul>
              )}
              {j.tests && (
                <div className="ci-job-tests dim">
                  {j.tests.tests} tests: {j.tests.passed} passed, {j.tests.failed} failed, {j.tests.skipped} skipped
                </div>
              )}
            </li>
          );
        })}
      </ul>
      <div className="ci-legend dim">
        <span>
          <span className="ci-bar queue legend" aria-hidden /> waiting for a runner
        </span>
        <span>Open a job for its steps.</span>
      </div>
    </section>
  );
}

function Change({ a, b, unit, lowerIsBetter = true }: { a: number | null | undefined; b: number | null | undefined; unit: "ms" | "percent" | "count"; lowerIsBetter?: boolean }) {
  const fmt = (v: number | null | undefined) => (v === null || v === undefined ? "—" : unit === "ms" ? formatMs(v) : unit === "percent" ? formatPercent(v) : formatCount(v));
  let tone = "";
  if (a != null && b != null && a !== b) {
    const worse = lowerIsBetter ? b > a : b < a;
    const big = unit === "count" ? Math.abs(b - a) >= Math.max(1, a * 0.2) : Math.abs(b - a) >= Math.max(a * 0.1, unit === "percent" ? 0.1 : 5);
    tone = big ? (worse ? "bad" : "good") : "";
  }
  return (
    <span className={`ci-change ${tone}`}>
      {fmt(a)} → <b>{fmt(b)}</b>
    </span>
  );
}

function Deploys({ deploys }: { deploys: CIDeploy[] }) {
  return (
    <section className="panel ci-section">
      <h3 className="ci-section-title">
        <Rocket size={14} aria-hidden /> Deployed as
      </h3>
      <p className="dim ci-section-text">Releases whose version names this commit, with the service's traffic in the hour before and after the deploy.</p>
      <div className="table-wrap">
        <table className="data">
          <thead>
            <tr>
              <th>Service</th>
              <th>Version</th>
              <th>Deployed</th>
              <th className="num">Requests</th>
              <th className="num">p95</th>
              <th className="num">5xx rate</th>
              <th className="num">Exceptions</th>
            </tr>
          </thead>
          <tbody>
            {deploys.map((d) => (
              <DeployRow key={`${d.service}|${d.version}`} d={d} />
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function DeployRow({ d }: { d: CIDeploy }) {
  const b: Partial<Traffic> = d.before ?? {};
  const a: Partial<Traffic> = d.after ?? {};
  return (
    <tr>
      <td>{d.service || "—"}</td>
      <td className="mono">{d.version}</td>
      <td title={formatDateTime(new Date(d.deployed_at))}>
        <a href="#/performance">{formatRelative(d.deployed_at)}</a>
      </td>
      <td className="num">
        <Change a={b.requests} b={a.requests} unit="count" lowerIsBetter={false} />
      </td>
      <td className="num">
        <Change a={b.p95_ms} b={a.p95_ms} unit="ms" />
      </td>
      <td className="num">
        <Change a={b.error_rate} b={a.error_rate} unit="percent" />
      </td>
      <td className="num">
        <Change a={b.errors} b={a.errors} unit="count" />
      </td>
    </tr>
  );
}

function Tests({ failed, slowest, total }: { failed: CITest[]; slowest: CITest[]; total: number }) {
  if (total === 0) {
    return (
      <section className="panel ci-section">
        <h3 className="ci-section-title">Tests</h3>
        <div className="panel-state">No test results for this run. Upload JUnit XML after your tests with the run's id; Help on this page has the step.</div>
      </section>
    );
  }
  return (
    <div className="grid ci-tests-grid">
      <section className="panel span-7 ci-section">
        <h3 className="ci-section-title">Failed tests</h3>
        {failed.length === 0 ? (
          <div className="panel-state">Every test passed.</div>
        ) : (
          <ul className="ci-failed">
            {failed.map((t, i) => (
              <li key={i}>
                <div className="ci-failed-head">
                  <Outcome outcome={t.status} compact />
                  <TestName test={t.test} file={t.file} service={t.job} />
                  {t.flaky && <span className="pill" title="Seen passing and failing on the same code">Flaky</span>}
                  <span className="dim num">{formatDuration(t.duration_ms)}</span>
                </div>
                {t.message && <pre className="ci-message">{t.message}</pre>}
              </li>
            ))}
          </ul>
        )}
      </section>
      <section className="panel span-5 ci-section">
        <h3 className="ci-section-title">Slowest tests</h3>
        <div className="table-wrap">
          <table className="data">
            <tbody>
              {slowest.map((t, i) => (
                <tr key={i}>
                  <td>
                    <TestName test={t.test} file={t.file} />
                  </td>
                  <td className="num">{formatDuration(t.duration_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
