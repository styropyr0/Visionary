"""Node-exporter style host metrics (the system_metrics columns from cpu_count on).

psutil is used when installed; on Linux, /proc fills the same values without
it, including the host and process columns the Recorder reads from psutil.
Anything a platform can't report stays NULL.
"""

from __future__ import annotations

import os
import re
import sys
from typing import Any, Dict, Optional

try:
    import psutil
except ImportError:
    psutil = None  # type: ignore[assignment]

try:
    import resource
except ImportError:  # Windows
    resource = None  # type: ignore[assignment]

IS_LINUX = sys.platform.startswith("linux")

# psutil reports disk read/write time in ms, except on Windows where it is
# whole seconds.
_DISK_TIME_TO_MS = 1000 if sys.platform == "win32" else 1

_MB = float(1 << 20)

# Partitions and virtual devices whose I/O is already counted in a whole
# disk, so totals are not doubled.
PARTITION = re.compile(
    r"^((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|dm-\d+|sr\d+|fd\d+|md\d+)$")

# Clock ticks per second for /proc/self/stat times.
_CLK_TCK = os.sysconf("SC_CLK_TCK") if hasattr(os, "sysconf") and IS_LINUX else 100

_PROC_STAT_CPU = ("user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal", "guest", "guest_nice")


class NodeCollector:
    def __init__(self, proc: Any, disk_path: str) -> None:
        self._proc = proc
        self._disk_path = disk_path
        self._prev: Dict[str, Any] = {}

    def prime(self) -> None:
        self._prev = self._counters(*_linux_sources())

    def sample(self, v: Dict[str, Any], secs: float) -> None:
        stat, snmp = _linux_sources()
        self._gauges(v, stat, snmp)
        now = self._counters(stat, snmp)
        prev, self._prev = self._prev, now
        self._deltas(v, now, prev, secs)

    # ---------------------------------------------------------------- gauges

    def _gauges(self, v: Dict[str, Any], stat: Dict[str, Any], snmp: Dict[str, int]) -> None:
        count = _quiet(psutil.cpu_count) if psutil is not None else None
        v["cpu_count"] = count or os.cpu_count()

        meminfo = _meminfo() if IS_LINUX and psutil is None else {}
        if psutil is not None:
            vm = _quiet(psutil.virtual_memory)
            if vm is not None:
                v["host_mem_available_mb"] = round(vm.available / _MB, 1)
                cached = getattr(vm, "cached", 0)
                if cached:
                    v["host_mem_cached_mb"] = round(cached / _MB, 1)
            sw = _quiet(psutil.swap_memory)
            if sw is not None:
                v["swap_used_mb"] = round(sw.used / _MB, 1)
                v["swap_total_mb"] = round(sw.total / _MB, 1)
        elif meminfo:
            total, avail = meminfo.get("MemTotal"), meminfo.get("MemAvailable")
            if total and avail is not None:
                # psutil's definition: used = total - available.
                v["host_mem_total_mb"] = round(total / _MB, 1)
                v["host_mem_used_mb"] = round((total - avail) / _MB, 1)
                v["host_mem_percent"] = round((total - avail) / total * 100.0, 1)
            if avail is not None:
                v["host_mem_available_mb"] = round(avail / _MB, 1)
            cached = meminfo.get("Cached", 0) + meminfo.get("SReclaimable", 0)  # as psutil counts it
            if cached:
                v["host_mem_cached_mb"] = round(cached / _MB, 1)
            if "SwapTotal" in meminfo:
                v["swap_total_mb"] = round(meminfo["SwapTotal"] / _MB, 1)
                v["swap_used_mb"] = round((meminfo["SwapTotal"] - meminfo.get("SwapFree", 0)) / _MB, 1)

        if hasattr(os, "statvfs"):
            st = _quiet(lambda: os.statvfs(self._disk_path))
            if st is not None and st.f_files > 0:
                v["disk_inodes_percent"] = round((st.f_files - st.f_ffree) / st.f_files * 100.0, 1)

        pids = _quiet(psutil.pids) if psutil is not None else None
        if pids is not None:
            v["host_procs"] = len(pids)
        elif IS_LINUX:
            v["host_procs"] = _quiet(lambda: sum(1 for n in os.listdir("/proc") if n.isdigit()))
        if "procs_running" in stat:
            v["procs_running"] = stat["procs_running"]
        if "procs_blocked" in stat:
            v["procs_blocked"] = stat["procs_blocked"]

        if psutil is None and IS_LINUX:
            _self_status(v)

        if "CurrEstab" in snmp:
            v["tcp_established"] = snmp["CurrEstab"]

        if resource is not None:
            lim = _quiet(lambda: resource.getrlimit(resource.RLIMIT_NOFILE))
            if lim is not None and 0 < lim[0] < (1 << 40) and lim[0] != resource.RLIM_INFINITY:
                v["proc_max_fds"] = int(lim[0])

    # -------------------------------------------------------------- counters

    def _counters(self, stat: Dict[str, Any], snmp: Dict[str, int]) -> Dict[str, Any]:
        """Cumulative readings; a key is present only if its source worked."""
        c: Dict[str, Any] = {}

        cpu = None
        if psutil is not None:
            t = _quiet(psutil.cpu_times)
            if t is not None:
                cpu = t._asdict()
        if cpu is None and "cpu" in stat:
            cpu = stat["cpu"]
        if cpu is not None:
            c["cpu"] = cpu

        disks = None
        if psutil is not None:
            per = _quiet(lambda: psutil.disk_io_counters(perdisk=True))
            if per:
                disks = {name: (d.read_bytes, d.write_bytes, d.read_count, d.write_count,
                                (d.read_time + d.write_time) * _DISK_TIME_TO_MS, getattr(d, "busy_time", None))
                         for name, d in per.items()}
        if disks is None and IS_LINUX:
            disks = _quiet(_diskstats)
        if disks:
            whole = {n: d for n, d in disks.items() if not PARTITION.match(n)}
            c["disk"] = (
                sum(d[0] for d in whole.values()), sum(d[1] for d in whole.values()),
                sum(d[2] for d in whole.values()), sum(d[3] for d in whole.values()),
                sum(d[4] for d in whole.values()),
                {n: d[5] for n, d in whole.items() if d[5] is not None},
            )

        net = None
        if psutil is not None:
            io = _quiet(psutil.net_io_counters)
            if io is not None:
                net = (io.packets_recv, io.packets_sent, io.errin + io.errout, io.dropin + io.dropout,
                       io.bytes_recv, io.bytes_sent)
        if net is None and IS_LINUX:
            net = _quiet(_net_dev)
        if net is not None:
            c["net"] = net

        if "RetransSegs" in snmp:
            c["tcp_retrans"] = snmp["RetransSegs"]

        ctxt = None
        if psutil is not None:
            cs = _quiet(psutil.cpu_stats)
            if cs is not None and cs.ctx_switches > 0:
                ctxt = cs.ctx_switches
        if ctxt is None and "ctxt" in stat:
            ctxt = stat["ctxt"]
        if ctxt is not None:
            c["ctxt"] = ctxt

        pio = None
        if self._proc is not None and hasattr(self._proc, "io_counters"):
            io = _quiet(self._proc.io_counters)
            if io is not None:
                pio = (io.read_bytes, io.write_bytes)
        if pio is None and IS_LINUX:
            pio = _quiet(_self_io)
        if pio is not None:
            c["proc_io"] = pio

        if psutil is None and IS_LINUX:
            ticks = _quiet(_self_cpu_ticks)
            if ticks is not None:
                c["proc_cpu"] = ticks
        return c

    def _deltas(self, v: Dict[str, Any], now: Dict[str, Any], prev: Dict[str, Any], secs: float) -> None:
        if "cpu" in now and "cpu" in prev:
            a, b = now["cpu"], prev["cpu"]
            total = _cpu_total(a) - _cpu_total(b)
            if total > 0:
                def pct(*fields: str) -> float:
                    return round(100.0 * sum(a.get(f, 0.0) - b.get(f, 0.0) for f in fields) / total, 1)

                # Busy share as psutil.cpu_percent counts it (idle and iowait are idle).
                v.setdefault("host_cpu_percent", round(100.0 - pct("idle", "iowait"), 1))

                v["cpu_user_percent"] = pct("user", "nice")
                v["cpu_system_percent"] = pct("system", "irq", "softirq")
                if IS_LINUX:
                    v["cpu_iowait_percent"] = pct("iowait")
                    v["cpu_steal_percent"] = pct("steal")

        if "disk" in now and "disk" in prev:
            rb, wb, rc, wc, tm, busy = now["disk"]
            prb, pwb, prc, pwc, ptm, pbusy = prev["disk"]
            if rb >= prb and wb >= pwb:
                reads, writes = rc - prc, wc - pwc
                v["disk_read_kbps"] = round((rb - prb) / 1024.0 / secs, 1)
                v["disk_write_kbps"] = round((wb - pwb) / 1024.0 / secs, 1)
                v["disk_read_iops"] = round(reads / secs, 1)
                v["disk_write_iops"] = round(writes / secs, 1)
                if reads + writes > 0 and tm >= ptm:
                    v["disk_await_ms"] = round((tm - ptm) / (reads + writes), 2)
                if IS_LINUX and busy:
                    busiest = max((t - pbusy[n] for n, t in busy.items() if n in pbusy and t >= pbusy[n]),
                                  default=0)
                    v["disk_busy_percent"] = round(min(100.0, 100.0 * busiest / (secs * 1000.0)), 1)

        if "net" in now and "net" in prev:
            rx, tx, errs, drops, rxb, txb = now["net"]
            prx, ptx, perrs, pdrops, prxb, ptxb = prev["net"]
            if rxb >= prxb and txb >= ptxb:
                v.setdefault("net_rx_kbps", round((rxb - prxb) / 1024.0 / secs, 1))
                v.setdefault("net_tx_kbps", round((txb - ptxb) / 1024.0 / secs, 1))
            if rx >= prx and tx >= ptx:
                v["net_rx_pps"] = round((rx - prx) / secs, 1)
                v["net_tx_pps"] = round((tx - ptx) / secs, 1)
                if errs >= perrs:
                    v["net_errors"] = errs - perrs
                if drops >= pdrops:
                    v["net_drops"] = drops - pdrops

        if "tcp_retrans" in now and "tcp_retrans" in prev and now["tcp_retrans"] >= prev["tcp_retrans"]:
            v["tcp_retrans"] = now["tcp_retrans"] - prev["tcp_retrans"]

        if "ctxt" in now and "ctxt" in prev and now["ctxt"] >= prev["ctxt"]:
            v["ctx_switches_ps"] = round((now["ctxt"] - prev["ctxt"]) / secs, 1)

        if "proc_io" in now and "proc_io" in prev:
            (r, w), (pr, pw) = now["proc_io"], prev["proc_io"]
            if r >= pr and w >= pw:
                v["proc_read_kbps"] = round((r - pr) / 1024.0 / secs, 1)
                v["proc_write_kbps"] = round((w - pw) / 1024.0 / secs, 1)

        if "proc_cpu" in now and "proc_cpu" in prev and now["proc_cpu"] >= prev["proc_cpu"]:
            # % of one core, like psutil's Process.cpu_percent.
            v["proc_cpu_percent"] = round((now["proc_cpu"] - prev["proc_cpu"]) / _CLK_TCK / secs * 100.0, 1)


