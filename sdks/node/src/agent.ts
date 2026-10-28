import { hostname } from "node:os";
import { cwd } from "node:process";
import type { Config, Options } from "./config.js";
import { resolveConfig } from "./config.js";
import type { RequestState } from "./context.js";
import { currentRequest } from "./context.js";
import type { Entry } from "./entry.js";
import type { Logger } from "./log.js";
import { consoleLogger, errMessage } from "./log.js";
import { LogPipeline, activeLogs, logValues, setActiveLogs } from "./logs/pipeline.js";
import { parseLevel, secretKeys } from "./logs/rules.js";
import { openStore } from "./store/open.js";
import type { Table, Value } from "./store/sql.js";
import { insertSQL } from "./store/sql.js";
import type { Schema, Store } from "./store/store.js";
import { createSchema } from "./store/store.js";
import { HttpCounters, Recorder } from "./system/recorder.js";
import { ErrorLimiter, RUNTIME, describe, errorStatus, fingerprint } from "./telemetry/errors.js";
import { ExplainWorker } from "./telemetry/explain.js";
import { instrumentHttp } from "./telemetry/http.js";
import { Profiler } from "./telemetry/profiler.js";
import type { SpanRow } from "./telemetry/spans.js";
import { setSpanSink, spanValues, visibility } from "./telemetry/spans.js";
import { formatTs } from "./util.js";
import { Retention, Writer } from "./writer.js";

export const SDK_VERSION: string = typeof __SDK_VERSION__ === "string" ? __SDK_VERSION__ : "0.0.0";

/**
 * Runs request logging, system metrics, spans, errors and app logs for one process.
 * With no database configured it logs one warning and every method is a
 * no-op; it never throws into the application once constructed.
 */
export class Agent {
  readonly config: Config;
  readonly http = new HttpCounters();
  readonly instance: string;
  readonly logger: Logger;
  private store?: Store;
  private writer?: Writer;
  private recorder?: Recorder;
  private retention?: Retention;
  private profiler?: Profiler;
  private explainer?: ExplainWorker;
  /** Own writer, so a flood of log lines never crowds out request rows. */
  private logs?: LogPipeline;
  private logWriter?: Writer;
  private readonly schemas = new Map<Schema, Promise<boolean> | true>();
  private readonly schemaReported = new Set<Schema>();
  private readonly limiter = new ErrorLimiter();
  private started = false;
  private closing?: Promise<void>;
  private readonly onMonitor = (err: unknown) => this.recordUncaught(err);
  private readonly onRejection = (reason: unknown) => this.recordError(reason, false);

  constructor(options: Options = {}) {
    this.config = resolveConfig(options);
    this.logger = options.logger ?? consoleLogger;
    this.instance = this.config.instance || hostname();
    try {
      this.store = openStore(this.config, this.logger);
    } catch (e) {
      this.logger.warn(`${errMessage(e)}; collection is off`);
      return;
    }
    if (!this.store) {
      this.logger.warn(
        "visionary: no database configured (set VISIONARY_DATABASE_URL, pass `db`, or set the D1_* variables); collection is off",
      );
      return;
    }
    const cfg = this.config;
    if (cfg.logRequests || cfg.spans || cfg.errors) {
      this.writer = new Writer(cfg, this.store, () => this.writerReady(), this.logger);
    }
    if (cfg.systemMetrics && cfg.systemInterval > 0) {
      this.recorder = new Recorder(
        {
          intervalS: cfg.systemInterval,
          instance: this.instance,
          service: cfg.service,
          version: cfg.version,
          diskPath: cfg.diskPath || cwd(),
          dbStats: cfg.dbStats,
        },
        this.store,
        () => this.ready("system"),
        this.http,
        this.logger,
      );
    }
    if (cfg.logs) {
      const store = this.store;
      const ident = { service: cfg.service, instance: this.instance, version: cfg.version };
      const secrets = secretKeys(cfg.redactFields);
      this.logWriter = new Writer(cfg, store, () => this.ready("logs"), this.logger, {
        queueSize: 10_000,
        logRow: (r) => logValues(r, ident, secrets),
      });
      this.logs = new LogPipeline(
        cfg.service,
        parseLevel(cfg.logLevel),
        () => store.rows(`SELECT name, value FROM visionary_settings WHERE name LIKE 'logs.%'`, []),
        (log) => this.logWriter?.push({ log }),
      );
    }
    const tables: [Table, number][] = [];
    if (this.writer) tables.push(["api_logs", cfg.retentionDays], ["spans", cfg.retentionDays], ["errors", cfg.retentionDays]);
    if (this.writer && cfg.profile) tables.push(["profiles", cfg.retentionDays]);
    if (this.recorder) tables.push(["system_metrics", cfg.systemRetentionDays]);
    if (this.logWriter) tables.push(["app_logs", cfg.retentionDays]);
    this.retention = new Retention(this.store, tables, this.logger);
  }

