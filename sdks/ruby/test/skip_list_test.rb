# frozen_string_literal: true

require "minitest/autorun"
require "logger"
require "visionary"

class SkipListTest < Minitest::Test
  S = Visionary::SkipList

  class FakeStore
    attr_accessor :result

    def initialize(result = []) = @result = result

    def rows(_sql)
      raise result if result.is_a?(Exception)
      result
    end
  end

  def test_parse
    assert_equal ["/health", "/static/*"], S.parse('[" /health ", "", "  ", "/static/*"]')
    assert_equal [], S.parse("not json")
    assert_equal [], S.parse('{"a": 1}')
    assert_equal [], S.parse('["/a", 1]')
    assert_equal [], S.parse("null")
    assert_equal [], S.parse("")
  end

  def test_match
    list = ["/health", "/static/*"]
    assert S.match?(list, "/health")
    refute S.match?(list, "/health/deep")
    refute S.match?(list, "/Health")
    assert S.match?(list, "/static/app.js")
    assert S.match?(list, "/static/")
    refute S.match?(list, "/static")
    assert S.match?(["*"], "/anything")
    refute S.match?([], "/health")
  end

  def test_poll_keeps_last_list_on_error
    store = FakeStore.new([["requests.other", "x"], ["requests.skip", '["/health"]']])
    s = S.new(store, Logger.new(nil))
    refute s.skip?("/health")
    s.poll
    assert s.skip?("/health")
    store.result = RuntimeError.new("no such table: visionary_settings")
    s.poll
    assert s.skip?("/health")
    store.result = [["requests.skip", "oops"]]
    s.poll
    refute s.skip?("/health")
  end

  def test_log_path_checks_prefix_and_skip_list
    cfg = Visionary::Config.new
    cfg.log_requests = true
    cfg.path_prefix = "/api"
    skip = S.new(FakeStore.new([["requests.skip", '["/api/health"]']]), Logger.new(nil))
    skip.poll
    agent = Visionary::Agent.allocate
    agent.instance_variable_set(:@config, cfg)
    agent.instance_variable_set(:@writer, Object.new)
    agent.instance_variable_set(:@skip, skip)
    assert agent.log_path?("/api/users")
    refute agent.log_path?("/api/health")
    refute agent.log_path?("/other")
  end
end
