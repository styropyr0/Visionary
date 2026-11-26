# frozen_string_literal: true

module Visionary
  # Reports the loaded gems to app_packages once, 30 s after start, off the request path
  class Packages
    DELAY = 30
    MAX = 5000
    TABLE = "app_packages"

    def self.gems(specs = Gem.loaded_specs)
      specs.values.map { |s| [s.name.to_s, s.version.to_s] }
    end

    def self.rows(gems, at, service, instance, version)
      gems.reject { |name, v| name.empty? || v.empty? }.uniq.first(MAX).map do |name, v|
        [at, service, instance, version, "RubyGems", name, v]
      end
    end

    def initialize(cfg, instance, store, schemas, logger, delay: DELAY)
      @cfg = cfg
      @instance = instance
      @store = store
      @schemas = schemas
      @logger = logger
      @delay = delay
      reset
    end

    def start
      @thread = Thread.new { run }
      @thread.name = "visionary-packages" if @thread.respond_to?(:name=)
      self
    end

    def after_fork
      reset
      start
    end

    def close
      @mutex.synchronize do
        @stopped = true
        @cond.broadcast
      end
    end

    def report
      rows = self.class.rows(self.class.gems, Visionary.timestamp(Time.now), @cfg.service, @instance, @cfg.version)
      return if rows.empty?
      raise "tables not ready" unless @schemas.ensure(:telemetry)

      rows = rows.map { |r| Store.fit(@store.dialect, TABLE, r) }
      stmts = if @store.string_params?
                sql = Schema.insert_sql(TABLE, true)
                rows.map { |r| [sql, r] }
              else
                per = Writer::MAX_PARAMS / Schema::TABLES.fetch(TABLE).size
                rows.each_slice(per).map { |slice| [Schema.insert_rows_sql(TABLE, slice.size), slice.flatten(1)] }
              end
      @store.batch(stmts)
    rescue StandardError => e
      @logger.error("visionary: write packages failed: #{e.message}")
    end

    private

    def reset
      @mutex = Mutex.new
      @cond = ConditionVariable.new
      @stopped = false
      @thread = nil
    end

    def run
      Telemetry.internal!
      @mutex.synchronize do
        deadline = mono + @delay
        until @stopped || (left = deadline - mono) <= 0
          @cond.wait(@mutex, left)
        end
      end
      report unless @mutex.synchronize { @stopped }
    end

    def mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)
  end
end
