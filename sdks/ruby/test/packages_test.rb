# frozen_string_literal: true

require "minitest/autorun"
require "logger"
require "tmpdir"
require "sqlite3"
require "visionary"

class PackagesTest < Minitest::Test
  Spec = Struct.new(:name, :version)

  def test_gems_from_loaded_specs
    specs = { "rack" => Spec.new("rack", Gem::Version.new("3.1.8")) }
    assert_equal [%w[rack 3.1.8]], Visionary::Packages.gems(specs)
    assert(Visionary::Packages.gems.any? { |name, _| name == "minitest" })
  end

  def test_rows_skip_empty_and_duplicates_and_cap
    rows = Visionary::Packages.rows([%w[rack 3.1.8], %w[rack 3.1.8], ["broken", ""]], "t", nil, "i", "abc")
    assert_equal [["t", nil, "i", "abc", "RubyGems", "rack", "3.1.8"]], rows
    many = Array.new(6000) { |i| ["g#{i}", "1.0"] }
    assert_equal 5000, Visionary::Packages.rows(many, "t", nil, "i", nil).size
  end

  def test_report_writes_one_created_at
    Dir.mktmpdir do |dir|
      store = Visionary::Store.open_url("sqlite://#{dir}/p.db")
      cfg = Visionary::Config.new
      cfg.service = "api"
      schemas = Visionary::Schemas.new(store, Logger.new(nil))
      Visionary::Packages.new(cfg, "i", store, schemas, Logger.new(nil)).report

      rows = store.rows("SELECT service, instance, ecosystem, COUNT(*), COUNT(DISTINCT created_at) FROM app_packages")
      assert_equal "api", rows[0][0]
      assert_equal "RubyGems", rows[0][2]
      assert_operator rows[0][3].to_i, :>, 0
      assert_equal 1, rows[0][4].to_i
      store.close
    end
  end

  def test_env_turns_it_off
    assert Visionary::Config.from_env({}).packages
    refute Visionary::Config.from_env("VISIONARY_PACKAGES" => "false").packages
  end
end
