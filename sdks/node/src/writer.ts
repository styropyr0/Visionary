import type { Config, LookupResult } from "./config.js";
import type { Entry } from "./entry.js";
import { requestValues } from "./entry.js";
import type { Logger } from "./log.js";
import { errMessage } from "./log.js";
import type { LogRecord } from "./logs/pipeline.js";
import { Redactor } from "./redact.js";
import type { Stmt, Table, Value } from "./store/sql.js";
import { PURGE_CHUNK, TABLE_COLUMNS, insertRows, insertSQL, purgeSQL } from "./store/sql.js";
import type { Store } from "./store/store.js";
import { formatTs, sleep } from "./util.js";

const QUEUE_SIZE = 5000;
/** D1 takes 50 statements a call, one per row. */
const D1_BATCH = 50;
const SQL_BATCH = 500;
const MAX_SQL_BATCH = 2000;
const PARALLEL_WRITES = 4;
const FLUSH_MS = 3000;

export type Item = { entry: Entry } | { table: Table; args: Value[] } | { log: LogRecord };

export interface WriterOptions {
  /** Rows waiting at most (default 5000); past it they are dropped and counted. */
  queueSize?: number;
  logRow?: (r: LogRecord) => Value[];
}

/** Rows waiting for the database, and rows written or dropped since start. */
export interface WriterStats {
  queued: number;
  written: number;
  dropped: number;
}

/**
 * Queues rows without blocking and writes them in batches, every 3 s or once
 * a batch is waiting: 50 rows on D1, 500 on SQL databases as multi-row
 * INSERTs. PostgreSQL and MySQL take up to 4 batches at once. A full queue
 * drops rows and counts them.
 */
export class Writer {
  private queue: Item[] = [];
  private dropped = 0;
  private written = 0;
  private lost = 0;
  private timer?: NodeJS.Timeout;
  private kicking = false;
  private readonly flights = new Set<Promise<void>>();
  private closed = false;
  private readonly redact: Redactor;
  private readonly inserts: Record<Table, string>;
  private readonly queueSize: number;
  readonly batchSize: number;
  readonly parallel: number;

  constructor(
    private readonly cfg: Config,
    private readonly store: Store,
    private readonly ready: () => Promise<boolean>,
    private readonly logger: Logger,
    private readonly opts: WriterOptions = {},
  ) {
    this.queueSize = opts.queueSize ?? QUEUE_SIZE;
    const size = cfg.batchSize;
    this.batchSize = store.stringParams ? D1_BATCH : size > 0 && size <= MAX_SQL_BATCH ? size : SQL_BATCH;
    // SQLite and D1 take one writer at a time.
    this.parallel = store.stringParams || store.dialect === "sqlite" ? 1 : PARALLEL_WRITES;
    this.redact = new Redactor(cfg.redactHeaders, cfg.redactFields, cfg.redactRequestFields);
    const tables = Object.keys(TABLE_COLUMNS) as Table[];
    this.inserts = Object.fromEntries(tables.map((t) => [t, insertSQL(t, store.stringParams)])) as Record<Table, string>;
  }

  start(): void {
    this.timer = setInterval(() => {
      this.kick(true);
      this.reportDrops();
    }, FLUSH_MS);
    this.timer.unref();
  }

  /** Enqueues without blocking; a full queue drops the item. */
  push(item: Item): void {
    if (this.closed) return;
    if (this.queue.length >= this.queueSize) {
      this.dropped++;
      this.lost++;
      return;
    }
    this.queue.push(item);
    if (this.queue.length >= this.batchSize && !this.kicking) {
      this.kicking = true;
      setImmediate(() => {
        this.kicking = false;
        this.kick(false);
      });
    }
  }

  stats(): WriterStats {
    return { queued: this.queue.length, written: this.written, dropped: this.lost };
  }

  /** Drains the queue, waiting at most timeoutMs. */
  async close(timeoutMs: number): Promise<void> {
    this.closed = true;
    if (this.timer) clearInterval(this.timer);
    const drained = (async () => {
      while (this.queue.length > 0 || this.flights.size > 0) {
        this.kick(true);
        await Promise.all(this.flights);
      }
      this.reportDrops();
    })();
    let timer: NodeJS.Timeout | undefined;
    const timedOut = new Promise<boolean>((resolve) => {
      timer = setTimeout(() => resolve(true), timeoutMs);
    });
    const late = await Promise.race([drained.then(() => false), timedOut]);
    clearTimeout(timer);
    if (late) this.logger.warn(`visionary: flush timed out; ${this.queue.length} rows pending`);
  }