def _cpu_total(t: Dict[str, float]) -> float:
    # Linux counts guest time inside user/nice already (psutil does the same
    # subtraction for cpu_percent).
    return sum(t.values()) - t.get("guest", 0.0) - t.get("guest_nice", 0.0)


def _linux_sources() -> tuple:
    """/proc/stat and the TCP counters, read once per sample (empty elsewhere)."""
    if not IS_LINUX:
        return {}, {}
    return _proc_stat(), _quiet(_tcp_snmp) or {}


def _proc_stat() -> Dict[str, Any]:
    out: Dict[str, Any] = {}
    try:
        with open("/proc/stat") as f:
            for line in f:
                parts = line.split()
                if not parts:
                    continue
                key = parts[0]
                if key == "cpu":
                    out["cpu"] = {name: float(x) for name, x in zip(_PROC_STAT_CPU, parts[1:])}
                elif key in ("ctxt", "procs_running", "procs_blocked"):
                    out[key] = int(parts[1])
    except (OSError, ValueError):
        pass
    return out


def _meminfo() -> Dict[str, int]:
    out: Dict[str, int] = {}
    try:
        with open("/proc/meminfo") as f:
            for line in f:
                name, _, rest = line.partition(":")
                fields = rest.split()
                if fields:
                    out[name] = int(fields[0]) * (1024 if len(fields) > 1 and fields[1] == "kB" else 1)
    except (OSError, ValueError):
        pass
    return out


