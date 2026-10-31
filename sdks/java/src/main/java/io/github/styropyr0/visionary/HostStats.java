package io.github.styropyr0.visionary;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.function.BiConsumer;

/**
 * Pressure stall information and virtual memory counters (Linux only, else
 * NULL). In a container pressure comes from the cgroup v2 directory, since
 * {@code /proc/pressure} is the whole machine's. {@code sdks/go/hoststats} is the
 * reference
 */
final class HostStats {

    record Pressure(double someAvg10, double someAvg60, Double fullAvg10) {
    }

    record VmStat(long pgFault, long pgMajFault, long pswpIn, long pswpOut, long pgScan, long pgSteal, Long oomKill) {
    }

    /** The /proc/meminfo fields the columns use, in kB */
    record MemInfo(long dirty, long writeback, long slab, long shmem, long committed, long commitLimit) {
    }

    private final Path proc;
    private final boolean container;
    private final Path cgroupDir;
    private VmStat vm;

    HostStats(Path proc, boolean container, Path cgroupDir) {
        this.proc = proc;
        this.container = container;
        this.cgroupDir = cgroupDir;
    }

    /** Null off Linux */
    static HostStats open(CgroupReader cgroup) {
        if (!System.getProperty("os.name", "").startsWith("Linux")) {
            return null;
        }
        boolean container = cgroup != null && cgroup.inContainer();
        return new HostStats(Path.of("/proc"), container, cgroup == null ? null : cgroup.v2Dir());
    }

    void prime() {
        vm = vmStat();
    }

    void sample(Object[] row, double secs) {
        if (secs <= 0) {
            return;
        }
        samplePressure(row);
        sampleVm(row, secs);
    }

    private void samplePressure(Object[] row) {
        Path dir = proc.resolve("pressure");
        String suffix = "";
        if (container) {
            if (cgroupDir == null) {
                return;
            }
            dir = cgroupDir;
            suffix = ".pressure";
        }
        Pressure cpu = parsePressure(read(dir.resolve("cpu" + suffix)));
        if (cpu != null) {
            set(row, "psi_cpu_some_avg10", cpu.someAvg10());
            set(row, "psi_cpu_some_avg60", cpu.someAvg60());
        }
        Pressure mem = parsePressure(read(dir.resolve("memory" + suffix)));
        if (mem != null) {
            set(row, "psi_mem_some_avg10", mem.someAvg10());
            set(row, "psi_mem_some_avg60", mem.someAvg60());
            set(row, "psi_mem_full_avg10", mem.fullAvg10());
        }
        Pressure io = parsePressure(read(dir.resolve("io" + suffix)));
        if (io != null) {
            set(row, "psi_io_some_avg10", io.someAvg10());
            set(row, "psi_io_some_avg60", io.someAvg60());
            set(row, "psi_io_full_avg10", io.fullAvg10());
        }
    }

    private VmStat vmStat() {
        return parseVmStat(read(proc.resolve("vmstat")));
    }

    private void sampleVm(Object[] row, double secs) {
        VmStat now = vmStat();
        VmStat p = vm;
        vm = now;
        if (now != null && p != null) {
            if (now.pgFault() >= now.pgMajFault() && p.pgFault() >= p.pgMajFault()) {
                rate(row, "minor_faults_ps", now.pgFault() - now.pgMajFault(), p.pgFault() - p.pgMajFault(), secs);
            }
            rate(row, "major_faults_ps", now.pgMajFault(), p.pgMajFault(), secs);
            rate(row, "swap_in_ps", now.pswpIn(), p.pswpIn(), secs);
            rate(row, "swap_out_ps", now.pswpOut(), p.pswpOut(), secs);
            rate(row, "pages_scanned_ps", now.pgScan(), p.pgScan(), secs);
            rate(row, "pages_stolen_ps", now.pgSteal(), p.pgSteal(), secs);
            if (now.oomKill() != null && p.oomKill() != null && now.oomKill() >= p.oomKill()) {
                set(row, "host_oom_kills", now.oomKill() - p.oomKill());
            }
        }
        MemInfo m = parseMemInfo(read(proc.resolve("meminfo")));
        if (m != null) {
            set(row, "mem_dirty_mb", mb(m.dirty()));
            set(row, "mem_writeback_mb", mb(m.writeback()));
            set(row, "mem_slab_mb", mb(m.slab()));
            set(row, "mem_shmem_mb", mb(m.shmem()));
            set(row, "mem_committed_mb", mb(m.committed()));
            set(row, "mem_commit_limit_mb", mb(m.commitLimit()));
        }
    }

    private static void rate(Object[] row, String column, long now, long prev, double secs) {
        if (now >= prev) {
            set(row, column, Values.round((now - prev) / secs, 1));
        }
    }

    private static double mb(long kb) {
        return Values.round(kb / 1024.0, 1);
    }

