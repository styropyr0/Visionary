import { useMemo, useState } from "react";
import { Server, Sparkles } from "lucide-react";
import { Select } from "../components/Select";
import { api, ApiError, rangeQuery, type Result, type Viz } from "../lib/api";
import { formatBytes, formatCount, formatMs, formatPercent, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatLogTime } from "../lib/time";
import { langOf, relabel, wordsFor, type RuntimeWords } from "../lib/runtime";
import { sparkPoints } from "../charts/option";
import { DataTable, type Column } from "../components/DataTable";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";

interface PanelDef {
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

const SECTIONS: { title: string; text: string; panels: PanelDef[] }[] = [
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

function useSystem<T = Result>(name: string, instance: string) {
  const { resolved, tick } = useApp();
  const qs = rangeQuery(resolved.from, resolved.to, { instance });
  return useQuery<T>(`sys-${name}?${qs}#${tick}`, (signal) => api<T>(`/api/system/${name}?${qs}`, { signal }));
}

export function System() {
  const [instance, setInstance] = useState("");
  const instances = useSystem("instances", "");
  const stats = useSystem("stats", instance);
  const cpu = useSystem("cpu", instance);
  const memory = useSystem("process_memory", instance);
  const disk = useSystem("disk_busy", instance);
  const diskIO = useSystem("disk_throughput", instance);
  const net = useSystem("network", instance);

  const missing = instances.error instanceof ApiError && instances.error.status === 404;
  const rows = useMemo(() => instances.data?.rows ?? [], [instances.data]);
  const names = rows.map((r) => String(r[0]));
  // The language of the shown instances: one instance's, or the one they all
  // share. Mixed or unknown falls back to neutral words.
  const runtimes = useMemo(() => {
    const cols = instances.data?.columns ?? [];
    const si = cols.indexOf("sdk");
    const ri = cols.indexOf("runtime");
    return new Map(rows.map((r) => [String(r[0]), { lang: langOf(r[si], r[ri]), runtime: ri >= 0 && r[ri] ? String(r[ri]) : "" }]));
  }, [instances.data, rows]);
  const words = useMemo(() => {
    const shown = instance ? [runtimes.get(instance)] : [...runtimes.values()];
    const langs = new Set(shown.map((x) => x?.lang ?? null));
    return wordsFor(langs.size === 1 ? [...langs][0] : null);
  }, [runtimes, instance]);
  const instanceLabel = (n: string) => {
    const r = runtimes.get(n)?.runtime;
    return r ? `${n} · ${r}` : n;
  };
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
  const speed = (v: number) => `${formatBytes(v)}/s`;

  return (
    <>
      <TopBar
        title="System"
        actions={
          <>
            {names.length > 1 && (
              <Select
                label="Instance"
                icon={<Server size={14} />}
                value={instance}
                onChange={setInstance}
                options={[{ value: "", label: "All instances" }, ...names.map((n) => ({ value: n, label: instanceLabel(n) }))]}
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
          <div className="setup-card">
            <div className="empty-icon">
              <Server size={20} />
            </div>
            <h2>No system metrics yet</h2>
            <p>
              System stats come from the <code className="mono">system_metrics</code> table, which a Visionary SDK in your service
              fills every few seconds with host, disk, network, process, runtime and database-pool samples. Add the SDK and this view
              fills in on its own.
            </p>
            <p style={{ color: "var(--text-3)", fontSize: 12.5 }}>{instances.error?.message}</p>
          </div>
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

            {SECTIONS.map((sec) => (
              <SectionPanels key={sec.title} {...sec} instance={instance} words={words} />
            ))}

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

function SystemPanel({ name, title, description, viz, span, empty, runtime, instance, words }: PanelDef & { instance: string; words: RuntimeWords }) {
  const tokens = useChartTokens();
  const q = useSystem(name, instance);
  const height = span === "span-3" ? 180 : 220;
  const lang = runtime?.(words);
  const data = q.data && lang ? relabel(q.data, lang.labels) : q.data;
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
    />
  );
}

function SpikesPanel({ instance }: { instance: string }) {
  const q = useSystem("spikes", instance);
  const investigate = (row: unknown[]) => {
    const t = toNumber(row[0]);
    if (t === null) return;
    const when = new Date(t * 1000);
    const q = `What caused the CPU spike around ${when.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    })}? Compare that window with normal traffic and server health, including disk, network and the database pool.`;
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
