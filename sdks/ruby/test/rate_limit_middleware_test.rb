# frozen_string_literal: true

require "minitest/autorun"
require "json"
require "tmpdir"
require "visionary"

class RateLimitMiddlewareTest < Minitest::Test
  RL = Visionary::RateLimit

  class QuietLogger
    attr_reader :lines

    def initialize = @lines = []
    %i[debug info warn error].each { |m| define_method(m) { |msg = nil| @lines << msg } }
  end

  # Answers the two settings reads and keeps what is written.
  class FakeStore
    attr_accessor :version, :config, :fail_reads, :d1
    attr_reader :config_reads, :batches

    def initialize(config, version: "v1", d1: false)
      @config = config.is_a?(String) ? config : JSON.generate(config)
      @version = version
      @d1 = d1
      @config_reads = 0
      @batches = []
    end

    def string_params? = @d1
    def dialect = @d1 ? "d1" : "sqlite"

    def rows(sql)
      raise "no such table: visionary_settings" if fail_reads
      if sql == RL::Poller::VERSION_SQL
        version ? [[version]] : []
      elsif sql == RL::Poller::CONFIG_SQL
        @config_reads += 1
        [[config]]
      else
        raise "unexpected #{sql}"
      end
    end

    def batch(stmts) = @batches << stmts
  end

  class FakeAgent
    attr_reader :config, :http, :logger, :requests

    def initialize(cfg, limiter)
      @config = cfg
      @limiter = limiter
      @http = Visionary::System::HttpCounters.new
      @logger = QuietLogger.new
      @requests = []
    end

    def enabled? = true
    def log_path?(_path) = true
    def telemetry = nil
    def logs = nil
    def route_for(env) = env["PATH_INFO"]
    def rate_limiter = @limiter
    def enqueue(item) = @requests << item.last
  end

  RULES = {
    "rules" => [
      { "id" => "ip1", "name" => "API", "action" => "limit", "paths" => ["/api/*"], "limit" => 1, "window_s" => 60,
        "algorithm" => "fixed" },
      { "id" => "plan", "action" => "limit", "key" => "user", "limit_by" => "attr:plan", "limits" => { "free" => 0 },
        "limit" => 100, "window_s" => 60 }
    ]
  }.freeze

  def setup
    @cfg = Visionary::Config.new.tap do |c|
      c.service = "api"
      c.capture_bodies = true
    end
    @store = FakeStore.new(RULES)
    @poller = RL::Poller.new(RL::Limiter.new("api"), @store, @cfg, "i-1", QuietLogger.new)
    @poller.poll
    @agent = FakeAgent.new(@cfg, @poller.limiter)
  end

  def teardown
    RL::Check.current = nil
    Visionary::RequestContext.current = nil
  end

  def call(path, app, ip: "198.51.100.7")
    env = { "REQUEST_METHOD" => "GET", "PATH_INFO" => path, "QUERY_STRING" => "", "REMOTE_ADDR" => ip }
    status, headers, body = Visionary::Middleware.new(app, @agent).call(env)
    text = +""
    body.each { |chunk| text << chunk }
    body.close if body.respond_to?(:close)
    [status, headers, text]
  end

  def test_request_phase_answers_before_the_app
    calls = 0
    app = lambda do |_env|
      calls += 1
      [200, { "content-type" => "text/plain" }, ["ok"]]
    end
    assert_equal 200, call("/api/x", app)[0]
    status, headers, body = call("/api/x", app)
    assert_equal 429, status
    assert_equal 1, calls, "the app ran for a limited request"
    assert_operator headers["retry-after"].to_i, :>, 0
    assert_equal "application/json", headers["content-type"]
    assert_equal '{"error":{"code":"rate_limited","key":"ip1","message":"Too many requests"}}', body
    assert_equal 200, call("/api/x", app, ip: "198.51.100.8")[0]
    assert_equal [200, 429, 200], @agent.requests.map(&:status), "every request is still logged"
  end

  def test_user_check_raises_and_middleware_answers
    seen = []
    app = lambda do |_env|
      Visionary.set_user("u1", role: "member")
      Visionary.check_rate_limit!(plan: "pro")
      Visionary.check_rate_limit!(plan: "free") # only the first call counts
      [200, {}, ["ok"]]
    end
    assert_equal 200, call("/u", app)[0]

    app = lambda do |_env|
      Visionary.set_user("u1")
      begin
        Visionary.check_rate_limit!("plan" => "free")
      rescue Visionary::RateLimited => e
        seen << e
      end
      Visionary.check_rate_limit!(plan: "pro") # repeats the outcome
      [200, {}, ["not reached"]]
    end
    status, headers, body = call("/u", app)
    assert_equal 429, status
    assert_equal "60", headers["retry-after"]
    assert_includes body, '"key":"plan"'
    assert_equal [429, "60"], [seen[0].status, seen[0].headers["retry-after"]]
    assert_equal 429, @agent.requests.last.status
  end

  def test_unlogged_requests_are_limited_too
    def @agent.log_path?(_path) = false
    app = lambda do |_env|
      Visionary.set_user("u2")
      Visionary.check_rate_limit!(plan: "free")
      [200, {}, ["ok"]]
    end
    assert_equal 429, call("/u", app)[0]
    ok = ->(_env) { [200, {}, ["ok"]] }
    assert_equal [200, 429], [call("/api/y", ok)[0], call("/api/y", ok)[0]]
    assert_empty @agent.requests
  end

  def test_user_check_outside_a_request_is_a_no_op
    assert_nil Visionary.check_rate_limit!(plan: "free")
  end

  def test_events_written_with_the_spec_columns
    app = ->(_env) { [200, {}, ["ok"]] }
    3.times { call("/api/x", app) }
    @poller.flush
    assert_equal 1, @store.batches.size
    sql, args = @store.batches[0][0]
    assert_equal "INSERT INTO rate_limit_events (created_at, service, instance, rule_id, rule_name, outcome, " \
                 "key_value, ip, country, method, path, user_id, hits) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", sql
    assert_match(/\A\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z\z/, args[0])
    assert_equal ["api", "i-1", "ip1", "API", "limited", "198.51.100.7", "198.51.100.7", nil, "GET", "/api/x", nil, 2],
                 args[1..]
    @poller.flush
    assert_equal 1, @store.batches.size, "nothing new to write"
  end

  def test_d1_writes_one_statement_per_row_in_batches_of_50
    store = FakeStore.new({ "rules" => [{ "id" => "b", "action" => "block", "mode" => "monitor" }] }, d1: true)
    poller = RL::Poller.new(RL::Limiter.new, store, @cfg, "i-1", QuietLogger.new)
    poller.poll
    120.times { |i| poller.limiter.check_request(RL::Request.new("GET", "/", "10.0.0.#{i}"), Time.now.to_f) }
    poller.flush
    assert_equal [50, 50, 20], store.batches.map(&:size)
    sql, args = store.batches[0][0]
    assert_equal 13, sql.scan("NULLIF(?, '')").size
    assert_equal 13, args.size
    assert_equal ["would_block", nil], [args[5], args[6]]
  end

  def test_config_read_only_when_the_version_changes
    assert_equal 1, @store.config_reads
    @poller.poll
    @poller.poll
    assert_equal 1, @store.config_reads
    @store.version = "v2"
    @store.config = JSON.generate("rules" => [{ "id" => "all", "action" => "block" }])
    @poller.poll
    assert_equal 2, @store.config_reads
    assert_equal 403, @poller.limiter.check_request(RL::Request.new("GET", "/", "1.1.1.1"), 1.0).denial.status

    @store.fail_reads = true
    @poller.poll
    assert @poller.limiter.active?, "a failed read dropped the rules"

    @store.fail_reads = false
    @store.version = nil
    @poller.poll
    refute @poller.limiter.active?, "rules kept after the version row went away"
  end

  def test_invalid_json_keeps_rules_and_logs_once_per_version
    logger = QuietLogger.new
    poller = RL::Poller.new(RL::Limiter.new, @store, @cfg, "i-1", logger)
    poller.poll
    @store.version = "v2"
    @store.config = "{not json"
    3.times { poller.poll }
    assert poller.limiter.active?
    assert_equal 1, logger.lines.count { |l| l.include?("not valid JSON") }
    assert_equal 5, @store.config_reads # setup, this poller's first, then each poll of the bad version
  end

  def test_failed_write_is_logged_once
    logger = QuietLogger.new
    store = FakeStore.new({ "rules" => [{ "id" => "b", "action" => "block" }] })
    def store.batch(_stmts) = raise("no such table: rate_limit_events")
    poller = RL::Poller.new(RL::Limiter.new, store, @cfg, "i-1", logger)
    2.times do
      poller.poll
      poller.limiter.check_request(RL::Request.new("GET", "/", "1.1.1.1"), 1.0)
      poller.flush
    end
    assert_equal 1, logger.lines.count { |l| l.include?("rate_limit_events") }
  end

  def test_env_turns_it_off
    assert Visionary::Config.from_env({}).rate_limit
    refute Visionary::Config.from_env({ "VISIONARY_RATE_LIMIT" => "false" }).rate_limit
    Dir.mktmpdir do |dir|
      cfg = Visionary::Config.new.tap do |c|
        c.database_url = "sqlite://#{dir}/v.db"
        c.logger = QuietLogger.new
      end
      on = Visionary::Agent.new(cfg)
      refute_nil on.rate_limiter
      on.close(1)
      cfg.rate_limit = false
      off = Visionary::Agent.new(cfg)
      assert_nil off.rate_limiter
      off.close(1)
    end
  end
end
