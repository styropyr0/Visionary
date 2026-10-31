import { useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, CheckCircle2, Copy, RefreshCw } from "lucide-react";
import { DataTable, type Column } from "../components/DataTable";
import { InfoTip } from "../components/InfoTip";
import { Panel } from "../components/Panel";
import { useToast } from "../components/Toast";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import type { Result, Viz } from "../lib/api";
import { copyText } from "../lib/clipboard";
import { formatMs, toNumber } from "../lib/format";
import { dateFormat, formatLogTime, formatRelative } from "../lib/time";
import type { CheckKind, UptimeCheck } from "../lib/uptime";
import { formatDuration } from "../lib/uptime";
import { heartbeatURL, newHeartbeatURL, type DetailRun, type HeartbeatView } from "../lib/uptimeKinds";
import { heartbeatSchedule, msText } from "./Uptime.kinds";
import { StateBadge } from "./Uptime.parts";

const asResult = (columns: string[], rows: unknown[][]): Result => ({ columns, rows, truncated: false, rows_read: rows.length, duration_ms: 0, sql: "", bucket: 0, from: "", to: "" });

const ms = (v: unknown) => (toNumber(v) === null ? <span className="dim">—</span> : formatMs(toNumber(v)!));
const time = (v: unknown) => <span className="mono">{formatLogTime(String(v))}</span>;
const errCell = (v: unknown) => (v ? <span className="up-err">{String(v)}</span> : <span className="dim">—</span>);
const resultCell = (v: unknown, row: unknown[]) => <StateBadge state={v ? (row[2] ? "degraded" : "up") : "down"} />;

export const LATENCY_TEXT: Record<CheckKind, { label: string; title: string; text: string }> = {
  http: { label: "Response", title: "Response time", text: "Median and 95th percentile of successful checks per interval, including reading the body." },
  browser: { label: "Response", title: "Response time", text: "Median and 95th percentile of the whole script per interval, for runs that passed." },
  api: { label: "Run time", title: "Run time", text: "Median and 95th percentile of all requests of a run together, for runs that passed." },
  tcp: { label: "Connect", title: "Connect time", text: "Median and 95th percentile of the time to connect, and to read the reply when one is expected, for runs that passed." },
  dns: { label: "Resolution", title: "Resolution time", text: "Median and 95th percentile of the time to resolve the name, for runs that passed." },
  ping: { label: "Round trip", title: "Round trip", text: "Median and 95th percentile of the average round trip of each run." },
  ssl: { label: "Handshake", title: "Connect and handshake", text: "Median and 95th percentile of the time to connect and complete the TLS handshake, for runs that passed." },
  playwright: { label: "Test time", title: "Test time", text: "Median and 95th percentile of the test's duration per interval, for runs that passed." },
  heartbeat: { label: "Job run time", title: "Job run time", text: "Median and 95th percentile of the time between each start ping and its success ping, for jobs that send /start." },
};

export function KindTile({ check, runs, heartbeat }: { check: UptimeCheck; runs?: DetailRun[]; heartbeat?: HeartbeatView }) {
  const last = runs?.[0];
  let label = "Last run";
  let value = "—";
  let unit = "";
  let footer = "No runs yet";
  switch (check.kind) {
    case "api": {
      const reqs = last?.detail.requests ?? [];
      label = "Requests";
      value = String(check.api?.requests.length ?? 0);
      if (last) footer = last.ok ? "All passed last run" : `Failed at ${reqs.find((q) => q.state === "failed")?.name ?? "a request"}`;
      break;
    }
    case "tcp":
      label = "Connect";
      if (last?.detail.connect_ms !== undefined) value = formatMs(last.detail.connect_ms);
      if (last) footer = last.detail.address || last.error || "";
      break;
    case "dns":
      label = "Answers";
      if (last) {
        value = String(last.detail.answers?.length ?? 0);
        footer = last.detail.answers?.[0] ?? last.error;
      }
      break;
    case "ping":
      label = "Packet loss";
      if (last?.detail.loss_pct !== undefined) {
        value = String(last.detail.loss_pct);
        unit = "%";
        footer = `avg ${msText(last.detail.avg_ms)} · ${last.detail.received}/${last.detail.sent} replies`;
      }
      break;
    case "heartbeat":
      label = "Last ping";
      if (heartbeat) {
        value = heartbeat.last_ping ? formatRelative(heartbeat.last_ping) : "None";
        footer = heartbeat.next_expected ? `next by ${formatLogTime(heartbeat.next_expected)}` : "";
      }
      break;
  }
  return (
    <section className="panel span-2">
      <div className="stat">
        <div className="stat-label">{label}</div>
        <div className="stat-value">
          {value}
          {unit && <span className="stat-unit">{unit}</span>}
        </div>
        <div className="stat-delta">
          <span className="vs" style={{ marginLeft: 0 }} title={footer}>
            {footer}
          </span>
        </div>
      </div>
    </section>
  );
}

