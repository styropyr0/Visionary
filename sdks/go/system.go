package visionary

import (
	"context"
	"database/sql"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// systemColumns is the insert order; sample builds rows in it.
var systemColumns = func() []string {
	cols := append([]string{}, systemBaseColumns...)
	for _, c := range AddedSystemColumns {
		cols = append(cols, c.Name)
	}
	return cols
}()

// systemBaseColumns are the columns of the first release.
var systemBaseColumns = []string{
	"created_at", "instance", "interval_s",
	"host_cpu_percent", "load_1", "load_5", "load_15",
	"host_mem_used_mb", "host_mem_total_mb", "host_mem_percent",
	"disk_used_percent", "disk_free_gb", "net_rx_kbps", "net_tx_kbps",
	"proc_cpu_percent", "proc_rss_mb", "proc_threads", "proc_open_fds",
	"goroutines", "heap_alloc_mb", "heap_inuse_mb", "heap_objects",
	"gc_count", "gc_pause_ms", "gc_cpu_percent",
	"db_total_conns", "db_idle_conns", "db_acquired_conns", "db_max_conns",
	"db_acquires", "db_empty_acquires", "db_acquire_wait_ms",
	"http_requests", "http_errors", "http_rps", "http_in_flight",
	"uptime_s",
}

// samplesPerWrite batches rows: one write per minute at the default interval.
const samplesPerWrite = 4

type recorder struct {
	cfg     SystemConfig
	ident   identity
	db      store
	insert  string
	logger  *slog.Logger
	proc    *process.Process
	diskDir string
	started time.Time
	http    httpCounters

	last     time.Time
	requests int64
	errors   int64
	netRx    uint64
	netTx    uint64
	numGC    uint32
	pauseNs  uint64
	gcCPU    [2]metrics.Sample // GC and total CPU seconds, cumulative
	prevGC   float64
	prevCPU  float64
	dbPrev   DBStats
	prev     counters
	buf      [][]any
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newRecorder(cfg SystemConfig, ident identity, db store, logger *slog.Logger) *recorder {
	proc, _ := process.NewProcess(int32(os.Getpid()))
	dir := cfg.DiskPath
	if dir == "" {
		wd, _ := os.Getwd()
		dir = filepath.VolumeName(wd) + string(filepath.Separator)
	}
	return &recorder{
		cfg:     cfg,
		ident:   ident,
		db:      db,
		insert:  insertSQL("system_metrics", systemColumns, db.stringParams()),
		logger:  logger,
		proc:    proc,
		diskDir: dir,
		started: time.Now(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (r *recorder) start(ctx context.Context) {
	ctx = quiet(ctx)
	r.prime()
	go r.run(ctx)
	if r.cfg.RetentionDays > 0 {
		sql := r.db.dialect().purgeSQL("system_metrics")
		go runDaily(ctx, func() { purge(ctx, r.db, r.logger, sql, r.cfg.RetentionDays, "system_metrics") })
	}
	r.logger.Info("visionary: recording system metrics", "interval", r.cfg.Interval.String(), "instance", r.ident.instance)
}

func (r *recorder) close(ctx context.Context) {
	r.stopOnce.Do(func() { close(r.stop) })
	select {
	case <-r.done:
	case <-ctx.Done():
	}
}

// prime takes baseline readings so the first sample has real deltas.
func (r *recorder) prime() {
	r.last = time.Now()
	_, _ = cpu.Percent(0, false)
	if r.proc != nil {
		_, _ = r.proc.Percent(0)
	}
	if io, err := gnet.IOCounters(false); err == nil && len(io) > 0 {
		r.netRx, r.netTx = io[0].BytesRecv, io[0].BytesSent
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	r.numGC, r.pauseNs = ms.NumGC, ms.PauseTotalNs
	r.gcCPU[0].Name, r.gcCPU[1].Name = "/cpu/classes/gc/total:cpu-seconds", "/cpu/classes/total:cpu-seconds"
	r.prevGC, r.prevCPU = r.readGCCPU()
	if r.cfg.DB != nil {
		r.dbPrev = r.cfg.DB()
	}
	r.prev = readCounters(r.proc)
}

func (r *recorder) run(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.flushOnExit()
			return
		case <-r.stop:
			r.flushOnExit()
			return
		case <-t.C:
			r.buf = append(r.buf, r.sample())
			if len(r.buf) >= samplesPerWrite {
				wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				r.flush(wctx)
				cancel()
			}
		}
	}
}

func (r *recorder) flushOnExit() {
	ctx, cancel := context.WithTimeout(quiet(context.Background()), 10*time.Second)
	defer cancel()
	r.flush(ctx)
}

func (r *recorder) flush(ctx context.Context) {
	if len(r.buf) == 0 {
		return
	}
	stmts := make([]stmt, len(r.buf))
	for i, row := range r.buf {
		stmts[i] = stmt{sql: r.insert, args: row}
	}
	if err := r.db.batch(ctx, stmts); err != nil {
		r.logger.Warn("visionary: system metrics write failed", "rows", len(r.buf), "err", err)
		// Keep at most an hour of samples while the database is unreachable.
		if keep := int(time.Hour / r.cfg.Interval); len(r.buf) > keep {
			r.buf = r.buf[len(r.buf)-keep:]
		}
		return
	}
	r.buf = r.buf[:0]
}

// sample reads every source once. A source that fails (load average where
// unsupported, file descriptors without permission) is stored as NULL.
func (r *recorder) sample() []any {
	now := time.Now()
	secs := now.Sub(r.last).Seconds()
	r.last = now
	if secs <= 0 {
		secs = r.cfg.Interval.Seconds()
	}
	v := make(map[string]any, len(systemColumns))
	v["created_at"] = now.UTC().Format(tsLayout)
	v["instance"] = r.ident.instance
	v["service"] = nullable(r.ident.service)
	v["version"] = nullable(r.ident.version)
	v["sdk"] = "go"
	v["runtime"] = runtime.Version()
	v["interval_s"] = round(secs, 2)

	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		v["host_cpu_percent"] = round(pct[0], 1)
	}
	if avg, err := load.Avg(); err == nil {
		v["load_1"], v["load_5"], v["load_15"] = round(avg.Load1, 2), round(avg.Load5, 2), round(avg.Load15, 2)
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		v["host_mem_used_mb"] = round(mb(vm.Used), 1)
		v["host_mem_total_mb"] = round(mb(vm.Total), 1)
		v["host_mem_percent"] = round(vm.UsedPercent, 1)
	}
	if du, err := disk.Usage(r.diskDir); err == nil {
		v["disk_used_percent"] = round(du.UsedPercent, 1)
		v["disk_free_gb"] = round(float64(du.Free)/(1<<30), 2)
	}
	if io, err := gnet.IOCounters(false); err == nil && len(io) > 0 {
		rx, tx := io[0].BytesRecv, io[0].BytesSent
		if rx >= r.netRx && tx >= r.netTx {
			v["net_rx_kbps"] = round(float64(rx-r.netRx)/1024/secs, 1)
			v["net_tx_kbps"] = round(float64(tx-r.netTx)/1024/secs, 1)
		}
		r.netRx, r.netTx = rx, tx
	}

	if r.proc != nil {
		if pct, err := r.proc.Percent(0); err == nil {
			v["proc_cpu_percent"] = round(pct, 1)
		}
		if mi, err := r.proc.MemoryInfo(); err == nil {
			v["proc_rss_mb"] = round(mb(mi.RSS), 1)
		}
		if n, err := r.proc.NumThreads(); err == nil {
			v["proc_threads"] = int64(n)
		}
		if n, err := r.proc.NumFDs(); err == nil {
			v["proc_open_fds"] = int64(n)
		}
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	v["goroutines"] = int64(runtime.NumGoroutine())
	v["heap_alloc_mb"] = round(mb(ms.HeapAlloc), 1)
	v["heap_inuse_mb"] = round(mb(ms.HeapInuse), 1)
	v["heap_objects"] = int64(ms.HeapObjects)
	v["gc_count"] = int64(ms.NumGC - r.numGC)
	gcPauseMs := float64(ms.PauseTotalNs-r.pauseNs) / 1e6
	v["gc_pause_ms"] = round(gcPauseMs, 2)
	// GCCPUFraction covers the whole run; this is the GC share of this interval.
	if gc, total := r.readGCCPU(); total > r.prevCPU && gc >= r.prevGC {
		v["gc_cpu_percent"] = round(100*(gc-r.prevGC)/(total-r.prevCPU), 2)
		r.prevGC, r.prevCPU = gc, total
	}
	r.numGC, r.pauseNs = ms.NumGC, ms.PauseTotalNs

	if r.cfg.DB != nil {
		st := r.cfg.DB()
		v["db_total_conns"] = st.Total
		v["db_idle_conns"] = st.Idle
		v["db_acquired_conns"] = st.InUse
		v["db_max_conns"] = st.Max
		if !st.noAcquires {
			v["db_acquires"] = st.Acquires - r.dbPrev.Acquires
		}
		v["db_empty_acquires"] = st.EmptyAcquires - r.dbPrev.EmptyAcquires
		v["db_acquire_wait_ms"] = round(float64(st.AcquireWait-r.dbPrev.AcquireWait)/float64(time.Millisecond), 1)
		r.dbPrev = st
	}

	reqs, errs := r.http.requests.Load(), r.http.errors.Load()
	dReqs, dErrs := reqs-r.requests, errs-r.errors
	r.requests, r.errors = reqs, errs
	v["http_requests"] = dReqs
	v["http_errors"] = dErrs
	v["http_rps"] = round(float64(dReqs)/secs, 2)
	v["http_in_flight"] = r.http.inFlight.Load()

	v["uptime_s"] = int64(time.Since(r.started).Seconds())
	r.sampleExtra(v, secs)

	row := make([]any, len(systemColumns))
	for i, c := range systemColumns {
		row[i] = v[c] // missing sources stay nil (NULL)
	}
	return row
}

// readGCCPU returns the runtime's estimates of CPU seconds spent on GC and
// available in total (GOMAXPROCS over wall time), both since start.
func (r *recorder) readGCCPU() (gc, total float64) {
	metrics.Read(r.gcCPU[:])
	if r.gcCPU[0].Value.Kind() != metrics.KindFloat64 || r.gcCPU[1].Value.Kind() != metrics.KindFloat64 {
		return 0, 0
	}
	return r.gcCPU[0].Value.Float64(), r.gcCPU[1].Value.Float64()
}

// SQLDB reports pool stats for a database/sql pool.
func SQLDB(db *sql.DB) func() DBStats {
	return func() DBStats {
		s := db.Stats()
		return DBStats{
			Total:         int64(s.OpenConnections),
			Idle:          int64(s.Idle),
			InUse:         int64(s.InUse),
			Max:           int64(s.MaxOpenConnections),
			EmptyAcquires: s.WaitCount,
			AcquireWait:   s.WaitDuration,
			noAcquires:    true,
		}
	}
}

// goos is runtime.GOOS, named so sampleExtra reads clearly.
const goos = runtime.GOOS

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

func round(v float64, prec int) float64 {
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}
