package io.github.styropyr0.visionary;

import java.util.List;

/** Where rows go. Implementations are called from Visionary's background threads only. */
interface Store {

    /** Creates the table and its indexes if they don't exist. */
    void createSchema(Table table) throws Exception;

    /** Inserts all rows as one unit: one transaction (SQL) or one batch call (D1). */
    void insert(Table table, List<Object[]> rows) throws Exception;

    /** Runs one chunk of the retention delete and returns the number of rows removed. */
    long purgeChunk(Table table, String cutoff) throws Exception;

    /** Short description for logs, without credentials. */
    String describe();

    void close();
}