  /** True when anything is collected (a database and at least one feature). */
  get active(): boolean {
    return !!this.writer || !!this.recorder || !!this.logWriter;
  }

  /** Whether a request to path gets an api_logs row. */
  shouldLog(path: string): boolean {
    return !!this.writer && this.config.logRequests && path.startsWith(this.config.pathPrefix);
  }

  /** Launches the background work. Tables are created asynchronously, so a
   * slow or unreachable database never delays startup. Safe to call twice. */
  start(): this {
    if (this.started || this.closing || !this.active) return this;
    this.started = true;
    const cfg = this.config;
    if (this.writer) {
      this.writer.start();
      void this.writerReady();
      if (cfg.logRequests) {
        this.logger.info(
          `visionary: logging requests (path_prefix=${JSON.stringify(cfg.pathPrefix)}, bodies=${cfg.captureBodies}, retention_days=${cfg.retentionDays})`,
        );
      }
      if (cfg.spans) {
        setSpanSink({
          thresholdMs: cfg.spanThresholdMs,
          max: cfg.spanMax,
          standalone: (row) => this.pushSpan(row, null),
          late: (row, requestId) => this.pushSpan(row, requestId),
        });
        if (cfg.instrumentHttp) instrumentHttp();
      }
      if (cfg.errors && cfg.captureUncaught) {
        process.on("uncaughtExceptionMonitor", this.onMonitor);
        // Without an app listener an unhandled rejection becomes an uncaught
        // exception and reaches the monitor; adding our own listener would
        // stop the crash, so only join one the app already has.
        if (process.listenerCount("unhandledRejection") > 0) process.on("unhandledRejection", this.onRejection);
      }
      if (cfg.version) this.writeRelease();
      if (cfg.profile && this.store) {
        const store = this.store;
        this.profiler = new Profiler(
          { service: cfg.service, instance: this.instance, version: cfg.version },
          () => store.rows(`SELECT name, value FROM visionary_settings WHERE name LIKE 'profile.%'`, []),
          (args) => this.writer?.push({ table: "profiles", args }),
          this.logger,
        );
        this.profiler.start();
      }
    }
    if (this.logWriter && this.logs) {
      this.logWriter.start();
      void this.ready("logs");
      this.logs.start();
      setActiveLogs(this.logs);
    }
    if (this.store && cfg.explain.length > 0) {
      this.explainer = new ExplainWorker(cfg.explain, this.store, this.instance, this.logger);
      this.explainer.start();
    }
    if (this.recorder) {
      void this.ready("system");
      this.recorder.start();
      this.logger.info(`visionary: recording system metrics every ${cfg.systemInterval}s (instance ${this.instance})`);
    }
    if (this.retention?.needed) this.retention.start();
    return this;
  }

  /** Flushes queued rows and buffered samples, waiting at most timeoutMs. */
  close(timeoutMs = 10_000): Promise<void> {
    if (this.closing) return this.closing;
    this.closing = (async () => {
      process.off("uncaughtExceptionMonitor", this.onMonitor);
      process.off("unhandledRejection", this.onRejection);
      setSpanSink(undefined);
      this.retention?.stop();
      this.profiler?.stop();
      this.explainer?.stop();
      this.logs?.stop();
      if (this.logs && activeLogs() === this.logs) setActiveLogs(undefined);
      if (this.started) {
        const deadline = Date.now() + timeoutMs;
        await Promise.all([this.writer?.close(timeoutMs), this.logWriter?.close(timeoutMs), this.recorder?.close()]);
        if (Date.now() > deadline) this.logger.warn("visionary: close took longer than its timeout");
      }
      await this.store?.close().catch(() => undefined);
    })();
    return this.closing;
  }

