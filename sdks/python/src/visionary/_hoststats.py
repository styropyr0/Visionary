"""Pressure and virtual memory columns of system_metrics, from small files
under /proc. Linux only, sdks/go/hoststats is the reference
"""

from __future__ import annotations

from typing import Any, Dict, NamedTuple, Optional, Tuple

from ._node import IS_LINUX


class Pressure(NamedTuple):
    some_avg10: float
    some_avg60: float
    full_avg10: Optional[float]


class VMStat(NamedTuple):
    pgfault: int
    pgmajfault: int
    pswpin: int
    pswpout: int
    pgscan: int
    pgsteal: int
    oom_kill: Optional[int]


def parse_pressure(text: str) -> Optional[Pressure]:
    """'some avg10=0.12 avg60=...' and the optional 'full' line"""
    some = full = None
    for line in text.splitlines():
        kind, _, rest = line.partition(" ")
        fields = dict(f.split("=", 1) for f in rest.split() if "=" in f)
        try:
            a10, a60 = float(fields["avg10"]), float(fields["avg60"])
        except (KeyError, ValueError):
            continue
        if kind == "some":
            some = (a10, a60)
        elif kind == "full":
            full = a10
    if some is None:
        return None
    return Pressure(some[0], some[1], full)


def _each_kv(text: str, sep: str):
    for line in text.splitlines():
        k, found, v = line.partition(sep)
        if not found or not k:
            continue
        v = v.strip().split(" ", 1)[0]
        if v.isdigit():
            yield k, int(v)


def parse_vmstat(text: str) -> Optional[VMStat]:
    """Reclaim sums the kswapd, direct and khugepaged counters but not
    pgscan_anon or pgscan_file, which split the same pages by type"""
    s = {"pgfault": None, "pgmajfault": 0, "pswpin": 0, "pswpout": 0, "pgscan": 0, "pgsteal": 0, "oom_kill": None}
    for k, v in _each_kv(text, " "):
        if k in ("pgfault", "pgmajfault", "pswpin", "pswpout", "oom_kill"):
            s[k] = v
        elif k == "pgscan_direct_throttle":
            continue
        elif k.startswith(("pgscan_kswapd", "pgscan_direct", "pgscan_khugepaged")):
            s["pgscan"] += v
        elif k.startswith(("pgsteal_kswapd", "pgsteal_direct", "pgsteal_khugepaged")):
            s["pgsteal"] += v
    if s["pgfault"] is None:
        return None
    return VMStat(**s)


_MEMINFO = {"Dirty": "dirty", "Writeback": "writeback", "Slab": "slab", "Shmem": "shmem",
            "Committed_AS": "committed", "CommitLimit": "commit_limit"}


def parse_meminfo(text: str) -> Optional[Dict[str, int]]:
    """The fields in kB, None unless Dirty, Committed_AS and CommitLimit are there"""
    m: Dict[str, int] = {}
    for k, v in _each_kv(text, ":"):
        if k in _MEMINFO:
            m[_MEMINFO[k]] = v
    if not all(k in m for k in ("dirty", "committed", "commit_limit")):
        return None
    return {k: m.get(k, 0) for k in _MEMINFO.values()}


def parse_cgroup_mem_stat(text: str) -> Optional[Tuple[int, int]]:
    """Faults and major faults of memory.stat, v2 (pgfault) or v1 (total_pgfault)"""
    faults, major = None, 0
    for k, v in _each_kv(text, " "):
        if k in ("pgfault", "total_pgfault"):
            faults = v
        elif k in ("pgmajfault", "total_pgmajfault"):
            major = v
    return None if faults is None else (faults, major)


def parse_cgroup_mem_events(text: str) -> Optional[Tuple[int, int]]:
    """high and max of a v2 memory.events"""
    ev = {k: v for k, v in _each_kv(text, " ") if k in ("high", "max")}
    return (ev["high"], ev["max"]) if len(ev) == 2 else None


class HostStats:
    """Keeps the /proc/vmstat counters the per-second columns are deltas of.
    In a container pressure comes from cgroup_dir, the cgroup v2 directory,
    and with none it is left out, since /proc/pressure is the whole machine's"""

    def __init__(self, proc: str = "/proc", container: bool = False, cgroup_dir: str = "",
                 linux: bool = IS_LINUX) -> None:
        self._proc = proc.rstrip("/")
        self._container = container
        self._cgroup_dir = cgroup_dir.rstrip("/")
        self._linux = linux
        self._vm: Optional[VMStat] = None

    def prime(self) -> None:
        if self._linux:
            self._vm = self._read_vmstat()

    def sample(self, v: Dict[str, Any], secs: float) -> None:
        if not self._linux or secs <= 0:
            return
        self._pressure(v)
        self._vmstat(v, secs)
        text = _read(self._proc + "/meminfo")
        m = parse_meminfo(text) if text is not None else None
        if m is not None:
            for k in _MEMINFO.values():
                v["mem_%s_mb" % k] = round(m[k] / 1024, 1)

    def _pressure(self, v: Dict[str, Any]) -> None:
        if self._container:
            if not self._cgroup_dir:
                return
            paths = [self._cgroup_dir + "/" + n + ".pressure" for n in ("cpu", "memory", "io")]
        else:
            paths = [self._proc + "/pressure/" + n for n in ("cpu", "memory", "io")]
        for col, path in zip(("cpu", "mem", "io"), paths):
            text = _read(path)
            p = parse_pressure(text) if text is not None else None
            if p is None:
                continue
            v["psi_%s_some_avg10" % col], v["psi_%s_some_avg60" % col] = p.some_avg10, p.some_avg60
            if col != "cpu" and p.full_avg10 is not None:
                v["psi_%s_full_avg10" % col] = p.full_avg10

    def _read_vmstat(self) -> Optional[VMStat]:
        text = _read(self._proc + "/vmstat")
        return parse_vmstat(text) if text is not None else None

    def _vmstat(self, v: Dict[str, Any], secs: float) -> None:
        now, p = self._read_vmstat(), self._vm
        self._vm = now
        if now is None or p is None:
            return

        def rate(col: str, a: int, b: int) -> None:
            if a >= b:
                v[col] = round((a - b) / secs, 1)

        if now.pgfault >= now.pgmajfault and p.pgfault >= p.pgmajfault:
            rate("minor_faults_ps", now.pgfault - now.pgmajfault, p.pgfault - p.pgmajfault)
        rate("major_faults_ps", now.pgmajfault, p.pgmajfault)
        rate("swap_in_ps", now.pswpin, p.pswpin)
        rate("swap_out_ps", now.pswpout, p.pswpout)
        rate("pages_scanned_ps", now.pgscan, p.pgscan)
        rate("pages_stolen_ps", now.pgsteal, p.pgsteal)
        if now.oom_kill is not None and p.oom_kill is not None and now.oom_kill >= p.oom_kill:
            v["host_oom_kills"] = now.oom_kill - p.oom_kill


def _read(p: str) -> Optional[str]:
    try:
        with open(p) as f:
            return f.read()
    except OSError:
        return None
