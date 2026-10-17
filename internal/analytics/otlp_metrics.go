package analytics

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Host and process metrics in the OpenTelemetry semantic conventions become
// system_metrics rows: datapoints of one instance in one 15 s window make one
// row, and whatever the exporter did not send stays NULL.

const otlpMetricWindow = 15 // seconds

// otlpSystemCols are the system_metrics columns OTLP can fill, in insert
// order.
var otlpSystemCols = []string{
	"created_at", "instance", "interval_s", "service", "version",
	"host_cpu_percent", "cpu_user_percent", "cpu_system_percent", "cpu_iowait_percent", "cpu_steal_percent", "cpu_count",
	"load_1", "load_5", "load_15",
	"host_mem_used_mb", "host_mem_total_mb", "host_mem_percent", "host_mem_available_mb", "host_mem_cached_mb",
	"swap_used_mb", "swap_total_mb",
	"disk_used_percent", "disk_free_gb", "disk_total_gb",
	"disk_read_kbps", "disk_write_kbps", "disk_read_iops", "disk_write_iops",
	"net_rx_kbps", "net_tx_kbps", "net_rx_pps", "net_tx_pps", "net_errors", "net_drops",
	"host_procs", "procs_running", "procs_blocked",
	"proc_cpu_percent", "proc_rss_mb", "proc_threads", "proc_open_fds", "uptime_s",
	"goroutines", "heap_alloc_mb", "heap_inuse_mb", "gc_count",
}

var otlpSystemTable = &otlpTable{"system_metrics", otlpSystemCols}

// otlpIntCols are written as integers.
var otlpIntCols = map[string]bool{
	"cpu_count": true, "net_errors": true, "net_drops": true, "host_procs": true, "procs_running": true,
	"procs_blocked": true, "proc_threads": true, "proc_open_fds": true, "uptime_s": true, "goroutines": true, "gc_count": true,
}

// otlpRates are cumulative counters read as per-second rates; otlpDeltas as
// the increase since the previous datapoint.
var (
	otlpRates = map[string]bool{
		"system.cpu.time": true, "process.cpu.time": true, "system.disk.io": true, "system.disk.operations": true,
		"system.network.io": true, "system.network.packets": true, otlpLegacy + "process.cpu.time": true,
	}
	otlpDeltas = map[string]bool{"system.network.errors": true, "system.network.dropped": true, "runtime.gc.count": true}
)

// otlpPartition is the SDK contract's rule for disk devices that are
// partitions of a disk counted already.
var otlpPartition = regexp.MustCompile(`^((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|dm-\d+|sr\d+|fd\d+|md\d+)$`)

type otlpPoint struct {
	v float64
	t uint64
}

type otlpObs struct {
	attrs map[string]string
	v     float64
}

// otlpMetricRow is one instance's datapoints in one window: metric name ->
// series -> last value (deltas add up).
type otlpMetricRow struct {
	res       *otlpResource
	key       string // instance and service
	bucket    int64
	at        uint64
	firstSeen time.Time
	obs       map[string]map[string]otlpObs
}

// otlpMetricLinger is how long a window stays open for more exports after
// its first datapoint arrived, when no later window closes it first.
const otlpMetricLinger = 20 * time.Second

func (o *otlpState) ingestMetrics(ctx context.Context, rms []*metricspb.ResourceMetrics) (int64, error) {
	var rejected int64
	now := time.Now()
	o.mu.Lock()
	out := &otlpRows{}
	for _, rm := range rms {
		res := otlpResourceOf(rm.GetResource().GetAttributes())
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				name, filter := otlpMetricName(m.GetName())
				if name == "" {
					continue
				}
				var points []*metricspb.NumberDataPoint
				cumulative, monotonic := true, false
				switch {
				case m.GetGauge() != nil:
					points = m.GetGauge().GetDataPoints()
				case m.GetSum() != nil:
					points = m.GetSum().GetDataPoints()
					cumulative = m.GetSum().GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
					monotonic = m.GetSum().GetIsMonotonic()
				default:
					continue
				}
				for _, p := range points {
					if p.GetTimeUnixNano() == 0 {
						rejected++
						continue
					}
					pa := otlpAttrMap(p.GetAttributes())
					attrs := make(map[string]string, len(pa))
					for k := range pa {
						attrs[k] = pa.str(k)
					}
					if filter != nil && !filter(attrs) {
						continue
					}
					if v, ok := o.metricValue(res, name, attrs, p, cumulative, monotonic); ok {
						o.observe(res, name, attrs, p.GetTimeUnixNano(), v, now)
					}
				}
			}
			o.noteRelease(out, res, uint64(now.UnixNano()))
		}
	}
	due := o.closedMetricRows(now, false)
	o.metricRowsInto(out, due)
	o.mu.Unlock()
	if err := o.store(ctx, out); err != nil {
		// Keep the closed windows for the next attempt.
		o.mu.Lock()
		for _, row := range due {
			k := row.key + "\n" + otlpParam(row.bucket)
			if _, newer := o.metricRows[k]; !newer {
				o.metricRows[k] = row
			}
		}
		o.mu.Unlock()
		return 0, err
	}
	return rejected, nil
}

