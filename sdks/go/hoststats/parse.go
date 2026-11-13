// Package hoststats reads the deeper Linux host statistics that both the Go
// SDK and the host agent record in system_metrics: pressure stall
// information, virtual memory, kernel limits, temperatures and clock speeds,
// and GPUs. Every reader is cheap (small files under /proc and /sys, at most
// one nvidia-smi call per sample) and a value that cannot be read is left
// out, so it is stored as NULL
package hoststats

import (
	"bytes"
	"strconv"
)

// Pressure is one /proc/pressure or cgroup *.pressure file, in percent
type Pressure struct {
	SomeAvg10, SomeAvg60 float64
	FullAvg10, FullAvg60 float64
	HasFull              bool
}

// ParsePressure reads "some avg10=0.12 avg60=..." and the optional "full" line
func ParsePressure(b []byte) (Pressure, bool) {
	var p Pressure
	ok := false
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		kind, rest, _ := bytes.Cut(line, []byte(" "))
		a10, ok10 := pressureField(rest, "avg10=")
		a60, ok60 := pressureField(rest, "avg60=")
		if !ok10 || !ok60 {
			continue
		}
		switch string(kind) {
		case "some":
			p.SomeAvg10, p.SomeAvg60, ok = a10, a60, true
		case "full":
			p.FullAvg10, p.FullAvg60, p.HasFull = a10, a60, true
		}
	}
	return p, ok
}

func pressureField(b []byte, key string) (float64, bool) {
	i := bytes.Index(b, []byte(key))
	if i < 0 {
		return 0, false
	}
	v := b[i+len(key):]
	if j := bytes.IndexByte(v, ' '); j >= 0 {
		v = v[:j]
	}
	f, err := strconv.ParseFloat(string(v), 64)
	return f, err == nil
}

// VMStat holds the /proc/vmstat counters the columns are rates of
type VMStat struct {
	PgFault, PgMajFault uint64
	PswpIn, PswpOut     uint64
	PgScan, PgSteal     uint64
	OOMKill             uint64
	HasOOMKill          bool
}

// ParseVMStat sums reclaim over kswapd, direct and khugepaged scans, and
// the per-zone names older kernels use, but not pgscan_anon or pgscan_file,
// which split the same pages by type
func ParseVMStat(b []byte) (VMStat, bool) {
	var s VMStat
	ok := false
	eachKV(b, ' ', func(k []byte, v uint64) {
		switch string(k) {
		case "pgfault":
			s.PgFault, ok = v, true
		case "pgmajfault":
			s.PgMajFault = v
		case "pswpin":
			s.PswpIn = v
		case "pswpout":
			s.PswpOut = v
		case "oom_kill":
			s.OOMKill, s.HasOOMKill = v, true
		case "pgscan_direct_throttle":
		default:
			if hasAnyPrefix(k, "pgscan_kswapd", "pgscan_direct", "pgscan_khugepaged") {
				s.PgScan += v
			} else if hasAnyPrefix(k, "pgsteal_kswapd", "pgsteal_direct", "pgsteal_khugepaged") {
				s.PgSteal += v
			}
		}
	})
	return s, ok
}

// MemInfo holds the /proc/meminfo fields beyond what gopsutil reports, in kB
type MemInfo struct {
	Dirty, Writeback, Slab, Shmem, Committed, CommitLimit uint64
	ok                                                    uint8
}

// ParseMemInfo reads "Dirty:   1234 kB" lines
func ParseMemInfo(b []byte) (MemInfo, bool) {
	var m MemInfo
	eachKV(b, ':', func(k []byte, v uint64) {
		switch string(k) {
		case "Dirty":
			m.Dirty, m.ok = v, m.ok|1
		case "Writeback":
			m.Writeback = v
		case "Slab":
			m.Slab = v
		case "Shmem":
			m.Shmem = v
		case "Committed_AS":
			m.Committed, m.ok = v, m.ok|2
		case "CommitLimit":
			m.CommitLimit, m.ok = v, m.ok|4
		}
	})
	return m, m.ok == 7
}

