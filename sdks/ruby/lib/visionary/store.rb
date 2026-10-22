# frozen_string_literal: true

require_relative "store/sql"
require_relative "store/d1"
require_relative "store/active_record"

module Visionary
  # Where rows go: D1 over REST, a connection opened from a database URL, or
  # the app's ActiveRecord pool. Every store answers the same calls:
  # exec_script, exec, column_names, rows, batch([[sql, args], ...]), changes,
  # after_fork and close.
  module Store
    # Picks the store from the config, or nil when none is configured.
    def self.open(cfg)
      return ActiveRecord.new(cfg.connection) if cfg.connection

      url = cfg.database_url.to_s.strip
      return open_url(url) unless url.empty? || url.downcase.start_with?("d1:")

      creds = [cfg.d1_account_id, cfg.d1_database_id, cfg.d1_api_token].map { |v| v.to_s.strip }
      return nil if creds.any?(&:empty?)

      D1.new(account_id: creds[0], database_id: creds[1], api_token: creds[2], api_base: cfg.d1_api_base.to_s)
    end

    # Makes values acceptable to the backend: PostgreSQL text rejects NUL,
    # and strict MySQL rejects values longer than the column.
    def self.fit(dialect, table, values)
      case dialect
      when "postgres"
        values.map { |v| v.is_a?(String) && v.include?("\0") ? v.delete("\0") : v }
      when "mysql"
        limits = Schema.mysql_limits[table] || {}
        Schema::TABLES.fetch(table).each_with_index.map do |col, i|
          v = values[i]
          (lim = limits[col]) && v.is_a?(String) ? cut(v, *lim) : v
        end
      else
        values
      end
    end

    def self.cut(s, unit, max)
      if unit == :chars
        s.length > max ? s[0, max] : s
      elsif s.bytesize > max
        s.byteslice(0, max).scrub("")
      else
        s
      end
    end
  end
end
