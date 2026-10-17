import { quietly } from "../context.js";
import { optionalRequire } from "../util.js";
import type { Stmt, Value } from "./sql.js";
import { statements } from "./sql.js";
import type { Store } from "./store.js";

/** The part of better-sqlite3's Database and node:sqlite's DatabaseSync the
 * SDK uses; both are synchronous. */
interface SyncDatabase {
  exec(sql: string): unknown;
  prepare(sql: string): {
    run(...args: unknown[]): { changes: number | bigint };
    all(...args: unknown[]): unknown[];
  };
  close(): unknown;
}

/** SQLite through better-sqlite3, else node:sqlite (Node 22.5+). Writes are
 * synchronous but small and batched off the request path. */
export class SqliteStore implements Store {
  readonly dialect = "sqlite" as const;
  readonly stringParams = false;
  private readonly prepared = new Map<string, ReturnType<SyncDatabase["prepare"]>>();

  private constructor(
    private readonly db: SyncDatabase,
    private readonly owned: boolean,
  ) {}

  static open(path: string): SqliteStore {
    const Better = optionalRequire("better-sqlite3");
    let db: SyncDatabase | undefined;
    try {
      // The native binding loads here; one built for another platform throws.
      if (Better) db = new Better(path);
    } catch {
      db = undefined;
    }
    if (!db) {
      const nodeSqlite = optionalRequire("node:sqlite");
      if (!nodeSqlite?.DatabaseSync) {
        throw new Error("visionary: SQLite needs better-sqlite3 (npm install better-sqlite3) or Node 22.5+ (node:sqlite)");
      }
      db = new nodeSqlite.DatabaseSync(path) as SyncDatabase;
    }
    // Other processes (the Visionary server, other instances) share the file.
    db!.exec("PRAGMA busy_timeout = 10000");
    return new SqliteStore(db!, true);
  }

  static fromClient(db: SyncDatabase): SqliteStore {
    return new SqliteStore(db, false);
  }

  async execScript(script: string): Promise<void> {
    quietly(() => {
      for (const sql of statements(script)) this.db.exec(sql);
    });
  }

  async batch(stmts: Stmt[]): Promise<void> {
    quietly(() => {
      this.db.exec("BEGIN");
      try {
        for (const s of stmts) this.stmt(s.sql).run(...s.args);
        this.db.exec("COMMIT");
      } catch (e) {
        try {
          this.db.exec("ROLLBACK");
        } catch {
          // no transaction left to roll back
        }
        throw e;
      }
    });
  }

  async execChanges(sql: string, args: Value[]): Promise<number> {
    return quietly(() => Number(this.stmt(sql).run(...args).changes));
  }

  async columns(sql: string): Promise<string[]> {
    return quietly(() => this.db.prepare(sql).all().map((r) => String(Object.values(r as object)[0])));
  }

  async close(): Promise<void> {
    this.prepared.clear();
    if (this.owned) this.db.close();
  }

  private stmt(sql: string) {
    let s = this.prepared.get(sql);
    if (!s) {
      s = this.db.prepare(sql);
      this.prepared.set(sql, s);
    }
    return s;
  }
}

/** The file path in sqlite:///abs/path.db, sqlite:///C:/x.db or sqlite://relative.db. */
export function sqlitePath(rest: string): string {
  let path = decodeURIComponent(rest.split("?")[0]!);
  if (/^\/[A-Za-z]:/.test(path)) path = path.slice(1);
  return path;
}
