# frozen_string_literal: true

module Visionary
  module System
    # Pressure stall information and virtual memory figures from small Linux
    # files, once per sample (sdks/go/hoststats is the reference). In a
    # container pressure comes from the cgroup v2 directory, since
    # /proc/pressure is the whole machine's
    class HostStats
      PRESSURE = [%w[cpu psi_cpu], %w[memory psi_mem], %w[io psi_io]].freeze

      # root and linux are for tests
      def initialize(container: false, cgroup_dir: nil, root: "/", linux: Sources::LINUX)
        @container = container
        @cgroup_dir = cgroup_dir
        @proc = File.join(root, "proc")
        @linux = linux
      end

      def prime
        @vm = read_vmstat if @linux
      end

      # Adds the columns it could read to v. secs is the time since the
      # previous sample, for rates
      def sample(v, secs)
        return unless @linux && secs.positive?
        sample_pressure(v)
        sample_vm(v, secs)
      end

      def self.parse_pressure(text)
        out = {}
        text.to_s.each_line do |line|
          kind, rest = line.split(" ", 2)
          a10 = pressure_field(rest, "avg10=")
          a60 = pressure_field(rest, "avg60=")
          out[kind] = [a10, a60] if a10 && a60 && %w[some full].include?(kind)
        end
        out["some"] ? out : nil
      end

      def self.pressure_field(rest, key)
        m = rest.to_s.match(/#{key}(\S+)/) or return nil
        Float(m[1], exception: false)
      end

      # Sums reclaim over kswapd, direct and khugepaged scans, but not
      # pgscan_anon or pgscan_file, which split the same pages by type
      def self.parse_vmstat(text)
        s = { pgscan: 0, pgsteal: 0 }
        each_kv(text, " ") do |k, n|
          case k
          when "pgfault", "pgmajfault", "pswpin", "pswpout", "oom_kill" then s[k.to_sym] = n
          when "pgscan_direct_throttle" then nil
          when /\Apgscan_(kswapd|direct|khugepaged)/ then s[:pgscan] += n
          when /\Apgsteal_(kswapd|direct|khugepaged)/ then s[:pgsteal] += n
          end
        end
        s[:pgfault] ? s : nil
      end

      # The /proc/meminfo fields in kB, nil unless Dirty, Committed_AS and
      # CommitLimit are all there
      def self.parse_meminfo(text)
        m = {}
        each_kv(text, ":") { |k, n| m[k] = n }
        %w[Dirty Committed_AS CommitLimit].all? { |k| m.key?(k) } ? m : nil
      end

      # The fault counters of a cgroup's memory.stat, v2 or v1
      def self.parse_cgroup_mem_stat(text)
        faults = major = nil
        each_kv(text, " ") do |k, n|
          case k
          when "pgfault", "total_pgfault" then faults = n
          when "pgmajfault", "total_pgmajfault" then major = n
          end
        end
        faults ? [faults, major.to_i] : nil
      end

      # high and max of a v2 memory.events
      def self.parse_cgroup_mem_events(text)
        ev = {}
        each_kv(text, " ") { |k, n| ev[k] = n if %w[high max].include?(k) }
        ev.size == 2 ? [ev["high"], ev["max"]] : nil
      end

      # "key<sep> value [unit]" lines with an unsigned value
      def self.each_kv(text, sep)
        text.to_s.each_line do |line|
          i = line.index(sep)
          next if i.nil? || i.zero?
          n = line[(i + 1)..].split.first
          yield line[0, i], n.to_i if n&.match?(/\A\d+\z/)
        end
      end

      private

      def sample_pressure(v)
        if @container
          return unless @cgroup_dir
          files = PRESSURE.map { |name, _| File.join(@cgroup_dir, "#{name}.pressure") }
        else
          files = PRESSURE.map { |name, _| File.join(@proc, "pressure", name) }
        end
        PRESSURE.each_with_index do |(name, col), i|
          p = self.class.parse_pressure(Sources.read(files[i])) or next
          v["#{col}_some_avg10"], v["#{col}_some_avg60"] = p["some"]
          v["#{col}_full_avg10"] = p["full"][0] if p["full"] && name != "cpu"
        end
      end

      def read_vmstat = self.class.parse_vmstat(Sources.read(File.join(@proc, "vmstat")))

      def sample_vm(v, secs)
        now = read_vmstat
        if now && (p = @vm)
          rate = ->(col, a, b) { v[col] = ((a - b) / secs).round(1) if a >= b }
          if now[:pgfault] >= now[:pgmajfault].to_i && p[:pgfault] >= p[:pgmajfault].to_i
            rate.call("minor_faults_ps", now[:pgfault] - now[:pgmajfault].to_i, p[:pgfault] - p[:pgmajfault].to_i)
          end
          rate.call("major_faults_ps", now[:pgmajfault].to_i, p[:pgmajfault].to_i)
          rate.call("swap_in_ps", now[:pswpin].to_i, p[:pswpin].to_i)
          rate.call("swap_out_ps", now[:pswpout].to_i, p[:pswpout].to_i)
          rate.call("pages_scanned_ps", now[:pgscan], p[:pgscan])
          rate.call("pages_stolen_ps", now[:pgsteal], p[:pgsteal])
          v["host_oom_kills"] = now[:oom_kill] - p[:oom_kill] if now[:oom_kill] && p[:oom_kill] && now[:oom_kill] >= p[:oom_kill]
        end
        @vm = now
        m = self.class.parse_meminfo(Sources.read(File.join(@proc, "meminfo"))) or return
        { "Dirty" => "mem_dirty_mb", "Writeback" => "mem_writeback_mb", "Slab" => "mem_slab_mb", "Shmem" => "mem_shmem_mb",
          "Committed_AS" => "mem_committed_mb", "CommitLimit" => "mem_commit_limit_mb" }.each do |k, col|
          v[col] = (m[k].to_i / 1024.0).round(1)
        end
      end
    end
  end
end
