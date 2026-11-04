import { Fragment, useMemo, useState } from "react";
import { useConnected } from "../lib/meta";
import { Server, Sparkles } from "lucide-react";
import { Select } from "../components/Select";
import { api, ApiError, rangeQuery, type Result, type Viz } from "../lib/api";
import { formatBytes, formatCount, formatMs, formatPercent, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { dateFormat, formatLogTime } from "../lib/time";
import { langOf, relabel, wordsFor, type RuntimeWords } from "../lib/runtime";
import { sparkPoints } from "../charts/option";
import { DataTable, type Column } from "../components/DataTable";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { MachineGPUs, MachineHead, MachineSnapshots, machineFor, useMachine, useMachines } from "./System.machine";
import { AgentSteps, SetupCard, SetupHelpButton, SetupOption } from "../components/SetupCard";

export interface PanelDef {
  name: string;
  title: string;
  description: string;
  viz: Viz;
  span: string;
  /** Says why a panel can be empty, e.g. a Linux-only source. */
  empty?: string;
  /** Description and series labels in the service language's words. */
  runtime?: (w: RuntimeWords) => { description: string; labels: Record<string, string> };
}

const LINUX_ONLY = "Not reported by this host. This metric is collected on Linux.";
const PSI_ONLY = "Linux only, from kernel 4.20 with pressure stall information on. Containers report their own cgroup's pressure on cgroup v2.";
const SENSORS_ONLY = "Linux only, and only where the kernel exposes CPU sensors. Most virtual machines have none.";

export const SECTIONS: { title: string; text: string; panels: PanelDef[] }[] = [
  {
    title: "CPU",
    text: "How busy the machine is and where the time goes. iowait means CPUs idle while waiting on disk; steal means the hypervisor gave the time to another VM.",
    panels: [
      { name: "cpu", title: "Host CPU", description: "Whole-machine CPU use: average and peak per interval.", viz: { type: "timeseries", x: "t", y: ["avg_cpu", "max_cpu"], unit: "percent" }, span: "span-6" },
      {
        name: "cpu_breakdown",
        title: "CPU time by mode",
        description: "Share of all CPU time spent in user code, the kernel, waiting on disk (iowait) and stolen by the hypervisor (steal).",
        viz: { type: "timeseries", x: "t", y: ["user_time", "system_time", "iowait", "steal"], unit: "percent", stack: true },
        span: "span-6",
      },
      { name: "process_cpu", title: "Process CPU", description: "This process alone, as a percent of one core (can exceed 100% on multi-core hosts).", viz: { type: "timeseries", x: "t", y: ["avg_process_cpu", "max_process_cpu"], unit: "percent" }, span: "span-6" },
      {
        name: "load_per_core",
        title: "Load per core",
        description: "1-minute load average divided by the number of CPUs. Above 1 means work is queuing for CPU.",
        viz: { type: "timeseries", x: "t", y: ["load_per_core", "peak_load_per_core"], unit: "none" },
        span: "span-6",
        empty: "Load average is not reported on Windows hosts.",
      },
    ],
  },
  {
    title: "Memory",
    text: "Available memory is what new work can use without swapping; cached memory is page cache the kernel gives back under pressure.",
    panels: [
      { name: "memory_detail", title: "Host memory", description: "Memory in use, available to new work, and held as page cache.", viz: { type: "timeseries", x: "t", y: ["used", "available", "cached"], unit: "bytes" }, span: "span-6" },
      {
        name: "process_memory",
        title: "Process memory",
        description: "Resident memory of this process and its runtime heap in use.",
        viz: { type: "timeseries", x: "t", y: ["rss_bytes", "heap_bytes"], unit: "bytes" },
        span: "span-6",
        runtime: (w) => ({ description: w.heapHelp, labels: { rss_bytes: "Resident memory", heap_bytes: w.heap } }),
      },
      { name: "swap", title: "Swap", description: "Swap in use against the total. Steady swap use under load slows everything down.", viz: { type: "timeseries", x: "t", y: ["swap_used", "swap_total"], unit: "bytes" }, span: "span-6", empty: "No swap reported by this host." },
      { name: "memory", title: "Host memory used", description: "Percent of host memory in use.", viz: { type: "timeseries", x: "t", y: ["host_memory"], unit: "percent" }, span: "span-6" },
    ],
  },
  {
    title: "Pressure",
    text: "The share of time tasks were stalled waiting for CPU, memory or disk, over the last 10 seconds. Some means at least one task waited; full means every task did. Pressure shows contention before utilization reaches 100%: sustained memory or I/O pressure above a few percent is worth a look, full pressure above zero is serious.",
    panels: [
      {
        name: "pressure",
        title: "Pressure stall (%)",
        description: "Average of the 10-second pressure per interval. CPU some above 50%, or memory and I/O some above 10%, means work is queuing for that resource.",
        viz: { type: "timeseries", x: "t", y: ["cpu_some", "memory_some", "memory_full", "io_some", "io_full"], unit: "percent" },
        span: "span-12",
        empty: PSI_ONLY,
      },
    ],
  },
  {
    title: "Virtual memory",
    text: "How the kernel juggles memory. Major faults and swap-ins read memory back from disk and stall the process doing it; scanning and reclaiming mean the page cache is being squeezed. Committed above the commit limit is normal with overcommit, but a steady climb means something keeps allocating.",
    panels: [
      { name: "page_faults", title: "Page faults (per second)", description: "Minor faults are served from memory; major faults had to read from disk. A steady stream of major faults means the working set does not fit.", viz: { type: "timeseries", x: "t", y: ["minor", "major"], unit: "count" }, span: "span-6", empty: LINUX_ONLY },
      { name: "swap_activity", title: "Swap in and out (pages per second)", description: "Pages read back from swap and written out to it. Swap in during load is the one that hurts.", viz: { type: "timeseries", x: "t", y: ["swapped_in", "swapped_out"], unit: "count" }, span: "span-6", empty: LINUX_ONLY },
      { name: "reclaim", title: "Reclaim (pages per second)", description: "Pages the kernel scanned looking for memory to free, the pages it freed, and processes it killed out of memory per interval. Scanning far above reclaiming means it is struggling.", viz: { type: "timeseries", x: "t", y: ["scanned", "reclaimed", "processes_killed"], unit: "count" }, span: "span-4", empty: LINUX_ONLY },
      { name: "dirty_memory", title: "Dirty, writeback, slab and shared", description: "Dirty pages wait to be written to disk, writeback pages are being written now. Slab is kernel caches, shared is tmpfs and shared memory.", viz: { type: "timeseries", x: "t", y: ["dirty", "writeback", "slab", "shared"], unit: "bytes" }, span: "span-4", empty: LINUX_ONLY },
      { name: "commit", title: "Committed memory", description: "Memory promised to processes against the commit limit (swap plus a share of RAM). Past the limit, allocations fail when overcommit is strict.", viz: { type: "timeseries", x: "t", y: ["committed", "commit_limit"], unit: "bytes" }, span: "span-4", empty: LINUX_ONLY },
    ],
  },
  {
    title: "Disk",
    text: "Throughput and IOPS show how much the disks do; busy time and await show whether they keep up. A disk near 100% busy with rising await is the bottleneck.",
    panels: [
      { name: "disk_space", title: "Disk space", description: "Space and inodes used on the filesystem the service runs from.", viz: { type: "timeseries", x: "t", y: ["space_used", "inodes_used"], unit: "percent" }, span: "span-4" },
      { name: "disk_throughput", title: "Disk read/write speed", description: "How fast the host reads from and writes to its disks, summed over whole disks.", viz: { type: "timeseries", x: "t", y: ["read_speed", "write_speed"], unit: "bytes_per_s" }, span: "span-4" },
      { name: "disk_iops", title: "Disk operations", description: "Read and write operations per second.", viz: { type: "timeseries", x: "t", y: ["read_ops", "write_ops"], unit: "count" }, span: "span-4" },
      { name: "disk_busy", title: "Disk busy", description: "Share of each interval the busiest disk spent doing I/O.", viz: { type: "timeseries", x: "t", y: ["avg_busy", "peak_busy"], unit: "percent" }, span: "span-6", empty: LINUX_ONLY },
      { name: "disk_latency", title: "Disk latency", description: "Average time per disk operation (await), including time queued.", viz: { type: "timeseries", x: "t", y: ["avg_await", "peak_await"], unit: "ms" }, span: "span-6" },
    ],
  },
  {
    title: "Network",
    text: "Errors, drops and TCP retransmits rising together usually mean a saturated link or a struggling peer.",
    panels: [
      { name: "network", title: "Network throughput", description: "Bytes received and sent per second by the host.", viz: { type: "timeseries", x: "t", y: ["received_bytes", "sent_bytes"], unit: "bytes_per_s" }, span: "span-6" },
      { name: "network_packets", title: "Packets", description: "Packets received and sent per second.", viz: { type: "timeseries", x: "t", y: ["received", "sent"], unit: "count" }, span: "span-6" },
      { name: "network_errors", title: "Errors and drops", description: "Interface errors and dropped packets per interval, in and out.", viz: { type: "timeseries", x: "t", y: ["errors", "drops"], unit: "count" }, span: "span-4" },
      { name: "tcp", title: "TCP connections", description: "Connections in the ESTABLISHED state.", viz: { type: "timeseries", x: "t", y: ["established"], unit: "count" }, span: "span-4", empty: LINUX_ONLY },
      { name: "tcp_retrans", title: "TCP retransmits", description: "Segments sent again per interval. Spikes mean packet loss somewhere on the path.", viz: { type: "timeseries", x: "t", y: ["retransmits"], unit: "count" }, span: "span-4", empty: LINUX_ONLY },
    ],
  },
  {
    title: "System limits",
    text: "Kernel tables that fail quietly when full: file handles, tracked connections and sockets. A full conntrack table drops new connections, and many sockets stuck in CLOSE_WAIT mean an app is not closing them.",
    panels: [
      { name: "file_handles", title: "System file handles", description: "Open file handles on the whole machine against fs.file-max. Modern kernels have no limit, then only the open count shows.", viz: { type: "timeseries", x: "t", y: ["open_files", "limit_files"], unit: "count" }, span: "span-6", empty: LINUX_ONLY },
      { name: "conntrack", title: "Connection tracking", description: "Connections netfilter tracks against nf_conntrack_max. Only reported when the conntrack module is loaded.", viz: { type: "timeseries", x: "t", y: ["tracked", "table_limit"], unit: "count" }, span: "span-6", empty: "Linux only, and only with the nf_conntrack module loaded." },
      { name: "sockets", title: "Sockets", description: "Sockets in use, TCP connections in TIME_WAIT and CLOSE_WAIT, and orphaned TCP sockets. CLOSE_WAIT is not counted on hosts with more than 20,000 sockets.", viz: { type: "timeseries", x: "t", y: ["sockets", "time_wait", "close_wait", "orphans"], unit: "count" }, span: "span-6", empty: LINUX_ONLY },
      { name: "udp_errors", title: "UDP errors", description: "UDP datagrams that could not be delivered, and those dropped because a socket buffer was full, per interval. Buffer errors mean an app reads too slowly.", viz: { type: "timeseries", x: "t", y: ["undeliverable", "buffer_full"], unit: "count" }, span: "span-6", empty: LINUX_ONLY },
    ],
  },
  {
    title: "Thermals",
    text: "A hot CPU slows itself down to protect itself. Clock speed well below the maximum under load, or throttle events, mean cooling or power limits are costing performance.",
    panels: [
      { name: "cpu_temp", title: "CPU temperature (°C)", description: "The hottest CPU package or core sensor: average and peak per interval.", viz: { type: "timeseries", x: "t", y: ["avg_temp", "peak_temp"], unit: "none" }, span: "span-4", empty: SENSORS_ONLY },
      { name: "cpu_clock", title: "CPU clock (MHz)", description: "Average current clock across CPUs against the highest the CPU can run.", viz: { type: "timeseries", x: "t", y: ["current_clock", "max_clock"], unit: "none" }, span: "span-4", empty: LINUX_ONLY },
      { name: "cpu_throttling", title: "Thermal throttling", description: "Times the CPU cores slowed down for heat per interval.", viz: { type: "timeseries", x: "t", y: ["throttle_events"], unit: "count" }, span: "span-4", empty: "Linux only, on CPUs that report thermal throttling (Intel)." },
    ],
  },
  {
    title: "Process and runtime",
    text: "The service process itself: concurrency, garbage collection, file handles and its own I/O.",
    panels: [
      {
        name: "goroutines",
        title: "Concurrency",
        description: "Goroutines (Go) or live threads, and OS threads. A steady climb usually means a leak or stuck work.",
        viz: { type: "timeseries", x: "t", y: ["goroutines", "threads"], unit: "count" },
        span: "span-4",
        runtime: (w) => ({ description: w.concurrencyHelp, labels: { goroutines: w.concurrency, threads: "OS threads" } }),
      },
      { name: "gc", title: "GC pause", description: "Total garbage-collection pause per interval.", viz: { type: "timeseries", x: "t", y: ["gc_pause_ms"], unit: "ms" }, span: "span-4" },
      { name: "file_descriptors", title: "Open files", description: "Open files and sockets against the process limit. Hitting the limit makes new connections fail.", viz: { type: "timeseries", x: "t", y: ["open_files", "limit_files"], unit: "count" }, span: "span-4" },
      { name: "process_io", title: "Process read/write speed", description: "How fast this process reads and writes, to disk and other files.", viz: { type: "timeseries", x: "t", y: ["read_bytes", "write_bytes"], unit: "bytes_per_s" }, span: "span-6" },
      { name: "processes", title: "Host processes", description: "Processes running, and blocked waiting on I/O.", viz: { type: "timeseries", x: "t", y: ["running", "blocked"], unit: "count" }, span: "span-3", empty: LINUX_ONLY },
      { name: "context_switches", title: "Context switches", description: "Host context switches per second.", viz: { type: "timeseries", x: "t", y: ["per_second"], unit: "count" }, span: "span-3", empty: LINUX_ONLY },
    ],
  },
  {
    title: "Database pool",
    text: "In-use connections reaching the limit, or time spent waiting for one, means requests are queuing for the database.",
    panels: [
      { name: "db_pool", title: "Database connections", description: "Connections in use and open, against the pool limit.", viz: { type: "timeseries", x: "t", y: ["in_use", "open", "pool_limit"], unit: "count" }, span: "span-6", empty: "No pool stats: pass the pool to the SDK to record them." },
      { name: "db_wait", title: "Waiting for a connection", description: "Total time requests spent waiting for a database connection per interval.", viz: { type: "timeseries", x: "t", y: ["acquire_wait_ms"], unit: "ms" }, span: "span-6", empty: "No pool stats: pass the pool to the SDK to record them." },
    ],
  },
  {
    title: "Traffic",
    text: "What the service was asked to do while the numbers above moved.",
    panels: [
      { name: "throughput", title: "Throughput", description: "Requests per second (average and peak) and the most requests in flight at once.", viz: { type: "timeseries", x: "t", y: ["avg_rps", "peak_rps", "max_in_flight"], unit: "count" }, span: "span-12" },
    ],
  },
];

/** The service's own container (its cgroup), shown when the SDK reports one. */
const CONTAINER_SECTION: (typeof SECTIONS)[number] = {
  title: "Container",
  text: "The service's own container, measured by the SDK against the container's limits. In a container the host charts below describe the machine underneath, not what the service may use.",
  panels: [
    { name: "container_cpu", title: "Container CPU", description: "Cores the container used, its peak, and its CPU limit (no line: no limit).", viz: { type: "timeseries", x: "t", y: ["cores_used", "peak_cores", "cpu_limit"], unit: "none" }, span: "span-6" },
    { name: "container_memory", title: "Container memory", description: "Memory in use (without page cache, as docker stats counts it) against the container's limit. At the limit the kernel kills the process.", viz: { type: "timeseries", x: "t", y: ["used", "memory_limit"], unit: "bytes" }, span: "span-6" },
    { name: "container_throttling", title: "CPU throttling", description: "Share of scheduling periods in which the container hit its CPU limit and had to wait. Steady throttling slows requests down.", viz: { type: "timeseries", x: "t", y: ["throttled"], unit: "percent" }, span: "span-6", empty: "No CPU limit, so nothing is throttled." },
    { name: "container_faults", title: "Container page faults (per second)", description: "Page faults in the container's cgroup. Major faults read from disk, often because the container is near its memory limit.", viz: { type: "timeseries", x: "t", y: ["minor", "major"], unit: "count" }, span: "span-6" },
    { name: "container_memory_events", title: "Memory limit hits", description: "Times the container went over its memory.high throttle point and hit memory.max, per interval (cgroup v2).", viz: { type: "timeseries", x: "t", y: ["over_high", "hit_max"], unit: "count" }, span: "span-6", empty: "No memory limit hits reported (cgroup v2 only)." },
    { name: "container_oom", title: "Out-of-memory kills", description: "Processes the kernel killed in the container for exceeding its memory limit.", viz: { type: "bar", x: "t", y: ["oom_kills"], unit: "count" }, span: "span-6", empty: "No out-of-memory kills reported." },
  ],
};

/** GPU summary columns, for SDK instances without per-device rows from the agent */
const GPU_SECTION: (typeof SECTIONS)[number] = {
  title: "GPUs",
  text: "The busiest GPU's use and the fullest one's memory, from nvidia-smi or the amdgpu driver. Install the host agent on the machine for one card per GPU and the processes using them.",
  panels: [
    { name: "gpu", title: "GPU use (%)", description: "Busiest GPU's utilization and the highest share of GPU memory in use.", viz: { type: "timeseries", x: "t", y: ["gpu_busy", "gpu_memory"], unit: "percent" }, span: "span-8", empty: "No GPU found on this host." },
    { name: "gpu_temp", title: "GPU temperature (°C)", description: "Hottest GPU per interval.", viz: { type: "timeseries", x: "t", y: ["peak_temp"], unit: "none" }, span: "span-4", empty: "This GPU reports no temperature." },
  ],
};

function useSystem<T = Result>(name: string, instance: string) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, { instance });
  return useQuery<T>(`sys-${name}?${qs}#${tick}`, (signal) => api<T>(`/api/system/${name}?${qs}`, { signal }));
}

