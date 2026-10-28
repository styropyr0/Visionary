# frozen_string_literal: true

module Visionary
  # Everything the agent needs. Config.from_env fills it from VISIONARY_*
  # (see sdks/SPEC.md "Environment"); set attributes in Visionary.configure to
  # override or to add hooks.
  #
  # Storage is chosen in this order: +connection+ (an ActiveRecord connection
  # class such as ActiveRecord::Base), then +database_url+, then the D1
  # credentials.
  class Config
    COMMIT_VARIABLES = %w[
      RAILWAY_GIT_COMMIT_SHA RENDER_GIT_COMMIT VERCEL_GIT_COMMIT_SHA HEROKU_SLUG_COMMIT
      SOURCE_VERSION GIT_COMMIT GITHUB_SHA K_REVISION
    ].freeze

    attr_accessor :database_url, :d1_account_id, :d1_database_id, :d1_api_token, :d1_api_base,
                  :connection, :instance, :logger,
                  :log_requests, :path_prefix, :capture_bodies, :max_body_bytes,
                  :queue_size, :batch_size, :flush_interval, :log_retention_days,
                  :redact_headers, :redact_fields, :redact_request_fields,
                  :error_code_field, :error_key_field,
                  :system_metrics, :system_interval, :system_retention_days, :disk_path,
                  :service, :version, :spans, :span_threshold_ms, :span_max, :errors,
                  :profile, :logs, :log_level, :log_queue_size

    # Runs on the writer thread, once per distinct user per batch, for users
    # missing a handle or role: ->(id) { [handle, role] } or nil.
    attr_accessor :lookup_user
    # Returns the route pattern for a Rack env when the integration can't
    # see it: ->(env) { "/users/:id" }.
    attr_accessor :route
    # Returns pool stats as a Hash: total, idle, in_use, max, acquires,
    # empty_acquires, acquire_wait_ms (the last three cumulative). Defaults to
    # the ActiveRecord pool when ActiveRecord is loaded.
    attr_accessor :db_stats

    def initialize
      @database_url = ""
      @d1_account_id = @d1_database_id = @d1_api_token = @d1_api_base = ""
      @connection = nil
      @instance = ""
      @logger = nil
      @log_requests = true
      @path_prefix = ""
      @capture_bodies = true
      @max_body_bytes = 8192
      @queue_size = 5000
      @batch_size = 50
      @flush_interval = 3.0
      @log_retention_days = 0 # 0: off, the Visionary server applies Settings → Retention
      @redact_headers = []
      @redact_fields = []
      @redact_request_fields = []
      @error_code_field = "error.code"
      @error_key_field = "error.key"
      @system_metrics = true
      @system_interval = 15.0
      @system_retention_days = 0 # 0: off, as above
      @disk_path = ""
      @service = nil
      @version = nil
      @spans = true
      @span_threshold_ms = 100.0
      @span_max = 100
      @errors = true
      # false: never profile, whatever the dashboard says.
      @profile = true
      # false: never capture application logs (VISIONARY_LOGS).
      @logs = true
      # The lowest level kept until Settings → Logs has been read.
      @log_level = "info"
      @log_queue_size = 10_000
    end

    def self.from_env(env = ENV)
      s = ->(key) { env[key].to_s.strip }
      c = new
      c.database_url = s.("VISIONARY_DATABASE_URL")
      c.d1_account_id = s.("D1_ACCOUNT_ID")
      c.d1_database_id = s.("D1_DATABASE_ID")
      c.d1_api_token = s.("D1_API_TOKEN")
      c.d1_api_base = s.("D1_API_BASE")
      c.instance = s.("VISIONARY_INSTANCE")
      c.log_requests = parse_bool(s.("VISIONARY_LOG_REQUESTS"), true)
      c.path_prefix = s.("VISIONARY_LOG_PATH_PREFIX")
      c.capture_bodies = parse_bool(s.("VISIONARY_LOG_BODIES"), true)
      c.max_body_bytes = parse_int(s.("VISIONARY_LOG_MAX_BODY_BYTES"), 8192)
      c.log_retention_days = parse_int(s.("VISIONARY_LOG_RETENTION_DAYS"), 0)
      c.redact_headers = parse_list(s.("VISIONARY_LOG_REDACT_HEADERS"))
      c.redact_fields = parse_list(s.("VISIONARY_LOG_REDACT_FIELDS"))
      c.redact_request_fields = parse_list(s.("VISIONARY_LOG_REDACT_REQUEST_FIELDS"))
      c.error_code_field = s.("VISIONARY_LOG_ERROR_CODE_FIELD").then { |v| v.empty? ? "error.code" : v }
      c.error_key_field = s.("VISIONARY_LOG_ERROR_KEY_FIELD").then { |v| v.empty? ? "error.key" : v }
      c.system_metrics = parse_bool(s.("VISIONARY_SYSTEM_METRICS"), true)
      c.system_interval = parse_duration(s.("VISIONARY_SYSTEM_INTERVAL"), 15.0)
      c.system_retention_days = parse_int(s.("VISIONARY_SYSTEM_RETENTION_DAYS"), 0)
      c.service = s.("VISIONARY_SERVICE").then { |v| v.empty? ? nil : v }
      c.version = release_from(env)
      c.spans = parse_bool(s.("VISIONARY_SPANS"), true)
      c.span_threshold_ms = parse_float(s.("VISIONARY_SPAN_THRESHOLD_MS"), 100.0)
      c.span_max = parse_int(s.("VISIONARY_SPAN_MAX"), 100)
      c.errors = parse_bool(s.("VISIONARY_ERRORS"), true)
      c.profile = parse_bool(s.("VISIONARY_PROFILE"), true)
      c.logs = parse_bool(s.("VISIONARY_LOGS"), true)
      c.log_level = s.("VISIONARY_LOG_LEVEL").then { |v| Logs.parse_level(v).positive? ? v.downcase : "info" }
      c
    end

    # VISIONARY_RELEASE, else the platform's commit variable; commit hashes
    # are shortened to 12 characters.
    def self.release_from(env)
      ["VISIONARY_RELEASE", *COMMIT_VARIABLES].each do |key|
        v = env[key].to_s.strip
        next if v.empty?
        return v.match?(/\A\h{13,64}\z/) ? v[0, 12] : v
      end
      nil
    end

    def self.parse_bool(v, default)
      case v.to_s.strip.downcase
      when "true", "1", "yes" then true
      when "false", "0", "no" then false
      else default
      end
    end

    def self.parse_int(v, default)
      n = Integer(v.to_s.strip, 10)
      n >= 0 ? n : default
    rescue ArgumentError
      default
    end

    def self.parse_float(v, default)
      n = Float(v.to_s.strip)
      n >= 0 ? n : default
    rescue ArgumentError
      default
    end

    def self.parse_list(v)
      v.to_s.split(",").map(&:strip).reject(&:empty?)
    end

    # Seconds from "15", "15s", "500ms", "1m", "1h".
    def self.parse_duration(v, default)
      v = v.to_s.strip.downcase
      return default if v.empty?
      m = v.match(/\A(\d+(?:\.\d+)?)(ms|s|m|h)?\z/)
      return default unless m
      n = m[1].to_f
      case m[2]
      when "ms" then n / 1000.0
      when "m" then n * 60
      when "h" then n * 3600
      else n
      end
    end
  end
end
