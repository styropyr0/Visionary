import { readFileSync } from "node:fs";
import path from "node:path";
import type { Value } from "../store/sql.js";
import { round } from "../util.js";
import { IS_LINUX } from "./linux.js";

/** One /proc/pressure or cgroup *.pressure file, in percent */
export interface Pressure {
  someAvg10: number;
  someAvg60: number;
  fullAvg10?: number;
}

/** Reads "some avg10=0.12 avg60=..." and the optional "full" line */
export function parsePressure(text: string): Pressure | undefined {
  let some: [number, number] | undefined;
  let full: number | undefined;
  for (const line of text.split("\n")) {
    const a10 = /\bavg10=([\d.]+)/.exec(line);
    const a60 = /\bavg60=([\d.]+)/.exec(line);
    if (!a10 || !a60) continue;
    if (line.startsWith("some ")) some = [Number(a10[1]), Number(a60[1])];
    else if (line.startsWith("full ")) full = Number(a10[1]);
  }
  if (!some) return undefined;
  return { someAvg10: some[0], someAvg60: some[1], fullAvg10: full };
}

/** The /proc/vmstat counters the columns are rates of */
export interface VMStat {
  pgfault: number;
  pgmajfault: number;
  pswpin: number;
  pswpout: number;
  pgscan: number;
  pgsteal: number;
  oomKill?: number;
}

/** Sums reclaim over kswapd, direct and khugepaged scans but not pgscan_anon
 * or pgscan_file, which split the same pages by type */
export function parseVMStat(text: string): VMStat | undefined {
  const s: VMStat = { pgfault: 0, pgmajfault: 0, pswpin: 0, pswpout: 0, pgscan: 0, pgsteal: 0 };
  let ok = false;
  eachKV(text, " ", (k, v) => {
    switch (k) {
      case "pgfault":
        s.pgfault = v;
        ok = true;
        break;
      case "pgmajfault":
        s.pgmajfault = v;
        break;
      case "pswpin":
        s.pswpin = v;
        break;
      case "pswpout":
        s.pswpout = v;
        break;
      case "oom_kill":
        s.oomKill = v;
        break;
      case "pgscan_direct_throttle":
        break;
      default:
        if (/^pgscan_(kswapd|direct|khugepaged)/.test(k)) s.pgscan += v;
        else if (/^pgsteal_(kswapd|direct|khugepaged)/.test(k)) s.pgsteal += v;
    }
  });
  return ok ? s : undefined;
}

/** The /proc/meminfo fields of the mem_* columns, in kB */
export interface MemInfo {
  dirty: number;
  writeback: number;
  slab: number;
  shmem: number;
  committed: number;
  commitLimit: number;
}

/** Undefined unless Dirty, Committed_AS and CommitLimit are all there */
export function parseMemInfo(text: string): MemInfo | undefined {
  const f: Record<string, number> = {};
  eachKV(text, ":", (k, v) => (f[k] = v));
  if (f.Dirty === undefined || f.Committed_AS === undefined || f.CommitLimit === undefined) return undefined;
  return {
    dirty: f.Dirty,
    writeback: f.Writeback ?? 0,
    slab: f.Slab ?? 0,
    shmem: f.Shmem ?? 0,
    committed: f.Committed_AS,
    commitLimit: f.CommitLimit,
  };
}

/** The fault counters of a cgroup's memory.stat, v2 (pgfault) or v1 (total_pgfault) */
export function parseCgroupMemStat(text: string): { faults: number; major: number } | undefined {
  let faults: number | undefined;
  let major = 0;
  eachKV(text, " ", (k, v) => {
    if (k === "pgfault" || k === "total_pgfault") faults = v;
    else if (k === "pgmajfault" || k === "total_pgmajfault") major = v;
  });
  return faults === undefined ? undefined : { faults, major };
}

/** high and max from a v2 memory.events */
export function parseCgroupMemEvents(text: string): { high: number; max: number } | undefined {
  let high: number | undefined;
  let max: number | undefined;
  eachKV(text, " ", (k, v) => {
    if (k === "high") high = v;
    else if (k === "max") max = v;
  });
  return high === undefined || max === undefined ? undefined : { high, max };
}

