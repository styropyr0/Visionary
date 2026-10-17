# frozen_string_literal: true

require_relative "telemetry/sql_text"
require_relative "telemetry/errors"

module Visionary
  module Telemetry
    INTERNAL = :visionary_internal

    # Marks the current thread as Visionary's own, so its database and HTTP
    # calls (the writes themselves) never become spans.
    def self.internal!
      Thread.current[INTERNAL] = true
      RequestContext.current = nil
    end

    def self.internal? = Thread.current[INTERNAL] == true

    def self.runtime
      @runtime ||= if RUBY_ENGINE == "ruby" then "CRuby #{RUBY_VERSION}"
                   elsif defined?(JRUBY_VERSION) then "JRuby #{JRUBY_VERSION}"
                   else "#{RUBY_ENGINE} #{RUBY_ENGINE_VERSION}"
                   end
    end

    def self.now_ms = Process.clock_gettime(Process::CLOCK_MONOTONIC) * 1000.0

    # Turns spans and exceptions into rows: inside a request they attach to
    # it (and are written with it), outside they go straight to the writer.
    class Recorder
      attr_reader :spans_on, :errors_on

      def initialize(cfg, instance, writer)
        @cfg = cfg
        @instance = instance
        @writer = writer
        @spans_on = cfg.spans ? true : false
        @errors_on = cfg.errors ? true : false
        @threshold = cfg.span_threshold_ms.to_f
        @limiter = Errors::Limiter.new
      end

      # start_mono is the CLOCK_MONOTONIC start in ms, duration in ms.
      def span(kind:, name:, start_mono:, duration_ms:, target: nil, detail: nil, status: "ok", error: nil, row_count: nil)
        return unless @spans_on && @writer
        return if Telemetry.internal?

        ctx = RequestContext.current
        duration_ms = duration_ms.to_f
        started_at = Time.now - ((Telemetry.now_ms - start_mono) / 1000.0)
        span = Span.new(
          kind: kind.to_s, name: cut(name.to_s, 255), target: target && cut(target.to_s, 255),
          detail: detail && cut(detail.to_s, SQLText::MAX_DETAIL), started_at: started_at,
          offset_ms: ctx ? (start_mono - ctx.started_mono).round(3) : nil,
          duration_ms: duration_ms.round(3), status: status, error: error && cut(error.to_s, 500),
          row_count: row_count, request_id: ctx&.request_id
        )
        if ctx
          ctx.add_span(span)
        elsif duration_ms >= @threshold
          @writer.enqueue([:span, span_row(span)])
        end
      rescue StandardError
        nil
      end

      def span_row(span)
        [Visionary.timestamp(span.started_at), span.request_id, @cfg.service, @instance, span.kind, span.name,
         span.target, span.detail, span.offset_ms, span.duration_ms, span.status, span.error, span.row_count]
      end

      # Rows for a finished request's spans when it was slow or failed.
      def request_span_rows(ctx, duration_ms, status)
        return [] unless @spans_on && (duration_ms >= @threshold || status >= 500)
        ctx.spans.map { |s| span_row(s) }
      end

      def exception(exception, handled:, ctx: RequestContext.current)
        return unless @errors_on && @writer && exception.is_a?(Exception)

        fp = Errors.fingerprint(exception)
        return unless @limiter.allow?(fp)

        row = {
          created_at: Visionary.timestamp(Time.now), type: Errors.type_name(exception),
          message: Errors.message(exception), stack: Errors.stack(exception),
          fingerprint: fp, handled: handled ? 1 : 0
        }
        if ctx
          ctx.add_error(row)
        else
          @writer.enqueue([:error, error_row(row, nil, nil)])
        end
      rescue StandardError
        nil
      end

      # route and user come from the request when it has finished.
      def error_row(row, ctx, route, user_id = nil)
        [row[:created_at], @cfg.service, @instance, @cfg.version, ctx&.request_id, ctx&.method, nonempty(route),
         ctx&.path, nonempty(user_id), row[:type], nonempty(row[:message]), nonempty(row[:stack]),
         row[:fingerprint], row[:handled], Telemetry.runtime]
      end

      private

      def nonempty(s)
        s.nil? || s.to_s.empty? ? nil : s
      end

      def cut(s, max)
        s.length > max ? s[0, max] : s
      end
    end
  end
end
