# frozen_string_literal: true

require "minitest/autorun"
require "visionary"

class BackgroundTest < Minitest::Test
  class FakeWriter
    attr_reader :items

    def initialize
      @items = []
      @mutex = Mutex.new
    end

    def enqueue(item) = @mutex.synchronize { @items << item }
    def spans = @mutex.synchronize { @items.select { |k, _| k == :span }.map(&:last) }
  end

  FakeAgent = Struct.new(:telemetry)

  # span_row positions
  REQUEST_ID = 1
  KIND = 4
  NAME = 5
  OFFSET = 8
  DURATION = 9
  STATUS = 10
  ERROR = 11

  def setup
    @writer = FakeWriter.new
    cfg = Visionary::Config.new.tap do |c|
      c.service = "test"
      c.span_max = 3
      c.span_threshold_ms = 100.0
    end
    @rec = Visionary::Telemetry::Recorder.new(cfg, "inst", @writer)
    @saved_agent = Visionary.instance_variable_get(:@agent)
    Visionary.instance_variable_set(:@agent, FakeAgent.new(@rec))
    @t0 = Visionary::Telemetry.now_ms - 50 # the request started 50 ms ago
    @ctx = Visionary::RequestContext.new(
      request_id: "req-1", method: "GET", path: "/", started_at: Time.now,
      started_mono: @t0, span_max: 3, db_visible: true
    )
    Visionary::RequestContext.current = @ctx
  end

  def teardown
    Visionary::RequestContext.current = nil
    Visionary.instance_variable_set(:@agent, @saved_agent)
  end

  def db_span(duration, start: Visionary::Telemetry.now_ms - duration)
    @rec.span(kind: "db", name: "SELECT users", start_mono: start, duration_ms: duration)
  end

  def test_spans_during_the_request_join_it_and_count
    db_span(2.0)
    assert_empty @writer.spans
    assert_equal 1, @ctx.spans.size
    assert_equal [1, 2.0, 0, 0.0], @ctx.totals
  end

  def test_late_spans_keep_request_id_and_offset
    @ctx.logged!
    start = Visionary::Telemetry.now_ms - 1
    db_span(1.0, start: start)
    rows = @writer.spans
    assert_equal 1, rows.size
    assert_equal "req-1", rows[0][REQUEST_ID]
    assert_in_delta (start - @t0), rows[0][OFFSET], 0.01
    assert_operator rows[0][OFFSET], :>=, 45
  end

  def test_late_spans_are_kept_below_threshold_up_to_the_cap_then_only_slow_ones
    @ctx.logged!
    5.times { db_span(1.0) }   # 3 kept (cap), 2 dropped
    db_span(150.0)             # beyond the cap but slow: kept
    db_span(99.0)              # beyond the cap and fast: dropped
    durations = @writer.spans.map { |r| r[DURATION] }
    assert_equal [1.0, 1.0, 1.0, 150.0], durations
  end

  def test_late_spans_do_not_change_totals_or_request_spans
    db_span(2.0)
    @ctx.logged!
    totals = @ctx.totals
    db_span(5.0)
    @rec.span(kind: "http", name: "POST api", start_mono: Visionary::Telemetry.now_ms, duration_ms: 3.0)
    assert_equal totals, @ctx.totals
    assert_equal [1, 2.0, 0, 0.0], @ctx.totals
    assert_equal 1, @ctx.spans.size
  end

  def test_background_records_a_span_after_the_request
    thread = Visionary.background("send welcome email") do
      sleep 0.05
      db_span(1.0)
      :done
    end
    @ctx.logged! # the response went out while the work runs
    assert_equal :done, thread.value
    rows = @writer.spans
    bg = rows.find { |r| r[KIND] == "background" }
    inner = rows.find { |r| r[KIND] == "db" }
    refute_nil bg
    refute_nil inner
    assert_equal "send welcome email", bg[NAME]
    assert_equal "ok", bg[STATUS]
    assert_nil bg[ERROR]
    assert_equal "req-1", bg[REQUEST_ID]
    assert_equal "req-1", inner[REQUEST_ID]
    assert_operator bg[OFFSET], :>=, 50
    assert_operator bg[DURATION], :>=, 50
    assert_operator inner[OFFSET], :>=, bg[OFFSET]
    assert_equal [0, 0.0, 0, 0.0], @ctx.totals
  end

  def test_background_that_raises_records_the_error_and_reraises
    thread = Visionary.background("charge card") { raise ArgumentError, "card declined" }
    thread.report_on_exception = false
    @ctx.logged!
    err = assert_raises(ArgumentError) { thread.join }
    assert_equal "card declined", err.message
    bg = @writer.spans.find { |r| r[KIND] == "background" }
    refute_nil bg
    assert_equal "error", bg[STATUS]
    assert_equal "ArgumentError: card declined", bg[ERROR]
    assert_equal "req-1", bg[REQUEST_ID]
  end

  def test_background_finishing_before_the_request_is_logged_is_not_counted
    Visionary.background("warm cache") { db_span(1.0) }.join
    kinds = @ctx.spans.map(&:kind).sort
    assert_equal %w[background db], kinds
    assert_equal [1, 1.0, 0, 0.0], @ctx.totals # the db call counts, the background span does not
    assert_empty @writer.spans
  end

  def test_background_outside_a_request_is_a_lone_span
    Visionary::RequestContext.current = nil
    Visionary.background("quick job") { nil }.join
    assert_empty @writer.spans # below the threshold
    Visionary.background("slow job") { sleep 0.12 }.join
    rows = @writer.spans
    assert_equal 1, rows.size
    assert_equal "slow job", rows[0][NAME]
    assert_nil rows[0][REQUEST_ID]
    assert_nil rows[0][OFFSET]
  end

  def test_background_without_an_agent_still_runs
    Visionary.instance_variable_set(:@agent, nil)
    assert_equal 42, Visionary.background("x") { 42 }.value
  end
end
