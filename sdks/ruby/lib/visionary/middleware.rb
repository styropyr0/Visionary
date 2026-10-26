# frozen_string_literal: true

require "securerandom"

module Visionary
  # Rack middleware: logs each request to api_logs, counts throughput for
  # system_metrics, collects the request's spans and records exceptions.
  # Mount it outermost so it sees the final status. Works with any Rack app
  # (Rails via the Railtie, Sinatra, Hanami, Roda, Grape).
  #
  #   use Visionary::Middleware
  class Middleware
    REQUEST_ID = "HTTP_X_REQUEST_ID"
    # Where frameworks leave an exception they turned into an error page.
    EXCEPTION_KEYS = %w[visionary.exception action_dispatch.exception sinatra.error rack.exception].freeze
    UNCAPTURABLE = %w[multipart/ image/ video/ audio/ application/octet-stream application/zip application/pdf].freeze

    def initialize(app, agent = nil)
      @app = app
      @agent = agent
    end

    def call(env)
      agent = @agent || Visionary.agent
      return @app.call(env) unless agent&.enabled?

      path = "#{env['SCRIPT_NAME']}#{env['PATH_INFO']}"
      path = "/" if path.empty?
      return passthrough(agent, env) unless agent.log_path?(path)

      log(agent, env, path)
    end

    def self.capturable?(content_type)
      ct = content_type.to_s.downcase
      ct.empty? || UNCAPTURABLE.none? { |p| ct.start_with?(p) }
    end

    # Rack env headers as [[name, value], ...], e.g. ["User-Agent", "curl/8"].
    def self.header_pairs(env)
      out = []
      env.each do |k, v|
        next unless k.is_a?(String)
        if k.start_with?("HTTP_") && k != "HTTP_VERSION"
          out << [k[5..].split("_").map(&:capitalize).join("-"), v.to_s]
        elsif k == "CONTENT_TYPE" || k == "CONTENT_LENGTH"
          out << [k.split("_").map(&:capitalize).join("-"), v.to_s] unless v.to_s.empty?
        end
      end
      out
    end

    # A response header by lowercase name, whatever case the app used.
    def self.header(headers, name)
      return nil unless headers.respond_to?(:each)
      v = headers[name]
      if v.nil?
        headers.each do |k, val|
          next unless k.to_s.casecmp?(name)
          v = val
          break
        end
      end
      v.is_a?(Array) ? v.join(", ") : v
    rescue StandardError
      nil
    end

    private

    # Not logged: count it, and still record what escapes.
    def passthrough(agent, env)
      agent.http.started
      Profiler::Tally.started!
      t0 = Telemetry.now_ms
      status = 500
      begin
        res = @app.call(env)
        status = res[0].to_i
        res
      rescue Exception => e # re-raised below; the app's handling is unchanged
        agent.telemetry&.exception(e, handled: false, ctx: nil)
        raise
      ensure
        agent.http.finished(status)
        if Profiler::Tally.busy?
          route = agent.route_for(env)
          route = "#{env['SCRIPT_NAME']}#{env['PATH_INFO']}" if route.empty?
          Profiler::Tally.finished(env["REQUEST_METHOD"], route, Telemetry.now_ms - t0)
        end
      end
    end

    def log(agent, env, path)
      cfg = agent.config
      started_at = Time.now
      t0 = Telemetry.now_ms
      request_id = env[REQUEST_ID].to_s
      request_id = SecureRandom.hex(16) if request_id.empty? || request_id.length > 128
      headers = self.class.header_pairs(env)
      env[REQUEST_ID] = request_id # so the app (Rails' RequestId) uses the same id

      req_type = env["CONTENT_TYPE"].to_s
      req_capture = Capture.new(cfg.max_body_bytes, cfg.capture_bodies && self.class.capturable?(req_type))
      env["rack.input"] = InputProxy.new(env["rack.input"], req_capture) if env["rack.input"] && cfg.capture_bodies

      ctx = RequestContext.new(
        request_id: request_id, method: env["REQUEST_METHOD"].to_s, path: path, started_at: started_at,
        started_mono: t0, span_max: cfg.span_max.to_i, db_visible: Telemetry::ActiveRecordHook.installed?
      )
      agent.http.started
      Profiler::Tally.started!
      outer = RequestContext.current
      RequestContext.current = ctx
      begin
        status, res_headers, body = @app.call(env)
      rescue Exception => e # re-raised below; the app's handling is unchanged
        agent.telemetry&.exception(e, handled: false, ctx: ctx)
        finish(agent, env, ctx, headers, req_capture, 500, nil, Capture.new(0, false), t0)
        raise
      ensure
        RequestContext.current = outer
      end

      status = status.to_i
      begin
        res_headers["x-request-id"] = request_id unless self.class.header(res_headers, "x-request-id")
      rescue StandardError
        nil
      end
      if status >= 500 && (exc = EXCEPTION_KEYS.lazy.map { |k| env[k] }.find { |x| x.is_a?(Exception) })
        agent.telemetry&.exception(exc, handled: false, ctx: ctx)
      end
      resp_type = self.class.header(res_headers, "content-type").to_s
      resp_capture = Capture.new(cfg.max_body_bytes, cfg.capture_bodies && self.class.capturable?(resp_type))
      proxy = BodyProxy.new(body, resp_capture) do
        finish(agent, env, ctx, headers, req_capture, status, res_headers, resp_capture, t0)
      end
      [status, res_headers, proxy]
    end

    def finish(agent, env, ctx, headers, req_capture, status, res_headers, resp_capture, t0)
      agent.http.finished(status)
      ctx.logged!
      duration = (Telemetry.now_ms - t0).round(3)
      route = ctx.route
      route = agent.route_for(env) if route.empty?
      Profiler::Tally.finished(ctx.method, route.empty? ? ctx.path : route, duration)
      user_id, handle, role = ctx.user
      tel = agent.telemetry
      entry = Entry.new(
        request_id: ctx.request_id, started_at: ctx.started_at, method: ctx.method, path: ctx.path,
        route: route, query: env["QUERY_STRING"].to_s, status: status, duration_ms: duration,
        ip: client_ip(env), user_agent: env["HTTP_USER_AGENT"].to_s,
        user_id: user_id, user_handle: handle, user_role: role, login_account: ctx.login_account, headers: headers,
        request_content_type: env["CONTENT_TYPE"].to_s, request_body: req_capture.bytes,
        request_truncated: req_capture.truncated?,
        request_size: [env["CONTENT_LENGTH"].to_i, req_capture.size].max,
        response_content_type: self.class.header(res_headers, "content-type").to_s,
        response_body: resp_capture.bytes, response_truncated: resp_capture.truncated?,
        response_size: [self.class.header(res_headers, "content-length").to_i, resp_capture.size].max,
        totals: tel&.spans_on ? ctx.totals : [nil, nil, nil, nil],
        span_rows: tel ? tel.request_span_rows(ctx) : [],
        error_rows: tel ? ctx.errors.map { |row| tel.error_row(row, ctx, route, user_id) } : []
      )
      agent.enqueue([:request, entry])
    rescue StandardError => e
      agent.logger.error("visionary: dropping request log entry: #{e.class}: #{e.message}")
    end

    # The first X-Forwarded-For hop (most deployments sit behind a proxy),
    # else the peer. Informational only.
    def client_ip(env)
      fwd = env["HTTP_X_FORWARDED_FOR"].to_s
      return fwd.split(",", 2).first.strip unless fwd.empty?
      env["REMOTE_ADDR"].to_s
    end
  end
end