// observe adds a value to its instance's window. The caller holds o.mu.
func (o *otlpState) observe(res *otlpResource, name string, attrs map[string]string, ns uint64, v float64, now time.Time) {
	bucket := int64(ns/1e9) / otlpMetricWindow * otlpMetricWindow
	rowKey := res.instance + "\n" + res.service
	k := rowKey + "\n" + otlpParam(bucket)
	row := o.metricRows[k]
	if row == nil {
		row = &otlpMetricRow{res: res, key: rowKey, bucket: bucket, firstSeen: now, obs: map[string]map[string]otlpObs{}}
		o.metricRows[k] = row
	}
	row.at = max(row.at, ns)
	series := row.obs[name]
	if series == nil {
		series = map[string]otlpObs{}
		row.obs[name] = series
	}
	sk := otlpSeriesKey(attrs)
	if prev, ok := series[sk]; ok && otlpDeltas[name] {
		v += prev.v
	}
	series[sk] = otlpObs{attrs, v}
}

// closedMetricRows takes the windows that are complete: a later window of
// the same instance exists, they lingered long enough, or all of them when
// all is set or memory is short. The caller holds o.mu.
func (o *otlpState) closedMetricRows(now time.Time, all bool) []*otlpMetricRow {
	all = all || len(o.metricRows) > otlpMaxMetricRows
	newest := map[string]int64{}
	for _, row := range o.metricRows {
		newest[row.key] = max(newest[row.key], row.bucket)
	}
	var due []*otlpMetricRow
	for k, row := range o.metricRows {
		if all || row.bucket < newest[row.key] || now.Sub(row.firstSeen) >= otlpMetricLinger {
			due = append(due, row)
			delete(o.metricRows, k)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].at < due[j].at })
	return due
}

// dueMetrics is the closed windows as rows, for the background flush.
func (o *otlpState) dueMetrics(now time.Time) *otlpRows {
	rows := &otlpRows{}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.metricRowsInto(rows, o.closedMetricRows(now, false))
	return rows
}

// metricRowsInto turns windows into system_metrics rows. The caller holds
// o.mu.
func (o *otlpState) metricRowsInto(out *otlpRows, due []*otlpMetricRow) {
	for _, row := range due {
		vals := row.columns()
		if len(vals) == 0 {
			continue
		}
		at := time.Unix(0, int64(row.at))
		interval := float64(otlpMetricWindow)
		if prev, ok := o.lastRow[row.key]; ok && at.After(prev) {
			if at.Sub(prev) < time.Hour {
				interval = math.Round(at.Sub(prev).Seconds()*100) / 100
			}
			o.lastRow[row.key] = at
		} else if !ok {
			o.lastRow[row.key] = at
		}
		r := []any{otlpTime(row.at), row.res.instance, interval, otlpNullStr(row.res.service), otlpNullStr(row.res.version)}
		for _, c := range otlpSystemCols[5:] {
			v, ok := vals[c]
			switch {
			case !ok || math.IsNaN(v) || math.IsInf(v, 0):
				r = append(r, nil)
			case otlpIntCols[c]:
				r = append(r, int64(math.Round(v)))
			default:
				r = append(r, math.Round(v*100)/100)
			}
		}
		out.add(otlpSystemTable, r)
	}
	if len(o.lastRow) > otlpMaxSeries {
		o.lastRow = map[string]time.Time{}
	}
}

// otlpLegacy prefixes the older process.runtime.<runtime>.* metrics. SDKs
// often send them next to the current names, so they are read only when the
// current one is missing.
const otlpLegacy = "legacy."

