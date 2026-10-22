# frozen_string_literal: true

require "json"
require "rbconfig"

module Visionary
  # CPU profiles, switched on and off from the dashboard (Settings ->
  # Profiling, kept in visionary_settings). One background thread reads the
  # settings every 30 s and, while profiling is on, samples the stacks of the
  # threads that are running every 10 ms for profile.duration_ms every
  # profile.interval_s. Each profile is one profiles row: folded stacks plus
  # the time each route's requests took while it ran.
  #
  # Ruby has no CPU-time sampler without a native extension, so this walks
  # Thread.list and keeps the threads whose status is "run". Threads that
  # sleep, wait on a lock or queue, or block on IO report "sleep" and are
  # left out. Of the "run" threads only one holds the GVL at a time; the
  # others are waiting for it (or inside a C call that released it), so with
  # several busy threads each is counted, which over-counts waiting time.
  # Each sample stands for about 10 ms: a tick that comes late because a
  # busy thread held the GVL counts once per 10 ms it covers.
  class Profiler
    POLL_S = 30
    MAX_FOLDED = 90_000
    KEEP_WHOLE = 72_000 # the heaviest stacks kept whole; the rest roll up (see fold)
    MIN_SAMPLES = 20
    TICK_S = 0.01
    NOW_FRESH_S = 300
    DEFAULT_INTERVAL_S = 300.0
    DEFAULT_DURATION_MS = 10_000.0
    MAX_DURATION_MS = 120_000.0
    SETTINGS_SQL = "SELECT name, value FROM visionary_settings WHERE name LIKE 'profile.%'"

    # Requests seen by the middleware: how many started (a turn with none
    # new is skipped) and, while a profile runs, the ms each "METHOD route"
    # spent on requests that finished.
    module Tally
      @mutex = Mutex.new
      @started = 0
      @busy = nil

      class << self
        attr_reader :started

        # Under the GVL a lost increment is possible but harmless: only
        # "did it change" matters.
        def started!
          @started += 1
        end

        def busy? = !@busy.nil?

        def finished(method, route, ms)
          return unless @busy
          key = "#{method} #{route}"
          @mutex.synchronize { @busy[key] = (@busy[key] || 0.0) + ms.to_f if @busy }
        rescue StandardError
          nil
        end

        def begin!
          @mutex.synchronize { @busy = {} }
        end

        # The busy ms since begin!, and stops counting.
        def finish!
          @mutex.synchronize do
            out = @busy || {}
            @busy = nil
            out
          end
        end

        def after_fork
          @mutex = Mutex.new
          @busy = nil
        end
      end
    end

    Settings = Struct.new(:enabled, :services, :interval_s, :duration_ms, :now, keyword_init: true)

    class << self
      # visionary_settings rows ([[name, value], ...]) as Settings, with the
      # spec's defaults for missing or unreadable values.
      def parse_settings(rows)
        h = {}
        rows.each { |name, value| h[name.to_s] = value.to_s.strip }
        services = begin
          list = h["profile.services"].to_s.empty? ? nil : JSON.parse(h["profile.services"])
          list.is_a?(Array) ? list.map(&:to_s) : nil
        rescue JSON::ParserError
          nil
        end
        Settings.new(
          enabled: %w[true 1].include?(h["profile.enabled"].to_s.downcase),
          services: services,
          interval_s: positive(h["profile.interval_s"], DEFAULT_INTERVAL_S),
          duration_ms: [positive(h["profile.duration_ms"], DEFAULT_DURATION_MS), MAX_DURATION_MS].min,
          now: h["profile.now"].to_s.empty? ? nil : h["profile.now"]
        )
      end

      # A missing or empty list means every service; a service with no name
      # matches only that.
      def participates?(services, service)
        return true if services.nil? || services.empty?
        return false if service.to_s.empty?
        services.include?(service.to_s)
      end

      # profile.now asks for one profile when it is newer than this process's
      # start and less than five minutes old.
      def now_due?(value, started_at, now = Time.now)
        t = parse_time(value)
        !t.nil? && t > started_at && now - t < NOW_FRESH_S
      end

      # created_at text ("2026-10-22T10:00:00.000Z", or with a space and no Z)
      # as a UTC Time.
      def parse_time(s)
        m = s.to_s.strip.match(/\A(\d{4})-(\d\d)-(\d\d)[T ](\d\d):(\d\d):(\d\d)(\.\d+)?(Z|[+-]00:?00)?\z/)
        return nil unless m
        Time.utc(m[1].to_i, m[2].to_i, m[3].to_i, m[4].to_i, m[5].to_i, m[6].to_i + m[7].to_f)
      rescue ArgumentError
        nil
      end

      # Stack counts ({"a;b;c" => n}) as folded text, heaviest first within
      # MAX_FOLDED bytes (sdks/SPEC.md, "CPU profiles"): whole lines up to
      # KEEP_WHOLE, then the rest cut after the deepest frame they share with
      # a kept stack and ending in "(other)", then one "(other) n" for
      # whatever is left.
      def fold(counts)
        samples = counts.values.sum
        sorted = counts.sort_by { |stack, n| [-n, stack] }
        out = +""
        # Whole lines first, leaving room for the rolled-up ones.
        i = 0
        while i < sorted.size
          line = "#{sorted[i][0]} #{sorted[i][1]}\n"
          break if out.bytesize + line.bytesize > KEEP_WHOLE
          out << line
          i += 1
        end
        return [out, samples] if i == sorted.size

        # The rest are cut after the deepest frame they share with a kept
        # stack and end in "(other)", so their time stays under the right caller.
        prefixes = {}
        sorted[0...i].each do |stack, _|
          j = -1
          prefixes[stack[0...j]] = true while (j = stack.index(";", j + 1))
          prefixes[stack] = true
        end
        rolled = Hash.new(0)
        other = 0
        sorted[i..].each do |stack, n|
          cut = ""
          j = -1
          while (j = stack.index(";", j + 1))
            cut = stack[0...j] if prefixes[stack[0...j]]
          end
          cut = stack if prefixes[stack]
          if cut.empty?
            other += n
          elsif cut == stack # the whole stack leads into a kept one: nothing to cut
            rolled[stack] += n
          else
            rolled["#{cut};(other)"] += n
          end
        end
        rolled.sort_by { |stack, n| [-n, stack] }.each do |stack, n|
          line = "#{stack} #{n}\n"
          if out.bytesize + line.bytesize > MAX_FOLDED - 32 # room for the last "(other) n"
            other += n
          else
            out << line
          end
        end
        out << "(other) #{other}\n" if other.positive?
        [out, samples]
      end

      # Whether the sampler takes a stack of this thread: it is using the
      # CPU (or waiting for the GVL) and is not one of Visionary's own.
      def sample?(thread, current = Thread.current)
        thread.status == "run" && !own?(thread, current)
      rescue StandardError
        false
      end

      # The sampler itself and Visionary's background threads.
      def own?(thread, current = Thread.current)
        thread.equal?(current) || thread.name.to_s.start_with?("visionary-") ||
          thread[Telemetry::INTERNAL] == true
      end

      private

      def positive(v, default)
        n = Float(v.to_s)
        n.positive? && n.finite? ? n : default
      rescue ArgumentError
        default
      end
    end

    # Turns backtrace locations into "label file:line" frames, where file
    # says whose code it is: relative to the working directory for the app,
    # lib:<gem>/<path> for gems, std:<path> for Ruby's own library.
    class Frames
      def initialize(root: Dir.pwd, gem_dirs: Frames.gem_dirs, std_dirs: Frames.std_dirs,
                     fold_case: Gem.win_platform?)
        @fold_case = fold_case
        @root = dir(root)
        @gem_dirs = gem_dirs.map { |d| dir(d) }.uniq.sort_by { |d| -d.length }
        @std_dirs = std_dirs.map { |d| dir(d) }.uniq.sort_by { |d| -d.length }
        @files = {}
      end

      def self.gem_dirs
        dirs = Gem.path.flat_map { |p| ["#{p}/gems", "#{p}/bundler/gems"] }
        if defined?(::Bundler) && ::Bundler.respond_to?(:bundle_path)
          begin
            bp = ::Bundler.bundle_path.to_s
            dirs.push("#{bp}/gems", "#{bp}/bundler/gems")
          rescue StandardError
            nil
          end
        end
        dirs
      end

      def self.std_dirs
        RbConfig::CONFIG.values_at("rubyarchdir", "rubylibdir").compact.reject(&:empty?)
      end

      # Outermost caller first. Only the sampled frame keeps its line: callers
      # are one frame per function, whichever line they called from.
      def stack(locations)
        leaf = locations.first
        locations.reverse_each.map { |loc| frame(loc, loc.equal?(leaf)) }.join(";")
      end

      def frame(loc, line = true)
        label = loc.label || loc.base_label || "(unknown)"
        file = file(loc.absolute_path || loc.path)
        name = if file
          line ? "#{label} #{file}:#{loc.lineno}" : "#{label} #{file}"
        else
          label.to_s
        end
        name.include?(";") ? name.tr(";", ",") : name
      end

      def file(path)
        return nil if path.nil? || path.empty?
        @files.fetch(path) { @files[path] = classify(path) }
      end

      private

      def classify(path)
        # Ruby's own library compiled in (Kernel#loop, Integer#times, ...):
        # <internal:kernel> is kernel.rb in Ruby's source.
        return "std:#{path[10..-2]}.rb" if path.start_with?("<internal:") && path.end_with?(">")
        return nil if path.start_with?("(", "<")

        p = path.tr("\\", "/")
        key = @fold_case ? p.downcase : p
        @gem_dirs.each do |d|
          next unless key.start_with?(d)
          return gem_file(p[d.length..])
        end
        if (m = p.match(%r{/vendor/bundle/[^/]+/[^/]+/(?:bundler/)?gems/(.+)\z}))
          return gem_file(m[1])
        end
        @std_dirs.each do |d|
          return "std:#{p[d.length..]}" if key.start_with?(d)
        end
        return p[@root.length..] if key.start_with?(@root)
        p
      end

      # "rack-3.2.7/lib/rack/builder.rb" -> "lib:rack/rack/builder.rb".
      def gem_file(rest)
        dir, inner = rest.split("/", 2)
        name = dir.sub(/-\d[^-]*(?:-.+)?\z/, "").sub(/-\h{7,40}\z/, "")
        name = dir if name.empty?
        "lib:#{name}/#{inner.to_s.delete_prefix('lib/')}"
      end

      # With a trailing slash, case-folded where the file system is.
      def dir(d)
        s = d.to_s.tr("\\", "/").chomp("/") + "/"
        @fold_case ? s.downcase : s
      end
    end

    # CRuby lets a thread that computes keep the GVL for 100 ms (shifted by
    # its priority) while another waits for it. A request that computes for
    # 40 ms and then writes its response gives the GVL up at the write, when
    # it no longer runs, so a sampler waiting for the GVL would rarely catch
    # it computing. While a profile runs, the app's threads get the lowest
    # priority (-3: a 12.5 ms quantum) and get their own back afterwards. In
    # CRuby the priority only sets that quantum; all threads drop together,
    # so none gains on another.
    class Quantum
      LOWEST = -3

      def initialize(enabled: RUBY_ENGINE == "ruby")
        @enabled = enabled
        @saved = {}
        @first = true
      end

      def shorten(thread)
        return if !@enabled || @saved.key?(thread)
        prio = thread.priority
        # A thread started during the profile inherited the lowered priority.
        @saved[thread] = !@first && prio == LOWEST ? 0 : prio
        thread.priority = LOWEST if prio > LOWEST
      rescue StandardError
        nil
      end

      def first_pass_done
        @first = false
      end

      def restore
        return unless @enabled
        @saved.each do |t, prio|
          t.priority = prio if t.alive? && t.priority != prio
        rescue StandardError
          nil
        end
        Thread.list.each do |t|
          next if @saved.key?(t) || Profiler.own?(t)
          t.priority = 0 if t.priority == LOWEST
        rescue StandardError
          nil
        end
        @saved.clear
      end
    end

    attr_reader :settings

    def initialize(cfg, instance, store, writer, logger, started_at: Time.now)
      @cfg = cfg
      @instance = instance
      @store = store
      @writer = writer
      @logger = logger
      @started_at = started_at
      @frames = Frames.new
      @settings = Settings.new(enabled: false, services: nil, interval_s: DEFAULT_INTERVAL_S,
                               duration_ms: DEFAULT_DURATION_MS, now: nil)
      reset
    end

    def start
      @thread = Thread.new { run }
      @thread.name = "visionary-profiler" if @thread.respond_to?(:name=)
      self
    end

    def after_fork
      Tally.after_fork
      reset
      start
    end

    def close(timeout = 1)
      @mutex.synchronize do
        @stopped = true
        @cond.broadcast
      end
      @thread&.join(timeout)
    end

    # Whether profiles are taken on the schedule: enabled in the dashboard
    # for this service.
    def active?
      @settings.enabled && Profiler.participates?(@settings.services, @cfg.service)
    end

    # Reads the settings; true when profile.now asks for a profile now.
    def poll
      rows = @store.rows(SETTINGS_SQL)
      @read_failed = false
      was = active?
      @settings = Profiler.parse_settings(rows)
      now_value = @settings.now
      due = !now_value.nil? && now_value != @last_now &&
            Profiler.now_due?(now_value, @started_at) &&
            Profiler.participates?(@settings.services, @cfg.service)
      @last_now = now_value
      log_switch(was)
      due
    rescue StandardError => e
      unless @read_failed
        @read_failed = true
        @logger.info("visionary: profiling settings unreadable (#{e.message.lines.first.to_s.strip}); profiling stays off")
      end
      was = active?
      @settings = Profiler.parse_settings([])
      log_switch(was)
      false
    end

    # Samples for duration_ms and queues the profile. Returns the row, or
    # nil when it was dropped (too few samples, no finished request).
    def capture
      @last_started = Tally.started
      duration_s = @settings.duration_ms / 1000.0
      counts = Hash.new(0)
      started_at = Time.now
      t0 = mono
      deadline = t0 + duration_s
      quantum = Quantum.new
      Tally.begin!
      last = mono
      begin
        loop do
          tick = mono
          # A thread busy in Ruby code keeps the GVL for a quantum before the
          # sampler gets it back, so a late tick stands for the ticks it
          # missed: the stack it finds is the one that held the GVL.
          weight = [((tick - last) / TICK_S).round, 1].max
          last = tick
          Thread.list.each do |t|
            next if Profiler.own?(t)
            quantum.shorten(t)
            next unless t.status == "run"
            locs = t.backtrace_locations
            counts[@frames.stack(locs)] += weight if locs && !locs.empty?
          end
          quantum.first_pass_done
          break if mono >= deadline || wait(TICK_S - (mono - tick))
        end
      ensure
        quantum&.restore
        busy = Tally.finish!
      end
      elapsed_ms = ((mono - t0) * 1000).round(3)
      folded, samples = Profiler.fold(counts)
      routes = busy.transform_values { |ms| [ms.round, 1].max }
      return nil if samples < MIN_SAMPLES || routes.empty?

      row = [Visionary.timestamp(started_at), @cfg.service, @instance, @cfg.version, "ruby",
             elapsed_ms, samples, JSON.generate(routes), folded]
      @writer.enqueue([:profile, row])
      row
    rescue StandardError => e
      @logger.warn("visionary: CPU profile failed: #{e.class}: #{e.message}")
      nil
    end

    private

    def reset
      @mutex = Mutex.new
      @cond = ConditionVariable.new
      @stopped = false
      @thread = nil
      @last_started = Tally.started
    end

    def run
      Telemetry.internal!
      next_poll = mono
      next_profile = nil
      until stopped?
        if mono >= next_poll
          next_poll = mono + POLL_S
          if poll
            @logger.info("visionary: taking a CPU profile now (asked for in the dashboard)")
            capture
            next_profile = mono + @settings.interval_s if next_profile
          end
        end
        if active?
          next_profile ||= mono + @settings.interval_s
          if mono >= next_profile
            capture unless Tally.started == @last_started
            next_profile = mono + @settings.interval_s
          end
        else
          next_profile = nil
        end
        break if wait([next_poll, next_profile].compact.min - mono)
      end
    rescue StandardError => e
      @logger.error("visionary: profiler stopped: #{e.class}: #{e.message}")
    end

    def log_switch(was)
      now = active?
      return if now == was
      if now
        @logger.info("visionary: CPU profiling on (#{@settings.duration_ms.round} ms every #{@settings.interval_s}s)")
      else
        @logger.info("visionary: CPU profiling off")
      end
    end

    # Waits up to secs; true when stopped.
    def wait(secs)
      @mutex.synchronize do
        @cond.wait(@mutex, secs) if !@stopped && secs.positive?
        @stopped
      end
    end

    def stopped? = @mutex.synchronize { @stopped }

    def mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)
  end
end
