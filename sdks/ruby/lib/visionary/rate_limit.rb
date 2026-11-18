# frozen_string_literal: true

require "uri"
require_relative "rate_limit/config"
require_relative "rate_limit/limiter"
require_relative "rate_limit/poller"

module Visionary
  # Raised by Visionary.check_rate_limit! when a rule limits or blocks the
  # request. The Rack middleware (and in Rails a rescue_from the Railtie adds)
  # turns it into the response.
  class RateLimited < StandardError
    attr_reader :status, :headers, :body

    def initialize(denial)
      @status = denial.status
      @headers = RateLimit.headers(denial)
      @body = denial.body
      super("rate limited by rule #{denial.rule_id}")
    end

    def to_rack = [status, headers.dup, [body]]
  end

  module RateLimit
    def self.headers(denial)
      h = { "content-type" => denial.content_type }
      h["retry-after"] = denial.retry_after.to_s if denial.retry_after.to_i.positive?
      h
    end

    def self.response(denial) = [denial.status, headers(denial), [denial.body]]

    # The request as the limiter sees it, from a Rack env.
    def self.request(env, path, remote_ip)
      query = nil
      Request.new(
        env["REQUEST_METHOD"].to_s, path, remote_ip,
        ->(name) { header(env, name) },
        lambda do |name|
          query ||= parse_query(env["QUERY_STRING"].to_s)
          query[name].to_s
        end
      )
    end

    def self.header(env, name)
      key = name.upcase.tr("-", "_")
      key = "HTTP_#{key}" unless key == "CONTENT_TYPE" || key == "CONTENT_LENGTH"
      v = env[key]
      v.is_a?(Array) ? v.first.to_s : v.to_s
    end

    # First value of each URL-decoded parameter.
    def self.parse_query(qs)
      out = {}
      qs.split("&").each do |pair|
        next if pair.empty?
        k, v = pair.split("=", 2)
        k = decode(k)
        out[k] = decode(v.to_s) unless out.key?(k)
      end
      out
    end

    def self.decode(s)
      URI.decode_www_form_component(s)
    rescue ArgumentError
      s
    end

    # One request's rate-limit check, from the middleware to check_rate_limit!.
    class Check
      KEY = :visionary_rate_limit

      attr_reader :state

      # Runs the request phase; nil when no rule is in force.
      def self.start(limiter, env, path, remote_ip)
        return nil unless limiter&.active?
        req = RateLimit.request(env, path, remote_ip)
        new(limiter, req, limiter.check_request(req, Time.now.to_f))
      rescue StandardError
        nil
      end

      def initialize(limiter, req, state)
        @limiter = limiter
        @req = req
        @state = state
        @mutex = Mutex.new
        @user = User.new("", "")
        @done = false
        @raised = nil
      end

      def denial = @state.denial

      def set_user(id, role)
        @mutex.synchronize { @user = User.new(id.to_s, role.to_s) }
      end

      # The user phase, once; raises RateLimited when it (or an earlier call) denied.
      def check_user!(attrs)
        err = @mutex.synchronize do
          unless @done
            @done = true
            begin
              attrs = (attrs || {}).to_h { |k, v| [k.to_s, v.to_s] }
              d = @limiter.check_user(@state, @req, @user, attrs, Time.now.to_f)
              @raised = RateLimited.new(d) if d
            rescue StandardError
              @raised = nil
            end
          end
          @raised
        end
        raise err if err
      end

      if Fiber.respond_to?(:[])
        def self.current = Fiber[KEY]
        def self.current=(check)
          Fiber[KEY] = check
        end
      else
        def self.current = Thread.current[KEY]
        def self.current=(check)
          Thread.current[KEY] = check
        end
      end
    end
  end
end
