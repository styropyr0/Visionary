# frozen_string_literal: true

module Visionary
  module System
    # Reads the CPU and memory limits and use of the process's own cgroup
    # (Linux, v2 or v1): in a container these are the numbers that matter,
    # since the host columns describe the whole machine underneath. The
    # container_* columns are filled only in a container, or when the cgroup
    # sets a CPU or memory limit (a systemd unit with MemoryMax=, say).
    class Cgroup
      MB = 1024.0 * 1024
      MARKERS = %w[docker kubepods containerd libpod lxc].freeze
      # Variables container platforms set in every container: Kubernetes,
      # Railway, Render, Heroku, Cloud Run, ECS/Fargate and Azure Container Apps.
      CONTAINER_ENV = %w[KUBERNETES_SERVICE_HOST RAILWAY_SERVICE_ID RENDER_SERVICE_ID DYNO K_SERVICE ECS_CONTAINER_METADATA_URI_V4 CONTAINER_APP_NAME].freeze

      # The process's cgroup; nil off Linux or when there is none to read.
      # root and env are for tests.
      def self.detect(root: "/", env: ENV)
        return nil unless Sources::LINUX || root != "/"
        text = Sources.read(File.join(root, "proc/self/cgroup")) or return nil
        c = new(root, text, env)
        c.cpu_dir || c.mem_dir ? c : nil
      end

      attr_reader :cpu_dir, :mem_dir

      def initialize(root, text, env)
        @root = root
        @in_ctr = in_container?(text, env)
        @v2 = false
        @cpu_dir = @cpu_acct = @mem_dir = nil
        text.strip.each_line do |line|
          id, ctrls, path = line.chomp.split(":", 3)
          next if path.nil?
          list = ctrls.split(",")
          if id == "0" && ctrls.empty?
            dir = dir_for("sys/fs/cgroup", path)
            if File.exist?(File.join(dir, "cpu.stat"))
              @v2 = true
              @cpu_dir = @mem_dir = dir
              break
            end
          elsif list.include?("memory")
            @mem_dir = dir_for("sys/fs/cgroup/memory", path)
          elsif list.include?("cpu") || list.include?("cpuacct")
            dir = dir_for("sys/fs/cgroup/#{ctrls}", path)
            @cpu_dir = dir if list.include?("cpu")
            @cpu_acct = dir if list.include?("cpuacct")
          end
        end
        @primed = false
      end

      # The baseline the first sample's deltas start from.
      def prime
        @usage, @periods, @throttled, @ooms = counters
        @last = mono
        @primed = true
      end

      # Fills the container_* columns of v.
      def sample(v)
        cpus, mem_limit = limits
        return if !@in_ctr && cpus.zero? && mem_limit.zero?
        now = mono
        usage, periods, throttled, ooms = counters
        secs = @primed ? now - @last : 0
        if secs.positive? && usage >= @usage
          v["container_cpu_cores"] = ((usage - @usage) / 1e9 / secs).round(3)
          if periods > @periods && throttled >= @throttled
            v["container_cpu_throttled_percent"] = (100.0 * (throttled - @throttled) / (periods - @periods)).round(1)
          elsif cpus.positive?
            v["container_cpu_throttled_percent"] = 0.0
          end
          v["container_oom_kills"] = ooms - @ooms if ooms && @ooms && ooms >= @ooms
        end
        @usage, @periods, @throttled, @ooms = usage, periods, throttled, ooms
        @last = now
        @primed = true
        v["container_cpu_limit"] = cpus.round(2) if cpus.positive?
        if (used = mem_used)
          v["container_mem_used_mb"] = (used / MB).round(1)
        end
        v["container_mem_limit_mb"] = (mem_limit / MB).round(1) if mem_limit.positive?
      end

      private

      def mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)

      def path(*parts) = File.join(@root, *parts)

      # The cgroup's directory under a controller's mount. Inside a
      # container's cgroup namespace the path is "/", and with a private mount
      # the host path does not exist below it, so the mount itself is the
      # cgroup.
      def dir_for(mount, cgroup_path)
        dir = File.join(@root, mount, cgroup_path)
        File.directory?(dir) ? dir : File.join(@root, mount)
      end

      # Tells a container from a process on the host by the runtimes' marker
      # files, the platforms' variables and the cgroup path.
      def in_container?(text, env)
        File.exist?(path(".dockerenv")) || File.exist?(path("run/.containerenv")) ||
          CONTAINER_ENV.any? { |k| !env[k].to_s.empty? } || MARKERS.any? { |k| text.include?(k) }
      end

      def read_int(file)
        s = Sources.read(file)&.strip
        s&.match?(/\A\d+\z/) ? s.to_i : nil
      end

      # "key value" lines (cpu.stat, memory.stat, memory.events).
      def read_kv(file)
        out = {}
        Sources.read(file)&.each_line do |line|
          k, n, extra = line.split
          out[k] = n.to_i if extra.nil? && n&.match?(/\A\d+\z/)
        end
        out
      end

      # The CPU limit in cores and the memory limit in bytes, 0 when
      # unlimited.
      def limits
        cpus = 0.0
        mem = 0
        if @v2
          q, p = Sources.read(File.join(@cpu_dir, "cpu.max"))&.split
          cpus = q.to_f / p.to_f if q && q != "max" && p.to_f.positive?
          mem = read_int(File.join(@mem_dir, "memory.max")) || 0 # "max": 0
          return [cpus, mem]
        end
        if @cpu_dir
          q = Sources.read(File.join(@cpu_dir, "cpu.cfs_quota_us")).to_s.strip.to_i
          p = read_int(File.join(@cpu_dir, "cpu.cfs_period_us"))
          cpus = q.to_f / p if q.positive? && p&.positive?
        end
        if @mem_dir
          # v1 writes a page-rounded maximum int64 for no limit.
          m = read_int(File.join(@mem_dir, "memory.limit_in_bytes"))
          mem = m if m && m < 2**62
        end
        [cpus, mem]
      end

      # The cumulative CPU time (ns), CFS periods, throttled periods and OOM
      # kills (nil where the kernel doesn't count them).
      def counters
        if @v2
          st = read_kv(File.join(@cpu_dir, "cpu.stat"))
          ev = read_kv(File.join(@mem_dir, "memory.events"))
          return [st["usage_usec"].to_i * 1000, st["nr_periods"].to_i, st["nr_throttled"].to_i, ev["oom_kill"]]
        end
        usage = @cpu_acct ? read_int(File.join(@cpu_acct, "cpuacct.usage")).to_i : 0
        st = @cpu_dir ? read_kv(File.join(@cpu_dir, "cpu.stat")) : {}
        ooms = @mem_dir ? read_kv(File.join(@mem_dir, "memory.oom_control"))["oom_kill"] : nil
        [usage, st["nr_periods"].to_i, st["nr_throttled"].to_i, ooms]
      end

      # Memory in use without the inactive page cache, as docker stats
      # counts it.
      def mem_used
        return nil unless @mem_dir
        file, key = @v2 ? %w[memory.current inactive_file] : %w[memory.usage_in_bytes total_inactive_file]
        used = read_int(File.join(@mem_dir, file)) or return nil
        inactive = read_kv(File.join(@mem_dir, "memory.stat"))[key]
        inactive && inactive < used ? used - inactive : used
      end
    end
  end
end
