import { useMemo } from "react";
import { api, ApiError, rangeQuery, type Result, type Viz } from "../lib/api";
import { fieldTerm, logsHref } from "../lib/applogs";
import { formatBytes, formatCount, titleCase, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { formatDateTime, formatFull, formatRelative } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { DataTable } from "../components/DataTable";
import { HardDrive, Server } from "lucide-react";
import { dim, formatUptime, HeadCard, Meter, pct, StatusBadge, type HostStatus } from "../components/Machine";
import { Panel } from "../components/Panel";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { Section } from "../components/Section";
import { AgentSteps, SetupCard, SetupOption } from "../components/SetupCard";
import { MachineStorage } from "./System.storage";

/**
 * What the host agent adds to System: the machine's details, every mount,
 * the top processes and systemd units. Charts stay System's own panels,
 * since the agent writes the same system_metrics columns.
 */

interface MachineInfo {
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

interface MachineData {
  host: MachineInfo;
  processes: Result;
  mounts: Result;
  units: Result;
  gpus?: Result;
  gpu_processes?: Result;
  sensors?: Result;
  storage?: Result;
}

/** The agent's machines; an error (an older server) reads as none. */
export function useMachines() {
  const { tick } = useApp();
  return useQuery<Result>(`machines#${tick}`, (signal) => api<Result>("/api/system/machines", { signal }));
}

/** The machine System shows: the chosen instance, or the only machine. */
export function machineFor(instance: string, machines: Result | undefined): string {
  if (instance) return instance;
  return machines?.rows.length === 1 ? String(machines.rows[0][0]) : "";
}

export function useMachine(host: string) {
  const { tick } = useApp();
  return useQuery<MachineData>(host ? `machine-${host}#${tick}` : null, (signal) =>
    api<MachineData>(`/api/system/machines/${encodeURIComponent(host)}`, { signal }),
  );
}

/** The machine's details, or a list to pick from, or how to add the agent. */
export function MachineHead({ host, machines, onPick }: { host: string; machines: Result | undefined; onPick: (host: string) => void }) {
  const m = useMachine(host);
  if (!machines) return null;
  if (!host) {
    if (machines.rows.length === 0) {
      return (
        <details className="span-12 machine-hint">
          <summary>Add the host agent to see this machine's drives, processes, services and containers, and its SSH and sudo activity in Security.</summary>
          <SetupCard icon={<HardDrive size={20} />} title="Watch the whole machine" text="Every drive, the top processes, failed systemd services, containers, and SSH logins, sudo, open ports and pending updates, every minute." topic="agent" className="sc-inline">
            <SetupOption title="Install the host agent" text="A small service on each Linux machine. It posts here with a server key and never needs database credentials.">
              <AgentSteps />
            </SetupOption>
          </SetupCard>
        </details>
      );
    }
    return <MachineList machines={machines} onPick={onPick} />;
  }
  if (m.error instanceof ApiError && m.error.status === 404) return null; // an SDK instance without the agent
  const h = m.data?.host;
  if (!h) return null;
  const booted = h.boot_time ? new Date(h.boot_time) : null;
  return (
    <HeadCard
      icon={<Server size={20} />}
      title={h.host}
      subtitle={[osName(h.os), h.arch, h.service && `service ${h.service}`].filter(Boolean).join(" · ")}
      badge={<StatusBadge status={h.status} />}
      meta={
        <span title={`Watched since ${formatFull(new Date(h.first_seen))}`}>
          Reported {formatRelative(h.last_seen)} · every {formatCount(h.report_s)}s ·{" "}
          <a href={logsHref(fieldTerm("host", h.host))}>Logs</a>
        </span>
      }
      facts={[
        { label: "Address", value: h.ip ?? "—", mono: true },
        { label: "CPUs", value: h.cpu_count ?? "—" },
        { label: "Memory", value: h.mem_total_mb ? formatBytes(h.mem_total_mb * 1048576) : "—" },
        {
          label: "Up for",
          value: booted ? formatUptime((Date.now() - booted.getTime()) / 1000) : "—",
          sub: booted ? `since ${formatDateTime(booted)}` : undefined,
          title: booted ? formatFull(booted) : undefined,
        },
        { label: "Kernel", value: kernelName(h.kernel), mono: true, title: h.kernel ?? undefined },
        { label: "Agent", value: h.agent_version ?? "—", mono: true },
      ]}
    />
  );
}

/** "Microsoft Windows 11 Pro" reads as "Windows 11 Pro". */
function osName(os: string | null): string {
  return (os ?? "Unknown OS").replace(/^Microsoft\s+/, "");
}

/** Windows repeats its build: "10.0.26300.9711 Build 26300.9711". */
function kernelName(k: string | null): string {
  if (!k) return "—";
  const m = k.match(/^(.*?)\s+Build\s+(\S+)$/);
  return m && m[1].includes(m[2]) ? m[1] : k;
}

/** Every machine with an agent, when the project has several. */
function MachineList({ machines, onPick }: { machines: Result; onPick: (host: string) => void }) {
  const col = (name: string) => machines.columns.indexOf(name);
  return (
    <Panel
      className="span-12"
      title="Machines"
      description="Machines the host agent watches. Pick one to see its drives, processes and services; the charts below cover every instance."
      result={machines}
      table={
        <DataTable
          result={machines}
          maxHeight={300}
          rowKey={(r) => r[0]}
          onRowClick={(r) => onPick(String(r[0]))}
          columns={[
            {
              key: "host",
              label: "Machine",
              render: (v, row) => (
                <span className="host-name">
                  <span>{String(v)}</span>
                  {row[col("ip")] ? <span className="mono dim">{String(row[col("ip")])}</span> : null}
                </span>
              ),
            },
            { key: "os", label: "OS" },
            { key: "status", label: "Status", render: (v) => <StatusBadge status={v as HostStatus} /> },
            { key: "cpu_percent", label: "CPU", align: "right", render: (v) => <Meter value={v} /> },
            { key: "mem_percent", label: "Memory", align: "right", render: (v) => <Meter value={v} /> },
            { key: "disk_percent", label: "Fullest disk", align: "right", render: (v) => <Meter value={v} /> },
            { key: "uptime_s", label: "Uptime", align: "right", render: (v) => (toNumber(v) === null ? dim : formatUptime(toNumber(v)!)) },
            {
              key: "failed_units",
              label: "Failed units",
              align: "right",
              render: (v) => (toNumber(v) === null ? dim : <span className={toNumber(v)! > 0 ? "host-bad" : ""}>{formatCount(toNumber(v)!)}</span>),
            },
            { key: "containers", label: "Containers", align: "right", render: (v) => (toNumber(v) === null ? dim : formatCount(toNumber(v)!)) },
            { key: "last_seen", label: "Last report", render: (v) => <span title={String(v)}>{formatRelative(String(v))}</span> },
          ]}
        />
      }
    />
  );
}

/** Mounts, top processes and systemd units from the latest report. */
export function MachineSnapshots({ host }: { host: string }) {
  const q = useMachine(host);
  const d = q.data;
  if (!host || !d) return null;
  return (
    <>
      <Section title="Drives" text="Every real mount in the latest agent report. Inodes run out separately from space: many small files can fill a disk that still shows free space." />
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

      <MachineStorage storage={d.storage} mounts={d.mounts} />

      {d.sensors && d.sensors.rows.length > 0 && <Section title="Sensors" text="Every temperature and fan the kernel exposes, from the latest agent report. The CPU temperature chart above uses the hottest CPU package or core sensor." />}
      {d.sensors && d.sensors.rows.length > 0 && (
        <Panel
          className="span-12"
          title="Temperatures and fans"
          result={d.sensors}
          loading={q.loading}
          table={
            <DataTable
              result={d.sensors}
              maxHeight={300}
              columns={[
                { key: "name", label: "Sensor" },
                { key: "kind", label: "Kind", render: (v) => titleCase(String(v ?? "")) },
                { key: "value", label: "Reading", align: "right", render: (v, row) => (cell(d.sensors!, row, "unit") === "C" ? unit("°C", 1)(v) : unit("rpm")(v)) },
              ]}
            />
          }
        />
      )}

      <Section title="Processes and services" text="The top processes by CPU and by memory on the whole machine, and the failed or watched systemd services (VISIONARY_AGENT_UNITS), from the latest agent report." />
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
        description="Failed services, plus the services listed in VISIONARY_AGENT_UNITS. Empty when the machine has no systemd."
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
                { key: "state", label: "State", render: (v) => <span className={`host-unit ${String(v)}`}>{titleCase(String(v))}</span> },
                { key: "sub", label: "Sub", render: (v) => (v ? titleCase(String(v)) : dim) },
              ]}
            />
          )
        }
      />
    </>
  );
}

