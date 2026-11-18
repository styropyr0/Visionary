# frozen_string_literal: true

require "digest"

module Visionary
  module RateLimit
    LIMITED = "limited"
    BLOCKED = "blocked"
    WOULD_LIMIT = "would_limit"
    WOULD_BLOCK = "would_block"

    USER_PROPS = %i[user role attr].freeze

    # What the limiter sees of a request. header and query are callables
    # returning a header's (lower-case name) or query parameter's first value.
    Request = Struct.new(:http_method, :path, :remote_ip, :header, :query)

    User = Struct.new(:id, :role)

    # The answer for a request that is limited or blocked.
    Denial = Struct.new(:status, :rule_id, :retry_after, :body, :content_type, keyword_init: true)

    # One rate_limit_events aggregate.
    Event = Struct.new(:created_at, :rule_id, :rule_name, :outcome, :key_value, :ip, :country, :http_method, :path,
                       :user_id, :hits, keyword_init: true)

    # Carries one request between the two phases.
    State = Struct.new(:set, :ip, :country, :allowed, :user_done, :denial)

    # A property: [kind, name], name being a lower-case header, query or attribute name.
    def self.parse_prop(s)
      s = s.to_s
      if s.start_with?("header:") && s.length > 7 then [:header, s[7..].downcase]
      elsif s.start_with?("query:") && s.length > 6 then [:query, s[6..]]
      elsif s.start_with?("attr:") && s.length > 5 then [:attr, s[5..]]
      elsif %w[ip country method path global user role].include?(s) then [s.to_sym, ""]
      end
    end

    # Matches a whole path against a pattern where * is any run of characters.
    def self.glob(pattern, str)
      pat = pattern.b
      s = str.to_s.b
      p = i = 0
      star = -1
      mark = 0
      while i < s.bytesize
        if p < pat.bytesize && pat.getbyte(p) == 42
          star = p
          mark = i
          p += 1
        elsif p < pat.bytesize && pat.getbyte(p) == s.getbyte(i)
          p += 1
          i += 1
        elsif star >= 0
          p = star + 1
          mark += 1
          i = mark
        else
          return false
        end
      end
      p += 1 while p < pat.bytesize && pat.getbyte(p) == 42
      p == pat.bytesize
    end

    def self.clip(s, n)
      s = s.to_s
      s.length > n ? s[0, n] : s
    end

    # Per-key counters of one rule.
    class CounterSet
      attr_reader :mutex, :map

      def initialize
        @mutex = Mutex.new
        @map = {}
      end

      def clear! = @map = {}
    end

    # A counter: win/curr/prev for fixed and sliding windows, tokens/last for the bucket.
    Counter = Struct.new(:win, :curr, :prev, :tokens, :last)

    class CompiledRule
      attr_accessor :id, :name, :monitor, :action, :verbs, :paths, :has_ips, :prefixes, :countries, :match, :key,
                    :limit, :window, :alg, :burst, :limit_by, :limits, :status, :body, :content_type, :user_phase,
                    :counters

      def initialize
        @verbs = []
        @paths = []
        @prefixes = []
        @countries = []
        @match = []
        @limits = {}
        @user_phase = false
        @limit = @window = @burst = 0.0
        @alg = :sliding
      end

      # What the counters depend on; a change resets them.
      def shape = [key, window, alg, burst]

      def deny(retry_after)
        st = status.to_i
        st = action == "block" ? 403 : 429 if st.zero?
        if !body.empty?
          out = body.gsub("{retry_after}", retry_after.to_s)
          ct = body.lstrip.start_with?("{", "[") ? "application/json" : "text/plain; charset=utf-8"
        elsif action == "block"
          out = %({"error":{"code":"blocked","key":"#{id}","message":"Forbidden"}})
          ct = "application/json"
        else
          out = %({"error":{"code":"rate_limited","key":"#{id}","message":"Too many requests"}})
          ct = "application/json"
        end
        ct = content_type unless content_type.empty?
        Denial.new(status: st, rule_id: id, retry_after: retry_after, body: out, content_type: ct)
      end

      def capacity(lim) = burst.positive? ? burst : lim

      # Takes one request on key's counter: [retry_after, over].
      def hit(k, lim, t)
        w_s = window
        return [w_s.ceil, true] if lim.zero?

        counters.mutex.synchronize do
          c = counters.map[k]
          unless c
            if counters.map.size >= MAX_COUNTERS
              sweep_locked(t)
              counters.clear! if counters.map.size >= MAX_COUNTERS
            end
            c = Counter.new(nil, 0.0, 0.0, capacity(lim), t)
            counters.map[k] = c
          end
          if alg == :token_bucket
            cap = capacity(lim)
            rate = lim / w_s
            if t > c.last
              c.tokens = [cap, c.tokens + ((t - c.last) * rate)].min
              c.last = t
            end
            return [[1, ((1 - c.tokens) / rate).ceil].max, true] if c.tokens < 1
            c.tokens -= 1
            return [0, false]
          end
          w = (t / w_s).floor
          if c.win.nil?
            c.prev = c.curr = 0.0
          elsif w == c.win + 1
            c.prev = c.curr
            c.curr = 0.0
          elsif w > c.win + 1
            c.prev = c.curr = 0.0
          end
          c.win = c.win.nil? ? w : [c.win, w].max
          over = if alg == :fixed
                   c.curr >= lim
                 else
                   e = t - (w * w_s)
                   (c.prev * (w_s - e) / w_s) + c.curr + 1 > lim
                 end
          return [[1, (((w + 1) * w_s) - t).ceil].max, true] if over
          c.curr += 1
          [0, false]
        end
      end

      # Drops idle counters; the caller holds the set's mutex.
      def sweep_locked(t)
        w = (t / window).floor
        counters.map.delete_if do |_, c|
          if alg == :token_bucket
            # the limit may differ per key; the static one is close enough to call it idle
            limit <= 0 || c.tokens + ((t - c.last) * (limit / window)) >= capacity(limit)
          else
            c.win < w - 1
          end
        end
      end
    end

    RuleSet = Struct.new(:ip_header, :country_headers, :request, :user, :by_id)

    # Evaluates the rules in force and keeps the counters. Thread-safe.
    class Limiter
      DEFAULT_REDACT = %w[Authorization Proxy-Authorization Cookie Set-Cookie X-Api-Key X-Auth-Token].freeze

      # Sees every recorded outcome as (rule_id, outcome); for tests.
      attr_accessor :trace

      def initialize(service = nil, redact_headers: [])
        @service = service.to_s
        @set = nil
        @shapes = {}
        @load_mutex = Mutex.new
        @agg_mutex = Mutex.new
        @agg = {}
        @redact = (DEFAULT_REDACT + Array(redact_headers)).to_h { |h| [h.to_s.strip.downcase, true] }
      end

      # Puts a parsed config in force, keeping the counters of rules whose
      # key, window, algorithm and burst did not change.
      def load(config)
        @load_mutex.synchronize do
          nxt = compile(config)
          old = @set
          shapes = {}
          nxt.by_id.each do |id, r|
            shapes[id] = r.shape
            r.counters = if old && @shapes[id] == shapes[id] && old.by_id[id]
                           old.by_id[id].counters
                         else
                           CounterSet.new
                         end
          end
          @shapes = shapes
          @set = nxt
        end
      end

      def clear = load(Config.new(rules: []))

      def active?
        s = @set
        !s.nil? && !s.by_id.empty?
      end

      # The request phase; the State goes to check_user later.
      def check_request(req, now)
        set = @set
        st = State.new(nil, "", "", false, false, nil)
        return st if set.nil? || set.by_id.empty?

        st.set = set
        st.ip = client_ip(req, set.ip_header)
        st.country = country(req, set.country_headers)
        run(set.request, st, req, nil, {}, now)
        st
      rescue StandardError
        State.new(nil, "", "", false, false, nil) # fail open
      end

      # The user phase, once per request; later calls repeat the first outcome.
      def check_user(st, req, user, attrs, now)
        return nil if st.nil? || st.set.nil?
        return st.denial if st.user_done || st.allowed || st.denial

        st.user_done = true
        return nil if st.set.user.empty?

        begin
          run(st.set.user, st, req, user || User.new("", ""), attrs || {}, now)
        rescue StandardError
          st.denial = nil
        end
        st.denial
      end

      # Drops idle counters of every rule; called at each poll.
      def sweep(now)
        set = @set
        return unless set
        set.by_id.each_value do |r|
          next unless r.counters && r.action == "limit"
          r.counters.mutex.synchronize { r.sweep_locked(now.to_f) }
        end
      end

      # The outcomes recorded since the last call, oldest first.
      def drain
        @agg_mutex.synchronize do
          out = @agg.values
          @agg = {}
          out
        end
      end

      private

      def compile(c)
        headers = Array(c.country_headers).map { |h| h.to_s.strip }.reject(&:empty?)
        headers = DEFAULT_COUNTRY_HEADERS if headers.empty?
        rs = RuleSet.new(c.ip_header.to_s.strip, headers, [], [], {})
        return rs unless c.on?

        Array(c.rules).each do |src|
          r = compile_rule(src)
          next if r.nil? || !src.on? || rs.by_id[r.id] || !serves?(Array(src.services))
          rs.by_id[r.id] = r
          (r.user_phase ? rs.user : rs.request) << r
        end
        rs
      end

      def serves?(list)
        return true if list.empty?
        return false if @service.empty?
        list.include?(@service)
      end

      def compile_rule(src)
        return nil unless RateLimit.valid_id?(src.id)
        r = CompiledRule.new
        r.id = src.id
        r.name = src.name.to_s.empty? ? src.id : src.name
        r.action = src.action.to_s
        r.status = src.status.to_i
        r.body = src.body.to_s
        r.content_type = src.content_type.to_s
        case src.mode.to_s
        when "", "enforce" then r.monitor = false
        when "monitor" then r.monitor = true
        else return nil
        end
        return nil unless %w[limit block allow].include?(r.action)

        r.verbs = Array(src.http_methods).map { |m| m.strip.upcase }
        r.paths = Array(src.paths)
        r.has_ips = !Array(src.ips).empty?
        r.prefixes = Array(src.ips).filter_map { |s| RateLimit.parse_prefix(s) }
        r.countries = Array(src.countries).map { |c| c.strip.upcase }
        (src.match || {}).each do |name, values|
          p = RateLimit.parse_prop(name)
          return nil if p.nil? || p[0] == :global
          r.match << [p, values]
          r.user_phase ||= USER_PROPS.include?(p[0])
        end
        return r unless r.action == "limit"

        key = RateLimit.parse_prop(src.key.to_s.empty? ? "ip" : src.key)
        return nil unless key
        r.key = key
        r.user_phase ||= USER_PROPS.include?(key[0])
        w = src.window_s.to_f
        return nil if w < 1 || w > MAX_WINDOW
        r.window = w
        r.limit = src.limit.to_f
        r.burst = src.burst.to_f
        r.alg = case src.algorithm.to_s
                when "", "sliding" then :sliding
                when "fixed" then :fixed
                when "token_bucket" then :token_bucket
                else return nil
                end
        unless src.limit_by.to_s.empty?
          p = RateLimit.parse_prop(src.limit_by)
          return nil if p.nil? || p[0] == :global
          r.limit_by = p
          r.limits = src.limits || {}
          r.user_phase ||= USER_PROPS.include?(p[0])
        end
        r
      end

      def run(rules, st, req, user, attrs, now)
        t = now.to_f
        rules.each do |r|
          next unless matches?(r, st, req, user, attrs)
          case r.action
          when "allow"
            next if r.monitor
            st.allowed = true
            return
          when "block"
            if r.monitor
              record(r, WOULD_BLOCK, "", st, req, user, t)
              next
            end
            record(r, BLOCKED, "", st, req, user, t)
            st.denial = r.deny(0)
            return
          when "limit"
            k = value(r.key, st, req, user, attrs)
            next if k.empty?
            lim = r.limit
            if r.limit_by
              n = r.limits[value(r.limit_by, st, req, user, attrs)]
              lim = n unless n.nil?
            end
            next if lim.negative?
            retry_after, over = r.hit(k, lim, t)
            next unless over
            if r.monitor
              record(r, WOULD_LIMIT, k, st, req, user, t)
              next
            end
            record(r, LIMITED, k, st, req, user, t)
            st.denial = r.deny(retry_after)
            return
          end
        end
      end

      def matches?(r, st, req, user, attrs)
        return false if !r.verbs.empty? && !r.verbs.include?(req.http_method.to_s.upcase)
        return false if !r.paths.empty? && r.paths.none? { |g| RateLimit.glob(g, req.path) }
        return false if r.has_ips && !in_prefixes?(r.prefixes, st.ip)
        return false if !r.countries.empty? && (st.country.empty? || !r.countries.include?(st.country))

        r.match.all? do |p, values|
          v = value(p, st, req, user, attrs)
          values.empty? ? !v.empty? : values.include?(v)
        end
      end

      def value(prop, st, req, user, attrs)
        kind, name = prop
        v = case kind
            when :ip then st.ip
            when :country then st.country
            when :method then req.http_method.to_s.upcase
            when :path then req.path
            when :header then req.header&.call(name)
            when :query then req.query&.call(name)
            when :global then "*"
            when :user then user&.id
            when :role then user&.role
            when :attr then attrs[name]
            end
        v.to_s
      end

      def record(r, outcome, key, st, req, user, t)
        @trace&.call(r.id, outcome)
        if !key.empty? && r.key[0] == :header && @redact[r.key[1]]
          key = "sha256:#{Digest::SHA256.hexdigest(key)[0, 12]}"
        end
        key = RateLimit.clip(key, 200)
        ip = st.ip
        @agg_mutex.synchronize do
          id = [r.id, outcome, key, ip]
          e = @agg[id]
          if e.nil? && @agg.size >= MAX_AGGREGATES
            key = "(other)"
            ip = ""
            id = [r.id, outcome, key, ip]
            e = @agg[id]
          end
          unless e
            e = Event.new(created_at: Time.at(t), rule_id: r.id, rule_name: r.name, outcome: outcome, key_value: key,
                          ip: ip, country: st.country, http_method: req.http_method.to_s.upcase,
                          path: RateLimit.clip(req.path, 300), user_id: user ? user.id.to_s : "", hits: 0)
            @agg[id] = e
          end
          e.hits += 1
        end
      end

      def client_ip(req, header)
        if !header.empty? && req.header
          v = req.header.call(header.downcase).to_s
          first = v.split(",", 2).first.to_s.strip
          return first unless first.empty?
        end
        req.remote_ip.to_s
      end

      def country(req, headers)
        return "" unless req.header
        headers.each do |h|
          v = req.header.call(h.downcase).to_s.strip
          return v.upcase unless v.empty?
        end
        ""
      end

      def in_prefixes?(list, ip)
        addr = RateLimit.parse_addr(ip)
        return false unless addr
        list.any? { |p| p.family == addr.family && p.include?(addr) }
      end
    end
  end
end
