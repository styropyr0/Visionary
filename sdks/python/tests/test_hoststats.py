"""Pressure, virtual memory and container memory columns, read from a fake
Linux root with the Go SDK's fixture text

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import os
import shutil
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from visionary._cgroup import CgroupReader  # noqa: E402
from visionary._hoststats import (  # noqa: E402
    HostStats, VMStat, parse_cgroup_mem_events, parse_cgroup_mem_stat, parse_meminfo, parse_pressure, parse_vmstat,
)
from visionary._storage import SYSTEM_COLUMNS, D1Backend, schema_statements  # noqa: E402

FIXTURES = {
    "proc/pressure/cpu": "some avg10=12.50 avg60=8.25 avg300=3.10 total=123456789\n"
                         "full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
    "proc/pressure/memory": "some avg10=4.20 avg60=2.10 avg300=0.80 total=9876543\n"
                            "full avg10=1.50 avg60=0.75 avg300=0.20 total=1234567\n",
    "proc/pressure/io": "some avg10=30.00 avg60=22.40 avg300=10.00 total=55555555\n"
                        "full avg10=18.75 avg60=12.00 avg300=5.00 total=44444444\n",
    "cgroup/cpu.pressure": "some avg10=55.10 avg60=40.00 avg300=20.00 total=1000\n"
                           "full avg10=50.00 avg60=35.00 avg300=18.00 total=900\n",
    "cgroup/memory.pressure": "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
                              "full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
    "cgroup/io.pressure": "some avg10=1.00 avg60=0.50 avg300=0.10 total=10\n"
                          "full avg10=0.90 avg60=0.40 avg300=0.10 total=9\n",
    "proc/vmstat": "nr_free_pages 1234567\nnr_dirty 512\npgpgin 104857600\npgpgout 209715200\n"
                   "pswpin 1000\npswpout 2000\npgfault 50000000\npgmajfault 20000\n"
                   "pgsteal_kswapd 30000\npgsteal_direct 5000\npgsteal_khugepaged 0\npgsteal_anon 1000\n"
                   "pgsteal_file 34000\npgscan_kswapd 60000\npgscan_direct 9000\npgscan_khugepaged 0\n"
                   "pgscan_direct_throttle 7\npgscan_anon 2000\npgscan_file 67000\noom_kill 3\n",
    "proc/meminfo": "MemTotal:       16303300 kB\nMemFree:         1048576 kB\nMemAvailable:    8388608 kB\n"
                    "Buffers:          204800 kB\nCached:          4194304 kB\nSwapCached:            0 kB\n"
                    "Dirty:             10240 kB\nWriteback:          2048 kB\nShmem:            524288 kB\n"
                    "Slab:             819200 kB\nSReclaimable:     614400 kB\nCommitLimit:    12345678 kB\n"
                    "Committed_AS:   20971520 kB\nHugePages_Total:       0\n",
}


def write(root, rel, text):
    p = os.path.join(root, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w", newline="\n") as f:
        f.write(text)


def replace(root, rel, old, new):
    with open(os.path.join(root, rel)) as f:
        text = f.read()
    assert old in text, old
    write(root, rel, text.replace(old, new, 1))


class FixtureRoot(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        for rel, text in FIXTURES.items():
            write(self.root, rel, text)
        self.proc = os.path.join(self.root, "proc")


class ParserTest(FixtureRoot):
    def test_parsers(self):
        p = parse_pressure(FIXTURES["proc/pressure/memory"])
        self.assertEqual((p.some_avg10, p.some_avg60, p.full_avg10), (4.2, 2.1, 1.5))
        self.assertIsNone(parse_pressure("garbage\n"))

        self.assertEqual(parse_vmstat(FIXTURES["proc/vmstat"]), VMStat(
            pgfault=50000000, pgmajfault=20000, pswpin=1000, pswpout=2000, pgscan=69000, pgsteal=35000, oom_kill=3))

        self.assertEqual(parse_meminfo(FIXTURES["proc/meminfo"]), {
            "dirty": 10240, "writeback": 2048, "slab": 819200, "shmem": 524288,
            "committed": 20971520, "commit_limit": 12345678})
        self.assertIsNone(parse_meminfo("MemTotal: 1 kB\nDirty: 2 kB\n"))

        self.assertEqual(parse_cgroup_mem_stat("anon 123\npgfault 900\npgmajfault 12\n"), (900, 12))
        self.assertEqual(parse_cgroup_mem_stat("cache 1\npgfault 5\ntotal_pgfault 800\ntotal_pgmajfault 7\n"), (800, 7))
        self.assertEqual(parse_cgroup_mem_events("low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n"), (14, 3))
        self.assertIsNone(parse_cgroup_mem_events("low 0\nhigh 14\n"))


class HostStatsTest(FixtureRoot):
    def test_host(self):
        s = HostStats(proc=self.proc, linux=True)
        s.prime()
        replace(self.root, "proc/vmstat", "pgfault 50000000", "pgfault 50030000")
        replace(self.root, "proc/vmstat", "pgmajfault 20000", "pgmajfault 20150")
        replace(self.root, "proc/vmstat", "pswpin 1000", "pswpin 1300")
        replace(self.root, "proc/vmstat", "pgscan_kswapd 60000", "pgscan_kswapd 61500")
        replace(self.root, "proc/vmstat", "oom_kill 3", "oom_kill 4")
        v = {}
        s.sample(v, 15)
        self.assertEqual(v, {
            "psi_cpu_some_avg10": 12.5, "psi_cpu_some_avg60": 8.25, "psi_mem_some_avg10": 4.2,
            "psi_mem_some_avg60": 2.1, "psi_mem_full_avg10": 1.5,
            "psi_io_some_avg10": 30.0, "psi_io_some_avg60": 22.4, "psi_io_full_avg10": 18.75,
            "minor_faults_ps": 1990.0, "major_faults_ps": 10.0, "swap_in_ps": 20.0, "swap_out_ps": 0.0,
            "pages_scanned_ps": 100.0, "pages_stolen_ps": 0.0, "host_oom_kills": 1,
            "mem_dirty_mb": 10.0, "mem_writeback_mb": 2.0, "mem_slab_mb": 800.0, "mem_shmem_mb": 512.0,
            "mem_committed_mb": 20480.0, "mem_commit_limit_mb": 12056.3,
        })
        self.assertIsInstance(v["host_oom_kills"], int)

    def test_counter_reset_is_null(self):
        s = HostStats(proc=self.proc, linux=True)
        s.prime()
        replace(self.root, "proc/vmstat", "pswpin 1000", "pswpin 10")
        v = {}
        s.sample(v, 15)
        self.assertNotIn("swap_in_ps", v)
        self.assertEqual(v["swap_out_ps"], 0.0)

    def test_container_reads_its_cgroup(self):
        s = HostStats(proc=self.proc, container=True, cgroup_dir=os.path.join(self.root, "cgroup"), linux=True)
        s.prime()
        v = {}
        s.sample(v, 15)
        self.assertEqual((v["psi_cpu_some_avg10"], v["psi_mem_some_avg10"], v["psi_io_full_avg10"]), (55.1, 0.0, 0.9))

        s = HostStats(proc=self.proc, container=True, linux=True)
        s.prime()
        v = {}
        s.sample(v, 15)
        self.assertNotIn("psi_cpu_some_avg10", v)
        self.assertIn("minor_faults_ps", v)

    def test_off_linux_reads_nothing(self):
        s = HostStats(proc=self.proc, linux=False)
        s.prime()
        v = {}
        s.sample(v, 15)
        self.assertEqual(v, {})


class CgroupMemTest(FixtureRoot):
    def test_faults_and_events(self):
        cg = self.root + "/sys/fs/cgroup"
        write(self.root, "proc/self/cgroup", "0::/\n")
        write(self.root, ".dockerenv", "")
        write(self.root, "sys/fs/cgroup/cpu.stat", "usage_usec 1000\nnr_periods 0\nnr_throttled 0\n")
        write(self.root, "sys/fs/cgroup/cpu.max", "max 100000\n")
        write(self.root, "sys/fs/cgroup/memory.max", "max\n")
        write(self.root, "sys/fs/cgroup/memory.current", "1048576\n")
        write(self.root, "sys/fs/cgroup/memory.stat", "anon 123\npgfault 900\npgmajfault 12\n")
        write(self.root, "sys/fs/cgroup/memory.events", "low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n")
        c = CgroupReader.find(self.root)
        self.assertTrue(c.v2 and c.in_container)
        self.assertEqual(c.cpu_dir, cg)
        c.prime()
        c._last -= 10
        write(self.root, "sys/fs/cgroup/memory.stat", "anon 123\npgfault 1920\npgmajfault 32\n")
        write(self.root, "sys/fs/cgroup/memory.events", "low 0\nhigh 20\nmax 4\noom 1\noom_kill 1\n")
        v = {}
        c.sample(v)
        self.assertAlmostEqual(v["container_minor_faults_ps"], 100.0, delta=1)
        self.assertAlmostEqual(v["container_major_faults_ps"], 2.0, delta=0.1)
        self.assertEqual((v["container_mem_high_events"], v["container_mem_max_events"]), (6, 1))

        write(self.root, "sys/fs/cgroup/memory.events", "low 0\nhigh 1\nmax 4\n")
        v = {}
        c.sample(v)
        self.assertNotIn("container_mem_high_events", v)


class D1SplitTest(unittest.TestCase):
    def test_wide_row_is_insert_then_update(self):
        class FakeD1:
            calls = []

            def batch(self, statements):
                self.calls.append(statements)

        client = FakeD1()
        row = tuple(range(len(SYSTEM_COLUMNS)))
        D1Backend(client).insert_many([("system", [row, row])])
        stmts = client.calls[0]
        self.assertEqual(len(stmts), 4)
        (ins, ins_args), (upd, upd_args) = stmts[0], stmts[1]
        self.assertEqual(len(ins_args), 100)
        self.assertEqual(ins.count("?"), 100)
        self.assertTrue(upd.startswith("UPDATE system_metrics SET container_mem_high_events = NULLIF(?, '')"))
        self.assertTrue(upd.endswith("WHERE id = last_insert_rowid()"))
        self.assertEqual(upd_args, [str(i) for i in range(100, len(SYSTEM_COLUMNS))])

    def test_schema_has_every_column(self):
        db = sqlite3.connect(":memory:")
        for s in schema_statements("sqlite", "system"):
            db.execute(s)
        names = [r[1] for r in db.execute("PRAGMA table_info(system_metrics)")][1:]
        self.assertEqual(names, SYSTEM_COLUMNS)


if __name__ == "__main__":
    unittest.main()