// ParseFileNr reads /proc/sys/fs/file-nr: allocated, unused and maximum
// file handles. Open handles are allocated minus unused
func ParseFileNr(b []byte) (open, max uint64, ok bool) {
	f := bytes.Fields(b)
	if len(f) != 3 {
		return 0, 0, false
	}
	alloc, e1 := strconv.ParseUint(string(f[0]), 10, 64)
	unused, e2 := strconv.ParseUint(string(f[1]), 10, 64)
	max, e3 := strconv.ParseUint(string(f[2]), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || unused > alloc {
		return 0, 0, false
	}
	return alloc - unused, max, true
}

// SockStat holds /proc/net/sockstat
type SockStat struct {
	Used, TCPInUse, TCPOrphan, TCPTimeWait uint64
}

// ParseSockStat reads "sockets: used 120" and "TCP: inuse 5 orphan 0 tw 2 ..."
func ParseSockStat(b []byte) (SockStat, bool) {
	var s SockStat
	found := 0
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		proto, rest, ok := bytes.Cut(line, []byte(": "))
		if !ok {
			continue
		}
		f := bytes.Fields(rest)
		for i := 0; i+1 < len(f); i += 2 {
			v, err := strconv.ParseUint(string(f[i+1]), 10, 64)
			if err != nil {
				continue
			}
			switch string(proto) + " " + string(f[i]) {
			case "sockets used":
				s.Used = v
				found++
			case "TCP inuse":
				s.TCPInUse = v
				found++
			case "TCP orphan":
				s.TCPOrphan = v
			case "TCP tw":
				s.TCPTimeWait = v
			}
		}
	}
	return s, found == 2
}

// UDPErrors holds the UDP receive and buffer error counters
type UDPErrors struct{ InErrors, BufErrors uint64 }

// ParseSNMP reads the "Udp:" header and value lines of /proc/net/snmp
func ParseSNMP(b []byte) (UDPErrors, bool) {
	var header [][]byte
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		if !bytes.HasPrefix(line, []byte("Udp: ")) {
			continue
		}
		f := bytes.Fields(line[5:])
		if header == nil {
			header = f
			continue
		}
		var u UDPErrors
		ok := false
		for i := 0; i < len(header) && i < len(f); i++ {
			v, err := strconv.ParseUint(string(f[i]), 10, 64)
			if err != nil {
				continue
			}
			switch string(header[i]) {
			case "InErrors":
				u.InErrors, ok = v, true
			case "RcvbufErrors", "SndbufErrors":
				u.BufErrors += v
			}
		}
		return u, ok
	}
	return UDPErrors{}, false
}

// ParseSNMP6 reads the Udp6 lines of /proc/net/snmp6 ("Udp6InErrors 0")
func ParseSNMP6(b []byte) (UDPErrors, bool) {
	var u UDPErrors
	ok := false
	eachKV(b, ' ', func(k []byte, v uint64) {
		switch string(k) {
		case "Udp6InErrors":
			u.InErrors, ok = v, true
		case "Udp6RcvbufErrors", "Udp6SndbufErrors":
			u.BufErrors += v
		}
	})
	return u, ok
}

// CountTCPState counts the rows of /proc/net/tcp or tcp6 in a state, given
// in hex as the file writes it ("08" is CLOSE_WAIT)
func CountTCPState(b []byte, state string) int {
	n := 0
	_, b = nextLine(b)
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		if string(field(line, 3)) == state {
			n++
		}
	}
	return n
}

// ParseCgroupMemStat reads the fault counters of a cgroup's memory.stat,
// v2 (pgfault) or v1 (total_pgfault)
func ParseCgroupMemStat(b []byte) (faults, major uint64, ok bool) {
	eachKV(b, ' ', func(k []byte, v uint64) {
		switch string(k) {
		case "pgfault", "total_pgfault":
			faults, ok = v, true
		case "pgmajfault", "total_pgmajfault":
			major = v
		}
	})
	return faults, major, ok
}

// ParseCgroupMemEvents reads high and max from a v2 memory.events
func ParseCgroupMemEvents(b []byte) (high, max uint64, ok bool) {
	n := 0
	eachKV(b, ' ', func(k []byte, v uint64) {
		switch string(k) {
		case "high":
			high, n = v, n+1
		case "max":
			max, n = v, n+1
		}
	})
	return high, max, n == 2
}

