import { statfsSync } from "node:fs";
import os from "node:os";
import { PerformanceObserver } from "node:perf_hooks";
import v8 from "node:v8";
import type { Logger } from "../log.js";
import { errMessage } from "../log.js";
import { type Value, SYSTEM_COLUMNS, insertSQL } from "../store/sql.js";
import type { Store } from "../store/store.js";
import { formatTs, round } from "../util.js";
import * as linux from "./linux.js";

/** Pool stats for the db_* columns. acquires, emptyAcquires and
 * acquireWaitMs are cumulative; the recorder stores per-sample deltas. */
export interface DbStats {
  total?: number;
  idle?: number;
  inUse?: number;
  max?: number;
  acquires?: number;
  emptyAcquires?: number;
  acquireWaitMs?: number;
}

/** Throughput seen by the integrations, read by the sampler. */
export class HttpCounters {
  requests = 0;
  errors = 0;
  inFlight = 0;

  started(): void {
    this.inFlight++;
  }

  finished(status: number): void {
    this.inFlight--;
    this.requests++;
    if (status >= 500) this.errors++;
  }
}

// One write per minute at the default 15 s interval.
const SAMPLES_PER_WRITE = 4;
const MB = 1 << 20;
const GB = 1 << 30;

interface Cumulative {
  cpu?: { busy: number; total: number; user: number; system: number; iowait?: number; steal?: number };
  disk?: linux.DiskCounters;
  net?: linux.NetCounters;
  tcpRetrans?: number;
  ctxt?: number;
  procIo?: { read: number; write: number };
  /** Gauges read from the same files, used as-is. */
  stat?: linux.ProcStat;
  tcp?: Record<string, number>;
}

export interface RecorderOptions {
  intervalS: number;
  instance: string;
  service: string;
  version: string;
  diskPath: string;
  dbStats?: () => DbStats | undefined;
}

/** Samples host, process and runtime metrics on a timer. A source that fails
 * or doesn't exist on this OS is NULL, never an error. */
export class Recorder {
  private readonly insert: string;
  private readonly startedAt = Date.now();
  private timer?: NodeJS.Timeout;
  private buf: Value[][] = [];
  private flushing?: Promise<void>;
  private gcObserver?: PerformanceObserver;
  private gcCount = 0;
  private gcMs = 0;

  private last = 0;
  private lastCpuUsage = process.cpuUsage();
  private requests = 0;
  private errors = 0;
  private prevGcCount = 0;
  private prevGcMs = 0;
  private prevDb: DbStats = {};
  private prev: Cumulative = {};

  constructor(
    private readonly opts: RecorderOptions,
    private readonly store: Store,
    private readonly ready: () => Promise<boolean>,
    readonly http: HttpCounters,
    private readonly logger: Logger,
  ) {
    this.insert = insertSQL("system_metrics", store.stringParams);
  }

  start(): void {
    try {
      this.gcObserver = new PerformanceObserver((list) => {
        for (const e of list.getEntries()) {
          this.gcCount++;
          this.gcMs += e.duration;
        }
      });
      this.gcObserver.observe({ entryTypes: ["gc"] });
    } catch {
      this.gcObserver = undefined;
    }
    this.prime();
    this.timer = setInterval(() => this.tick(), this.opts.intervalS * 1000);
    this.timer.unref();
  }

  async close(): Promise<void> {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
    this.gcObserver?.disconnect();
    await this.flushing;
    await this.flush();
  }

  /** Baseline readings so the first sample has real deltas. */
  private prime(): void {
    this.last = performance.now();
    this.lastCpuUsage = process.cpuUsage();
    this.requests = this.http.requests;
    this.errors = this.http.errors;
    this.prevGcCount = this.gcCount;
    this.prevGcMs = this.gcMs;
    this.prevDb = this.readDb() ?? {};
    this.prev = readCumulative();
  }

  private tick(): void {
    try {
      this.buf.push(this.sample());
    } catch (e) {
      this.logger.warn("visionary: system sample failed: " + errMessage(e));
    }
    if (this.buf.length >= SAMPLES_PER_WRITE && !this.flushing) {
      this.flushing = this.flush().finally(() => (this.flushing = undefined));
    }
  }