/** #/system?instance=web-1 opens the view on one instance (the Containers view links here). */
function readInstance(): string {
  return new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("instance") ?? "";
}

/** Who wrote an instance's rows, as the dropdown names it. */
function reporterName(sdk: unknown, runtime: unknown): string {
  if (sdk === "agent") return "Host agent";
  if (sdk === "otlp") return "OpenTelemetry";
  const lang = langOf(sdk, runtime);
  return lang ? `${wordsFor(lang).name} SDK` : "";
}

interface Source {
  value: string;
  label: string;
  lang: ReturnType<typeof langOf>;
  members: string[];
}

/**
 * One entry per agent host, and one per SDK service: a service in a container
 * reports under its hostname, which changes on every deploy, so its instances
 * are picked together as "service:<name>".
 */
function systemSources(r: Result | undefined): Source[] {
  if (!r) return [];
  const col = (name: string) => r.columns.indexOf(name);
  const [si, ri, vi] = [col("sdk"), col("runtime"), col("service")];
  const out = new Map<string, Source>();
  for (const row of r.rows) {
    const inst = String(row[0]);
    const sdk = si >= 0 ? row[si] : null;
    const runtime = ri >= 0 && row[ri] ? String(row[ri]) : "";
    const service = vi >= 0 && row[vi] ? String(row[vi]) : "";
    const value = service && sdk !== "agent" ? `service:${service}` : inst;
    const have = out.get(value);
    if (have) {
      have.members.push(inst);
      continue;
    }
    const by = reporterName(sdk, runtime);
    out.set(value, {
      value,
      label: [value === inst ? inst : service, by, runtime].filter(Boolean).join(" · "),
      lang: langOf(sdk, runtime),
      members: [inst],
    });
  }
  return [...out.values()];
}

