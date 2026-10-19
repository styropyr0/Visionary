package io.github.styropyr0.visionary;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Reads the CPU and memory limits and use of the process's own cgroup (Linux,
 * v2 or v1): in a container these are the numbers that matter, since the host
 * columns describe the whole machine underneath. The {@code container_*} columns
 * are filled only in a container, or when the cgroup sets a CPU or memory limit
 * (a systemd unit with MemoryMax=, say). {@code sdks/go/cgroup.go} is the reference.
 */
final class CgroupReader {

    private static final double MB = 1024.0 * 1024.0;
    private static final String[] CONTAINER_MARKS = {"docker", "kubepods", "containerd", "libpod", "lxc"};

    private boolean v2;
    private Path cpuDir; // v2: the cgroup; v1: the cpu controller's directory
    private Path cpuAcct; // v1: the cpuacct controller's directory
    private Path memDir;
    private final boolean inContainer;

    // Previous readings, touched only on the sampler thread.
    private long last;
    private long usage; // CPU time, ns
    private long periods;
    private long throttled;
    private long ooms;
    private boolean primed;

    private CgroupReader(boolean inContainer) {
        this.inContainer = inContainer;
    }

    /** The process's cgroup; null off Linux or when there is none to read. */
    static CgroupReader open() {
        if (!System.getProperty("os.name", "").startsWith("Linux")) {
            return null;
        }
        String platform = null;
        for (String k : CONTAINER_ENV) {
            String v = System.getenv(k);
            if (v != null && !v.isEmpty()) {
                platform = v;
            }
        }
        return open(Path.of("/"), platform);
    }

    /**
     * Variables container platforms set in every container: Kubernetes, Railway, Render, Heroku,
     * Cloud Run, ECS/Fargate and Azure Container Apps.
     */
    private static final String[] CONTAINER_ENV = {"KUBERNETES_SERVICE_HOST", "RAILWAY_SERVICE_ID", "RENDER_SERVICE_ID", "DYNO", "K_SERVICE", "ECS_CONTAINER_METADATA_URI_V4", "CONTAINER_APP_NAME"};

    /**
     * Reads {@code proc/self/cgroup} and {@code sys/fs/cgroup} under {@code root}; {@code platform}
     * is the value of a container platform's variable, null outside one.
     */
    static CgroupReader open(Path root, String platform) {
        String cgroups;
        try {
            cgroups = Files.readString(root.resolve("proc/self/cgroup"));
        } catch (IOException | RuntimeException e) {
            return null;
        }
        boolean inContainer = Files.exists(root.resolve(".dockerenv"))
                || Files.exists(root.resolve("run/.containerenv"))
                || (platform != null && !platform.isEmpty());
        for (String mark : CONTAINER_MARKS) {
            inContainer |= cgroups.contains(mark);
        }
        CgroupReader c = new CgroupReader(inContainer);
        Path mount = root.resolve("sys/fs/cgroup");
        for (String line : cgroups.strip().split("\n")) {
            String[] f = line.strip().split(":", 3);
            if (f.length != 3) {
                continue;
            }
            String ctrls = f[1];
            String path = f[2];
            if (f[0].equals("0") && ctrls.isEmpty()) {
                Path dir = cgroupDir(mount, path);
                if (Files.exists(dir.resolve("cpu.stat"))) {
                    c.v2 = true;
                    c.cpuDir = dir;
                    c.memDir = dir;
                    break;
                }
            } else if (hasController(ctrls, "memory")) {
                c.memDir = cgroupDir(mount.resolve("memory"), path);
            } else if (hasController(ctrls, "cpu") || hasController(ctrls, "cpuacct")) {
                Path dir = cgroupDir(mount.resolve(ctrls), path);
                if (hasController(ctrls, "cpu")) {
                    c.cpuDir = dir;
                }
                if (hasController(ctrls, "cpuacct")) {
                    c.cpuAcct = dir;
                }
            }
        }
        if (c.cpuDir == null && c.memDir == null) {
            return null;
        }
        return c;
    }

    /**
     * The cgroup's directory under a controller's mount. Inside a container's cgroup
     * namespace the path is "/", and with a private mount the host path does not exist
     * below it, so the mount itself is the cgroup.
     */
    private static Path cgroupDir(Path mount, String path) {
        String rel = path.replaceFirst("^/+", "");
        Path dir = rel.isEmpty() ? mount : mount.resolve(rel);
        return Files.exists(dir) ? dir : mount;
    }

    private static boolean hasController(String list, String name) {
        for (String c : list.split(",")) {
            if (c.equals(name)) {
                return true;
            }
        }
        return false;
    }

    /** A file holding one number; null when missing or not a number ("max"). */
    private static Long readLong(Path p) {
        try {
            return Long.parseLong(Files.readString(p).strip());
        } catch (IOException | RuntimeException e) {
            return null;
        }
    }

    /** "key value" lines (cpu.stat, memory.stat, memory.events); empty when unreadable. */
    private static Map<String, Long> readKv(Path p) {
        Map<String, Long> out = new HashMap<>();
        List<String> lines;
        try {
            lines = Files.readAllLines(p);
        } catch (IOException | RuntimeException e) {
            return out;
        }
        for (String line : lines) {
            String[] f = line.strip().split("\\s+");
            if (f.length == 2) {
                try {
                    out.put(f[0], Long.parseLong(f[1]));
                } catch (NumberFormatException e) {
                    // not a counter
                }
            }
        }
        return out;
    }

