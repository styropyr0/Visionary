import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, ArrowLeft, CheckCircle2, ExternalLink, HardDrive, KeyRound, Search, XCircle } from "lucide-react";
import { api, ApiError, rangeQuery, type Result } from "../lib/api";
import { formatBytes, formatCount, formatPercent, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { formatLogTime, formatRelative } from "../lib/time";
import { wordsFor } from "../lib/runtime";
import { useQuery } from "../lib/useQuery";
import { DataTable, type Column } from "../components/DataTable";
import { ErrorNotice } from "../components/ErrorNotice";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { SECTIONS, SystemPanel, type PanelDef } from "./System";
import "./Hosts.css";

type HostStatus = "online" | "stale" | "offline";

interface HostInfo {
  host: string;
  service: string | null;
  os: string | null;
  kernel: string | null;
  arch: string | null;
  cpu_count: number | null;
  mem_total_mb: number | null;
  boot_time: string | null;
  agent_version: string | null;
  ip: string | null;
  first_seen: string;
  last_seen: string;
  status: HostStatus;
  age_s: number;
  report_s: number;
}

interface HostDetailData {
  host: HostInfo;
  processes: Result;
  mounts: Result;
  units: Result;
  events: Result;
}

/** The host lives in the hash (#/hosts/db-1) so detail views can be linked. */
function readHost(): string {
  const m = window.location.hash.match(/^#\/?hosts\/([^?]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

const hostHref = (h: string) => `#/hosts/${encodeURIComponent(h)}`;

export function Hosts() {
  const [host, setHost] = useState(readHost);
  useEffect(() => {
    const onHash = () => setHost(readHost());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return host ? <HostDetail key={host} host={host} /> : <HostList />;
}

const STATUS_ICON = { online: CheckCircle2, stale: AlertTriangle, offline: XCircle };
const STATUS_LABEL = { online: "Online", stale: "Stale", offline: "Offline" };

/** Status as icon + word; color is never the only cue. */
function StatusBadge({ status, large }: { status: HostStatus; large?: boolean }) {
  const Icon = STATUS_ICON[status] ?? XCircle;
  return (
    <span className={`host-status ${status} ${large ? "large" : ""}`}>
      <Icon size={large ? 16 : 14} aria-hidden />
      {STATUS_LABEL[status] ?? status}
    </span>
  );
}

function formatUptime(sec: number): string {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

const dim = <span className="dim">—</span>;
const pct = (v: unknown) => (toNumber(v) === null ? dim : formatPercent(toNumber(v)!));

/** A percent with a small bar, red past 90 and amber past 75. */
function Meter({ value }: { value: unknown }) {
  const n = toNumber(value);
  if (n === null) return dim;
  const level = n >= 90 ? "bad" : n >= 75 ? "warn" : "ok";
  return (
    <span className="host-meter">
      <span className="host-meter-bar" aria-hidden>
        <span className={level} style={{ width: `${Math.min(100, Math.max(0, n))}%` }} />
      </span>
      {formatPercent(n)}
    </span>
  );
}

function HostList() {
  const { tick } = useApp();
  const fleet = useQuery<Result>(`hosts#${tick}`, (signal) => api<Result>("/api/hosts", { signal }));
  const [search, setSearch] = useState("");
  const data = fleet.data;
  const col = (name: string) => data?.columns.indexOf(name) ?? -1;

  const shown = useMemo<Result | undefined>(() => {
    if (!data) return undefined;
    const q = search.trim().toLowerCase();
    if (!q) return data;
    const keys = ["host", "service", "os", "ip", "status"].map((k) => data.columns.indexOf(k));
    return { ...data, rows: data.rows.filter((r) => keys.some((i) => String(r[i] ?? "").toLowerCase().includes(q))) };
  }, [data, search]);

  const tiles = useMemo(() => {
    const rows = data?.rows ?? [];
    const count = (s: HostStatus) => rows.filter((r) => r[col("status")] === s).length;
    const sum = (k: string) => rows.reduce((a, r) => a + (toNumber(r[col(k)]) ?? 0), 0);
    return { total: rows.length, online: count("online"), stale: count("stale"), offline: count("offline"), failed: sum("failed_units"), security: sum("security_24h"), warnings: sum("security_warnings_24h") };
  }, [data]);

  const columns: Column[] = [
    {
      key: "host",
      label: "Host",
      render: (v, row) => (
        <span className="host-name">
          <a href={hostHref(String(v))} onClick={(e) => e.stopPropagation()}>
            {String(v)}
          </a>
          {row[col("ip")] ? <span className="mono dim">{String(row[col("ip")])}</span> : null}
        </span>
      ),
    },
    { key: "service", label: "Service" },
    { key: "os", label: "OS" },
    { key: "status", label: "Status", render: (v) => <StatusBadge status={v as HostStatus} /> },
    { key: "cpu_percent", label: "CPU", align: "right", render: (v) => <Meter value={v} /> },
    { key: "mem_percent", label: "Memory", align: "right", render: (v) => <Meter value={v} /> },
    { key: "disk_percent", label: "Fullest disk", align: "right", render: (v) => <Meter value={v} /> },
    { key: "load_per_core", label: "Load / core", align: "right", render: (v) => (toNumber(v) === null ? dim : toNumber(v)!.toFixed(2)) },
    { key: "uptime_s", label: "Uptime", align: "right", render: (v) => (toNumber(v) === null ? dim : formatUptime(toNumber(v)!)) },
    {
      key: "failed_units",
      label: "Failed units",
      align: "right",
      render: (v) => (toNumber(v) === null ? dim : <span className={toNumber(v)! > 0 ? "host-bad" : ""}>{formatCount(toNumber(v)!)}</span>),
    },
    {
      key: "security_24h",
      label: "Security 24h",
      align: "right",
      render: (v, row) => {
        const n = toNumber(v) ?? 0;
        const w = toNumber(row[col("security_warnings_24h")]) ?? 0;
        return <span className={w > 0 ? "host-warn" : ""} title={w > 0 ? `${w} warnings` : undefined}>{formatCount(n)}</span>;
      },
    },
    { key: "last_seen", label: "Last seen", render: (v) => <span title={String(v)}>{formatRelative(String(v))}</span> },
  ];

  return (
    <>
      <TopBar title="Hosts" section="Monitor" actions={<RefreshControl busy={fleet.loading} />} />
      <div className="content">
        {fleet.error && !data && <ErrorNotice title="Could not load hosts" error={fleet.error} />}
        {data && data.rows.length === 0 && <InstallAgent />}
        {data && data.rows.length > 0 && shown && (
          <div className="grid">
            <div className="span-3 host-tile">
              <StatTile label="Hosts online" value={`${tiles.online} / ${tiles.total}`} delta={null} deltaLabel="" upIs="neutral" loading={fleet.loading} footer={`${tiles.stale} stale · ${tiles.offline} offline`} />
            </div>
            <div className="span-3 host-tile">
              <StatTile label="Not reporting" description="Hosts with no report for three report intervals or more." value={String(tiles.stale + tiles.offline)} delta={null} deltaLabel="" upIs="bad" loading={fleet.loading} footer={tiles.stale + tiles.offline ? "Open a host for details" : "Every agent is reporting"} />
            </div>
            <div className="span-3 host-tile">
              <StatTile label="Failed units" description="Failed systemd services in each host's latest report." value={formatCount(tiles.failed)} delta={null} deltaLabel="" upIs="bad" loading={fleet.loading} footer="systemd services in state failed" />
            </div>
            <div className="span-3 host-tile">
              <StatTile label="Security events (24h)" description="SSH, sudo, port, update and reboot findings from the agents." value={formatCount(tiles.security)} delta={null} deltaLabel="" upIs="bad" loading={fleet.loading} footer={`${formatCount(tiles.warnings)} warnings`} />
            </div>
            <section className="panel span-12" aria-busy={fleet.loading || undefined}>
              {fleet.loading && <div className="progress" aria-hidden />}
              <header className="panel-head">
                <h3 className="panel-title">Fleet</h3>
                <div className="panel-actions">
                  <label className="host-search">
                    <Search size={14} aria-hidden />
                    <input className="input" type="search" placeholder="Search hosts" aria-label="Search hosts" value={search} onChange={(e) => setSearch(e.target.value)} />
                  </label>
                </div>
              </header>
              <DataTable result={shown} columns={columns} rowKey={(r) => r[0]} onRowClick={(r) => (window.location.hash = hostHref(String(r[0])))} />
            </section>
          </div>
        )}
      </div>
    </>
  );
}

/** How to install the agent, with this server's URL filled in. */
function InstallAgent() {
  const url = window.location.origin;
  const docker = `docker run -d --name visionary-agent --restart unless-stopped \\
  --pid=host --net=host -v /:/host:ro \\
  -e HOST_ROOT=/host -e HOST_PROC=/host/proc -e HOST_SYS=/host/sys -e HOST_ETC=/host/etc \\
  -e VISIONARY_URL=${url} -e VISIONARY_KEY=<server key> \\
  ghcr.io/styropyr0/visionary-agent:latest`;
  const systemd = `curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \\
  | sudo VISIONARY_URL=${url} VISIONARY_KEY=<server key> sh`;
  return (
    <section className="panel">
      <div className="empty host-install">
        <div className="empty-icon">
          <HardDrive size={20} />
        </div>
        <h3>No hosts yet</h3>
        <p>
          The Visionary agent watches a whole machine (a database box, a worker, a bastion) without an instrumented app: CPU, memory,
          disks, network, top processes, systemd units, SSH logins, sudo, open ports and pending updates. It reports here over HTTPS
          and never needs database credentials.
        </p>
        <ol className="host-steps">
          <li>
            <a href="#/settings?tab=keys">
              <KeyRound size={13} /> Create an API key
            </a>{" "}
            with the <b>server</b> scope in Settings (or use one from <code className="mono">VISIONARY_INGEST_KEYS</code>).
          </li>
          <li>
            Run the agent in Docker:
            <pre className="code mono">{docker}</pre>
          </li>
          <li>
            Or install it as a systemd service:
            <pre className="code mono">{systemd}</pre>
          </li>
        </ol>
        <p className="dim">The host appears here within a minute of its first report.</p>
      </div>
    </section>
  );
}

/** The System view's panels that describe a whole machine, per section. */
const HOST_PANELS: { title: string; panels: { name: string; span: string }[] }[] = [
  { title: "CPU", panels: [{ name: "cpu", span: "span-4" }, { name: "cpu_breakdown", span: "span-4" }, { name: "load_per_core", span: "span-4" }] },
  { title: "Memory", panels: [{ name: "memory_detail", span: "span-6" }, { name: "swap", span: "span-6" }] },
  {
    title: "Disk",
    panels: [
      { name: "disk_space", span: "span-4" },
      { name: "disk_throughput", span: "span-4" },
      { name: "disk_iops", span: "span-4" },
      { name: "disk_busy", span: "span-6" },
      { name: "disk_latency", span: "span-6" },
    ],
  },
  {
    title: "Network",
    panels: [
      { name: "network", span: "span-6" },
      { name: "network_packets", span: "span-6" },
      { name: "network_errors", span: "span-4" },
      { name: "tcp", span: "span-4" },
      { name: "tcp_retrans", span: "span-4" },
    ],
  },
];

const PANEL_DEFS = new Map<string, PanelDef>(SECTIONS.flatMap((s) => s.panels).map((p) => [p.name, p]));
const SECTION_TEXT = new Map(SECTIONS.map((s) => [s.title, s.text]));
const NEUTRAL = wordsFor(null);

function HostDetail({ host }: { host: string }) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to);
  const q = useQuery<HostDetailData>(`host-${host}?${qs}#${tick}`, (signal) => api<HostDetailData>(`/api/hosts/${encodeURIComponent(host)}?${qs}`, { signal }));
  const d = q.data;
  const h = d?.host;
  const notFound = q.error instanceof ApiError && q.error.status === 404;

  return (
    <>
      <TopBar
        title={host}
        section="Hosts"
        actions={
          <>
            <a className="btn ghost" href={`#/system?instance=${encodeURIComponent(host)}`}>
              <ExternalLink size={14} />
              Open in System
            </a>
            <TimeRangePicker />
            <RefreshControl busy={q.loading} />
          </>
        }
      />
      <div className="content">
        <a className="host-back" href="#/hosts">
          <ArrowLeft size={14} />
          All hosts
        </a>
        {notFound && <div className="notice">No agent has reported for this host.</div>}
        {q.error && !notFound && !d && <ErrorNotice title="Could not load the host" error={q.error} />}
        {h && d && (
          <div className="grid">
            <section className="panel span-12 host-head">
              <div className="host-head-main">
                <StatusBadge status={h.status} large />
                <div className="host-head-text">
                  <div className="host-head-os">
                    {h.os ?? "Unknown OS"}
                    {h.kernel && <span className="dim"> · kernel {h.kernel}</span>}
                  </div>
                  <div className="dim">
                    Last report {formatRelative(h.last_seen)} · every {formatCount(h.report_s)}s{h.service && ` · service ${h.service}`}
                  </div>
                </div>
              </div>
              <dl className="kv host-kv">
                <dt>Address</dt>
                <dd className="mono">{h.ip ?? "—"}</dd>
                <dt>Architecture</dt>
                <dd>{h.arch ?? "—"}</dd>
                <dt>CPUs</dt>
                <dd>{h.cpu_count ?? "—"}</dd>
                <dt>Memory</dt>
                <dd>{h.mem_total_mb ? formatBytes(h.mem_total_mb * 1048576) : "—"}</dd>
                <dt>Booted</dt>
                <dd>{h.boot_time ? `${formatLogTime(h.boot_time)} (${formatRelative(h.boot_time)})` : "—"}</dd>
                <dt>Agent</dt>
                <dd>{h.agent_version ?? "—"}</dd>
                <dt>First seen</dt>
                <dd>{formatLogTime(h.first_seen)}</dd>
              </dl>
            </section>

            {HOST_PANELS.map((sec) => (
              <HostSection key={sec.title} title={sec.title} panels={sec.panels} host={host} />
            ))}

            <Section title="Filesystems" text="Every real mount in the latest report. Inodes run out separately from space: many small files can fill a disk that still shows free space." />
            <Panel
              className="span-12"
              title="Mounts"
              result={d.mounts}
              loading={q.loading}
              table={
                <DataTable
                  result={d.mounts}
                  maxHeight={360}
                  columns={[
                    { key: "mount", label: "Mount", render: (v) => <span className="mono">{String(v)}</span> },
                    { key: "device", label: "Device", render: (v) => (v ? <span className="mono dim">{String(v)}</span> : dim) },
                    { key: "fstype", label: "Type" },
                    { key: "used_percent", label: "Used", align: "right", render: (v) => <Meter value={v} /> },
                    { key: "free_gb", label: "Free", align: "right", render: (v) => (toNumber(v) === null ? dim : formatBytes(toNumber(v)! * 1073741824)) },
                    { key: "total_gb", label: "Size", align: "right", render: (v) => (toNumber(v) === null ? dim : formatBytes(toNumber(v)! * 1073741824)) },
                    { key: "inodes_percent", label: "Inodes used", align: "right", render: pct },
                  ]}
                />
              }
            />

            <Section title="Processes and services" text="The top processes by CPU and by memory, and the failed or watched systemd services (VISIONARY_AGENT_UNITS), from the latest report." />
            <Panel
              className="span-8"
              title="Top processes"
              description="CPU is a percent of one core, averaged over the last report interval. Command lines are clipped and obvious secrets are masked by the agent."
              result={d.processes}
              loading={q.loading}
              table={
                <DataTable
                  result={d.processes}
                  maxHeight={420}
                  columns={[
                    { key: "pid", label: "PID", align: "right" },
                    { key: "name", label: "Name" },
                    { key: "username", label: "User" },
                    { key: "cpu_percent", label: "CPU", align: "right", render: pct },
                    { key: "rss_mb", label: "Memory", align: "right", render: (v) => (toNumber(v) === null ? dim : formatBytes(toNumber(v)! * 1048576)) },
                    { key: "cmdline", label: "Command", render: (v) => (v ? <span className="mono host-cmd">{String(v)}</span> : dim) },
                  ]}
                />
              }
            />
            <Panel
              className="span-4"
              title="systemd units"
              description="Failed services, plus the services listed in VISIONARY_AGENT_UNITS. Empty when the host has no systemd."
              result={d.units}
              loading={q.loading}
              table={
                d.units.rows.length === 0 ? (
                  <div className="panel-state">No failed or watched units.</div>
                ) : (
                  <DataTable
                    result={d.units}
                    maxHeight={420}
                    columns={[
                      { key: "unit", label: "Unit", render: (v) => <span className="mono">{String(v)}</span> },
                      { key: "state", label: "State", render: (v) => <span className={`host-unit ${String(v)}`}>{String(v)}</span> },
                      { key: "sub", label: "Sub" },
                    ]}
                  />
                )
              }
            />

            <Section title="Security" text="Failed and successful SSH logins, sudo, listening ports opened or closed, pending updates and reboots, as the agent saw them in this range." />
            <Panel
              className="span-12"
              title="Host security events"
              description="One row per kind, user and address per report, with how many times it happened. Newest first, up to 200."
              result={d.events}
              loading={q.loading}
              table={
                d.events.rows.length === 0 ? (
                  <div className="panel-state">No security events in this time range.</div>
                ) : (
                  <DataTable
                    result={d.events}
                    maxHeight={420}
                    columns={[
                      { key: "created_at", label: "Time", render: (v) => <span className="mono">{formatLogTime(String(v))}</span> },
                      { key: "kind", label: "Kind", render: (v) => <span className="mono">{String(v)}</span> },
                      { key: "severity", label: "Severity", render: (v) => <span className={`host-sev ${String(v)}`}>{String(v)}</span> },
                      { key: "actor", label: "User" },
                      { key: "ip", label: "Address", render: (v) => (v ? <span className="mono">{String(v)}</span> : dim) },
                      { key: "target", label: "Target", render: (v) => (v ? <span className="mono host-cmd">{String(v)}</span> : dim) },
                      { key: "detail", label: "Detail" },
                      { key: "count", label: "Count", align: "right", unit: "count" },
                    ]}
                  />
                )
              }
            />
          </div>
        )}
      </div>
    </>
  );
}

function HostSection({ title, panels, host }: { title: string; panels: { name: string; span: string }[]; host: string }) {
  return (
    <>
      <Section title={title} text={SECTION_TEXT.get(title)} />
      {panels.map(({ name, span }) => {
        const def = PANEL_DEFS.get(name);
        return def ? <SystemPanel key={name} {...def} span={span} instance={host} words={NEUTRAL} /> : null;
      })}
    </>
  );
}
