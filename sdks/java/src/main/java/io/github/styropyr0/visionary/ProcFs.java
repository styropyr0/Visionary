package io.github.styropyr0.visionary;

import java.io.IOException;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.regex.Pattern;

/**
 * Linux {@code /proc} readers for the node-level metrics the JVM doesn't expose.
 * Every reader returns null when the file is missing, unreadable or not in the
 * expected shape; nothing here throws into the sampler.
 */
final class ProcFs {

    /**
     * Partitions and virtual devices whose I/O is already counted in a whole
     * disk, so totals are not doubled (SPEC.md partition rule).
     */
    static final Pattern PARTITION = Pattern.compile(
            "^((sd|vd|xvd|hd)[a-z]+\\d+|nvme\\d+n\\d+p\\d+|mmcblk\\d+p\\d+|loop\\d+|ram\\d+|zram\\d+|dm-\\d+|sr\\d+|fd\\d+|md\\d+)$");

    private static final Path ROOT = Path.of("/proc");
    private static final long SECTOR_BYTES = 512;

    private final boolean available = Files.isReadable(ROOT.resolve("stat"));

    boolean available() {
        return available;
    }

    /** Aggregate CPU jiffies from the first line of /proc/stat. */
    record Cpu(long user, long nice, long system, long idle, long iowait, long irq, long softirq, long steal) {
        long total() {
            return user + nice + system + idle + iowait + irq + softirq + steal;
        }
    }

    /** The rest of /proc/stat: context switches and run-queue counts. */
    record Stat(Cpu cpu, Long ctxt, Long procsRunning, Long procsBlocked) {
    }

    /** Sums over whole disks; {@code busyMs} is the I/O time per disk. */
    record Disks(long readBytes, long writeBytes, long reads, long writes, long ioTimeMs, Map<String, Long> busyMs) {
    }

    /** Sums over all interfaces. */
    record Net(long rxBytes, long txBytes, long rxPackets, long txPackets, long errors, long drops) {
    }

    record Tcp(long currEstab, long retransSegs) {
    }

    /** {@code usedKb} as psutil and gopsutil count it: page cache and buffers are not used. */
    record Memory(Long totalKb, Long usedKb, Long availableKb, Long cachedKb) {
    }

    record ProcessIo(long readBytes, long writeBytes) {
    }

