# frozen_string_literal: true

require "json"

module Visionary
  # Scrubs credentials from headers and bodies before anything is stored.
  # The body patterns are regexes on purpose, so bodies cut off by the
  # capture cap (invalid JSON) are still scrubbed.
  class Redactor
    REDACTED = "[REDACTED]"
    DEFAULT_HEADERS = %w[Authorization Proxy-Authorization Cookie Set-Cookie X-Api-Key X-Auth-Token].freeze
    DEFAULT_FIELDS = %w[
      password passwd secret client_secret token access_token refresh_token id_token
      api_key apikey otp pin card_number cvv private_key
    ].freeze

    def initialize(extra_headers = [], extra_fields = [], request_only = [])
      @headers = (DEFAULT_HEADERS + extra_headers).map { |h| h.strip.downcase }.reject(&:empty?).to_h { |h| [h, true] }
      both = key_pattern(DEFAULT_FIELDS + extra_fields)
      req_only = key_pattern(request_only)
      req_keys = req_only.empty? ? both : "#{both}|#{req_only}"
      @resp_json = json_secrets(both)
      @req_json = json_secrets(req_keys)
      @form = /(^|&)(#{req_keys})=[^&]*/i
    end

    # Headers is [[name, value], ...]; the result is a JSON object.
    def headers(pairs)
      return "" if pairs.empty?
      out = {}
      pairs.each { |name, value| out[Visionary.utf8(name)] = @headers[name.downcase] ? REDACTED : Visionary.utf8(value) }
      JSON.generate(out.sort.to_h)
    rescue JSON::GeneratorError, EncodingError
      ""
    end

    def request_body(body, content_type)
      return "" if body.empty?
      if content_type.to_s.include?("application/x-www-form-urlencoded")
        body.gsub(@form) { "#{Regexp.last_match(1)}#{Regexp.last_match(2)}=#{REDACTED}" }
      else
        body.gsub(@req_json) { "#{Regexp.last_match(1)}\"#{REDACTED}\"" }
      end
    end

    def response_body(body)
      return "" if body.empty?
      body.gsub(@resp_json) { "#{Regexp.last_match(1)}\"#{REDACTED}\"" }
    end

    # The error code and key from a JSON error response at dotted paths.
    # Bodies that aren't valid JSON (e.g. truncated) fall back to a pattern
    # match on the path's last segment.
    def self.error_fields(body, code_path, key_path)
      v = begin
        JSON.parse(body)
      rescue JSON::ParserError, EncodingError
        return [match_field(body, code_path), match_field(body, key_path)]
      end
      [lookup(v, code_path), lookup(v, key_path)]
    end

    def self.lookup(v, path)
      return "" if path.to_s.empty?
      path.split(".").each do |seg|
        return "" unless v.is_a?(Hash)
        v = v[seg]
      end
      case v
      when String then v
      when Integer then v.to_s
      when Float then v.finite? && v == v.floor && v.abs < 1e15 ? v.to_i.to_s : v.to_s
      else ""
      end
    end

    def self.match_field(body, path)
      return "" if path.to_s.empty?
      last = path.split(".").last
      m = body.match(/"#{Regexp.escape(last)}"\s*:\s*"([^"]*)"/)
      m ? m[1] : ""
    end

    private

    def key_pattern(names)
      names.map(&:strip).reject(&:empty?).map { |n| Regexp.escape(n) }.join("|")
    end

    def json_secrets(keys)
      /("(?:#{keys})"\s*:\s*)("(?:[^"\\]|\\.)*"?|[^,}\]\s]+)/i
    end
  end
end
