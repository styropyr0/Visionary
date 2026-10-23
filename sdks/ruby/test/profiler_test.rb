# frozen_string_literal: true

require "minitest/autorun"
require "json"
require "tmpdir"
require "visionary"

class ProfilerTest < Minitest::Test
  P = Visionary::Profiler

  class FakeStore
    attr_accessor :result

    def initialize(result = []) = @result = result

    def rows(_sql)
      raise result if result.is_a?(Exception)
      result
    end
  end

  class FakeWriter
    attr_reader :items

    def initialize = @items = []
    def enqueue(item) = @items << item
  end

  class QuietLogger
    attr_reader :lines

    def initialize = @lines = []
    %i[debug info warn error].each { |m| define_method(m) { |msg = nil| @lines << msg } }
  end

  def config(service: nil)
    Visionary::Config.new.tap { |c| c.service = service }
  end

  def profiler(rows, service: nil, started_at: Time.now - 60)
    @log = QuietLogger.new
    @writer = FakeWriter.new
    P.new(config(service: service), "host-1", FakeStore.new(rows), @writer, @log, started_at: started_at)
  end

  # --- folding -------------------------------------------------------------

  def test_fold_sorts_heaviest_first_and_counts_samples
    folded, samples = P.fold({ "a;b" => 2, "a;c" => 5, "d" => 1 })
    assert_equal "a;c 5\na;b 2\nd 1\n", folded
    assert_equal 8, samples
  end

  def test_fold_caps_bytes_and_sums_the_rest_into_other
    counts = {}
    2000.times { |i| counts["frame_#{i} #{'x' * 80} app.rb:#{i}"] = 2000 - i }
    folded, samples = P.fold(counts)
    assert_operator folded.bytesize, :<=, P::MAX_FOLDED + 32
    last = folded.lines.last
    assert_match(/\A\(other\) \d+\n\z/, last)
    kept = folded.lines[0..-2].sum { |l| l.split(" ").last.to_i }
    assert_equal samples, kept + last.split(" ").last.to_i
    assert folded.start_with?("frame_0 ")
    counts = folded.lines.reject { |l| l.include?("(other)") }.map { |l| l.split(" ").last.to_i }
    assert_equal counts.sort.reverse, counts
  end

  # Stacks that don't fit are cut after the deepest frame they share with a
  # kept stack, so their time stays under the right caller.
  def test_fold_rolls_up_into_callers
    heavy = "serve;handler;#{'h' * (P::KEEP_WHOLE - 40)}"
    counts = {
      heavy => 100,
      "serve;handler;json;encode;#{'e' * 30_000}" => 7,
      "serve;handler;json;encode;#{'f' * 30_000}" => 5,
      "serve;handler" => 4, # leads into the kept stack: kept as it is
      "worker;loop;#{'w' * 30_000}" => 3
    }
    folded, samples = P.fold(counts)
    assert_equal 119, samples
    ["#{heavy} 100\n", "serve;handler;(other) 12\n", "serve;handler 4\n", "(other) 3\n"].each do |want|
      assert_includes folded, want
    end
    assert_operator folded.bytesize, :<=, P::MAX_FOLDED
  end

  # --- frame naming --------------------------------------------------------

  def frames(fold_case: false)
    P::Frames.new(root: "/srv/app", gem_dirs: ["/usr/lib/ruby/gems/3.4.0/gems", "/usr/lib/ruby/gems/3.4.0/bundler/gems"],
                  std_dirs: ["/usr/lib/ruby/3.4.0"], fold_case: fold_case)
  end

  Loc = Struct.new(:label, :base_label, :absolute_path, :path, :lineno)

  def test_frame_names_by_owner
    f = frames
    assert_equal "app/models/user.rb", f.file("/srv/app/app/models/user.rb")
    assert_equal "lib:rack/rack/builder.rb", f.file("/usr/lib/ruby/gems/3.4.0/gems/rack-3.2.7/lib/rack/builder.rb")
    assert_equal "lib:rack-protection/rack/protection.rb",
                 f.file("/usr/lib/ruby/gems/3.4.0/gems/rack-protection-4.2.1/lib/rack/protection.rb")
    assert_equal "lib:sqlite3/sqlite3/database.rb",
                 f.file("/usr/lib/ruby/gems/3.4.0/gems/sqlite3-2.10.0-x64-mingw-ucrt/lib/sqlite3/database.rb")
    assert_equal "lib:rails/actionpack/lib/action_controller/metal.rb",
                 f.file("/usr/lib/ruby/gems/3.4.0/bundler/gems/rails-0a1b2c3d4e5f/actionpack/lib/action_controller/metal.rb")
    assert_equal "lib:puma/puma/server.rb", f.file("/srv/app/vendor/bundle/ruby/3.4.0/gems/puma-8.0.2/lib/puma/server.rb")
    assert_equal "std:json/common.rb", f.file("/usr/lib/ruby/3.4.0/json/common.rb")
    assert_equal "std:kernel.rb", f.file("<internal:kernel>")
    assert_nil f.file("(eval at /srv/app/x.rb:1)")
    assert_nil f.file(nil)
    assert_equal "/opt/other/thing.rb", f.file("/opt/other/thing.rb")
  end

  def test_frame_text_and_stack_order
    f = frames
    inner = Loc.new("Fib#calc", "calc", "/srv/app/fib.rb", "/srv/app/fib.rb", 7)
    outer = Loc.new("block in <main>", "<main>", "/srv/app/config.ru", "/srv/app/config.ru", 3)
    cfunc = Loc.new("Integer#times", "times", nil, "<internal:numeric>", 257)
    weird = Loc.new("a;b", "a;b", nil, nil, 0)
    assert_equal "Fib#calc fib.rb:7", f.frame(inner)
    assert_equal "Integer#times std:numeric.rb:257", f.frame(cfunc)
    assert_equal "a,b", f.frame(weird)
    # backtrace_locations is innermost first; folded is outermost first, and
    # only the sampled frame keeps its line.
    assert_equal "block in <main> config.ru;Integer#times std:numeric.rb;Fib#calc fib.rb:7",
                 f.stack([inner, cfunc, outer])
  end

  def test_windows_paths_fold_case
    f = P::Frames.new(root: "C:/Work/App", gem_dirs: ["C:/Ruby34-x64/lib/ruby/gems/3.4.0/gems"],
                      std_dirs: ["C:/Ruby34-x64/lib/ruby/3.4.0"], fold_case: true)
    assert_equal "app.rb", f.file("c:/work/app/app.rb")
    assert_equal "lib:rack/rack.rb", f.file("C:\\Ruby34-x64\\lib\\ruby\\gems\\3.4.0\\gems\\rack-3.2.7\\lib\\rack.rb")
    assert_equal "std:set.rb", f.file("C:/Ruby34-x64/lib/ruby/3.4.0/set.rb")
  end

  def test_real_locations_from_this_process
    f = P::Frames.new
    loc = caller_locations(0, 1).first
    assert_match(/\Atest_real_locations_from_this_process test\/profiler_test.rb:\d+\z/, f.frame(loc).sub("ProfilerTest#", ""))
    std = nil
    [1].each { std = caller_locations(0, 2)[1] } # Array#each, a C method shown at its caller
    refute_nil std
    json = JSON.method(:generate).source_location&.first
    assert_match(/\A(std|lib):json\//, f.file(json)) if json
  end

  # --- settings ------------------------------------------------------------

  def test_parse_settings_defaults
    s = P.parse_settings([])
    refute s.enabled
    assert_nil s.services
    assert_equal 300.0, s.interval_s
    assert_equal 10_000.0, s.duration_ms
    assert_nil s.now
  end

  def test_parse_settings_values_and_garbage
    s = P.parse_settings([["profile.enabled", "true"], ["profile.services", '["api","web"]'],
                          ["profile.interval_s", "60"], ["profile.duration_ms", "2500"],
                          ["profile.now", "2026-10-22T10:00:00.000Z"]])
    assert s.enabled
    assert_equal %w[api web], s.services
    assert_equal 60.0, s.interval_s
    assert_equal 2500.0, s.duration_ms
    assert_equal "2026-10-22T10:00:00.000Z", s.now

    bad = P.parse_settings([["profile.enabled", "yes please"], ["profile.services", "not json"],
                            ["profile.interval_s", "-1"], ["profile.duration_ms", "abc"]])
    refute bad.enabled
    assert_nil bad.services
    assert_equal 300.0, bad.interval_s
    assert_equal 10_000.0, bad.duration_ms
  end

  def test_services_filter
    assert P.participates?(nil, "api")
    assert P.participates?([], "api")
    assert P.participates?([], nil)
    assert P.participates?(%w[api web], "api")
    refute P.participates?(%w[api web], "worker")
    refute P.participates?(%w[api], nil)
    refute P.participates?(%w[api], "")
  end

  def test_now_freshness
    start = Time.utc(2026, 10, 22, 10, 0, 0)
    now = start + 120
    assert P.now_due?("2026-10-22T10:01:00.000Z", start, now)
    assert P.now_due?("2026-10-22 10:01:00", start, now)
    refute P.now_due?("2026-10-22T09:59:59.000Z", start, now), "older than the process"
    refute P.now_due?("2026-10-22T10:01:00.000Z", start, start + 60 + 301), "more than 5 minutes old"
    refute P.now_due?("soon", start, now)
    refute P.now_due?(nil, start, now)
  end

  def test_poll_turns_on_and_off_and_logs_once_each
    p = profiler([["profile.enabled", "true"]], service: "api")
    p.poll
    assert p.active?
    p.poll
    store = p.instance_variable_get(:@store)
    store.result = [["profile.enabled", "false"]]
    p.poll
    refute p.active?
    assert_equal 1, @log.lines.grep(/profiling on/).size
    assert_equal 1, @log.lines.grep(/profiling off/).size
  end

  def test_poll_honours_services
    p = profiler([["profile.enabled", "true"], ["profile.services", '["web"]']], service: "api")
    p.poll
    refute p.active?
    p2 = profiler([["profile.enabled", "true"], ["profile.services", '["web"]']], service: "web")
    p2.poll
    assert p2.active?
  end

  def test_poll_profile_now_fires_once_and_respects_services
    fresh = Visionary.timestamp(Time.now - 5)
    p = profiler([["profile.now", fresh]], service: "api")
    assert p.poll, "fresh profile.now asks for a profile even while disabled"
    refute p.active?
    refute p.poll, "same value does not fire twice"

    stale = profiler([["profile.now", Visionary.timestamp(Time.now - 400)]], started_at: Time.now - 3600)
    refute stale.poll

    before_start = profiler([["profile.now", Visionary.timestamp(Time.now - 30)]], started_at: Time.now - 10)
    refute before_start.poll

    other = profiler([["profile.now", fresh], ["profile.services", '["web"]']], service: "api")
    refute other.poll
  end

  def test_poll_read_failure_means_off_logged_once
    p = profiler(RuntimeError.new("no such table: visionary_settings"))
    refute p.poll
    refute p.poll
    refute p.active?
    assert_equal 1, @log.lines.grep(/settings unreadable/).size
  end

  def test_visionary_profile_false_is_a_hard_off
    assert Visionary::Config.from_env({}).profile
    refute Visionary::Config.from_env({ "VISIONARY_PROFILE" => "false" }).profile
    Dir.mktmpdir do |dir|
      base = { "VISIONARY_DATABASE_URL" => "sqlite:///#{dir}/v.db", "VISIONARY_SYSTEM_METRICS" => "false" }
      on = Visionary::Agent.new(Visionary::Config.from_env(base).tap { |c| c.logger = QuietLogger.new })
      off = Visionary::Agent.new(Visionary::Config.from_env(base.merge("VISIONARY_PROFILE" => "false"))
                                                 .tap { |c| c.logger = QuietLogger.new })
      refute_nil on.instance_variable_get(:@profiler)
      assert_nil off.instance_variable_get(:@profiler)
      [on, off].each(&:close)
    end
  end

  # --- sampling ------------------------------------------------------------

  def busy_loop(stop)
    x = 0
    x += 1 until stop[0]
    x
  end

  def test_status_filter
    stop = [false]
    q = Queue.new
    sleeper = Thread.new { sleep }
    waiter = Thread.new { q.pop }
    spinner = Thread.new { busy_loop(stop) }
    own = Thread.new { sleep 0.01 until stop[0] }
    own.name = "visionary-writer"
    internal = Thread.new do
      Visionary::Telemetry.internal!
      busy_loop(stop)
    end
    sleep 0.1
    refute P.sample?(sleeper)
    refute P.sample?(waiter)
    assert P.sample?(spinner)
    refute P.sample?(own)
    refute P.sample?(internal)
    refute P.sample?(Thread.current)
  ensure
    stop[0] = true
    q&.push(1)
    [sleeper, waiter, spinner, own, internal].compact.each { |t| t.kill.join(1) }
  end

  def test_quantum_lowers_priorities_and_restores_them
    q = P::Quantum.new(enabled: true)
    a = Thread.new { sleep }
    b = Thread.new { sleep }
    b.priority = 2
    sleep 0.05
    [a, b].each { |t| q.shorten(t) }
    q.first_pass_done
    assert_equal [-3, -3], [a.priority, b.priority]
    c = Thread.new { sleep } # started during the profile from a lowered thread
    c.priority = -3
    d = Thread.new { sleep } # started after the last pass
    d.priority = -3
    q.shorten(c)
    q.restore
    assert_equal [0, 2, 0, 0], [a, b, c, d].map(&:priority)
  ensure
    [a, b, c, d].compact.each { |t| t.kill.join(1) }
  end

  def run_capture(duration_ms:, finish_request:, min_samples: P::MIN_SAMPLES)
    p = profiler([["profile.enabled", "true"], ["profile.duration_ms", duration_ms.to_s]])
    p.poll
    stop = [false]
    spinner = Thread.new { busy_loop(stop) }
    idle = Array.new(3) { Thread.new { sleep } }
    reporter = Thread.new do
      sleep 0.05
      P::Tally.finished("GET", "/fib", 12.4) if finish_request
    end
    row = p.capture(min_samples)
    [row, @writer.items]
  ensure
    stop[0] = true
    [spinner, reporter, *idle].compact.each { |t| t.kill.join(1) }
  end

  def test_capture_names_the_busy_method_and_routes
    row, items = run_capture(duration_ms: 500, finish_request: true)
    refute_nil row, "profile kept"
    assert_equal [[:profile, row]], items
    created_at, _service, instance, _version, sdk, duration_ms, samples, routes, folded = row
    assert_match(/\A\d{4}-\d\d-\d\dT.*Z\z/, created_at)
    assert_equal "host-1", instance
    assert_equal "ruby", sdk
    assert_operator duration_ms, :>=, 450
    assert_operator samples, :>=, P::MIN_SAMPLES
    assert_equal({ "GET /fib" => 12 }, JSON.parse(routes))
    assert_match(/busy_loop test\/profiler_test.rb:\d+ \d+$/, folded.lines.first.chomp.sub("ProfilerTest#", ""))
    refute_match(/Kernel#sleep/, folded, "sleeping threads are left out")
    assert_equal samples, folded.lines.sum { |l| l.split(" ").last.to_i }
  end

  def test_capture_kept_without_a_finished_request
    row, items = run_capture(duration_ms: 300, finish_request: false)
    refute_nil row, "profile kept"
    assert_equal [[:profile, row]], items
    assert_operator row[6], :>=, P::MIN_SAMPLES
    assert_equal({}, JSON.parse(row[7]))
  end

  def test_capture_dropped_with_too_few_samples
    row, items = run_capture(duration_ms: 50, finish_request: true)
    assert_nil row
    assert_empty items
  end

  def test_profile_now_kept_with_few_samples
    row, items = run_capture(duration_ms: 50, finish_request: false, min_samples: 1)
    refute_nil row, "profile kept"
    assert_equal [[:profile, row]], items
    assert_operator row[6], :<, P::MIN_SAMPLES
  end

  def test_capture_dropped_when_closed
    p = profiler([["profile.enabled", "true"], ["profile.duration_ms", "300"]])
    p.poll
    p.close
    assert_nil p.capture(1)
    assert_empty @writer.items
  end
end
