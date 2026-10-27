import { useEffect, useMemo, useState } from "react";
import { FileText, Globe, ScrollText, Search, ShieldAlert, Sparkles } from "lucide-react";
import type { Result, Viz } from "../lib/api";
import { formatCount, formatPercent, humanize, relabelColumn, titleCase, toNumber, yesNo } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatLogTime } from "../lib/time";
import {
  auditLabel,
  investigateQuestion,
  kindLabel,
  sourceLabel,
  KINDS,
  rowObjects,
  securityApi,
  useSecurityPanel,
  type Finding,
  type FindingFilters,
} from "../lib/security";
import { DataTable, type Column } from "../components/DataTable";
import { Drawer } from "../components/Modal";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { Select } from "../components/Select";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { linkify } from "../components/ErrorNotice";
import { SecurityReport } from "./Security.report";
import { AccountDrawer, AuditExport, FindingActions, IntelPanel, IPCard, StatusPill, SuppressionsPanel, TriageBox } from "./Security.triage";
import "./Security.css";
import { AgentSteps, SetupCard, SetupOption } from "../components/SetupCard";

/** Re-renders on hash changes, so #/security/report opens without a route change. */
function useHash(): string {
  const [hash, setHash] = useState(window.location.hash);
  useEffect(() => {
    const on = () => setHash(window.location.hash);
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return hash;
}

export function Security() {
  const hash = useHash();
  if (/^#\/security\/report/.test(hash)) return <SecurityReport />;
  return <SecurityPage />;
}

const EMPTY: FindingFilters = { source: "", kind: "", severity: "", host: "", ip: "", status: "" };

function SecurityPage() {
  const { resolved, tick } = useApp();
  const meta = useQuery(`sec-meta#${tick}`, (signal) => securityApi.meta(signal));
  const summary = useSecurityPanel("summary");
  const failed = useSecurityPanel("failed_logins");
  const [filters, setFilters] = useState<FindingFilters>(EMPTY);
  const [selected, setSelected] = useState<Finding | null>(null);
  const [account, setAccount] = useState<string | null>(null);

  const s = rowObjects(summary.data)[0] ?? {};
  const n = (k: string) => toNumber(s[k]);
  const hosts = meta.data?.hosts ?? 0;
  const reportHref = `#/security/report?from=${encodeURIComponent(resolved.from.toISOString())}&to=${encodeURIComponent(resolved.to.toISOString())}`;

  return (
    <>
      <TopBar
        title="Security"
        actions={
          <>
            <a className="btn" href={reportHref}>
              <FileText size={14} />
              Report
            </a>
            <TimeRangePicker />
            <RefreshControl busy={summary.loading} />
          </>
        }
      />
      <div className="content">
        <div className="grid">
          {meta.data && !meta.data.enabled && (
            <div className="span-12 notice">
              <ShieldAlert size={16} />
              <span>The API detectors are off (Settings → Server → Security detectors). Host events and the audit log still show here.</span>
            </div>
          )}
          {meta.data?.error && (
            <div className="span-12 notice">
              <ShieldAlert size={16} />
              <span>The last detector run failed: {meta.data.error}</span>
            </div>
          )}
          <StatTile
            label="Findings"
            description="Security findings first seen in this range, from the API detectors and the host agent."
            value={n("findings") === null ? "—" : formatCount(n("findings")!)}
            delta={null}
            deltaLabel=""
            upIs="bad"
            loading={summary.loading}
            footer={n("findings") !== null ? `${formatCount(n("critical") ?? 0)} critical · ${formatCount(n("warning") ?? 0)} warning · ${formatCount(n("info") ?? 0)} info` : undefined}
          />
          <StatTile
            label="Failed logins"
            description="Rejected sign-ins on login routes: 401, 403, or an error key that says the credentials were wrong."
            value={n("failed_logins") === null ? "—" : formatCount(n("failed_logins")!)}
            delta={null}
            deltaLabel=""
            upIs="bad"
            loading={summary.loading}
            footer="On login-like routes"
          />
          <StatTile
            label="Scanner IPs"
            description="IPs that asked for probe paths such as /.env or /wp-admin, or sent a scanner's user agent."
            value={n("scanner_ips") === null ? "—" : formatCount(n("scanner_ips")!)}
            delta={null}
            deltaLabel=""
            upIs="bad"
            loading={summary.loading}
          />
          <StatTile
            label="Blocked or forbidden"
            description="Share of requests answered 401, 403 or 429."
            value={n("blocked_rate") === null ? "—" : formatPercent(n("blocked_rate")!)}
            delta={null}
            deltaLabel=""
            upIs="bad"
            loading={summary.loading}
            footer={n("blocked") !== null ? `${formatCount(n("blocked")!)} of ${formatCount(n("requests") ?? 0)} requests` : undefined}
          />
          <StatTile
            label="Pending updates"
            description="Hosts whose latest report has security updates waiting."
            value={hosts === 0 ? "—" : formatCount(n("pending_update_hosts") ?? 0)}
            delta={null}
            deltaLabel=""
            upIs="bad"
            loading={summary.loading}
            footer={hosts === 0 ? "No host agent yet" : `of ${formatCount(hosts)} host(s)`}
          />
          <StatTile
            label="SSH failures"
            description="Failed SSH logins reported by the host agent."
            value={hosts === 0 && !n("ssh_failures") ? "—" : formatCount(n("ssh_failures") ?? 0)}
            delta={null}
            deltaLabel=""
            upIs="bad"
            loading={summary.loading}
            footer={hosts === 0 ? "No host agent yet" : "Across all hosts"}
          />

          <Section
            title="Findings"
            text={`Each row is one finding. API findings are written at most once per kind and IP or account every ${meta.data?.dedupe ?? "10m"}; their count grows while the activity goes on.`}
          />
          <FindingsTable filters={filters} setFilters={setFilters} onSelect={setSelected} />

          <Section title="Response" text="What the detectors skip, and where they get the place, network and reputation of an address." />
          <SuppressionsPanel />
          <IntelPanel />

          <Section title="Sign-ins and access" text="Failed logins and auth errors over time, and the IPs, paths and clients behind them." />
          <ChartPanel
            name="failed_logins"
            q={failed}
            title="Failed logins"
            description="Sign-in attempts on login routes, failed and succeeded."
            viz={{ type: "timeseries", x: "t", y: ["failed", "succeeded"], unit: "count" }}
            span="span-6"
          />
          <SecurityChart
            name="auth_errors"
            title="Auth errors"
            description="4xx responses by kind: 401 (not signed in), 403 (not allowed), 429 (rate limited) and the rest."
            viz={{ type: "timeseries", x: "t", y: ["unauthorized_401", "forbidden_403", "rate_limited_429", "other_4xx"], unit: "count", stack: true }}
            span="span-6"
          />
          <SecurityChart
            name="findings_over_time"
            title="Findings over time"
            description="Findings first seen per interval, by severity."
            label={titleCase}
            viz={{ type: "timeseries", x: "t", y: ["value"], series: "series", unit: "count", stack: true }}
            span="span-6"
          />
          <SecurityTable
            name="findings_by_kind"
            title="Findings by kind"
            description="How many findings of each kind, and how many occurrences they stand for."
            span="span-6"
            columns={[
              { key: "kind", render: (v) => kindLabel(String(v)) },
              { key: "source", render: (v) => <span className="pill">{sourceLabel(String(v))}</span> },
              { key: "findings", align: "right", unit: "count" },
              { key: "occurrences", align: "right", unit: "count" },
              { key: "critical", align: "right", unit: "count" },
              { key: "last_seen", label: "Last seen", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span> },
            ]}
          />
          <SecurityTable
            name="top_ips"
            title="Top offending IPs"
            description="IPs with the most failed logins and probe requests, then client errors. Click one to see its requests."
            span="span-12"
            onRowClick={(row, r) => openLogs(String(row[r.columns.indexOf("ip")]))}
            columns={[
              { key: "ip", label: "IP", render: (v) => <span className="mono">{String(v)}</span> },
              { key: "requests", align: "right", unit: "count" },
              { key: "failed_logins", label: "Failed logins", align: "right", unit: "count" },
              { key: "unauthorized", label: "401", align: "right", unit: "count" },
              { key: "forbidden", label: "403", align: "right", unit: "count" },
              { key: "probes", align: "right", unit: "count" },
              { key: "client_errors", label: "4xx", align: "right", unit: "count" },
              { key: "paths", align: "right", unit: "count" },
              { key: "last_seen", label: "Last seen", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span> },
            ]}
          />
          <SecurityTable
            name="probed_paths"
            title="Top probed paths"
            description="Requests for paths that only scanners ask for. Any 2xx answer deserves a look."
            span="span-6"
            columns={[
              { key: "path", render: (v) => <span className="mono">{String(v)}</span> },
              { key: "hits", align: "right", unit: "count" },
              { key: "ips", label: "IPs", align: "right", unit: "count" },
              {
                key: "answered_2xx",
                label: "2xx",
                align: "right",
                render: (v) => ((toNumber(v) ?? 0) > 0 ? <span className="flag bad">{formatCount(toNumber(v)!)}</span> : <span className="dim">0</span>),
              },
            ]}
          />
          <SecurityTable
            name="top_agents"
            title="Top user agents by failures"
            description="User agents with the most 4xx responses. Known scanners are marked."
            span="span-6"
            columns={[
              {
                key: "user_agent",
                label: "User agent",
                render: (v, row) => (
                  <span className="sec-agent">
                    {toNumber(row[4]) === 1 && <span className="sec-sev critical">scanner</span>}
                    <span className="mono">{v ? String(v) : "—"}</span>
                  </span>
                ),
              },
              { key: "failures", label: "4xx", align: "right", unit: "count" },
              { key: "ips", label: "IPs", align: "right", unit: "count" },
              { key: "failed_logins", label: "Failed logins", align: "right", unit: "count" },
            ]}
          />

          <HostSecurity hosts={hosts} />
          <AuditSection admin={meta.data?.is_admin ?? false} />
        </div>
      </div>
      {account && (
        <AccountDrawer
          account={account}
          onClose={() => setAccount(null)}
          onFinding={(f) => {
            setAccount(null);
            setSelected(f);
          }}
        />
      )}
      {selected && (
        <FindingDrawer
          finding={selected}
          onClose={() => setSelected(null)}
          onTimeline={(a) => {
            setSelected(null);
            setAccount(a);
          }}
        />
      )}
    </>
  );
}

/** Opens Request logs on one IP, over the range shown (or the one given). */
function openLogs(ip: string, from?: string, to?: string, setRange?: (r: { kind: "absolute"; from: string; to: string }) => void) {
  if (from && to && setRange) setRange({ kind: "absolute", from, to });
  window.location.hash = `#/logs?ip=${encodeURIComponent(ip)}`;
}

export function SeverityBadge({ severity }: { severity: string }) {
  return <span className={`sec-sev ${severity}`}>{titleCase(severity)}</span>;
}

function whoOf(f: Finding): string {
  if (f.host) return f.ip ? `${f.host} · ${f.ip}` : f.host;
  return f.ip || f.actor || "—";
}

function FindingsTable({
  filters,
  setFilters,
  onSelect,
}: {
  filters: FindingFilters;
  setFilters: (f: FindingFilters) => void;
  onSelect: (f: Finding) => void;
}) {
  const { resolved, tick } = useApp();
  const [typed, setTyped] = useState({ host: filters.host, ip: filters.ip });
  const [older, setOlder] = useState<Finding[]>([]);
  const [more, setMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  useEffect(() => {
    const id = window.setTimeout(() => {
      if (typed.host !== filters.host || typed.ip !== filters.ip) setFilters({ ...filters, ...typed });
    }, 350);
    return () => window.clearTimeout(id);
  }, [typed, filters, setFilters]);

  const key = JSON.stringify(filters);
  const q = useQuery(`sec-findings?${resolved.from.toISOString()}-${resolved.to.toISOString()}-${key}#${tick}`, (signal) =>
    securityApi.findings(resolved.from, resolved.to, filters, signal),
  );
  useEffect(() => {
    setOlder([]);
    setMore(q.data?.more ?? false);
  }, [q.data]);
  const rows = useMemo(() => [...(q.data?.findings ?? []), ...older], [q.data, older]);
  const loadMore = async () => {
    const last = rows[rows.length - 1];
    if (!last) return;
    setLoadingMore(true);
    try {
      const next = await securityApi.findings(resolved.from, resolved.to, { ...filters, before: String(last.id) });
      setOlder((o) => [...o, ...next.findings]);
      setMore(next.more);
    } finally {
      setLoadingMore(false);
    }
  };
  const pick = (k: keyof FindingFilters) => (v: string) => setFilters({ ...filters, [k]: v });
  const result: Result = useMemo(
    () => ({
      columns: ["id", "created_at", "severity", "kind", "source", "who", "summary", "count", "status"],
      rows: rows.map((f) => [f.id, f.created_at, f.severity, f.kind, f.source, whoOf(f), f.summary || f.target, f.count, f.status]),
      truncated: false,
      rows_read: 0,
      duration_ms: 0,
      sql: "",
      bucket: 0,
      from: "",
      to: "",
    }),
    [rows],
  );
  const byId = useMemo(() => new Map(rows.map((f) => [f.id, f])), [rows]);
  const columns: Column[] = [
    { key: "created_at", label: "First seen", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 160 },
    { key: "severity", render: (v) => <SeverityBadge severity={String(v)} />, width: 90 },
    { key: "kind", render: (v) => kindLabel(String(v)), width: 150 },
    { key: "source", render: (v) => <span className="pill">{sourceLabel(String(v))}</span>, width: 70 },
    { key: "who", label: "Host, IP or account", render: (v) => <span className="mono">{String(v)}</span>, width: 200 },
    { key: "summary", label: "What happened", render: (v) => (v ? String(v) : <span className="dim">—</span>) },
    { key: "count", align: "right", unit: "count", width: 70 },
    { key: "status", render: (v) => <StatusPill status={(v as Finding["status"]) || "open"} />, width: 110 },
  ];
  const active = Object.values(filters).some(Boolean);

  return (
    <section className="panel span-12 sec-findings" aria-busy={q.loading || undefined}>
      {q.loading && <div className="progress" aria-hidden />}
      <div className="filter-row sec-filters" role="search">
        <Select
          label="Source"
          align="left"
          value={filters.source}
          onChange={pick("source")}
          options={[
            { value: "", label: "All sources" },
            { value: "api", label: "API detectors" },
            { value: "host", label: "Host agent" },
            { value: "audit", label: "Visionary itself" },
          ]}
        />
        <Select
          label="Status"
          align="left"
          value={filters.status}
          onChange={pick("status")}
          options={[
            { value: "", label: "Any status" },
            { value: "open", label: "Open" },
            { value: "acknowledged", label: "Acknowledged" },
            { value: "resolved", label: "Resolved" },
            { value: "false_positive", label: "False positive" },
          ]}
        />
        <Select
          label="Kind"
          align="left"
          value={filters.kind}
          onChange={pick("kind")}
          maxWidth={220}
          options={[{ value: "", label: "All kinds" }, ...Object.entries(KINDS).map(([k, v]) => ({ value: k, label: v.label }))]}
        />
        <Select
          label="Severity"
          align="left"
          value={filters.severity}
          onChange={pick("severity")}
          options={[
            { value: "", label: "Any severity" },
            { value: "warning", label: "Warning and up" },
            { value: "critical", label: "Critical only" },
          ]}
        />
        <input className="input" placeholder="Host" value={typed.host} onChange={(e) => setTyped({ ...typed, host: e.target.value })} style={{ width: 150 }} aria-label="Host" />
        <div className="search" style={{ maxWidth: 240 }}>
          <Search size={14} />
          <input className="input mono" placeholder="IP address" value={typed.ip} onChange={(e) => setTyped({ ...typed, ip: e.target.value })} aria-label="IP address" />
        </div>
        {active && (
          <button
            type="button"
            className="btn ghost"
            onClick={() => {
              setTyped({ host: "", ip: "" });
              setFilters(EMPTY);
            }}
          >
            Clear filters
          </button>
        )}
      </div>
      {q.error && !q.data && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {q.data &&
        (rows.length === 0 ? (
          <div className="panel-state">No findings in this range{active ? " with these filters" : ""}.</div>
        ) : (
          <DataTable result={result} columns={columns} sortable={false} maxHeight={480} rowKey={(r) => r[0]} onRowClick={(r) => onSelect(byId.get(Number(r[0]))!)} />
        ))}
      {more && (
        <div className="load-more">
          <button type="button" className="btn" onClick={loadMore} disabled={loadingMore}>
            {loadingMore ? "Loading…" : "Load older findings"}
          </button>
        </div>
      )}
    </section>
  );
}

function FindingDrawer({ finding, onClose, onTimeline }: { finding: Finding; onClose: () => void; onTimeline: (account: string) => void }) {
  const { setRange } = useApp();
  const q = useQuery(`sec-finding-${finding.id}`, (signal) => securityApi.finding(finding.id, signal));
  const [triaged, setTriaged] = useState<Finding | null>(null);
  const [ipOpen, setIpOpen] = useState(false);
  const f = triaged ?? q.data?.finding ?? finding;
  const extra = Object.entries(f.detail ?? {}).filter(([k]) => k !== "summary");
  const investigate = () => {
    window.location.hash = `#/explore?q=${encodeURIComponent(investigateQuestion(f))}`;
  };
  const reqColumns: Column[] = [
    { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 160 },
    { key: "method", render: (v) => <span className="method">{String(v)}</span>, width: 64 },
    { key: "status", align: "right", width: 60 },
    { key: "path", render: (v) => <span className="mono">{String(v)}</span> },
    { key: "error_key", label: "Error", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">—</span>) },
  ];
  return (
    <Drawer
      onClose={onClose}
      title={
        <span style={{ display: "inline-flex", gap: 10, alignItems: "center" }}>
          <SeverityBadge severity={f.severity} />
          {kindLabel(f.kind)}
        </span>
      }
    >
      {f.summary && <p className="sec-summary">{f.summary}</p>}
      <TriageBox finding={f} onChange={setTriaged} />
      <FindingActions finding={f} onTimeline={onTimeline} />
      <div className="sec-actions">
        <button type="button" className="btn primary" onClick={investigate}>
          <Sparkles size={14} />
          Investigate
        </button>
        {f.ip && (
          <button type="button" className="btn" onClick={() => openLogs(f.ip, q.data?.from, q.data?.to, setRange)}>
            <ScrollText size={14} />
            Request logs for this IP
          </button>
        )}
      </div>
      <dl className="kv">
        <dt>Kind</dt>
        <dd>
          {kindLabel(f.kind)}
          {KINDS[f.kind] && <div className="dim">{KINDS[f.kind].text}</div>}
        </dd>
        <dt>Source</dt>
        <dd>{f.source === "api" ? "API detectors" : f.source === "host" ? "Host agent" : f.source === "audit" ? "Visionary's audit log" : f.source}</dd>
        <dt>First seen</dt>
        <dd>{formatFull(new Date(f.created_at))}</dd>
        <dt>Occurrences</dt>
        <dd>{formatCount(f.count, false)}</dd>
        {f.host && (
          <>
            <dt>Host</dt>
            <dd className="mono">{f.host}</dd>
          </>
        )}
        {f.ip && (
          <>
            <dt>IP</dt>
            <dd className="sec-ip-row">
              <span className="mono">{f.ip}</span>
              {!ipOpen && (
                <button type="button" className="btn sm" onClick={() => setIpOpen(true)}>
                  <Globe size={14} />
                  About this IP
                </button>
              )}
            </dd>
          </>
        )}
        {f.actor && (
          <>
            <dt>Account</dt>
            <dd className="mono">{f.actor}</dd>
          </>
        )}
        {f.target && (
          <>
            <dt>Target</dt>
            <dd className="mono">{f.target}</dd>
          </>
        )}
        {f.service && (
          <>
            <dt>Service</dt>
            <dd>{f.service}</dd>
          </>
        )}
        {extra.map(([k, v]) => (
          <FragmentKV key={k} k={k} v={v} />
        ))}
      </dl>
      {f.ip && ipOpen && (
        <>
          <div className="section-label">About {f.ip}</div>
          <IPCard ip={f.ip} />
        </>
      )}
      <div className="section-label">{q.data?.match === "audit" ? "Audit entries" : "Related requests"}</div>
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {!q.data && !q.error && <div className="panel-state">Loading…</div>}
      {q.data?.audit && <DataTable result={q.data.audit} sortable={false} maxHeight={360} columns={auditColumns} />}
      {q.data && !q.data.requests && !q.data.audit && <p className="dim">This finding has no IP or account to match requests against.</p>}
      {q.data?.requests && (
        <>
          <p className="dim" style={{ marginTop: 4 }}>
            Requests from the same {q.data.match === "ip" ? "IP" : q.data.match === "role" ? "role" : "account"} between {formatLogTime(q.data.from)} and {formatLogTime(q.data.to)}
            {q.data.requests.truncated ? " (newest 100)" : ""}.
            {q.data.statuses && q.data.statuses.rows.length > 0 && (
              <> By status: {q.data.statuses.rows.map((r) => `${String(r[0])} × ${formatCount(toNumber(r[1]) ?? 0, false)}`).join(", ")}.</>
            )}
          </p>
          <DataTable result={q.data.requests} columns={reqColumns} sortable={false} maxHeight={360} />
        </>
      )}
    </Drawer>
  );
}

const auditColumns: Column[] = [
  { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 160 },
  { key: "action", render: (v) => auditLabel(String(v)) },
  { key: "user_name", label: "User", render: (v) => (v ? String(v) : <span className="dim">—</span>) },
  { key: "target", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">—</span>) },
  { key: "ip", label: "IP", render: (v) => <span className="mono dim">{String(v || "—")}</span> },
];

function FragmentKV({ k, v }: { k: string; v: unknown }) {
  const text = Array.isArray(v) ? v.join(", ") : typeof v === "boolean" ? yesNo(v) : typeof v === "object" && v !== null ? JSON.stringify(v) : String(v);
  return (
    <>
      <dt>{humanize(k)}</dt>
      <dd className={Array.isArray(v) ? "mono" : undefined}>{text}</dd>
    </>
  );
}

function ChartPanel({ name, q, title, description, viz, span, empty }: { name: string; q: ReturnType<typeof useSecurityPanel>; title: string; description: string; viz: Viz; span: string; empty?: string }) {
  const tokens = useChartTokens();
  const height = 220;
  return (
    <Panel
      className={span}
      title={title}
      description={description}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={height}
      note={vizNote(q.data, viz, tokens)}
      chart={q.data && <VizView result={q.data} viz={viz} height={height} title={title} empty={empty} />}
      table={q.data && <DataTable result={q.data} maxHeight={height} defaultUnit={viz.unit} />}
      dashboard={{ kind: "builtin", ref: `security/panels/${name}`, viz }}
    />
  );
}

function SecurityChart({ label, ...props }: { name: string; title: string; description: string; viz: Viz; span: string; empty?: string; label?: (v: string) => string }) {
  const q = useSecurityPanel(props.name);
  const data = useMemo(() => (q.data && label ? relabelColumn(q.data, "series", label) : q.data), [q.data, label]);
  return <ChartPanel {...props} q={{ ...q, data }} />;
}

function SecurityTable({
  name,
  title,
  description,
  span,
  columns,
  onRowClick,
  empty,
}: {
  name: string;
  title: string;
  description: string;
  span: string;
  columns: Column[];
  onRowClick?: (row: unknown[], r: Result) => void;
  empty?: string;
}) {
  const q = useSecurityPanel(name);
  return (
    <Panel
      className={span}
      title={title}
      description={description}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={160}
      table={
        q.data &&
        (q.data.rows.length === 0 && empty ? (
          <div className="panel-state">{empty}</div>
        ) : (
          <DataTable result={q.data} columns={columns} maxHeight={360} onRowClick={onRowClick ? (row) => onRowClick(row, q.data!) : undefined} />
        ))
      }
    />
  );
}

const NO_AGENT = "Install the Visionary host agent on your machines to see SSH logins, open ports and pending updates";

function HostSecurity({ hosts }: { hosts: number }) {
  const status = useSecurityPanel("host_status");
  const noAgent = hosts === 0 && status.data !== undefined && status.data.rows.length === 0;
  return (
    <>
      <Section title="Host security" text="What the Visionary host agent reports from your VMs: SSH logins, sudo use, listening ports and pending updates." />
      {noAgent ? (
        <SetupCard icon={<ShieldAlert size={20} />} title="No machines report here yet" text={`${NO_AGENT}.`} topic="agent" className="span-12 sc-inline">
          <SetupOption title="Install the host agent" text="It reads the auth log or journald for SSH and sudo, watches listening ports, and checks for updates every 6 hours.">
            <AgentSteps />
          </SetupOption>
        </SetupCard>
      ) : (
        <>
          <Panel
            className="span-12"
            title="Hosts"
            description="Per host over the range: failed and successful SSH logins, sudo use, ports opened and closed, reboots, and the updates waiting in its latest report."
            result={status.data}
            loading={status.loading}
            error={status.error}
            bodyHeight={120}
            table={
              status.data && (
                <DataTable
                  result={status.data}
                  maxHeight={360}
                  columns={[
                    { key: "host", render: (v) => <span className="mono">{String(v)}</span> },
                    { key: "ssh_failures", label: "SSH failures", align: "right", render: (v) => countFlag(v, 1) },
                    { key: "ssh_logins", label: "SSH logins", align: "right", unit: "count" },
                    { key: "sudo", align: "right", unit: "count" },
                    { key: "ports_opened", label: "Ports opened", align: "right", render: (v) => countFlag(v, 1, "warn") },
                    { key: "ports_closed", label: "Ports closed", align: "right", unit: "count" },
                    { key: "reboots", align: "right", unit: "count" },
                    { key: "pending_updates", label: "Pending updates", align: "right", render: (v) => (v == null ? <span className="dim">—</span> : countFlag(v, 1, "warn")) },
                    { key: "last_event", label: "Last event", render: (v) => (v ? <span className="mono dim">{formatLogTime(String(v))}</span> : <span className="dim">—</span>) },
                  ]}
                />
              )
            }
          />
          <SecurityChart
            name="ssh_failures"
            title="SSH failures by host"
            description="Failed SSH logins per interval, per host."
            viz={{ type: "timeseries", x: "t", y: ["value"], series: "series", unit: "count", stack: true }}
            span="span-6"
            empty="No failed SSH logins in this range."
          />
          <SecurityTable
            name="host_events"
            title="Host events"
            description="SSH logins, sudo, ports, updates and reboots, newest first."
            span="span-6"
            empty="No host events in this range."
            columns={[
              { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span> },
              { key: "host", render: (v) => <span className="mono">{String(v ?? "—")}</span> },
              { key: "kind", render: (v) => kindLabel(String(v)) },
              { key: "actor", label: "User", render: (v) => (v ? String(v) : <span className="dim">—</span>) },
              { key: "target", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">—</span>) },
            ]}
          />
        </>
      )}
    </>
  );
}

function countFlag(v: unknown, at: number, tone: "bad" | "warn" = "bad") {
  const n = toNumber(v) ?? 0;
  return n >= at ? <span className={`flag ${tone}`}>{formatCount(n)}</span> : <span className="dim">0</span>;
}

function AuditSection({ admin }: { admin: boolean }) {
  const { resolved, tick } = useApp();
  const q = useQuery(admin ? `sec-audit?${resolved.from.toISOString()}-${resolved.to.toISOString()}#${tick}` : null, (signal) =>
    securityApi.audit(resolved.from, resolved.to, signal),
  );
  const result: Result | undefined = useMemo(() => {
    if (!q.data) return undefined;
    return {
      columns: ["created_at", "action", "user", "target", "ip"],
      rows: q.data.entries.map((e) => [e.created_at, e.action, e.user_name || e.user_id || "—", e.target, e.ip]),
      truncated: false,
      rows_read: 0,
      duration_ms: 0,
      sql: "",
      bucket: 0,
      from: "",
      to: "",
    };
  }, [q.data]);
  return (
    <>
      <Section title="Visionary audit" text="Sensitive actions in Visionary itself: sign-ins and failed sign-ins, API keys, notification channels, role mapping, secrets shown and security actions." />
      {!admin ? (
        <div className="span-12 panel">
          <div className="panel-state">Only admins can see Visionary's audit log.</div>
        </div>
      ) : (
        <Panel
          className="span-12"
          title="Recent sensitive actions"
          description="Read from Visionary's audit log; the full log is in Settings."
          result={result}
          loading={q.loading}
          error={q.error}
          bodyHeight={120}
          extraActions={
            q.data && q.data.counts.length > 0 ? (
              <span className="dim sec-counts">{q.data.counts.map((c) => `${auditLabel(c.action)} ${c.count}`).join(" · ")}</span>
            ) : undefined
          }
          table={
            result &&
            (result.rows.length === 0 ? (
              <div className="panel-state">No sensitive actions in this range.</div>
            ) : (
              <DataTable
                result={result}
                maxHeight={360}
                sortable={false}
                columns={[
                  { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 160 },
                  { key: "action", render: (v) => <span className={String(v).endsWith("failed") || String(v).endsWith("refused") ? "flag bad" : undefined}>{auditLabel(String(v))}</span> },
                  { key: "user" },
                  { key: "target", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">—</span>) },
                  { key: "ip", label: "IP", render: (v) => <span className="mono dim">{String(v || "—")}</span> },
                ]}
              />
            ))
          }
        />
      )}
      {admin && <AuditExport />}
    </>
  );
}
