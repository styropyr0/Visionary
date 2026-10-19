import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, Box, ExternalLink, Search, Server } from "lucide-react";
import { api, rangeQuery, type Result, type Viz } from "../lib/api";
import { formatBytes, formatCount, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { formatDateTime, formatFull, formatLogTime, formatRelative } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { DataTable, type Column } from "../components/DataTable";
import { ErrorNotice } from "../components/ErrorNotice";
import { dim, formatUptime, HeadCard, InstallAgent, Meter, type Fact } from "../components/Machine";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { Select } from "../components/Select";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import "./Containers.css";

/** The container lives in the hash (#/containers/db-1/postgres) so it can be linked. */
function readTarget(): { host: string; name: string } {
  const m = window.location.hash.match(/^#\/?containers\/([^/?]+)\/([^?]+)/);
  return m ? { host: decodeURIComponent(m[1]), name: decodeURIComponent(m[2]) } : { host: "", name: "" };
}

const containerHref = (host: string, name: string) => `#/containers/${encodeURIComponent(host)}/${encodeURIComponent(name)}`;

export function Containers() {
  const [target, setTarget] = useState(readTarget);
  useEffect(() => {
    const onHash = () => setTarget(readTarget());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return target.name ? <ContainerDetail key={`${target.host}/${target.name}`} {...target} /> : <ContainerList />;
}

function useContainers() {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to);
  return useQuery<Result>(`containers?${qs}#${tick}`, (signal) => api<Result>(`/api/containers?${qs}`, { signal }));
}

function useContainerData(path: string, params: Record<string, string>) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, params);
  return useQuery<Result>(`ctr-${path}?${qs}#${tick}`, (signal) => api<Result>(`/api/containers/${path}?${qs}`, { signal }));
}

/** A result's rows as objects. */
function records(r: Result | undefined): Record<string, unknown>[] {
  if (!r) return [];
  return r.rows.map((row) => Object.fromEntries(r.columns.map((c, i) => [c, row[i]])));
}

const STATE_TONE: Record<string, string> = { running: "ok", restarting: "warn", paused: "warn", exited: "bad", dead: "bad", created: "muted", removing: "muted" };

function StateBadge({ state, health }: { state: unknown; health?: unknown }) {
  const s = String(state ?? "");
  const h = health ? String(health) : "";
  const tone = h === "unhealthy" ? "bad" : (STATE_TONE[s] ?? "muted");
  return (
    <span className={`ctr-state ${tone}`}>
      <span className="ctr-dot" aria-hidden />
      {s}
      {h && <span className="ctr-health">({h})</span>}
    </span>
  );
}

const KIND_LABEL: Record<string, string> = {
  restarted: "Restarted",
  oom_killed: "Out of memory",
  exited: "Exited",
  unhealthy: "Unhealthy",
  healthy: "Healthy again",
};

const bytesMB = (v: unknown) => (toNumber(v) === null ? dim : formatBytes(toNumber(v)! * 1048576));

const eventColumns = (withContainer: boolean): Column[] => [
  { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span> },
  ...(withContainer
    ? [
        {
          key: "container",
          label: "Container",
          render: (v: unknown, row: unknown[]) => (
            <a className="mono" href={containerHref(String(row[1]), String(v))} onClick={(e) => e.stopPropagation()}>
              {String(v)}
            </a>
          ),
        },
      ]
    : []),
  { key: "kind", label: "Event", render: (v) => KIND_LABEL[String(v)] ?? String(v) },
  { key: "severity", render: (v) => <span className={`host-sev ${String(v)}`}>{String(v)}</span> },
  { key: "exit_code", label: "Exit code", align: "right", render: (v) => (v == null ? dim : String(v)) },
  { key: "detail", render: (v) => (v ? String(v) : dim) },
  { key: "count", align: "right", unit: "count" },
];

function ContainerList() {
  const list = useContainers();
  const [host, setHost] = useState("");
  const [search, setSearch] = useState("");
  const all = records(list.data);
  const hosts = [...new Set(all.map((r) => String(r.host)))];
  const rows = all.filter((r) => !host || r.host === host);
  const events = useContainerData("events", host ? { host } : {});

  const shown = useMemo<Result | undefined>(() => {
    if (!list.data) return undefined;
    const q = search.trim().toLowerCase();
    const hi = list.data.columns.indexOf("host");
    const keys = ["name", "image", "project", "state", "health", "ports"].map((k) => list.data!.columns.indexOf(k));
    return {
      ...list.data,
      rows: list.data.rows.filter((r) => (!host || r[hi] === host) && (!q || keys.some((i) => String(r[i] ?? "").toLowerCase().includes(q)))),
    };
  }, [list.data, host, search]);

  const ev = records(events.data);
  const running = rows.filter((r) => r.state === "running").length;
  const unhealthy = rows.filter((r) => r.health === "unhealthy").length;
  const sumKind = (k: string) => ev.filter((e) => e.kind === k).reduce((a, e) => a + (toNumber(e.count) ?? 0), 0);
  const nearLimit = rows.filter((r) => (toNumber(r.mem_percent) ?? 0) >= 90).length;
  const filter: Record<string, string> = host ? { host } : {};

  const columns: Column[] = [
    {
      key: "name",
      label: "Container",
      render: (v, row) => (
        <span className="host-name">
          <a href={containerHref(String(row[0]), String(v))} onClick={(e) => e.stopPropagation()}>
            {String(v)}
          </a>
          <span className="mono dim ctr-image">{String(row[2] ?? "")}</span>
        </span>
      ),
    },
    ...(hosts.length > 1 ? [{ key: "host", label: "Machine", render: (v: unknown) => <span className="mono">{String(v)}</span> }] : []),
    { key: "project", label: "Project", render: (v) => (v ? String(v) : dim) },
    { key: "state", label: "State", render: (v, row) => <StateBadge state={v} health={row[5]} /> },
    { key: "cpu_percent", label: "CPU", align: "right", render: (v) => <Meter value={v} /> },
    {
      key: "mem_used_mb",
      label: "Memory",
      align: "right",
      render: (v, row) => (
        <span className="ctr-mem">
          {bytesMB(v)}
          {toNumber(row[15]) !== null && <span className="dim"> / {formatBytes(toNumber(row[15])! * 1048576)}</span>}
        </span>
      ),
    },
    { key: "mem_percent", label: "Of limit", align: "right", render: (v) => <Meter value={v} /> },
    { key: "restarts", align: "right", render: (v) => (toNumber(v) ? <span className="host-warn">{formatCount(toNumber(v)!)}</span> : <span className="dim">0</span>) },
    {
      key: "started_at",
      label: "Up",
      align: "right",
      render: (v, row) =>
        row[4] === "running" && v ? (
          formatUptime((Date.now() - new Date(String(v)).getTime()) / 1000)
        ) : row[10] ? (
          <span className="dim" title={String(row[10])}>
            stopped {formatRelative(String(row[10]))}
          </span>
        ) : (
          dim
        ),
    },
    { key: "ports", render: (v) => (v ? <span className="mono ctr-ports">{String(v)}</span> : dim) },
    {
      key: "events",
      label: "Events",
      align: "right",
      render: (v, row) => {
        const n = toNumber(v) ?? 0;
        const w = toNumber(row[20]) ?? 0;
        return n === 0 ? <span className="dim">0</span> : <span className={w > 0 ? "host-warn" : ""}>{formatCount(n)}</span>;
      },
    },
  ];

  return (
    <>
      <TopBar
        title="Containers"
        section="Monitor"
        actions={
          <>
            {hosts.length > 1 && (
              <Select label="Machine" icon={<Server size={14} />} value={host} onChange={setHost} options={[{ value: "", label: "All machines" }, ...hosts.map((h) => ({ value: h, label: h }))]} maxWidth={220} />
            )}
            <TimeRangePicker />
            <RefreshControl busy={list.loading} />
          </>
        }
      />
      <div className="content">
        {list.error && !list.data && <ErrorNotice title="Could not load containers" error={list.error} />}
        {list.data && list.data.rows.length === 0 && (
          <div className="grid">
            <InstallAgent title="No containers yet">
              The host agent reports every Docker or Podman container on the machine it runs on: state, health, restarts, CPU and memory
              against each container's limits, network and disk, plus OOM kills, crashes and failed health checks. It finds the engine's
              socket on its own (<code className="mono">/var/run/docker.sock</code> or Podman's), or set{" "}
              <code className="mono">VISIONARY_AGENT_DOCKER</code>.
            </InstallAgent>
          </div>
        )}
        {list.data && list.data.rows.length > 0 && shown && (
          <div className="grid">
            <div className="span-3 host-tile">
              <StatTile label="Running" value={`${running} / ${rows.length}`} delta={null} deltaLabel="" upIs="neutral" loading={list.loading} footer={`${rows.length - running} not running`} />
            </div>
            <div className="span-3 host-tile">
              <StatTile
                label="Unhealthy"
                description="Containers whose health check is failing now."
                value={formatCount(unhealthy)}
                delta={null}
                deltaLabel=""
                upIs="bad"
                loading={list.loading}
                footer={nearLimit ? `${nearLimit} near the memory limit` : "None near the memory limit"}
              />
            </div>
            <div className="span-3 host-tile">
              <StatTile label="Restarts" description="Restarts by the restart policy in this range: a container that keeps crashing restarts again and again." value={formatCount(sumKind("restarted"))} delta={null} deltaLabel="" upIs="bad" loading={events.loading} footer={`${formatCount(sumKind("exited"))} exits`} />
            </div>
            <div className="span-3 host-tile">
              <StatTile label="Out-of-memory kills" description="Containers the kernel killed for going over their memory limit, in this range." value={formatCount(sumKind("oom_killed"))} delta={null} deltaLabel="" upIs="bad" loading={events.loading} footer={`${formatCount(sumKind("unhealthy"))} health check failures`} />
            </div>

            <section className="panel span-12" aria-busy={list.loading || undefined}>
              {list.loading && <div className="progress" aria-hidden />}
              <header className="panel-head">
                <h3 className="panel-title">Containers</h3>
                <div className="panel-actions">
                  <label className="host-search">
                    <Search size={14} aria-hidden />
                    <input className="input" type="search" placeholder="Search containers" aria-label="Search containers" value={search} onChange={(e) => setSearch(e.target.value)} />
                  </label>
                </div>
              </header>
              <DataTable result={shown} columns={columns} rowKey={(r) => `${r[0]}/${r[1]}`} onRowClick={(r) => (window.location.hash = containerHref(String(r[0]), String(r[1])))} />
            </section>

            <Section title="Usage" text="Each running container, from the agent's samples. CPU is a percent of the container's CPU limit, or of all the machine's CPUs when it has none." />
            <ContainerChart path="cpu" params={filter} title="CPU" description="Average CPU per container." viz={{ type: "timeseries", x: "t", y: ["value"], series: "series", unit: "percent" }} span="span-6" />
            <ContainerChart path="memory" params={filter} title="Memory" description="Memory in use per container, without page cache." viz={{ type: "timeseries", x: "t", y: ["value"], series: "series", unit: "bytes", stack: true }} span="span-6" />
            <ContainerChart path="network" params={filter} title="Network" description="Bytes received plus sent per second, per container. Empty for containers on the host network." viz={{ type: "timeseries", x: "t", y: ["value"], series: "series", unit: "bytes_per_s" }} span="span-6" />
            <ContainerChart path="disk" params={filter} title="Disk" description="Bytes read plus written per second, per container." viz={{ type: "timeseries", x: "t", y: ["value"], series: "series", unit: "bytes_per_s" }} span="span-6" />

            <Section title="Events" text="Restarts, out-of-memory kills, exits and health check changes the agent saw between its reports." />
            <Panel
              className="span-12"
              title="Container events"
              result={events.data}
              loading={events.loading}
              error={events.error}
              table={events.data && (events.data.rows.length === 0 ? <div className="panel-state">No container events in this range.</div> : <DataTable result={events.data} columns={eventColumns(true)} maxHeight={420} />)}
            />
          </div>
        )}
      </div>
    </>
  );
}

function ContainerChart({ path, params, title, description, viz, span, empty }: { path: string; params: Record<string, string>; title: string; description: string; viz: Viz; span: string; empty?: string }) {
  const tokens = useChartTokens();
  const q = useContainerData(`panels/${path}`, params);
  return (
    <Panel
      className={span}
      title={title}
      description={description}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={220}
      note={vizNote(q.data, viz, tokens)}
      chart={q.data && <VizView result={q.data} viz={viz} height={220} title={title} empty={empty} />}
      table={q.data && <DataTable result={q.data} maxHeight={220} defaultUnit={viz.unit} />}
    />
  );
}

/** The facts of the container's head card. */
function containerFacts(c: Record<string, unknown>, cpuLimit: number | null, memLimit: number | null): Fact[] {
  const running = c.state === "running";
  const started = c.started_at ? new Date(String(c.started_at)) : null;
  const finished = c.finished_at ? new Date(String(c.finished_at)) : null;
  const restarts = toNumber(c.restarts) ?? 0;
  const exit = c.exit_code == null ? null : Number(c.exit_code);
  const oom = !!toNumber(c.oom_killed);
  const at = running ? started : finished;
  return [
    {
      label: running ? "Up for" : "Stopped",
      value: at ? (running ? formatUptime((Date.now() - at.getTime()) / 1000) : formatRelative(at.toISOString())) : "—",
      sub: at ? `${running ? "since" : "at"} ${formatDateTime(at)}` : undefined,
      title: at ? formatFull(at) : undefined,
    },
    { label: "Restarts", value: formatCount(restarts), tone: restarts > 0 ? "warn" : undefined },
    {
      label: "Last exit",
      value: exit === null ? "—" : oom ? "Out of memory" : `Code ${exit}`,
      sub: exit !== null && oom ? `code ${exit}` : undefined,
      tone: oom ? "bad" : exit !== null && exit !== 0 && exit !== 143 ? "warn" : undefined,
    },
    { label: "CPU limit", value: cpuLimit ? `${cpuLimit} ${cpuLimit === 1 ? "core" : "cores"}` : "None" },
    { label: "Memory limit", value: memLimit ? formatBytes(memLimit * 1048576) : "None" },
    { label: "Processes", value: toNumber(c.pids) === null ? "—" : formatCount(toNumber(c.pids)!) },
    { label: "Ports", value: c.ports ? String(c.ports) : "None", mono: !!c.ports, title: c.ports ? String(c.ports) : undefined },
  ];
}

function ContainerDetail({ host, name }: { host: string; name: string }) {
  const list = useContainers();
  const c = records(list.data).find((r) => r.host === host && r.name === name);
  const params = { host, container: name };
  const events = useContainerData("events", params);
  const cpuLimit = toNumber(c?.cpu_limit_cores);
  const memLimit = toNumber(c?.mem_limit_mb);

  return (
    <>
      <TopBar
        title={name}
        section="Containers"
        actions={
          <>
            <a className="btn ghost" href={`#/system?instance=${encodeURIComponent(host)}`}>
              <ExternalLink size={14} />
              Machine in System
            </a>
            <TimeRangePicker />
            <RefreshControl busy={list.loading} />
          </>
        }
      />
      <div className="content">
        <a className="host-back" href="#/containers">
          <ArrowLeft size={14} />
          All containers
        </a>
        {list.data && !c && <div className="notice">This container is not in the latest report of {host}. Its history is below.</div>}
        <div className="grid">
          {c && (
            <HeadCard
              icon={<Box size={20} />}
              title={name}
              subtitle={
                <>
                  <span className="mono">{String(c.image ?? "")}</span>
                  {c.project ? ` · Compose project ${String(c.project)}` : ""} · on {host}
                </>
              }
              badge={<StateBadge state={c.state} health={c.health} />}
              meta={`Reported ${formatRelative(String(c.last_seen))}`}
              facts={containerFacts(c, cpuLimit, memLimit)}
            />
          )}

          <Section title="CPU and memory" text={`CPU is a percent of the container's limit${cpuLimit ? ` (${cpuLimit} cores)` : ", or of all the machine's CPUs since it has none"}. Memory leaves out page cache, as docker stats does.`} />
          <ContainerChart path="cpu" params={params} title="CPU" description="Average and peak CPU per interval." viz={{ type: "timeseries", x: "t", y: ["avg_cpu", "peak_cpu"], unit: "percent" }} span="span-6" />
          <ContainerChart path="throttled" params={params} title="CPU throttling" description="Share of scheduling periods in which the container hit its CPU limit and had to wait." viz={{ type: "timeseries", x: "t", y: ["throttled"], unit: "percent" }} span="span-6" empty="No CPU limit, so nothing is throttled." />
          <ContainerChart path="memory" params={params} title="Memory" description="Memory in use against the limit (the machine's memory when there is none)." viz={{ type: "timeseries", x: "t", y: ["used", "limit_bytes"], unit: "bytes" }} span="span-6" />
          <ContainerChart path="memory_percent" params={params} title="Memory of limit" description="At 100% the kernel kills the container's process (out of memory)." viz={{ type: "timeseries", x: "t", y: ["avg_used", "peak_used"], unit: "percent" }} span="span-6" />

          <Section title="I/O" text="Rates between two samples. Containers on the host network report no network of their own." />
          <ContainerChart path="network" params={params} title="Network" description="Bytes received and sent per second." viz={{ type: "timeseries", x: "t", y: ["received", "sent"], unit: "bytes_per_s" }} span="span-4" />
          <ContainerChart path="disk" params={params} title="Disk" description="Bytes read and written per second." viz={{ type: "timeseries", x: "t", y: ["read_bytes", "written_bytes"], unit: "bytes_per_s" }} span="span-4" />
          <ContainerChart path="pids" params={params} title="Processes" description="Processes and threads in the container." viz={{ type: "timeseries", x: "t", y: ["pids"], unit: "count" }} span="span-4" />

          <Section title="Events" />
          <Panel
            className="span-12"
            title="Events"
            description="Restarts, out-of-memory kills, exits and health check changes, newest first."
            result={events.data}
            loading={events.loading}
            error={events.error}
            table={events.data && (events.data.rows.length === 0 ? <div className="panel-state">No events for this container in this range.</div> : <DataTable result={events.data} columns={eventColumns(false)} maxHeight={420} />)}
          />
        </div>
      </div>
    </>
  );
}
