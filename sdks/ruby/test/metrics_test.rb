# frozen_string_literal: true

require "minitest/autorun"
require "json"
require "logger"
require "tmpdir"
require "visionary"
require "sqlite3"

class MetricsTest < Minitest::Test
  M = Visionary::Metrics

  # Row positions after created_at, service and instance.
  NAME = 0
  KIND = 1
  LABELS = 3
  SERIES = 4
  VALUE = 5
  COUNT = 6
  BUCKETS = 10

  def setup
    @saved = M.instance_variable_get(:@registry)
    M.instance_variable_set(:@registry, M::Registry.new)
  end

  def teardown
    M.instance_variable_set(:@registry, @saved)
  end

  def rows = M.registry.collect.first

  def test_series_id_matches_the_contract
    text, id = M.series("orders_placed", M.clean_labels("region" => "eu"))
    assert_equal '{"region":"eu"}', text
    assert_equal "da4f5c6244c4f3e4", id
    assert_equal "{}", M.series("x", {}).first
  end

  def test_labels_are_canonical
    labels = M.clean_labels(b: 1, "a" => nil, "" => "x", "q" => "say \"hi\"\\\n<&>é")
    text, = M.series("m", labels)
    assert_equal '{"a":"","b":"1","q":"say \"hi\"\\\\\u000a<&>é"}', text
    many = M.clean_labels((1..30).to_h { |i| [format("k%02d", i), "v"] })
    assert_equal 20, many.size
    assert_equal "k01", many.keys.first
    assert_equal 100, M.clean_labels("k" * 150 => "v").keys.first.bytesize
    assert_equal 199, M.clean_labels("k" => "a" + ("é" * 150))["k"].bytesize
  end

  def test_counter_adds_up_and_resets
    c = Visionary.counter("orders_placed", region: "eu")
    c.add(2)
    c.inc
    Visionary.counter("orders_placed", { "region" => "eu" }).add(1.5)
    [0, -1, Float::NAN, Float::INFINITY, "x", nil].each { |bad| c.add(bad) }
    got = rows
    assert_equal 1, got.size
    assert_equal ["orders_placed", "counter", nil, '{"region":"eu"}', "da4f5c6244c4f3e4", 4.5], got[0][0, 6]
    assert_nil got[0][COUNT]
    assert_empty rows, "no increase, no row"
  end

  def test_gauge_repeats_once_set
    g = Visionary.gauge("queue_depth", queue: "emails")
    assert_empty rows
    g.add(3)
    g.add(-1)
    assert_equal 2.0, rows[0][VALUE]
    g.set(7)
    g.set(Float::NAN)
    assert_equal 7.0, rows[0][VALUE]
    assert_equal 7.0, rows[0][VALUE]
  end

  def test_kinds_are_separate_series
    Visionary.counter("x").add(1)
    Visionary.gauge("x").set(5)
    assert_equal %w[counter gauge], rows.map { |r| r[KIND] }.sort
  end

  def test_histogram_buckets
    h = Visionary.histogram("checkout_ms")
    [0, 5, 7, 10_000, 20_000].each { |v| h.observe(v) }
    r = rows[0]
    assert_equal 5, r[COUNT]
    assert_equal [30_012.0, 0.0, 20_000.0], r[7, 3]
    assert_in_delta 30_012 / 5.0, r[VALUE]
    b = JSON.parse(r[BUCKETS])
    assert_equal M::DEFAULT_BUCKETS.map(&:to_i), b["bounds"]
    assert_equal [1, 1, 1] + [0] * 11 + [1, 1], b["counts"]
    assert r[BUCKETS].start_with?('{"bounds":[0,5,10,')
    assert_empty rows
  end

  def test_describe_sets_unit_and_buckets
    Visionary.describe_metric("took", unit: "ms", buckets: [100, 10, 10])
    Visionary.histogram("took").observe(50)
    r = rows[0]
    assert_equal "ms", r[2]
    assert_equal '{"bounds":[10,100],"counts":[0,1,0]}', r[BUCKETS]
  end

  def test_series_cap_drops_and_counts
    M.registry.max_series = 3
    5.times { |i| Visionary.counter("by_user", user: i.to_s).add(1) }
    Visionary.counter("by_user", user: "0").add(1) # existing series still records
    Visionary.counter("other").add(1)
    got, dropped = M.registry.collect
    assert_equal 4, got.size
    assert_equal({ "by_user" => 2 }, dropped)
    assert_empty M.registry.collect.last
  end

  def test_empty_name_records_nothing
    Visionary.counter("  ").add(1)
    assert_empty rows
  end

  def test_env
    c = Visionary::Config.from_env({})
    assert c.metrics
    assert_equal 15.0, c.metrics_interval
    assert_equal 1000, c.metrics_max_series
    c = Visionary::Config.from_env("VISIONARY_METRICS" => "no", "VISIONARY_METRICS_INTERVAL" => "500ms",
                                   "VISIONARY_METRICS_MAX_SERIES" => "10")
    refute c.metrics
    assert_equal 0.5, c.metrics_interval
    assert_equal 10, c.metrics_max_series
  end

  def test_writes_rows_to_sqlite
    Dir.mktmpdir do |dir|
      cfg = Visionary::Config.new.tap do |c|
        c.database_url = "sqlite://#{dir}/m.db"
        c.service = "shop"
        c.instance = "i-1"
        c.log_requests = c.system_metrics = c.spans = c.errors = c.logs = c.profile = false
        c.metrics_interval = 60
      end
      agent = Visionary::Agent.new(cfg).start
      Visionary.counter("orders_placed", region: "eu").add(2)
      Visionary.histogram("checkout_ms").observe(12)
      agent.close(10)
      db = SQLite3::Database.new("#{dir}/m.db")
      got = db.execute("SELECT service, instance, name, kind, labels, series, value, count, buckets FROM metrics ORDER BY kind")
      assert_equal ["shop", "i-1", "orders_placed", "counter", '{"region":"eu"}', "da4f5c6244c4f3e4", 2.0, nil, nil], got[0]
      assert_equal "histogram", got[1][3]
      assert_equal 1, got[1][7]
      db.close
    end
  end

  def test_no_table_without_metrics
    Dir.mktmpdir do |dir|
      cfg = Visionary::Config.new.tap do |c|
        c.database_url = "sqlite://#{dir}/m.db"
        c.log_requests = c.system_metrics = c.spans = c.errors = c.logs = c.profile = false
      end
      Visionary::Agent.new(cfg).start.close(10)
      db = SQLite3::Database.new("#{dir}/m.db")
      assert_empty db.execute("SELECT name FROM sqlite_master WHERE name = 'metrics'")
      db.close
    end
  end
end
