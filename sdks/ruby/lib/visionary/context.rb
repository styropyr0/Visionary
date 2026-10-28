# frozen_string_literal: true

module Visionary
  # What the request being handled has said about itself: the user, the route
  # pattern, the spans and errors recorded while it ran, and the log lines
  # held back until it ends.
  #
  # The middleware puts one object in fiber storage (Ruby 3.2+, inherited by
  # child fibers and threads, so it works under Falcon and in Async tasks) or a
  # fiber-local variable on Ruby 3.1. Code anywhere in the request then
  # mutates the same object, behind a mutex.
  class RequestContext
    KEY = :visionary_request

    attr_reader :request_id, :method, :path, :started_at, :started_mono, :errors, :db_visible, :trace_id

    def initialize(request_id:, method:, path:, started_at:, started_mono:, span_max:, db_visible:, trace_id: nil)
      @request_id = request_id
      @trace_id = trace_id
      @method = method
      @path = path
      @started_at = started_at
      @started_mono = started_mono
      @span_max = span_max
      @db_visible = db_visible
      @mutex = Mutex.new
      @user = ["", "", ""]
      @login_account = ""
      @route = ""
      @spans = []
      @errors = []
      @db_count = @http_count = 0
      @db_ms = @http_ms = 0.0
      @logged = false
      @late = 0
      @failed = false
      @held = nil
    end

    def set_user(id, handle, role)
      @mutex.synchronize { @user = [id.nil? ? "" : id.to_s, handle.to_s, role.to_s] }
    end

    def user
      @mutex.synchronize { @user }
    end

    # The account a sign-in on this request tried (Visionary.login_attempt).
    def login_account=(account)
      @mutex.synchronize { @login_account = account.to_s }
    end

    def login_account
      @mutex.synchronize { @login_account }
    end

    def route=(pattern)
      @mutex.synchronize { @route = pattern.to_s }
    end

    def route
      @mutex.synchronize { @route }
    end

    # Database and HTTP calls always count; rows are kept up to the cap.
    # Returns nil when the span joined the request, or, once the request has
    # been logged (work it started in the background), how many such late
    # spans the request has had, this one included: the caller writes it on
    # its own and it never touches the request's totals.
    def add_span(span)
      @mutex.synchronize do
        return @late += 1 if @logged

        case span.kind
        when "db"
          @db_count += 1
          @db_ms += span.duration_ms
          @db_visible = true
        when "http"
          @http_count += 1
          @http_ms += span.duration_ms
        end
        @spans << span if @spans.size < @span_max
        nil
      end
    end

    # Called when the request's row is being built: spans that end from now
    # on are late and no longer change totals or spans.
    def logged!
      @mutex.synchronize { @logged = true }
    end

    def add_error(row)
      @mutex.synchronize { @errors << row }
    end

    # Marks the request failed, so its held log lines are kept down to logs.buffer_level.
    def failed!
      @mutex.synchronize { @failed = true }
    end

    def failed? = @mutex.synchronize { @failed || !@errors.empty? }

    # Holds at most max lines, dropping the oldest. False once the request has been logged.
    def hold_log(record, max)
      @mutex.synchronize do
        return false if @logged
        @held ||= []
        @held.shift if @held.size >= max
        @held << record
        true
      end
    end

    def take_held_logs
      @mutex.synchronize do
        out = @held || []
        @held = nil
        out
      end
    end

    # [db_count, db_ms, http_count, http_ms]; database totals are NULL when no
    # database hook could see this request's calls.
    def totals
      @mutex.synchronize do
        [@db_visible ? @db_count : nil, @db_visible ? @db_ms.round(3) : nil, @http_count, @http_ms.round(3)]
      end
    end

    def spans
      @mutex.synchronize { @spans.sort_by(&:offset_ms) }
    end

    if Fiber.respond_to?(:[])
      def self.current = Fiber[KEY]
      def self.current=(ctx)
        Fiber[KEY] = ctx
      end
    else
      def self.current = Thread.current[KEY]
      def self.current=(ctx)
        Thread.current[KEY] = ctx
      end
    end
  end

  # One database, HTTP, cache, queue, custom, graphql or background call.
  Span = Struct.new(:kind, :name, :target, :detail, :started_at, :offset_ms, :duration_ms,
                    :status, :error, :row_count, :request_id, keyword_init: true)
end
