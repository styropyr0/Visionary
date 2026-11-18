# frozen_string_literal: true

require "json"
require "ipaddr"

module Visionary
  # Rate-limit rules made in the dashboard (Security → Rate limits), enforced
  # in the middleware. A port of sdks/go/ratelimit; see "Rate limits" in
  # sdks/SPEC.md.
  module RateLimit
    SETTING_VERSION = "ratelimit.version"
    SETTING_CONFIG = "ratelimit.config"

    MAX_WINDOW = 86_400
    MAX_COUNTERS = 10_000
    MAX_AGGREGATES = 1000

    DEFAULT_COUNTRY_HEADERS = %w[
      CF-IPCountry CloudFront-Viewer-Country X-Vercel-IP-Country Fastly-Geo-Country X-AppEngine-Country X-Country-Code
    ].freeze

    ID_PATTERN = /\A[A-Za-z0-9_-]{1,32}\z/

    Config = Struct.new(:enabled, :ip_header, :country_headers, :rules, keyword_init: true) do
      def on? = enabled != false
    end

    # One rule as stored; nil and zero values mean the field is missing.
    Rule = Struct.new(:id, :name, :enabled, :mode, :action, :services, :http_methods, :paths, :ips, :countries, :match,
                      :key, :limit, :window_s, :algorithm, :burst, :limit_by, :limits, :status, :body, :content_type,
                      keyword_init: true) do
      def on? = enabled != false
    end

    STRING_FIELDS = %w[id name mode action key algorithm limit_by body content_type].freeze
    LIST_FIELDS = %w[services methods paths ips countries].freeze
    NUMBER_FIELDS = %w[limit window_s burst].freeze

    class << self
      # Reads a stored document leniently: a rule with a field of the wrong
      # type is dropped, the rest are kept. Only invalid JSON raises.
      def parse(raw)
        doc = JSON.parse(raw.to_s)
        return Config.new(rules: []) unless doc.is_a?(Hash)

        enabled = doc["enabled"]
        headers = doc["country_headers"]
        rules = doc["rules"].is_a?(Array) ? doc["rules"].filter_map { |r| decode_rule(r) } : []
        Config.new(enabled: [true, false].include?(enabled) ? enabled : nil,
                   ip_header: doc["ip_header"].is_a?(String) ? doc["ip_header"] : "",
                   country_headers: string_list?(headers) ? Array(headers) : [], rules: rules)
      end

      def valid_id?(id) = id.is_a?(String) && ID_PATTERN.match?(id)

      # [IPAddr, ...] entry for an IP or CIDR, IPv4-mapped IPv6 as IPv4; nil when it does not parse.
      def parse_prefix(s)
        s = s.to_s.strip
        if s.include?("/")
          return nil unless s.match?(%r{\A[0-9A-Fa-f:.]+/\d{1,3}\z})
          addr = IPAddr.new(s)
          bits = addr.prefix
          addr = IPAddr.new("#{addr.native}/#{bits - 96}") if addr.ipv4_mapped? && bits >= 96
          addr
        else
          parse_addr(s)
        end
      rescue IPAddr::Error, ArgumentError
        nil
      end

      def parse_addr(s)
        s = s.to_s.strip.sub(/%.*\z/, "")
        return nil unless s.match?(/\A[0-9A-Fa-f:.]+\z/)
        addr = IPAddr.new(s)
        addr.ipv4_mapped? ? IPAddr.new(addr.native.to_s) : addr
      rescue IPAddr::Error, ArgumentError
        nil
      end

      private

      def string_list?(v) = v.nil? || (v.is_a?(Array) && v.all?(String))

      def decode_rule(r)
        return nil unless r.nil? || r.is_a?(Hash)
        r ||= {}
        out = {}
        STRING_FIELDS.each do |f|
          v = r[f]
          return nil unless v.nil? || v.is_a?(String)
          out[f.to_sym] = v.to_s
        end
        LIST_FIELDS.each do |f|
          return nil unless string_list?(r[f])
          out[f == "methods" ? :http_methods : f.to_sym] = Array(r[f])
        end
        NUMBER_FIELDS.each do |f|
          v = r[f]
          return nil unless v.nil? || v.is_a?(Numeric)
          out[f.to_sym] = v.to_f
        end
        return nil unless [nil, true, false].include?(r["enabled"])
        out[:enabled] = r["enabled"]
        return nil unless r["status"].nil? || r["status"].is_a?(Integer)
        out[:status] = r["status"].to_i
        match = r["match"]
        return nil unless match.nil? || (match.is_a?(Hash) && match.values.all? { |v| string_list?(v) })
        out[:match] = (match || {}).transform_values { |v| Array(v) }
        limits = r["limits"]
        return nil unless limits.nil? || (limits.is_a?(Hash) && limits.values.all?(Numeric))
        out[:limits] = (limits || {}).transform_values(&:to_f)
        Rule.new(**out)
      end
    end
  end
end