/** Calls fn for "key<sep> value [unit]" lines with an unsigned value */
function eachKV(text: string, sep: string, fn: (k: string, v: number) => void): void {
  for (const line of text.split("\n")) {
    const i = line.indexOf(sep);
    if (i <= 0) continue;
    const v = line.slice(i + 1).trim().split(" ")[0]!;
    if (/^\d+$/.test(v)) fn(line.slice(0, i), Number(v));
  }
}

/** Reads the pressure and virtual memory columns on Linux.
 * sdks/go/hoststats is the reference */
export class HostStats {
  private vm?: VMStat;

  /** pressureDir is the cgroup v2 directory in a container, "" in a container
   * without one (the host's pressure would mislead), undefined on the host.
   * root is "/" but for tests */
  constructor(
    private readonly pressureDir?: string,
    private readonly root = "/",
  ) {}

  private enabled(): boolean {
    return IS_LINUX || this.root !== "/";
  }

  private read(p: string): string | undefined {
    return readAbs(path.join(this.root, p));
  }

  prime(): void {
    if (!this.enabled()) return;
    const text = this.read("/proc/vmstat");
    this.vm = text === undefined ? undefined : parseVMStat(text);
  }

  sample(v: Record<string, Value | undefined>, secs: number): void {
    if (!this.enabled() || secs <= 0) return;
    this.pressure(v);
    this.vmstat(v, secs);
    const mi = this.read("/proc/meminfo");
    const m = mi === undefined ? undefined : parseMemInfo(mi);
    if (m) {
      const mb = (kb: number) => round(kb / 1024, 1);
      v.mem_dirty_mb = mb(m.dirty);
      v.mem_writeback_mb = mb(m.writeback);
      v.mem_slab_mb = mb(m.slab);
      v.mem_shmem_mb = mb(m.shmem);
      v.mem_committed_mb = mb(m.committed);
      v.mem_commit_limit_mb = mb(m.commitLimit);
    }
  }

  private pressure(v: Record<string, Value | undefined>): void {
    if (this.pressureDir === "") return;
    const dir = this.pressureDir ?? path.join(this.root, "/proc/pressure");
    const names = this.pressureDir === undefined ? ["cpu", "memory", "io"] : ["cpu.pressure", "memory.pressure", "io.pressure"];
    const at = (i: number) => {
      const text = readAbs(path.join(dir, names[i]!));
      return text === undefined ? undefined : parsePressure(text);
    };
    const cpu = at(0);
    if (cpu) {
      v.psi_cpu_some_avg10 = cpu.someAvg10;
      v.psi_cpu_some_avg60 = cpu.someAvg60;
    }
    const mem = at(1);
    if (mem) {
      v.psi_mem_some_avg10 = mem.someAvg10;
      v.psi_mem_some_avg60 = mem.someAvg60;
      v.psi_mem_full_avg10 = mem.fullAvg10;
    }
    const io = at(2);
    if (io) {
      v.psi_io_some_avg10 = io.someAvg10;
      v.psi_io_some_avg60 = io.someAvg60;
      v.psi_io_full_avg10 = io.fullAvg10;
    }
  }

  private vmstat(v: Record<string, Value | undefined>, secs: number): void {
    const text = this.read("/proc/vmstat");
    const now = text === undefined ? undefined : parseVMStat(text);
    const p = this.vm;
    this.vm = now;
    if (!now || !p) return;
    const rate = (col: string, a: number, b: number) => {
      if (a >= b) v[col] = round((a - b) / secs, 1);
    };
    if (now.pgfault >= now.pgmajfault && p.pgfault >= p.pgmajfault) {
      rate("minor_faults_ps", now.pgfault - now.pgmajfault, p.pgfault - p.pgmajfault);
    }
    rate("major_faults_ps", now.pgmajfault, p.pgmajfault);
    rate("swap_in_ps", now.pswpin, p.pswpin);
    rate("swap_out_ps", now.pswpout, p.pswpout);
    rate("pages_scanned_ps", now.pgscan, p.pgscan);
    rate("pages_stolen_ps", now.pgsteal, p.pgsteal);
    if (now.oomKill !== undefined && p.oomKill !== undefined && now.oomKill >= p.oomKill) {
      v.host_oom_kills = now.oomKill - p.oomKill;
    }
  }
}

function readAbs(p: string): string | undefined {
  try {
    return readFileSync(p, "utf8");
  } catch {
    return undefined;
  }
}
