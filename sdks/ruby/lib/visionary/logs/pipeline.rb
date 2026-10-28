# frozen_string_literal: true

require "socket"

module Visionary
  module Logs
    # Decides and queues captured lines; nothing on a request path waits for the database.
    class Pipeline
      attr_reader :writer, :service

      def initialize(cfg, instance, store, schemas, logger, writer: nil)
        @cfg = cfg
        @instance = instance
        @logger = logger
        @service = cfg.service.to_s
        @base = Logs.parse_level(cfg.log_level)
        @settings = Logs.defaults(@base)
        @secrets = Logs.secret_keys(cfg.redact_fields)
        @host = begin
          Socket.gethostname
        rescue StandardError
          nil
        end
        @writer = writer || LogWriter.new(cfg, store, schemas, logger, self)
        reset_rate
      end

      # Replaced whole by poll, so readers never see a half-updated one.
      def settings = @settings

      def settings=(s)
        @settings = s
      end

      def start
        @writer.start
        self
      end

      def close(timeout) = @writer.close(timeout)

      def after_fork
        reset_rate
        @writer.after_fork
      end

      # Keeps the previous values when the read fails (no server or table yet).
      def poll(store)
        @settings = Logs.parse_settings(store.rows(SETTINGS_SQL), @base)
        @read_failed = false
      rescue StandardError => e
        unless @read_failed
          @read_failed = true
          @logger.debug("visionary: log settings unavailable (#{e.message.lines.first.to_s.strip}); using defaults") if @logger.respond_to?(:debug)
        end
      end

      # nil while capture is off in the dashboard.
      def lowest
        s = @settings
        s.enabled ? s.lowest(@service) : nil
      end

      # Inside a request a line may be held; outside it must reach the floor.
      def wants?(sev, ctx = RequestContext.current)
        s = @settings
        return false unless s.enabled
        ctx ? sev >= s.lowest(@service) : sev >= s.floor(@service, "", "")
      end

      # Never blocks.
      def capture(rec, ctx = RequestContext.current)
        s = @settings
        # Skip the SDK's own messages, or a failed write's log line would queue behind it.
        return if !s.enabled || rec.message.start_with?("visionary: ")
        return if s.dropped?(@service, rec.logger, rec.message, rec.sev)

        unless ctx
          emit(rec) if rec.sev >= s.floor(@service, "", "", rec.at)
          return
        end
        rec.request_id ||= ctx.request_id
        rec.trace_id ||= ctx.trace_id
        rec.user_id ||= blank(ctx.user[0])
        return emit(rec) if rec.sev >= s.hold_below(@service, rec.at)
        return if ctx.hold_log(rec, MAX_HELD)

        # Logged after the request ended, so its route and user are known.
        emit(rec) if rec.sev >= s.floor(@service, ctx.route, ctx.user[0], rec.at)
      rescue StandardError
        nil
      end

      def finish(ctx, route, failed, duration_ms)
        lines = ctx.take_held_logs
        return if lines.empty?

        s = @settings
        user = ctx.user[0]
        floor = s.floor(@service, route, user)
        keep_all = s.buffer && (failed || (s.slow_ms.positive? && duration_ms >= s.slow_ms))
        lines.each do |r|
          r.user_id ||= blank(user)
          emit(r) if r.sev >= floor || (keep_all && r.sev >= s.buffer_level)
        end
      rescue StandardError
        nil
      end

      # Errors and fatals get ten times the per-minute cap, so a flood of info lines never hides them.
      def emit(rec)
        rate = @settings.rate
        if rate.positive?
          minute = Time.now.to_i / 60
          report = nil
          over = @rate_mutex.synchronize do
            if minute != @rate_minute
              report = @rate_drop if @rate_drop.positive?
              @rate_minute = minute
              @rate_count = @rate_drop = 0
            end
            @rate_count += 1
            (@rate_count > rate && (rec.sev < ERROR || @rate_count > 10 * rate)).tap { |o| @rate_drop += 1 if o }
          end
          if report
            @writer.enqueue([:log, Record.new(Time.now, WARN, "visionary",
                                              "visionary: #{report} log lines dropped over the cap of #{rate} a minute (Settings → Logs)")])
          end
          return if over
        end
        @writer.enqueue([:log, rec])
      end

      def row(rec)
        msg = Logs.truncate(Logs.scrub(Visionary.utf8(rec.message)), MAX_MESSAGE)
        attrs = rec.attrs && !rec.attrs.empty? ? Logs.encode_attrs(rec.attrs, @secrets) : nil
        [
          Visionary.timestamp(rec.at), @cfg.service, @instance, @cfg.version, "ruby", @host, nil,
          Logs.name(rec.sev), rec.sev, blank(rec.logger), msg, attrs, blank(rec.request_id), blank(rec.trace_id),
          blank(rec.span_id), blank(rec.user_id), blank(rec.session_id), blank(rec.file),
          rec.line.to_i.positive? ? rec.line : nil, blank(rec.func), msg.bytesize + attrs.to_s.bytesize
        ]
      end

      private

      def reset_rate
        @rate_mutex = Mutex.new
        @rate_minute = 0
        @rate_count = @rate_drop = 0
      end

      def blank(s)
        s.nil? || s.to_s.empty? ? nil : s.to_s
      end
    end

    # Its own thread and queue, so a flood of lines never crowds out request rows.
    class LogWriter < Writer
      def initialize(cfg, store, schemas, logger, pipeline)
        super(cfg, store, schemas, logger)
        @pipeline = pipeline
        @groups = [:logs]
        @queue_size = cfg.log_queue_size.to_i.positive? ? cfg.log_queue_size.to_i : QUEUE_SIZE
        reset
      end

      private

      def thread_name = "visionary-logs"

      def tick
        return if @next_poll && mono < @next_poll
        @next_poll = mono + POLL_S
        @pipeline.poll(@store)
      end

      def reset
        super
        @next_poll = nil
      end

      def report_drops
        n = @queue.take_dropped
        @logger.warn("visionary: log queue full; #{n} lines dropped") if n.positive?
      end

      def statements(_kind, rec, _users)
        [stmt(:log, @pipeline.row(rec))]
      end
    end
  end
end
