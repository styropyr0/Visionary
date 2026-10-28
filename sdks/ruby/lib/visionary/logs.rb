# frozen_string_literal: true

require "json"
require "time"

module Visionary
  # App logs (app_logs): the rules every SDK shares (sdks/go/logrules). What's kept
  # follows Settings → Logs; logs/pipeline.rb and logs/logger.rb do the capturing.
  module Logs
    TRACE = 1
    DEBUG = 2
    INFO = 3
    WARN = 4
    ERROR = 5
    FATAL = 6
    NAMES = [nil, "trace", "debug", "info", "warn", "error", "fatal"].freeze

    MAX_MESSAGE = 8 * 1024
    MAX_ATTRS = 16 * 1024
    MAX_VALUE = 1024
    MAX_HELD = 500
    QUEUE_SIZE = 10_000
    POLL_S = 30
    REDACTED = "[REDACTED]"
    SETTINGS_SQL = "SELECT name, value FROM visionary_settings WHERE name LIKE 'logs.%'"
    # Attributes with a column of their own; they are not repeated in attrs.
    COLUMN_ATTRS = %w[request_id trace_id span_id user_id session_id logger].freeze

    LEVELS = {}.tap do |h|
      { TRACE => %w[trace trc finest finer verbose vrb], DEBUG => %w[debug dbg fine config],
        INFO => %w[info inf information informational notice log], WARN => %w[warn warning wrn],
        ERROR => %w[error err eror severe],
        FATAL => %w[fatal critical crit alert emerg emergency panic ftl] }.each do |sev, names|
        names.each { |n| h[n] = sev }
      end
    end.freeze

    # Secrets in free text; every SDK uses the same patterns, in this order.
    SECRET_PATTERNS = [
      [/-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)/, "[REDACTED:private-key]"],
      [/\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}/, "[REDACTED:jwt]"],
      [%r{\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}}i, '\1 [REDACTED]'],
      [/\b(?:AKIA|ASIA)[0-9A-Z]{16}\b/, "[REDACTED:aws-key]"],
      [/\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret)\b(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)/i,
       '\1\2[REDACTED]']
    ].freeze

    LEVEL_FIELD = /\b(?:level|lvl|severity)=["']?([a-z]+)/i
    TIMESTAMP = %r{\A[\[(]?[0-9][0-9/:.,\-TZ+]*[\])]?\z}

    Record = Struct.new(:at, :sev, :logger, :message, :attrs, :request_id, :trace_id, :span_id, :user_id,
                        :session_id, :file, :line, :func)

    # Changes the level for part of the traffic; until nil means no end.
    Override = Struct.new(:service, :route, :user, :sev, :until) do
      def active?(service, now)
        return false if self.until && now > self.until
        self.service.empty? || self.service == service.to_s
      end

      # One with neither route nor user applies to its whole service.
      def matches?(route, user)
        return false if !self.route.empty? && !Logs.route_matches?(self.route, route.to_s)
        self.user.empty? || self.user == user.to_s
      end

      def scoped? = !route.empty? || !user.empty?
    end

    # Empty fields match anything; sev, when set, drops only lines at or below it.
    Drop = Struct.new(:service, :logger, :contains, :sev)

    Settings = Struct.new(:enabled, :level, :buffer, :buffer_level, :slow_ms, :rate, :overrides, :drops,
                          keyword_init: true) do
      # Base level, then service-wide overrides, then the most specific match (user beats
      # route); later overrides win ties.
      def floor(service, route, user, now = Time.now)
        level = self.level
        rank = 0
        overrides.each do |o|
          next unless o.active?(service, now) && o.matches?(route, user)
          r = o.user.empty? ? (o.route.empty? ? 1 : 2) : 3
          next if r < rank
          level = o.sev
          rank = r
        end
        level
      end

      # Lines in a request below this are held until it ends, since an override may still keep them.
      def hold_below(service, now = Time.now)
        level = self.level
        overrides.each { |o| level = o.sev if !o.scoped? && o.active?(service, now) }
        overrides.each { |o| level = [level, o.sev].max if o.scoped? && o.active?(service, now) }
        level
      end

      # The lowest level any line could be kept at now, for Logger#level.
      def lowest(service, now = Time.now)
        levels = [level]
        levels << buffer_level if buffer
        overrides.each { |o| levels << o.sev if o.active?(service, now) }
        levels.min
      end

      def dropped?(service, logger, message, sev)
        drops.any? do |d|
          (d.service.empty? || d.service == service.to_s) &&
            (d.logger.empty? || d.logger == logger.to_s) &&
            (d.sev.zero? || sev <= d.sev) &&
            (d.contains.empty? || message.to_s.include?(d.contains))
        end
      end
    end

    module_function

    def parse_level(s)
      LEVELS.fetch(s.to_s.strip.downcase, 0)
    end

    def name(sev) = sev.to_i.between?(TRACE, FATAL) ? NAMES[sev] : "info"

    def defaults(level = 0)
      Settings.new(enabled: true, level: level.to_i.positive? ? level : INFO, buffer: true, buffer_level: DEBUG,
                   slow_ms: 1000.0, rate: 6000, overrides: [], drops: [])
    end

    def parse_settings(rows, base = 0)
      s = defaults(base)
      rows.each do |name, value|
        v = value.to_s.strip
        case name.to_s
        when "logs.enabled" then s.enabled = bool(v, s.enabled)
        when "logs.level" then s.level = parse_level(v) if parse_level(v).positive?
        when "logs.buffer" then s.buffer = bool(v, s.buffer)
        when "logs.buffer_level" then s.buffer_level = parse_level(v) if parse_level(v).positive?
        when "logs.slow_ms"
          n = Float(v, exception: false)
          s.slow_ms = n if n&.positive? && n.finite?
        when "logs.rate"
          n = Integer(v, 10, exception: false)
          s.rate = n if n && n >= 0
        when "logs.overrides" then s.overrides = json_list(v).filter_map { |o| override(o) }
        when "logs.drop" then s.drops = json_list(v).filter_map { |d| drop(d) }
        end
      end
      s
    end

    def bool(v, default)
      case v.downcase
      when "true", "1", "t" then true
      when "false", "0", "f" then false
      else default
      end
    end

    def json_list(v)
      list = JSON.parse(v)
      list.is_a?(Array) ? list.select { |x| x.is_a?(Hash) } : []
    rescue JSON::ParserError
      []
    end

    def override(h)
      sev = parse_level(h["level"])
      return nil if sev.zero?
      until_s = h["until"].to_s.strip
      until_t = nil
      unless until_s.empty?
        until_t = parse_time(until_s)
        return nil unless until_t
      end
      Override.new(h["service"].to_s, h["route"].to_s, h["user"].to_s, sev, until_t)
    end

    def drop(h)
      d = Drop.new(h["service"].to_s, h["logger"].to_s, h["contains"].to_s, parse_level(h["level"]))
      return nil if d.service.empty? && d.logger.empty? && d.contains.empty? && d.sev.zero?
      d
    end

    # Formats Visionary writes; no zone means UTC.
    def parse_time(s)
      Profiler.parse_time(s) || Time.iso8601(s)
    rescue ArgumentError
      nil
    end

    def route_matches?(pattern, route)
      pattern.end_with?("*") ? route.start_with?(pattern.chomp("*")) : pattern == route
    end

    # The level a line states at its start ("ERROR ...", "[warn] ...", "2026-10-28 09:30:12 WARN ...",
    # level=debug in the first 200 bytes); 0 if none. A bare lower-case word isn't a level.
    def text_level(s)
      head = Visionary.utf8(s).byteslice(0, 200).scrub("")
      if (m = LEVEL_FIELD.match(head)) && (n = parse_level(m[1])).positive?
        return n
      end
      head.split.each_with_index do |f, i|
        break if i > 3
        next if TIMESTAMP.match?(f)
        word = f.gsub(/\A[\[\]<>():|]+|[\[\]<>():|]+\z/, "")
        n = parse_level(word)
        return 0 if n.zero?
        marked = f.start_with?("[", "<", "(") || f.end_with?(":", "]", "|")
        return marked || word == word.upcase ? n : 0
      end
      0
    end

    def strip_level_word(s, sev)
      i = s.index(" ")
      return s unless i
      word = s[0, i]
      rest = s[(i + 1)..]
      return s if rest.strip.empty? || word != word.upcase
      w = word.chomp(":").downcase
      w == name(sev) || w == "warning" ? rest : s
    end

    def scrub(s)
      SECRET_PATTERNS.each do |re, with|
        s = s.gsub(re, with) if re.match?(s)
      end
      s
    end

    def secret_keys(extra = [])
      (Redactor::DEFAULT_FIELDS + Array(extra)).map { |f| f.to_s.strip.downcase }.reject(&:empty?).to_h { |f| [f, true] }
    end

    # attrs as flat JSON with secrets redacted, capped at MAX_ATTRS: long values are cut
    # first, then keys dropped in sorted order.
    def encode_attrs(attrs, secret)
      out = {}
      attrs.each do |k, v|
        out[k] = if secret[k.split(".").last.to_s.downcase]
                   REDACTED
                 elsif v.is_a?(String)
                   scrub(v)
                 else
                   v
                 end
      end
      s = JSON.generate(out)
      return s if s.bytesize <= MAX_ATTRS

      out.each { |k, v| out[k] = "#{truncate(v, MAX_VALUE)}…" if v.is_a?(String) && v.bytesize > MAX_VALUE }
      s = JSON.generate(out)
      return s if s.bytesize <= MAX_ATTRS

      kept = { "_truncated" => true }
      out.keys.sort.each do |k|
        kept[k] = out[k]
        next if JSON.generate(kept).bytesize <= MAX_ATTRS
        kept.delete(k)
        break
      end
      JSON.generate(kept)
    end

    def truncate(s, n)
      s.bytesize <= n ? s : s.byteslice(0, n).scrub("")
    end

    def flatten(hash, prefix = "", out = {})
      hash.each do |k, v|
        key = "#{prefix}#{k}"
        if v.is_a?(Hash)
          flatten(v, "#{key}.", out)
        elsif !v.nil?
          out[key] = value(v)
        end
      end
      out
    end

    def value(v)
      case v
      when String then Visionary.utf8(v)
      when Integer, true, false then v
      when Float then v.finite? ? v : v.to_s
      when Time then v.getutc.iso8601(6)
      when Symbol then v.to_s
      else
        v = v.iso8601 if v.respond_to?(:iso8601) && !v.is_a?(Numeric)
        Visionary.utf8(v.to_s)
      end
    rescue StandardError
      "?"
    end

    # The trace id of a traceparent header; nil when malformed or all zeros.
    def trace_parent_id(header)
      parts = header.to_s.strip.split("-")
      return nil if parts.size < 4
      id = parts[1]
      id.match?(/\A[0-9a-f]{32}\z/) && id.match?(/[1-9a-f]/) ? id : nil
    end
  end
end

require_relative "logs/pipeline"
require_relative "logs/logger"
