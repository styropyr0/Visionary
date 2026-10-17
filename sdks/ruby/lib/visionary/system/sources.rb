# frozen_string_literal: true

require "etc"

module Visionary
  module System
    # Readers for host and process figures. Linux has /proc; elsewhere most
    # host figures are unavailable and stay NULL. Every reader returns nil
    # (or an empty Hash) when its source fails.
    module Sources
      LINUX = RUBY_PLATFORM.include?("linux")
      WINDOWS = Gem.win_platform?
      CPU_FIELDS = %i[user nice system idle iowait irq softirq steal guest guest_nice].freeze
      # Partitions and virtual devices whose I/O is already counted in a
      # whole disk, so totals are not doubled.
      PARTITION = /\A((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|dm-\d+|sr\d+|fd\d+|md\d+)\z/

      module_function

      def quiet
        yield
      rescue StandardError, NotImplementedError
        nil
      end

      def read(path)
        File.read(path)
      rescue SystemCallError, IOError
        nil
      end

      # /proc/stat: aggregate CPU times, context switches, running and
      # blocked processes.
      def proc_stat
        out = {}
        read("/proc/stat")&.each_line do |line|
          parts = line.split
          case parts[0]
          when "cpu" then out[:cpu] = CPU_FIELDS.zip(parts[1..].map(&:to_f)).to_h.reject { |_, v| v.nil? }
          when "ctxt", "procs_running", "procs_blocked" then out[parts[0].to_sym] = parts[1].to_i
          end
        end
        out
      end

      def loadavg
        f = read("/proc/loadavg")&.split
        f && f.size >= 3 ? f[0, 3].map(&:to_f) : nil
      end

      # /proc/meminfo in bytes.
      def meminfo
        out = {}
        read("/proc/meminfo")&.each_line do |line|
          name, rest = line.split(":", 2)
          fields = rest.to_s.split
          next if fields.empty?
          out[name] = fields[0].to_i * (fields[1] == "kB" ? 1024 : 1)
        end
        out
      end

      # Whole disks: name => [read bytes, written bytes, reads, writes, ms
      # reading + writing, ms doing I/O].
      def diskstats
        out = {}
        read("/proc/diskstats")&.each_line do |line|
          p = line.split
          next if p.size < 14 || PARTITION.match?(p[2])
          out[p[2]] = [p[5].to_i * 512, p[9].to_i * 512, p[3].to_i, p[7].to_i, p[6].to_i + p[10].to_i, p[12].to_i]
        end
        out
      end

      # All interfaces: [rx bytes, tx bytes, rx packets, tx packets, errors, drops].
      def net_dev
        text = read("/proc/net/dev") or return nil
        sums = [0, 0, 0, 0, 0, 0]
        text.each_line do |line|
          next unless line.include?(":")
          p = line.split(":", 2)[1].split
          next if p.size < 12
          sums[0] += p[0].to_i
          sums[1] += p[8].to_i
          sums[2] += p[1].to_i
          sums[3] += p[9].to_i
          sums[4] += p[2].to_i + p[10].to_i
          sums[5] += p[3].to_i + p[11].to_i
        end
        sums
      end

      # /proc/net/snmp Tcp counters (CurrEstab, RetransSegs, ...).
      def tcp
        rows = read("/proc/net/snmp")&.each_line&.select { |l| l.start_with?("Tcp:") }&.map(&:split)
        return {} unless rows && rows.size >= 2
        rows[0][1..].zip(rows[1][1..].map(&:to_i)).to_h
      end

      # VmRSS (bytes) and Threads from /proc/self/status.
      def self_status
        out = {}
        read("/proc/self/status")&.each_line do |line|
          if line.start_with?("VmRSS:") then out[:rss] = line.split[1].to_i * 1024
          elsif line.start_with?("Threads:") then out[:threads] = line.split[1].to_i
          end
        end
        out
      end

      def self_io
        vals = {}
        read("/proc/self/io")&.each_line do |line|
          k, v = line.split(":", 2)
          vals[k.strip] = v.to_i
        end
        vals.key?("read_bytes") ? [vals["read_bytes"], vals["write_bytes"].to_i] : nil
      end

      def open_fds
        quiet { Dir.children("/proc/self/fd").size } if LINUX
      end

      def host_procs
        quiet { Dir.children("/proc").count { |n| n.match?(/\A\d+\z/) } } if LINUX
      end

      def max_fds
        soft = quiet { Process.getrlimit(:NOFILE)[0] }
        return nil unless soft
        soft.positive? && soft < (1 << 40) && soft != Process::RLIM_INFINITY ? soft : nil
      end

      def cpu_count = quiet { Etc.nprocessors }

      def process_cpu_seconds
        quiet { Process.clock_gettime(Process::CLOCK_PROCESS_CPUTIME_ID) } ||
          quiet { Process.times.then { |t| t.utime + t.stime } }
      end

      # The filesystem holding path, from df (POSIX output, 1 KiB blocks):
      # { total:, used:, free: } in bytes, plus inode use when df knows it.
      def disk(path)
        return nil if WINDOWS || @no_df
        blocks = df(["-Pk", path]) or return nil
        out = { total: blocks[1] * 1024, used: blocks[2] * 1024, free: blocks[3] * 1024 }
        if LINUX && (inodes = df(["-Pi", path])) && inodes[1].positive?
          out[:inodes_percent] = inodes[2] * 100.0 / inodes[1]
        end
        out
      end

      def df(args)
        text = IO.popen(["df", *args], err: File::NULL, &:read)
        fields = text.lines[1..].to_a.join(" ").split
        nums = fields[1, 3].to_a
        return nil unless nums.size == 3 && nums.all? { |n| n.match?(/\A\d+\z/) }
        [fields[0], *nums.map(&:to_i)]
      rescue Errno::ENOENT
        @no_df = true
        nil
      rescue StandardError
        nil
      end

      # Ruby heap: live slots and the pages holding them.
      def heap
        st = GC.stat
        consts = defined?(GC::INTERNAL_CONSTANTS) ? GC::INTERNAL_CONSTANTS : {}
        slot = consts[:BASE_SLOT_SIZE] || consts[:RVALUE_SIZE] || 40
        page = consts[:HEAP_PAGE_SIZE]
        {
          live: st[:heap_live_slots],
          alloc_bytes: st[:heap_live_slots] && st[:heap_live_slots] * slot,
          inuse_bytes: page && st[:heap_allocated_pages] && st[:heap_allocated_pages] * page,
          gc_count: GC.count,
          gc_ms: st[:time]
        }
      end

      # The app's ActiveRecord pool, when it has one.
      def active_record_pool
        return nil unless defined?(::ActiveRecord::Base) && ::ActiveRecord::Base.respond_to?(:connection_pool)
        st = ::ActiveRecord::Base.connection_pool.stat
        { total: st[:connections], idle: st[:idle], in_use: st[:busy], max: st[:size] }
      rescue StandardError
        nil
      end
    end
  end
end
