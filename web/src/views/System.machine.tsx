import { api, ApiError, type Result } from "../lib/api";
import { formatBytes, formatCount, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { formatLogTime, formatRelative } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { DataTable } from "../components/DataTable";
import { dim, formatUptime, InstallAgent, Meter, pct, StatusBadge, type HostStatus } from "../components/Machine";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";

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
          <InstallAgent title="Watch the machine with the host agent">
            The agent reports the whole machine every minute: every drive, the top processes, failed systemd services, Docker or Podman
            containers, and SSH logins, sudo, open ports and pending updates. It never needs database credentials.
          </InstallAgent>
        </details>
      );
    }
    return <MachineList machines={machines} onPick={onPick} />;
  }
  if (m.error instanceof ApiError && m.error.status === 404) return null; // an SDK instance without the agent
  const h = m.data?.host;
  if (!h) return null;
  return (
    <section className="panel span-12 host-head">
      <div className="host-head-main">
        <StatusBadge status={h.status} large />
        <div className="host-head-text">
          <div className="host-head-os">
            {h.host} · {h.os ?? "Unknown OS"}
            {h.kernel && <span className="dim"> · kernel {h.kernel}</span>}
          </div>
          <div className="dim">
            Host agent: last report {formatRelative(h.last_seen)} · every {formatCount(h.report_s)}s{h.service && ` · service ${h.service}`}
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
  );
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
                { key: "state", label: "State", render: (v) => <span className={`host-unit ${String(v)}`}>{String(v)}</span> },
                { key: "sub", label: "Sub" },
              ]}
            />
          )
        }
      />
    </>
  );
}
