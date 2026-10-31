# frozen_string_literal: true

require "json"

module Visionary
  # Settings → Data: requests.skip in visionary_settings, a JSON array of
  # exact paths or prefixes ending in "*". A skipped request is logged like
  # one outside the path prefix. One thread reads it at start and every 30 s;
  # a failed read (no server or table yet) keeps the last list.
  class SkipList
    POLL_S = 30
    SETTING = "requests.skip"
    SETTINGS_SQL = "SELECT name, value FROM visionary_settings WHERE name LIKE 'requests.%'"

    # Anything but a JSON array of strings skips nothing.
    def self.parse(value)
      list = JSON.parse(value.to_s)
      return [] unless list.is_a?(Array) && list.all?(String)
      list.map(&:strip).reject(&:empty?)
    rescue JSON::ParserError
      []
    end

    def self.match?(list, path)
      list.any? { |p| p.end_with?("*") ? path.start_with?(p.chomp("*")) : p == path }
    end

    def initialize(store, logger)
      @store = store
      @logger = logger
      @list = [].freeze
      reset
    end

    def skip?(path) = SkipList.match?(@list, path)

    def poll
      list = []
      @store.rows(SETTINGS_SQL).each { |name, value| list = SkipList.parse(value) if name.to_s == SETTING }
      @list = list.freeze
    rescue StandardError
      nil
    end

    def start
      @thread = Thread.new { run }
      @thread.name = "visionary-skip" if @thread.respond_to?(:name=)
      self
    end

    def after_fork
      reset
      start
    end

    def close
      @mutex.synchronize do
        @stopped = true
        @cond.broadcast
      end
    end

    private

    def reset
      @mutex = Mutex.new
      @cond = ConditionVariable.new
      @stopped = false
      @thread = nil
    end

    def run
      Telemetry.internal!
      loop do
        poll
        stopped = @mutex.synchronize do
          @cond.wait(@mutex, POLL_S) unless @stopped
          @stopped
        end
        break if stopped
      end
    rescue StandardError => e
      @logger.error("visionary: skip list stopped: #{e.message}")
    end
  end
end
