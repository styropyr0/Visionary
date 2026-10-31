import { useEffect, useMemo, useState } from "react";
import { Rocket, Search, Workflow } from "lucide-react";
import type { Result } from "../lib/api";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatCount, formatPercent } from "../lib/format";
import { formatDateTime, formatRelative } from "../lib/time";
import { OUTCOME_LABEL, ciApi, formatDuration, junitStep, outcomeOf, runHref, shortSha, type CIFilters, type CIRun, type FlakyTest } from "../lib/ci";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { StatTile } from "../components/StatTile";
import { Panel } from "../components/Panel";
import { VizView } from "../components/VizView";
import { DataTable, type Column } from "../components/DataTable";
import { Select } from "../components/Select";
import { ErrorNotice } from "../components/ErrorNotice";
import { Drawer } from "../components/Modal";
import { SetupCard, SetupCommand, SetupOption } from "../components/SetupCard";
import { CIRunDetail } from "./CI.detail";
import { Outcome, TestName } from "./CI.parts";
import "./CI.css";

type Tab = "pipelines" | "tests" | "flaky";
const TABS: { key: Tab; label: string }[] = [
  { key: "pipelines", label: "Pipelines" },
  { key: "tests", label: "Tests" },
  { key: "flaky", label: "Flaky" },
];

function readHash(): { run: number | null; tab: Tab } {
  const p = new URLSearchParams(window.location.hash.split("?")[1] ?? "");
  const n = Number(p.get("run"));
  const tab = p.get("tab") as Tab;
  return { run: Number.isInteger(n) && n > 0 ? n : null, tab: TABS.some((t) => t.key === tab) ? tab : "pipelines" };
}

