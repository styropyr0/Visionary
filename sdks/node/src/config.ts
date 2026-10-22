import type { DbStats } from "./system/recorder.js";
import type { Explainer } from "./telemetry/explain.js";
import type { Logger } from "./log.js";

/** Who made a request. Only `id` is required. */
export interface User {
  id: string | number;
  handle?: string;
  role?: string;
}

export type Dialect = "sqlite" | "postgres" | "mysql";

/**
 * Options for `init()`. Every field is optional; anything left out comes from
 * the `VISIONARY_*` environment variables (see the README).
 *
 * Storage is chosen in this order: `db` (a pool or connection the app already
 * has), `databaseUrl`, then the `d1` credentials.
 */
export interface Options {
  /** A `pg.Pool`, a `mysql2` pool (callback or promise API), a
   * `better-sqlite3` Database or a `node:sqlite` DatabaseSync. The app keeps
   * owning it; `close()` does not end it. */
  db?: unknown;
  /** The SQL flavour of `db`; detected when left out. */
  dialect?: Dialect;
  /** postgres://, mysql://, sqlite:///abs/path.db, sqlite://relative.db or d1:// */
  databaseUrl?: string;
  d1?: { accountId?: string; databaseId?: string; apiToken?: string; apiBase?: string };
  /** Labels this process's rows (default: the hostname). */
  instance?: string;
  service?: string;
  /** The release; defaults to the platform's commit variable. */
  version?: string;
  logger?: Logger;

  logRequests?: boolean;
  pathPrefix?: string;
  captureBodies?: boolean;
  maxBodyBytes?: number;
  /** Days of api_logs, spans, errors and profiles the SDK keeps (VISIONARY_LOG_RETENTION_DAYS).
   * 0, the default, deletes nothing: the Visionary server applies Settings → Retention. */
  retentionDays?: number;
  redactHeaders?: string[];
  redactFields?: string[];
  redactRequestFields?: string[];
  errorCodeField?: string;
  errorKeyField?: string;
  /** Fills in handle and role for a user id, on the writer, once per distinct
   * user per batch. Keep it cheap (a cache). */
  lookupUser?: (id: string) => LookupResult | Promise<LookupResult>;

  systemMetrics?: boolean;
  /** Seconds between samples (default 15). */
  systemInterval?: number;
  /** Days of system_metrics the SDK keeps (VISIONARY_SYSTEM_RETENTION_DAYS); 0, the default, deletes nothing. */
  systemRetentionDays?: number;
  /** Pool stats for the db_* columns; see `pgPoolStats`. */
  dbStats?: () => DbStats | undefined;
  /** Filesystem reported in the disk_* columns (default: the working directory). */
  diskPath?: string;

  spans?: boolean;
  spanThresholdMs?: number;
  spanMax?: number;
  /** Record outbound fetch/undici and http/https calls (default true). */
  instrumentHttp?: boolean;
  errors?: boolean;
  /** Record uncaught exceptions with `uncaughtExceptionMonitor` (default true). */
  captureUncaught?: boolean;
  /** Databases the dashboard may ask query plans of (see explainWith). */
  explain?: Explainer[];
  /**
   * Take CPU profiles when the dashboard turns them on (Settings →
   * Profiling). Default true; false never profiles, whatever the dashboard says.
   */
  profile?: boolean;
}

export type LookupResult = { handle?: string; role?: string } | null | undefined;

export interface Config {
  db: unknown;
  dialect: Dialect | "";
  databaseUrl: string;
  d1: { accountId: string; databaseId: string; apiToken: string; apiBase: string };
  instance: string;
  service: string;
  version: string;
  logRequests: boolean;
  pathPrefix: string;
  captureBodies: boolean;
  maxBodyBytes: number;
  retentionDays: number;
  redactHeaders: string[];
  redactFields: string[];
  redactRequestFields: string[];
  errorCodeField: string;
  errorKeyField: string;
  lookupUser?: Options["lookupUser"];
  systemMetrics: boolean;
  systemInterval: number;
  systemRetentionDays: number;
  dbStats?: Options["dbStats"];
  diskPath: string;
  spans: boolean;
  spanThresholdMs: number;
  spanMax: number;
  instrumentHttp: boolean;
  errors: boolean;
  captureUncaught: boolean;
  explain: Explainer[];
  profile: boolean;
}

type Env = Record<string, string | undefined>;

