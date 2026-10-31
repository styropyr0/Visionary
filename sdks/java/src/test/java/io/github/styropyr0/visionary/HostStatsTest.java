package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

/** The same fixtures as sdks/go/hoststats/testdata */
class HostStatsTest {

    static final String PROC_PRESSURE_CPU = "some avg10=12.50 avg60=8.25 avg300=3.10 total=123456789\n"
            + "full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n";
    static final String PROC_PRESSURE_MEMORY = "some avg10=4.20 avg60=2.10 avg300=0.80 total=9876543\n"
            + "full avg10=1.50 avg60=0.75 avg300=0.20 total=1234567\n";
    static final String PROC_PRESSURE_IO = "some avg10=30.00 avg60=22.40 avg300=10.00 total=55555555\n"
            + "full avg10=18.75 avg60=12.00 avg300=5.00 total=44444444\n";
    static final String CGROUP_CPU_PRESSURE = "some avg10=55.10 avg60=40.00 avg300=20.00 total=1000\n"
            + "full avg10=50.00 avg60=35.00 avg300=18.00 total=900\n";
    static final String CGROUP_MEMORY_PRESSURE = "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
            + "full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n";
    static final String CGROUP_IO_PRESSURE = "some avg10=1.00 avg60=0.50 avg300=0.10 total=10\n"
            + "full avg10=0.90 avg60=0.40 avg300=0.10 total=9\n";
    static final String VMSTAT = """
            nr_free_pages 1234567
            nr_dirty 512
            pgpgin 104857600
            pgpgout 209715200
            pswpin 1000
            pswpout 2000
            pgfault 50000000
            pgmajfault 20000
            pgsteal_kswapd 30000
            pgsteal_direct 5000
            pgsteal_khugepaged 0
            pgsteal_anon 1000
            pgsteal_file 34000
            pgscan_kswapd 60000
            pgscan_direct 9000
            pgscan_khugepaged 0
            pgscan_direct_throttle 7
            pgscan_anon 2000
            pgscan_file 67000
            oom_kill 3
            """;
    static final String MEMINFO = """
            MemTotal:       16303300 kB
            MemFree:         1048576 kB
            MemAvailable:    8388608 kB
            Buffers:          204800 kB
            Cached:          4194304 kB
            SwapCached:            0 kB
            Dirty:             10240 kB
            Writeback:          2048 kB
            Shmem:            524288 kB
            Slab:             819200 kB
            SReclaimable:     614400 kB
            CommitLimit:    12345678 kB
            Committed_AS:   20971520 kB
            HugePages_Total:       0
            """;

    @TempDir
    Path root;

    @Test
    void parsers() {
        HostStats.Pressure p = HostStats.parsePressure(PROC_PRESSURE_MEMORY);
        assertEquals(new HostStats.Pressure(4.2, 2.1, 1.5), p);
        assertNull(HostStats.parsePressure("garbage\n"));

        assertEquals(new HostStats.VmStat(50000000, 20000, 1000, 2000, 69000, 35000, 3L), HostStats.parseVmStat(VMSTAT));
        assertEquals(new HostStats.MemInfo(10240, 2048, 819200, 524288, 20971520, 12345678), HostStats.parseMemInfo(MEMINFO));
        assertNull(HostStats.parseMemInfo("MemTotal: 1 kB\n"));

        assertArrayEquals(new long[] {900, 12}, HostStats.parseCgroupMemStat("anon 123\npgfault 900\npgmajfault 12\n"));
        assertArrayEquals(new long[] {800, 7}, HostStats.parseCgroupMemStat("cache 1\npgfault 5\ntotal_pgfault 800\ntotal_pgmajfault 7\n"));
        assertArrayEquals(new long[] {14, 3}, HostStats.parseCgroupMemEvents("low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n"));
    }

    private void write(String rel, String text) throws IOException {
        Path p = root.resolve(rel);
        Files.createDirectories(p.getParent());
        Files.writeString(p, text);
    }

    private static Object get(Object[] row, String column) {
        return row[Table.SYSTEM_METRICS.indexOf(column)];
    }

