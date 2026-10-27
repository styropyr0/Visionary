import { useMemo, useState } from "react";
import { AlertTriangle, ArrowLeft, Pause, Pencil, Play, RotateCw, ShieldCheck } from "lucide-react";
import { DataTable, type Column } from "../components/DataTable";
import { Panel } from "../components/Panel";
import { StatTile } from "../components/StatTile";
import { useToast } from "../components/Toast";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import type { Result, Viz } from "../lib/api";
import { formatMs, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { dateFormat, formatLogTime, formatRelative } from "../lib/time";
import { formatDuration, formatInterval, formatUptime, getCheckAlert, getIncidents, getPanel, getSummary, runCheck, setEnabled, type UptimeCheck, type Window } from "../lib/uptime";
import { CheckAlertLine, DefaultRuleModal } from "./Uptime.alerts";
import { useQuery } from "../lib/useQuery";
import { CheckDrawer } from "./Uptime.form";
import { BarsLegend, DayBars, StateBadge } from "./Uptime.parts";
import { ErrorNotice } from "../components/ErrorNotice";
import { InfoTip } from "../components/InfoTip";

const LATENCY: Viz = { type: "timeseries", x: "t", y: ["p50", "p95"], unit: "ms" };
const PHASES: Viz = { type: "timeseries", x: "t", y: ["DNS", "Connect", "TLS", "TTFB"], unit: "ms", stack: true };
const PHASE_COLS: Record<string, string> = { dns: "DNS", connect: "Connect", tls: "TLS", ttfb: "TTFB" };
const CERT_WARN_DAYS = 14;

const ms = (v: unknown) => (toNumber(v) === null ? <span className="dim">—</span> : formatMs(toNumber(v)!));

export function UptimeDetail({ id }: { id: number }) {
  const { resolved, tick } = useApp();
  const toast = useToast();
  const tokens = useChartTokens();
  const [version, setVersion] = useState(0);
  const [editing, setEditing] = useState(false);
  const [fixing, setFixing] = useState(false);
  const [running, setRunning] = useState(false);
  const reload = () => setVersion((v) => v + 1);
  const key = `${id}#${resolved.from.toISOString()}#${resolved.to.toISOString()}#${tick}#${version}`;

  const summary = useQuery(`up-sum-${key}`, (signal) => getSummary(id, signal));
  const latency = useQuery(`up-lat-${key}`, (signal) => getPanel(id, "latency", resolved.from, resolved.to, signal));
  const phasesRaw = useQuery(`up-ph-${key}`, (signal) => getPanel(id, "phases", resolved.from, resolved.to, signal));
  const results = useQuery(`up-res-${key}`, (signal) => getPanel(id, "results", resolved.from, resolved.to, signal));
  const incidents = useQuery(`up-inc-${key}`, (signal) => getIncidents(id, resolved.from, resolved.to, signal));
  const alert = useQuery(`up-alert-${id}#${tick}#${version}`, (signal) => getCheckAlert(id, signal));

  // Phase columns read as labels in the legend and the table.
  const phases = useMemo<Result | undefined>(
    () => phasesRaw.data && { ...phasesRaw.data, columns: phasesRaw.data.columns.map((c) => PHASE_COLS[c] ?? c) },
    [phasesRaw.data],
  );

  const s = summary.data;
  const c = s?.check;
  const st = s?.state;

  const run = async () => {
    setRunning(true);
    try {
      const { result } = await runCheck(id);
      toast(result.ok ? "ok" : "err", result.ok ? `Up, ${formatMs(result.duration_ms)}` : (result.error ?? "Check failed"));
      reload();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setRunning(false);
    }
  };
  const toggle = async (check: UptimeCheck) => {
    try {
      await setEnabled(check.id, !check.enabled);
      toast("ok", check.enabled ? "Paused" : "Resumed");
      reload();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };

  const notFound = summary.error && "status" in summary.error && (summary.error as { status: number }).status === 404;

  return (
    <>
      <TopBar
        title={c?.name ?? "Check"}
        section="Uptime"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl busy={summary.loading || latency.loading} />
            {c && s?.can_edit && (
              <>
                <button type="button" className="btn" onClick={() => void run()} disabled={running}>
                  <RotateCw size={14} className={running ? "spin" : undefined} />
                  Run now
                </button>
                <button type="button" className="btn" onClick={() => void toggle(c)}>
                  {c.enabled ? <Pause size={14} /> : <Play size={14} />}
                  {c.enabled ? "Pause" : "Resume"}
                </button>
                <button type="button" className="btn" onClick={() => setEditing(true)}>
                  <Pencil size={14} />
                  Edit
                </button>
              </>
            )}
          </>
        }
      />
      <div className="content">
        <a className="up-back" href="#/uptime">
          <ArrowLeft size={14} />
          All checks
        </a>
        {notFound && <div className="notice">This check no longer exists.</div>}
        {summary.error && !notFound && !s && <ErrorNotice title="Could not load the check" error={summary.error} />}
        {c && st && s && (
          <div className="grid">
            <section className="panel span-12 up-head">
              <div className="up-head-main">
                <StateBadge state={st.state} large />
                <div className="up-head-text">
                  <div className="mono up-url">
                    {c.method} {c.url}
                  </div>
                  <div className="dim">
                    Every {formatInterval(c.interval_s)} · timeout {c.timeout_ms / 1000}s · expects {c.expect_status}
                    {c.body_contains && ` and “${c.body_contains}”`}
                    {c.degraded_ms > 0 && ` · degraded above ${formatMs(c.degraded_ms)}`}
                    {c.on_status_page && " · on the status page"}
                  </div>
                  {alert.data && c.enabled && <CheckAlertLine alert={alert.data.alert} canEdit={!!s.can_edit} onFix={() => setFixing(true)} />}
                </div>
                <div className="up-head-last dim">
                  {st.last_at ? (
                    <>
                      Last checked {formatRelative(st.last_at)}
                      {st.last_status > 0 && ` · HTTP ${st.last_status}`}
                      {st.state !== "down" && ` · ${formatMs(st.last_duration_ms)}`}
                    </>
                  ) : (
                    "Not checked yet"
                  )}
                </div>
              </div>
              {st.state === "down" && (
                <div className="up-alert">
                  <AlertTriangle size={15} />
                  <span>
                    Down{st.incident_since && ` since ${formatLogTime(st.incident_since)} (${formatDuration((Date.now() - new Date(st.incident_since).getTime()) / 1000)})`}
                    {st.last_error && <>: {st.last_error}</>}
                  </span>
                </div>
              )}
            </section>

            {(["24h", "7d", "30d", "90d"] as Window[]).map((w) => (
              <div className="span-2 up-tile" key={w}>
                <StatTile
                  label={`Uptime ${w}`}
                  value={formatUptime(s.uptime[w])}
                  delta={null}
                  deltaLabel=""
                  upIs="good"
                  loading={summary.loading}
                  footer={s.checks_run[w] ? `${s.checks_run[w].toLocaleString()} checks` : "No checks in this window"}
                />
              </div>
            ))}
            <div className="span-2 up-tile">
              <StatTile
                label="Response p95 (24h)"
                value={s.latency.p95 === null ? "—" : formatMs(s.latency.p95)}
                delta={null}
                deltaLabel=""
                upIs="bad"
                loading={summary.loading}
                footer={s.latency.p50 !== null ? `p50 ${formatMs(s.latency.p50)} · avg ${formatMs(s.latency.avg_ms ?? 0)}` : "No successful checks"}
              />
            </div>
            <CertTile expires={st.cert_expires_at} https={c.url.startsWith("https:")} />

            <section className="panel span-12">
              <header className="panel-head">
                <h3 className="panel-title">Last 90 days</h3>
                <div className="panel-actions">
                  <BarsLegend />
                </div>
              </header>
              <div className="panel-body">
                <DayBars days={st.days} height={34} />
                <div className="up-bars-axis dim">
                  <span>90 days ago</span>
                  <span>Today (UTC)</span>
                </div>
              </div>
            </section>

            <Panel
              className="span-6"
              title="Response time"
              description="Median and 95th percentile of successful checks per interval, including reading the body."
              result={latency.data}
              loading={latency.loading}
              error={latency.error}
              bodyHeight={240}
              note={vizNote(latency.data, LATENCY, tokens)}
              chart={latency.data && <VizView result={latency.data} viz={LATENCY} height={240} title="Response time" empty="No successful checks in this range." />}
              table={latency.data && <DataTable result={latency.data} maxHeight={240} defaultUnit="ms" />}
              dashboard={{ kind: "builtin", ref: `uptime/checks/${id}/latency`, viz: LATENCY, title: s ? `${s.check.name} · Response time` : undefined }}
            />
            <Panel
              className="span-6"
              title="Where the time goes"
              description="Average time per phase of successful checks: DNS lookup, TCP connect, TLS handshake, and time to first byte after the request is sent."
              result={phases}
              loading={phasesRaw.loading}
              error={phasesRaw.error}
              bodyHeight={240}
              note={phases && vizNote(phases, PHASES, tokens)}
              chart={phases && <VizView result={phases} viz={PHASES} height={240} title="Phases" empty="No successful checks in this range." />}
              table={
                phases && (
                  <DataTable
                    result={phases}
                    maxHeight={240}
                    columns={[{ key: "t" }, ...["DNS", "Connect", "TLS", "TTFB"].map((k) => ({ key: k, align: "right" as const, render: ms }))]}
                  />
                )
              }
            />

            <Panel
              className="span-12"
              title="Incidents"
              description="Runs of consecutive failed checks in this range. An incident ends at the next successful check."
              result={incidents.data?.result}
              loading={incidents.loading}
              error={incidents.error}
              bodyHeight={80}
              table={incidents.data && (incidents.data.incidents.length === 0 ? <div className="panel-state">No incidents in this range.</div> : <DataTable result={incidents.data.result} columns={INCIDENT_COLS} maxHeight={320} sortable={false} />)}
            />

            <Panel
              className="span-12"
              title="Recent results"
              description="The latest 500 checks in this range, newest first."
              result={results.data}
              loading={results.loading}
              error={results.error}
              bodyHeight={120}
              table={results.data && <DataTable result={results.data} columns={RESULT_COLS} maxHeight={440} sortable={false} />}
            />
          </div>
        )}
      </div>
      {fixing && c && alert.data && (
        <DefaultRuleModal
          checks={[{ check: c, alert: alert.data.alert }]}
          onClose={() => setFixing(false)}
          onDone={() => {
            setFixing(false);
            reload();
          }}
        />
      )}
      {editing && c && (
        <CheckDrawer
          check={c}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            reload();
          }}
          onDeleted={() => {
            window.location.hash = "#/uptime";
          }}
        />
      )}
    </>
  );
}

