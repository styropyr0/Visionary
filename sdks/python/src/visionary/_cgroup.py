"""The container_* system_metrics columns, from the process's own cgroup.

In a container these are the numbers that matter: the host_* columns describe
the whole machine underneath. Linux only (cgroup v2 or v1); the columns are
filled only in a container, or when the cgroup sets a CPU or memory limit (a
systemd unit with MemoryMax=, say). sdks/go/cgroup.go is the reference.
"""

from __future__ import annotations

import os
import time
from typing import Any, Dict, Optional, Tuple

from ._hoststats import parse_cgroup_io, parse_cgroup_mem_events, parse_cgroup_mem_stat
from ._node import IS_LINUX

_MB = float(1 << 20)

_CONTAINER_MARKS = ("docker", "kubepods", "containerd", "libpod", "lxc")
# Variables container platforms set in every container: Kubernetes, Railway,
# Render, Heroku, Cloud Run, ECS/Fargate and Azure Container Apps.
_CONTAINER_ENV = ("KUBERNETES_SERVICE_HOST", "RAILWAY_SERVICE_ID", "RENDER_SERVICE_ID", "DYNO", "K_SERVICE", "ECS_CONTAINER_METADATA_URI_V4", "CONTAINER_APP_NAME")


class CgroupReader:
    def __init__(self, root: str = "/") -> None:
        self._root = root
        self.v2 = False
        self.cpu_dir = ""  # v2: the cgroup; v1: the cpu controller's directory
        self.cpuacct_dir = ""  # v1: the cpuacct controller's directory
        self.mem_dir = ""
        self.pids_dir = ""
        self.io_dir = ""
        self.in_container = False
        self._last = 0.0
        self._usage = 0  # CPU time, ns
        self._periods = 0
        self._throttled = 0
        self._ooms = 0
        self._mem: Tuple[Optional[Tuple[int, int]], Optional[Tuple[int, int]]] = (None, None)
        self._io: Optional[Tuple[int, int]] = None
        self._primed = False

    @classmethod
    def find(cls, root: str = "/") -> Optional["CgroupReader"]:
        """The process's cgroup; None off Linux or when there is none to read.
        root is "/" except in tests."""
        if root == "/" and not IS_LINUX:
            return None
        c = cls(root)
        text = _read(c._path("/proc/self/cgroup"))
        if text is None:
            return None
        c.in_container = c._in_container(text)
        for line in text.strip().splitlines():
            f = line.split(":", 2)
            if len(f) != 3:
                continue
            ctrls, path = f[1].split(","), f[2]
            if f[0] == "0" and f[1] == "":
                d = c._cgroup_dir("/sys/fs/cgroup", path)
                if os.path.exists(_join(d, "cpu.stat")):
                    c.v2, c.cpu_dir, c.mem_dir, c.pids_dir, c.io_dir = True, d, d, d, d
                    break
            elif "memory" in ctrls:
                c.mem_dir = c._cgroup_dir("/sys/fs/cgroup/memory", path)
            elif "pids" in ctrls:
                c.pids_dir = c._cgroup_dir("/sys/fs/cgroup/pids", path)
            elif "blkio" in ctrls:
                c.io_dir = c._cgroup_dir("/sys/fs/cgroup/" + f[1], path)
            elif "cpu" in ctrls or "cpuacct" in ctrls:
                d = c._cgroup_dir("/sys/fs/cgroup/" + f[1], path)
                if "cpu" in ctrls:
                    c.cpu_dir = d
                if "cpuacct" in ctrls:
                    c.cpuacct_dir = d
        if not c.cpu_dir and not c.mem_dir:
            return None
        return c

    def prime(self) -> None:
        """The baseline the first sample's deltas start from."""
        self._usage, self._periods, self._throttled, self._ooms = self._counters()
        self._mem = self._read_mem()
        self._io = self._read_io()
        self._last, self._primed = time.monotonic(), True

    def sample(self, v: Dict[str, Any]) -> None:
        cpus, mem_limit = self._limits()
        if not self.in_container and cpus == 0 and mem_limit == 0:
            return
        now = time.monotonic()
        usage, periods, throttled, ooms = self._counters()
        secs = now - self._last
        # A counter that went down (the cgroup was reset) leaves the deltas NULL.
        if self._primed and secs > 0 and usage >= self._usage:
            v["container_cpu_cores"] = round((usage - self._usage) / 1e9 / secs, 3)
            if periods > self._periods and throttled >= self._throttled:
                v["container_cpu_throttled_percent"] = round(
                    100.0 * (throttled - self._throttled) / (periods - self._periods), 1)
            elif cpus > 0:
                v["container_cpu_throttled_percent"] = 0.0
            if ooms >= self._ooms:
                v["container_oom_kills"] = ooms - self._ooms
            self._sample_mem(v, secs)
            io = self._read_io()
            if io is not None and self._io is not None and io[0] >= self._io[0] and io[1] >= self._io[1]:
                v["container_read_kbps"] = round((io[0] - self._io[0]) / 1024 / secs, 1)
                v["container_write_kbps"] = round((io[1] - self._io[1]) / 1024 / secs, 1)
        self._io = self._read_io()
        self._usage, self._periods, self._throttled, self._ooms = usage, periods, throttled, ooms
        self._last, self._primed = now, True
        if cpus > 0:
            v["container_cpu_limit"] = round(cpus, 2)
        used = self._mem_used()
        if used is not None:
            v["container_mem_used_mb"] = round(used / _MB, 1)
        if mem_limit > 0:
            v["container_mem_limit_mb"] = round(mem_limit / _MB, 1)
        if self.pids_dir:
            n = _uint(_join(self.pids_dir, "pids.current"))
            if n is not None:
                v["container_pids"] = n
            n = _uint(_join(self.pids_dir, "pids.max"))  # "max" is not a number
            if n is not None:
                v["container_pids_max"] = n

    def _sample_mem(self, v: Dict[str, Any], secs: float) -> None:
        """The cgroup's fault rates and memory limit hits"""
        (faults, events), (pf, pe) = self._read_mem(), self._mem
        self._mem = (faults, events)
        if faults is not None and pf is not None:
            (f, m), (f0, m0) = faults, pf
            if f >= m and f0 >= m0 and m >= m0 and f - m >= f0 - m0:
                v["container_minor_faults_ps"] = round(((f - m) - (f0 - m0)) / secs, 1)
                v["container_major_faults_ps"] = round((m - m0) / secs, 1)
        if events is not None and pe is not None and events[0] >= pe[0] and events[1] >= pe[1]:
            v["container_mem_high_events"] = events[0] - pe[0]
            v["container_mem_max_events"] = events[1] - pe[1]

    # --------------------------------------------------------------- readers

    def _read_mem(self) -> Tuple[Optional[Tuple[int, int]], Optional[Tuple[int, int]]]:
        if not self.mem_dir:
            return None, None
        st = _read(_join(self.mem_dir, "memory.stat"))
        faults = parse_cgroup_mem_stat(st) if st is not None else None
        events = None
        if self.v2:
            ev = _read(_join(self.mem_dir, "memory.events"))
            events = parse_cgroup_mem_events(ev) if ev is not None else None
        return faults, events

    def _read_io(self) -> Optional[Tuple[int, int]]:
        """The bytes the cgroup read from and wrote to its block devices"""
        if not self.io_dir:
            return None
        text = _read(_join(self.io_dir, "io.stat" if self.v2 else "blkio.throttle.io_service_bytes"))
        return parse_cgroup_io(text) if text is not None else None

    def _limits(self) -> Tuple[float, int]:
        """The CPU limit in cores and the memory limit in bytes, 0 when unlimited."""
        cpus, mem = 0.0, 0
        if self.v2:
            f = (_read(_join(self.cpu_dir, "cpu.max")) or "").split()
            if len(f) == 2 and f[0] != "max":
                q, p = _float(f[0]), _float(f[1])
                if q is not None and p:
                    cpus = q / p
            return cpus, _uint(_join(self.mem_dir, "memory.max")) or 0  # "max": 0
        if self.cpu_dir:
            q = _float((_read(_join(self.cpu_dir, "cpu.cfs_quota_us")) or "").strip())
            p = _uint(_join(self.cpu_dir, "cpu.cfs_period_us"))
            if q is not None and q > 0 and p:
                cpus = q / p
        if self.mem_dir:
            # v1 writes a page-rounded maximum int64 for no limit.
            m = _uint(_join(self.mem_dir, "memory.limit_in_bytes"))
            if m is not None and m < 1 << 62:
                mem = m
        return cpus, mem

    def _counters(self) -> Tuple[int, int, int, int]:
        """Cumulative CPU time (ns), CFS periods, throttled periods and OOM kills."""
        if self.v2:
            st = _kv(_join(self.cpu_dir, "cpu.stat"))
            ev = _kv(_join(self.mem_dir, "memory.events"))
            return (st.get("usage_usec", 0) * 1000, st.get("nr_periods", 0),
                    st.get("nr_throttled", 0), ev.get("oom_kill", 0))
        usage = periods = throttled = ooms = 0
        if self.cpuacct_dir:
            usage = _uint(_join(self.cpuacct_dir, "cpuacct.usage")) or 0
        if self.cpu_dir:
            st = _kv(_join(self.cpu_dir, "cpu.stat"))
            periods, throttled = st.get("nr_periods", 0), st.get("nr_throttled", 0)
        if self.mem_dir:
            ooms = _kv(_join(self.mem_dir, "memory.oom_control")).get("oom_kill", 0)
        return usage, periods, throttled, ooms

    def _mem_used(self) -> Optional[int]:
        """Memory in use without the inactive page cache, as docker stats counts it."""
        name, key = ("memory.current", "inactive_file") if self.v2 else ("memory.usage_in_bytes", "total_inactive_file")
        used = _uint(_join(self.mem_dir, name))
        if used is None:
            return None
        inactive = _kv(_join(self.mem_dir, "memory.stat")).get(key)
        if inactive is not None and inactive < used:
            used -= inactive
        return used

    # ----------------------------------------------------------------- paths

    def _path(self, p: str) -> str:
        return p if self._root == "/" else _join(self._root, p)

    def _cgroup_dir(self, mount: str, path: str) -> str:
        """The cgroup's directory under a controller's mount. Inside a
        container's cgroup namespace the path is "/", and with a private mount
        the host path does not exist below it, so the mount itself is the cgroup."""
        mount = self._path(mount)
        d = _join(mount, path)
        return d if os.path.exists(d) else mount

    def _in_container(self, cgroups: str) -> bool:
        """Tells a container from a process on the host by the runtimes' marker
        files, the platforms' variables and the cgroup path."""
        if (os.path.exists(self._path("/.dockerenv")) or os.path.exists(self._path("/run/.containerenv"))
                or any(os.environ.get(k) for k in _CONTAINER_ENV)):
            return True
        return any(k in cgroups for k in _CONTAINER_MARKS)


def _join(a: str, b: str) -> str:
    b = b.strip("/")
    return a.rstrip("/") + "/" + b if b else a


def _read(p: str) -> Optional[str]:
    try:
        with open(p) as f:
            return f.read()
    except OSError:
        return None


def _uint(p: str) -> Optional[int]:
    s = (_read(p) or "").strip()
    return int(s) if s.isdigit() else None


def _float(s: str) -> Optional[float]:
    try:
        return float(s)
    except ValueError:
        return None


def _kv(p: str) -> Dict[str, int]:
    """'key value' lines (cpu.stat, memory.stat, memory.events)."""
    out: Dict[str, int] = {}
    for line in (_read(p) or "").splitlines():
        f = line.split()
        if len(f) == 2 and f[1].isdigit():
            out[f[0]] = int(f[1])
    return out