  private async flush(): Promise<void> {
    if (this.buf.length === 0) return;
    const rows = this.buf;
    this.buf = [];
    try {
      if (!(await this.ready())) throw new Error("system_metrics table not ready");
      await this.store.batch(rows.map((args) => ({ sql: this.insert, args })));
    } catch (e) {
      this.logger.warn(`visionary: system metrics write failed (${rows.length} rows kept): ${errMessage(e)}`);
      // Keep at most an hour of samples while the database is unreachable.
      const keep = Math.max(1, Math.floor(3600 / this.opts.intervalS));
      this.buf = [...rows, ...this.buf].slice(-keep);
    }
  }

  private readDb(): DbStats | undefined {
    try {
      return this.opts.dbStats?.();
    } catch {
      return undefined;
    }
  }

  sample(): Value[] {
    const nowMs = performance.now();
    let secs = (nowMs - this.last) / 1000;
    this.last = nowMs;
    if (secs <= 0) secs = this.opts.intervalS;
    const v: Record<string, Value | undefined> = {
      created_at: formatTs(Date.now()),
      instance: this.opts.instance,
      interval_s: round(secs, 2),
      service: this.opts.service || null,
      version: this.opts.version || null,
    };
    const quiet = <T>(fn: () => T): T | undefined => {
      try {
        return fn();
      } catch {
        return undefined;
      }
    };

    if (process.platform !== "win32") {
      const [l1, l5, l15] = os.loadavg();
      v.load_1 = round(l1!, 2);
      v.load_5 = round(l5!, 2);
      v.load_15 = round(l15!, 2);
    }
    const cpus = quiet(() => os.cpus().length);
    if (cpus) v.cpu_count = cpus;

    this.memory(v);
    const fs = quiet(() => statfsSync(this.opts.diskPath));
    if (fs && fs.blocks > 0) {
      const used = fs.blocks - fs.bfree;
      v.disk_used_percent = round((used / (used + fs.bavail)) * 100, 1);
      v.disk_free_gb = round((fs.bavail * fs.bsize) / GB, 2);
      v.disk_total_gb = round((fs.blocks * fs.bsize) / GB, 2);
      if (fs.files > 0) v.disk_inodes_percent = round(((fs.files - fs.ffree) / fs.files) * 100, 1);
    }

    const usage = process.cpuUsage(this.lastCpuUsage);
    this.lastCpuUsage = process.cpuUsage();
    v.proc_cpu_percent = round(((usage.user + usage.system) / (secs * 1e6)) * 100, 1);
    v.proc_rss_mb = round(process.memoryUsage.rss() / MB, 1);
    v.proc_threads = linux.selfThreads();
    v.proc_open_fds = process.platform === "win32" ? undefined : linux.openFds();
    v.proc_max_fds = linux.maxFds();

    // Node's concurrency units: active handles, requests and timers.
    v.goroutines = quiet(() => process.getActiveResourcesInfo().length);
    const heap = v8.getHeapStatistics();
    v.heap_alloc_mb = round(heap.used_heap_size / MB, 1);
    v.heap_inuse_mb = round(heap.total_heap_size / MB, 1);
    if (this.gcObserver) {
      const pauseMs = this.gcMs - this.prevGcMs;
      v.gc_count = this.gcCount - this.prevGcCount;
      v.gc_pause_ms = round(pauseMs, 2);
      v.gc_cpu_percent = round((pauseMs / (secs * 1000)) * 100, 2);
      this.prevGcCount = this.gcCount;
      this.prevGcMs = this.gcMs;
    }

    const db = this.readDb();
    if (db) {
      const prev = this.prevDb;
      v.db_total_conns = db.total;
      v.db_idle_conns = db.idle;
      v.db_acquired_conns = db.inUse;
      v.db_max_conns = db.max;
      if (db.acquires !== undefined) v.db_acquires = db.acquires - (prev.acquires ?? 0);
      if (db.emptyAcquires !== undefined) v.db_empty_acquires = db.emptyAcquires - (prev.emptyAcquires ?? 0);
      if (db.acquireWaitMs !== undefined) v.db_acquire_wait_ms = round(db.acquireWaitMs - (prev.acquireWaitMs ?? 0), 1);
      this.prevDb = db;
    }

    const reqs = this.http.requests;
    const errs = this.http.errors;
    v.http_requests = reqs - this.requests;
    v.http_errors = errs - this.errors;
    v.http_rps = round((reqs - this.requests) / secs, 2);
    v.http_in_flight = this.http.inFlight;
    this.requests = reqs;
    this.errors = errs;
    v.uptime_s = Math.floor((Date.now() - this.startedAt) / 1000);

    this.node(v, secs);
    return SYSTEM_COLUMNS.map((c) => v[c] ?? null);
  }

