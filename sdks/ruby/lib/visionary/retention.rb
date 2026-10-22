# frozen_string_literal: true

module Visionary
  # Deletes expired rows 60 s after start, then every 24 h, in chunks so no
  # statement runs long or hits D1's limits.
  class Retention
    FIRST_RUN = 60
    EVERY = 24 * 3600
    MAX_ROUNDS = 200

    # tables: { "api_logs" => days, ... }; 0 days (the default) purges nothing
    # and starts no thread: the Visionary server applies Settings → Retention.
    def initialize(store, logger, tables)
      @store = store
      @logger = logger
      @tables = tables.select { |_, days| days.to_i.positive? }
      reset
    end

    def needed? = !@tables.empty?

    def start
      return self unless needed?
      @thread = Thread.new { run }
      @thread.name = "visionary-retention" if @thread.respond_to?(:name=)
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

    def purge(table, days)
      cutoff = Visionary.timestamp(Time.now - (days * 86_400))
      sql = Schema.purge_sql(@store.dialect, table)
      total = 0
      MAX_ROUNDS.times do
        break if stopped?
        n = begin
          @store.changes(sql, [cutoff])
        rescue StandardError => e
          # The server creates profiles; before it has, there is nothing to purge.
          return total if table == "profiles" && e.message.match?(/no such table|does(?:n't| not) exist/i)
          @logger.error("visionary: retention purge of #{table} failed after #{total} rows: #{e.message}")
          return total
        end
        total += n
        break if n < Schema::PURGE_CHUNK
      end
      @logger.info("visionary: retention purge of #{table} deleted #{total} rows older than #{cutoff}") if total.positive?
      total
    end

    private

    def reset
      @mutex = Mutex.new
      @cond = ConditionVariable.new
      @stopped = false
      @thread = nil
    end

    def stopped? = @mutex.synchronize { @stopped }

    def run
      Telemetry.internal!
      wait = FIRST_RUN
      loop do
        @mutex.synchronize do
          deadline = mono + wait
          until @stopped || (left = deadline - mono) <= 0
            @cond.wait(@mutex, left)
          end
        end
        break if stopped?
        @tables.each { |table, days| purge(table, days) }
        wait = EVERY
      end
    rescue StandardError => e
      @logger.error("visionary: retention stopped: #{e.message}")
    end

    def mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)
  end
end
