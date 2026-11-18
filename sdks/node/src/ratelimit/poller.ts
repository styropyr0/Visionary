import type { Logger } from "../log.js";
import { errMessage } from "../log.js";
import { DEFAULT_REDACT_HEADERS } from "../redact.js";
import type { Stmt, Value } from "../store/sql.js";
import { insertRows, insertSQL } from "../store/sql.js";
import type { Store } from "../store/store.js";
import { formatTs } from "../util.js";
import type { RateLimitConfig } from "./limiter.js";
import { Limiter, SETTING_CONFIG, SETTING_VERSION, parseConfig } from "./limiter.js";

const POLL_MS = 15_000;
const STATEMENTS_PER_CALL = 50;

/**
 * Keeps the rules in force from visionary_settings and writes what was
 * limited to rate_limit_events, every 15 s off the request path. Counters
 * are per process.
 */
export class RateLimits {
  readonly limiter: Limiter;
  private version = "";
  private badVersion = "";
  private writeFailed = false;
  private timer?: NodeJS.Timeout;
  private running?: Promise<void>;
  private stopped = false;

  constructor(
    private readonly store: Store,
    private readonly ident: { service: string; instance: string },
    redactHeaders: string[],
    private readonly logger: Logger,
  ) {
    this.limiter = new Limiter(ident.service);
    this.limiter.setRedactHeaders([...DEFAULT_REDACT_HEADERS, ...redactHeaders]);
  }

  start(): void {
    this.timer = setInterval(() => void this.poll(), POLL_MS);
    this.timer.unref();
    void this.poll();
  }

  /** Stops polling and writes what is left. */
  async close(): Promise<void> {
    this.stopped = true;
    if (this.timer) clearInterval(this.timer);
    await this.running;
    await this.flush();
  }

  poll(): Promise<void> {
    if (this.running || this.stopped) return this.running ?? Promise.resolve();
    this.running = (async () => {
      try {
        await this.refresh();
        this.limiter.sweep(Date.now() / 1000);
        await this.flush();
      } catch {
        // never escapes the poll
      } finally {
        this.running = undefined;
      }
    })();
    return this.running;
  }

  private async refresh(): Promise<void> {
    let rows: string[][];
    try {
      rows = await this.store.rows(`SELECT value FROM visionary_settings WHERE name = '${SETTING_VERSION}'`, []);
    } catch {
      return; // keep the rules in force
    }
    if (rows.length === 0 || rows[0]!.length === 0) {
      if (this.version !== "") this.limiter.clear();
      this.version = "";
      return;
    }
    const next = rows[0]![0]!;
    if (next === this.version) return;
    let cfg: string[][];
    try {
      cfg = await this.store.rows(`SELECT value FROM visionary_settings WHERE name = '${SETTING_CONFIG}'`, []);
    } catch {
      return;
    }
    const raw = cfg[0]?.[0] ?? "";
    let parsed: RateLimitConfig = { rules: [] };
    if (raw !== "") {
      try {
        parsed = parseConfig(raw);
      } catch (e) {
        if (this.badVersion !== next) {
          this.badVersion = next;
          this.logger.warn(`visionary: rate limit rules are not valid JSON; keeping the previous ones: ${errMessage(e)}`);
        }
        return;
      }
    }
    this.limiter.load(parsed);
    this.version = next;
  }

  async flush(): Promise<void> {
    const events = this.limiter.drain();
    if (events.length === 0) return;
    const service = this.ident.service || null;
    const nul = (s: string): Value => s || null;
    const rows: Value[][] = events.map((e) => [
      formatTs(e.createdAt),
      service,
      this.ident.instance,
      e.ruleId,
      e.ruleName,
      e.outcome,
      nul(e.keyValue),
      nul(e.ip),
      nul(e.country),
      nul(e.method),
      nul(e.path),
      nul(e.userId),
      e.hits,
    ]);
    let stmts: Stmt[];
    if (this.store.stringParams) {
      const sql = insertSQL("rate_limit_events", true);
      stmts = rows.map((args) => ({ sql, args }));
    } else {
      stmts = insertRows("rate_limit_events", rows);
    }
    try {
      for (let i = 0; i < stmts.length; i += STATEMENTS_PER_CALL) {
        await this.store.batch(stmts.slice(i, i + STATEMENTS_PER_CALL));
      }
    } catch (e) {
      if (!this.writeFailed) {
        this.writeFailed = true;
        this.logger.info(
          `visionary: could not record rate-limited requests (the Visionary server creates rate_limit_events): ${errMessage(e)}`,
        );
      }
    }
  }
}