    /** "some avg10=0.12 avg60=..." and the optional "full" line, null without a some line */
    static Pressure parsePressure(String text) {
        if (text == null) {
            return null;
        }
        Double some10 = null;
        Double some60 = null;
        Double full10 = null;
        for (String line : text.split("\n")) {
            int sp = line.indexOf(' ');
            if (sp < 0) {
                continue;
            }
            String rest = line.substring(sp + 1);
            Double a10 = pressureField(rest, "avg10=");
            Double a60 = pressureField(rest, "avg60=");
            if (a10 == null || a60 == null) {
                continue;
            }
            switch (line.substring(0, sp)) {
                case "some" -> {
                    some10 = a10;
                    some60 = a60;
                }
                case "full" -> full10 = a10;
                default -> {
                }
            }
        }
        return some10 == null ? null : new Pressure(some10, some60, full10);
    }

    private static Double pressureField(String s, String key) {
        int i = s.indexOf(key);
        if (i < 0) {
            return null;
        }
        String v = s.substring(i + key.length());
        int j = v.indexOf(' ');
        if (j >= 0) {
            v = v.substring(0, j);
        }
        try {
            return Double.parseDouble(v.strip());
        } catch (NumberFormatException e) {
            return null;
        }
    }

    /**
     * Sums reclaim over kswapd, direct and khugepaged scans, but not pgscan_anon or
     * pgscan_file, which split the same pages by type. Null without pgfault
     */
    static VmStat parseVmStat(String text) {
        long[] v = new long[6];
        Long[] oom = {null};
        boolean[] ok = {false};
        eachKv(text, ' ', (k, n) -> {
            switch (k) {
                case "pgfault" -> {
                    v[0] = n;
                    ok[0] = true;
                }
                case "pgmajfault" -> v[1] = n;
                case "pswpin" -> v[2] = n;
                case "pswpout" -> v[3] = n;
                case "oom_kill" -> oom[0] = n;
                case "pgscan_direct_throttle" -> {
                }
                default -> {
                    if (k.startsWith("pgscan_kswapd") || k.startsWith("pgscan_direct") || k.startsWith("pgscan_khugepaged")) {
                        v[4] += n;
                    } else if (k.startsWith("pgsteal_kswapd") || k.startsWith("pgsteal_direct") || k.startsWith("pgsteal_khugepaged")) {
                        v[5] += n;
                    }
                }
            }
        });
        return ok[0] ? new VmStat(v[0], v[1], v[2], v[3], v[4], v[5], oom[0]) : null;
    }

    /** Null unless Dirty, Committed_AS and CommitLimit are all there */
    static MemInfo parseMemInfo(String text) {
        long[] v = new long[6];
        int[] found = {0};
        eachKv(text, ':', (k, n) -> {
            switch (k) {
                case "Dirty" -> {
                    v[0] = n;
                    found[0] |= 1;
                }
                case "Writeback" -> v[1] = n;
                case "Slab" -> v[2] = n;
                case "Shmem" -> v[3] = n;
                case "Committed_AS" -> {
                    v[4] = n;
                    found[0] |= 2;
                }
                case "CommitLimit" -> {
                    v[5] = n;
                    found[0] |= 4;
                }
                default -> {
                }
            }
        });
        return found[0] == 7 ? new MemInfo(v[0], v[1], v[2], v[3], v[4], v[5]) : null;
    }

    /** Faults and major faults of a cgroup's memory.stat, v2 or v1, null without them */
    static long[] parseCgroupMemStat(String text) {
        long[] out = new long[2];
        boolean[] ok = {false};
        eachKv(text, ' ', (k, n) -> {
            switch (k) {
                case "pgfault", "total_pgfault" -> {
                    out[0] = n;
                    ok[0] = true;
                }
                case "pgmajfault", "total_pgmajfault" -> out[1] = n;
                default -> {
                }
            }
        });
        return ok[0] ? out : null;
    }

    /** The high and max counts of a v2 memory.events, null unless both are there */
    static long[] parseCgroupMemEvents(String text) {
        long[] out = new long[2];
        int[] found = {0};
        eachKv(text, ' ', (k, n) -> {
            switch (k) {
                case "high" -> {
                    out[0] = n;
                    found[0]++;
                }
                case "max" -> {
                    out[1] = n;
                    found[0]++;
                }
                default -> {
                }
            }
        });
        return found[0] == 2 ? out : null;
    }

    /** Calls fn for "key: value kB" style lines with an unsigned value */
    private static void eachKv(String text, char sep, BiConsumer<String, Long> fn) {
        if (text == null) {
            return;
        }
        for (String line : text.split("\n")) {
            int i = line.indexOf(sep);
            if (i <= 0) {
                continue;
            }
            String v = line.substring(i + 1).strip();
            int j = v.indexOf(' ');
            if (j >= 0) {
                v = v.substring(0, j);
            }
            if (v.isEmpty() || v.charAt(0) == '-' || v.charAt(0) == '+') {
                continue;
            }
            try {
                fn.accept(line.substring(0, i), Long.parseLong(v));
            } catch (NumberFormatException e) {
                continue;
            }
        }
    }

    static String read(Path p) {
        try {
            return Files.readString(p);
        } catch (IOException | RuntimeException e) {
            return null;
        }
    }

    private static void set(Object[] row, String column, Object value) {
        row[Table.SYSTEM_METRICS.indexOf(column)] = value;
    }
}