  /** Writes full batches (or, with partial, whatever is queued) while a
   * writer is free; each write that finishes starts the next. */
  private kick(partial: boolean): void {
    while (this.flights.size < this.parallel && (this.queue.length >= this.batchSize || (partial && this.queue.length > 0))) {
      const flight: Promise<void> = this.write(this.queue.splice(0, this.batchSize))
        .catch((e) => this.logger.error("visionary: writer failed: " + errMessage(e)))
        .finally(() => {
          this.flights.delete(flight);
          this.kick(false);
        });
      this.flights.add(flight);
    }
  }

  private reportDrops(): void {
    if (this.dropped > 0) {
      this.logger.warn(`visionary: queue full; ${this.dropped} rows dropped`);
      this.dropped = 0;
    }
  }

  private async write(batch: Item[]): Promise<void> {
    let stmts: Stmt[];
    try {
      stmts = await this.statements(batch);
    } catch (e) {
      this.lost += batch.length;
      this.logger.error(`visionary: building rows failed; ${batch.length} dropped: ${errMessage(e)}`);
      return;
    }
    let err: unknown;
    for (let attempt = 1; attempt <= 3; attempt++) {
      try {
        if (!(await this.ready())) throw new Error("tables not ready");
        await this.store.batch(stmts);
        this.written += batch.length;
        return;
      } catch (e) {
        err = e;
      }
      if (attempt < 3) await sleep(attempt * 1000);
    }
    this.lost += batch.length;
    this.logger.error(`visionary: batch write failed; ${batch.length} rows dropped: ${errMessage(err)}`);
  }

  /** One INSERT per row on D1; elsewhere each table's rows as multi-row INSERTs. */
  async statements(batch: Item[]): Promise<Stmt[]> {
    const users = await this.lookupUsers(batch);
    const rows = batch.map((item): [Table, Value[]] => {
      if ("entry" in item) return ["api_logs", requestValues(item.entry, this.cfg, this.redact, users)];
      if ("log" in item) return ["app_logs", this.opts.logRow!(item.log)];
      return [item.table, item.args];
    });
    if (this.store.stringParams) return rows.map(([table, args]) => ({ sql: this.inserts[table], args }));
    const byTable = new Map<Table, Value[][]>();
    for (const [table, args] of rows) {
      const group = byTable.get(table);
      if (group) group.push(args);
      else byTable.set(table, [args]);
    }
    return [...byTable].flatMap(([table, group]) => insertRows(table, group));
  }

  /** Resolves each distinct user missing a handle or role once per batch. */
  private async lookupUsers(batch: Item[]): Promise<Map<string, LookupResult>> {
    const out = new Map<string, LookupResult>();
    const lookup = this.cfg.lookupUser;
    if (!lookup) return out;
    for (const item of batch) {
      if (!("entry" in item)) continue;
      const e = item.entry;
      if (!e.userId || (e.userHandle && e.userRole) || out.has(e.userId)) continue;
      try {
        out.set(e.userId, await lookup(e.userId));
      } catch (err) {
        this.logger.warn(`visionary: lookupUser(${e.userId}) failed: ${errMessage(err)}`);
        out.set(e.userId, null);
      }
    }
    return out;
  }
}

/** Purges expired rows 60 s after start, then every 24 h. */
export class Retention {
  private timer?: NodeJS.Timeout;
  private stopped = false;

  constructor(
    private readonly store: Store,
    private readonly tables: [Table, number][],
    private readonly logger: Logger,
  ) {}

  get needed(): boolean {
    return this.tables.some(([, days]) => days > 0);
  }

  start(): void {
    const run = async () => {
      for (const [table, days] of this.tables) if (days > 0) await this.purge(table, days);
      if (!this.stopped) {
        this.timer = setTimeout(run, 24 * 3600 * 1000);
        this.timer.unref();
      }
    };
    this.timer = setTimeout(run, 60_000);
    this.timer.unref();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearTimeout(this.timer);
  }

  /** Deletes in chunks so no statement runs long or hits D1's limits. */
  async purge(table: Table, days: number): Promise<number> {
    const cutoff = formatTs(Date.now() - days * 86_400_000);
    const sql = purgeSQL(this.store.dialect, table);
    let total = 0;
    for (let i = 0; i < 200 && !this.stopped; i++) {
      let n: number;
      try {
        n = await this.store.execChanges(sql, [cutoff]);
      } catch (e) {
        this.logger.error(`visionary: retention purge of ${table} failed after ${total} rows: ${errMessage(e)}`);
        return total;
      }
      total += n;
      if (n < PURGE_CHUNK) break;
    }
    if (total > 0) this.logger.info(`visionary: retention purge of ${table} deleted ${total} rows older than ${cutoff}`);
    return total;
  }
}
