# frozen_string_literal: true

module Visionary
  module Telemetry
    # Statement sanitizing and span names for database calls.
    module SQLText
      STRING = /'(?:[^']|'')*'/
      # A number not part of an identifier (t1, $1 placeholders stay).
      NUMBER = /(?<![\w$.])-?\d+(?:\.\d+)?(?:e[+-]?\d+)?(?![\w.])/i
      IN_LIST = /\bIN\s*\(\s*\?(?:\s*,\s*\?)*\s*\)/i
      TABLE_AFTER = {
        "SELECT" => /\bFROM\s+([\w."`\[\]]+)/i,
        "DELETE" => /\bFROM\s+([\w."`\[\]]+)/i,
        "INSERT" => /\bINTO\s+([\w."`\[\]]+)/i,
        "REPLACE" => /\bINTO\s+([\w."`\[\]]+)/i,
        "UPDATE" => /\AUPDATE\s+(?:ONLY\s+)?([\w."`\[\]]+)/i
      }.freeze
      MAX_DETAIL = 10000

      module_function

      # Literals become ?, IN lists collapse to IN (?), whitespace to one space.
      def sanitize(sql)
        s = sql.to_s.scrub("")
        s = s.gsub(STRING, "?").gsub(NUMBER, "?").gsub(IN_LIST, "IN (?)")
        s = s.gsub(/\s+/, " ").strip
        s.length > MAX_DETAIL ? s[0, MAX_DETAIL] : s
      end

      # "SELECT users", "INSERT bookings", "BEGIN".
      def name(sql)
        s = sql.to_s.lstrip
        s = s.sub(%r{\A(?:/\*.*?\*/\s*)+}m, "")
        verb = s[/\A[a-zA-Z]+/].to_s.upcase
        return "SQL" if verb.empty?
        re = TABLE_AFTER[verb]
        table = re && s[re, 1]
        return verb unless table
        table = table.delete("\"`[]").split(".").last
        table && !table.empty? ? "#{verb} #{table}" : verb
      end
    end
  end
end