export function withLocation(result: Result, cols: Column[], runs?: DetailRun[]): { result: Result; columns: Column[] } {
  if (!runs?.some((r) => r.location && r.location !== "server")) return { result, columns: cols };
  const at = Math.max(1, cols.findIndex((c) => c.key === "ok") + 1);
  return {
    result: { ...result, columns: [...result.columns, "location"], rows: result.rows.map((row, i) => [...row, runs[i]?.location ?? "server"]) },
    columns: [...cols.slice(0, at), { key: "location", label: "Location" }, ...cols.slice(at)],
  };
}

function Box({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <section className="panel span-12">
      <header className="panel-head">
        <h3 className="panel-title">{title}</h3>
        <InfoTip text={description} />
      </header>
      <div className="panel-body">{children}</div>
    </section>
  );
}

const RTT: Viz = { type: "timeseries", x: "t", y: ["Average", "Max"], unit: "ms" };
const LOSS: Viz = { type: "timeseries", x: "t", y: ["Loss"], unit: "percent" };

export function PingPanels({ runs, loading, error }: { runs?: DetailRun[]; loading: boolean; error?: Error | null }) {
  const tokens = useChartTokens();
  const { rtt, loss, table } = useMemo(() => {
    if (!runs) return {};
    const asc = [...runs].reverse();
    return {
      rtt: asResult(["t", "Average", "Max"], asc.map((r) => [r.created_at, r.detail.avg_ms ?? null, r.detail.max_ms ?? null])),
      loss: asResult(["t", "Loss"], asc.map((r) => [r.created_at, r.detail.loss_pct ?? null])),
      table: asResult(
        ["created_at", "ok", "degraded", "address", "received", "loss", "min", "avg", "max", "error"],
        runs.map((r) => [r.created_at, r.ok, r.degraded, r.detail.address ?? "", `${r.detail.received ?? 0} / ${r.detail.sent ?? 0}`, r.detail.loss_pct ?? null, r.detail.min_ms ?? null, r.detail.avg_ms ?? null, r.detail.max_ms ?? null, r.error]),
      ),
    };
  }, [runs]);
  const cols: Column[] = [
    { key: "created_at", label: "Time", width: 190, render: time },
    { key: "ok", label: "Result", width: 110, render: resultCell },
    { key: "address", label: "Address", render: (v) => <span className="mono">{String(v) || "—"}</span> },
    { key: "received", label: "Replies", align: "right" },
    { key: "loss", label: "Loss", align: "right", render: (v) => (toNumber(v) === null ? <span className="dim">—</span> : `${toNumber(v)}%`) },
    { key: "min", label: "Min", align: "right", render: ms },
    { key: "avg", label: "Avg", align: "right", render: ms },
    { key: "max", label: "Max", align: "right", render: ms },
    { key: "error", label: "Error", render: errCell },
  ];
  return (
    <>
      <Panel
        className="span-6"
        title="Round trip"
        description="Average and slowest reply of each run, in milliseconds."
        result={rtt}
        loading={loading}
        error={error ?? undefined}
        bodyHeight={240}
        note={rtt && vizNote(rtt, RTT, tokens)}
        chart={rtt && <VizView result={rtt} viz={RTT} height={240} title="Round trip" empty="No replies in this range." />}
        table={rtt && <DataTable result={rtt} maxHeight={240} defaultUnit="ms" />}
      />
      <Panel
        className="span-6"
        title="Packet loss"
        description="Share of each run's packets that got no reply."
        result={loss}
        loading={loading}
        error={error ?? undefined}
        bodyHeight={240}
        note={loss && vizNote(loss, LOSS, tokens)}
        chart={loss && <VizView result={loss} viz={LOSS} height={240} title="Packet loss" empty="No runs in this range." />}
        table={loss && <DataTable result={loss} maxHeight={240} />}
      />
      <Panel className="span-12" title="Recent runs" description="The latest 500 runs in this range, newest first." result={table} loading={loading} error={error ?? undefined} bodyHeight={120} table={table && <DataTable {...withLocation(table, cols, runs)} maxHeight={440} sortable={false} />} />
    </>
  );
}