// ParseCgroupIO sums the bytes read and written over a cgroup's devices: v2
// io.stat ("8:0 rbytes=1 wbytes=2 ...") or v1 blkio.throttle.io_service_bytes
// ("8:0 Read 1", "8:0 Write 2")
func ParseCgroupIO(b []byte) (read, write uint64, ok bool) {
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		f := bytes.Fields(line)
		if len(f) == 3 && (string(f[1]) == "Read" || string(f[1]) == "Write") {
			n, err := strconv.ParseUint(string(f[2]), 10, 64)
			if err != nil {
				continue
			}
			if string(f[1]) == "Read" {
				read += n
			} else {
				write += n
			}
			ok = true
			continue
		}
		for _, kv := range f[min(len(f), 1):] {
			k, v, found := bytes.Cut(kv, []byte("="))
			if !found {
				continue
			}
			n, err := strconv.ParseUint(string(v), 10, 64)
			if err != nil {
				continue
			}
			switch string(k) {
			case "rbytes":
				read, ok = read+n, true
			case "wbytes":
				write, ok = write+n, true
			}
		}
	}
	return read, write, ok
}

// ParseCPUInfoMHz averages the "cpu MHz" lines of /proc/cpuinfo
func ParseCPUInfoMHz(b []byte) (float64, bool) {
	sum, n := 0.0, 0
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		k, v, ok := bytes.Cut(line, []byte(":"))
		if !ok || string(bytes.TrimSpace(k)) != "cpu MHz" {
			continue
		}
		if f, err := strconv.ParseFloat(string(bytes.TrimSpace(v)), 64); err == nil {
			sum += f
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// ParseDPMClock reads an AMD pp_dpm_sclk table ("1: 1200Mhz *"): the level
// marked current and the highest
func ParseDPMClock(b []byte) (cur, max float64, ok bool) {
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		_, rest, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		rest = bytes.TrimSpace(rest)
		star := bytes.HasSuffix(rest, []byte("*"))
		rest = bytes.TrimSpace(bytes.TrimSuffix(rest, []byte("*")))
		rest = bytes.TrimSuffix(bytes.TrimSuffix(rest, []byte("Mhz")), []byte("MHz"))
		f, err := strconv.ParseFloat(string(rest), 64)
		if err != nil {
			continue
		}
		max = maxf(max, f)
		if star {
			cur, ok = f, true
		}
	}
	return cur, max, ok
}

// ParseInt reads a file holding one integer, such as a sysfs attribute
func ParseInt(b []byte) (int64, bool) {
	n, err := strconv.ParseInt(string(bytes.TrimSpace(b)), 10, 64)
	return n, err == nil
}

// eachKV calls fn for "key<sep> value [unit]" lines with an unsigned value
func eachKV(b []byte, sep byte, fn func(k []byte, v uint64)) {
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		i := bytes.IndexByte(line, sep)
		if i <= 0 {
			continue
		}
		v := bytes.TrimSpace(line[i+1:])
		if j := bytes.IndexByte(v, ' '); j >= 0 {
			v = v[:j]
		}
		if n, err := strconv.ParseUint(string(v), 10, 64); err == nil {
			fn(line[:i], n)
		}
	}
}

func nextLine(b []byte) (line, rest []byte) {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i], b[i+1:]
	}
	return b, nil
}

// field is the n-th space-separated field of line, without allocating
func field(line []byte, n int) []byte {
	for i := 0; ; i++ {
		for len(line) > 0 && line[0] == ' ' {
			line = line[1:]
		}
		if len(line) == 0 {
			return nil
		}
		end := bytes.IndexByte(line, ' ')
		if end < 0 {
			end = len(line)
		}
		if i == n {
			return line[:end]
		}
		line = line[end:]
	}
}

func hasAnyPrefix(b []byte, prefixes ...string) bool {
	for _, p := range prefixes {
		if bytes.HasPrefix(b, []byte(p)) {
			return true
		}
	}
	return false
}

func maxf(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}