def _diskstats() -> Dict[str, tuple]:
    out: Dict[str, tuple] = {}
    with open("/proc/diskstats") as f:
        for line in f:
            p = line.split()
            if len(p) < 14:
                continue
            # reads, sectors read, ms reading, writes, sectors written, ms writing, ms doing I/O
            out[p[2]] = (int(p[5]) * 512, int(p[9]) * 512, int(p[3]), int(p[7]),
                         int(p[6]) + int(p[10]), int(p[12]))
    return out


def _net_dev() -> Optional[tuple]:
    rx = tx = errs = drops = rxb = txb = 0
    with open("/proc/net/dev") as f:
        for line in f:
            if ":" not in line:
                continue
            p = line.split(":", 1)[1].split()
            rxb += int(p[0])
            rx += int(p[1])
            txb += int(p[8])
            tx += int(p[9])
            errs += int(p[2]) + int(p[10])
            drops += int(p[3]) + int(p[11])
    return rx, tx, errs, drops, rxb, txb


def _tcp_snmp() -> Dict[str, int]:
    with open("/proc/net/snmp") as f:
        rows = [line.split() for line in f if line.startswith("Tcp:")]
    if len(rows) < 2:
        return {}
    return {k: int(x) for k, x in zip(rows[0][1:], rows[1][1:])}


