# frozen_string_literal: true

require "digest/sha1"
require "rbconfig"

module Visionary
  module Telemetry
    # Exception rows: type, message, stack and a fingerprint that groups
    # occurrences of the same error.
    module Errors
      MAX_MESSAGE = 1000
      MAX_STACK = 16 * 1024
      FRAMES = 5
      PER_MINUTE = 20
      SDK_DIR = File.expand_path("..", __dir__)

      module_function

      # The first 16 hex chars of SHA-1 over the type and the top in-app
      # frames as "function@file" (no line numbers); without frames, over the
      # type and the message with digit runs replaced by 0.
      def fingerprint(exception)
        type = exception.class.name || exception.class.to_s
        frames = app_frames(exception)
        basis = frames.empty? ? "#{type}\n#{exception.message.to_s.gsub(/\d+/, '0')}" : "#{type}\n#{frames.join("\n")}"
        Digest::SHA1.hexdigest(basis)[0, 16]
      end

      # Top frames from the application's own code: not the standard library,
      # installed gems or this SDK. When every frame is library code, the top
      # frames as they are.
      def app_frames(exception)
        locs = exception.backtrace_locations
        frames = if locs
                   locs.map { |l| [l.label.to_s, (l.absolute_path || l.path).to_s] }
                 else
                   Array(exception.backtrace).map { |line| parse_line(line) }.compact
                 end
        own = frames.reject { |_, file| library?(file) }
        own = frames if own.empty?
        own.first(FRAMES).map { |label, file| "#{label}@#{relative(file)}" }
      end

      def parse_line(line)
        m = line.match(/\A(.+?):\d+:in [`'](.*)'\z/)
        m && [m[2], m[1]]
      end

      def library?(file)
        return true if file.empty? || file.start_with?("<internal:") || file.start_with?("<")
        f = file.tr("\\", "/")
        library_dirs.any? { |dir| f.start_with?(dir) } || f.include?("/gems/") || f.include?("/vendor/bundle/")
      end

      def library_dirs
        @library_dirs ||= begin
          dirs = [SDK_DIR, RbConfig::CONFIG["rubylibdir"], RbConfig::CONFIG["rubyarchdir"], RbConfig::CONFIG["rubylibprefix"]]
          dirs.concat(Gem.path) if defined?(Gem) && Gem.respond_to?(:path)
          dirs << Bundler.bundle_path.to_s if defined?(Bundler) && Bundler.respond_to?(:bundle_path)
          dirs.compact.map { |d| d.to_s.tr("\\", "/").chomp("/") + "/" }.reject { |d| d == "/" }.uniq.freeze
        rescue StandardError
          [SDK_DIR.tr("\\", "/") + "/"].freeze
        end
      end

      # Paths relative to the app root, so instances deployed to different
      # directories fingerprint alike.
      def relative(file)
        f = file.tr("\\", "/")
        root = (@root ||= Dir.pwd.tr("\\", "/").chomp("/") + "/")
        f.start_with?(root) ? f.delete_prefix(root) : f
      end

      def type_name(exception)
        exception.class.name || exception.class.to_s
      end

      def message(exception)
        m = exception.message.to_s.scrub
        m.length > MAX_MESSAGE ? m[0, MAX_MESSAGE] : m
      end

      # Ruby's own format: "file:line:in 'method': message (Class)" then the
      # "from" lines, causes included.
      def stack(exception)
        s = if exception.backtrace
              exception.full_message(highlight: false, order: :top)
            else
              "#{message(exception)} (#{type_name(exception)})"
            end
        s = s.scrub
        s.bytesize > MAX_STACK ? s.byteslice(0, MAX_STACK).scrub("") : s
      rescue StandardError
        ""
      end

      # At most PER_MINUTE rows per fingerprint per minute in this process.
      class Limiter
        def initialize
          @mutex = Mutex.new
          @minute = nil
          @counts = Hash.new(0)
        end

        def allow?(fingerprint)
          minute = (Process.clock_gettime(Process::CLOCK_MONOTONIC) / 60).floor
          @mutex.synchronize do
            if minute != @minute
              @minute = minute
              @counts.clear
            end
            (@counts[fingerprint] += 1) <= PER_MINUTE
          end
        end
      end
    end
  end
end
