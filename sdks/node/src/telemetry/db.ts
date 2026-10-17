import { AsyncLocalStorage, AsyncResource } from "node:async_hooks";
import { basename } from "node:path";
import { markVisible, startSpan } from "./spans.js";
import type { ActiveSpan } from "./spans.js";
import { sanitizeSQL, sqlName } from "./sql.js";

const PATCHED = Symbol.for("visionary.instrumented");

function dbSpan(sql: string, target: string): ActiveSpan | undefined {
  return startSpan({ kind: "db", name: sqlName(sql), target, detail: sanitizeSQL(sql) });
}

/** Ends `s` when a call finishes: through its callback, its promise, or its
 * events; `rows` reads a row count from the result. Returns the call's result. */
function track(
  s: ActiveSpan,
  call: (args: unknown[]) => any,
  args: unknown[],
  rows: (result: any) => number | null,
): any {
  const cb = args.length > 0 ? args[args.length - 1] : undefined;
  if (typeof cb === "function") {
    args[args.length - 1] = function (this: unknown, err: unknown, result: unknown, ...rest: unknown[]) {
      s.end(err ?? undefined, err ? null : rows(result));
      return (cb as Function).call(this, err, result, ...rest);
    };
    return call(args);
  }
  let result: any;
  try {
    result = call(args);
  } catch (e) {
    s.end(e);
    throw e;
  }
  if (result && typeof result.then === "function") {
    result.then(
      (r: unknown) => s.end(undefined, rows(r)),
      (e: unknown) => s.end(e ?? new Error("rejected")),
    );
  } else if (result && typeof result.once === "function") {
    // A streaming query. Listening for "error" would change what an
    // unhandled error does, so only completion is observed.
    result.once("end", () => s.end());
  } else {
    s.end();
  }
  return result;
}

function wrapMethod(obj: any, method: string, wrapper: (orig: Function) => Function): void {
  const orig = obj?.[method];
  if (typeof orig !== "function" || orig[PATCHED]) return;
  const wrapped = wrapper(orig);
  (wrapped as any)[PATCHED] = true;
  obj[method] = wrapped;
}

// ---------------------------------------------------------------- pg

function pgTarget(self: any): string {
  const database =
    self?.database ??
    self?.connectionParameters?.database ??
    self?.options?.database ??
    dbFromUrl(self?.options?.connectionString);
  return "postgresql" + (database ? "/" + database : "");
}

