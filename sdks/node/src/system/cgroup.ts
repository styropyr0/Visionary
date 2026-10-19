import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import type { Value } from "../store/sql.js";
import { round } from "../util.js";
import { IS_LINUX } from "./linux.js";

/** Reads the CPU and memory limits and use of the process's own cgroup
 * (Linux, v2 or v1): in a container these are the numbers that matter, since
 * the host columns describe the whole machine underneath. The container_*
 * columns are filled only in a container, or when the cgroup sets a CPU or
 * memory limit (a systemd unit with MemoryMax=, say). sdks/go/cgroup.go is
 * the reference. */
export class CgroupReader {
  private v2 = false;
  /** v2: the cgroup; v1: the cpu controller's directory */
  private cpuDir = "";
  /** v1: the cpuacct controller's directory */
  private cpuAcct = "";
  private memDir = "";
  private inCtr = false;
  private last = 0;
  /** The previous sample's counters; usage is CPU time in ns. */
  private prev: Counters = {};
  private primed = false;

  /** root is "/" but for tests, which point it at a fake filesystem. */
  private constructor(private readonly root: string) {}

  /** Finds the process's cgroup; undefined when there is none to read. */
  static open(root = "/"): CgroupReader | undefined {
    if (!IS_LINUX && root === "/") return undefined;
    const c = new CgroupReader(root);
    const text = read(c.at("/proc/self/cgroup"));
    if (text === undefined) return undefined;
    c.inCtr = c.inContainer(text);
    for (const line of text.trim().split("\n")) {
      const f = line.trim().split(":");
      if (f.length < 3) continue;
      const [id, ctrls] = f as [string, string];
      const p = f.slice(2).join(":");
      const list = ctrls.split(",");
      if (id === "0" && ctrls === "") {
        const dir = c.cgroupDir("/sys/fs/cgroup", p);
        if (existsSync(path.join(dir, "cpu.stat"))) {
          c.v2 = true;
          c.cpuDir = c.memDir = dir;
          break;
        }
      } else if (list.includes("memory")) {
        c.memDir = c.cgroupDir("/sys/fs/cgroup/memory", p);
      } else if (list.includes("cpu") || list.includes("cpuacct")) {
        const dir = c.cgroupDir("/sys/fs/cgroup/" + ctrls, p);
        if (list.includes("cpu")) c.cpuDir = dir;
        if (list.includes("cpuacct")) c.cpuAcct = dir;
      }
    }
    return c.cpuDir || c.memDir ? c : undefined;
  }

  private at(p: string): string {
    return path.join(this.root, p);
  }

  /** The cgroup's directory under a controller's mount. Inside a container's
   * cgroup namespace the path is "/", and with a private mount the host path
   * does not exist below it, so the mount itself is the cgroup. */
  private cgroupDir(mount: string, p: string): string {
    const dir = this.at(path.posix.join(mount, p));
    return existsSync(dir) ? dir : this.at(mount);
  }

  /** Tells a container from a process on the host by the runtimes' marker
   * files, the cgroup path and Kubernetes' environment. */
  private inContainer(cgroups: string): boolean {
    if (existsSync(this.at("/.dockerenv")) || existsSync(this.at("/run/.containerenv"))) return true;
    if (this.root === "/" && process.env.KUBERNETES_SERVICE_HOST) return true;
    return ["docker", "kubepods", "containerd", "libpod", "lxc"].some((k) => cgroups.includes(k));
  }

  /** The CPU limit in cores and the memory limit in bytes, 0 when unlimited. */
  private limits(): { cpus: number; mem: number } {
    let cpus = 0;
    let mem = 0;
    if (this.v2) {
      const f = read(path.join(this.cpuDir, "cpu.max"))?.trim().split(/\s+/);
      if (f?.length === 2 && f[0] !== "max") {
        const q = Number(f[0]);
        const p = Number(f[1]);
        if (q > 0 && p > 0) cpus = q / p;
      }
      mem = readUint(path.join(this.memDir, "memory.max")) ?? 0; // "max": 0
      return { cpus, mem };
    }
    if (this.cpuDir) {
      const q = Number(read(path.join(this.cpuDir, "cpu.cfs_quota_us"))?.trim());
      const p = readUint(path.join(this.cpuDir, "cpu.cfs_period_us"));
      if (q > 0 && p) cpus = q / p;
    }
    if (this.memDir) {
      // v1 writes a page-rounded maximum int64 for no limit.
      const m = readUint(path.join(this.memDir, "memory.limit_in_bytes"));
      if (m !== undefined && m < 2 ** 62) mem = m;
    }
    return { cpus, mem };
  }

