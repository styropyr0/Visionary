# frozen_string_literal: true

module Visionary
  # W3C trace context (https://www.w3.org/TR/trace-context/)
  module TraceContext
    KEY = :visionary_span
    TRACEPARENT = "traceparent"
    TRACESTATE = "tracestate"

    # state is the caller's tracestate, passed on unchanged
    SpanContext = Struct.new(:trace_id, :span_id, :flags, :state) do
      def traceparent = "00-#{trace_id}-#{span_id}-#{flags || '01'}"
    end

    HEX = /\A[0-9a-f]+\z/

    module_function

    # The innermost custom, background or request span, nil outside a trace
    if Fiber.respond_to?(:[])
      def current = Fiber[KEY]

      def current=(sc)
        Fiber[KEY] = sc
      end
    else
      def current = Thread.current[KEY]

      def current=(sc)
        Thread.current[KEY] = sc
      end
    end

    def with(sc)
      outer = current
      self.current = sc
      yield
    ensure
      self.current = outer
    end

    # nil when malformed, version ff or an all-zero id
    def parse(header)
      parts = String.new(header.to_s.strip, encoding: Encoding::UTF_8).split("-", -1)
      return nil if parts.size < 4

      version, trace, span, flags = parts
      return nil unless version.size == 2 && version != "ff" && trace.size == 32 && span.size == 16 && flags.size == 2
      # Version 00 has exactly four fields, later versions may append more
      return nil if version == "00" && parts.size != 4
      return nil unless [version, trace, span, flags].all? { |p| p.match?(HEX) }
      return nil if trace.delete("0").empty? || span.delete("0").empty?

      SpanContext.new(trace, span, flags, nil)
    end

    def new_trace_id = random_hex(16)

    def new_span_id = random_hex(8)

    # Not secrets, so Ruby's seeded generator is enough
    def random_hex(bytes)
      loop do
        hex = Random.bytes(bytes).unpack1("H*")
        return hex unless hex.delete("0").empty?
      end
    end

    def request_span(env)
      if (otel = otel_span)
        return [SpanContext.new(otel.trace_id, new_span_id, otel.flags, env["HTTP_TRACESTATE"]), otel.span_id]
      end
      if (sc = parse(env["HTTP_TRACEPARENT"]))
        state = String.new(env["HTTP_TRACESTATE"].to_s, encoding: Encoding::UTF_8)
        return [SpanContext.new(sc.trace_id, new_span_id, sc.flags, state.empty? ? nil : state), sc.span_id]
      end
      [SpanContext.new(new_trace_id, new_span_id, "01", nil), nil]
    end

    def child(parent = current)
      return [SpanContext.new(new_trace_id, new_span_id, "01", nil), nil] unless parent

      [SpanContext.new(parent.trace_id, new_span_id, parent.flags, parent.state), parent.span_id]
    end

    # Leaves a traceparent the caller set alone and returns the header names it added
    def inject(req, sc)
      return [] if sc.nil? || req[TRACEPARENT]

      req[TRACEPARENT] = sc.traceparent
      added = [TRACEPARENT]
      if sc.state && !sc.state.empty? && req[TRACESTATE].nil?
        req[TRACESTATE] = sc.state
        added << TRACESTATE
      end
      added
    end

    # An OpenTelemetry SDK in the app has already read the header
    def otel_span
      return nil unless defined?(::OpenTelemetry::Trace)

      sc = ::OpenTelemetry::Trace.current_span.context
      return nil unless sc.valid?

      SpanContext.new(sc.hex_trace_id, sc.hex_span_id, sc.trace_flags.sampled? ? "01" : "00", nil)
    rescue StandardError
      nil
    end
  end
end