function CertTile({ expires, https }: { expires?: string; https: boolean }) {
  const days = expires ? Math.floor((new Date(expires).getTime() - Date.now()) / 86_400_000) : null;
  const warn = days !== null && days < CERT_WARN_DAYS;
  return (
    <section className="panel span-2">
      <div className="stat">
        <div className="stat-label">
          Certificate
          <InfoTip text={`TLS certificate of the last response. Warns when it expires within ${CERT_WARN_DAYS} days.`} icon={<ShieldCheck size={12} />} />
        </div>
        <div className="stat-value">
          {days === null ? "—" : days < 0 ? "Expired" : days}
          {days !== null && days >= 0 && <span className="stat-unit">{days === 1 ? "day" : "days"}</span>}
        </div>
        <div className={`stat-delta ${warn ? "up-cert-warn" : ""}`}>
          {warn && <AlertTriangle size={13} aria-hidden />}
          <span className="vs" style={{ marginLeft: 0, color: warn ? "inherit" : undefined }}>
            {expires ? `${warn ? "Renew soon · " : ""}expires ${dateFormat({ year: "numeric", month: "short", day: "numeric" }).format(new Date(expires))}` : https ? "Not seen yet" : "Plain HTTP, no certificate"}
          </span>
        </div>
      </div>
    </section>
  );
}

