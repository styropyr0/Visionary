# frozen_string_literal: true

require "minitest/autorun"
require "json"
require "logger"
require "stringio"
require "visionary"

# Application logs: the rules every SDK shares (sdks/go/logrules), holding
# and deciding lines per request, and the Logger the app writes to.
class LogsTest < Minitest::Test
  L = Visionary::Logs

  class FakeWriter
    def initialize
      @items = []
      @mutex = Mutex.new
    end

    def enqueue(item) = @mutex.synchronize { @items << item }

    # The queued records, in the order they were logged (held lines are
    # queued later).
    def take
      @mutex.synchronize { @items.map(&:last).tap { @items.clear } }.sort_by(&:at)
    end
  end

  class FakeStore
    def initialize(rows) = @rows = rows

    def rows(_sql)
      raise @rows if @rows.is_a?(Exception)
      @rows
    end
  end

  class QuietLogger
    %i[debug info warn error].each { |m| define_method(m) { |_msg = nil| nil } }
  end

  # Just enough of an Agent for the middleware and Visionary.logger.
  class FakeAgent
    attr_reader :config, :http, :logs, :logger

    def initialize(cfg, logs)
      @config = cfg
      @logs = logs
      @http = Visionary::System::HttpCounters.new
      @logger = QuietLogger.new
    end

    def enabled? = true
    def log_path?(_path) = true
    def telemetry = nil
    def route_for(env) = env["PATH_INFO"]
    def enqueue(_item) = nil
  end

  def setup
    @saved_agent = Visionary.instance_variable_get(:@agent)
  end

  def teardown
    Visionary::RequestContext.current = nil
    Visionary.instance_variable_set(:@agent, @saved_agent)
  end

  # A pipeline with the given settings rows, installed as the running
  # agent's; its queued lines are read with queued.
  def pipeline(rows = [], service: "api")
    cfg = Visionary::Config.new.tap do |c|
      c.service = service
      c.version = "v2"
      c.capture_bodies = false
    end
    @writer = FakeWriter.new
    @pipe = L::Pipeline.new(cfg, "i-1", nil, nil, QuietLogger.new, writer: @writer)
    @pipe.settings = L.parse_settings(rows, L::INFO)
    @agent = FakeAgent.new(cfg, @pipe)
    Visionary.instance_variable_set(:@agent, @agent)
    @pipe
  end

  def queued = @writer.take
  def messages = queued.map(&:message).join(",")

  # Runs one request through the middleware; the block logs while it is
  # handled, and the response has status after taking took seconds.
  def serve(path, status = 200, took: 0, user: nil, &block)
    app = lambda do |_env|
      Visionary.set_user(user) if user
      block&.call(Visionary.logger)
      sleep(took) if took.positive?
      [status, {}, ["ok"]]
    end
    env = { "REQUEST_METHOD" => "GET", "PATH_INFO" => path, "QUERY_STRING" => "",
            "HTTP_TRACEPARENT" => "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" }
    _, _, body = Visionary::Middleware.new(app, @agent).call(env)
    body.close
  end

  # Logged a little apart, so the lines' times order them.
  def three_lines(log)
    log.debug("debug line")
    sleep 0.002
    log.info("info line")
    sleep 0.002
    log.warn("warn line")
  end

  # --- rules ---------------------------------------------------------------

  def test_parse_level
    { "WARNING" => L::WARN, "crit" => L::FATAL, "notice" => L::INFO, "Severe" => L::ERROR, "" => 0, "loud" => 0 }
      .each { |s, want| assert_equal want, L.parse_level(s), s }
    assert_equal "info", L.name(0)
    assert_equal "fatal", L.name(L::FATAL)
  end

  def test_parse_settings_and_floors
    future = Visionary.timestamp(Time.now + 3600)
    past = Visionary.timestamp(Time.now - 3600)
    ov = JSON.generate([
      { route: "/checkout", level: "debug", until: future },
      { user: "42", level: "trace" },
      { route: "/health", level: "warn" },
      { route: "/admin/*", level: "debug" },
      { route: "/old", level: "debug", until: past },
      { service: "other", level: "debug" },
      { level: "nonsense" },
      { route: "/bad", level: "debug", until: "tomorrow" }
    ])
    s = L.parse_settings([["logs.level", "warning"], ["logs.overrides", ov], ["logs.rate", "-1"],
                          ["logs.slow_ms", "250"], ["logs.buffer", "nope"], ["logs.enabled", "false"]])
    assert_equal L::WARN, s.level
    assert_equal 6000, s.rate
    assert_equal 250.0, s.slow_ms
    assert s.buffer
    refute s.enabled
    assert_equal 6, s.overrides.size

    { ["/checkout", ""] => L::DEBUG, ["/health", ""] => L::WARN, ["/admin/users", ""] => L::DEBUG,
      ["/old", ""] => L::WARN, ["/any", "42"] => L::TRACE, ["/health", "42"] => L::TRACE, ["/any", ""] => L::WARN }
      .each { |(route, user), want| assert_equal want, s.floor("api", route, user), "#{route} #{user}" }
    assert_equal L::DEBUG, s.floor("other", "", "")
    assert_equal L::WARN, s.hold_below("api")

    d = L.parse_settings([])
    assert d.enabled
    assert_equal [L::INFO, true, L::DEBUG, 1000.0, 6000], [d.level, d.buffer, d.buffer_level, d.slow_ms, d.rate]
    assert_equal L::WARN, L.parse_settings([], L::WARN).level
    assert_equal L::INFO, L.parse_settings([["logs.overrides", "not json"]]).floor("api", "/x", "1")
  end

  def test_dropped
    drops = JSON.generate([{ contains: "healthcheck" }, { logger: "noisy", level: "info" },
                           { service: "web", level: "debug" }, {}])
    s = L.parse_settings([["logs.drop", drops]])
    assert_equal 3, s.drops.size
    [
      ["api", "", "healthcheck ok", L::ERROR, true],
      ["api", "noisy", "x", L::INFO, true],
      ["api", "noisy", "x", L::WARN, false],
      ["web", "", "x", L::DEBUG, true],
      ["web", "", "x", L::INFO, false],
      ["api", "", "x", L::TRACE, false]
    ].each do |service, logger, msg, sev, want|
      assert_equal want, s.dropped?(service, logger, msg, sev), [service, logger, msg, sev].inspect
    end
  end

  def test_text_level
    {
      "ERROR something" => L::ERROR,
      "2026-10-28 WARN disk" => L::WARN,
      "level=debug msg=x" => L::DEBUG,
      "[info] started" => L::INFO,
      "time=x level=WARN msg=y" => L::WARN,
      "INFO handled" => L::INFO,
      "payment failed" => 0,
      "user error rate is low but ok" => 0,
      "error: connection refused" => L::ERROR,
      "Error handling request" => 0,
      "10:03:44 [ERROR] x" => L::ERROR,
      "2026/10/28 10:03:44 FATAL x" => L::FATAL,
      "<warning> x" => L::WARN
    }.each { |s, want| assert_equal want, L.text_level(s), s }
  end

  def test_strip_level_word
    {
      "INFO started" => "started",
      "WARNING: disk" => "disk",
      "ERROR" => "ERROR",
      "Error handling" => "Error handling",
      "[warn] slow" => "[warn] slow",
      "WARN" => "WARN",
      "DEBUG   spaced x" => "  spaced x"
    }.each do |s, want|
      sev = L.text_level(s)
      sev = L::INFO if sev.zero?
      assert_equal want, L.strip_level_word(s, sev), s
    end
  end

  def test_scrub
    s = "login password=hunter2 token: \"abc def\" Authorization: Bearer abcdefghijkl key AKIAABCDEFGHIJKLMNOP " \
        "jwt eyJhbGciOiJ.eyJzdWIiOiIx.c2lnbmF0dXJl -----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY----- done"
    got = L.scrub(s)
    %w[hunter2 abc\ def abcdefghijkl AKIAABCDEFGHIJKLMNOP eyJhbGciOiJ MIIE].each { |leak| refute_includes got, leak }
    assert_includes got, "password=[REDACTED]"
    assert_includes got, "Bearer [REDACTED]"
    assert got.end_with?(" done"), got
    assert_equal "plain text", L.scrub("plain text")
    assert_equal "[REDACTED:private-key]", L.scrub("-----BEGIN PRIVATE KEY-----\nMIIE cut off")
  end

  def test_encode_attrs
    s = L.encode_attrs({ "user.password" => "x", "n" => 1, "note" => "token=abc", "ssn" => "1" }, L.secret_keys(["SSN"]))
    assert_includes s, '"user.password":"[REDACTED]"'
    assert_includes s, '"note":"token=[REDACTED]"'
    assert_includes s, '"ssn":"[REDACTED]"'
    assert_includes s, '"n":1'

    big = (1..40).to_h { |i| ["k" * i, "v" * 2000] }
    s = L.encode_attrs(big, {})
    assert_operator s.bytesize, :<=, L::MAX_ATTRS
    assert_equal "#{'v' * 1024}…", JSON.parse(s)["k"]

    many = (0...2000).to_h { |i| ["x#{i.to_s.rjust(6, '0')}", "value"] }
    s = L.encode_attrs(many, {})
    parsed = JSON.parse(s)
    assert_operator s.bytesize, :<=, L::MAX_ATTRS
    assert_equal true, parsed["_truncated"]
    assert parsed.key?("x000000") # kept in sorted order
    refute parsed.key?("x001999")
  end

  def test_truncate
    assert_equal "h", L.truncate("héllo", 2)
    assert_equal "abc", L.truncate("abc", 5)
  end

  def test_trace_parent_id
    assert_equal "4bf92f3577b34da6a3ce929d0e0e4736",
                 L.trace_parent_id("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
    ["", "00-xyz-1-1", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"].each do |bad|
      assert_nil L.trace_parent_id(bad), bad
    end
  end

  def test_env
    c = Visionary::Config.from_env({})
    assert c.logs
    assert_equal "info", c.log_level
    c = Visionary::Config.from_env("VISIONARY_LOGS" => "false", "VISIONARY_LOG_LEVEL" => "WARNING")
    refute c.logs
    assert_equal "warning", c.log_level
    assert_equal "info", Visionary::Config.from_env("VISIONARY_LOG_LEVEL" => "loud").log_level
  end

  # --- requests ------------------------------------------------------------

  def test_debug_kept_only_for_failed_requests
    pipeline
    serve("/ok") { |log| three_lines(log) }
    assert_equal "info line,warn line", messages
    serve("/fail", 500) { |log| three_lines(log) }
    got = queued
    # info and warn are written at once; debug when the request has failed.
    assert_equal "debug line,info line,warn line", got.map(&:message).join(",")
    got.each do |r|
      refute_nil r.request_id
      assert_equal "4bf92f3577b34da6a3ce929d0e0e4736", r.trace_id
    end
  end

  def test_debug_kept_for_raised_and_captured_errors
    pipeline
    app = lambda do |_env|
      three_lines(Visionary.logger)
      raise ArgumentError, "boom"
    end
    assert_raises(ArgumentError) do
      Visionary::Middleware.new(app, @agent).call({ "REQUEST_METHOD" => "GET", "PATH_INFO" => "/x" })
    end
    assert_equal "debug line,info line,warn line", messages
    serve("/handled") do |log|
      three_lines(log)
      Visionary.capture_exception(RuntimeError.new("handled"))
    end
    assert_equal "debug line,info line,warn line", messages
  end

  def test_debug_kept_for_slow_requests
    pipeline([["logs.slow_ms", "20"]])
    serve("/slow", took: 0.03) { |log| three_lines(log) }
    assert_equal "debug line,info line,warn line", messages
    pipeline([["logs.slow_ms", "20"], ["logs.buffer", "false"]])
    serve("/slow", 500, took: 0.03) { |log| three_lines(log) }
    assert_equal "info line,warn line", messages
  end

  def test_overrides
    future = Visionary.timestamp(Time.now + 3600)
    past = Visionary.timestamp(Time.now - 3600)
    pipeline([["logs.overrides", JSON.generate([
      { route: "/checkout", level: "debug", until: future },
      { user: "42", level: "trace" },
      { route: "/health", level: "warn" },
      { route: "/old", level: "debug", until: past },
      { service: "other", level: "debug" }
    ])]])

    serve("/checkout") { |log| three_lines(log) }
    assert_equal "debug line,info line,warn line", messages, "route override"
    serve("/health") { |log| three_lines(log) }
    assert_equal "warn line", messages, "quieter route"
    serve("/old") { |log| three_lines(log) }
    assert_equal "info line,warn line", messages, "expired override"
    serve("/any", user: "42") { |log| three_lines(log) }
    got = queued
    assert_equal "debug line,info line,warn line", got.map(&:message).join(","), "user override"
    assert(got.all? { |r| r.user_id == "42" })
  end

  def test_held_lines_are_capped
    pipeline
    serve("/fail", 500) { |log| 600.times { |i| log.debug("line #{i}") } }
    got = queued
    assert_equal L::MAX_HELD, got.size
    assert_equal "line 100", got.first.message
  end

  def test_lines_after_the_request_ended_are_decided_at_once
    pipeline([["logs.overrides", JSON.generate([{ route: "/bg", level: "debug" }])]])
    ctx = nil
    serve("/bg") { ctx = Visionary::RequestContext.current }
    Visionary::RequestContext.current = ctx
    Visionary.logger.debug("late debug")
    assert_equal "late debug", messages
    serve("/other") { ctx = Visionary::RequestContext.current }
    Visionary::RequestContext.current = ctx
    Visionary.logger.debug("dropped")
    assert_equal "", messages
  end

  # --- outside requests, drops and the cap ----------------------------------

  def test_outside_requests
    pipeline([["logs.level", "warn"]])
    log = Visionary.logger
    log.info("startup")
    log.error({ message: "boom", err: "disk full", request_id: "abc", user_id: 7, nested: { n: 3 } })
    got = queued
    assert_equal ["boom"], got.map(&:message)
    assert_equal "abc", got[0].request_id
    assert_equal "7", got[0].user_id
    assert_equal({ "err" => "disk full", "nested.n" => 3 }, got[0].attrs)
  end

  def test_drops_and_rate
    pipeline([["logs.drop", JSON.generate([{ contains: "healthcheck" }, { logger: "noisy", level: "info" }])],
              ["logs.rate", "3"]])
    log = Visionary.logger
    log.info("healthcheck ok")
    log.info("noisy") { "chatty" }
    log.warn("noisy") { "kept" }
    5.times { log.info("flood") }
    log.error("still kept")
    assert_equal "kept,flood,flood,still kept", messages
  end

  def test_rate_warning_after_the_minute
    pipeline([["logs.rate", "1"]])
    log = Visionary.logger
    log.info("one")
    log.info("two")
    @pipe.instance_variable_set(:@rate_minute, 0) # the minute has passed
    log.info("three")
    got = queued
    warn_line = got.find { |r| r.logger == "visionary" }
    assert_equal L::WARN, warn_line.sev
    assert_equal "visionary: 1 log lines dropped over the cap of 1 a minute (Settings → Logs)", warn_line.message
    assert_equal %w[one three], (got - [warn_line]).map(&:message)
  end

  def test_skips_the_sdks_own_messages
    pipeline
    Visionary.logger.error("visionary: write failed")
    Thread.new do
      Visionary::Telemetry.internal!
      Visionary.logger.error("from the writer thread")
    end.join
    assert_empty queued
  end

  def test_settings_off_and_unreadable
    pipe = pipeline([["logs.enabled", "false"]])
    Visionary.logger.error("off")
    assert_empty queued
    pipe.poll(FakeStore.new([["logs.level", "debug"]]))
    assert_equal L::DEBUG, pipe.settings.level
    pipe.poll(FakeStore.new(RuntimeError.new("no such table: visionary_settings")))
    assert_equal L::DEBUG, pipe.settings.level, "keeps the previous values"
  end

  # --- the Logger ------------------------------------------------------------

  def test_forwards_to_inner_unchanged
    pipeline([["logs.level", "debug"]])
    out = StringIO.new
    inner = ::Logger.new(out, level: ::Logger::WARN, progname: "app")
    log = Visionary.logger(inner)
    calls = 0
    log.debug { calls += 1; "quiet" }
    log.warn("loud")
    log.add(::Logger::ERROR, "added")
    log.unknown("worst")
    log.level = ::Logger::INFO
    log.info("now printed")
    assert_equal 1, calls
    assert_equal ::Logger::INFO, inner.level
    assert_equal ::Logger::INFO, log.level
    printed = out.string
    refute_includes printed, "quiet"
    %w[loud added worst now\ printed].each { |m| assert_includes printed, m }
    got = queued
    assert_equal ["quiet", "loud", "added", "worst", "now printed"], got.map(&:message)
    assert_equal %w[debug warn error fatal info], got.map { |r| L.name(r.sev) }
    assert(log.debug?) # Visionary keeps debug lines even though inner doesn't print them
  end

  def test_messages
    pipeline([["logs.level", "debug"]])
    log = Visionary.logger
    log.progname = "billing"
    begin
      raise KeyError, "missing"
    rescue KeyError => e
      log.error(e)
    end
    log.info("worker") { "from a block" }
    log.info(event: "signup", plan: "pro")
    log << "WARNING: disk almost full\n"
    log.info("")
    got = queued
    assert got[0].message.start_with?("KeyError: missing\n"), got[0].message
    assert_includes got[0].message, "logs_test.rb"
    assert_equal "billing", got[0].logger
    assert_equal ["from a block", "worker"], [got[1].message, got[1].logger]
    assert_equal ["event=signup plan=pro", { "event" => "signup", "plan" => "pro" }], [got[2].message, got[2].attrs]
    assert_equal ["disk almost full", L::WARN], [got[3].message, got[3].sev]
    assert_equal 4, got.size
  end

  def test_code_location
    pipeline
    log = Visionary.logger(::Logger.new(nil))
    line = __LINE__ + 1
    log.warn("here")
    r = queued.first
    assert r.file.end_with?("test/logs_test.rb"), r.file
    refute r.file.include?(":"), "relative to the working directory: #{r.file}"
    assert_equal line, r.line
    assert_includes r.func, "test_code_location"
  end

  def test_level_before_start_and_without_inner
    Visionary.instance_variable_set(:@agent, nil)
    log = Visionary.logger
    assert_equal ::Logger::UNKNOWN, log.level
    assert log.info("nothing to capture into")
    pipeline([["logs.level", "warn"], ["logs.buffer", "false"]])
    assert_equal ::Logger::WARN, log.level
    refute log.info?
  end

  def test_silence
    pipeline([["logs.level", "debug"]])
    log = Visionary.logger
    log.silence(::Logger::ERROR) do
      log.info("silenced")
      log.error("kept")
    end
    log.info("after")
    assert_equal "kept,after", messages
  end

  # --- rows ------------------------------------------------------------------

  def test_row_matches_columns
    pipe = pipeline
    rec = L::Record.new(Time.now, L::WARN, nil, "hi password=x", { "a" => 1 })
    rec.line = 3
    row = pipe.row(rec)
    cols = Visionary::Schema::LOG_COLUMNS
    assert_equal 21, cols.size
    assert_equal cols.size, row.size
    assert_equal cols.size, Visionary::Schema.insert_sql("app_logs", false).count("?")
    at = ->(c) { row[cols.index(c)] }
    assert_equal %w[api i-1 v2 ruby], [at.("service"), at.("instance"), at.("version"), at.("source")]
    assert_equal ["warn", L::WARN, 3], [at.("level"), at.("severity"), at.("code_line")]
    assert_equal "hi password=[REDACTED]", at.("message")
    assert_equal '{"a":1}', at.("attrs")
    assert_equal at.("message").bytesize + 7, at.("size")
    assert_nil at.("container")
    assert_nil at.("logger")

    long = pipe.row(L::Record.new(Time.now, L::INFO, nil, "é" * 5000))
    assert_operator long[cols.index("message")].bytesize, :<=, L::MAX_MESSAGE
  end

  def test_log_writer_polls_and_builds_statements
    pipe = pipeline
    store = FakeStore.new([["logs.level", "error"]])
    def store.string_params? = false
    def store.dialect = "sqlite"
    w = L::LogWriter.new(Visionary::Config.new, store, nil, QuietLogger.new, pipe)
    w.send(:tick)
    assert_equal L::ERROR, pipe.settings.level
    sql, args = w.send(:statements, :log, L::Record.new(Time.now, L::INFO, nil, "x"), {}).first
    assert sql.start_with?("INSERT INTO app_logs ("), sql
    assert_equal 21, args.size
  end
end
