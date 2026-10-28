# frozen_string_literal: true

module Visionary
  module Logs
    # A ::Logger that copies every line to app_logs and forwards every call to inner unchanged.
    # Visionary also sees levels inner doesn't print, so failed requests keep their debug lines.
    # In a Hash message, "message"/"msg" is the text and the other keys become attrs.
    class Logger
      # ::Logger's severities, without requiring the logger gem.
      SEVERITIES = { debug: 0, info: 1, warn: 2, error: 3, fatal: 4, unknown: 5 }.freeze
      TO_SEV = [DEBUG, INFO, WARN, ERROR, FATAL, FATAL].freeze
      GUARD = :visionary_capturing
      SDK_DIR = File.expand_path("..", __dir__).tr("\\", "/")
      # Frames of the logging machinery between the app and this logger.
      MACHINERY = %r{/logger\.rb\z|/logger/[^/]+\.rb\z|/active_support/(?:broadcast_logger|tagged_logging|logger\w*)\.rb\z|/semantic_logger/}

      attr_reader :inner
      attr_accessor :progname, :formatter, :datetime_format

      # tags_from: a logger whose ActiveSupport::TaggedLogging tags are
      # stored as the "tags" attribute (inner when not given).
      def initialize(inner = nil, progname: nil, tags_from: nil)
        @inner = inner
        @progname = progname
        @tags_from = tags_from || inner
        @level = nil
        @local_key = :"visionary_log_level_#{object_id}"
      end

      def add(severity, message = nil, progname = nil, &block)
        block = once(block)
        result = @inner ? @inner.add(severity, message, progname, &block) : true
        capture(severity, message, progname, block)
        result
      end
      alias log add

      SEVERITIES.each do |name, n|
        define_method(name) do |progname = nil, &block|
          block = once(block)
          result = @inner ? @inner.public_send(name, progname, &block) : true
          capture(n, nil, progname, block)
          result
        end

        define_method(:"#{name}?") do
          (@inner&.public_send(:"#{name}?") || false) || n >= capture_level
        end

        define_method(:"#{name}!") do
          @inner&.public_send(:"#{name}!")
          @level = n unless @inner
          self
        end
      end

      def <<(msg)
        result = @inner ? (@inner << msg) : self
        text = Visionary.utf8(msg).chomp
        sev = Logs.text_level(text)
        sev = INFO if sev.zero?
        capture_record(sev, Logs.strip_level_word(text, sev), nil, {})
        result
      end

      # Alone (broadcast from Rails.logger), the lowest level Visionary keeps, so those lines reach it.
      def level
        return @inner.level if @inner
        capture_level
      end

      # Alone it changes nothing: what is kept follows Settings → Logs.
      def level=(v)
        @inner ? (@inner.level = v) : (@level = v)
      end

      # Set per thread or fiber by ActiveSupport's LoggerSilence (Rails.logger.silence).
      def local_level
        Thread.current[@local_key]
      end

      def local_level=(v)
        @inner.local_level = v if @inner.respond_to?(:local_level=)
        Thread.current[@local_key] = v.nil? ? nil : Logger.severity(v)
      end

      def silence(severity = SEVERITIES[:error], &block)
        prev = Thread.current[@local_key]
        Thread.current[@local_key] = Logger.severity(severity)
        @inner.respond_to?(:silence) ? @inner.silence(severity, &block) : yield(self)
      ensure
        Thread.current[@local_key] = prev
      end

      def close = @inner&.close
      def reopen(*args) = @inner&.reopen(*args)

      def method_missing(name, *args, **kwargs, &block)
        return super unless @inner.respond_to?(name)
        @inner.public_send(name, *args, **kwargs, &block)
      end

      def respond_to_missing?(name, include_private = false)
        @inner.respond_to?(name) || super
      end

      def self.severity(v)
        return v if v.is_a?(Integer)
        SEVERITIES.fetch(v.to_s.downcase.to_sym, SEVERITIES[:unknown])
      end

      private

      def pipeline
        agent = Visionary.running_agent
        agent.respond_to?(:logs) ? agent.logs : nil
      end

      def capture_level
        lowest = pipeline&.lowest
        return SEVERITIES[:unknown] unless lowest
        TO_SEV.index(lowest) || 0
      end

      # A block that runs at most once, so inner and the capture share its value.
      def once(block)
        return nil unless block
        done = false
        value = nil
        proc do |*args|
          unless done
            value = block.call(*args)
            done = true
          end
          value
        end
      end

      # Like ::Logger#add: with no message, the block or else progname is the message.
      def capture(severity, message, progname, block)
        pipe = pipeline
        return unless pipe
        n = severity.nil? ? SEVERITIES[:unknown] : Logger.severity(severity)
        sev = TO_SEV[n.clamp(0, 5)]
        return if skip?(n) || !pipe.wants?(sev)

        if message.nil?
          if block
            message = block.call
          else
            message = progname
            progname = nil
          end
        end
        progname ||= @progname
        text, attrs = Logger.split(message)
        capture_record(sev, text, progname, attrs, pipe)
      rescue StandardError
        nil
      end

      def capture_record(sev, text, progname, attrs, pipe = pipeline)
        return if pipe.nil? || Thread.current[GUARD] || Telemetry.internal?
        return if text.empty? && attrs.empty?

        Thread.current[GUARD] = true
        begin
          rec = Record.new(Time.now, sev, progname.nil? ? nil : progname.to_s, text)
          tags = Logger.tags(@tags_from)
          attrs["tags"] = tags.join(", ") unless tags.empty?
          COLUMN_ATTRS.each do |k|
            v = attrs[k]
            next unless v.is_a?(String) || v.is_a?(Integer)
            rec[k.to_sym] = v.to_s
            attrs.delete(k)
          end
          rec.attrs = attrs unless attrs.empty?
          if (loc = Logger.location)
            rec.file = Logs.frames.file(loc.absolute_path || loc.path)
            rec.line = loc.lineno
            rec.func = loc.label
          end
          otel(rec) if rec.trace_id.nil?
          pipe.capture(rec)
        ensure
          Thread.current[GUARD] = nil
        end
      rescue StandardError
        nil
      end

      def skip?(n)
        return true if Thread.current[GUARD] || Telemetry.internal?
        local = Thread.current[@local_key]
        !local.nil? && n < local
      end

      # Falls back to the active OpenTelemetry span when the request brought no traceparent.
      def otel(rec)
        return unless defined?(::OpenTelemetry::Trace)
        ctx = RequestContext.current
        return if ctx&.trace_id
        sc = ::OpenTelemetry::Trace.current_span.context
        return unless sc.valid?
        rec.trace_id = sc.hex_trace_id
        rec.span_id = sc.hex_span_id
      rescue StandardError
        nil
      end

      class << self
        def split(message)
          case message
          when String then [Visionary.utf8(message), {}]
          when Exception
            text = "#{message.class}: #{message.message}"
            bt = message.backtrace
            text = "#{text}\n#{bt.join("\n")}" if bt && !bt.empty?
            [Visionary.utf8(text), {}]
          when Hash
            attrs = Logs.flatten(message)
            text = (attrs.delete("message") || attrs.delete("msg")).to_s
            # With no message the line reads like logfmt, as it is printed.
            text = attrs.map { |k, v| "#{k}=#{v}" }.join(" ") if text.empty?
            [text, attrs]
          else [Visionary.utf8(message.inspect), {}]
          end
        end

        # TaggedLogging tags, including through a Rails 7.1+ BroadcastLogger.
        def tags(logger)
          return [] unless logger
          loggers = logger.respond_to?(:broadcasts) ? [logger, *logger.broadcasts] : [logger]
          loggers.each do |l|
            f = l.respond_to?(:formatter) ? l.formatter : nil
            next unless f.respond_to?(:current_tags)
            tags = f.current_tags
            return tags.map(&:to_s) unless tags.nil? || tags.empty?
          end
          []
        rescue StandardError
          []
        end

        # The first frame outside this SDK and the logger machinery.
        def location
          i = 1
          while i < 40
            loc = caller_locations(i, 1)&.first
            return nil unless loc
            return loc unless machinery?(loc.absolute_path || loc.path)
            i += 1
          end
          nil
        end

        def machinery?(path)
          return true if path.nil? || path.start_with?("<internal:")
          p = path.tr("\\", "/")
          sdk = "#{SDK_DIR}/"
          if Gem.win_platform?
            p = p.downcase
            sdk = sdk.downcase
          end
          p.start_with?(sdk) || MACHINERY.match?(p)
        end
      end
    end

    def self.frames
      @frames ||= Profiler::Frames.new
    end
  end
end
