# frozen_string_literal: true

require "uri"

module Visionary
  module Store
    class ConfigError < StandardError; end

    # A connection the SDK opens itself from VISIONARY_DATABASE_URL, through
    # the pg, mysql2 or sqlite3 gem (whichever the app has installed). One
    # connection, used by one background thread at a time, reopened after an
    # error.
    class SQL
      attr_reader :dialect

      def initialize(dialect, &connect)
        @dialect = dialect
        @connect = connect
        @mutex = Mutex.new
        @conn = nil
      end

      def string_params? = false

      def exec_script(statements)
        statements.each do |s|
          run { |c| c.execute(s, []) }
        rescue StandardError => e
          next if dialect == "mysql" && Store.duplicate_index?(e)
          raise
        end
      end

      def exec(sql)
        run { |c| c.execute(sql, []) }
      end

      def column_names(sql)
        run { |c| c.column(sql) }
      end

      # Every row of a query, as arrays of values.
      def rows(sql)
        run { |c| c.rows(sql) }
      end

      # One transaction for the whole batch.
      def batch(stmts)
        run do |c|
          c.execute("BEGIN", [])
          begin
            stmts.each { |sql, args| c.execute(sql, args) }
            c.execute("COMMIT", [])
          rescue StandardError
            begin
              c.execute("ROLLBACK", [])
            rescue StandardError
              nil
            end
            raise
          end
        end
      end

      def changes(sql, args)
        run { |c| c.execute(sql, args) }
      end

      # The parent's connection (a socket) must not be used or closed from
      # the child: it is kept referenced (so no finalizer closes it) and a new
      # one is opened lazily.
      def after_fork
        @mutex = Mutex.new
        (@inherited ||= []) << @conn if @conn
        @conn = nil
      end

      def close
        @mutex.synchronize { discard }
      end

      private

      def run
        @mutex.synchronize do
          @conn ||= @connect.call
          yield @conn
        rescue StandardError
          discard if @conn&.broken?
          raise
        end
      end

      def discard
        @conn&.close
      rescue StandardError
        nil
      ensure
        @conn = nil
      end
    end

    # MySQL has no CREATE INDEX IF NOT EXISTS; an existing index is fine.
    def self.duplicate_index?(error)
      (error.respond_to?(:error_number) && error.error_number == 1061) ||
        error.message.match?(/1061|Duplicate key name/i)
    end

    # Turns "?" into "$1, $2, ..." for PostgreSQL, leaving quoted text alone.
    def self.rebind(sql)
      return sql unless sql.include?("?")
      n = 0
      quote = nil
      out = +""
      sql.each_char do |ch|
        if quote
          quote = nil if ch == quote
        elsif ch == "'" || ch == '"'
          quote = ch
        elsif ch == "?"
          n += 1
          out << "$" << n.to_s
          next
        end
        out << ch
      end
      out
    end

    # Opens the store for a database URL: postgres://, postgresql://,
    # mysql://, sqlite:///absolute/path.db or sqlite://relative.db.
    def self.open_url(raw)
      u = URI.parse(raw)
      case u.scheme.to_s.downcase
      when "postgres", "postgresql"
        driver("pg", "postgres")
        SQL.new("postgres") { PgConn.new(PG.connect(raw).tap { |c| c.set_notice_processor { nil } }) }
      when "mysql", "mysql2"
        driver("mysql2", "mysql")
        SQL.new("mysql") { Mysql2Conn.new(Mysql2Conn.client(u)) }
      when "sqlite", "sqlite3"
        driver("sqlite3", "sqlite")
        path = sqlite_path(raw, u.scheme)
        SQL.new("sqlite") { Sqlite3Conn.new(path) }
      else
        raise ConfigError, "visionary: unsupported database URL scheme #{u.scheme.inspect}"
      end
    rescue URI::InvalidURIError => e
      raise ConfigError, "visionary: database URL: #{e.message}"
    end

    def self.sqlite_path(raw, scheme)
      rest = raw.sub(%r{\A#{Regexp.escape(scheme)}://}i, "").sub(%r{\A/}, "")
      rest = "/#{rest}" if raw.match?(%r{\A#{Regexp.escape(scheme)}:///}i) && !rest.include?(":")
      URI.decode_www_form_component(rest.sub(/\?.*\z/, ""))
    end

    def self.driver(gem_name, dialect)
      require gem_name
    rescue LoadError
      raise ConfigError, "visionary: #{dialect} needs the #{gem_name} gem (add it to your Gemfile)"
    end

    # Thin driver adapters: execute returns affected rows, column returns the
    # first column of every row, rows every row as an array.
    class PgConn
      def initialize(conn)
        @conn = conn
      end

      def execute(sql, args)
        res = @conn.exec_params(Store.rebind(sql), args.map { |a| pg_param(a) })
        res.cmd_tuples
      ensure
        res&.clear
      end

      def column(sql)
        res = @conn.exec(sql)
        res.column_values(0).map(&:to_s)
      ensure
        res&.clear
      end

      def rows(sql)
        res = @conn.exec(sql)
        res.values
      ensure
        res&.clear
      end

      def broken?
        @conn.finished? || @conn.status != PG::CONNECTION_OK
      rescue StandardError
        true
      end

      def close
        @conn.close unless @conn.finished?
      end

      private

      # Text params; PostgreSQL types them from the target column. NUL is
      # the one character text columns reject.
      def pg_param(v)
        case v
        when nil then nil
        when Float then D1.param(v)
        when String then v.include?("\0") ? v.delete("\0") : v
        else v.to_s
        end
      end
    end

    class Mysql2Conn
      def self.client(u)
        q = URI.decode_www_form(u.query.to_s).to_h
        Mysql2::Client.new(
          host: u.host || "localhost", port: u.port || 3306,
          username: u.user && URI.decode_www_form_component(u.user),
          password: u.password && URI.decode_www_form_component(u.password),
          database: u.path.to_s.delete_prefix("/"),
          encoding: q.fetch("encoding", "utf8mb4"),
          connect_timeout: 10, read_timeout: 30, write_timeout: 30
        )
      end

      def initialize(client)
        @client = client
        @stmts = {}
      end

      def execute(sql, args)
        return @client.query(sql).then { @client.affected_rows } if args.empty?

        stmt = (@stmts[sql] ||= @client.prepare(sql))
        stmt.execute(*args)
        stmt.affected_rows
      end

      def column(sql)
        @client.query(sql, as: :array).map { |r| r[0].to_s }
      end

      def rows(sql)
        @client.query(sql, as: :array).to_a
      end

      def broken?
        !@client.ping
      rescue StandardError
        true
      end

      def close
        @stmts.each_value do |s|
          s.close
        rescue StandardError
          nil
        end
        @client.close
      end
    end

    class Sqlite3Conn
      def initialize(path)
        @db = SQLite3::Database.new(path)
        @db.busy_timeout = 5000
      end

      def execute(sql, args)
        @db.execute(sql, args)
        @db.changes
      end

      def column(sql)
        @db.execute(sql).map { |r| r[0].to_s }
      end

      def rows(sql) = @db.execute(sql)

      def broken? = @db.closed?

      def close
        @db.close unless @db.closed?
      end
    end
  end
end
