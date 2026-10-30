# frozen_string_literal: true

module Visionary
  # W3C trace context (https://www.w3.org/TR/trace-context/). A request
  # continues the trace in its traceparent header or starts one; the request
  # and every call it makes get their own span id; outbound Net::HTTP requests
  # carry the trace on, so the service they reach joins it.
  module TraceContext
    KEY = :visionary_span
    TRACEPARENT = "traceparent"
    TRACESTATE = "tracestate"

    # The span that calls made now belong to. flags is the traceparent
    # trace-flags byte in hex ("01" sampled); state the caller's tracestate,
    # passed on unchanged.
    SpanContext = Struct.new(:trace_id, :span_id, :flags, :state) do
      def traceparent = "00-#{trace_id}-#{span_id}-#{flags || '01'}"
    end

    HEX = /\A[0-9a-f]+\z/

    module_function

    # The innermost span: a custom or background span, else the request's
    # server span. nil outside any trace.
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

    # Runs the block with sc as the current span.
    def with(sc)
      outer = current
      self.current = sc
      yield
    ensure
      self.current = outer
    end

    # A traceparent header as a SpanContext, or nil when malformed, version
    # ff or an all-zero id ("no parent").
    def parse(header)
      parts = header.to_s.strip.split("-", -1)
      return nil if parts.size < 4

      version, trace, span, flags = parts
      return nil unless version.size == 2 && version != "ff" && trace.size == 32 && span.size == 16 && flags.size == 2
      # Version 00 has exactly four fields; later versions may append more.
      return nil if version == "00" && parts.size != 4
      return nil unless [version, trace, span, flags].all? { |p| p.match?(HEX) }
      return nil if trace.delete("0").empty? || span.delete("0").empty?

      SpanContext.new(trace, span, flags, nil)
    end

    def new_trace_id = random_hex(16)

    def new_span_id = random_hex(8)

    # Not secrets: Ruby's seeded generator is enough. Never all zero.
    def random_hex(bytes)
      loop do
        hex = Random.bytes(bytes).unpack1("H*")
        return hex unless hex.delete("0").empty?
      end
    end

    # The server span of an incoming request (a Rack env) and the caller's
    # span id: a child of the caller when traceparent names one (or of the
    # active OpenTelemetry span), else the root of a new trace.
    def request_span(env)
      if (otel = otel_span)
        return [SpanContext.new(otel.trace_id, new_span_id, otel.flags, env["HTTP_TRACESTATE"]), otel.span_id]
      end
      if (sc = parse(env["HTTP_TRACEPARENT"]))
        state = env["HTTP_TRACESTATE"].to_s
        return [SpanContext.new(sc.trace_id, new_span_id, sc.flags, state.empty? ? nil : state), sc.span_id]
      end
      [SpanContext.new(new_trace_id, new_span_id, "01", nil), nil]
    end

    # A new span inside the current one, and its parent's span id. Outside
    # any trace it starts one, so the service a call reaches can still be
    # followed.
    def child(parent = current)
      return [SpanContext.new(new_trace_id, new_span_id, "01", nil), nil] unless parent

      [SpanContext.new(parent.trace_id, new_span_id, parent.flags, parent.state), parent.span_id]
    end

    # Sets traceparent (and tracestate) on an outbound request so the service
    # it reaches continues the trace with sc as the parent. A traceparent the
    # caller set itself is left alone. Returns the header names it added.
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

    # The active OpenTelemetry span when the app also runs an OpenTelemetry
    # SDK that already read the header.
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