    @Test
    void hostSample() throws IOException {
        write("proc/pressure/cpu", PROC_PRESSURE_CPU);
        write("proc/pressure/memory", PROC_PRESSURE_MEMORY);
        write("proc/pressure/io", PROC_PRESSURE_IO);
        write("proc/vmstat", VMSTAT);
        write("proc/meminfo", MEMINFO);
        HostStats h = new HostStats(root.resolve("proc"), false, null);
        h.prime();
        write("proc/vmstat", VMSTAT.replace("pgfault 50000000", "pgfault 50030000")
                .replace("pgmajfault 20000", "pgmajfault 20150")
                .replace("pswpin 1000", "pswpin 1300")
                .replace("pgscan_kswapd 60000", "pgscan_kswapd 61500")
                .replace("oom_kill 3", "oom_kill 4"));
        Object[] row = new Object[Table.SYSTEM_METRICS.columns.size()];
        h.sample(row, 15);
        assertEquals(12.5, get(row, "psi_cpu_some_avg10"));
        assertEquals(8.25, get(row, "psi_cpu_some_avg60"));
        assertEquals(4.2, get(row, "psi_mem_some_avg10"));
        assertEquals(1.5, get(row, "psi_mem_full_avg10"));
        assertEquals(22.4, get(row, "psi_io_some_avg60"));
        assertEquals(18.75, get(row, "psi_io_full_avg10"));
        assertEquals(1990.0, get(row, "minor_faults_ps"));
        assertEquals(10.0, get(row, "major_faults_ps"));
        assertEquals(20.0, get(row, "swap_in_ps"));
        assertEquals(0.0, get(row, "swap_out_ps"));
        assertEquals(100.0, get(row, "pages_scanned_ps"));
        assertEquals(0.0, get(row, "pages_stolen_ps"));
        assertEquals(1L, get(row, "host_oom_kills"));
        assertEquals(10.0, get(row, "mem_dirty_mb"));
        assertEquals(2.0, get(row, "mem_writeback_mb"));
        assertEquals(800.0, get(row, "mem_slab_mb"));
        assertEquals(512.0, get(row, "mem_shmem_mb"));
        assertEquals(20480.0, get(row, "mem_committed_mb"));
        assertEquals(12056.3, get(row, "mem_commit_limit_mb"));
    }

    @Test
    void containerPressureAndMemory() throws IOException {
        write("proc/self/cgroup", "0::/\n");
        write(".dockerenv", "");
        write("proc/pressure/cpu", PROC_PRESSURE_CPU);
        write("sys/fs/cgroup/cpu.stat", "usage_usec 1000\nnr_periods 0\nnr_throttled 0\n");
        write("sys/fs/cgroup/cpu.max", "max 100000\n");
        write("sys/fs/cgroup/memory.max", "max\n");
        write("sys/fs/cgroup/memory.current", "1048576\n");
        write("sys/fs/cgroup/memory.stat", "anon 123\npgfault 900\npgmajfault 12\n");
        write("sys/fs/cgroup/memory.events", "low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n");
        write("sys/fs/cgroup/cpu.pressure", CGROUP_CPU_PRESSURE);
        write("sys/fs/cgroup/memory.pressure", CGROUP_MEMORY_PRESSURE);
        write("sys/fs/cgroup/io.pressure", CGROUP_IO_PRESSURE);

        CgroupReader cg = CgroupReader.open(root, null);
        HostStats h = new HostStats(root.resolve("proc"), cg.inContainer(), cg.v2Dir());
        cg.prime(0);
        h.prime();
        write("sys/fs/cgroup/memory.stat", "anon 123\npgfault 1500\npgmajfault 42\n");
        write("sys/fs/cgroup/memory.events", "low 0\nhigh 20\nmax 4\noom 1\noom_kill 1\n");
        Object[] row = new Object[Table.SYSTEM_METRICS.columns.size()];
        cg.sample(row, 15_000_000_000L);
        h.sample(row, 15);
        assertEquals(55.1, get(row, "psi_cpu_some_avg10"));
        assertEquals(0.0, get(row, "psi_mem_some_avg10"));
        assertEquals(0.9, get(row, "psi_io_full_avg10"));
        assertEquals(38.0, get(row, "container_minor_faults_ps"));
        assertEquals(2.0, get(row, "container_major_faults_ps"));
        assertEquals(6L, get(row, "container_mem_high_events"));
        assertEquals(1L, get(row, "container_mem_max_events"));

        HostStats v1 = new HostStats(root.resolve("proc"), true, null);
        Object[] empty = new Object[Table.SYSTEM_METRICS.columns.size()];
        v1.sample(empty, 15);
        assertNull(get(empty, "psi_cpu_some_avg10"), "a container without cgroup v2 read the host's pressure");
    }

    @Test
    void d1SplitsWideRows() {
        String[] sql = D1Store.rowSql(Table.SYSTEM_METRICS);
        assertTrue(Table.SYSTEM_METRICS.columns.size() > D1Store.MAX_PARAMS);
        assertEquals(2, sql.length);
        assertEquals(D1Store.MAX_PARAMS, sql[0].split("NULLIF").length - 1);
        assertEquals(Table.SYSTEM_METRICS.columns.size() - D1Store.MAX_PARAMS, sql[1].split("NULLIF").length - 1);
        assertTrue(sql[1].startsWith("UPDATE system_metrics SET " + Table.SYSTEM_METRICS.columns.get(D1Store.MAX_PARAMS) + " = NULLIF(?, '')"));
        assertTrue(sql[1].endsWith("WHERE id = last_insert_rowid()"));
        assertEquals(1, D1Store.rowSql(Table.RELEASES).length);
    }
}
