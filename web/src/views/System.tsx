import { useMemo, useState } from "react";
import { Server, Sparkles } from "lucide-react";
import { api, ApiError, rangeQuery, type Result, type Viz } from "../lib/api";
import { formatBytes, formatCount, formatMs, formatPercent, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatLogTime, formatRelative } from "../lib/time";
import { sparkPoints } from "../charts/option";
import { DataTable, type Column } from "../components/DataTable";
import { Panel } from "../components/Panel";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";

const PANELS: { name: string; title: string; description: string; viz: Viz; span: string }[] = [
  {
    name: "cpu",
    title: "Host CPU",
    description: "Whole-machine CPU use: average and peak per interval.",
    viz: { type: "timeseries", x: "t", y: ["avg_cpu", "max_cpu"], unit: "percent" },
    span: "span-6",
  },
  {
    name: "process_cpu",
    title: "Process CPU",
    description: "The API process alone, as a percent of one core (can exceed 100% on multi-core hosts).",
    viz: { type: "timeseries", x: "t", y: ["avg_process_cpu", "max_process_cpu"], unit: "percent" },
    span: "span-6",
  },
  {
    name: "process_memory",
    title: "Process memory",
    description: "Resident memory of the API process and the Go heap in use.",
    viz: { type: "timeseries", x: "t", y: ["rss_bytes", "heap_bytes"], unit: "bytes" },
    span: "span-6",
  },
  {
    name: "memory",
    title: "Host memory and disk",
    description: "Percent of host memory and of the disk in use.",
    viz: { type: "timeseries", x: "t", y: ["host_memory", "disk_used"], unit: "percent" },
    span: "span-6",
  },
  {
    name: "throughput",
    title: "Throughput",
    description: "Requests per second (average and peak) and the most requests in flight at once.",
    viz: { type: "timeseries", x: "t", y: ["avg_rps", "peak_rps", "max_in_flight"], unit: "count" },
    span: "span-6",
  },
  {
    name: "db_pool",
    title: "Database connections",
    description: "Connections in use and open, against the pool limit. In-use reaching the limit means requests queue for a connection.",
    viz: { type: "timeseries", x: "t", y: ["in_use", "open", "pool_limit"], unit: "count" },
    span: "span-6",
  },
  {
    name: "db_wait",
    title: "Waiting for a connection",
    description: "Total time requests spent waiting for a database connection per interval.",
    viz: { type: "timeseries", x: "t", y: ["acquire_wait_ms"], unit: "ms" },
    span: "span-4",
  },
  {
    name: "goroutines",
    title: "Goroutines and threads",
    viz: { type: "timeseries", x: "t", y: ["goroutines", "threads"], unit: "count" },
    description: "A steady climb in goroutines usually means a leak or stuck work.",
    span: "span-4",
  },
  {
    name: "gc",
    title: "GC pause",
    description: "Total garbage-collection pause per interval.",
    viz: { type: "timeseries", x: "t", y: ["gc_pause_ms"], unit: "ms" },
    span: "span-4",
  },
  {
    name: "load",
    title: "Load average",
    description: "Run-queue length over 1, 5 and 15 minutes (not reported on Windows hosts).",
    viz: { type: "timeseries", x: "t", y: ["load_1", "load_5", "load_15"], unit: "none" },
    span: "span-6",
  },
  {
    name: "network",
    title: "Network per second",
    description: "Bytes received and sent per second by the host.",
    viz: { type: "timeseries", x: "t", y: ["received_bytes", "sent_bytes"], unit: "bytes" },
    span: "span-6",
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
  const db = useSystem("db_pool", instance);
  const goroutines = useSystem("goroutines", instance);

  const missing = instances.error instanceof ApiError && instances.error.status === 404;
  const names = (instances.data?.rows ?? []).map((r) => String(r[0]));
  const s = useMemo(() => {
    const r = stats.data;
    if (!r || r.rows.length === 0) return null;
    const o: Record<string, unknown> = {};
    r.columns.forEach((c, i) => (o[c] = r.rows[0][i]));
    return o;
  }, [stats.data]);
  const n = (k: string) => toNumber(s?.[k]);

  return (
    <>
      <TopBar
        title="System"
        actions={
          <>
            {names.length > 1 && (
              <select className="select" value={instance} onChange={(e) => setInstance(e.target.value)} aria-label="Instance">
                <option value="">All instances</option>
                {names.map((n) => (
                  <option key={n}>{n}</option>
                ))}
              </select>
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
              System stats come from the <code className="mono">system_metrics</code> table, which the API server fills every
              few seconds with host, process, runtime and database-pool samples. Turn the recorder on in your API server and this
              view fills in on its own.
            </p>
            <p style={{ color: "var(--text-3)", fontSize: 12.5 }}>{instances.error?.message}</p>
          </div>
        ) : (
          <div className="grid">
            <StatTile
              label="Host CPU"
              description="Latest sample, with the range average and peak below."
              value={n("cpu_now") === null ? "—" : formatPercent(n("cpu_now")!)}
              delta={null}
              deltaLabel=""
              upIs="bad"
              spark={sparkPoints(cpu.data, ["avg_cpu"])}
              loading={stats.loading}
              footer={n("cpu_max") !== null ? `avg ${formatPercent(n("cpu_avg") ?? 0)} · peak ${formatPercent(n("cpu_max")!)}` : undefined}
            />
            <StatTile
              label="Host memory"
              value={n("mem_now") === null ? "—" : formatPercent(n("mem_now")!)}
              delta={null}
              deltaLabel=""
              upIs="bad"
              loading={stats.loading}
              footer={n("mem_max") !== null ? `peak ${formatPercent(n("mem_max")!)}` : undefined}
            />
            <StatTile
              label="Process memory"
              value={n("rss_now") === null ? "—" : formatBytes(n("rss_now")! * 1048576)}
              delta={null}
              deltaLabel=""
              upIs="bad"
              spark={sparkPoints(memory.data, ["rss_bytes"])}
              loading={stats.loading}
              footer={n("rss_max") !== null ? `peak ${formatBytes(n("rss_max")! * 1048576)}` : undefined}
            />
            <StatTile
              label="DB connections in use"
              value={n("db_now") === null ? "—" : `${formatCount(n("db_now")!)} / ${formatCount(n("db_limit") ?? 0)}`}
              delta={null}
              deltaLabel=""
              upIs="bad"
              spark={sparkPoints(db.data, ["in_use"])}
              loading={stats.loading}
              footer={n("db_max") !== null ? `peak ${formatCount(n("db_max")!)}` : undefined}
            />
            <StatTile
              label="Goroutines"
              value={n("goroutines_now") === null ? "—" : formatCount(n("goroutines_now")!)}
              delta={null}
              deltaLabel=""
              upIs="bad"
              spark={sparkPoints(goroutines.data, ["goroutines"])}
              loading={stats.loading}
              footer={n("goroutines_max") !== null ? `peak ${formatCount(n("goroutines_max")!)}` : undefined}
            />
            <StatTile
              label="Uptime"
              value={n("uptime_s") === null ? "—" : formatUptime(n("uptime_s")!)}
              delta={null}
              deltaLabel=""
              upIs="neutral"
              loading={stats.loading}
              footer={s?.last_seen ? `last sample ${formatRelative(String(s.last_seen))}` : undefined}
            />

            {PANELS.map((p) => (
              <SystemPanel key={p.name} {...p} instance={instance} />
            ))}

            <SpikesPanel instance={instance} />
          </div>
        )}
      </div>
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

function SystemPanel({ name, title, description, viz, span, instance }: (typeof PANELS)[number] & { instance: string }) {
  const tokens = useChartTokens();
  const q = useSystem(name, instance);
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
      chart={q.data && <VizView result={q.data} viz={viz} height={height} title={title} />}
      table={q.data && <DataTable result={q.data} maxHeight={height} defaultUnit={viz.unit} />}
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
    })}? Compare that window with normal traffic and server health.`;
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
        <button className="btn sm ghost spike-investigate" onClick={() => investigate(row)}>
          <Sparkles size={13} />
          Investigate
        </button>
      ),
    },
  ];
  return (
    <Panel
      className="span-12"
      title="Notable moments"
      description="The intervals with the highest CPU in this range, with what the API was doing at the time. Investigate hands the moment to the analyst."
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={160}
      table={q.data && <DataTable result={q.data} columns={columns} maxHeight={420} sortable={false} />}
    />
  );
}
