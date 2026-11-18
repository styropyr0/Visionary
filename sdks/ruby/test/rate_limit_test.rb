# frozen_string_literal: true

require "minitest/autorun"
require "json"
require "visionary"

class RateLimitTest < Minitest::Test
  RL = Visionary::RateLimit
  CASES = File.expand_path("../../go/ratelimit/testdata/cases.json", __dir__)

  def request(step)
    headers = step["headers"] || {}
    query = step["query"] || {}
    RL::Request.new(
      step["method"] || "GET", step["path"] || "/", step["remote_ip"] || "203.0.113.1",
      ->(name) { headers.find { |k, _| k.casecmp?(name) }&.last.to_s },
      ->(name) { query[name].to_s }
    )
  end

  # sdks/go/ratelimit/testdata/cases.json, which every SDK runs.
  def test_shared_cases
    cases = JSON.parse(File.read(CASES))
    assert_equal 25, cases.size
    cases.each do |c|
      limiter = RL::Limiter.new(c["service"])
      records = []
      limiter.trace = ->(id, outcome) { records << "#{id}:#{outcome}" }
      limiter.load(RL.parse(JSON.generate(c["config"])))
      c["steps"].each_with_index do |s, i|
        records.clear
        where = "#{c['name']} step #{i}"
        req = request(s)
        st = limiter.check_request(req, s["t"].to_f)
        u = s["user"] || {}
        limiter.check_user(st, req, RL::User.new(u["id"].to_s, u["role"].to_s), s["attrs"] || {}, s["t"].to_f)
        d = st.denial
        want = s["expect"]
        assert_equal want["status"].to_i, d ? d.status : 0, "#{where}: status"
        if d
          assert_equal want["rule"], d.rule_id, "#{where}: rule" if want.key?("rule")
          assert_equal want["retry_after"], d.retry_after, "#{where}: retry_after" if want.key?("retry_after")
          assert_equal want["body"], d.body, "#{where}: body" if want.key?("body")
          assert_equal want["content_type"], d.content_type, "#{where}: content_type" if want.key?("content_type")
        end
        assert_equal s["records"] || [], records, "#{where}: records"
      end
    end
  end

  def test_user_check_runs_once
    l = RL::Limiter.new
    l.load(RL.parse('{"rules":[{"id":"u","action":"limit","key":"user","limit":1,"window_s":60}]}'))
    req = RL::Request.new("GET", "/", "1.1.1.1")
    st = l.check_request(req, 6000.0)
    assert_nil l.check_user(st, req, RL::User.new("a", ""), {}, 6000.0)
    assert_nil l.check_user(st, req, RL::User.new("a", ""), {}, 6000.0)
    st = l.check_request(req, 6000.0)
    refute_nil l.check_user(st, req, RL::User.new("a", ""), {}, 6000.0)
  end

  def test_counters_survive_reload_unless_shape_changes
    l = RL::Limiter.new
    rule = { "id" => "r", "action" => "limit", "limit" => 1, "window_s" => 60, "algorithm" => "fixed" }
    load = ->(r) { l.load(RL.parse(JSON.generate("rules" => [r]))) }
    load.(rule)
    req = RL::Request.new("GET", "/", "1.1.1.1")
    l.check_request(req, 6000.0)
    load.(rule.merge("name" => "renamed"))
    refute_nil l.check_request(req, 6000.0).denial, "counter lost on a rename"
    load.(rule.merge("window_s" => 30))
    assert_nil l.check_request(req, 6000.0).denial, "counter kept after the window changed"
  end

  def test_records_aggregate_and_redact
    l = RL::Limiter.new("api")
    l.load(RL.parse('{"rules":[{"id":"k","name":"Keys","action":"limit","key":"header:X-Api-Key","limit":0,"window_s":60}]}'))
    req = RL::Request.new("post", "/v1/x", "1.1.1.1", ->(n) { n == "x-api-key" ? "secret-value" : "" })
    3.times { l.check_request(req, 6000.0) }
    ev = l.drain
    assert_equal 1, ev.size
    e = ev[0]
    assert_equal [3, "limited", "POST", "Keys"], [e.hits, e.outcome, e.http_method, e.rule_name]
    assert_match(/\Asha256:\h{12}\z/, e.key_value)
    assert_empty l.drain
  end

  def test_aggregates_overflow_into_other
    l = RL::Limiter.new
    l.load(RL.parse('{"rules":[{"id":"b","action":"block"}]}'))
    (RL::MAX_AGGREGATES + 5).times do |i|
      l.check_request(RL::Request.new("GET", "/", "10.0.#{i / 250}.#{i % 250}"), 1.0)
    end
    ev = l.drain
    assert_equal RL::MAX_AGGREGATES + 1, ev.size
    assert_equal ["(other)", "", 5], [ev.last.key_value, ev.last.ip, ev.last.hits]
  end

  def test_counter_cap_drops_idle_then_all
    l = RL::Limiter.new
    l.load(RL.parse('{"rules":[{"id":"c","action":"limit","limit":5,"window_s":1,"algorithm":"fixed"}]}'))
    RL::MAX_COUNTERS.times { |i| l.check_request(RL::Request.new("GET", "/", "k#{i}"), 100.0) }
    counters = l.instance_variable_get(:@set).by_id["c"].counters.map
    assert_equal RL::MAX_COUNTERS, counters.size
    l.check_request(RL::Request.new("GET", "/", "new"), 105.0)
    assert_equal 1, l.instance_variable_get(:@set).by_id["c"].counters.map.size
  end

  def test_parse_keeps_good_rules
    c = RL.parse('{"rules":[{"id":"a","action":"block","limit":"x"},{"id":"b","action":"block"}]}')
    assert_equal ["b"], c.rules.map(&:id)
    assert_raises(JSON::ParserError) { RL.parse('{"rules":') }
  end

  def test_glob
    [["/a*", "/a", true], ["/a*", "/a/b/c", true], ["*", "/", true], ["/a/*/c", "/a/b/c", true],
     ["/a/*/c", "/a/b/d", false], ["/a", "/a/", false], ["*.json", "/x/y.json", true], ["/a**b", "/ab", true]].each do |p, s, want|
      assert_equal want, RL.glob(p, s), "glob(#{p}, #{s})"
    end
  end

  def test_fails_open
    l = RL::Limiter.new
    l.load(RL.parse('{"rules":[{"id":"b","action":"block","match":{"header:X":["1"]}}]}'))
    req = RL::Request.new("GET", "/", "1.1.1.1", ->(_n) { raise "boom" })
    assert_nil l.check_request(req, 1.0).denial
  end
end