export function DnsPanels({ runs, loading, error }: { runs?: DetailRun[]; loading: boolean; error?: Error | null }) {
  const latest = runs?.[0];
  const table = runs && asResult(["created_at", "ok", "degraded", "server", "resolve", "answers", "error"], runs.map((r) => [r.created_at, r.ok, r.degraded, r.detail.server ?? "", r.detail.resolve_ms ?? null, (r.detail.answers ?? []).join(", "), r.error]));
  const cols: Column[] = [
    { key: "created_at", label: "Time", width: 190, render: time },
    { key: "ok", label: "Result", width: 110, render: resultCell },
    { key: "server", label: "Server", render: (v) => <span className="mono">{String(v)}</span> },
    { key: "resolve", label: "Resolution", align: "right", render: ms },
    { key: "answers", label: "Answers", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">None</span>) },
    { key: "error", label: "Error", render: errCell },
  ];
  return (
    <>
      <Box title="Latest answers" description="What the last run resolved, sorted.">
        {latest ? (
          <div className="up-answers">
            {(latest.detail.answers ?? []).length === 0 ? <span className="dim">No answers.</span> : latest.detail.answers!.map((a) => <code key={a}>{a}</code>)}
            <span className="dim">
              {latest.detail.type} via {latest.detail.server} · {msText(latest.detail.resolve_ms)} · {formatRelative(latest.created_at)}
            </span>
          </div>
        ) : (
          <div className="panel-state">No runs in this range.</div>
        )}
      </Box>
      <Panel className="span-12" title="Recent runs" description="The latest 500 runs in this range, newest first." result={table} loading={loading} error={error ?? undefined} bodyHeight={120} table={table && <DataTable {...withLocation(table, cols, runs)} maxHeight={440} sortable={false} />} />
    </>
  );
}

export function TcpPanels({ runs, loading, error }: { runs?: DetailRun[]; loading: boolean; error?: Error | null }) {
  const table = runs && asResult(["created_at", "ok", "degraded", "address", "connect", "banner", "error"], runs.map((r) => [r.created_at, r.ok, r.degraded, r.detail.address ?? "", r.detail.connect_ms ?? null, r.detail.banner ?? "", r.error]));
  const cols: Column[] = [
    { key: "created_at", label: "Time", width: 190, render: time },
    { key: "ok", label: "Result", width: 110, render: resultCell },
    { key: "address", label: "Address", render: (v) => <span className="mono">{String(v) || "—"}</span> },
    { key: "connect", label: "Connect", align: "right", render: ms },
    { key: "banner", label: "Reply", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">—</span>) },
    { key: "error", label: "Error", render: errCell },
  ];
  return <Panel className="span-12" title="Recent runs" description="The latest 500 runs in this range, newest first, with the start of what the server sent." result={table} loading={loading} error={error ?? undefined} bodyHeight={120} table={table && <DataTable {...withLocation(table, cols, runs)} maxHeight={440} sortable={false} />} />;
}

export function CertPanels({ check, runs, loading, error }: { check: UptimeCheck; runs?: DetailRun[]; loading: boolean; error?: Error | null }) {
  const d = runs?.[0]?.detail;
  const day = (s?: string) => (s ? dateFormat({ year: "numeric", month: "short", day: "numeric" }).format(new Date(s)) : "—");
  const table = runs && asResult(["created_at", "ok", "degraded", "days", "tls", "error"], runs.map((r) => [r.created_at, r.ok, r.degraded, r.detail.days_left ?? null, r.detail.tls_version ?? "", r.error]));
  const cols: Column[] = [
    { key: "created_at", label: "Time", width: 190, render: time },
    { key: "ok", label: "Result", width: 110, render: resultCell },
    { key: "days", label: "Days left", align: "right" },
    { key: "tls", label: "TLS", align: "right", render: (v) => (v ? `TLS ${v}` : <span className="dim">—</span>) },
    { key: "error", label: "Error", render: errCell },
  ];
  const ok = (good: boolean | undefined, ignored: boolean | undefined, yes: string, no: string) => (
    <span className={good ? "" : ignored ? "dim" : "up-err"}>
      {good ? <CheckCircle2 size={13} aria-hidden className="up-ok-icon" /> : <AlertTriangle size={13} aria-hidden />} {good ? yes : `${no}${ignored ? " (ignored)" : ""}`}
    </span>
  );
  return (
    <>
      <Box title="Certificate" description="As the last run saw it.">
        {d?.subject ? (
          <dl className="kv up-cert">
            <dt>Subject</dt>
            <dd className="mono">{d.subject}</dd>
            <dt>Names</dt>
            <dd className="mono">{(d.names ?? []).join(", ") || "—"}</dd>
            <dt>Issuer</dt>
            <dd>{d.issuer}</dd>
            <dt>Valid</dt>
            <dd>
              {day(d.not_before)} to {day(d.not_after)} · {d.days_left !== undefined && d.days_left < 0 ? "expired" : `${d.days_left} days left`}
            </dd>
            <dt>Chain</dt>
            <dd>
              {ok(d.chain_valid, check.ssl?.ignore_chain, "Trusted", "Not trusted")}
              {d.chain_error && <div className="dim">{d.chain_error}</div>}
              <div className="mono dim">{(d.chain ?? []).join(" → ")}</div>
            </dd>
            <dt>Host name</dt>
            <dd>{ok(d.hostname_valid, check.ssl?.ignore_hostname, `Matches ${check.ssl?.server_name || check.ssl?.host}`, `Does not name ${check.ssl?.server_name || check.ssl?.host}`)}</dd>
            <dt>Connection</dt>
            <dd>
              TLS {d.tls_version} · <span className="mono">{d.cipher}</span> · {d.address}
            </dd>
          </dl>
        ) : (
          <div className="panel-state">{runs?.[0]?.error || (loading ? "" : "No certificate seen in this range.")}</div>
        )}
      </Box>
      <Panel className="span-12" title="Recent runs" description="The latest 500 runs in this range, newest first." result={table} loading={loading} error={error ?? undefined} bodyHeight={120} table={table && <DataTable {...withLocation(table, cols, runs)} maxHeight={440} sortable={false} />} />
    </>
  );
}

export function HeartbeatPanels({ check, view, loading, error, canEdit, onChanged }: { check: UptimeCheck; view?: HeartbeatView; loading: boolean; error?: Error | null; canEdit: boolean; onChanged: () => void }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const url = view?.path ? heartbeatURL(view.path) : "";
  const copy = async (text: string) => {
    if (await copyText(text)) toast("ok", "Copied.");
  };
  const regenerate = async () => {
    if (!window.confirm("Make a new URL? The current one stops working at once, so update every job that calls it.")) return;
    setBusy(true);
    try {
      await newHeartbeatURL(check.id);
      toast("ok", "New URL made");
      onChanged();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const table = view && asResult(["created_at", "kind", "exit_code", "duration_ms", "message"], view.pings.map((p) => [p.created_at, p.kind, p.exit_code, p.duration_ms, p.message]));
  const cols: Column[] = [
    { key: "created_at", label: "Time", width: 190, render: time },
    { key: "kind", label: "Ping", width: 120, render: (v) => (v === "start" ? <span className="dim">Start</span> : <StateBadge state={v === "success" ? "up" : "down"} />) },
    { key: "exit_code", label: "Exit code", align: "right", render: (v) => (v === null || v === undefined ? <span className="dim">—</span> : String(v)) },
    { key: "duration_ms", label: "Run time", align: "right", render: (v) => (toNumber(v) === null ? <span className="dim">—</span> : formatDuration(toNumber(v)! / 1000)) },
    { key: "message", label: "Message", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">—</span>) },
  ];
  return (
    <>
      <Box title="Heartbeat" description={`Expected ${heartbeatSchedule(check.heartbeat)}.`}>
        {view && (
          <div className="up-hb">
            {url ? (
              <div className="up-hb-url">
                <code className="mono">{url}</code>
                <button type="button" className="btn sm" onClick={() => void copy(url)} title="Copy the URL">
                  <Copy size={13} />
                  Copy
                </button>
                <button type="button" className="btn sm" onClick={() => void regenerate()} disabled={busy || !canEdit} title="Make a new URL; the current one stops working">
                  <RefreshCw size={13} />
                  New URL
                </button>
              </div>
            ) : (
              <div className="dim">Editors can see the URL to call.</div>
            )}
            {url && (
              <div className="up-hb-examples">
                <code className="mono">curl -fsS -m 10 --retry 3 {url}</code>
                <span className="dim">
                  Add <code>/start</code> before the job for its run time, <code>/fail</code> or <code>/$?</code> (the exit code) after it to report a failure, and POST a short message as the body.
                </span>
              </div>
            )}
            <dl className="kv">
              <dt>Last ping</dt>
              <dd>{view.last_ping ? `${formatRelative(view.last_ping)} (${formatLogTime(view.last_ping)})` : "None yet"}</dd>
              <dt>Next expected</dt>
              <dd>
                {view.next_expected ? `${formatLogTime(view.next_expected)}, late after ${formatLogTime(view.late_after)}` : "—"}
                {view.waiting && <span className="dim"> · waiting for the first ping</span>}
              </dd>
              <dt>Last run time</dt>
              <dd>{view.last_duration_ms === null ? <span className="dim">Send /start before the job to measure it</span> : formatDuration(view.last_duration_ms / 1000)}</dd>
              {!view.ok && (
                <>
                  <dt>Why it is down</dt>
                  <dd className="up-err">{view.reason}</dd>
                </>
              )}
            </dl>
          </div>
        )}
        {!view && error && <div className="panel-state error">{error.message}</div>}
      </Box>
      <Panel
        className="span-12"
        title="Pings"
        description="Pings received in this range, newest first: success, start and failure, with exit codes, messages and run times (start to success)."
        result={table}
        loading={loading}
        error={error ?? undefined}
        bodyHeight={120}
        table={table && (view!.pings.length === 0 ? <div className="panel-state">No pings in this range.</div> : <DataTable result={table} columns={cols} maxHeight={440} sortable={false} />)}
      />
    </>
  );
}