// otlpMetricName is the name this file reads the metric under, or "" to
// ignore it; filter picks the datapoints that apply.
func otlpMetricName(name string) (string, func(map[string]string) bool) {
	if rest, ok := strings.CutPrefix(name, "process.runtime."); ok {
		_, tail, _ := strings.Cut(rest, ".")
		switch tail {
		case "cpu_time":
			return otlpLegacy + "process.cpu.time", nil
		case "cpu.utilization":
			// psutil's cpu_percent: a share of one core, not of all CPUs.
			return otlpLegacy + "process.cpu.core_utilization", nil
		case "memory":
			return otlpLegacy + "process.memory.usage", func(a map[string]string) bool { return a["type"] == "" || a["type"] == "rss" }
		case "thread_count":
			return otlpLegacy + "process.thread.count", nil
		case "goroutines":
			return otlpLegacy + "go.goroutine.count", nil
		case "gc_count", "gc.count":
			return "runtime.gc.count", nil
		case "mem.heap_alloc":
			return "go.heap_alloc", nil
		}
		return "", nil
	}
	switch name {
	case "system.cpu.utilization", "system.cpu.time", "system.cpu.logical.count",
		"system.cpu.load_average.1m", "system.cpu.load_average.5m", "system.cpu.load_average.15m",
		"system.memory.usage", "system.memory.utilization", "system.memory.limit", "system.paging.usage",
		"system.filesystem.usage", "system.disk.io", "system.disk.operations",
		"system.network.io", "system.network.packets", "system.network.errors", "system.network.dropped",
		"system.process.count", "system.processes.count",
		"process.cpu.time", "process.cpu.utilization", "process.memory.usage", "process.thread.count",
		"process.open_file_descriptor.count", "process.uptime", "go.goroutine.count", "jvm.thread.count":
		return name, nil
	case "system.swap.usage": // opentelemetry-instrumentation-system-metrics
		return "system.paging.usage", nil
	case "system.network.dropped_packets":
		return "system.network.dropped", nil
	case "jvm.memory.used", "jvm.memory.committed":
		return name, func(a map[string]string) bool { return a["jvm.memory.type"] == "heap" }
	}
	return "", nil
}

// metricValue is the datapoint's value, or for counters its rate or delta
// against the previous datapoint of the series. The caller holds o.mu.
func (o *otlpState) metricValue(res *otlpResource, name string, attrs map[string]string, p *metricspb.NumberDataPoint, cumulative, monotonic bool) (float64, bool) {
	v := p.GetAsDouble()
	if _, isInt := p.GetValue().(*metricspb.NumberDataPoint_AsInt); isInt {
		v = float64(p.GetAsInt())
	}
	if !otlpRates[name] && !otlpDeltas[name] || !monotonic {
		return v, true
	}
	if !cumulative {
		if otlpDeltas[name] {
			return v, true
		}
		if dt := float64(p.GetTimeUnixNano()-p.GetStartTimeUnixNano()) / 1e9; p.GetStartTimeUnixNano() > 0 && dt > 0 {
			return v / dt, true
		}
		return 0, false
	}
	key := res.instance + "\n" + name + "\n" + otlpSeriesKey(attrs)
	prev, seen := o.series[key]
	if len(o.series) >= otlpMaxSeries {
		o.series = map[string]otlpPoint{}
	}
	o.series[key] = otlpPoint{v, p.GetTimeUnixNano()}
	if !seen || p.GetTimeUnixNano() <= prev.t || v < prev.v {
		return 0, false
	}
	if otlpDeltas[name] {
		return v - prev.v, true
	}
	return (v - prev.v) / (float64(p.GetTimeUnixNano()-prev.t) / 1e9), true
}

func otlpSeriesKey(attrs map[string]string) string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + attrs[k] + ";")
	}
	return b.String()
}

// otlpAttr reads the first of keys (current and older attribute names).
func otlpAttr(a map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := a[k]; v != "" {
			return v
		}
	}
	return ""
}

const (
	otlpMiB = 1 << 20
	otlpGiB = 1 << 30
)