const INCIDENT_COLS: Column[] = [
  { key: "started_at", label: "Started", render: (v) => <span className="mono">{formatLogTime(String(v))}</span> },
  {
    key: "ended_at",
    label: "Ended",
    render: (v) =>
      v ? (
        <span className="mono">{formatLogTime(String(v))}</span>
      ) : (
        <span className="up-state down">
          <AlertTriangle size={13} aria-hidden />
          Ongoing
        </span>
      ),
  },
  { key: "duration_s", label: "Duration", align: "right", render: (v) => formatDuration(toNumber(v) ?? 0) },
  { key: "failures", label: "Failed checks", align: "right", unit: "count" },
  { key: "status", label: "Status", align: "right", render: (v) => (toNumber(v) ? String(v) : <span className="dim">No response</span>) },
  { key: "error", label: "Reason", render: (v) => (v ? <span className="up-err">{String(v)}</span> : <span className="dim">—</span>) },
];

const RESULT_COLS: Column[] = [
  { key: "created_at", label: "Time", width: 190, render: (v) => <span className="mono">{formatLogTime(String(v))}</span> },
  {
    key: "ok",
    label: "Result",
    width: 110,
    render: (v, row) => <StateBadge state={toNumber(v) === 1 ? (toNumber(row[8]) === 1 ? "degraded" : "up") : "down"} />,
  },
  { key: "status", label: "Status", align: "right", render: (v) => (toNumber(v) ? String(v) : <span className="dim">—</span>) },
  { key: "duration_ms", label: "Total", align: "right", render: ms },
  { key: "dns_ms", label: "DNS", align: "right", render: ms },
  { key: "connect_ms", label: "Connect", align: "right", render: ms },
  { key: "tls_ms", label: "TLS", align: "right", render: ms },
  { key: "ttfb_ms", label: "TTFB", align: "right", render: ms },
  { key: "retried", label: "Retried", align: "right", render: (v) => (toNumber(v) === 1 ? "Yes" : <span className="dim">No</span>) },
  { key: "error", label: "Error", render: (v) => (v ? <span className="up-err">{String(v)}</span> : <span className="dim">—</span>) },
];