def _self_io() -> Optional[tuple]:
    vals: Dict[str, int] = {}
    with open("/proc/self/io") as f:
        for line in f:
            k, _, x = line.partition(":")
            vals[k.strip()] = int(x)
    if "read_bytes" not in vals:
        return None
    return vals["read_bytes"], vals["write_bytes"]


def _self_cpu_ticks() -> int:
    """utime + stime of this process, in clock ticks."""
    with open("/proc/self/stat") as f:
        # Fields after the parenthesised name; utime and stime are fields 14 and 15.
        rest = f.read().rpartition(")")[2].split()
    return int(rest[11]) + int(rest[12])


def _self_status(v: Dict[str, Any]) -> None:
    """RSS, threads and open files of this process from /proc/self."""
    try:
        with open("/proc/self/status") as f:
            for line in f:
                if line.startswith("VmRSS:"):
                    v["proc_rss_mb"] = round(int(line.split()[1]) * 1024 / _MB, 1)
                elif line.startswith("Threads:"):
                    v["proc_threads"] = int(line.split()[1])
    except (OSError, ValueError, IndexError):
        pass
    fds = _quiet(lambda: len(os.listdir("/proc/self/fd")))
    if fds is not None:
        v["proc_open_fds"] = fds


def _quiet(fn: Any) -> Any:
    try:
        return fn()
    except Exception:
        return None