// columns computes the system_metrics values of the window.
func (r *otlpMetricRow) columns() map[string]float64 {
	out := map[string]float64{}
	set := func(col string, v float64, ok bool) {
		if ok {
			out[col] = v
		}
	}
	// sum adds a metric's series, optionally only those matching keep.
	sum := func(name string, keep func(map[string]string) bool) (float64, bool) {
		total, any := 0.0, false
		for _, ob := range r.obs[name] {
			if keep == nil || keep(ob.attrs) {
				total += ob.v
				any = true
			}
		}
		return total, any
	}
	setScaled := func(col, name string, keep func(map[string]string) bool, scale float64) {
		if v, ok := sum(name, keep); ok {
			out[col] = v / scale
		}
	}
	setSum := func(col, name string, keep func(map[string]string) bool) { setScaled(col, name, keep, 1) }
	// pick is the current metric name when it was sent, else the legacy one.
	pick := func(name string) string {
		if len(r.obs[name]) > 0 {
			return name
		}
		return otlpLegacy + name
	}
	state := func(a map[string]string) string {
		return otlpAttr(a, "state", "system.cpu.state", "cpu.mode", "system.memory.state", "system.paging.state",
			"system.filesystem.state", "status", "system.process.status", "process.cpu.state", "cpu.state")
	}
	is := func(states ...string) func(map[string]string) bool {
		return func(a map[string]string) bool {
			s := state(a)
			for _, x := range states {
				if s == x {
					return true
				}
			}
			return false
		}
	}
	direction := func(dirs ...string) func(map[string]string) bool {
		return func(a map[string]string) bool {
			d := otlpAttr(a, "direction", "network.io.direction", "disk.io.direction", "system.disk.direction")
			for _, x := range dirs {
				if d == x {
					return true
				}
			}
			return false
		}
	}

	// CPU: utilization by state (or the rate of cpu time, which is the same
	// ratio), averaged over the logical CPUs.
	cpuName := "system.cpu.utilization"
	if len(r.obs[cpuName]) == 0 {
		cpuName = "system.cpu.time"
	}
	if series := r.obs[cpuName]; len(series) > 0 {
		cpus := map[string]bool{}
		for _, ob := range series {
			cpus[otlpAttr(ob.attrs, "cpu", "system.cpu.logical_number", "cpu.logical_number")] = true
		}
		total, _ := sum(cpuName, nil)
		if total > 0 {
			idle, _ := sum(cpuName, is("idle"))
			wait, _ := sum(cpuName, is("wait", "iowait"))
			user, _ := sum(cpuName, is("user", "nice"))
			system, _ := sum(cpuName, is("system", "interrupt", "softirq", "irq"))
			steal, hasSteal := sum(cpuName, is("steal"))
			out["host_cpu_percent"] = 100 * (total - idle - wait) / total
			out["cpu_user_percent"] = 100 * user / total
			out["cpu_system_percent"] = 100 * system / total
			if _, ok := sum(cpuName, is("wait", "iowait")); ok {
				out["cpu_iowait_percent"] = 100 * wait / total
			}
			set("cpu_steal_percent", 100*steal/total, hasSteal)
		}
		if _, ok := cpus[""]; !ok {
			out["cpu_count"] = float64(len(cpus))
		}
	}
	if v, ok := sum("system.cpu.logical.count", nil); ok {
		out["cpu_count"] = v
	}
	setSum("load_1", "system.cpu.load_average.1m", nil)
	setSum("load_5", "system.cpu.load_average.5m", nil)
	setSum("load_15", "system.cpu.load_average.15m", nil)

	// Memory, by state.
	used, hasUsed := sum("system.memory.usage", is("used"))
	total, hasTotal := sum("system.memory.limit", nil)
	if !hasTotal {
		total, hasTotal = sum("system.memory.usage", is("used", "free", "cached", "buffered", "buffers"))
	}
	set("host_mem_used_mb", used/otlpMiB, hasUsed)
	set("host_mem_total_mb", total/otlpMiB, hasTotal && total > 0)
	if v, ok := sum("system.memory.utilization", is("used")); ok {
		out["host_mem_percent"] = 100 * v
	} else if hasUsed && total > 0 {
		out["host_mem_percent"] = 100 * used / total
	}
	setSum("host_mem_available_mb", "system.memory.usage", is("available"))
	if v, ok := sum("system.memory.usage", is("cached")); ok && v > 0 {
		out["host_mem_cached_mb"] = v / otlpMiB
	}
	if out["host_mem_available_mb"] > 0 {
		out["host_mem_available_mb"] /= otlpMiB
	}
	swapUsed, hasSwap := sum("system.paging.usage", is("used"))
	swapFree, _ := sum("system.paging.usage", is("free"))
	set("swap_used_mb", swapUsed/otlpMiB, hasSwap)
	set("swap_total_mb", (swapUsed+swapFree)/otlpMiB, hasSwap)

	// Filesystem: the root mount, else the largest.
	mounts := map[string]map[string]float64{}
	for _, ob := range r.obs["system.filesystem.usage"] {
		m := otlpAttr(ob.attrs, "mountpoint", "system.filesystem.mountpoint", "device", "system.device")
		if mounts[m] == nil {
			mounts[m] = map[string]float64{}
		}
		mounts[m][state(ob.attrs)] += ob.v
	}
	var fs map[string]float64
	best := -1.0
	for m, st := range mounts {
		size := st["used"] + st["free"] + st["reserved"]
		if m == "/" {
			fs, best = st, math.Inf(1)
		} else if size > best {
			fs, best = st, size
		}
	}
	if fs != nil && fs["used"]+fs["free"] > 0 {
		out["disk_used_percent"] = 100 * fs["used"] / (fs["used"] + fs["free"])
		out["disk_free_gb"] = fs["free"] / otlpGiB
		out["disk_total_gb"] = (fs["used"] + fs["free"] + fs["reserved"]) / otlpGiB
	}

	// Disk and network I/O rates.
	wholeDisk := func(dirs ...string) func(map[string]string) bool {
		d := direction(dirs...)
		return func(a map[string]string) bool {
			return d(a) && !otlpPartition.MatchString(otlpAttr(a, "device", "system.device"))
		}
	}
	setScaled("disk_read_kbps", "system.disk.io", wholeDisk("read"), 1024)
	setScaled("disk_write_kbps", "system.disk.io", wholeDisk("write"), 1024)
	setSum("disk_read_iops", "system.disk.operations", wholeDisk("read"))
	setSum("disk_write_iops", "system.disk.operations", wholeDisk("write"))
	setScaled("net_rx_kbps", "system.network.io", direction("receive"), 1024)
	setScaled("net_tx_kbps", "system.network.io", direction("transmit"), 1024)
	setSum("net_rx_pps", "system.network.packets", direction("receive"))
	setSum("net_tx_pps", "system.network.packets", direction("transmit"))
	setSum("net_errors", "system.network.errors", nil)
	setSum("net_drops", "system.network.dropped", nil)

	// Processes on the host.
	procs := "system.processes.count"
	if len(r.obs[procs]) == 0 {
		procs = "system.process.count"
	}
	setSum("host_procs", procs, nil)
	setSum("procs_running", procs, is("running"))
	setSum("procs_blocked", procs, is("blocked"))

	// This process. process.cpu.time's rate is cores in use; utilization is
	// a share of all CPUs.
	if v, ok := sum(pick("process.cpu.time"), nil); ok {
		out["proc_cpu_percent"] = 100 * v
	} else if v, ok := sum("process.cpu.utilization", nil); ok {
		cores := out["cpu_count"]
		if cores == 0 {
			cores = 1
		}
		out["proc_cpu_percent"] = 100 * v * cores
	} else if v, ok := sum(otlpLegacy+"process.cpu.core_utilization", nil); ok {
		out["proc_cpu_percent"] = 100 * v
	}
	setScaled("proc_rss_mb", pick("process.memory.usage"), nil, otlpMiB)
	setSum("proc_threads", pick("process.thread.count"), nil)
	setSum("proc_open_fds", "process.open_file_descriptor.count", nil)
	setSum("uptime_s", "process.uptime", nil)

	// Runtime: goroutines in Go, live threads elsewhere.
	if v, ok := sum(pick("go.goroutine.count"), nil); ok {
		out["goroutines"] = v
	} else if v, ok := sum("jvm.thread.count", nil); ok {
		out["goroutines"] = v
	} else if v, ok := out["proc_threads"]; ok {
		out["goroutines"] = v
	}
	if v, ok := sum("go.heap_alloc", nil); ok {
		out["heap_alloc_mb"] = v / otlpMiB
	} else if v, ok := sum("jvm.memory.used", nil); ok {
		out["heap_alloc_mb"] = v / otlpMiB
	}
	if v, ok := sum("jvm.memory.committed", nil); ok {
		out["heap_inuse_mb"] = v / otlpMiB
	}
	setSum("gc_count", "runtime.gc.count", nil)
	return out
}
