import { quietly } from "../context.js";
import type { Logger } from "../log.js";
import { errMessage } from "../log.js";
import { optionalRequire } from "../util.js";
import type { Stmt, Value } from "./sql.js";
import { rebind, statements } from "./sql.js";
import type { Store } from "./store.js";

interface PgQueryable {
  query(sql: string, values?: unknown[]): Promise<{ rows: any[]; rowCount: number | null }>;
}
interface PgPool extends PgQueryable {
  connect(): Promise<PgQueryable & { release(err?: unknown): void }>;
  end(): Promise<void>;
  on(event: "error", fn: (e: Error) => void): void;
}

/** PostgreSQL through `pg`: an app-provided Pool or Client, or a small pool
 * the SDK opens from a URL. */
export class PostgresStore implements Store {
  readonly dialect = "postgres" as const;
  readonly stringParams = false;

  private constructor(
    private readonly db: PgQueryable,
    private readonly owned: boolean,
  ) {}

  static fromUrl(url: string, logger: Logger): PostgresStore {
    const pg = optionalRequire("pg");
    if (!pg?.Pool) throw new Error("visionary: PostgreSQL needs the pg package: npm install pg");
    const pool: PgPool = new pg.Pool({ connectionString: url, max: 2, connectionTimeoutMillis: 10_000 });
    // An idle connection that drops emits "error"; unhandled it would crash the app.
    pool.on("error", (e) => logger.warn("visionary: postgres connection error: " + errMessage(e)));
    return new PostgresStore(pool, true);
  }

  static fromClient(db: PgQueryable): PostgresStore {
    return new PostgresStore(db, false);
  }

  execScript(script: string): Promise<void> {
    return quietly(async () => {
      for (const sql of statements(script)) await this.db.query(sql);
    });
  }

  batch(stmts: Stmt[]): Promise<void> {
    return quietly(async () => {
      // A Pool gives a dedicated connection for the transaction; a single
      // Client is shared with the app, so its rows go in one by one.
      if (!this.owned && !("totalCount" in this.db)) {
        for (const s of stmts) await this.db.query(rebind(s.sql), s.args.map(clean));
        return;
      }
      const client = await (this.db as PgPool).connect();
      try {
        await client.query("BEGIN");
        for (const s of stmts) await client.query(rebind(s.sql), s.args.map(clean));
        await client.query("COMMIT");
        client.release();
      } catch (e) {
        await client.query("ROLLBACK").catch(() => undefined);
        client.release(e);
        throw e;
      }
    });
  }

  execChanges(sql: string, args: Value[]): Promise<number> {
    return quietly(async () => (await this.db.query(rebind(sql), args)).rowCount ?? 0);
  }

  columns(sql: string): Promise<string[]> {
    return quietly(async () => (await this.db.query(sql)).rows.map((r) => String(Object.values(r)[0])));
  }

  async close(): Promise<void> {
    if (this.owned) await (this.db as PgPool).end().catch(() => undefined);
  }
}

/** PostgreSQL text cannot hold NUL bytes; one would fail the whole batch. */
function clean(v: Value): Value {
  return typeof v === "string" && v.includes("\0") ? v.replaceAll("\0", "") : v;
}
