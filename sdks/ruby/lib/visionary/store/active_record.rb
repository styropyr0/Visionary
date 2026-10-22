# frozen_string_literal: true

module Visionary
  module Store
    # Writes through the application's ActiveRecord pool. Each operation
    # checks a connection out for the background thread doing it and returns
    # it right after, so Visionary holds at most one pool connection at a time
    # (the mutex) and only while writing. Values are quoted by the adapter,
    # exactly as ActiveRecord does when prepared statements are off.
    class ActiveRecord
      attr_reader :dialect

      def initialize(connection_class)
        @klass = connection_class
        @mutex = Mutex.new
        adapter = adapter_name.downcase
        @dialect = if adapter.include?("postg") then "postgres"
                   elsif adapter.include?("mysql") || adapter.include?("trilogy") then "mysql"
                   elsif adapter.include?("sqlite") then "sqlite"
                   else raise ConfigError, "visionary: unsupported ActiveRecord adapter #{adapter.inspect}"
                   end
      end

      def string_params? = false

      def exec_script(statements)
        statements.each do |s|
          with { |c| c.execute(s) }
        rescue StandardError => e
          next if dialect == "mysql" && Store.duplicate_index?(e)
          raise
        end
      end

      def exec(sql)
        with { |c| c.execute(sql) }
      end

      def column_names(sql)
        with { |c| c.select_values(sql).map(&:to_s) }
      end

      # Every row of a query, as arrays of values.
      def rows(sql)
        with { |c| c.select_rows(sql) }
      end

      def batch(stmts)
        with do |c|
          c.transaction(requires_new: true) do
            stmts.each { |sql, args| c.execute(bind(c, sql, args)) }
          end
        end
      end

      def changes(sql, args)
        with { |c| c.exec_delete(bind(c, sql, args)) }
      end

      # ActiveRecord discards inherited pools itself after fork.
      def after_fork
        @mutex = Mutex.new
      end

      def close; end

      private

      def adapter_name
        if @klass.respond_to?(:connection_db_config)
          @klass.connection_db_config.adapter.to_s
        else
          @klass.connection_pool.with_connection(&:adapter_name).to_s
        end
      end

      # Our own statements stay out of the app's log.
      def with(&block)
        @mutex.synchronize do
          logger = @klass.logger
          if logger.respond_to?(:silence)
            logger.silence(::Logger::WARN) { @klass.connection_pool.with_connection(&block) }
          else
            @klass.connection_pool.with_connection(&block)
          end
        end
      end

      def bind(conn, sql, args)
        i = -1
        quote = nil
        out = +""
        sql.each_char do |ch|
          if quote
            quote = nil if ch == quote
          elsif ch == "'"
            quote = ch
          elsif ch == "?"
            out << conn.quote(args[i += 1])
            next
          end
          out << ch
        end
        out
      end
    end
  end
end