  private memory(v: Record<string, Value | undefined>): void {
    const mi = linux.IS_LINUX ? linux.meminfo() : undefined;
    if (mi?.MemTotal) {
      // As psutil and gopsutil count it: used excludes buffers and page cache.
      const cached = (mi.Cached ?? 0) + (mi.SReclaimable ?? 0);
      const available = mi.MemAvailable ?? (mi.MemFree ?? 0) + cached + (mi.Buffers ?? 0);
      v.host_mem_total_mb = round(mi.MemTotal / MB, 1);
      v.host_mem_used_mb = round((mi.MemTotal - (mi.MemFree ?? 0) - (mi.Buffers ?? 0) - cached) / MB, 1);
      v.host_mem_percent = round(((mi.MemTotal - available) / mi.MemTotal) * 100, 1);
      v.host_mem_available_mb = round(available / MB, 1);
      if (cached > 0) v.host_mem_cached_mb = round(cached / MB, 1);
      if (mi.SwapTotal !== undefined) {
        v.swap_total_mb = round(mi.SwapTotal / MB, 1);
        v.swap_used_mb = round((mi.SwapTotal - (mi.SwapFree ?? 0)) / MB, 1);
      }
      return;
    }
    const total = os.totalmem();
    const free = os.freemem();
    v.host_mem_total_mb = round(total / MB, 1);
    v.host_mem_used_mb = round((total - free) / MB, 1);
    v.host_mem_percent = round(((total - free) / total) * 100, 1);
    v.host_mem_available_mb = round(free / MB, 1);
  }