export function System() {
  const [picked, setInstance] = useState(readInstance);
  const instances = useSystem("instances", "");
  const sources = useMemo(() => systemSources(instances.data), [instances.data]);
  // A link to one SDK instance opens its whole service
  const instance = sources.find((x) => x.members.includes(picked))?.value ?? picked;
  const stats = useSystem("stats", instance);
  const cpu = useSystem("cpu", instance);
  const memory = useSystem("process_memory", instance);
  const disk = useSystem("disk_busy", instance);
  const diskIO = useSystem("disk_throughput", instance);
  const net = useSystem("network", instance);
  const machines = useMachines();
  const machine = machineFor(instance, machines.data);
  const machineData = useMachine(machine);
  const agentGPUs = (machineData.data?.gpus?.rows.length ?? 0) > 0;

  const connected = useConnected("system");
  const missing = !connected || (instances.error instanceof ApiError && instances.error.status === 404);
  // The language of the shown instances: one source's, or the one they all
  // share. Mixed or unknown falls back to neutral words.
  const words = useMemo(() => {
    const shown = sources.filter((x) => !instance || x.value === instance);
    const langs = new Set(shown.map((x) => x.lang));
    return wordsFor(langs.size === 1 ? [...langs][0] : null);
  }, [sources, instance]);
  const s = useMemo(() => {
    const r = stats.data;
    if (!r || r.rows.length === 0) return null;
    const o: Record<string, unknown> = {};
    r.columns.forEach((c, i) => (o[c] = r.rows[0][i]));
    return o;
  }, [stats.data]);
  const n = (k: string) => toNumber(s?.[k]);
  const dash = (v: number | null, f: (x: number) => string) => (v === null ? "—" : f(v));
  const cores = n("cpu_count");
  const loadNow = n("load_now");
  const perCore = loadNow !== null && cores ? loadNow / cores : null;
  const swapTotal = n("swap_total");
  const fdsLimit = n("fds_limit");
  const ctrMem = n("ctr_mem_now");
  const ctrMemLimit = n("ctr_mem_limit");
  const inContainer = ctrMem !== null || n("ctr_cpu_now") !== null;
  const speed = (v: number) => `${formatBytes(v)}/s`;
  const psiMem = n("psi_mem_now");
  const cpuTemp = n("cpu_temp_now");
  const gpuCount = n("gpu_count");

  return (
    <>
      <TopBar
        title="System"
        actions={
          <>
            {sources.length > 0 && (
              <Select
                label="Instance"
                icon={<Server size={14} />}
                value={sources.length > 1 ? instance : sources[0].value}
                onChange={setInstance}
                options={[...(sources.length > 1 ? [{ value: "", label: "All instances" }] : []), ...sources.map((x) => ({ value: x.value, label: x.label }))]}
                maxWidth={240}
              />
            )}
            <TimeRangePicker />
            <RefreshControl busy={stats.loading || cpu.loading} />
          </>
        }
      />
      <div className="content">
        {missing ? (
          <SetupCard icon={<Server size={20} />} title="No system metrics yet" text="CPU, memory, disks, network and the database pool come from either source, every 15 seconds." topic="sdk">
            <SetupOption title="From your services" text="The Visionary SDK samples the machine and process it runs in, along with its runtime and database pool.">
              <SetupHelpButton topic="sdk">Add the SDK</SetupHelpButton>
            </SetupOption>
            <SetupOption title="From any Linux machine" text="The host agent adds every drive, the top processes, systemd services and containers, with or without an app on it.">
              <AgentSteps />
            </SetupOption>
          </SetupCard>
        ) : (
          <div className="grid">
            <StatTile
              label="Host CPU"
              description="Latest sample, with the range average and peak below."
              value={dash(n("cpu_now"), formatPercent)}
              delta={null}
              deltaLabel=""
              upIs="bad"
              spark={sparkPoints(cpu.data, ["avg_cpu"])}
              loading={stats.loading}
              footer={n("cpu_max") !== null ? `avg ${formatPercent(n("cpu_avg") ?? 0)} · peak ${formatPercent(n("cpu_max")!)}` : undefined}
            />
            <StatTile
              label="Load per core"
              description="1-minute load average divided by the CPU count. Above 1.0 means work is waiting for CPU."
              value={dash(perCore, (v) => v.toFixed(2))}
              delta={null}
              deltaLabel=""
              upIs="bad"
              loading={stats.loading}
              footer={cores ? `${formatCount(cores)} CPUs · load ${loadNow?.toFixed(2) ?? "—"}` : "Not reported on this host"}
            />
            <StatTile
              label="Host memory"
              value={dash(n("mem_now"), formatPercent)}
              delta={null}
              deltaLabel=""
              upIs="bad"
              loading={stats.loading}
              footer={n("mem_max") !== null ? `peak ${formatPercent(n("mem_max")!)}` : undefined}
            />
            <StatTile
              label="Swap used"
              value={dash(n("swap_now"), (v) => formatBytes(v * 1048576))}
              delta={null}
              deltaLabel=""
              upIs="bad"
              loading={stats.loading}
              footer={swapTotal ? `of ${formatBytes(swapTotal * 1048576)}` : "No swap configured"}
            />
            <StatTile
              label="Disk space"
              value={dash(n("disk_now"), formatPercent)}
              delta={null}
              deltaLabel=""
              upIs="bad"
              loading={stats.loading}
              footer={n("disk_free_gb") !== null ? `${formatBytes(n("disk_free_gb")! * 1073741824)} free` : undefined}
            />
            <StatTile
              label="Disk busy"
              description="Share of time the busiest disk spent on I/O. Linux only."
              value={dash(n("disk_busy_avg"), formatPercent)}
              delta={null}
              deltaLabel=""
              upIs="bad"
              spark={sparkPoints(disk.data, ["avg_busy"])}
              loading={stats.loading}
              footer={n("disk_busy_max") !== null ? `peak ${formatPercent(n("disk_busy_max")!)}` : "Not reported on this host"}
            />
            <StatTile
              label="Disk read speed"
              description="Average read speed over the range, across whole disks."
              value={dash(n("disk_read_avg"), speed)}
              delta={null}
              deltaLabel=""
              upIs="neutral"
              spark={sparkPoints(diskIO.data, ["read_speed"])}
              loading={stats.loading}
              footer={n("disk_read_max") !== null ? `peak ${speed(n("disk_read_max")!)}` : "Not reported on this host"}
            />
            <StatTile
              label="Disk write speed"
              description="Average write speed over the range, across whole disks."
              value={dash(n("disk_write_avg"), speed)}
              delta={null}
              deltaLabel=""
              upIs="neutral"
              spark={sparkPoints(diskIO.data, ["write_speed"])}
              loading={stats.loading}
              footer={n("disk_write_max") !== null ? `peak ${speed(n("disk_write_max")!)}` : "Not reported on this host"}
            />
            <StatTile
              label="Network"
              description="Average bytes per second received plus sent."
              value={dash(n("net_bytes_avg"), speed)}
              delta={null}
              deltaLabel=""
              upIs="neutral"
              spark={sparkPoints(net.data, ["received_bytes", "sent_bytes"])}
              loading={stats.loading}
              footer={n("net_bytes_max") !== null ? `peak ${formatBytes(n("net_bytes_max")!)}/s` : undefined}
            />
            <StatTile
              label="Process memory"
              value={dash(n("rss_now"), (v) => formatBytes(v * 1048576))}
              delta={null}
              deltaLabel=""
              upIs="bad"
              spark={sparkPoints(memory.data, ["rss_bytes"])}
              loading={stats.loading}
              footer={[n("rss_max") !== null ? `peak ${formatBytes(n("rss_max")! * 1048576)}` : "", n("uptime_s") !== null ? `up ${formatUptime(n("uptime_s")!)}` : ""].filter(Boolean).join(" · ") || undefined}
            />
            <StatTile
              label="Open files"
              description="Open files and sockets of the process, against its limit."
              value={dash(n("fds_now"), (v) => formatCount(v))}
              delta={null}
              deltaLabel=""
              upIs="bad"
              loading={stats.loading}
              footer={fdsLimit ? `limit ${formatCount(fdsLimit)} · ${formatPercent((100 * (n("fds_now") ?? 0)) / fdsLimit)} used` : undefined}
            />
            <StatTile
              label="DB connections in use"
              value={n("db_now") === null ? "—" : `${formatCount(n("db_now")!)} / ${formatCount(n("db_limit") ?? 0)}`}
              delta={null}
              deltaLabel=""
              upIs="bad"
              loading={stats.loading}
              footer={n("db_max") !== null ? `peak ${formatCount(n("db_max")!)}` : "No pool stats recorded"}
            />

            {inContainer && (
              <StatTile
                label="Container memory"
                description="Memory the service's container uses, against its limit."
                value={dash(ctrMem, (v) => formatBytes(v * 1048576))}
                delta={null}
                deltaLabel=""
                upIs="bad"
                loading={stats.loading}
                footer={ctrMemLimit ? `limit ${formatBytes(ctrMemLimit * 1048576)} · ${formatPercent((100 * (ctrMem ?? 0)) / ctrMemLimit)} used` : "No memory limit"}
              />
            )}

            {psiMem !== null && (
              <StatTile
                label="Memory pressure"
                description="Share of the last 10 seconds some tasks stalled waiting for memory. Above a few percent the machine is short of memory."
                value={formatPercent(psiMem)}
                delta={null}
                deltaLabel=""
                upIs="bad"
                loading={stats.loading}
                footer={n("psi_io_now") !== null ? `I/O pressure ${formatPercent(n("psi_io_now")!)}` : undefined}
              />
            )}
            {cpuTemp !== null && (
              <StatTile
                label="CPU temperature"
                description="The hottest CPU package or core sensor in the latest sample."
                value={`${cpuTemp.toFixed(0)} °C`}
                delta={null}
                deltaLabel=""
                upIs="bad"
                loading={stats.loading}
                footer={cpuTemp >= 90 ? "Hot enough to throttle" : "Hottest CPU sensor"}
              />
            )}
            {gpuCount !== null && (
              <StatTile
                label="GPU busy"
                description="The busiest GPU's utilization in the latest sample."
                value={dash(n("gpu_util_now"), formatPercent)}
                delta={null}
                deltaLabel=""
                upIs="neutral"
                loading={stats.loading}
                footer={`${formatCount(gpuCount)} ${gpuCount === 1 ? "GPU" : "GPUs"}`}
              />
            )}

            <MachineHead host={machine} machines={machines.data} onPick={setInstance} />

            {inContainer && <SectionPanels {...CONTAINER_SECTION} instance={instance} words={words} />}
            {SECTIONS.map((sec) => (
              <Fragment key={sec.title}>
                <SectionPanels {...sec} instance={instance} words={words} />
                {sec.title === "Thermals" && (agentGPUs ? <MachineGPUs host={machine} /> : gpuCount !== null && <SectionPanels {...GPU_SECTION} instance={instance} words={words} />)}
              </Fragment>
            ))}

            <MachineSnapshots host={machine} />

            <Section title="Notable moments" />
            <SpikesPanel instance={instance} />
          </div>
        )}
      </div>
    </>
  );
}

