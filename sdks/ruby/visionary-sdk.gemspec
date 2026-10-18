# frozen_string_literal: true

require_relative "lib/visionary/version"

Gem::Specification.new do |spec|
  spec.name = "visionary-sdk"
  spec.version = Visionary::VERSION
  spec.summary = "Request logs, spans, errors and system metrics for Visionary, from Rack and Rails apps"
  spec.description = <<~DESC
    The Ruby collector for Visionary. A Rack middleware (with a Railtie for Rails)
    writes one row per HTTP request, database and outbound HTTP spans, errors,
    deploy markers and periodic host/process samples to PostgreSQL, MySQL,
    SQLite or Cloudflare D1, on background threads.
  DESC
  spec.authors = ["styropyr0"]
  spec.license = "Apache-2.0"
  spec.homepage = "https://github.com/styropyr0/Visionary"
  spec.metadata = {
    "homepage_uri" => spec.homepage,
    "source_code_uri" => "https://github.com/styropyr0/Visionary/tree/main/sdks/ruby",
    "changelog_uri" => "https://github.com/styropyr0/Visionary/blob/main/sdks/ruby/CHANGELOG.md",
    "bug_tracker_uri" => "https://github.com/styropyr0/Visionary/issues",
    "documentation_uri" => "https://github.com/styropyr0/Visionary/tree/main/sdks/ruby#readme",
    "rubygems_mfa_required" => "true"
  }
  spec.required_ruby_version = ">= 3.1"
  # LICENSE is copied from the repository root before `gem build` (see .github/workflows/sdks.yml).
  spec.files = Dir["lib/**/*.{rb,sql}"] + %w[README.md CHANGELOG.md] + Dir["LICENSE"]
  spec.require_paths = ["lib"]
  # No runtime dependencies. The database driver is the app's choice:
  # pg, mysql2 or sqlite3 for VISIONARY_DATABASE_URL, or ActiveRecord's pool.
end
