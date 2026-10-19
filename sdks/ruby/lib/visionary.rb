# frozen_string_literal: true

require_relative "visionary/version"
require_relative "visionary/config"
require_relative "visionary/schema"
require_relative "visionary/store"
require_relative "visionary/schemas"
require_relative "visionary/redactor"
require_relative "visionary/context"
require_relative "visionary/queue"
require_relative "visionary/writer"
require_relative "visionary/retention"
require_relative "visionary/system/sources"
require_relative "visionary/system/cgroup"
require_relative "visionary/system/recorder"
require_relative "visionary/telemetry"
require_relative "visionary/telemetry/hooks"
require_relative "visionary/body"
require_relative "visionary/middleware"
require_relative "visionary/agent"
require_relative "visionary/fork"

# Visionary collects what the Visionary server analyses, straight from a Ruby
# service: one api_logs row per HTTP request, spans for database and outbound
# HTTP calls, errors, deploy markers and periodic system_metrics samples.
#
#   # config.ru (Sinatra, Roda, Hanami, any Rack app)
#   require "visionary"
#   use Visionary::Middleware
#
# In Rails the Railtie inserts the middleware. Everything is configured
# through VISIONARY_* environment variables, or Visionary.configure.
module Visionary
  @lock = Mutex.new
  @agent = nil
  @config = nil

  class << self
    # The configuration the agent starts with (from the environment unless
    # set). Change it before the first request, e.g. in an initializer.
    def config
      @config ||= Config.from_env
    end

    attr_writer :config

    def configure
      yield config
      config
    end

    # Builds and starts the process-wide agent; later calls return it. Never
    # raises: on any problem the agent is a no-op.
    def start(cfg = nil)
      return @agent if @agent
      @lock.synchronize do
        return @agent if @agent
        @config = cfg if cfg
        agent = begin
          Agent.new(config).start
        rescue StandardError => e
          warn("visionary: start failed (#{e.class}: #{e.message}); collection is off")
          Agent.new(Config.new.tap { |c| c.log_requests = c.system_metrics = c.spans = c.errors = false })
        end
        at_exit { agent.close }
        @agent = agent
      end
    end

    # The running agent, started on first use.
    def agent = @agent || start

    # The agent's span and error recorder, or nil when collection is off.
    def telemetry = @agent&.telemetry

    # Flushes and stops collection, waiting at most timeout seconds.
    def close(timeout = 10)
      @agent&.close(timeout)
    end

    # Attaches the signed-in user to the request being logged. Call it from
    # authentication code (a before_action, a Warden callback, a Rack
    # middleware) once the user is known. Only id is required. No-op outside a
    # request seen by Visionary::Middleware.
    def set_user(id, handle: nil, role: nil)
      RequestContext.current&.set_user(id, handle, role)
      nil
    end

    # Records the route pattern (e.g. "/users/:id") for routers Visionary
    # can't see. No-op outside a request.
    def set_route(pattern)
      ctx = RequestContext.current
      ctx.route = pattern if ctx
      nil
    end

    # Measures the block as a span and returns its value. kind is "custom",
    # "cache", "queue", "db" or "http"; name should be low-cardinality (the
    # operation, not its arguments).
    #
    #   Visionary.span("charge card", kind: "http", target: "api.stripe.com") { stripe.charge(...) }
    def span(name, kind: "custom", target: nil, detail: nil)
      rec = telemetry
      return yield unless rec&.spans_on

      t0 = Telemetry.now_ms
      status = "ok"
      error = nil
      begin
        yield
      rescue Exception => e # re-raised; only observed here
        status = "error"
        error = "#{e.class}: #{e.message}"
        raise
      ensure
        rec.span(kind: kind, name: name, target: target, detail: detail, start_mono: t0,
                 duration_ms: Telemetry.now_ms - t0, status: status, error: error)
      end
    end

    # Records a handled error (handled = 1). Inside a request it links to it.
    #
    #   rescue PaymentError => e
    #     Visionary.capture_exception(e)
    def capture_exception(exception = $!)
      telemetry&.exception(exception, handled: true)
      nil
    end

    # Restarts the background threads in a forked child. Called
    # automatically through Process._fork; Puma's on_worker_boot may call it
    # too (it is idempotent per process).
    def after_fork
      agent = @agent
      return unless agent
      @lock = Mutex.new
      agent.after_fork
    rescue StandardError => e
      warn("visionary: restart after fork failed: #{e.message}")
    end

    # created_at format for every table: UTC, milliseconds, literal Z.
    def timestamp(time)
      time.getutc.strftime("%Y-%m-%dT%H:%M:%S.%LZ")
    end

    # Bytes as UTF-8 text, invalid sequences replaced.
    def utf8(s)
      return "" if s.nil?
      s = s.to_s
      return s if s.encoding == Encoding::UTF_8 && s.valid_encoding?
      s.dup.force_encoding(Encoding::UTF_8).scrub
    end
  end
end

require_relative "visionary/railtie" if defined?(Rails::Railtie)