    Stat stat() {
        List<String> lines = lines("stat");
        if (lines == null) {
            return null;
        }
        try {
            Cpu cpu = null;
            Long ctxt = null;
            Long running = null;
            Long blocked = null;
            for (String line : lines) {
                String[] f = line.trim().split("\\s+");
                switch (f[0]) {
                    case "cpu" -> {
                        long[] v = new long[8];
                        for (int i = 0; i < v.length && i + 1 < f.length; i++) {
                            v[i] = Long.parseLong(f[i + 1]);
                        }
                        cpu = new Cpu(v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]);
                    }
                    case "ctxt" -> ctxt = Long.parseLong(f[1]);
                    case "procs_running" -> running = Long.parseLong(f[1]);
                    case "procs_blocked" -> blocked = Long.parseLong(f[1]);
                    default -> {
                        // other lines are not used
                    }
                }
            }
            return new Stat(cpu, ctxt, running, blocked);
        } catch (RuntimeException e) {
            return null;
        }
    }

    Memory memory() {
        List<String> lines = lines("meminfo");
        if (lines == null) {
            return null;
        }
        try {
            Map<String, Long> kb = new HashMap<>();
            for (String line : lines) {
                int colon = line.indexOf(':');
                if (colon > 0) {
                    String[] f = line.substring(colon + 1).trim().split("\\s+");
                    kb.put(line.substring(0, colon), Long.parseLong(f[0]));
                }
            }
            Long cached = kb.get("Cached");
            // Match `free` and gopsutil: reclaimable slab counts as cache.
            if (cached != null && kb.containsKey("SReclaimable")) {
                cached += kb.get("SReclaimable");
            }
            Long total = kb.get("MemTotal");
            Long used = null;
            if (total != null && kb.containsKey("MemFree")) {
                used = total - kb.get("MemFree") - kb.getOrDefault("Buffers", 0L) - (cached == null ? 0 : cached);
                if (used < 0 && kb.containsKey("MemAvailable")) {
                    used = total - kb.get("MemAvailable");
                }
            }
            return new Memory(total, used, kb.get("MemAvailable"), cached);
        } catch (RuntimeException e) {
            return null;
        }
    }

    Disks disks() {
        List<String> lines = lines("diskstats");
        if (lines == null) {
            return null;
        }
        try {
            long readBytes = 0;
            long writeBytes = 0;
            long reads = 0;
            long writes = 0;
            long ioTime = 0;
            Map<String, Long> busy = new HashMap<>();
            for (String line : lines) {
                String[] f = line.trim().split("\\s+");
                if (f.length < 14 || PARTITION.matcher(f[2]).matches()) {
                    continue;
                }
                reads += Long.parseLong(f[3]);
                readBytes += Long.parseLong(f[5]) * SECTOR_BYTES;
                ioTime += Long.parseLong(f[6]);
                writes += Long.parseLong(f[7]);
                writeBytes += Long.parseLong(f[9]) * SECTOR_BYTES;
                ioTime += Long.parseLong(f[10]);
                busy.put(f[2], Long.parseLong(f[12]));
            }
            return new Disks(readBytes, writeBytes, reads, writes, ioTime, busy);
        } catch (RuntimeException e) {
            return null;
        }
    }

    Net net() {
        List<String> lines = lines("net/dev");
        if (lines == null) {
            return null;
        }
        try {
            long[] sum = new long[6];
            boolean any = false;
            for (String line : lines) {
                int colon = line.indexOf(':');
                if (colon < 0) {
                    continue;
                }
                String[] f = line.substring(colon + 1).trim().split("\\s+");
                if (f.length < 12) {
                    continue;
                }
                sum[0] += Long.parseLong(f[0]);
                sum[1] += Long.parseLong(f[8]);
                sum[2] += Long.parseLong(f[1]);
                sum[3] += Long.parseLong(f[9]);
                sum[4] += Long.parseLong(f[2]) + Long.parseLong(f[10]);
                sum[5] += Long.parseLong(f[3]) + Long.parseLong(f[11]);
                any = true;
            }
            return any ? new Net(sum[0], sum[1], sum[2], sum[3], sum[4], sum[5]) : null;
        } catch (RuntimeException e) {
            return null;
        }
    }

    /** The Tcp header and value lines of /proc/net/snmp. */
    Tcp tcp() {
        List<String> lines = lines("net/snmp");
        if (lines == null) {
            return null;
        }
        try {
            String[] header = null;
            for (String line : lines) {
                if (!line.startsWith("Tcp:")) {
                    continue;
                }
                String[] f = line.trim().split("\\s+");
                if (header == null) {
                    header = f;
                    continue;
                }
                Long estab = null;
                Long retrans = null;
                for (int i = 1; i < header.length && i < f.length; i++) {
                    if (header[i].equals("CurrEstab")) {
                        estab = Long.parseLong(f[i]);
                    } else if (header[i].equals("RetransSegs")) {
                        retrans = Long.parseLong(f[i]);
                    }
                }
                return estab == null || retrans == null ? null : new Tcp(estab, retrans);
            }
            return null;
        } catch (RuntimeException e) {
            return null;
        }
    }

    ProcessIo processIo() {
        List<String> lines = lines("self/io");
        if (lines == null) {
            return null;
        }
        try {
            Long read = null;
            Long write = null;
            for (String line : lines) {
                if (line.startsWith("read_bytes:")) {
                    read = Long.parseLong(line.substring(11).trim());
                } else if (line.startsWith("write_bytes:")) {
                    write = Long.parseLong(line.substring(12).trim());
                }
            }
            return read == null || write == null ? null : new ProcessIo(read, write);
        } catch (RuntimeException e) {
            return null;
        }
    }

    /** Number of processes: the numeric entries of /proc. */
    Long processCount() {
        if (!available) {
            return null;
        }
        long n = 0;
        try (DirectoryStream<Path> dir = Files.newDirectoryStream(ROOT)) {
            for (Path p : dir) {
                String name = p.getFileName().toString();
                if (!name.isEmpty() && name.chars().allMatch(Character::isDigit)) {
                    n++;
                }
            }
            return n;
        } catch (IOException | RuntimeException e) {
            return null;
        }
    }

    /** VmRSS of this process in kB. */
    Long residentKb() {
        List<String> lines = lines("self/status");
        if (lines == null) {
            return null;
        }
        try {
            for (String line : lines) {
                if (line.startsWith("VmRSS:")) {
                    return Long.parseLong(line.replaceAll("\\D+", ""));
                }
            }
        } catch (RuntimeException ignored) {
            // treated as unavailable
        }
        return null;
    }

    private List<String> lines(String file) {
        if (!available) {
            return null;
        }
        try {
            return Files.readAllLines(ROOT.resolve(file));
        } catch (IOException | RuntimeException e) {
            return null;
        }
    }
}
