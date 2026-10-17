# frozen_string_literal: true

module Visionary
  module Telemetry
    # Database spans from ActiveRecord's sql.active_record notifications.
    module ActiveRecordHook
      SKIP = %w[SCHEMA CACHE].freeze
      SYSTEMS = { "postg" => "postgresql", "mysql" => "mysql", "trilogy" => "mysql", "sqlite" => "sqlite" }.freeze

      @installed = false
      @targets = {}
      @mutex = Mutex.new

      class << self
        def installed? = @installed

        def install
          return if @installed || !defined?(::ActiveSupport::Notifications)
          @installed = true
          ::ActiveSupport::Notifications.subscribe("sql.active_record") do |_name, start, finish, _id, payload|
            record(start, finish, payload)
          end
        end

        def record(start, finish, payload)
          rec = Visionary.telemetry
          return unless rec&.spans_on
          return if payload[:cached] || SKIP.include?(payload[:name])
          return if Telemetry.internal?

          duration = (finish - start).to_f * 1000.0
          sql = payload[:sql].to_s
          exc = payload[:exception]
          rec.span(
            kind: "db", name: SQLText.name(sql), target: target(payload[:connection]),
            detail: SQLText.sanitize(sql), start_mono: Telemetry.now_ms - duration, duration_ms: duration,
            status: exc ? "error" : "ok", error: exc && "#{exc[0]}: #{exc[1]}",
            row_count: payload[:row_count].is_a?(Integer) ? payload[:row_count] : nil
          )
        rescue StandardError
          nil
        end

        # "postgresql/voygram", cached per pool.
        def target(conn)
          return nil unless conn.respond_to?(:pool)
          pool = conn.pool
          @mutex.synchronize do
            @targets.fetch(pool.object_id) do
              @targets[pool.object_id] = describe(pool, conn)
            end
          end
        rescue StandardError
          nil
        end

        def describe(pool, conn)
          cfg = pool.respond_to?(:db_config) ? pool.db_config : nil
          adapter = (cfg ? cfg.adapter : conn.adapter_name).to_s.downcase
          system = SYSTEMS.find { |k, _| adapter.include?(k) }&.last || adapter
          db = cfg&.database.to_s
          db = File.basename(db, ".*") if system == "sqlite"
          db.empty? ? system : "#{system}/#{db}"
        end
      end
    end

    # Outbound HTTP spans from Net::HTTP (and the many clients built on it).
    module NetHTTPHook
      def request(req, body = nil, &block)
        rec = Visionary.telemetry
        # Net::HTTP#request calls itself again once the connection is up;
        # that inner call is the one measured.
        return super unless rec&.spans_on && started? && !Telemetry.internal?

        t0 = Telemetry.now_ms
        status = "ok"
        error = nil
        begin
          res = super
          status = "error" if res.code.to_i >= 500
          res
        rescue Exception => e # re-raised; the caller sees the same failure
          status = "error"
          error = "#{e.class}: #{e.message}"
          raise
        ensure
          Telemetry::NetHTTPHook.record(rec, self, req, t0, status, error)
        end
      end

      def self.record(rec, http, req, t0, status, error)
        host = http.address.to_s
        scheme = http.use_ssl? ? "https" : "http"
        port = http.port == (http.use_ssl? ? 443 : 80) ? "" : ":#{http.port}"
        path = req.path.to_s.split("?", 2).first
        path = URI(path).path if path.start_with?("http://", "https://")
        rec.span(
          kind: "http", name: "#{req.method} #{host}", target: host,
          detail: "#{req.method} #{scheme}://#{host}#{port}#{path}",
          start_mono: t0, duration_ms: Telemetry.now_ms - t0, status: status, error: error
        )
      rescue StandardError
        nil
      end

      def self.install
        return if @installed
        require "net/http"
        Net::HTTP.prepend(self)
        @installed = true
      end
    end
  end
end