  /** Creates a schema once; a failure is retried on the next write. */
  ready(schema: Schema): Promise<boolean> {
    const s = this.schemas.get(schema);
    if (s === true) return Promise.resolve(true);
    if (s) return s;
    const p = createSchema(this.store!, schema).then(
      () => {
        this.schemas.set(schema, true);
        return true;
      },
      (e) => {
        this.schemas.delete(schema);
        if (!this.schemaReported.has(schema)) {
          this.schemaReported.add(schema);
          this.logger.error(`visionary: create ${schema} tables failed (will retry): ${errMessage(e)}`);
        }
        return false;
      },
    );
    this.schemas.set(schema, p);
    return p;
  }

  private async writerReady(): Promise<boolean> {
    const [requests, telemetry] = await Promise.all([
      this.config.logRequests ? this.ready("requests") : true,
      this.ready("telemetry"),
    ]);
    return requests && telemetry;
  }

  finishRequest(state: RequestState, entry: Entry): void {
    this.logs?.finish(state, entry.route, entry.status >= 500 || state.failed, entry.durationMs);
    if (!this.writer) return;
    const cfg = this.config;
    const seen = visibility();
    if (cfg.spans) {
      if (seen.db) {
        entry.dbCount = state.dbCount;
        entry.dbMs = state.dbMs;
      }
      if (seen.http) {
        entry.httpCount = state.httpCount;
        entry.httpMs = state.httpMs;
      }
    }
    this.writer.push({ entry });
    if (cfg.spans && state.spans.length > 0) {
      for (const row of state.spans.sort((a, b) => a.slot - b.slot)) this.pushSpan(row, state.requestId);
    }
  }

  private pushSpan(row: SpanRow, requestId: string | null): void {
    this.writer?.push({ table: "spans", args: spanValues(row, requestId, this.config.service, this.instance) });
  }

  /** Records an error; inside a request it links to it. */
  recordError(err: unknown, handled: boolean, state: RequestState | undefined = currentRequest()): void {
    if (state && !state.done) state.failed = true;
    const row = this.errorRow(err, handled, state);
    if (row) this.writer?.push({ table: "errors", args: row });
  }

  /** Records an exception that reached the framework (not a 4xx it asked for). */
  recordUnhandled(err: unknown, state: RequestState | undefined): void {
    const status = errorStatus(err);
    if (status !== undefined && status < 500) return;
    this.recordError(err, false, state);
  }

  private errorRow(err: unknown, handled: boolean, state: RequestState | undefined): Value[] | undefined {
    if (!this.writer || !this.config.errors) return undefined;
    try {
      state?.resolveUser();
      const d = describe(err);
      const fp = fingerprint(d.type, d.stack, d.message);
      if (!this.limiter.allow(fp)) return undefined;
      const cfg = this.config;
      return [
        formatTs(Date.now()),
        cfg.service || null,
        this.instance,
        cfg.version || null,
        state?.requestId ?? null,
        state?.method ?? null,
        state ? state.resolveRoute() || null : null,
        state?.path ?? null,
        state?.userId || null,
        d.type.slice(0, 255),
        d.message.slice(0, 1000) || null,
        truncateBytes(d.stack, 16 * 1024) || null,
        fp,
        handled ? 1 : 0,
        RUNTIME,
      ];
    } catch {
      return undefined;
    }
  }

  /** The process is about to die: write the row now. SQLite stores write
   * synchronously, so the row survives; others get a best-effort send. */
  private recordUncaught(err: unknown): void {
    const row = this.errorRow(err, false, currentRequest());
    if (!row || !this.store) return;
    try {
      this.store.batch([{ sql: insertSQL("errors", this.store.stringParams), args: row }]).catch(() => undefined);
    } catch {
      // nothing more can be done
    }
  }

  private writeRelease(): void {
    const cfg = this.config;
    this.writer?.push({
      table: "releases",
      args: [formatTs(Date.now()), cfg.service || null, this.instance, cfg.version, "node", SDK_VERSION, RUNTIME],
    });
  }
}

function truncateBytes(s: string, limit: number): string {
  if (Buffer.byteLength(s) <= limit) return s;
  return Buffer.from(s).subarray(0, limit).toString("utf8").replace(/�$/, "");
}