/** The configuration from the environment, with `opts` taking precedence. */
export function resolveConfig(opts: Options = {}, env: Env = process.env): Config {
  const s = (key: string, def = ""): string => (env[key] ?? "").trim() || def;
  return {
    db: opts.db,
    dialect: opts.dialect ?? "",
    databaseUrl: opts.databaseUrl ?? s("VISIONARY_DATABASE_URL"),
    d1: {
      accountId: opts.d1?.accountId ?? s("D1_ACCOUNT_ID"),
      databaseId: opts.d1?.databaseId ?? s("D1_DATABASE_ID"),
      apiToken: opts.d1?.apiToken ?? s("D1_API_TOKEN"),
      apiBase: opts.d1?.apiBase ?? s("D1_API_BASE"),
    },
    instance: opts.instance ?? s("VISIONARY_INSTANCE"),
    service: opts.service ?? s("VISIONARY_SERVICE"),
    version: opts.version ?? releaseFromEnv(env),
    logRequests: opts.logRequests ?? parseBool(s("VISIONARY_LOG_REQUESTS"), true),
    pathPrefix: opts.pathPrefix ?? s("VISIONARY_LOG_PATH_PREFIX"),
    captureBodies: opts.captureBodies ?? parseBool(s("VISIONARY_LOG_BODIES"), true),
    maxBodyBytes: positive(opts.maxBodyBytes ?? parseInt0(s("VISIONARY_LOG_MAX_BODY_BYTES"), 8192), 8192),
    retentionDays: opts.retentionDays ?? parseInt0(s("VISIONARY_LOG_RETENTION_DAYS"), 0),
    redactHeaders: opts.redactHeaders ?? parseList(s("VISIONARY_LOG_REDACT_HEADERS")),
    redactFields: opts.redactFields ?? parseList(s("VISIONARY_LOG_REDACT_FIELDS")),
    redactRequestFields: opts.redactRequestFields ?? parseList(s("VISIONARY_LOG_REDACT_REQUEST_FIELDS")),
    errorCodeField: opts.errorCodeField ?? s("VISIONARY_LOG_ERROR_CODE_FIELD", "error.code"),
    errorKeyField: opts.errorKeyField ?? s("VISIONARY_LOG_ERROR_KEY_FIELD", "error.key"),
    lookupUser: opts.lookupUser,
    systemMetrics: opts.systemMetrics ?? parseBool(s("VISIONARY_SYSTEM_METRICS"), true),
    systemInterval: opts.systemInterval ?? parseDuration(s("VISIONARY_SYSTEM_INTERVAL"), 15),
    systemRetentionDays: opts.systemRetentionDays ?? parseInt0(s("VISIONARY_SYSTEM_RETENTION_DAYS"), 0),
    dbStats: opts.dbStats,
    diskPath: opts.diskPath ?? "",
    spans: opts.spans ?? parseBool(s("VISIONARY_SPANS"), true),
    spanThresholdMs: opts.spanThresholdMs ?? parseInt0(s("VISIONARY_SPAN_THRESHOLD_MS"), 100),
    spanMax: opts.spanMax ?? parseInt0(s("VISIONARY_SPAN_MAX"), 100),
    instrumentHttp: opts.instrumentHttp ?? true,
    errors: opts.errors ?? parseBool(s("VISIONARY_ERRORS"), true),
    captureUncaught: opts.captureUncaught ?? true,
    explain: parseBool(s("VISIONARY_EXPLAIN"), true) ? (opts.explain ?? []) : [],
    profile: opts.profile ?? parseBool(s("VISIONARY_PROFILE"), true),
  };
}

const COMMIT_VARS = [
  "RAILWAY_GIT_COMMIT_SHA",
  "RENDER_GIT_COMMIT",
  "VERCEL_GIT_COMMIT_SHA",
  "HEROKU_SLUG_COMMIT",
  "SOURCE_VERSION",
  "GIT_COMMIT",
  "GITHUB_SHA",
  "K_REVISION",
];

/** VISIONARY_RELEASE, else the platform's commit variable; commit hashes are
 * shortened to 12 characters. */
export function releaseFromEnv(env: Env = process.env): string {
  for (const key of ["VISIONARY_RELEASE", ...COMMIT_VARS]) {
    const v = (env[key] ?? "").trim();
    if (v) return /^[0-9a-f]{13,}$/i.test(v) ? v.slice(0, 12) : v;
  }
  return "";
}

export function parseBool(value: string, def: boolean): boolean {
  const v = value.trim().toLowerCase();
  if (v === "true" || v === "1" || v === "yes") return true;
  if (v === "false" || v === "0" || v === "no") return false;
  return def;
}

function parseInt0(value: string, def: number): number {
  if (!/^\d+$/.test(value.trim())) return def;
  return Number(value.trim());
}

function positive(n: number, def: number): number {
  return n > 0 ? n : def;
}

export function parseList(value: string): string[] {
  return value
    .split(",")
    .map((p) => p.trim())
    .filter(Boolean);
}

const UNITS: Record<string, number> = { ns: 1e-9, us: 1e-6, "µs": 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };

/** Seconds from "15", "15s", "500ms" or "1m30s" (Go duration syntax). */
export function parseDuration(value: string, def: number): number {
  const v = value.trim();
  if (!v) return def;
  if (/^\d+$/.test(v)) return Number(v);
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/gy;
  let total = 0;
  let pos = 0;
  for (let m = re.exec(v); m; m = re.exec(v)) {
    total += Number(m[1]) * UNITS[m[2]!]!;
    pos = re.lastIndex;
  }
  return pos === v.length && pos > 0 ? total : def;
}
