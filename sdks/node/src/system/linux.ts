import { readdirSync, readFileSync } from "node:fs";

/** /proc readers for the node-level columns. Each returns undefined when the
 * file is missing or unreadable, so the column stays NULL. */

export const IS_LINUX = process.platform === "linux";

function read(path: string): string | undefined {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return undefined;
  }
}

const CPU_FIELDS = ["user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal", "guest", "guest_nice"] as const;
export type CpuTimes = Partial<Record<(typeof CPU_FIELDS)[number], number>>;

export interface ProcStat {
  cpu?: CpuTimes;
  ctxt?: number;
  procsRunning?: number;
  procsBlocked?: number;
}

export function procStat(): ProcStat {
  const text = read("/proc/stat");
  const out: ProcStat = {};
  if (!text) return out;
  for (const line of text.split("\n")) {
    const p = line.trim().split(/\s+/);
    if (p[0] === "cpu") {
      const cpu: CpuTimes = {};
      CPU_FIELDS.forEach((name, i) => {
        if (p[i + 1] !== undefined) cpu[name] = Number(p[i + 1]);
      });
      out.cpu = cpu;
    } else if (p[0] === "ctxt") out.ctxt = Number(p[1]);
    else if (p[0] === "procs_running") out.procsRunning = Number(p[1]);
    else if (p[0] === "procs_blocked") out.procsBlocked = Number(p[1]);
  }
  return out;
}

/** /proc/meminfo in bytes. */
export function meminfo(): Record<string, number> | undefined {
  const text = read("/proc/meminfo");
  if (!text) return undefined;
  const out: Record<string, number> = {};
  for (const line of text.split("\n")) {
    const m = /^(\w+(?:\(\w+\))?):\s+(\d+)(\s+kB)?/.exec(line);
    if (m) out[m[1]!] = Number(m[2]) * (m[3] ? 1024 : 1);
  }
  return out;
}

export interface DiskCounters {
  readBytes: number;
  writeBytes: number;
  reads: number;
  writes: number;
  /** read + write time, ms */
  ioMs: number;
  /** per-disk time spent doing I/O, ms */
  busy: Map<string, number>;
}

// Partitions and virtual devices whose I/O is already counted in a whole
// disk, so totals are not doubled.
const PARTITION = /^((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|dm-\d+|sr\d+|fd\d+|md\d+)$/;

export function diskstats(): DiskCounters | undefined {
  const text = read("/proc/diskstats");
  if (!text) return undefined;
  const c: DiskCounters = { readBytes: 0, writeBytes: 0, reads: 0, writes: 0, ioMs: 0, busy: new Map() };
  for (const line of text.split("\n")) {
    const p = line.trim().split(/\s+/);
    if (p.length < 14 || PARTITION.test(p[2]!)) continue;
    // reads, sectors read, ms reading, writes, sectors written, ms writing, ms doing I/O
    c.reads += Number(p[3]);
    c.readBytes += Number(p[5]) * 512;
    c.writes += Number(p[7]);
    c.writeBytes += Number(p[9]) * 512;
    c.ioMs += Number(p[6]) + Number(p[10]);
    c.busy.set(p[2]!, Number(p[12]));
  }
  return c;
}

export interface NetCounters {
  rxBytes: number;
  txBytes: number;
  rxPackets: number;
  txPackets: number;
  errors: number;
  drops: number;
}

export function netDev(): NetCounters | undefined {
  const text = read("/proc/net/dev");
  if (!text) return undefined;
  const c: NetCounters = { rxBytes: 0, txBytes: 0, rxPackets: 0, txPackets: 0, errors: 0, drops: 0 };
  for (const line of text.split("\n")) {
    const colon = line.indexOf(":");
    if (colon < 0) continue;
    const p = line.slice(colon + 1).trim().split(/\s+/).map(Number);
    if (p.length < 12) continue;
    c.rxBytes += p[0]!;
    c.rxPackets += p[1]!;
    c.errors += p[2]! + p[10]!;
    c.drops += p[3]! + p[11]!;
    c.txBytes += p[8]!;
    c.txPackets += p[9]!;
  }
  return c;
}

/** The Tcp: counters of /proc/net/snmp (CurrEstab, RetransSegs, ...). */
export function tcpSnmp(): Record<string, number> | undefined {
  const text = read("/proc/net/snmp");
  if (!text) return undefined;
  const rows = text.split("\n").filter((l) => l.startsWith("Tcp:"));
  if (rows.length < 2) return undefined;
  const names = rows[0]!.trim().split(/\s+/).slice(1);
  const values = rows[1]!.trim().split(/\s+/).slice(1);
  const out: Record<string, number> = {};
  names.forEach((n, i) => (out[n] = Number(values[i])));
  return out;
}

export function selfIo(): { read: number; write: number } | undefined {
  const text = read("/proc/self/io");
  if (!text) return undefined;
  const r = /^read_bytes:\s*(\d+)/m.exec(text);
  const w = /^write_bytes:\s*(\d+)/m.exec(text);
  return r && w ? { read: Number(r[1]), write: Number(w[1]) } : undefined;
}

export function selfThreads(): number | undefined {
  const m = /^Threads:\s*(\d+)/m.exec(read("/proc/self/status") ?? "");
  return m ? Number(m[1]) : undefined;
}

/** Soft limit on open files, undefined when unlimited or unknown. */
export function maxFds(): number | undefined {
  const m = /^Max open files\s+(\d+|unlimited)/m.exec(read("/proc/self/limits") ?? "");
  if (!m || m[1] === "unlimited") return undefined;
  const n = Number(m[1]);
  return n > 0 && n < 2 ** 40 ? n : undefined;
}

/** Open file descriptors: /proc/self/fd on Linux, /dev/fd on macOS. */
export function openFds(): number | undefined {
  for (const dir of ["/proc/self/fd", "/dev/fd"]) {
    try {
      return readdirSync(dir).length;
    } catch {
      // try the next
    }
  }
  return undefined;
}

export function hostProcs(): number | undefined {
  try {
    return readdirSync("/proc").filter((n) => /^\d+$/.test(n)).length;
  } catch {
    return undefined;
  }
}
