# frozen_string_literal: true

module Visionary
  module System
    # Throughput seen by the middleware, read by the sampler.
    class HttpCounters
      def initialize
        @mutex = Mutex.new
        @requests = @errors = @in_flight = 0
      end

      def started
        @mutex.synchronize { @in_flight += 1 }
      end

      def finished(status)
        @mutex.synchronize do
          @in_flight -= 1
          @requests += 1
          @errors += 1 if status >= 500
        end
      end

      def snapshot = @mutex.synchronize { [@requests, @errors, @in_flight] }
    end

    # Samples host, process and runtime figures every interval on a
    # background thread and writes them four at a time.
    class Recorder
      SAMPLES_PER_WRITE = 4
      MB = 1024.0 * 1024

      def initialize(cfg, instance, store, schemas, http, logger)
        @cfg = cfg
        @interval = cfg.system_interval.to_f
        @instance = instance
        @store = store
        @schemas = schemas
        @http = http
        @logger = logger
        @disk_path = cfg.disk_path.to_s.empty? ? Dir.pwd : cfg.disk_path
        @sql = Schema.insert_sql("system_metrics", store.string_params?)
        @started = mono
        reset
      end

      attr_writer :http

      def start
        prime
        @thread = Thread.new { run }
        @thread.name = "visionary-system" if @thread.respond_to?(:name=)
        self
      end

      def after_fork
        reset
        start
      end

      def close(timeout)
        @mutex.synchronize do
          @stopped = true
          @cond.broadcast
        end
        @thread.nil? || !@thread.join(timeout).nil?
      end

      # One row in Schema::TABLES["system_metrics"] order. A source that
      # fails or doesn't exist here is NULL, never an error.
      def sample
        now = mono
        secs = now - @last
        @last = now
        secs = @interval if secs <= 0
        v = { "created_at" => Visionary.timestamp(Time.now), "instance" => @instance, "interval_s" => secs.round(2) }
        stat = Sources::LINUX ? Sources.proc_stat : {}
        tcp = Sources::LINUX ? Sources.tcp : {}

        host(v, stat, secs)
        process(v)
        runtime(v, secs)
        db(v)
        throughput(v, secs, now)
        node_gauges(v, stat, tcp)
        counters = read_counters(stat, tcp)
        node_deltas(v, counters, @prev, secs)
        @prev = counters
        v["service"] = @cfg.service
        v["version"] = @cfg.version
        v["sdk"] = "ruby"
        v["runtime"] = Telemetry.runtime
        @cgroup&.sample(v)
        Schema::TABLES["system_metrics"].map { |c| v[c] }
      end

      private

      def reset
        @mutex = Mutex.new
        @cond = ConditionVariable.new
        @stopped = false
        @thread = nil
        @buf = []
      end

      def mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)

      # Baseline readings so the first sample has real deltas.
      def prime
        @last = mono
        @requests, @errors, = @http.snapshot
        stat = Sources::LINUX ? Sources.proc_stat : {}
        @cpu_prev = stat[:cpu]
        @net_prev = Sources.net_dev
        @proc_cpu_prev = Sources.process_cpu_seconds
        @proc_cpu_at = mono
        heap = Sources.heap
        @gc_count_prev = heap[:gc_count]
        @gc_ms_prev = heap[:gc_ms]
        @db_prev = db_stats || {}
        @prev = read_counters(stat, Sources::LINUX ? Sources.tcp : {})
        @cgroup = Cgroup.detect # nil off Linux
        @cgroup&.prime
      end

      def run
        Telemetry.internal!
        @schemas.ensure(:system)
        next_at = mono + @interval
        loop do
          break if wait_until(next_at)
          next_at += @interval
          next_at = mono + @interval if next_at < mono # fell behind (suspended host); don't burst
          begin
            @buf << sample
          rescue StandardError => e
            @logger.error("visionary: system sample failed: #{e.class}: #{e.message}")
          end
          flush if @buf.size >= SAMPLES_PER_WRITE
        end
        flush
      rescue StandardError => e
        @logger.error("visionary: system sampler stopped: #{e.message}")
      end

      # True once stopped.
      def wait_until(at)
        @mutex.synchronize do
          until @stopped || (left = at - mono) <= 0
            @cond.wait(@mutex, left)
          end
          @stopped
        end
      end

      # Keeps the rows (at most an hour's worth) when the write fails.
      def flush
        return if @buf.empty?
        @schemas.ensure(:system)
        begin
          @store.batch(@buf.map { |row| [@sql, Store.fit(@store.dialect, "system_metrics", row)] })
          @buf.clear
        rescue StandardError => e
          @logger.warn("visionary: system metrics write failed (#{@buf.size} rows kept): #{e.message}")
          keep = [1, (3600 / @interval).to_i].max
          @buf.shift(@buf.size - keep) if @buf.size > keep
        end
      end

      def host(v, stat, secs)
        if (cpu = stat[:cpu]) && @cpu_prev
          total = cpu_total(cpu) - cpu_total(@cpu_prev)
          if total.positive?
            idle = (cpu[:idle].to_f + cpu[:iowait].to_f) - (@cpu_prev[:idle].to_f + @cpu_prev[:iowait].to_f)
            v["host_cpu_percent"] = (100.0 * (total - idle) / total).clamp(0, 100).round(1)
          end
        end
        @cpu_prev = stat[:cpu]
        if (load = Sources::LINUX && Sources.loadavg)
          v["load_1"], v["load_5"], v["load_15"] = load.map { |x| x.round(2) }
        end
        mem = Sources::LINUX ? Sources.meminfo : {}
        if (total = mem["MemTotal"]).to_i.positive?
          cached = mem["Cached"].to_i + mem["SReclaimable"].to_i
          # psutil's definition: used is what isn't available.
          available = mem["MemAvailable"] || (mem["MemFree"].to_i + mem["Buffers"].to_i + cached)
          used = total - available
          v["host_mem_used_mb"] = (used / MB).round(1)
          v["host_mem_total_mb"] = (total / MB).round(1)
          v["host_mem_percent"] = (100.0 * used / total).round(1)
          v["host_mem_available_mb"] = (mem["MemAvailable"] / MB).round(1) if mem["MemAvailable"]
          v["host_mem_cached_mb"] = (cached / MB).round(1) if cached.positive?
          if mem["SwapTotal"]
            v["swap_total_mb"] = (mem["SwapTotal"] / MB).round(1)
            v["swap_used_mb"] = ((mem["SwapTotal"] - mem["SwapFree"].to_i) / MB).round(1)
          end
        end
        if (du = Sources.disk(@disk_path)) && du[:total].positive?
          v["disk_used_percent"] = (100.0 * du[:used] / (du[:used] + du[:free])).round(1)
          v["disk_free_gb"] = (du[:free] / (MB * 1024)).round(2)
          v["disk_total_gb"] = (du[:total] / (MB * 1024)).round(2)
          v["disk_inodes_percent"] = du[:inodes_percent].round(1) if du[:inodes_percent]
        end
        net = Sources.net_dev
        if net && @net_prev && net[0] >= @net_prev[0] && net[1] >= @net_prev[1]
          v["net_rx_kbps"] = ((net[0] - @net_prev[0]) / 1024.0 / secs).round(1)
          v["net_tx_kbps"] = ((net[1] - @net_prev[1]) / 1024.0 / secs).round(1)
        end
        @net_prev = net
      end

      def process(v)
        cpu = Sources.process_cpu_seconds
        # Timed on its own: under GVL contention the readers before this can
        # take seconds, which would skew a share of the sample's interval.
        at = mono
        v["proc_cpu_percent"] = (100.0 * (cpu - @proc_cpu_prev) / (at - @proc_cpu_at)).round(1) if cpu && @proc_cpu_prev && at > @proc_cpu_at
        @proc_cpu_prev = cpu
        @proc_cpu_at = at
        st = Sources::LINUX ? Sources.self_status : {}
        v["proc_rss_mb"] = (st[:rss] / MB).round(1) if st[:rss]
        v["proc_threads"] = st[:threads] || Thread.list.size
        v["proc_open_fds"] = Sources.open_fds
      end

      def runtime(v, secs)
        v["goroutines"] = Thread.list.count(&:alive?)
        heap = Sources.heap
        v["heap_alloc_mb"] = (heap[:alloc_bytes] / MB).round(1) if heap[:alloc_bytes]
        v["heap_inuse_mb"] = (heap[:inuse_bytes] / MB).round(1) if heap[:inuse_bytes]
        v["heap_objects"] = heap[:live]
        v["gc_count"] = heap[:gc_count] - @gc_count_prev
        if heap[:gc_ms] && @gc_ms_prev
          ms = (heap[:gc_ms] - @gc_ms_prev).to_f
          v["gc_pause_ms"] = ms.round(2)
          v["gc_cpu_percent"] = (ms / (secs * 1000.0) * 100.0).round(2)
        end
        @gc_count_prev = heap[:gc_count]
        @gc_ms_prev = heap[:gc_ms]
      end

      def db_stats
        st = @cfg.db_stats ? Sources.quiet { @cfg.db_stats.call } : Sources.active_record_pool
        st.is_a?(Hash) ? st.transform_keys(&:to_sym) : nil
      end

      def db(v)
        st = db_stats or return
        { total: "db_total_conns", idle: "db_idle_conns", in_use: "db_acquired_conns", max: "db_max_conns" }.each do |k, col|
          v[col] = st[k].to_i unless st[k].nil?
        end
        { acquires: "db_acquires", empty_acquires: "db_empty_acquires" }.each do |k, col|
          v[col] = st[k].to_i - @db_prev[k].to_i unless st[k].nil?
        end
        v["db_acquire_wait_ms"] = (st[:acquire_wait_ms].to_f - @db_prev[:acquire_wait_ms].to_f).round(1) unless st[:acquire_wait_ms].nil?
        @db_prev = st
      end

      def throughput(v, secs, now)
        reqs, errs, in_flight = @http.snapshot
        d_reqs = reqs - @requests
        v["http_requests"] = d_reqs
        v["http_errors"] = errs - @errors
        v["http_rps"] = (d_reqs / secs).round(2)
        v["http_in_flight"] = in_flight
        @requests = reqs
        @errors = errs
        v["uptime_s"] = (now - @started).to_i
      end

      def node_gauges(v, stat, tcp)
        v["cpu_count"] = Sources.cpu_count
        v["host_procs"] = Sources.host_procs
        v["procs_running"] = stat[:procs_running]
        v["procs_blocked"] = stat[:procs_blocked]
        v["tcp_established"] = tcp["CurrEstab"]
        v["proc_max_fds"] = Sources.max_fds
      end

      # Cumulative readings; a key is present only when its source worked.
      def read_counters(stat, tcp)
        c = {}
        c[:cpu] = stat[:cpu] if stat[:cpu]
        if Sources::LINUX && !(disks = Sources.diskstats).empty?
          c[:disk] = (0..4).map { |i| disks.values.sum { |d| d[i] } } << disks.transform_values { |d| d[5] }
        end
        if (net = Sources.net_dev)
          c[:net] = net[2, 4]
        end
        c[:tcp_retrans] = tcp["RetransSegs"] if tcp["RetransSegs"]
        c[:ctxt] = stat[:ctxt] if stat[:ctxt]
        if Sources::LINUX && (io = Sources.self_io)
          c[:proc_io] = io
        end
        c
      end

      def node_deltas(v, now, prev, secs)
        if now[:cpu] && prev[:cpu]
          a = now[:cpu]
          b = prev[:cpu]
          total = cpu_total(a) - cpu_total(b)
          if total.positive?
            pct = ->(*f) { (100.0 * f.sum { |k| a[k].to_f - b[k].to_f } / total).round(1) }
            v["cpu_user_percent"] = pct.call(:user, :nice)
            v["cpu_system_percent"] = pct.call(:system, :irq, :softirq)
            v["cpu_iowait_percent"] = pct.call(:iowait)
            v["cpu_steal_percent"] = pct.call(:steal)
          end
        end
        if now[:disk] && prev[:disk]
          rb, wb, rc, wc, tm, busy = now[:disk]
          prb, pwb, prc, pwc, ptm, pbusy = prev[:disk]
          if rb >= prb && wb >= pwb
            reads = rc - prc
            writes = wc - pwc
            v["disk_read_kbps"] = ((rb - prb) / 1024.0 / secs).round(1)
            v["disk_write_kbps"] = ((wb - pwb) / 1024.0 / secs).round(1)
            v["disk_read_iops"] = (reads / secs).round(1)
            v["disk_write_iops"] = (writes / secs).round(1)
            v["disk_await_ms"] = ((tm - ptm).to_f / (reads + writes)).round(2) if (reads + writes).positive? && tm >= ptm
            busiest = busy.filter_map { |n, t| pbusy[n] && t >= pbusy[n] ? t - pbusy[n] : nil }.max || 0
            v["disk_busy_percent"] = [100.0, 100.0 * busiest / (secs * 1000.0)].min.round(1) unless busy.empty?
          end
        end
        if now[:net] && prev[:net]
          rx, tx, errs, drops = now[:net]
          prx, ptx, perrs, pdrops = prev[:net]
          if rx >= prx && tx >= ptx
            v["net_rx_pps"] = ((rx - prx) / secs).round(1)
            v["net_tx_pps"] = ((tx - ptx) / secs).round(1)
            v["net_errors"] = errs - perrs if errs >= perrs
            v["net_drops"] = drops - pdrops if drops >= pdrops
          end
        end
        if now[:tcp_retrans] && prev[:tcp_retrans] && now[:tcp_retrans] >= prev[:tcp_retrans]
          v["tcp_retrans"] = now[:tcp_retrans] - prev[:tcp_retrans]
        end
        if now[:ctxt] && prev[:ctxt] && now[:ctxt] >= prev[:ctxt]
          v["ctx_switches_ps"] = ((now[:ctxt] - prev[:ctxt]) / secs).round(1)
        end
        return unless now[:proc_io] && prev[:proc_io]
        (r, w), (pr, pw) = now[:proc_io], prev[:proc_io]
        return unless r >= pr && w >= pw
        v["proc_read_kbps"] = ((r - pr) / 1024.0 / secs).round(1)
        v["proc_write_kbps"] = ((w - pw) / 1024.0 / secs).round(1)
      end

      # Linux counts guest time inside user/nice already.
      def cpu_total(t)
        t.values.sum - t[:guest].to_f - t[:guest_nice].to_f
      end
    end
  end
end
