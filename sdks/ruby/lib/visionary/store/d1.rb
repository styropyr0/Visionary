# frozen_string_literal: true

require "json"
require "net/http"
require "uri"

module Visionary
  module Store
    # Cloudflare D1 over its REST API. Every param travels as a string; the
    # inserts wrap placeholders in NULLIF(?, '') so "" means NULL.
    class D1
      DEFAULT_API_BASE = "https://api.cloudflare.com/client/v4"
      MAX_BATCH = 50
      RESPONSE_LIMIT = 1 << 20

      class Error < StandardError; end

      def initialize(account_id:, database_id:, api_token:, api_base: "", timeout: 20)
        base = (api_base.to_s.strip.empty? ? DEFAULT_API_BASE : api_base.strip).chomp("/")
        db = "#{base}/accounts/#{account_id.strip}/d1/database/#{database_id.strip}"
        @query_uri = URI("#{db}/query")
        @raw_uri = URI("#{db}/raw")
        @token = api_token.strip
        @timeout = timeout
        @mutex = Mutex.new
      end

      def dialect = "d1"
      def string_params? = true

      def exec_script(statements)
        statements.each_slice(MAX_BATCH) { |chunk| post(@query_uri, { batch: chunk.map { |s| { sql: s, params: [] } } }) }
      end

      def exec(sql)
        post(@query_uri, { sql: sql })
      end

      def column_names(sql)
        result = post(@raw_uri, { sql: sql })
        rows = result.is_a?(Array) && result.dig(0, "results", "rows")
        Array(rows).map { |r| r.is_a?(Array) ? r[0].to_s : nil }.compact
      end

      # Every row of a query, as arrays of values.
      def rows(sql)
        result = post(@raw_uri, { sql: sql })
        rows = result.is_a?(Array) && result.dig(0, "results", "rows")
        Array(rows).select { |r| r.is_a?(Array) }
      end

      def batch(stmts)
        stmts.each_slice(MAX_BATCH) do |chunk|
          post(@query_uri, { batch: chunk.map { |sql, args| { sql: sql, params: args.map { |a| self.class.param(a) } } } })
        end
      end

      def changes(sql, args)
        result = post(@query_uri, { sql: sql, params: args.map { |a| self.class.param(a) } })
        Integer(result.dig(0, "meta", "changes") || 0)
      rescue TypeError, ArgumentError
        0
      end

      def after_fork
        @http = nil
        @mutex = Mutex.new
      end

      def close
        @mutex.synchronize { finish }
      end

      def self.param(v)
        case v
        when nil then ""
        when true then "1"
        when false then "0"
        when Float
          s = v.to_s
          s.include?("e") ? format("%.6f", v) : s
        else v.to_s
        end
      end

      private

      def post(uri, body)
        @mutex.synchronize do
          req = Net::HTTP::Post.new(uri)
          req["Authorization"] = "Bearer #{@token}"
          req["Content-Type"] = "application/json"
          req.body = JSON.generate(body)
          res = begin
            connection(uri).request(req)
          rescue StandardError => e
            finish
            raise Error, "d1: request: #{e.message}"
          end
          raw = res.body.to_s
          raise Error, "d1: response larger than #{RESPONSE_LIMIT} bytes" if raw.bytesize > RESPONSE_LIMIT
          env = begin
            JSON.parse(raw)
          rescue JSON::ParserError
            raise Error, "d1: status #{res.code}: unreadable response"
          end
          if !env.is_a?(Hash) || env["success"] != true || res.code.to_i >= 300
            msgs = (env.is_a?(Hash) ? Array(env["errors"]) : []).map { |e| e.is_a?(Hash) ? e["message"].to_s : nil }.compact
            raise Error, "d1: status #{res.code}: #{msgs.empty? ? 'unknown D1 error' : msgs.join('; ')}"
          end
          env["result"]
        end
      end

      # One keep-alive connection, reopened after errors.
      def connection(uri)
        return @http if @http&.started?
        http = Net::HTTP.new(uri.host, uri.port)
        http.use_ssl = uri.scheme == "https"
        http.open_timeout = @timeout
        http.read_timeout = @timeout
        http.write_timeout = @timeout if http.respond_to?(:write_timeout=)
        http.keep_alive_timeout = 30
        @http = http.start
      end

      def finish
        @http&.finish if @http&.started?
      rescue StandardError
        nil
      ensure
        @http = nil
      end
    end
  end
end
