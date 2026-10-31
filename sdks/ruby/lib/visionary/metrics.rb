# frozen_string_literal: true

require "digest"

module Visionary
  # Custom metrics: counters, gauges and histograms the application defines.
  # Values add up in memory and the started agent writes one metrics row per
  # series every interval (15 s by default). Recording never waits for the
  # database, and works before the agent starts.
  #
  # Each distinct label set is one series; past max_series (1000) series per
  # metric name new ones are dropped and counted, so a label holding user ids
  # cannot fill the database.
  module Metrics
    MAX_NAME = 200
    MAX_LABELS = 20
    MAX_KEY = 100
    MAX_VALUE = 200
    DEFAULT_MAX_SERIES = 1000

    # OpenTelemetry's defaults, which suit milliseconds.
    DEFAULT_BUCKETS = [0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10_000].map(&:to_f).freeze

    # One series. Every kind shares it; the handles below use their part.
    class Series
      attr_reader :name, :kind, :unit, :labels, :id, :bounds

      def initialize(name, kind, unit, labels, id, bounds)
        @name = name
        @kind = kind
        @unit = unit
        @labels = labels
        @id = id
        @bounds = bounds
        @mutex = Mutex.new
        @touched = false
        @total = 0.0
        @value = 0.0
        reset_histogram
      end

      def add(n)
        @mutex.synchronize { @total += n }
      end

      def set(v)
        @mutex.synchronize do
          @value = v
          @touched = true
        end
      end

      def move(delta)
        @mutex.synchronize do
          @value += delta
          @touched = true
        end
      end

      def observe(v)
        i = @bounds.bsearch_index { |b| b >= v } || @bounds.size
        @mutex.synchronize do
          @counts[i] += 1
          @min = v if @count.zero? || v < @min
          @max = v if @count.zero? || v > @max
          @count += 1
          @sum += v
        end
      end

      # This interval's row values, or nil when there is nothing to write;
      # counters and histograms start the next interval from zero.
      def take
        @mutex.synchronize do
          case @kind
          when "counter"
            next nil unless @total.positive?
            v = @total
            @total = 0.0
            { value: v }
          when "gauge"
            @touched ? { value: @value } : nil
          else
            next nil if @count.zero?
            row = { value: @sum / @count, count: @count, sum: @sum, min: @min, max: @max,
                    buckets: Metrics.encode_buckets(@bounds, @counts) }
            reset_histogram
            row
          end
        end
      end

      private

      def reset_histogram
        return unless @bounds

        @counts = Array.new(@bounds.size + 1, 0)
        @count = 0
        @sum = 0.0
        @min = @max = 0.0
      end
    end

    # A counter series. Rows hold the increase over each interval.
    class Counter
      def initialize(series) = @s = series

      # Adds n (ignored unless positive and finite).
      def add(n = 1)
        n = Metrics.number(n)
        @s.add(n) if @s && n&.positive?
        nil
      end

      def inc = add(1)
    end

    # A gauge series. Rows hold the last value, every interval once set.
    class Gauge
      def initialize(series) = @s = series

      # Sets the gauge (ignored when not finite).
      def set(v)
        v = Metrics.number(v)
        @s.set(v) if @s && v
        nil
      end

      # Moves the gauge by delta, from 0 when it was never set.
      def add(delta)
        delta = Metrics.number(delta)
        @s.move(delta) if @s && delta
        nil
      end
    end

    # A histogram series. Rows hold each interval's count, sum, min, max and
    # bucket counts.
    class Histogram
      def initialize(series) = @s = series

      # Records one value (ignored when not finite).
      def observe(v)
        v = Metrics.number(v)
        @s.observe(v) if @s && v
        nil
      end
    end

    # Every series of the process, for its whole life.
    class Registry
      attr_accessor :max_series

      def initialize
        @mutex = Mutex.new
        @series = {}
        @per_name = Hash.new(0)
        @dropped = Hash.new(0)
        @opts = {}
        @max_series = DEFAULT_MAX_SERIES
      end

      def describe(name, unit, buckets)
        name = Metrics.clean_name(name)
        return if name.empty?

        buckets = buckets&.filter_map { |b| Metrics.number(b) }&.sort&.uniq
        unit = unit.to_s.strip
        @mutex.synchronize { @opts[name] = { unit: unit.empty? ? nil : unit, buckets: buckets && !buckets.empty? ? buckets : nil } }
      end

      # The series, or nil when the name is empty or the name is over its cap.
      def get(name, kind, labels)
        name = Metrics.clean_name(name)
        return nil if name.empty?

        labels = Metrics.clean_labels(labels)
        key = [name, kind, labels]
        @mutex.synchronize do
          s = @series[key]
          return s if s
          if @per_name[name] >= @max_series
            @dropped[name] += 1
            return nil
          end
          text, id = Metrics.series(name, labels)
          opts = @opts[name] || {}
          bounds = kind == "histogram" ? (opts[:buckets] || DEFAULT_BUCKETS) : nil
          @per_name[name] += 1
          @series[key] = Series.new(name, kind, opts[:unit], text, id, bounds)
        end
      end

      # Each series' interval as rows (in series order) and the drops since
      # the last call, by name.
      def collect
        all, dropped = @mutex.synchronize do
          d = @dropped
          @dropped = Hash.new(0) unless d.empty?
          [@series.values, d]
        end
        rows = all.filter_map do |s|
          v = s.take
          v && [s, v]
        end
        rows.sort_by! { |s, _| s.id }
        [rows.map { |s, v| Metrics.row(s, v) }, dropped]
      end
    end

    # metrics columns after created_at, service and instance, which the
    # recorder fills in.
    def self.row(s, v)
      [s.name, s.kind, s.unit, s.labels, s.id, v[:value], v[:count], v[:sum], v[:min], v[:max], v[:buckets]]
    end

    @registry = Registry.new

    class << self
      attr_reader :registry

      def counter(name, labels) = Counter.new(get(name, "counter", labels))
      def gauge(name, labels) = Gauge.new(get(name, "gauge", labels))
      def histogram(name, labels) = Histogram.new(get(name, "histogram", labels))

      # VISIONARY_METRICS=false makes every handle a no-op.
      def get(name, kind, labels)
        Visionary.config.metrics ? registry.get(name, kind, labels) : nil
      end
      def describe(name, unit:, buckets:) = registry.describe(name, unit, buckets)

      # Labels from a Hash and keywords together.
      def labels(hash, kw)
        return kw if hash.nil?
        return hash if kw.empty?

        hash.to_h.merge(kw)
      end

      # A finite Float, or nil.
      def number(v)
        f = Float(v)
        f.finite? ? f : nil
      rescue ArgumentError, TypeError
        nil
      end

      def clean_name(name) = cut(name.to_s.strip, MAX_NAME)

      # String keys and values: empty keys dropped, keys and values cut, the
      # first 20 keys in byte order kept, in that order.
      def clean_labels(labels)
        return {} if labels.nil? || labels.empty?

        out = {}
        labels.each do |k, v|
          k = cut(k.to_s.strip, MAX_KEY)
          next if k.empty?
          out[k] = cut(v.nil? ? "" : v.to_s, MAX_VALUE)
        end
        out.keys.sort.first(MAX_LABELS).to_h { |k| [k, out[k]] }
      end

      # The canonical labels text of a series and its id (sdks/SPEC.md):
      # a JSON object with keys in byte order, and the first 16 hex digits of
      # SHA-256 over name, a newline and that text. labels must be clean.
      def series(name, labels)
        text = +"{"
        labels.keys.sort.each_with_index do |k, i|
          text << "," if i.positive?
          text << json_string(k) << ":" << json_string(labels[k])
        end
        text << "}"
        [text, Digest::SHA256.hexdigest("#{name}\n#{text}")[0, 16]]
      end

      # Escapes only what JSON requires, so every SDK produces the same bytes.
      def json_string(s)
        out = +"\""
        s.each_char do |c|
          out << case c
                 when "\"" then "\\\""
                 when "\\" then "\\\\"
                 else c.ord < 0x20 ? format("\\u%04x", c.ord) : c
                 end
        end
        out << "\""
      end

      # {"bounds":[...],"counts":[...]} with whole numbers written as such.
      def encode_buckets(bounds, counts)
        "{\"bounds\":[#{bounds.map { |b| num(b) }.join(',')}],\"counts\":[#{counts.join(',')}]}"
      end

      def num(f) = f == f.floor && f.abs < 1e15 ? f.to_i.to_s : f.to_s

      # UTF-8, cut to max bytes on a character boundary.
      def cut(s, max)
        s = Visionary.utf8(s)
        return s if s.bytesize <= max

        s.byteslice(0, max).scrub("")
      end
    end

    # Writes the registry every interval through a writer of its own.
    class Recorder
      def initialize(cfg, instance, store, schemas, logger, registry: Metrics.registry)
        @cfg = cfg
        @instance = instance
        @logger = logger
        @registry = registry
        @registry.max_series = cfg.metrics_max_series if cfg.metrics_max_series.to_i.positive?
        @interval = cfg.metrics_interval.to_f.positive? ? cfg.metrics_interval.to_f : 15.0
        @writer = Writer.new(cfg, store, schemas, logger)
        reset
      end

      attr_reader :writer

      def start
        @writer.start
        spawn
        self
      end

      # Writes the last interval, then flushes.
      def close(timeout)
        @mutex.synchronize do
          @stopped = true
          @cond.broadcast
        end
        @thread&.join(timeout)
        @writer.close(timeout)
      end

      # The parent writes what it recorded before the fork.
      def after_fork
        @registry.collect
        reset
        @writer.after_fork
        spawn
      end

      def flush(at = Time.now)
        rows, dropped = @registry.collect
        dropped.each do |name, n|
          @logger.warn("visionary: metric series dropped over the limit (metric=#{name}, dropped=#{n}, " \
                       "limit=#{@registry.max_series})")
        end
        return if rows.empty?

        # The table is created once something is recorded, so services
        # without custom metrics never get it.
        @writer.use_table!
        stamp = Visionary.timestamp(at)
        rows.each do |r|
          @writer.enqueue([:metric, [stamp, @cfg.service, @instance, *r]])
        end
      end

      private

      def reset
        @mutex = Mutex.new
        @cond = ConditionVariable.new
        @stopped = false
        @thread = nil
      end

      def spawn
        @thread = Thread.new { run }
        @thread.name = "visionary-metrics" if @thread.respond_to?(:name=)
      end

      def run
        Telemetry.internal!
        loop do
          stop = @mutex.synchronize do
            deadline = mono + @interval
            until @stopped || (left = deadline - mono) <= 0
              @cond.wait(@mutex, left)
            end
            @stopped
          end
          flush
          break if stop
        end
      rescue StandardError => e
        @logger.error("visionary: metrics stopped: #{e.message}")
      end

      def mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    end

    # Its own thread and queue (room for many series at once).
    class Writer < Visionary::Writer
      QUEUE_SIZE = 20_000

      def initialize(cfg, store, schemas, logger)
        super
        @groups = []
        @queue_size = QUEUE_SIZE
        reset
      end

      def use_table!
        @groups = [:metrics] if @groups.empty?
      end

      private

      def thread_name = "visionary-metrics-writer"

      def report_drops
        n = @queue.take_dropped
        @logger.warn("visionary: metrics queue full; #{n} rows dropped") if n.positive?
      end

      def statements(kind, row, _users) = [fit(kind, row)]
    end
  end
end
