# frozen_string_literal: true

module Visionary
  # Creates tables on first use and retries on later writes until it works,
  # so a database that is down at startup doesn't leave tables missing.
  # Older tables gain the columns added since (ALTER TABLE ... ADD COLUMN).
  class Schemas
    def initialize(store, logger)
      @store = store
      @logger = logger
      @ready = {}
      @reported = {}
      @mutex = Mutex.new
    end

    def ensure(group)
      @mutex.synchronize do
        return true if @ready[group]
        attempt(group)
        @ready[group] = true
      rescue StandardError => e
        unless @reported[group]
          @reported[group] = true
          @logger.error("visionary: create #{group} tables failed (will retry): #{e.message}")
        end
        false
      end
    end

    private

    # CREATE ... IF NOT EXISTS is not race-safe in PostgreSQL: when several
    # processes start at once, one may fail while another creates the table.
    def attempt(group)
      tries = 0
      begin
        create(group)
      rescue StandardError
        raise if (tries += 1) >= 3
        sleep(0.25 * tries)
        retry
      end
    end

    def create(group)
      dialect = @store.dialect
      @store.exec_script(Schema.statements(dialect, group))
      g = Schema::GROUPS.fetch(group)
      return if g[:added].empty?

      existing = @store.column_names(Schema.columns_query(dialect, g[:table]))
      Schema.missing_columns(dialect, g[:table], g[:added], existing).each do |stmt|
        @store.exec(stmt)
      rescue StandardError => e
        raise unless Schema.duplicate_column?(e)
      end
    end
  end
end