/** The value of column name in row, by the result's columns */
function cell(r: Result, row: unknown[], name: string): unknown {
  const i = r.columns.indexOf(name);
  return i >= 0 ? row[i] : null;
}

const mb = (v: unknown) => (toNumber(v) === null ? dim : formatBytes(toNumber(v)! * 1048576));
const unit = (suffix: string, digits = 0) => (v: unknown) => (toNumber(v) === null ? dim : `${toNumber(v)!.toFixed(digits)} ${suffix}`);

/** One card per GPU from the agent: use, temperature and power over time, and the processes holding its memory */
export function MachineGPUs({ host }: { host: string }) {
  const m = useMachine(host);
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to);
  const series = useQuery<Result>(host ? `machine-gpus-${host}?${qs}#${tick}` : null, (signal) =>
    api<Result>(`/api/system/machines/${encodeURIComponent(host)}/gpus?${qs}`, { signal }),
  );
  const gpus = m.data?.gpus;
  if (!host || !gpus || gpus.rows.length === 0) return null;
  const procs = m.data?.gpu_processes;
  return (
    <>
      <Section title="GPUs" text="Each GPU in the latest agent report. Busy is the share of time the GPU ran work; memory is the share of its memory in use, which is what runs out first for model serving and training. Above about 85 °C most GPUs slow themselves down." />
      {gpus.rows.map((row) => {
        const index = toNumber(cell(gpus, row, "gpu_index")) ?? 0;
        const name = String(cell(gpus, row, "name") ?? "GPU");
        return <GPUCard key={index} index={index} name={name} snapshot={gpus} row={row} series={series.data} loading={series.loading} />;
      })}
      {procs && procs.rows.length > 0 && (
        <Panel
          className="span-12"
          title="Processes using GPU memory"
          description="From nvidia-smi at the latest report. AMD GPUs do not list processes."
          result={procs}
          table={
            <DataTable
              result={procs}
              maxHeight={300}
              columns={[
                { key: "gpu_index", label: "GPU", align: "right" },
                { key: "pid", label: "PID", align: "right" },
                { key: "name", label: "Process", render: (v) => <span className="mono host-cmd">{String(v ?? "")}</span> },
                { key: "used_mem_mb", label: "GPU memory", align: "right", render: mb },
              ]}
            />
          }
        />
      )}
    </>
  );
}