export function CI() {
  const [hash, setHash] = useState(readHash);
  useEffect(() => {
    const on = () => setHash(readHash());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return hash.run ? <CIRunDetail key={hash.run} id={hash.run} /> : <CIList tab={hash.tab} />;
}

const NO_FILTERS: CIFilters = { service: "", repo: "", workflow: "", branch: "" };

function CIList({ tab }: { tab: Tab }) {
  const { resolved, tick } = useApp();
  const [f, setF] = useState<CIFilters>(NO_FILTERS);
  const [history, setHistory] = useState<string | null>(null);
  const setup = useQuery(`ci-setup#${tick}`, ciApi.setup);
  const key = `${resolved.from.toISOString()}|${resolved.to.toISOString()}|${JSON.stringify(f)}#${tick}`;
  const summary = useQuery(`ci-summary?${key}`, (signal) => ciApi.summary(resolved.from, resolved.to, f, signal));
  const opts = summary.data?.options;
  const empty = setup.data && !setup.data.has_runs && !setup.data.has_tests;
  const pick = (k: keyof CIFilters, label: string, all: string) => {
    const list = opts?.[k] ?? [];
    if (list.length < 2 && !f[k]) return null;
    return (
      <Select
        label={label}
        value={f[k]}
        maxWidth={220}
        options={[{ value: "", label: all }, ...list.map((v) => ({ value: v, label: v }))]}
        onChange={(v) => setF({ ...f, [k]: v })}
      />
    );
  };

  return (
    <>
      <TopBar
        title="CI"
        section="Monitor"
        actions={
          <>
            {pick("service", "Service", "All services")}
            {tab === "pipelines" && pick("workflow", "Workflow", "All workflows")}
            {pick("branch", "Branch", "All branches")}
            <TimeRangePicker />
            <RefreshControl busy={summary.loading} />
          </>
        }
      />
      <div className="content">
        {empty ? (
          <CISetupCard secretSet={setup.data?.secret_set ?? false} />
        ) : (
          <>
            <nav className="tabs" role="tablist" aria-label="CI">
              {TABS.map((t) => (
                <button
                  type="button"
                  key={t.key}
                  role="tab"
                  aria-selected={tab === t.key}
                  className={tab === t.key ? "on" : ""}
                  onClick={() => (window.location.hash = t.key === "pipelines" ? "#/ci" : `#/ci?tab=${t.key}`)}
                >
                  {t.label}
                </button>
              ))}
            </nav>
            {tab === "pipelines" && <PipelinesTab filters={f} summary={summary} />}
            {tab === "tests" && <TestsTab filters={f} onTest={setHistory} />}
            {tab === "flaky" && <FlakyTab filters={f} onTest={setHistory} />}
          </>
        )}
      </div>
      {history && <TestHistoryDrawer hash={history} onClose={() => setHistory(null)} />}
    </>
  );
}

function ms(v: number | null | undefined) {
  return formatDuration(v ?? null);
}

function PipelinesTab({ filters, summary }: { filters: CIFilters; summary: ReturnType<typeof useQuery<Awaited<ReturnType<typeof ciApi.summary>>>> }) {
  const d = summary.data;
  const t = d?.totals;
  const loading = summary.loading;
  const jobColumns: Column[] = [
    { key: "job", label: "Job", render: (v, row) => <span title={String(row[1] ?? "")}>{String(v)}<span className="dim"> · {String(row[1] || "—")}</span></span> },
    { key: "failures", label: "Failed", align: "right", render: (v, row) => `${v} of ${row[2]}` },
    { key: "failure_rate", label: "Failure rate", align: "right", unit: "percent" },
    { key: "p50_ms", label: "p50", align: "right", render: (v) => ms(v as number) },
    { key: "p95_ms", label: "p95", align: "right", render: (v) => ms(v as number) },
    { key: "queue_p50_ms", label: "Queue p50", align: "right", render: (v) => ms(v as number) },
  ];
  const flowColumns: Column[] = [
    { key: "workflow", label: "Workflow", render: (v) => String(v || "(unnamed)") },
    { key: "branch", label: "Branch", render: (v) => <span className="mono">{String(v || "—")}</span> },
    { key: "runs", label: "Runs", align: "right", unit: "count" },
    { key: "success_rate", label: "Success", align: "right", unit: "percent" },
    { key: "p50_ms", label: "p50", align: "right", render: (v) => ms(v as number) },
    { key: "p95_ms", label: "p95", align: "right", render: (v) => ms(v as number) },
    { key: "queue_p50_ms", label: "Queue p50", align: "right", render: (v) => ms(v as number) },
    { key: "last", label: "Last run", render: (v, row) => (
      <a href={runHref(Number(row[9]))} className="ci-last">
        <Outcome outcome={String(v)} compact />
        {formatRelative(String(row[8]))}
      </a>
    ) },
  ];
  return (
    <>
      <div className="grid">
        <StatTile label="Runs" value={t ? formatCount(t.runs) : "—"} delta={null} deltaLabel="" upIs="neutral" loading={loading} footer={t ? `${t.failure} failed, ${t.cancelled} cancelled` : "in this range"} />
        <StatTile label="Success rate" value={t?.success_rate != null ? formatPercent(t.success_rate) : "—"} description="Passed runs among those that passed or failed; cancelled and skipped runs don't count." delta={null} deltaLabel="" upIs="good" loading={loading} footer="of finished runs" />
        <StatTile label="Duration p50" value={ms(t?.p50_ms)} description="Half of the finished runs took less than this, from start to finish." delta={null} deltaLabel="" upIs="bad" loading={loading} footer="median run" />
        <StatTile label="Duration p95" value={ms(t?.p95_ms)} delta={null} deltaLabel="" upIs="bad" loading={loading} footer="slowest 5% of runs" />
        <StatTile label="Queue p50" value={ms(t?.queue_p50_ms)} description="How long a run waited before its first job started on a runner." delta={null} deltaLabel="" upIs="bad" loading={loading} footer="waiting for a runner" />
        <StatTile label="Queue p95" value={ms(t?.queue_p95_ms)} delta={null} deltaLabel="" upIs="bad" loading={loading} footer="longest waits" />
        <Panel
          className="span-4"
          title="Runs by outcome"
          description="Pipeline runs queued in each bucket, by how they ended."
          result={d?.outcomes}
          loading={loading}
          error={summary.error}
          bodyHeight={220}
          chart={d && <VizView result={d.outcomes} viz={{ type: "timeseries", x: "t", y: ["runs"], series: "outcome", stack: true, unit: "count" }} height={220} title="Runs by outcome" />}
          table={d && <DataTable result={d.outcomes} maxHeight={220} />}
        />
        <Panel
          className="span-4"
          title="Success rate by workflow"
          description="Share of finished runs that passed, for the busiest workflows."
          result={d?.rate}
          loading={loading}
          error={summary.error}
          bodyHeight={220}
          chart={d && <VizView result={d.rate} viz={{ type: "timeseries", x: "t", y: ["success_rate"], series: "workflow", unit: "percent" }} height={220} title="Success rate by workflow" />}
          table={d && <DataTable result={d.rate} maxHeight={220} />}
        />
        <Panel
          className="span-4"
          title="Duration and queue time"
          description="p50 and p95 of how long finished runs took, and the median wait before the first job started."
          result={d?.durations}
          loading={loading}
          error={summary.error}
          bodyHeight={220}
          chart={d && <VizView result={d.durations} viz={{ type: "timeseries", x: "t", y: ["duration_ms"], series: "series", unit: "ms" }} height={220} title="Duration and queue time" />}
          table={d && <DataTable result={d.durations} maxHeight={220} />}
        />
        <Panel
          className="span-12"
          title="Workflows"
          description="Each workflow on each branch: runs, success rate, p50 and p95 duration, queue time and its last run."
          result={d?.workflows}
          loading={loading}
          error={summary.error}
          table={d && <DataTable result={d.workflows} columns={flowColumns} maxHeight={320} />}
        />
        <Panel
          className="span-6"
          title="Slowest jobs"
          description="Jobs by p95 duration, with how long they waited for a runner."
          result={d?.slow_jobs}
          loading={loading}
          error={summary.error}
          table={d && <DataTable result={d.slow_jobs} columns={jobColumns} maxHeight={320} />}
        />
        <Panel
          className="span-6"
          title="Failing jobs"
          description="Jobs that failed most often in the range. Open a run below to see which step and tests failed."
          result={d?.fail_jobs}
          loading={loading}
          error={summary.error}
          table={d && (d.fail_jobs.rows.length ? <DataTable result={d.fail_jobs} columns={jobColumns} maxHeight={320} /> : <div className="panel-state">No job failed in this range.</div>)}
        />
      </div>
      <RunList filters={filters} />
    </>
  );
}

const RUN_VIEWS = [
  { key: "", label: "All" },
  { key: "failure", label: "Failed" },
  { key: "success", label: "Passed" },
  { key: "running", label: "Running" },
];

function RunList({ filters }: { filters: CIFilters }) {
  const { resolved, tick } = useApp();
  const [outcome, setOutcome] = useState("");
  const [q, setQ] = useState("");
  const args = { ...filters, outcome, q };
  const runs = useQuery(`ci-runs?${resolved.from.toISOString()}|${resolved.to.toISOString()}|${JSON.stringify(args)}#${tick}`, (signal) => ciApi.runs(resolved.from, resolved.to, args, signal));
  return (
    <>
      <div className="filter-row ci-filters">
        <div className="segmented" role="radiogroup" aria-label="Show">
          {RUN_VIEWS.map((v) => (
            <button type="button" key={v.key} role="radio" aria-checked={outcome === v.key} className={outcome === v.key ? "on" : ""} onClick={() => setOutcome(v.key)}>
              {v.label}
            </button>
          ))}
        </div>
        <div className="search">
          <Search size={14} />
          <input className="input" value={q} placeholder="Search commits, messages, branches and people" onChange={(e) => setQ(e.target.value)} aria-label="Search runs" />
        </div>
      </div>
      {runs.error && !runs.data ? (
        <ErrorNotice title="Could not load runs" error={runs.error} />
      ) : !runs.data ? (
        <div className="panel-state" style={{ height: 200 }} />
      ) : runs.data.runs.length === 0 ? (
        <div className="panel empty">
          <span className="empty-icon">
            <Workflow size={20} />
          </span>
          <h3>{outcome || q ? "No runs match" : "No runs in this range"}</h3>
          <p>Runs show up as GitHub sends them, or as your CI posts them. Widen the time range to see older ones.</p>
        </div>
      ) : (
        <div className="panel">
          <div className="table-wrap">
            <table className="data ci-runs">
              <thead>
                <tr>
                  <th>Outcome</th>
                  <th>Run</th>
                  <th>Commit</th>
                  <th>Started</th>
                  <th className="num">Queue</th>
                  <th className="num">Duration</th>
                  <th>Deployed</th>
                </tr>
              </thead>
              <tbody>
                {runs.data.runs.map((r) => (
                  <RunRow key={r.id} run={r} />
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </>
  );
}

function RunRow({ run: r }: { run: CIRun }) {
  const go = () => (window.location.hash = runHref(r.id));
  const deploys = r.deploys ?? [];
  return (
    <tr className="clickable" onClick={go}>
      <td>
        <Outcome outcome={outcomeOf(r)} />
      </td>
      <td className="ci-run-cell">
        <a href={runHref(r.id)} onClick={(e) => e.stopPropagation()}>
          {r.workflow || "(unnamed)"}
          {r.number ? ` #${r.number}` : ""}
          {r.attempt > 1 ? ` (attempt ${r.attempt})` : ""}
        </a>
        <span className="dim">
          {" "}
          · <span className="mono">{r.branch || "—"}</span>
          {r.actor ? ` · ${r.actor}` : ""}
        </span>
      </td>
      <td className="ci-commit-cell">
        {r.commit ? <span className="mono">{shortSha(r.commit)}</span> : "—"}
        {r.commit_message && <span className="dim"> {r.commit_message}</span>}
      </td>
      <td title={formatDateTime(new Date(r.started_at || r.created_at))}>{formatRelative(r.started_at || r.created_at)}</td>
      <td className="num">{ms(r.queue_ms)}</td>
      <td className="num">{r.status === "completed" ? ms(r.duration_ms) : "running"}</td>
      <td>
        {deploys.length > 0 ? (
          <span className="pill" title={deploys.map((d) => `${d.service} ${d.version}, ${formatDateTime(new Date(d.deployed_at))}`).join("\n")}>
            <Rocket size={11} aria-hidden />
            {deploys[0].service || deploys[0].version}
            {deploys.length > 1 ? ` +${deploys.length - 1}` : ""}
          </span>
        ) : (
          <span className="dim">—</span>
        )}
      </td>
    </tr>
  );
}

function TestsTab({ filters, onTest }: { filters: CIFilters; onTest: (hash: string) => void }) {
  const { resolved, tick } = useApp();
  const tests = useQuery(`ci-tests?${resolved.from.toISOString()}|${resolved.to.toISOString()}|${JSON.stringify(filters)}#${tick}`, (signal) => ciApi.tests(resolved.from, resolved.to, filters, signal));
  const d = tests.data;
  const t = d?.totals;
  const open = (row: unknown[]) => onTest(String(row[0]));
  const name: Column = { key: "test", label: "Test", render: (v, row) => <TestName test={String(v)} file={String(row[3] ?? "")} service={String(row[2] ?? "")} /> };
  const loading = tests.loading;
  return (
    <div className="grid">
      <StatTile label="Tests" value={t ? formatCount(t.tests) : "—"} delta={null} deltaLabel="" upIs="neutral" loading={loading} footer="distinct tests run" />
      <StatTile label="Results" value={t ? formatCount(t.results) : "—"} delta={null} deltaLabel="" upIs="neutral" loading={loading} footer="test runs uploaded" />
      <StatTile label="Failures" value={t ? formatCount(t.failures) : "—"} delta={null} deltaLabel="" upIs="bad" loading={loading} footer="failed or errored results" />
      <StatTile label="Failing tests" value={t ? formatCount(t.failing_tests) : "—"} delta={null} deltaLabel="" upIs="bad" loading={loading} footer="failed at least once" />
      <StatTile label="Failure rate" value={t && t.results - t.skipped > 0 ? formatPercent((100 * t.failures) / (t.results - t.skipped)) : "—"} delta={null} deltaLabel="" upIs="bad" loading={loading} footer="of results not skipped" />
      <StatTile label="Skipped" value={t ? formatCount(t.skipped) : "—"} delta={null} deltaLabel="" upIs="neutral" loading={loading} footer="results" />
      <Panel
        className="span-12"
        title="Test results"
        description="Results uploaded in each bucket, by status."
        result={d?.results}
        loading={loading}
        error={tests.error}
        bodyHeight={200}
        chart={d && <VizView result={d.results} viz={{ type: "timeseries", x: "t", y: ["results"], series: "status", stack: true, unit: "count" }} height={200} title="Test results" empty="No test results in this range. Upload JUnit XML from your CI." />}
        table={d && <DataTable result={d.results} maxHeight={200} />}
      />
      <Panel
        className="span-12"
        title="Failures by test"
        description="Tests that failed in the range, most failures first. Open one for its history across commits and branches."
        result={d?.failing}
        loading={loading}
        error={tests.error}
        table={
          d &&
          (d.failing.rows.length ? (
            <DataTable
              result={d.failing}
              onRowClick={open}
              maxHeight={360}
              columns={[
                name,
                { key: "runs", label: "Runs", align: "right", unit: "count" },
                { key: "failures", label: "Failed", align: "right", unit: "count" },
                { key: "last_failed", label: "Last failure", render: (v) => (v ? formatRelative(String(v)) : "—") },
              ]}
            />
          ) : (
            <div className="panel-state">No test failed in this range.</div>
          ))
        }
      />
      <Panel
        className="span-6"
        title="Slowest tests"
        description="Passing runs only, by average duration."
        result={d?.slowest}
        loading={loading}
        error={tests.error}
        table={
          d && (
            <DataTable
              result={d.slowest}
              onRowClick={open}
              maxHeight={360}
              columns={[
                name,
                { key: "runs", label: "Runs", align: "right", unit: "count" },
                { key: "avg_ms", label: "Average", align: "right", unit: "ms" },
                { key: "max_ms", label: "Max", align: "right", unit: "ms" },
              ]}
            />
          )
        }
      />
      <Panel
        className="span-6"
        title="Getting slower"
        description={`Average duration of passing runs in the second half of the range against the first${d ? ` (split at ${formatDateTime(new Date(d.midpoint))})` : ""}: at least 25% and 20 ms slower, with five runs in each half.`}
        result={d?.slower}
        loading={loading}
        error={tests.error}
        table={
          d &&
          (d.slower.rows.length ? (
            <DataTable
              result={d.slower}
              onRowClick={open}
              maxHeight={360}
              columns={[
                { key: "test", label: "Test", render: (v, row) => <TestName test={String(v)} service={String(row[2] ?? "")} /> },
                { key: "before_ms", label: "Before", align: "right", unit: "ms" },
                { key: "after_ms", label: "After", align: "right", unit: "ms" },
                { key: "change_pct", label: "Change", align: "right", render: (v) => <span className="flag bad">+{formatPercent(Number(v))}</span> },
              ]}
            />
          ) : (
            <div className="panel-state">No test got noticeably slower in this range.</div>
          ))
        }
      />
    </div>
  );
}

function ResultStrip({ results }: { results: string[] }) {
  return (
    <span className="ci-strip" aria-label={results.map((r) => OUTCOME_LABEL[r] ?? r).join(", ")}>
      {results.map((r, i) => (
        <span key={i} className={`ci-mark ${r}`} title={OUTCOME_LABEL[r] ?? r} />
      ))}
    </span>
  );
}

const REASON: Record<string, string> = {
  same_commit: "Passed and failed on one commit",
  flip_flop: "Fails alone between passes on the default branch",
};

function FlakyTab({ filters, onTest }: { filters: CIFilters; onTest: (hash: string) => void }) {
  const { resolved, tick } = useApp();
  const flaky = useQuery(`ci-flaky?${resolved.from.toISOString()}|${resolved.to.toISOString()}|${JSON.stringify(filters)}#${tick}`, (signal) => ciApi.flaky(resolved.from, resolved.to, filters, signal));
  const list = flaky.data?.tests ?? [];
  const from = resolved.from.toISOString();
  const result = useMemo<Result>(
    () => ({
      columns: ["test", "score", "reason", "runs", "failures", "recent", "last_failed_at", "hash", "item"],
      rows: list.map((t) => [t.test, t.score, REASON[t.reason], t.runs, t.failures, t.recent.join(" "), t.last_failed_at, t.hash, t]),
      truncated: false,
      rows_read: 0,
      duration_ms: 0,
      sql: "",
      bucket: 0,
      from,
      to: resolved.to.toISOString(),
    }),
    [list, from, resolved.to],
  );
  const isNew = (t: FlakyTest) => !!t.first_flagged && t.first_flagged >= from;
  return (
    <div className="grid">
      <StatTile label="Flaky tests" value={flaky.data ? String(list.length) : "—"} delta={null} deltaLabel="" upIs="bad" loading={flaky.loading} footer="among tests that failed in range" />
      <StatTile label="New" value={flaky.data ? String(flaky.data.new) : "—"} description="Tests first seen flaky inside this range." delta={null} deltaLabel="" upIs="bad" loading={flaky.loading} footer="first flagged in range" />
      <Panel
        className="span-8"
        title="Flaky tests"
        description="A test is flaky when it passed and failed on the same commit, or failed on its own between passes on the default branch at least twice. The score is the share of the commits it ran on where it flaked."
        result={result}
        loading={flaky.loading}
        error={flaky.error}
        table={
          flaky.data &&
          (list.length === 0 ? (
            <div className="panel-state">No flaky test in this range.</div>
          ) : (
            <DataTable
              result={result}
              onRowClick={(row) => onTest(String(row[7]))}
              maxHeight={520}
              columns={[
                {
                  key: "test",
                  label: "Test",
                  render: (v, row) => {
                    const t = row[8] as FlakyTest;
                    return (
                      <span className="ci-test-name">
                        <span className="mono">
                          {String(v)}
                          {isNew(t) && <span className="pill ci-new">New</span>}
                        </span>
                        <span className="dim">{String(row[2])}{t.service ? ` · ${t.service}` : ""}</span>
                      </span>
                    );
                  },
                },
                { key: "score", label: "Score", align: "right", render: (v) => <span className="ci-score">{formatPercent(Number(v))}</span> },
                { key: "failures", label: "Failed", align: "right", render: (v, row) => `${v} of ${row[3]}` },
                { key: "recent", label: "Last results", render: (_v, row) => <ResultStrip results={(row[8] as FlakyTest).recent} /> },
                { key: "last_failed_at", label: "Last failure", render: (v) => (v ? formatRelative(String(v)) : "—") },
              ]}
            />
          ))
        }
      />
    </div>
  );
}

function TestHistoryDrawer({ hash, onClose }: { hash: string; onClose: () => void }) {
  const h = useQuery(`ci-history?${hash}`, (signal) => ciApi.history(hash, signal));
  const d = h.data;
  return (
    <Drawer title={d?.test || "Test"} onClose={onClose} className="ci-drawer">
      {h.error && <ErrorNotice title="Could not load the test" error={h.error} />}
      {!d && !h.error && <div className="panel-state" style={{ height: 200 }} />}
      {d && (
        <>
          <dl className="kv">
            <dt>Test</dt>
            <dd className="mono">{d.test}</dd>
            {d.file && (
              <>
                <dt>File</dt>
                <dd className="mono">{d.file}</dd>
              </>
            )}
            {d.service && (
              <>
                <dt>Service</dt>
                <dd>{d.service}</dd>
              </>
            )}
            <dt>Flaky</dt>
            <dd>{d.flaky ? `Since ${formatDateTime(new Date(d.flaky.at))}: ${REASON[d.flaky.reason] ?? d.flaky.reason}${d.flaky.commit ? ` (${shortSha(d.flaky.commit)})` : ""}` : "Not seen flaky"}</dd>
            <dt>Last results</dt>
            <dd>
              <ResultStrip results={d.results.slice(0, 30).map((r) => r.status).reverse()} />
            </dd>
          </dl>
          <h3 className="ci-drawer-title">Last {d.results.length} results</h3>
          <ul className="ci-history">
            {d.results.map((r, i) => (
              <li key={i}>
                <div className="ci-history-head">
                  <Outcome outcome={r.status} />
                  <span title={formatDateTime(new Date(r.at))}>{formatRelative(r.at)}</span>
                  <span className="mono dim">{r.branch || "—"}</span>
                  {r.commit && <span className="mono dim">{shortSha(r.commit)}</span>}
                  <span className="dim">{formatDuration(r.duration_ms)}</span>
                  {r.run_id && (
                    <a href={runHref(Number(r.run_id))} onClick={onClose}>
                      {r.workflow || "Run"}
                      {r.number ? ` #${r.number}` : ""}
                    </a>
                  )}
                </div>
                {r.message && <pre className="ci-message">{r.message}</pre>}
              </li>
            ))}
          </ul>
        </>
      )}
    </Drawer>
  );
}

function CISetupCard({ secretSet }: { secretSet: boolean }) {
  const origin = window.location.origin;
  const curl = `curl -X POST ${origin}/api/ingest/ci/runs \\
  -H "Authorization: Bearer <server key>" \\
  -d '{"provider":"gitlab","repo":"shop/api","workflow":"pipeline","run_id":"9001",
       "branch":"main","commit":"<sha>","status":"success",
       "started_at":"2026-10-30T09:00:00Z","finished_at":"2026-10-30T09:12:00Z"}'`;
  return (
    <SetupCard
      icon={<Workflow size={20} />}
      title="See your CI pipelines and tests"
      text="Every workflow run with its jobs, success rate, duration and queue time; test results with failures, slow tests and flaky tests; and the deploy each commit became."
      topic="ci"
    >
      <SetupOption
        title="GitHub Actions"
        text={
          <>
            Add a webhook to the repository or organization for <b>Workflow runs</b> and <b>Workflow jobs</b>, with this payload URL and{" "}
            {secretSet ? "the secret saved in Settings → Server → CI." : <>a secret you also save in <a href="#/settings?tab=server">Settings → Server → CI</a>.</>}
          </>
        }
      >
        <SetupCommand code={`${origin}/api/ingest/ci/github?service=my-api`} />
      </SetupOption>
      <SetupOption title="Test results" text="A step after your tests uploads their JUnit XML with a server key.">
        <SetupCommand code={junitStep(origin)} />
      </SetupOption>
      <SetupOption title="Any other CI" text="GitLab, CircleCI, Buildkite or Jenkins post each run as JSON, with its jobs if you like.">
        <SetupCommand code={curl} />
      </SetupOption>
    </SetupCard>
  );
}
