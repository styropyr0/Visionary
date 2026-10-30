# frozen_string_literal: true

require "minitest/autorun"
require "net/http"
require "socket"
require "tmpdir"
require "visionary"
require "sqlite3"

class TraceTest < Minitest::Test
  T = Visionary::TraceContext
  TP = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

  class FakeWriter
    attr_reader :items

    def initialize
      @items = []
      @mutex = Mutex.new
    end

    def enqueue(item) = @mutex.synchronize { @items << item }
    def of(kind) = @mutex.synchronize { @items.select { |k, _| k == kind }.map(&:last) }
  end

  class QuietLogger
    %i[debug info warn error].each { |m| define_method(m) { |_msg = nil| nil } }
  end

  # Just enough of an Agent for the middleware and Visionary.span.
  class FakeAgent
    attr_reader :config, :http, :telemetry, :logger, :writer

    def initialize(cfg, telemetry, writer)
      @config = cfg
      @telemetry = telemetry
      @writer = writer
      @http = Visionary::System::HttpCounters.new
      @logger = QuietLogger.new
    end

    def enabled? = true
    def log_path?(path) = !path.start_with?("/health")
    def logs = nil
    def route_for(env) = env["PATH_INFO"]
    def enqueue(item) = @writer.enqueue(item)
  end

  # span_row positions
  KIND = 4
  NAME = 5
  TRACE = 13
  SPAN = 14
  PARENT = 15

  def setup
    @saved_agent = Visionary.instance_variable_get(:@agent)
    @writer = FakeWriter.new
  end

  def teardown
    Visionary.instance_variable_set(:@agent, @saved_agent)
    Visionary::RequestContext.current = nil
    T.current = nil
  end

  def agent(spans: true)
    cfg = Visionary::Config.new.tap do |c|
      c.service = "api"
      c.capture_bodies = false
      c.spans = spans
      c.span_threshold_ms = 0
    end
    rec = Visionary::Telemetry::Recorder.new(cfg, "i-1", @writer)
    @agent = FakeAgent.new(cfg, rec, @writer)
    Visionary.instance_variable_set(:@agent, @agent)
    Visionary::Telemetry::NetHTTPHook.install
    @agent
  end

  def serve(path = "/x", headers = {}, &block)
    app = lambda do |_env|
      block&.call
      [200, {}, ["ok"]]
    end
    env = { "REQUEST_METHOD" => "GET", "PATH_INFO" => path, "QUERY_STRING" => "" }.merge(headers)
    _, _, body = Visionary::Middleware.new(app, @agent).call(env)
    body.close if body.respond_to?(:close)
  end

  def request = @writer.of(:request).last

  # A server that answers every request with 200 and remembers its headers.
  def with_server
    server = TCPServer.new("127.0.0.1", 0)
    seen = Queue.new
    thread = Thread.new do
      loop do
        sock = server.accept
        headers = {}
        sock.gets
        while (line = sock.gets) && line != "\r\n"
          k, v = line.split(":", 2)
          headers[k.downcase] = v.strip
        end
        seen << headers
        sock.write("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
        sock.close
      end
    rescue IOError
      nil
    end
    yield server.addr[1], seen
  ensure
    server&.close
    thread&.kill
  end

  def test_parse
    sc = T.parse(TP)
    assert_equal ["4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", "01"], [sc.trace_id, sc.span_id, sc.flags]
    assert T.parse("01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00-extra"), "later versions may add fields"
    [
      "", "00-xyz-1-1", "#{TP}-extra", TP.upcase, TP.sub("00-", "ff-"),
      "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
      "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1"
    ].each { |bad| assert_nil T.parse(bad), bad }
  end

  # Rack headers are binary strings, which database drivers store as blobs.
  def test_parse_returns_utf8_ids
    sc = T.parse(TP.b)
    assert_equal Encoding::UTF_8, sc.trace_id.encoding
    assert_equal Encoding::UTF_8, sc.span_id.encoding
  end

  def test_ids
    assert_match(/\A\h{32}\z/, T.new_trace_id)
    assert_match(/\A\h{16}\z/, T.new_span_id)
    refute_equal T.new_span_id, T.new_span_id
  end

  def test_request_continues_the_callers_trace
    agent
    serve("/x", "HTTP_TRACEPARENT" => TP, "HTTP_TRACESTATE" => "vendor=1")
    e = request
    assert_equal "4bf92f3577b34da6a3ce929d0e0e4736", e.trace_id
    assert_equal "00f067aa0ba902b7", e.parent_span_id
    assert_match(/\A\h{16}\z/, e.span_id)
    refute_equal "00f067aa0ba902b7", e.span_id
    row = Visionary::Writer.new(@agent.config, Struct.new(:string_params?, :dialect).new(false, "sqlite"), nil, nil).send(:request_row, e, {})
    assert_equal [e.trace_id, e.span_id, e.parent_span_id], row.last(3)
  end

  def test_request_without_traceparent_starts_a_trace
    agent
    serve("/x", "HTTP_TRACEPARENT" => "garbage")
    e = request
    assert_match(/\A\h{32}\z/, e.trace_id)
    assert_nil e.parent_span_id
    assert_nil T.current, "restored after the request"
  end

  def test_spans_nest_under_the_request_and_custom_spans
    agent
    serve("/x", "HTTP_TRACEPARENT" => TP) do
      Visionary.span("outer") do
        @agent.telemetry.span(kind: "db", name: "SELECT", start_mono: Visionary::Telemetry.now_ms, duration_ms: 1)
      end
      @agent.telemetry.span(kind: "cache", name: "GET", start_mono: Visionary::Telemetry.now_ms, duration_ms: 1)
    end
    e = request
    rows = e.span_rows.to_h { |r| [r[NAME], r] }
    assert(rows.values.all? { |r| r[TRACE] == e.trace_id })
    assert_equal e.span_id, rows["outer"][PARENT]
    assert_equal rows["outer"][SPAN], rows["SELECT"][PARENT]
    assert_equal e.span_id, rows["GET"][PARENT]
    assert_equal 3, rows.values.map { |r| r[SPAN] }.uniq.size
  end

  def test_span_outside_a_trace_starts_one
    agent
    Visionary.span("job") { nil }
    row = @writer.of(:span).last
    assert_match(/\A\h{32}\z/, row[TRACE])
    assert_nil row[PARENT]
  end

  def test_background_keeps_the_trace
    agent
    serve("/x", "HTTP_TRACEPARENT" => TP) do
      Visionary.background("mail") do
        Visionary.span("render") { nil }
      end.join
    end
    rows = request.span_rows.to_h { |r| [r[NAME], r] }
    assert_equal request.span_id, rows["mail"][PARENT]
    assert_equal rows["mail"][SPAN], rows["render"][PARENT]
    assert_equal "4bf92f3577b34da6a3ce929d0e0e4736", rows["render"][TRACE]
  end

  def test_outbound_http_carries_the_trace
    agent
    with_server do |port, seen|
      req = nil
      serve("/x", "HTTP_TRACEPARENT" => TP, "HTTP_TRACESTATE" => "vendor=1") do
        Net::HTTP.start("127.0.0.1", port) do |http|
          req = Net::HTTP::Get.new("/a")
          http.request(req)
        end
      end
      headers = seen.pop
      http_span = request.span_rows.find { |r| r[KIND] == "http" }
      assert_equal "00-4bf92f3577b34da6a3ce929d0e0e4736-#{http_span[SPAN]}-01", headers["traceparent"]
      assert_equal "vendor=1", headers["tracestate"]
      assert_equal request.span_id, http_span[PARENT]
      assert_nil req["traceparent"], "the caller's request is left as it was"
    end
  end

  def test_outbound_keeps_an_app_set_traceparent
    agent
    with_server do |port, seen|
      serve do
        Net::HTTP.start("127.0.0.1", port) do |http|
          req = Net::HTTP::Get.new("/a")
          req["traceparent"] = TP
          http.request(req)
        end
      end
      assert_equal TP, seen.pop["traceparent"]
    end
  end

  def test_outbound_carries_the_trace_with_spans_off_and_unlogged_paths
    agent(spans: false)
    with_server do |port, seen|
      serve("/health", "HTTP_TRACEPARENT" => TP) do
        Net::HTTP.get(URI("http://127.0.0.1:#{port}/a"))
      end
      tp = T.parse(seen.pop["traceparent"])
      assert_equal "4bf92f3577b34da6a3ce929d0e0e4736", tp.trace_id
      refute_equal "00f067aa0ba902b7", tp.span_id, "names this service's request span"
      assert_empty @writer.of(:request)
    end
  end

  def test_log_lines_take_the_current_span
    agent
    cfg = @agent.config
    lw = FakeWriter.new
    pipe = Visionary::Logs::Pipeline.new(cfg, "i-1", nil, nil, QuietLogger.new, writer: lw)
    pipe.settings = Visionary::Logs.parse_settings([], Visionary::Logs::INFO)
    @agent.define_singleton_method(:logs) { pipe }
    span_id = nil
    serve("/x", "HTTP_TRACEPARENT" => TP) do
      Visionary.logger.info("in request")
      Visionary.span("work") do
        span_id = T.current.span_id
        Visionary.logger.info("in span")
      end
    end
    recs = lw.of(:log).to_h { |r| [r.message, r] }
    assert_equal "4bf92f3577b34da6a3ce929d0e0e4736", recs["in request"].trace_id
    assert_equal request.span_id, recs["in request"].span_id
    assert_equal span_id, recs["in span"].span_id
  end

  def test_old_tables_gain_trace_columns_and_index
    # Not Dir.mktmpdir: Windows may still hold the file when the block ends.
    dir = Dir.mktmpdir
    begin
      path = "#{dir}/t.db"
      # The shipped scripts create the tables without the trace columns.
      cols = Visionary::Schema.statements("sqlite", :telemetry).join.scan(/parent_span_id/)
      assert_empty cols
      store = Visionary::Store.open(Visionary::Config.new.tap { |c| c.database_url = "sqlite://#{path}" })
      assert Visionary::Schemas.new(store, QuietLogger.new).ensure(:telemetry)
      assert Visionary::Schemas.new(store, QuietLogger.new).ensure(:requests)
      store.close
      db = SQLite3::Database.new(path)
      %w[spans api_logs].each do |t|
        cols = db.execute("SELECT name FROM pragma_table_info('#{t}')").flatten
        assert_includes cols, "parent_span_id"
        refute_empty db.execute("SELECT name FROM sqlite_master WHERE name = 'idx_#{t}_trace_id'")
      end
      db.close
    ensure
      FileUtils.rm_rf(dir)
    end
  end
end
