import type { Dialect } from "../config.js";
import mysqlLogs from "../../schema/mysql/logs.sql";
import mysqlRequests from "../../schema/mysql/requests.sql";
import mysqlSystem from "../../schema/mysql/system.sql";
import mysqlTelemetry from "../../schema/mysql/telemetry.sql";
import postgresLogs from "../../schema/postgres/logs.sql";
import postgresRequests from "../../schema/postgres/requests.sql";
import postgresSystem from "../../schema/postgres/system.sql";
import postgresTelemetry from "../../schema/postgres/telemetry.sql";
import sqliteLogs from "../../schema/sqlite/logs.sql";
import sqliteRequests from "../../schema/sqlite/requests.sql";
import sqliteSystem from "../../schema/sqlite/system.sql";
import sqliteTelemetry from "../../schema/sqlite/telemetry.sql";
import {
  ADDED_REQUEST_COLUMNS,
  ADDED_SYSTEM_COLUMNS,
  type Kind,
  type Stmt,
  type Table,
  columnsQuery,
  insertSQL,
  isDuplicateColumn,
  missingColumns,
  statements,
} from "./sql.js";

/** Where rows go: D1 over REST, or a SQL connection. */
export interface Store {
  readonly dialect: Dialect;
  /** Args travel as strings (D1). */
  readonly stringParams: boolean;
  /** Runs each statement of a schema script; MySQL's duplicate index error is ignored. */
  execScript(script: string): Promise<void>;
  /** Runs all statements in one transaction (or one D1 batch call). */
  batch(stmts: Stmt[]): Promise<void>;
  /** Runs one statement and reports the rows it changed. */
  execChanges(sql: string, args: Stmt["args"]): Promise<number>;
  /** Runs a query that returns one name per row. */
  columns(sql: string): Promise<string[]>;
  /** Runs a query; every value comes back as text ("" for NULL). */
  rows(sql: string, args: Stmt["args"]): Promise<string[][]>;
  close(): Promise<void>;
}

export type Schema = "requests" | "system" | "telemetry" | "logs";

const SCHEMAS: Record<Dialect, Record<Schema, string>> = {
  sqlite: { requests: sqliteRequests, system: sqliteSystem, telemetry: sqliteTelemetry, logs: sqliteLogs },
  postgres: { requests: postgresRequests, system: postgresSystem, telemetry: postgresTelemetry, logs: postgresLogs },
  mysql: { requests: mysqlRequests, system: mysqlSystem, telemetry: mysqlTelemetry, logs: mysqlLogs },
};

const MIGRATIONS: Partial<Record<Schema, [Table, [string, Kind][]]>> = {
  requests: ["api_logs", ADDED_REQUEST_COLUMNS],
  system: ["system_metrics", ADDED_SYSTEM_COLUMNS],
};

/** Creates a schema's tables and adds the columns an older table is missing. */
export async function createSchema(store: Store, schema: Schema): Promise<void> {
  await store.execScript(SCHEMAS[store.dialect][schema]);
  const migration = MIGRATIONS[schema];
  if (!migration) return;
  const [table, added] = migration;
  const existing = await store.columns(columnsQuery(store.dialect, table));
  for (const stmt of missingColumns(store.dialect, table, added, existing)) {
    try {
      await store.execScript(stmt);
    } catch (e) {
      if (!isDuplicateColumn(e)) throw e;
    }
  }
}

export function insertFor(store: Store, table: Table): string {
  return insertSQL(table, store.stringParams);
}

export { statements };
