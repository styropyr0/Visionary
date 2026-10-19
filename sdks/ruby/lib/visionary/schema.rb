# frozen_string_literal: true

module Visionary
  # Table definitions shared by every store: the shipped CREATE scripts (copies
  # of sdks/go/schema), column orders and the upgrades for older tables
  # (sdks/go/migrate.go).
  module Schema
    DIR = File.expand_path("schema", __dir__)

    REQUEST_COLUMNS = %w[
      request_id created_at method path route query status duration_ms ip user_agent
      user_id user_handle user_role request_headers request_content_type request_body
      request_body_truncated request_size response_content_type response_body
      response_body_truncated response_size error_code error_key
    ].freeze

    # api_logs columns added after the first release, in insert order.
    ADDED_REQUEST_COLUMNS = [
      %w[service text], %w[version text], %w[db_count int], %w[db_ms real], %w[http_count int], %w[http_ms real]
    ].freeze

    SYSTEM_BASE_COLUMNS = %w[
      created_at instance interval_s
      host_cpu_percent load_1 load_5 load_15
      host_mem_used_mb host_mem_total_mb host_mem_percent
      disk_used_percent disk_free_gb net_rx_kbps net_tx_kbps
      proc_cpu_percent proc_rss_mb proc_threads proc_open_fds
      goroutines heap_alloc_mb heap_inuse_mb heap_objects
      gc_count gc_pause_ms gc_cpu_percent
      db_total_conns db_idle_conns db_acquired_conns db_max_conns
      db_acquires db_empty_acquires db_acquire_wait_ms
      http_requests http_errors http_rps http_in_flight
      uptime_s
    ].freeze

    # system_metrics columns added after the first release, in insert order.
    ADDED_SYSTEM_COLUMNS = [
      %w[cpu_count int], %w[cpu_user_percent real], %w[cpu_system_percent real],
      %w[cpu_iowait_percent real], %w[cpu_steal_percent real],
      %w[host_mem_available_mb real], %w[host_mem_cached_mb real],
      %w[swap_used_mb real], %w[swap_total_mb real],
      %w[disk_total_gb real], %w[disk_inodes_percent real],
      %w[disk_read_kbps real], %w[disk_write_kbps real], %w[disk_read_iops real],
      %w[disk_write_iops real], %w[disk_busy_percent real], %w[disk_await_ms real],
      %w[net_rx_pps real], %w[net_tx_pps real], %w[net_errors int], %w[net_drops int],
      %w[tcp_established int], %w[tcp_retrans int],
      %w[host_procs int], %w[procs_running int], %w[procs_blocked int],
      %w[ctx_switches_ps real], %w[proc_read_kbps real], %w[proc_write_kbps real],
      %w[proc_max_fds int], %w[service text], %w[version text],
      %w[sdk text], %w[runtime text],
      %w[container_cpu_cores real], %w[container_cpu_limit real],
      %w[container_cpu_throttled_percent real], %w[container_mem_used_mb real],
      %w[container_mem_limit_mb real], %w[container_oom_kills int]
    ].freeze

    SPAN_COLUMNS = %w[
      created_at request_id service instance kind name target detail offset_ms duration_ms status error row_count
    ].freeze

    ERROR_COLUMNS = %w[
      created_at service instance version request_id method route path user_id
      type message stack fingerprint handled runtime
    ].freeze

    RELEASE_COLUMNS = %w[created_at service instance version sdk sdk_version runtime].freeze

    TABLES = {
      "api_logs" => REQUEST_COLUMNS + ADDED_REQUEST_COLUMNS.map(&:first),
      "system_metrics" => SYSTEM_BASE_COLUMNS + ADDED_SYSTEM_COLUMNS.map(&:first),
      "spans" => SPAN_COLUMNS,
      "errors" => ERROR_COLUMNS,
      "releases" => RELEASE_COLUMNS
    }.freeze

    # Which script creates a table, and the columns older copies may lack.
    GROUPS = {
      requests: { file: "requests.sql", table: "api_logs", added: ADDED_REQUEST_COLUMNS },
      system: { file: "system.sql", table: "system_metrics", added: ADDED_SYSTEM_COLUMNS },
      telemetry: { file: "telemetry.sql", table: nil, added: [] }
    }.freeze

    PURGE_CHUNK = 5000

    module_function

    def folder(dialect)
      %w[postgres mysql].include?(dialect) ? dialect : "sqlite"
    end

    def statements(dialect, group)
      script = File.read(File.join(DIR, folder(dialect), GROUPS.fetch(group)[:file]), encoding: "UTF-8")
      script.split(";").map(&:strip).reject(&:empty?)
    end

    def columns_query(dialect, table)
      case dialect
      when "postgres"
        "SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '#{table}'"
      when "mysql"
        "SELECT column_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = '#{table}'"
      else
        "SELECT name FROM pragma_table_info('#{table}')"
      end
    end

    def sql_type(dialect, kind)
      case kind
      when "text" then dialect == "mysql" ? "VARCHAR(255)" : "TEXT"
      when "int" then %w[postgres mysql].include?(dialect) ? "BIGINT" : "INTEGER"
      else { "postgres" => "DOUBLE PRECISION", "mysql" => "DOUBLE" }.fetch(dialect, "REAL")
      end
    end

    # ALTER statements for the added columns missing from existing names.
    def missing_columns(dialect, table, added, existing)
      have = existing.map { |c| c.to_s.downcase }
      added.reject { |name, _| have.include?(name) }
           .map { |name, kind| "ALTER TABLE #{table} ADD COLUMN #{name} #{sql_type(dialect, kind)}" }
    end

    # An ALTER that lost a race with another instance.
    def duplicate_column?(error)
      msg = error.message.downcase
      msg.include?("duplicate column") || msg.include?("already exists") || msg.include?("1060")
    end

    # MySQL rejects LIMIT inside IN (subquery) but allows DELETE ... LIMIT;
    # the others are the other way round.
    def purge_sql(dialect, table)
      if dialect == "mysql"
        "DELETE FROM #{table} WHERE created_at < ? ORDER BY id LIMIT #{PURGE_CHUNK}"
      else
        "DELETE FROM #{table} WHERE id IN (SELECT id FROM #{table} WHERE created_at < ? LIMIT #{PURGE_CHUNK})"
      end
    end

    # D1 takes string params only, so there each placeholder becomes
    # NULLIF(?, '') and nulls travel as empty strings.
    def insert_sql(table, string_params)
      cols = TABLES.fetch(table)
      mark = string_params ? "NULLIF(?, '')" : "?"
      "INSERT INTO #{table} (#{cols.join(', ')}) VALUES (#{([mark] * cols.size).join(', ')})"
    end

    # MySQL in strict mode rejects over-long values, which would fail the
    # whole batch, so values are cut to the column size first: characters for
    # VARCHAR(n), bytes for TEXT and MEDIUMTEXT. Read from the shipped schema.
    def mysql_limits
      @mysql_limits ||= begin
        limits = Hash.new { |h, k| h[k] = {} }
        GROUPS.each_value do |g|
          table = nil
          File.foreach(File.join(DIR, "mysql", g[:file]), encoding: "UTF-8") do |line|
            if (m = line.match(/CREATE TABLE IF NOT EXISTS (\w+)/))
              table = m[1]
            elsif table && (m = line.match(/\A\s*(\w+)\s+(VARCHAR\((\d+)\)|MEDIUMTEXT|TEXT)/))
              limits[table][m[1]] = m[3] ? [:chars, m[3].to_i] : [:bytes, m[2] == "TEXT" ? 65_535 : 16_777_215]
            end
          end
        end
        limits.default_proc = nil
        limits.freeze
      end
    end
  end
end
