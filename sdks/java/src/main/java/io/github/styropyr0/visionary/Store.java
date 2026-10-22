package io.github.styropyr0.visionary;

import java.util.List;
import java.util.Map;

/** Where rows go. Implementations are called from Visionary's background threads only. */
interface Store {

    /** Creates the table and its indexes if they don't exist. */
    void createSchema(Table table) throws Exception;

    /**
     * Inserts the rows of several tables as one unit: one transaction (SQL) or
     * batch calls of at most 50 statements (D1). Tables are written in map order.
     */
    void insert(Map<Table, List<Object[]>> rows) throws Exception;

    default void insert(Table table, List<Object[]> rows) throws Exception {
        insert(Map.of(table, rows));
    }

    /** Runs a read-only query without parameters and returns its rows as text (null stays null). */
    List<String[]> select(String sql) throws Exception;

    /** Runs one chunk of the retention delete and returns the number of rows removed. */
    long purgeChunk(Table table, String cutoff) throws Exception;

    /** Short description for logs, without credentials. */
    String describe();

    void close();
}