  /** The cumulative CPU time (ns), CFS periods, throttled periods and OOM
   * kills; a counter that can't be read is undefined. */
  private counters(): Counters {
    if (this.v2) {
      const st = readKV(path.join(this.cpuDir, "cpu.stat"));
      const ev = readKV(path.join(this.memDir, "memory.events"));
      const usec = st?.usage_usec;
      return { usage: usec === undefined ? undefined : usec * 1000, periods: st?.nr_periods, throttled: st?.nr_throttled, ooms: ev?.oom_kill };
    }
    const st = this.cpuDir ? readKV(path.join(this.cpuDir, "cpu.stat")) : undefined;
    return {
      usage: this.cpuAcct ? readUint(path.join(this.cpuAcct, "cpuacct.usage")) : undefined,
      periods: st?.nr_periods,
      throttled: st?.nr_throttled,
      // Newer kernels only.
      ooms: this.memDir ? readKV(path.join(this.memDir, "memory.oom_control"))?.oom_kill : undefined,
    };
  }

  /** Memory in use without the inactive page cache, as docker stats counts it. */
  private memUsed(): number | undefined {
    if (!this.memDir) return undefined;
    const [file, statKey] = this.v2 ? ["memory.current", "inactive_file"] : ["memory.usage_in_bytes", "total_inactive_file"];
    let used = readUint(path.join(this.memDir, file));
    if (used === undefined) return undefined;
    const inactive = readKV(path.join(this.memDir, "memory.stat"))?.[statKey];
    if (inactive !== undefined && inactive < used) used -= inactive;
    return used;
  }

  /** The baseline the first sample's deltas start from. */
  prime(): void {
    this.prev = this.counters();
    this.last = performance.now();
    this.primed = true;
  }

  /** Fills the container_* columns. */
  sample(v: Record<string, Value | undefined>): void {
    const { cpus, mem } = this.limits();
    if (!this.inCtr && cpus === 0 && mem === 0) return;
    const now = performance.now();
    const c = this.counters();
    const p = this.prev;
    const secs = (now - this.last) / 1000;
    if (this.primed && secs > 0) {
      // A counter that went down (the cgroup was reset) is NULL this sample.
      if (c.usage !== undefined && p.usage !== undefined && c.usage >= p.usage) {
        v.container_cpu_cores = round((c.usage - p.usage) / 1e9 / secs, 3);
      }
      const periods = (c.periods ?? 0) - (p.periods ?? 0);
      const throttled = (c.throttled ?? 0) - (p.throttled ?? 0);
      if (periods > 0 && throttled >= 0) {
        v.container_cpu_throttled_percent = round((100 * throttled) / periods, 1);
      } else if (cpus > 0) {
        v.container_cpu_throttled_percent = 0;
      }
      if (c.ooms !== undefined && p.ooms !== undefined && c.ooms >= p.ooms) v.container_oom_kills = c.ooms - p.ooms;
    }
    this.prev = c;
    this.last = now;
    this.primed = true;
    if (cpus > 0) v.container_cpu_limit = round(cpus, 2);
    const used = this.memUsed();
    if (used !== undefined) v.container_mem_used_mb = round(used / MB, 1);
    if (mem > 0) v.container_mem_limit_mb = round(mem / MB, 1);
  }
}

interface Counters {
  usage?: number;
  periods?: number;
  throttled?: number;
  ooms?: number;
}

const MB = 1 << 20;

function read(p: string): string | undefined {
  try {
    return readFileSync(p, "utf8");
  } catch {
    return undefined;
  }
}

function readUint(p: string): number | undefined {
  const s = read(p)?.trim();
  return s !== undefined && /^\d+$/.test(s) ? Number(s) : undefined;
}

/** "key value" lines (cpu.stat, memory.stat, memory.events). */
function readKV(p: string): Record<string, number> | undefined {
  const text = read(p);
  if (text === undefined) return undefined;
  const out: Record<string, number> = {};
  for (const line of text.split("\n")) {
    const f = line.trim().split(/\s+/);
    if (f.length === 2 && /^\d+$/.test(f[1]!)) out[f[0]!] = Number(f[1]);
  }
  return out;
}