  /** The node-level counters that are deltas between samples. */
  private node(v: Record<string, Value | undefined>, secs: number): void {
    const now = readCumulative();
    const prev = this.prev;
    this.prev = now;
    if (linux.IS_LINUX) {
      v.procs_running = now.stat?.procsRunning;
      v.procs_blocked = now.stat?.procsBlocked;
      v.host_procs = linux.hostProcs();
      v.tcp_established = now.tcp?.CurrEstab;
    }

    if (now.cpu && prev.cpu) {
      const total = now.cpu.total - prev.cpu.total;
      if (total > 0) {
        const pct = (a: number, b: number) => round((100 * (a - b)) / total, 1);
        v.host_cpu_percent = pct(now.cpu.busy, prev.cpu.busy);
        v.cpu_user_percent = pct(now.cpu.user, prev.cpu.user);
        v.cpu_system_percent = pct(now.cpu.system, prev.cpu.system);
        if (now.cpu.iowait !== undefined && prev.cpu.iowait !== undefined) v.cpu_iowait_percent = pct(now.cpu.iowait, prev.cpu.iowait);
        if (now.cpu.steal !== undefined && prev.cpu.steal !== undefined) v.cpu_steal_percent = pct(now.cpu.steal, prev.cpu.steal);
      }
    }
    if (now.disk && prev.disk && now.disk.readBytes >= prev.disk.readBytes && now.disk.writeBytes >= prev.disk.writeBytes) {
      const reads = now.disk.reads - prev.disk.reads;
      const writes = now.disk.writes - prev.disk.writes;
      v.disk_read_kbps = round((now.disk.readBytes - prev.disk.readBytes) / 1024 / secs, 1);
      v.disk_write_kbps = round((now.disk.writeBytes - prev.disk.writeBytes) / 1024 / secs, 1);
      v.disk_read_iops = round(reads / secs, 1);
      v.disk_write_iops = round(writes / secs, 1);
      if (reads + writes > 0 && now.disk.ioMs >= prev.disk.ioMs) {
        v.disk_await_ms = round((now.disk.ioMs - prev.disk.ioMs) / (reads + writes), 2);
      }
      // Busy is the busiest disk's share of the interval spent on I/O.
      let busiest = 0;
      for (const [name, t] of now.disk.busy) {
        const p = prev.disk.busy.get(name);
        if (p !== undefined && t >= p) busiest = Math.max(busiest, t - p);
      }
      if (now.disk.busy.size > 0) v.disk_busy_percent = round(Math.min(100, (100 * busiest) / (secs * 1000)), 1);
    }
    if (now.net && prev.net && now.net.rxBytes >= prev.net.rxBytes && now.net.txBytes >= prev.net.txBytes) {
      v.net_rx_kbps = round((now.net.rxBytes - prev.net.rxBytes) / 1024 / secs, 1);
      v.net_tx_kbps = round((now.net.txBytes - prev.net.txBytes) / 1024 / secs, 1);
      v.net_rx_pps = round((now.net.rxPackets - prev.net.rxPackets) / secs, 1);
      v.net_tx_pps = round((now.net.txPackets - prev.net.txPackets) / secs, 1);
      if (now.net.errors >= prev.net.errors) v.net_errors = now.net.errors - prev.net.errors;
      if (now.net.drops >= prev.net.drops) v.net_drops = now.net.drops - prev.net.drops;
    }
    if (now.tcpRetrans !== undefined && prev.tcpRetrans !== undefined && now.tcpRetrans >= prev.tcpRetrans) {
      v.tcp_retrans = now.tcpRetrans - prev.tcpRetrans;
    }
    if (now.ctxt !== undefined && prev.ctxt !== undefined && now.ctxt >= prev.ctxt) {
      v.ctx_switches_ps = round((now.ctxt - prev.ctxt) / secs, 1);
    }
    if (now.procIo && prev.procIo && now.procIo.read >= prev.procIo.read && now.procIo.write >= prev.procIo.write) {
      v.proc_read_kbps = round((now.procIo.read - prev.procIo.read) / 1024 / secs, 1);
      v.proc_write_kbps = round((now.procIo.write - prev.procIo.write) / 1024 / secs, 1);
    }
  }
}

/** Cumulative readings; a field is present only when its source worked.
 * Linux reads /proc; elsewhere only CPU times are available (os.cpus). */
function readCumulative(): Cumulative {
  if (!linux.IS_LINUX) {
    let user = 0, nice = 0, sys = 0, idle = 0, irq = 0;
    for (const c of os.cpus()) {
      user += c.times.user;
      nice += c.times.nice;
      sys += c.times.sys;
      idle += c.times.idle;
      irq += c.times.irq;
    }
    const total = user + nice + sys + idle + irq;
    return total > 0 ? { cpu: { busy: total - idle, total, user: user + nice, system: sys + irq } } : {};
  }
  const out: Cumulative = {};
  const stat = linux.procStat();
  out.stat = stat;
  const t = stat.cpu;
  if (t) {
    const f = (k: keyof linux.CpuTimes) => t[k] ?? 0;
    // Guest time is already counted in user and nice.
    const total = Object.values(t).reduce((a, b) => a + (b ?? 0), 0) - f("guest") - f("guest_nice");
    out.cpu = {
      busy: total - f("idle") - f("iowait"),
      total,
      user: f("user") + f("nice"),
      system: f("system") + f("irq") + f("softirq"),
      iowait: f("iowait"),
      steal: f("steal"),
    };
  }
  out.ctxt = stat.ctxt;
  out.disk = linux.diskstats();
  out.net = linux.netDev();
  out.tcp = linux.tcpSnmp();
  out.tcpRetrans = out.tcp?.RetransSegs;
  out.procIo = linux.selfIo();
  return out;
}
