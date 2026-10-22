# frozen_string_literal: true

require "minitest/autorun"
require "logger"
require "visionary"

# Retention is the Visionary server's job: the SDK purges only when asked.
class RetentionTest < Minitest::Test
  def test_defaults_are_off
    c = Visionary::Config.from_env({})
    assert_equal 0, c.log_retention_days
    assert_equal 0, c.system_retention_days
    assert_equal 0, Visionary::Config.new.log_retention_days
    assert_equal 0, Visionary::Config.new.system_retention_days
  end

  def test_env_turns_it_on
    c = Visionary::Config.from_env("VISIONARY_LOG_RETENTION_DAYS" => "90", "VISIONARY_SYSTEM_RETENTION_DAYS" => "7")
    assert_equal 90, c.log_retention_days
    assert_equal 7, c.system_retention_days
  end

  def test_no_thread_at_zero
    log = Logger.new(nil)
    off = Visionary::Retention.new(nil, log, "api_logs" => 0, "spans" => 0, "system_metrics" => 0, "profiles" => 0)
    refute off.needed?
    off.start
    assert_nil off.instance_variable_get(:@thread)
    assert Visionary::Retention.new(nil, log, "api_logs" => 30).needed?
  end
end
