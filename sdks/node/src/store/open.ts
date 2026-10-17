import type { Config, Dialect } from "../config.js";
import type { Logger } from "../log.js";
import { D1Store } from "./d1.js";
import { MysqlStore } from "./mysql.js";
import { PostgresStore } from "./postgres.js";
import { SqliteStore, sqlitePath } from "./sqlite.js";
import type { Store } from "./store.js";

/** The store the config asks for, or undefined when no database is configured.
 * Throws on a misconfiguration (unknown scheme, driver not installed). */
export function openStore(cfg: Config, logger: Logger): Store | undefined {
  if (cfg.db) return fromClient(cfg.db, cfg.dialect || detectDialect(cfg.db));

  const url = cfg.databaseUrl.trim();
  if (!url) {
    const { accountId, databaseId, apiToken, apiBase } = cfg.d1;
    return accountId && databaseId && apiToken ? new D1Store(accountId, databaseId, apiToken, apiBase) : undefined;
  }
  const sep = url.indexOf("://");
  if (sep < 0) throw new Error("visionary: VISIONARY_DATABASE_URL needs a scheme (postgres://, mysql://, sqlite://, d1://)");
  const scheme = url.slice(0, sep).split("+")[0]!.toLowerCase();
  const rest = url.slice(sep + 3);
  switch (scheme) {
    case "postgres":
    case "postgresql":
      return PostgresStore.fromUrl("postgresql://" + rest, logger);
    case "mysql":
    case "mariadb":
      return MysqlStore.fromUrl("mysql://" + rest);
    case "sqlite":
    case "sqlite3":
      return SqliteStore.open(sqlitePath(rest));
    case "d1": {
      // d1:// uses the D1_* variables; d1://ACCOUNT/DATABASE overrides the ids.
      const [account, database] = rest.replace(/^\/+|\/+$/g, "").split("/");
      const accountId = account || cfg.d1.accountId;
      const databaseId = database || cfg.d1.databaseId;
      if (!accountId || !databaseId || !cfg.d1.apiToken) {
        throw new Error("visionary: d1:// needs D1_ACCOUNT_ID, D1_DATABASE_ID and D1_API_TOKEN");
      }
      return new D1Store(accountId, databaseId, cfg.d1.apiToken, cfg.d1.apiBase);
    }
  }
  throw new Error(`visionary: unsupported database URL scheme "${scheme}"`);
}

function fromClient(db: any, dialect: Dialect | ""): Store {
  switch (dialect) {
    case "postgres":
      return PostgresStore.fromClient(db);
    case "mysql":
      return MysqlStore.fromClient(db);
    case "sqlite":
      return SqliteStore.fromClient(db);
  }
  throw new Error("visionary: could not tell which database `db` is; pass `dialect` (postgres, mysql or sqlite)");
}

/** Recognises pg, mysql2, better-sqlite3 and node:sqlite objects. */
function detectDialect(db: any): Dialect | "" {
  if ("totalCount" in db || "connectionParameters" in db || db.constructor?.name === "BoundPool") return "postgres";
  if (typeof db.getConnection === "function" || typeof db.execute === "function") return "mysql";
  if (typeof db.prepare === "function" && typeof db.exec === "function") return "sqlite";
  return "";
}
