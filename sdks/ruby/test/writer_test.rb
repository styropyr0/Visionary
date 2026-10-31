# frozen_string_literal: true

require "minitest/autorun"
require "tmpdir"
require "sqlite3"
require "visionary"

class WriterTest < Minitest::Test
  class QuietLogger
    %i[debug info warn error].each { |m| define_method(m) { |_msg = nil| nil } }
  end

  class FakeStore
    attr_reader :batches
    attr_accessor :fail

    def initialize(dialect = "sqlite")
      @dialect = dialect
      @batches = []
      @fail = false
    end

    attr_reader :dialect

    def string_params? = @dialect == "d1"

    def batch(stmts)
      raise "down" if @fail
      @batches << stmts
    end
  end

  class FakeConn
    def execute(_sql, _args) = 0
    def broken? = false
    def close; end
  end

  def writer(store, cfg = Visionary::Config.new)
    schemas = Object.new
    def schemas.ensure(_group) = true
    w = Visionary::Writer.new(cfg, store, schemas, QuietLogger.new)
    w.define_singleton_method(:sleep) { |_s| nil }
    w
  end

  def span(i, target = "db")
    ["2026-01-01T00:00:00.000Z", "r#{i}", "svc", "inst", "db", "SELECT 1", target, nil, 1.0, 2.0, "ok", nil, nil,
     nil, nil, nil]
  end

  def test_multi_row_inserts_split_at_the_parameter_limit
    store = FakeStore.new
    w = writer(store)
    assert_equal Visionary::Writer::SQL_BATCH, w.instance_variable_get(:@batch_size)
    cols = Visionary::Schema::TABLES["spans"].size
    w.send(:commit, Array.new(2000) { |i| [:span, span(i)] } + [[:release, %w[a b c d e f g]]])

    stmts = store.batches.fetch(0)
    per = Visionary::Writer::MAX_PARAMS / cols
    assert_equal [per, 2000 - per, 1], stmts.map { |sql, _| sql.scan("(?").size }
    stmts.each do |sql, args|
      assert_operator args.size, :<=, Visionary::Writer::MAX_PARAMS
      assert_equal args.size, sql.count("?")
    end
    assert_equal 2001, w.stats.written
  end

  def test_batch_size_bounds
    cfg = Visionary::Config.new
    cfg.batch_size = 5000
    assert_equal 500, writer(FakeStore.new, cfg).instance_variable_get(:@batch_size)
    cfg.batch_size = 2000
    assert_equal 2000, writer(FakeStore.new, cfg).instance_variable_get(:@batch_size)
  end

  def test_d1_stays_one_insert_per_row
    store = FakeStore.new("d1")
    cfg = Visionary::Config.new
    cfg.batch_size = 500
    w = writer(store, cfg)
    assert_equal 50, w.instance_variable_get(:@batch_size)
    w.send(:commit, [[:span, span(1)], [:span, span(2, nil)]])

    stmts = store.batches.fetch(0)
    assert_equal 2, stmts.size
    assert_equal Visionary::Schema.insert_sql("spans", true), stmts[0][0]
    assert_includes stmts[0][0], "NULLIF(?, '')"
    assert_nil stmts[1][1][6]
  end

  def test_stats_count_written_failed_and_queue_drops
    store = FakeStore.new
    cfg = Visionary::Config.new
    cfg.queue_size = 2
    w = writer(store, cfg)
    w.send(:commit, [[:span, span(1)]])
    store.fail = true
    w.send(:commit, [[:span, span(2)], [:span, span(3)]])
    3.times { |i| w.enqueue([:span, span(i)]) }
    assert_equal Visionary::WriterStats.new(queued: 2, written: 1, dropped: 3), w.stats
  end

  def test_sqlite_writes_nulls_and_uses_wal
    Dir.mktmpdir do |dir|
      store = Visionary::Store.open_url("sqlite://#{dir}/w.db")
      Visionary::Schemas.new(store, QuietLogger.new).ensure(:telemetry)
      w = writer(store)
      w.send(:commit, [[:span, span(1)], [:span, span(2, nil)], [:span, span(3, nil)]])

      assert_equal [[3, 2]], store.rows("SELECT COUNT(*), SUM(target IS NULL) FROM spans")
      assert_equal [["wal"]], store.rows("PRAGMA journal_mode")
      store.close
    end
  end

  def test_postgres_and_mysql_open_up_to_four_connections
    opened = 0
    store = Visionary::Store::SQL.new("postgres") { opened += 1; FakeConn.new }
    assert_equal 4, store.parallel
    gate = Queue.new
    threads = Array.new(6) { Thread.new { store.send(:run) { gate.pop } } }
    sleep 0.1 until store.instance_variable_get(:@all).size == 4
    6.times { gate << 1 }
    threads.each(&:join)
    assert_equal 4, opened
    assert_equal 1, Visionary::Store::SQL.new("sqlite") { FakeConn.new }.parallel
  end

  def test_multi_row_sql_and_rebind
    cols = Visionary::Schema::RELEASE_COLUMNS.join(", ")
    assert_equal "INSERT INTO releases (#{cols}) VALUES (?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?)",
                 Visionary::Schema.insert_rows_sql("releases", 2)
    assert_equal "INSERT INTO t (a, b) VALUES ($1, $2), ($3, $4)",
                 Visionary::Store.rebind("INSERT INTO t (a, b) VALUES (?, ?), (?, ?)")
    assert_equal "SELECT '?' WHERE a = $1", Visionary::Store.rebind("SELECT '?' WHERE a = ?")
  end
end
