# frozen_string_literal: true

module Visionary
  module RateLimit
    # Reads the rules from visionary_settings at start and every 15 s, and
    # writes the aggregated outcomes to rate_limit_events at each poll and
    # at close. A failed read keeps the rules in force.
    class Poller
      POLL_S = 15
      D1_BATCH = 50
      SQL_ROWS = 500
      VERSION_SQL = "SELECT value FROM visionary_settings WHERE name = '#{SETTING_VERSION}'".freeze
      CONFIG_SQL = "SELECT value FROM visionary_settings WHERE name = '#{SETTING_CONFIG}'".freeze
      COLUMNS = %w[created_at service instance rule_id rule_name outcome key_value ip country method path user_id
                   hits].freeze

      attr_reader :limiter

      def initialize(limiter, store, cfg, instance, logger)
        @limiter = limiter
        @store = store
        @service = cfg.service
        @instance = instance
        @logger = logger
        @version = ""
        @bad_version = nil
        @write_failed = false
        reset
      end

      # One round: rules, idle counters, outcomes.
      def poll(now = Time.now)
        sync_rules
        @limiter.sweep(now.to_f)
        flush
      end

      def sync_rules
        rows = @store.rows(VERSION_SQL)
        version = rows.first&.first
        if version.nil?
          @limiter.clear unless @version.empty?
          @version = ""
          return
        end
        version = version.to_s
        return if version == @version

        raw = @store.rows(CONFIG_SQL).first&.first.to_s
        begin
          config = raw.empty? ? Config.new(rules: []) : RateLimit.parse(raw)
        rescue JSON::ParserError => e
          if @bad_version != version
            @bad_version = version
            @logger.warn("visionary: rate limit rules are not valid JSON; keeping the previous ones (#{e.message})")
          end
          return
        end
        @limiter.load(config)
        @version = version
      rescue StandardError
        nil # keep the rules in force
      end

      def flush
        events = @limiter.drain
        return if events.empty?

        rows = events.map do |e|
          [Visionary.timestamp(e.created_at), @service, @instance, e.rule_id, e.rule_name, e.outcome, nz(e.key_value),
           nz(e.ip), nz(e.country), nz(e.http_method), nz(e.path), nz(e.user_id), e.hits]
        end
        statements(rows).each_slice(D1_BATCH) { |chunk| @store.batch(chunk) }
      rescue StandardError => e
        unless @write_failed
          @write_failed = true
          @logger.info("visionary: could not record rate-limited requests " \
                       "(the Visionary server creates rate_limit_events): #{e.message}")
        end
      end

      def start
        @thread = Thread.new { run }
        @thread.name = "visionary-ratelimit" if @thread.respond_to?(:name=)
        self
      end

      # The parent's outcomes are its own to write.
      def after_fork
        @limiter.drain
        reset
        start
      end

      # Stops the thread, which writes what is left, waiting at most timeout seconds.
      def close(timeout = 5)
        @mutex.synchronize do
          @stopped = true
          @cond.broadcast
        end
        if @thread
          @thread.join(timeout)
        else
          flush
        end
      end

      private

      def statements(rows)
        if @store.string_params?
          sql = "INSERT INTO rate_limit_events (#{COLUMNS.join(', ')}) VALUES (#{(["NULLIF(?, '')"] * COLUMNS.size).join(', ')})"
          return rows.map { |r| [sql, r] }
        end
        pg = @store.respond_to?(:dialect) && @store.dialect == "postgres"
        rows.each_slice(SQL_ROWS).map do |slice|
          one = "(#{(['?'] * COLUMNS.size).join(', ')})"
          sql = "INSERT INTO rate_limit_events (#{COLUMNS.join(', ')}) VALUES #{([one] * slice.size).join(', ')}"
          args = slice.flatten(1)
          args = args.map { |v| v.is_a?(String) && v.include?("\0") ? v.delete("\0") : v } if pg
          [sql, args]
        end
      end

      def nz(s) = s.nil? || s.to_s.empty? ? nil : s

      def reset
        @mutex = Mutex.new
        @cond = ConditionVariable.new
        @stopped = false
        @thread = nil
      end

      def run
        Telemetry.internal!
        loop do
          poll
          stopped = @mutex.synchronize do
            @cond.wait(@mutex, POLL_S) unless @stopped
            @stopped
          end
          break if stopped
        end
        flush
      rescue StandardError => e
        @logger.error("visionary: rate limits stopped: #{e.message}")
      end
    end
  end
end
