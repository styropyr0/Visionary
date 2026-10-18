import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, Search, Sparkles } from "lucide-react";
import type { Result, Viz } from "../lib/api";
import { formatCount, formatPercent, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { formatDateTime, formatLogTime, formatRelative, previousLabel } from "../lib/time";
import { askAnalyst, records, shortVersion, useTelemetry, type Issue, type IssueDetail, type IssuesResponse, type IssueStatus } from "../lib/telemetry";
import { DataTable, type Column } from "../components/DataTable";
import { Panel } from "../components/Panel";
import { Select } from "../components/Select";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { IssueStatusBadge, MiniSpark, VersionPills } from "../components/telemetry/bits";
import { StackTrace } from "../components/telemetry/StackTrace";
import { LogDetail } from "./Logs";
import { ErrorNotice } from "../components/ErrorNotice";
import { InfoTip } from "../components/InfoTip";
import "./Errors.css";

const TIMELINE: Viz = { type: "timeseries", x: "t", y: ["value"], series: "series", stack: true, unit: "count" };

/** The fingerprint in #/errors?fp=…, following the address bar. */
function useIssueParam(): string | null {
  const read = () => new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("fp");
  const [fp, setFp] = useState(read);
  useEffect(() => {
    const on = () => setFp(read());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return fp;
}

const openIssue = (fp: string) => (window.location.hash = `#/errors?fp=${encodeURIComponent(fp)}`);

export function Errors() {
  const fp = useIssueParam();
  const list = useTelemetry<IssuesResponse>(fp ? null : "errors");
  const detail = useTelemetry<IssueDetail>(fp ? `errors/${encodeURIComponent(fp)}` : null);
  return (
    <>
      <TopBar
        title="Errors"
        section="Monitor"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl busy={fp ? detail.loading : list.loading} />
          </>
        }
      />
      <div className="content">{fp ? <IssueView fp={fp} q={detail} /> : <IssueList q={list} />}</div>
    </>
  );
}

type Q<T> = { data?: T; loading: boolean; error?: Error };

function summaryOf(res: Result | null | undefined): Record<string, number> | null {
  const r = records(res ?? undefined)[0];
  if (!r) return null;
  const out: Record<string, number> = {};
  for (const [k, v] of Object.entries(r)) out[k] = toNumber(v) ?? 0;
  return out;
}

// ---------- Issue list ----------

// The last column carries the whole issue for the cell renderers.
const ISSUE_COLS = ["fingerprint", "type", "status", "occurrences", "users", "trend", "versions", "first_seen", "last_seen", "issue"];
const ISSUE = 9;

function IssueList({ q }: { q: Q<IssuesResponse> }) {
  const { range } = useApp();
  const tokens = useChartTokens();
  const [status, setStatus] = useState("");
  const [service, setService] = useState("");
  const [search, setSearch] = useState("");

  const data = q.data;
  const cur = summaryOf(data?.summary.current);
  const prev = summaryOf(data?.summary.previous);
  const delta = (k: string) => (cur && prev && prev[k] ? (cur[k] - prev[k]) / prev[k] : null);
  const services = useMemo(() => [...new Set((data?.issues ?? []).map((i) => i.service).filter(Boolean))].sort(), [data]);

  const table = useMemo<Result | undefined>(() => {
    if (!data) return undefined;
    const needle = search.trim().toLowerCase();
    const rows = data.issues
      .filter((i) => !status || i.status === status)
      .filter((i) => !service || i.service === service)
      .filter((i) => !needle || `${i.type} ${i.message} ${i.method} ${i.route}`.toLowerCase().includes(needle))
      .map((i) => [i.fingerprint, i.type, i.status, i.occurrences, i.users, i.trend ?? [], i.versions.length, i.first_seen, i.last_seen, i]);
    return { ...data.timeline, columns: ISSUE_COLS, rows, truncated: data.issues.length >= 100, sql: data.sql };
  }, [data, status, service, search]);

  const unhandledShare = cur && cur.occurrences > 0 ? (100 * cur.unhandled) / cur.occurrences : null;

  return (
    <div className="grid">
      {q.error && !data && (
        <ErrorNotice className="span-12" title="Could not load errors" error={q.error} />
      )}
      <StatTile label="Occurrences" value={cur ? formatCount(cur.occurrences) : "—"} delta={delta("occurrences")} deltaLabel={previousLabel(range)} upIs="bad" loading={q.loading} />
      <StatTile
        label="Issues"
        description="Distinct errors: occurrences with the same type and code location are one issue."
        value={cur ? formatCount(cur.issues) : "—"}
        delta={delta("issues")}
        deltaLabel={previousLabel(range)}
        upIs="bad"
        loading={q.loading}
      />
      <StatTile
        label="New issues"
        description="Issues that have only occurred in the version they occur in now."
        value={data ? formatCount(data.counts.new ?? 0) : "—"}
        delta={null}
        deltaLabel=""
        upIs="bad"
        loading={q.loading}
        footer="only in their current version"
      />
      <StatTile
        label="Regressed"
        description="Issues that were absent from the previous release and came back."
        value={data ? formatCount(data.counts.regressed ?? 0) : "—"}
        delta={null}
        deltaLabel=""
        upIs="bad"
        loading={q.loading}
        footer="back in a newer release"
      />
      <StatTile label="Users affected" value={cur ? formatCount(cur.users) : "—"} delta={delta("users")} deltaLabel={previousLabel(range)} upIs="bad" loading={q.loading} />
      <StatTile
        label="Unhandled"
        description="Exceptions that reached the framework and failed the request, as a share of all occurrences. The rest were reported by the application."
        value={unhandledShare === null ? "—" : formatPercent(unhandledShare)}
        delta={null}
        deltaLabel=""
        upIs="bad"
        loading={q.loading}
        footer={cur ? `${formatCount(cur.unhandled)} of ${formatCount(cur.occurrences)}` : undefined}
      />

      <Panel
        className="span-12"
        title="Errors over time"
        description="Occurrences per interval by exception type. Dashed lines mark deploys."
        result={data?.timeline}
        loading={q.loading}
        error={q.error}
        bodyHeight={200}
        note={vizNote(data?.timeline, TIMELINE, tokens)}
        chart={data && <VizView result={data.timeline} viz={TIMELINE} height={200} title="Errors over time" empty="No errors in this time range." />}
        table={data && <DataTable result={data.timeline} maxHeight={200} defaultUnit="count" />}
      />

      <div className="span-12 filter-row errors-filters" role="search">
        <Select
          label="Status"
          align="left"
          value={status}
          onChange={setStatus}
          options={[
            { value: "", label: "All statuses" },
            { value: "new", label: `New (${data?.counts.new ?? 0})` },
            { value: "regressed", label: `Regressed (${data?.counts.regressed ?? 0})` },
            { value: "ongoing", label: `Ongoing (${data?.counts.ongoing ?? 0})` },
          ]}
        />
        {services.length > 1 && (
          <Select label="Service" align="left" value={service} onChange={setService} options={[{ value: "", label: "All services" }, ...services.map((s) => ({ value: s, label: s }))]} />
        )}
        <div className="search">
          <Search size={14} />
          <input className="input" placeholder="Type, message or route" value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Search issues" />
        </div>
      </div>

      <Panel
        className="span-12"
        title="Issues"
        description="Errors grouped by fingerprint, most frequent first. Events and users count the selected range; versions and first seen cover all retained history."
        result={table}
        loading={q.loading}
        error={q.error}
        bodyHeight={240}
        table={
          table &&
          (data && data.issues.length === 0 ? (
            <div className="panel-state errors-empty">
              No errors recorded in this time range. The SDKs record unhandled exceptions on their own; report handled ones with the SDK's capture call (visionary.CaptureError in Go, capture_exception in Python).
            </div>
          ) : table.rows.length === 0 ? (
            <div className="panel-state">No issues match these filters.</div>
          ) : (
            <DataTable result={table} columns={issueColumns(data?.spark.bucket ?? 0)} onRowClick={(r) => openIssue(String(r[0]))} rowKey={(r) => r[0]} maxHeight={720} />
          ))
        }
      />
    </div>
  );
}

function IssueCell({ issue }: { issue: Issue }) {
  return (
    <div className="issue-cell">
      <div className="issue-head">
        <span className="mono issue-type">{issue.type}</span>
        {issue.route ? (
          <span className="mono dim issue-route">
            {issue.method} {issue.route}
          </span>
        ) : (
          issue.service && <span className="dim issue-route">background · {issue.service}</span>
        )}
        {issue.handled && <span className="pill">handled</span>}
      </div>
      <div className="issue-msg">{issue.message || <span className="dim">No message</span>}</div>
    </div>
  );
}

function bucketLabel(seconds: number): string {
  return seconds >= 3600 ? `${seconds / 3600} h` : `${seconds / 60} min`;
}

function issueColumns(bucket: number): Column[] {
  const issueOf = (row: unknown[]) => row[ISSUE] as Issue;
  return [
    { key: "type", label: "Issue", render: (_v, row) => <IssueCell issue={issueOf(row)} /> },
    { key: "status", render: (v, row) => <IssueStatusBadge status={v as IssueStatus} version={issueOf(row).status_version} />, width: 150 },
    { key: "occurrences", label: "Events", align: "right", unit: "count", width: 76 },
    { key: "users", align: "right", unit: "count", width: 68 },
    {
      key: "trend",
      render: (v) => (
        <span title={bucket ? `Events per ${bucketLabel(bucket)}` : undefined}>
          <MiniSpark points={v as number[]} />
        </span>
      ),
      width: 112,
    },
    { key: "versions", render: (_v, row) => <VersionPills versions={issueOf(row).versions} current={issueOf(row).status_version} max={2} />, width: 190 },
    { key: "first_seen", label: "First seen", render: (v) => <span title={formatLogTime(String(v))}>{formatRelative(String(v))}</span>, width: 96 },
    { key: "last_seen", label: "Last seen", render: (v) => <span title={formatLogTime(String(v))}>{formatRelative(String(v))}</span>, width: 96 },
  ];
}

// ---------- One issue ----------

const OCCURRENCES: Viz = { type: "timeseries", x: "t", y: ["value"], series: "series", stack: true, unit: "count" };

function IssueView({ fp, q }: { fp: string; q: Q<IssueDetail> }) {
  const tokens = useChartTokens();
  const [openLog, setOpenLog] = useState<number | null>(null);
  const d = q.data?.fingerprint === fp ? q.data : undefined;
  const latest = d?.latest;
  const stats = summaryOf(d?.stats);
  const known = d?.versions.filter((v) => v.version) ?? [];

  // Series read better as short versions.
  const timeline = useMemo(() => (d ? { ...d.timeline, rows: d.timeline.rows.map((r) => [r[0], r[1] === "unknown" ? "unknown" : shortVersion(String(r[1])), r[2]]) } : undefined), [d]);

  const ask = () => {
    if (!d || !latest) return;
    const where = latest.route ? ` on ${String(latest.method ?? "")} ${String(latest.route)}` : "";
    askAnalyst(
      `Investigate the error ${String(latest.type)}: "${String(latest.message ?? "").slice(0, 200)}"${where} (errors.fingerprint = '${fp}'). ` +
        `It occurred ${stats?.occurrences ?? 0} times in this range and was first seen ${d.first_seen}. When did it start, which routes, users and versions are affected, ` +
        `did a deploy (releases) or a change in traffic or latency line up with it, and what do the failing requests have in common?`,
    );
  };

  if (q.error && !d) {
    return (
      <>
        <BackLink />
        <ErrorNotice title="Could not load this issue" error={q.error} />
      </>
    );
  }

  return (
    <>
      <BackLink />
      <header className="issue-header" aria-busy={q.loading || undefined}>
        <div className="issue-title">
          <h2 className="mono">{latest ? String(latest.type) : "Loading…"}</h2>
          {latest?.message ? <p className="issue-message">{String(latest.message)}</p> : null}
          {d && latest && (
            <div className="issue-meta">
              <IssueStatusBadge status={d.status} version={d.status_version} />
              <span className="pill">{toNumber(latest.handled) ? "handled" : "unhandled"}</span>
              {latest.service ? <span>{String(latest.service)}</span> : null}
              {latest.runtime ? <span className="dim">{String(latest.runtime)}</span> : null}
              <span className="dim mono" title="Fingerprint">
                {fp}
              </span>
            </div>
          )}
        </div>
        <button className="btn" onClick={ask} disabled={!d}>
          <Sparkles size={14} />
          Ask the analyst
        </button>
      </header>

      <div className="grid">
        <StatTile label="Occurrences" value={stats ? formatCount(stats.occurrences) : "—"} delta={null} deltaLabel="" upIs="bad" loading={q.loading} footer="in this range" />
        <StatTile label="Users affected" value={stats ? formatCount(stats.users) : "—"} delta={null} deltaLabel="" upIs="bad" loading={q.loading} footer="in this range" />
        <StatTile label="First seen" value={d ? formatRelative(d.first_seen) : "—"} delta={null} deltaLabel="" upIs="neutral" loading={q.loading} footer={d ? formatDateTime(new Date(d.first_seen)) : undefined} />
        <StatTile
          label="Last seen"
          value={latest ? formatRelative(String(latest.created_at)) : "—"}
          delta={null}
          deltaLabel=""
          upIs="neutral"
          loading={q.loading}
          footer={latest ? formatDateTime(new Date(String(latest.created_at))) : undefined}
        />
        <StatTile
          label="Versions"
          value={d ? formatCount(known.length) : "—"}
          delta={null}
          deltaLabel=""
          upIs="neutral"
          loading={q.loading}
          footer={known.length ? `latest ${shortVersion(known[known.length - 1].version)}` : "no version reported"}
        />
        <StatTile label="Instances" value={stats ? formatCount(stats.instances) : "—"} delta={null} deltaLabel="" upIs="neutral" loading={q.loading} footer="in this range" />

        <Panel
          className="span-8"
          title="Occurrences over time"
          description="Per interval, by the version that raised it. Dashed lines mark deploys."
          result={timeline}
          loading={q.loading}
          bodyHeight={220}
          note={vizNote(timeline, OCCURRENCES, tokens)}
          chart={timeline && <VizView result={timeline} viz={OCCURRENCES} height={220} title="Occurrences over time" empty="No occurrences in this time range." />}
          table={timeline && <DataTable result={timeline} maxHeight={220} defaultUnit="count" />}
        />
        <section className="panel span-4">
          <header className="panel-head">
            <h3 className="panel-title">Versions</h3>
            <InfoTip text="Every version this error occurred in, over all retained history, with when that version was deployed." />
          </header>
          <div className="panel-body">
            {d && d.versions.length > 0 ? (
              <div className="table-wrap" style={{ maxHeight: 220 }}>
                <table className="data">
                  <thead>
                    <tr>
                      <th>Version</th>
                      <th>Deployed</th>
                      <th>Last seen</th>
                      <th className="num">Events</th>
                    </tr>
                  </thead>
                  <tbody>
                    {[...d.versions].reverse().map((v) => (
                      <tr key={v.version || "none"}>
                        <td title={v.version}>
                          <span className="mono">{v.version ? shortVersion(v.version) : <span className="dim">unknown</span>}</span>
                          {v.version && v.version === d.status_version && d.status !== "ongoing" && (
                            <span style={{ marginLeft: 8 }}>
                              <IssueStatusBadge status={d.status} />
                            </span>
                          )}
                        </td>
                        <td className="dim">{v.deployed_at ? formatDateTime(new Date(v.deployed_at)) : "—"}</td>
                        <td>{formatRelative(v.last_seen)}</td>
                        <td className="num">{formatCount(v.occurrences)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="panel-state">{d ? "No version reported. Set VISIONARY_RELEASE (or a platform commit variable) in the service." : ""}</div>
            )}
          </div>
        </section>

        <section className="panel span-12">
          <header className="panel-head">
            <h3 className="panel-title">Stack trace</h3>
            {latest && (
              <span className="dim issue-latest">
                latest occurrence, {formatLogTime(String(latest.created_at))}
                {latest.version ? ` · ${shortVersion(String(latest.version))}` : ""}
                {latest.instance ? ` · ${String(latest.instance)}` : ""}
              </span>
            )}
          </header>
          <div className="panel-body">
            {latest?.stack ? <StackTrace stack={String(latest.stack)} /> : <div className="panel-state">{d ? "This occurrence has no stack trace." : ""}</div>}
          </div>
        </section>

        <Panel
          className="span-6"
          title="Affected routes"
          result={d?.routes}
          loading={q.loading}
          bodyHeight={140}
          table={d && (d.routes.rows.length === 0 ? <div className="panel-state">No occurrences in this range.</div> : <DataTable result={d.routes} columns={routeColumns} maxHeight={300} />)}
        />
        <Panel
          className="span-6"
          title="Affected users"
          description="Signed-in users who hit this error in the range."
          result={d?.users}
          loading={q.loading}
          bodyHeight={140}
          table={
            d &&
            (d.users.rows.length === 0 ? <div className="panel-state">No signed-in user hit this error in this range.</div> : <DataTable result={d.users} columns={userColumns} maxHeight={300} />)
          }
        />

        <Panel
          className="span-12"
          title="Recent occurrences"
          description="The 50 most recent in the range. Open one to see its request: headers, bodies and the calls it made."
          result={d?.recent}
          loading={q.loading}
          bodyHeight={160}
          table={
            d &&
            (d.recent.rows.length === 0 ? (
              <div className="panel-state">No occurrences in this range.</div>
            ) : (
              <DataTable
                result={d.recent}
                columns={recentColumns}
                maxHeight={460}
                rowKey={(r) => r[0]}
                selectedKey={undefined}
                onRowClick={(r) => {
                  const id = toNumber(r[d.recent.columns.indexOf("log_id")]);
                  if (id !== null) setOpenLog(id);
                }}
              />
            ))
          }
        />
      </div>
      {openLog !== null && <LogDetail id={openLog} onClose={() => setOpenLog(null)} />}
    </>
  );
}

function BackLink() {
  return (
    <a className="btn ghost sm issue-back" href="#/errors">
      <ArrowLeft size={14} />
      All errors
    </a>
  );
}

const routeColumns: Column[] = [
  { key: "method", render: (v) => (v ? <span className="method">{String(v)}</span> : <span className="dim">—</span>), width: 70 },
  { key: "route", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">background</span>) },
  { key: "occurrences", label: "Events", align: "right", unit: "count" },
  { key: "users", align: "right", unit: "count" },
  { key: "last_seen", label: "Last seen", align: "right", render: (v) => formatRelative(String(v)) },
];

const userColumns: Column[] = [
  { key: "handle", label: "User", render: (v, row) => (v ? `@${String(v)}` : <span className="dim">#{String(row[0])}</span>) },
  { key: "user_id", label: "ID", render: (v) => <span className="mono dim">{String(v)}</span> },
  { key: "occurrences", label: "Events", align: "right", unit: "count" },
  { key: "last_seen", label: "Last seen", align: "right", render: (v) => formatRelative(String(v)) },
];

const recentColumns: Column[] = [
  { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 160 },
  { key: "method", render: (v) => (v ? <span className="method">{String(v)}</span> : <span className="dim">—</span>), width: 70 },
  { key: "path", render: (v, row) => (v ? <span className="mono" title={String(row[3] ?? "")}>{String(v)}</span> : <span className="dim">background job</span>) },
  { key: "user_id", label: "User", render: (v) => (v != null ? <span className="dim">#{String(v)}</span> : <span className="dim">—</span>), width: 80 },
  { key: "version", render: (v) => (v ? <span className="mono" title={String(v)}>{shortVersion(String(v))}</span> : <span className="dim">—</span>), width: 90 },
  { key: "instance", render: (v) => <span className="dim">{String(v ?? "—")}</span>, width: 120 },
  {
    key: "log_id",
    label: "Request",
    align: "right",
    render: (v, row) =>
      v != null ? (
        <span className="issue-link">View request</span>
      ) : row[10] ? (
        <span className="dim" title="The request itself was not logged (path prefix, retention, or request logging off).">
          not logged
        </span>
      ) : (
        <span className="dim">—</span>
      ),
    width: 110,
  },
];