function GPUCard({ index, name, snapshot, row, series, loading }: { index: number; name: string; snapshot: Result; row: unknown[]; series?: Result; loading: boolean }) {
  const mine = useMemo(() => {
    if (!series) return undefined;
    const gi = series.columns.indexOf("gpu_index");
    return { ...series, rows: series.rows.filter((r) => toNumber(r[gi]) === index) };
  }, [series, index]);
  const used = toNumber(cell(snapshot, row, "mem_used_mb"));
  const total = toNumber(cell(snapshot, row, "mem_total_mb"));
  const power = toNumber(cell(snapshot, row, "power_w"));
  const limit = toNumber(cell(snapshot, row, "power_limit_w"));
  const facts = [
    String(cell(snapshot, row, "vendor") ?? "").toUpperCase(),
    used !== null && total ? `${formatBytes(used * 1048576)} of ${formatBytes(total * 1048576)}` : "",
    power !== null ? `${power.toFixed(0)} W${limit ? ` of ${limit.toFixed(0)} W` : ""}` : "",
    toNumber(cell(snapshot, row, "clock_mhz")) !== null ? `${toNumber(cell(snapshot, row, "clock_mhz"))} MHz` : "",
    toNumber(cell(snapshot, row, "fan_percent")) !== null ? `fan ${toNumber(cell(snapshot, row, "fan_percent"))!.toFixed(0)}%` : "",
  ].filter(Boolean);
  const title = `GPU ${index} · ${name}`;
  return (
    <>
      <GPUChart className="span-6" title={`${title}: use (%)`} description={facts.join(" · ")} result={mine} loading={loading} viz={{ type: "timeseries", x: "t", y: ["busy", "memory_used", "memory_busy"], unit: "percent" }} />
      <GPUChart className="span-3" title="Temperature (°C)" description={`${title}, hottest reading per interval.`} result={mine} loading={loading} viz={{ type: "timeseries", x: "t", y: ["temperature"], unit: "none" }} />
      <GPUChart className="span-3" title="Power (W)" description={`${title}, average draw against its power limit.`} result={mine} loading={loading} viz={{ type: "timeseries", x: "t", y: ["power", "power_limit"], unit: "none" }} />
    </>
  );
}

function GPUChart({ className, title, description, result, loading, viz }: { className: string; title: string; description: string; result?: Result; loading: boolean; viz: Viz }) {
  const tokens = useChartTokens();
  const height = className === "span-3" ? 180 : 220;
  return (
    <Panel
      className={className}
      title={title}
      description={description}
      result={result}
      loading={loading}
      bodyHeight={height}
      note={vizNote(result, viz, tokens)}
      chart={result && <VizView result={result} viz={viz} height={height} title={title} empty="This GPU reported no readings in this time range." />}
      table={result && <DataTable result={result} maxHeight={height} defaultUnit={viz.unit} />}
    />
  );
}