function dbFromUrl(url: unknown): string {
  if (typeof url !== "string") return "";
  try {
    return decodeURIComponent(new URL(url).pathname.replace(/^\//, ""));
  } catch {
    return "";
  }
}

function pgRows(r: any): number | null {
  return typeof r?.rowCount === "number" ? r.rowCount : null;
}

function wrapPgQuery(orig: Function): Function {
  return function (this: unknown, ...args: any[]) {
    const first = args[0];
    const text = typeof first === "string" ? first : first?.text;
    // Submittables (pg-cursor, pg-query-stream) stream rows on their own
    // schedule and are left unrecorded.
    const s = typeof text === "string" && typeof first?.submit !== "function" ? dbSpan(text, pgTarget(this)) : undefined;
    if (!s) return orig.apply(this, args);
    return track(s, (a) => orig.apply(this, a), args, pgRows);
  };
}

/**
 * Records a span for every query made through `pg`. Pass the module to cover
 * every Client and Pool (`instrumentPg(require("pg"))` or the default import),
 * or a single Pool or Client.
 */
export function instrumentPg<T>(target: T): T {
  const t = target as any;
  if (typeof t?.Client?.prototype?.query === "function") {
    wrapMethod(t.Client.prototype, "query", wrapPgQuery);
  } else {
    wrapMethod(t, "query", wrapPgQuery);
  }
  markVisible("db");
  return target;
}

// ------------------------------------------------------------ mysql2

function mysqlConfig(obj: any): any {
  return (
    obj?.config?.connectionConfig ??
    obj?.config ??
    obj?.pool?.config?.connectionConfig ??
    obj?.connection?.config ??
    obj?.connection?.connection?.config
  );
}

function mysqlRows(r: any): number | null {
  // The promise API resolves to [rows, fields]; fields is undefined for writes.
  const rows = Array.isArray(r) && r.length === 2 && (Array.isArray(r[1]) || r[1] === undefined) ? r[0] : r;
  if (Array.isArray(rows)) return rows.length;
  return typeof rows?.affectedRows === "number" ? rows.affectedRows : null;
}

/**
 * Records a span for every query and execute on a `mysql2` pool or connection
 * (callback or promise API), including connections taken with getConnection().
 */
export function instrumentMysql2<T>(target: T): T {
  instrumentMysqlObject(target as any);
  markVisible("db");
  return target;
}

/** Set while a pool-level call runs, so the connection it uses internally
 * (mysql2 pools call query/execute on a pooled connection) is not recorded
 * a second time. */
const insideCall = new AsyncLocalStorage<boolean>();

/** Runs a wrapped driver call with the inside marker, keeping the caller's
 * own callback in the caller's context. */
function runInside(call: () => any, args: unknown[]): any {
  const last = args.length - 1;
  if (last >= 0 && typeof args[last] === "function") args[last] = AsyncResource.bind(args[last] as (...a: any[]) => any);
  return insideCall.run(true, call);
}

function instrumentMysqlObject(obj: any): void {
  if (!obj || typeof obj !== "object" || obj[PATCHED]) return;
  obj[PATCHED] = true;
  const database = mysqlConfig(obj)?.database;
  const target = "mysql" + (database ? "/" + database : "");
  for (const method of ["query", "execute"]) {
    wrapMethod(obj, method, (orig) =>
      function (this: unknown, ...args: any[]) {
        if (insideCall.getStore()) return orig.apply(this, args);
        const first = args[0];
        const sql = typeof first === "string" ? first : first?.sql;
        const s = typeof sql === "string" ? dbSpan(sql, target) : undefined;
        if (!s) return orig.apply(this, args);
        return track(s, (a) => runInside(() => orig.apply(this, a), a), args, mysqlRows);
      },
    );
  }
  wrapMethod(obj, "getConnection", (orig) =>
    function (this: unknown, ...args: any[]) {
      if (insideCall.getStore()) return orig.apply(this, args);
      const cb = args[args.length - 1];
      if (typeof cb === "function") {
        // Pools may call back from another request's context; keep the caller's.
        args[args.length - 1] = AsyncResource.bind((err: unknown, conn: unknown) => {
          if (!err) instrumentMysqlObject(conn);
          cb(err, conn);
        });
        return orig.apply(this, args);
      }
      const result = orig.apply(this, args);
      return result && typeof result.then === "function"
        ? result.then((conn: unknown) => (instrumentMysqlObject(conn), conn))
        : result;
    },
  );
}

// ------------------------------------------------------------ SQLite

/**
 * Records a span for every statement run on a `better-sqlite3` Database or a
 * `node:sqlite` DatabaseSync: `exec`, and `run`/`get`/`all`/`iterate` on
 * prepared statements.
 */
export function instrumentSqlite<T>(db: T): T {
  const d = db as any;
  const file = typeof d?.name === "string" ? d.name : typeof d?.location === "function" ? safe(() => d.location()) : "";
  const target = "sqlite" + (file && file !== ":memory:" ? "/" + basename(file) : "");
  wrapMethod(d, "exec", (orig) =>
    function (this: unknown, sql: string, ...rest: unknown[]) {
      const s = typeof sql === "string" ? dbSpan(sql, target) : undefined;
      if (!s) return orig.call(this, sql, ...rest);
      return track(s, (a) => orig.apply(this, a), [sql, ...rest], () => null);
    },
  );
  wrapMethod(d, "prepare", (orig) =>
    function (this: unknown, sql: string, ...rest: unknown[]) {
      const stmt = orig.call(this, sql, ...rest);
      if (stmt && typeof sql === "string") instrumentStatement(stmt, sql, target);
      return stmt;
    },
  );
  markVisible("db");
  return db;
}

const STATEMENT_ROWS: Record<string, (r: any) => number | null> = {
  run: (r) => (r && r.changes !== undefined ? Number(r.changes) : null),
  get: (r) => (r === undefined ? 0 : 1),
  all: (r) => (Array.isArray(r) ? r.length : null),
  iterate: () => null,
};

function instrumentStatement(stmt: any, sql: string, target: string): void {
  for (const [method, rows] of Object.entries(STATEMENT_ROWS)) {
    const orig = stmt[method];
    if (typeof orig !== "function") continue;
    try {
      stmt[method] = function (this: unknown, ...args: unknown[]) {
        const s = dbSpan(sql, target);
        if (!s) return orig.apply(this, args);
        try {
          const r = orig.apply(this, args);
          s.end(undefined, rows(r));
          return r;
        } catch (e) {
          s.end(e);
          throw e;
        }
      };
    } catch {
      return; // a sealed native object; leave it unrecorded
    }
  }
}

function safe(fn: () => unknown): string {
  try {
    const v = fn();
    return typeof v === "string" ? v : "";
  } catch {
    return "";
  }
}

// ------------------------------------------------------------ Prisma

const PRISMA_KEYWORDS: Record<string, string> = {
  findUnique: "SELECT", findUniqueOrThrow: "SELECT", findFirst: "SELECT", findFirstOrThrow: "SELECT",
  findMany: "SELECT", count: "SELECT", aggregate: "SELECT", groupBy: "SELECT", findRaw: "SELECT",
  create: "INSERT", createMany: "INSERT", createManyAndReturn: "INSERT",
  update: "UPDATE", updateMany: "UPDATE", updateManyAndReturn: "UPDATE",
  upsert: "UPSERT", delete: "DELETE", deleteMany: "DELETE",
};

function rawSQL(args: any): string {
  if (typeof args === "string") return args;
  if (Array.isArray(args) && typeof args[0] === "string") return args[0];
  if (Array.isArray(args?.strings)) return args.strings.join("?");
  if (Array.isArray(args?.[0]) && Array.isArray((args[0] as { raw?: unknown }).raw)) return args[0].join("?");
  if (typeof args?.sql === "string") return args.sql;
  return "";
}

/**
 * A Prisma Client extension that records a span for every operation:
 *
 *     const prisma = new PrismaClient().$extends(prismaExtension());
 *
 * Model operations are named like SQL (`SELECT User`, `INSERT Booking`) with
 * `User.findMany` as the detail; raw queries use their sanitized SQL.
 */
export function prismaExtension(opts: { target?: string } = {}) {
  markVisible("db");
  const target = opts.target ?? "prisma";
  return {
    name: "visionary",
    query: {
      async $allOperations({ model, operation, args, query }: { model?: string; operation: string; args: any; query: (args: any) => Promise<any> }) {
        let s: ActiveSpan | undefined;
        if (model) {
          const keyword = PRISMA_KEYWORDS[operation] ?? operation;
          s = startSpan({ kind: "db", name: `${keyword} ${model}`, target, detail: `${model}.${operation}` });
        } else {
          const sql = rawSQL(args);
          s = sql ? dbSpan(sql, target) : startSpan({ kind: "db", name: operation, target, detail: operation });
        }
        if (!s) return query(args);
        try {
          const result = await query(args);
          s.end(undefined, Array.isArray(result) ? result.length : typeof result?.count === "number" ? result.count : typeof result === "number" ? result : null);
          return result;
        } catch (e) {
          s.end(e);
          throw e;
        }
      },
    },
  };
}
