# frozen_string_literal: true

module Visionary
  # The raw capture of one request. Redaction, error-field extraction and
  # user lookup happen on the writer thread, off the request path.
  Entry = Struct.new(
    :request_id, :started_at, :method, :path, :route, :query, :status, :duration_ms, :ip, :user_agent,
    :user_id, :user_handle, :user_role, :login_account, :headers, :request_content_type, :request_body, :request_truncated,
    :request_size, :response_content_type, :response_body, :response_truncated, :response_size,
    :totals, :span_rows, :error_rows, :trace_id, :span_id, :parent_span_id,
    keyword_init: true
  )

  WriterStats = Struct.new(:queued, :written, :dropped, keyword_init: true) do
    def +(other)
      WriterStats.new(queued: queued + other.queued, written: written + other.written,
                      dropped: dropped + other.dropped)
    end
  end

  # Queue items are [:request, Entry], [:span, row], [:error, row], [:release, row] or [:profile, row]
  class Writer
    # D1 takes 50 statements a call, SQLite allows 32766 parameters
    D1_BATCH = 50
    SQL_BATCH = 500
    MAX_SQL_BATCH = 2000
    MAX_PARAMS = 30_000

    INSERTS = {
      request: "api_logs", span: "spans", error: "errors", release: "releases", profile: "profiles", log: "app_logs",
      metric: "metrics"
    }.freeze

    def initialize(cfg, store, schemas, logger)
      @cfg = cfg
      @store = store
      @schemas = schemas
      @logger = logger
      @redactor = Redactor.new(cfg.redact_headers, cfg.redact_fields, cfg.redact_request_fields)
      @batch_size = if store.string_params?
                      cfg.batch_size.between?(1, D1_BATCH) ? cfg.batch_size : D1_BATCH
                    else
                      cfg.batch_size.between?(1, MAX_SQL_BATCH) ? cfg.batch_size : SQL_BATCH
                    end
      @flush_interval = cfg.flush_interval.to_f.positive? ? cfg.flush_interval.to_f : 3.0
      @queue_size = cfg.queue_size.positive? ? cfg.queue_size : 5000
      @sql = INSERTS.transform_values { |t| Schema.insert_sql(t, store.string_params?) }
      @groups = []
      @groups << :requests if cfg.log_requests
      @groups << :telemetry if cfg.spans || cfg.errors || cfg.version
      reset
    end

    def start
      @thread = Thread.new { run }
      @thread.name = thread_name if @thread.respond_to?(:name=)
      self
    end

    # Threads don't survive fork; the child starts over with an empty queue.
    def after_fork
      reset
      start
    end

    # Never blocks; a full queue drops and counts the item.
    def enqueue(item)
      @queue.push(item)
    end

    def stats
      @stats.synchronize do
        WriterStats.new(queued: @queue.size, written: @written, dropped: @lost + @queue.dropped_total)
      end
    end

    # Stops the thread after it has drained the queue, waiting at most
    # timeout seconds. False if it was still busy then.
    def close(timeout)
      @queue.close
      return true unless @thread
      return true if @thread.join(timeout)
      @logger.warn("visionary: log flush timed out; #{@queue.size} pending")
      false
    end

    private

    def thread_name = "visionary-writer"

    # Runs on the writer thread before each wait for rows; see LogWriter.
    def tick; end

    def reset
      @queue = BoundedQueue.new(@queue_size)
      @thread = nil
      @stats = Mutex.new
      @written = @lost = 0
    end

    def run
      Telemetry.internal!
      @groups.each { |g| @schemas.ensure(g) }
      work = start_flushers
      next_flush = mono + @flush_interval
      batch = []
      loop do
        tick
        items = @queue.take(@batch_size - batch.size, next_flush)
        batch.concat(items)
        closed = @queue.closed? && @queue.size.zero?
        if batch.size >= @batch_size || mono >= next_flush || closed
          if work
            work.push(batch) unless batch.empty?
          elsif !batch.empty?
            write(batch)
          end
          batch = []
          report_drops
          next_flush = mono + @flush_interval
        end
        break if closed
      end
    rescue StandardError => e
      @logger.error("visionary: writer stopped: #{e.class}: #{e.message}")
    ensure
      if work
        work.close
        @flushers.each(&:join)
      end
    end

    # SQLite, D1 and an ActiveRecord pool take one batch at a time, on this thread
    def start_flushers
      n = @store.respond_to?(:parallel) ? @store.parallel : 1
      return nil if n <= 1

      work = SizedQueue.new(1)
      @flushers = Array.new(n) do
        Thread.new do
          Telemetry.internal!
          while (b = work.pop)
            write(b)
          end
        end
      end
      work
    end

    def report_drops
      n = @queue.take_dropped
      @logger.warn("visionary: queue full; #{n} entries dropped") if n.positive?
    end

    # A profile goes in its own transaction: the server creates its table, so
    # if that is missing only the profile is lost.
    def write(batch)
      profiles, rest = batch.partition { |kind, _| kind == :profile }
      commit(rest) unless rest.empty?
      profiles.each { |item| commit([item]) }
    end

    def commit(batch)
      begin
        users = lookup_users(batch)
        rows = batch.flat_map { |kind, item| statements(kind, item, users) }
        stmts = inserts(rows)
      rescue StandardError => e
        @logger.error("visionary: building rows failed; #{batch.size} entries dropped: #{e.class}: #{e.message}")
        count(:@lost, batch.size)
        return
      end
      err = nil
      3.times do |attempt|
        @groups.each { |g| @schemas.ensure(g) }
        begin
          @store.batch(stmts)
          count(:@written, rows.size)
          return
        rescue StandardError => e
          err = e
        end
        sleep(attempt + 1) if attempt < 2
      end
      @logger.error("visionary: batch write failed; #{rows.size} rows dropped: #{err.message}")
      count(:@lost, rows.size)
    end

    def count(var, n)
      @stats.synchronize { instance_variable_set(var, instance_variable_get(var) + n) }
    end

    # One INSERT per row for D1, else multi-row INSERTs split at MAX_PARAMS
    def inserts(rows)
      return rows.map { |kind, values| [@sql.fetch(kind), values] } if @store.string_params?

      rows.group_by(&:first).flat_map do |kind, group|
        table = INSERTS.fetch(kind)
        per = [MAX_PARAMS / Schema::TABLES.fetch(table).size, 1].max
        group.each_slice(per).map do |slice|
          [Schema.insert_rows_sql(table, slice.size), slice.flat_map(&:last)]
        end
      end
    end

    def statements(kind, item, users)
      if kind == :request
        out = [fit(:request, request_row(item, users))]
        item.span_rows&.each { |r| out << fit(:span, r) }
        item.error_rows&.each { |r| out << fit(:error, r) }
        out
      else
        [fit(kind, item)]
      end
    end

    def fit(kind, row)
      [kind, Store.fit(@store.dialect, INSERTS.fetch(kind), row)]
    end

    # Resolves each distinct user missing a handle or role once per batch.
    def lookup_users(batch)
      out = {}
      lookup = @cfg.lookup_user
      return out unless lookup
      batch.each do |kind, e|
        next unless kind == :request
        id = e.user_id
        next if id.empty? || (!e.user_handle.empty? && !e.user_role.empty?) || out.key?(id)
        found = begin
          lookup.call(id)
        rescue StandardError => ex
          @logger.warn("visionary: lookup_user(#{id.inspect}) failed: #{ex.message}")
          nil
        end
        out[id] = found ? [found[0].to_s, found[1].to_s] : ["", ""]
      end
      out
    end

    def request_row(e, users)
      handle = e.user_handle
      role = e.user_role
      if (found = users[e.user_id])
        handle = found[0] if handle.empty?
        role = found[1] if role.empty?
      end
      req_raw = Visionary.utf8(e.request_body)
      resp_raw = Visionary.utf8(e.response_body)
      code = key = ""
      code, key = Redactor.error_fields(resp_raw, @cfg.error_code_field, @cfg.error_key_field) if e.status >= 400
      req_body = resp_body = ""
      if @cfg.capture_bodies
        req_body = @redactor.request_body(req_raw, e.request_content_type)
        resp_body = @redactor.response_body(resp_raw)
      end
      db_count, db_ms, http_count, http_ms = e.totals
      [
        e.request_id, Visionary.timestamp(e.started_at), e.method, e.path, nz(e.route), nz(e.query),
        e.status, e.duration_ms, nz(e.ip), nz(e.user_agent), nz(e.user_id), nz(handle), nz(role),
        nz(@redactor.headers(e.headers)), nz(e.request_content_type), nz(req_body), e.request_truncated ? 1 : 0,
        e.request_size, nz(e.response_content_type), nz(resp_body), e.response_truncated ? 1 : 0, e.response_size,
        nz(code), nz(key), @cfg.service, @cfg.version, db_count, db_ms, http_count, http_ms,
        nz(clip_login(e.login_account)), e.trace_id, e.span_id, e.parent_span_id
      ]
    end

    # Trims a login identifier to what the column holds.
    def clip_login(s)
      Visionary.utf8(s).strip[0, 255]
    end

    def nz(s)
      s.nil? || s.empty? ? nil : s
    end

    def mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)
  end
end