    /** The CPU limit in cores, 0 when unlimited. */
    double cpuLimit() {
        if (v2) {
            try {
                String[] f = Files.readString(cpuDir.resolve("cpu.max")).strip().split("\\s+");
                if (f.length == 2 && !f[0].equals("max")) {
                    double q = Double.parseDouble(f[0]);
                    double p = Double.parseDouble(f[1]);
                    return p > 0 ? q / p : 0;
                }
            } catch (IOException | RuntimeException e) {
                // no limit we can read
            }
            return 0;
        }
        if (cpuDir != null) {
            Long q = readLong(cpuDir.resolve("cpu.cfs_quota_us"));
            Long p = readLong(cpuDir.resolve("cpu.cfs_period_us"));
            if (q != null && q > 0 && p != null && p > 0) {
                return (double) q / p;
            }
        }
        return 0;
    }

    /** The memory limit in bytes, 0 when unlimited. */
    long memLimit() {
        if (v2) {
            Long m = readLong(memDir.resolve("memory.max")); // "max" doesn't parse: unlimited
            return m == null || m < 0 ? 0 : m;
        }
        if (memDir != null) {
            // v1 writes a page-rounded maximum long for no limit.
            Long m = readLong(memDir.resolve("memory.limit_in_bytes"));
            if (m != null && m > 0 && m < 1L << 62) {
                return m;
            }
        }
        return 0;
    }

    /** Cumulative CPU time (ns), CFS periods, throttled periods and OOM kills. */
    private long[] counters() {
        if (v2) {
            Map<String, Long> st = readKv(cpuDir.resolve("cpu.stat"));
            Map<String, Long> ev = readKv(memDir.resolve("memory.events"));
            return new long[] {st.getOrDefault("usage_usec", 0L) * 1000, st.getOrDefault("nr_periods", 0L),
                    st.getOrDefault("nr_throttled", 0L), ev.getOrDefault("oom_kill", 0L)};
        }
        long[] out = new long[4];
        if (cpuAcct != null) {
            Long u = readLong(cpuAcct.resolve("cpuacct.usage"));
            out[0] = u == null ? 0 : u;
        }
        if (cpuDir != null) {
            Map<String, Long> st = readKv(cpuDir.resolve("cpu.stat"));
            out[1] = st.getOrDefault("nr_periods", 0L);
            out[2] = st.getOrDefault("nr_throttled", 0L);
        }
        if (memDir != null) {
            out[3] = readKv(memDir.resolve("memory.oom_control")).getOrDefault("oom_kill", 0L);
        }
        return out;
    }

    /** Memory in use without the inactive page cache, as docker stats counts it; null when unknown. */
    Long memUsed() {
        if (memDir == null) {
            return null;
        }
        Long used = readLong(memDir.resolve(v2 ? "memory.current" : "memory.usage_in_bytes"));
        if (used == null) {
            return null;
        }
        Long inactive = readKv(memDir.resolve("memory.stat")).get(v2 ? "inactive_file" : "total_inactive_file");
        if (inactive != null && inactive < used) {
            used -= inactive;
        }
        return used;
    }

    /** The baseline the first sample's deltas start from. */
    void prime() {
        prime(System.nanoTime());
    }

    void prime(long nowNanos) {
        long[] c = counters();
        usage = c[0];
        periods = c[1];
        throttled = c[2];
        ooms = c[3];
        last = nowNanos;
        primed = true;
    }

    void sample(Object[] row) {
        sample(row, System.nanoTime());
    }

    /** Fills the container_* columns. */
    void sample(Object[] row, long nowNanos) {
        double cpus = cpuLimit();
        long memLimit = memLimit();
        if (!inContainer && cpus == 0 && memLimit == 0) {
            return;
        }
        long[] c = counters();
        double secs = (nowNanos - last) / 1e9;
        if (primed && secs > 0 && c[0] >= usage) {
            set(row, "container_cpu_cores", Values.round((c[0] - usage) / 1e9 / secs, 3));
            if (c[1] > periods && c[2] >= throttled) {
                set(row, "container_cpu_throttled_percent",
                        Values.round(100.0 * (c[2] - throttled) / (c[1] - periods), 1));
            } else if (cpus > 0) {
                set(row, "container_cpu_throttled_percent", 0.0);
            }
            if (c[3] >= ooms) {
                set(row, "container_oom_kills", c[3] - ooms);
            }
        }
        usage = c[0];
        periods = c[1];
        throttled = c[2];
        ooms = c[3];
        last = nowNanos;
        primed = true;
        if (cpus > 0) {
            set(row, "container_cpu_limit", Values.round(cpus, 2));
        }
        Long used = memUsed();
        if (used != null) {
            set(row, "container_mem_used_mb", Values.round(used / MB, 1));
        }
        if (memLimit > 0) {
            set(row, "container_mem_limit_mb", Values.round(memLimit / MB, 1));
        }
    }

    private static void set(Object[] row, String column, Object value) {
        row[Table.SYSTEM_METRICS.indexOf(column)] = value;
    }
}
