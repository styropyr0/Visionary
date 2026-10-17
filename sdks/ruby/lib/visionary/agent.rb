# frozen_string_literal: true

require "socket"

module Visionary
  # Logs to stderr when the app gives no logger (the logger gem leaves the
  # default gems in Ruby 3.5, so it is not required here).
  class StderrLogger
    LEVELS = { debug: 0, info: 1, warn: 2, error: 3 }.freeze

    def initialize(level = :info, io = $stderr)
      @level = LEVELS.fetch(level)
      @io = io
    end

    LEVELS.each do |name, n|
      define_method(name) do |msg = nil, &block|
        return if n < @level
        @io.puts("#{Visionary.timestamp(Time.now)} #{name.upcase} #{msg || block&.call}")
      rescue IOError
        nil
      end
    end
  end

  # Runs request logging, telemetry and system metrics for one process. With
  # no database configured it logs one warning and every call is a no-op; it
  # never raises into the app once built.
  class Agent
    attr_reader :config, :http, :telemetry, :logger, :instance

    def initialize(cfg)
      @config = cfg
      @logger = cfg.logger || StderrLogger.new
      @http = System::HttpCounters.new
      @closed = false
      @started = false
      @lock = Mutex.new
      @store = open_store(cfg)
      return unless @store

      @instance = cfg.instance.to_s.empty? ? Socket.gethostname : cfg.instance
      @schemas = Schemas.new(@store, @logger)
      if cfg.log_requests || cfg.spans || cfg.errors || cfg.version
        @writer = Writer.new(cfg, @store, @schemas, @logger)
      end
      @telemetry = Telemetry::Recorder.new(cfg, @instance, @writer) if cfg.spans || cfg.errors
      if cfg.system_metrics && cfg.system_interval.to_f.positive?
        @system = System::Recorder.new(cfg, @instance, @store, @schemas, @http, @logger)
      end
      tables = {}
      tables["api_logs"] = cfg.log_retention_days if cfg.log_requests
      if @telemetry
        tables["spans"] = cfg.log_retention_days
        tables["errors"] = cfg.log_retention_days
      end
      tables["system_metrics"] = cfg.system_retention_days if @system
      @retention = Retention.new(@store, @logger, tables)
    end

    def enabled? = !@store.nil? && !@closed && (!@writer.nil? || !@system.nil?)

    def log_path?(path) = @config.log_requests && !@writer.nil? && path.start_with?(@config.path_prefix.to_s)

    # Queues a row for the writer; never blocks.
    def enqueue(item)
      @writer&.enqueue(item) unless @closed
    end

    # The route pattern for a finished request: the app's own resolver,
    # then what the framework left in the env.
    def route_for(env)
      route = @config.route ? System::Sources.quiet { @config.route.call(env) }.to_s : ""
      route = framework_route(env) if route.empty?
      route.sub(/\(\.:format\)\z/, "")
    end

    # Launches the background threads. Tables are created on them, so a slow
    # or unreachable database never delays startup. Safe to call twice.
    def start
      @lock.synchronize do
        return self if @started || @closed || !enabled?
        @started = true
        @pid = Process.pid
      end
      @writer&.start
      if @config.log_requests
        @logger.info("visionary: logging requests (path_prefix=#{@config.path_prefix.inspect}, " \
                     "bodies=#{@config.capture_bodies}, retention_days=#{@config.log_retention_days})")
      end
      if @system
        @system.start
        @logger.info("visionary: recording system metrics every #{@config.system_interval}s as #{@instance}")
      end
      @retention.start
      install_hooks
      release
      self
    end

    # Flushes queued rows and buffered samples, waiting at most timeout
    # seconds in total.
    def close(timeout = 10)
      @lock.synchronize do
        return if @closed
        @closed = true
      end
      deadline = mono + timeout
      @retention&.close
      idle = @writer ? @writer.close(timeout) : true
      idle = @system.close([deadline - mono, 0].max) && idle if @system
      @store.close if idle && @store
    rescue StandardError => e
      @logger.error("visionary: close failed: #{e.message}")
    end

    # Threads don't survive fork (Puma and Unicorn workers with preloading):
    # restart them in the child with fresh queues and connections.
    def after_fork
      return unless @started && !@closed && @pid != Process.pid
      @pid = Process.pid
      @lock = Mutex.new
      @http = System::HttpCounters.new
      @store.after_fork
      @system&.http = @http
      @system&.after_fork
      @writer&.after_fork
      @retention&.after_fork if @retention&.needed?
      @logger.info("visionary: restarted in forked process #{@pid}")
    end

    private

    def open_store(cfg)
      store = Store.open(cfg)
      unless store
        @logger.warn("visionary: no database configured (set VISIONARY_DATABASE_URL or D1_*); collection is off")
      end
      store
    rescue StandardError => e
      @logger.warn("#{e.message}; collection is off")
      nil
    end

    def install_hooks
      return unless @telemetry&.spans_on
      Telemetry::NetHTTPHook.install
      Telemetry::ActiveRecordHook.install if defined?(::ActiveRecord::Base) && defined?(::ActiveSupport::Notifications)
    rescue StandardError => e
      @logger.warn("visionary: span hooks not installed: #{e.message}")
    end

    # One deploy marker per process start when the release is known.
    def release
      return unless @writer && @config.version
      @writer.enqueue([:release, [Visionary.timestamp(Time.now), @config.service, @instance, @config.version,
                                  "ruby", VERSION, Telemetry.runtime]])
    end

    def framework_route(env)
      if (p = env["action_dispatch.route_uri_pattern"]) then p.to_s
      elsif (r = env["sinatra.route"]) then r.to_s.split(" ", 2).last.to_s
      elsif (info = env["grape.routing_args"]) && info.is_a?(Hash) && info[:route_info].respond_to?(:path)
        info[:route_info].path.to_s
      else ""
      end
    rescue StandardError
      ""
    end

    def mono = Process.clock_gettime(Process::CLOCK_MONOTONIC)
  end
end
