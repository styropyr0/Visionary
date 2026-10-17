import mysqlRequests from "../../schema/mysql/requests.sql";
import mysqlSystem from "../../schema/mysql/system.sql";
import mysqlTelemetry from "../../schema/mysql/telemetry.sql";
import { quietly } from "../context.js";
import { optionalRequire } from "../util.js";
import type { Stmt, Value } from "./sql.js";
import { statements } from "./sql.js";
import type { Store } from "./store.js";

interface MyQueryable {
  query(sql: string, values?: unknown[]): Promise<[any, any]>;
}
interface MyPool extends MyQueryable {
  getConnection(): Promise<MyQueryable & { beginTransaction(): Promise<void>; commit(): Promise<void>; rollback(): Promise<void>; release(): void }>;
  end(): Promise<void>;
}

/** MySQL 8 / MariaDB through `mysql2`: an app-provided pool or connection
 * (callback or promise API), or a small pool the SDK opens from a URL. */
export class MysqlStore implements Store {
  readonly dialect = "mysql" as const;
  readonly stringParams = false;

  private constructor(
    private readonly db: MyQueryable,
    private readonly owned: boolean,
  ) {}

  static fromUrl(url: string): MysqlStore {
    const mysql = optionalRequire("mysql2/promise");
    if (!mysql?.createPool) throw new Error("visionary: MySQL needs the mysql2 package: npm install mysql2");
    const pool = mysql.createPool({ uri: url, connectionLimit: 2, timezone: "Z", charset: "utf8mb4", connectTimeout: 10_000 });
    return new MysqlStore(pool, true);
  }

  static fromClient(db: any): MysqlStore {
    return new MysqlStore(typeof db.promise === "function" ? db.promise() : db, false);
  }

  execScript(script: string): Promise<void> {
    return quietly(async () => {
      for (const sql of statements(script)) {
        try {
          await this.db.query(sql);
        } catch (e) {
          // No CREATE INDEX IF NOT EXISTS in MySQL; an existing index is fine.
          if ((e as { errno?: number }).errno !== 1061) throw e;
        }
      }
    });
  }

  batch(stmts: Stmt[]): Promise<void> {
    return quietly(async () => {
      const rows = stmts.map((s) => ({ sql: s.sql, args: fit(s.sql, s.args) }));
      const pool = this.db as Partial<MyPool>;
      if (typeof pool.getConnection !== "function") {
        for (const s of rows) await this.db.query(s.sql, s.args);
        return;
      }
      const conn = await pool.getConnection();
      try {
        await conn.beginTransaction();
        for (const s of rows) await conn.query(s.sql, s.args);
        await conn.commit();
      } catch (e) {
        await conn.rollback().catch(() => undefined);
        throw e;
      } finally {
        conn.release();
      }
    });
  }

  execChanges(sql: string, args: Value[]): Promise<number> {
    return quietly(async () => Number((await this.db.query(sql, args))[0]?.affectedRows) || 0);
  }

  columns(sql: string): Promise<string[]> {
    return quietly(async () => ((await this.db.query(sql))[0] as any[]).map((r) => String(Object.values(r)[0])));
  }

  async close(): Promise<void> {
    if (this.owned) await (this.db as MyPool).end().catch(() => undefined);
  }
}

/** Column limits from the MySQL schema: VARCHAR(n) counts characters,
 * TEXT and MEDIUMTEXT count bytes. Strict mode rejects longer values, which
 * would fail the whole batch, so values are cut first. */
type Limit = { chars?: number; bytes?: number };

const LIMITS: Record<string, Record<string, Limit>> = (() => {
  const out: Record<string, Record<string, Limit>> = {};
  for (const script of [mysqlRequests, mysqlSystem, mysqlTelemetry]) {
    for (const stmt of statements(script)) {
      const m = /^CREATE TABLE IF NOT EXISTS (\w+) \(([\s\S]*)\)/.exec(stmt);
      if (!m) continue;
      const cols: Record<string, Limit> = {};
      for (const line of m[2]!.split("\n")) {
        const c = /^\s*(\w+)\s+(VARCHAR\((\d+)\)|MEDIUMTEXT|TEXT)\b/.exec(line);
        if (!c) continue;
        cols[c[1]!] = c[3] ? { chars: Number(c[3]) } : { bytes: c[2] === "TEXT" ? 65535 : 16777215 };
      }
      out[m[1]!] = cols;
    }
  }
  return out;
})();

function fit(sql: string, args: Value[]): Value[] {
  const m = /^INSERT INTO (\w+) \(([^)]*)\)/.exec(sql);
  const limits = m ? LIMITS[m[1]!] : undefined;
  if (!m || !limits) return args;
  const names = m[2]!.split(", ");
  return args.map((v, i) => {
    const limit = limits[names[i]!];
    if (typeof v !== "string" || !limit) return v;
    if (limit.chars !== undefined) return v.length > limit.chars ? [...v].slice(0, limit.chars).join("") : v;
    return cutBytes(v, limit.bytes!);
  });
}

function cutBytes(s: string, limit: number): string {
  const b = Buffer.from(s, "utf8");
  if (b.length <= limit) return s;
  // Decoding a cut multi-byte sequence yields U+FFFD; drop it.
  return b.subarray(0, limit).toString("utf8").replace(/�$/, "");
}