function SectionPanels({ title, text, panels, instance, words }: (typeof SECTIONS)[number] & { instance: string; words: RuntimeWords }) {
  return (
    <>
      <Section title={title} text={text} />
      {panels.map((p) => (
        <SystemPanel key={p.name} {...p} instance={instance} words={words} />
      ))}
    </>
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

export function SystemPanel({ name, title, description, viz, span, empty, runtime, instance, words }: PanelDef & { instance: string; words: RuntimeWords }) {
  const tokens = useChartTokens();
  const q = useSystem(name, instance);
  const height = span === "span-3" ? 180 : 220;
  const lang = runtime?.(words);
  const data = q.data && lang ? relabel(q.data, lang.labels) : q.data;
  // Dashboards draw the endpoint's own columns, so they get the viz as defined.
  const dashboard = { kind: "builtin" as const, ref: `system/${name}`, viz, params: instance ? { instance } : undefined };
  if (lang) viz = { ...viz, y: viz.y?.map((y) => lang.labels[y] ?? y) };
  return (
    <Panel
      className={span}
      title={title}
      description={lang?.description ?? description}
      result={data}
      loading={q.loading}
      error={q.error}
      bodyHeight={height}
      note={vizNote(data, viz, tokens)}
      chart={data && <VizView result={data} viz={viz} height={height} title={title} empty={empty} />}
      table={data && <DataTable result={data} maxHeight={height} defaultUnit={viz.unit} />}
      dashboard={dashboard}
    />
  );
}

function SpikesPanel({ instance }: { instance: string }) {
  const q = useSystem("spikes", instance);
  const investigate = (row: unknown[]) => {
    const t = toNumber(row[0]);
    if (t === null) return;
    const when = new Date(t * 1000);
    const q = `What caused the CPU spike around ${dateFormat({ month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", timeZoneName: "short" }).format(when)}? Compare that window with normal traffic and server health, including disk, network and the database pool.`;
    window.location.hash = `#/explore?q=${encodeURIComponent(q)}`;
  };
  const columns: Column[] = [
    { key: "t", label: "Interval", render: (v) => <span className="mono">{formatLogTime(new Date((toNumber(v) ?? 0) * 1000).toISOString())}</span> },
    { key: "cpu", label: "Peak CPU", align: "right", unit: "percent" },
    { key: "memory", label: "Memory", align: "right", unit: "percent" },
    { key: "db_in_use", label: "DB in use", align: "right", unit: "count" },
    { key: "in_flight", label: "In flight", align: "right", unit: "count" },
    { key: "requests", align: "right", unit: "count" },
    { key: "errors", label: "5xx", align: "right", unit: "count" },
    { key: "slowest_ms", label: "Slowest", align: "right", render: (v) => (toNumber(v) === null ? <span className="dim">—</span> : formatMs(toNumber(v)!)) },
    {
      key: "goroutines",
      label: "",
      align: "right",
      render: (_v, row) => (
        <button type="button" className="btn sm ghost spike-investigate" onClick={() => investigate(row)}>
          <Sparkles size={13} />
          Investigate
        </button>
      ),
    },
  ];
  return (
    <Panel
      className="span-12"
      title="Highest CPU intervals"
      description="The intervals with the highest CPU in this range, with what the API was doing at the time. Investigate hands the moment to the analyst."
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={160}
      table={q.data && <DataTable result={q.data} columns={columns} maxHeight={420} sortable={false} />}
    />
  );
}
