import { Writable } from "node:stream";
import { format } from "node:util";
import type { LogRecord } from "./pipeline.js";
import { activeLogs, logRecord, takeAttrs } from "./pipeline.js";
import type { AttrValue } from "./rules.js";
import { DEBUG, ERROR, FATAL, INFO, TRACE, WARN, parseLevel, stripLevelWord, textLevel } from "./rules.js";

// Copies console, pino and winston lines to Visionary without changing what they print.
// Until init() runs, or with VISIONARY_LOGS=false, lines are only printed.

/** Set while capturing, so anything the capture itself logs isn't captured again. */
let capturing = false;

function capture(make: () => LogRecord | undefined): void {
  const logs = activeLogs();
  if (!logs || capturing || !logs.current().enabled) return;
  capturing = true;
  try {
    const r = make();
    if (r) logs.capture(r);
  } catch {
    // a line that cannot be read is not worth failing the caller for
  } finally {
    capturing = false;
  }
}

type ConsoleMethod = "debug" | "log" | "info" | "warn" | "error" | "trace";

const CONSOLE_LEVELS: Record<ConsoleMethod, number> = { debug: DEBUG, log: 0, info: 0, warn: WARN, error: ERROR, trace: TRACE };

let restoreConsole: (() => void) | undefined;

/**
 * Copies console calls to Visionary; they still print as before. console.log/info lines
 * use a level stated at their start ("ERROR ...", "level=debug"), else info.
 * Returns a function that restores the console.
 */
export function captureConsole(): () => void {
  if (restoreConsole) return restoreConsole;
  const saved = new Map<ConsoleMethod, (...args: unknown[]) => void>();
  for (const method of Object.keys(CONSOLE_LEVELS) as ConsoleMethod[]) {
    const prev = console[method] as (...args: unknown[]) => void;
    saved.set(method, prev);
    console[method] = function visionaryConsole(this: unknown, ...args: unknown[]) {
      prev.apply(this ?? console, args);
      capture(() => consoleRecord(method, args));
    };
  }
  const restore = () => {
    if (restoreConsole !== restore) return;
    for (const [method, prev] of saved) console[method] = prev;
    restoreConsole = undefined;
  };
  restoreConsole = restore;
  return restore;
}

function consoleRecord(method: ConsoleMethod, args: unknown[]): LogRecord | undefined {
  const text = format(...args).replace(/[\r\n]+$/, "");
  if (text.trim() === "") return undefined;
  let sev = CONSOLE_LEVELS[method];
  let message = text;
  if (sev === 0) {
    sev = textLevel(text);
    if (sev > 0) message = stripLevelWord(text, sev);
    else sev = INFO;
  }
  return logRecord(sev, message);
}

function pinoSev(level: unknown): number {
  if (typeof level === "string") return parseLevel(level) || INFO;
  if (typeof level !== "number") return INFO;
  if (level < 20) return TRACE;
  if (level < 30) return DEBUG;
  if (level < 40) return INFO;
  if (level < 50) return WARN;
  if (level < 60) return ERROR;
  return FATAL;
}

const PINO_BASE = new Set(["level", "time", "msg", "pid", "hostname", "v"]);

export interface PinoStream {
  write(line: string): boolean;
}

/**
 * A pino destination that copies each line to Visionary and passes it on to `next`
 * (stdout by default; null to only capture). Give pino a lower level than you print at
 * (e.g. pino.multistream with `null`) so failed requests keep their debug lines.
 *
 *     const log = pino({ level: "debug" }, visionary.pinoStream());
 */
export function pinoStream(next: { write(chunk: string): unknown } | null = process.stdout): PinoStream {
  return {
    write(chunk: string): boolean {
      next?.write(chunk);
      for (const line of String(chunk).split("\n")) {
        if (line.trim()) capture(() => pinoRecord(line));
      }
      return true;
    },
  };
}

function pinoRecord(line: string): LogRecord | undefined {
  const doc = JSON.parse(line) as Record<string, unknown>;
  if (!doc || typeof doc !== "object") return undefined;
  const msg = typeof doc.msg === "string" ? doc.msg : typeof doc.message === "string" ? doc.message : "";
  const r = logRecord(pinoSev(doc.level), msg, lineTime(doc.time));
  const attrs: Record<string, AttrValue> = {};
  for (const [k, v] of Object.entries(doc)) {
    if (PINO_BASE.has(k) || (k === "message" && v === msg)) continue;
    flatten(attrs, k, v, 0);
  }
  if (typeof attrs.name === "string" && attrs.logger === undefined) {
    attrs.logger = attrs.name;
    delete attrs.name;
  }
  takeAttrs(r, attrs);
  return r;
}

function lineTime(v: unknown): number {
  if (typeof v === "number" && Number.isFinite(v)) return v;
  if (typeof v === "string") {
    const t = Date.parse(v);
    if (!Number.isNaN(t)) return t;
  }
  return Date.now();
}

function flatten(out: Record<string, AttrValue>, key: string, v: unknown, depth: number): void {
  if (v === undefined || typeof v === "function") return;
  if (typeof v === "string" || typeof v === "boolean") out[key] = v;
  else if (typeof v === "number") out[key] = Number.isFinite(v) ? v : String(v);
  else if (v instanceof Date) out[key] = Number.isNaN(v.getTime()) ? String(v) : v.toISOString();
  else if (v instanceof Error) out[key] = v.message;
  else if (v === null || typeof v !== "object") out[key] = String(v);
  else if (Array.isArray(v) || Buffer.isBuffer(v) || depth >= 8) out[key] = json(v);
  else for (const [k, x] of Object.entries(v)) flatten(out, key + "." + k, x, depth + 1);
}

function json(v: unknown): string {
  try {
    return JSON.stringify(v) ?? String(v);
  } catch {
    return String(v);
  }
}

function winstonSev(level: unknown): number {
  const name = String(level ?? "").toLowerCase();
  if (name === "silly") return TRACE;
  if (name === "http") return INFO;
  return parseLevel(name) || INFO;
}

const WINSTON_BASE = new Set(["level", "message", "timestamp"]);
const LEVEL = Symbol.for("level");

/**
 * A winston transport that copies each line to Visionary. It prints nothing, so keep your
 * Console transport.
 *
 *     transports: [new winston.transports.Console({ level: "info" }), visionary.winstonTransport()]
 */
export class VisionaryTransport extends Writable {
  constructor() {
    super({ objectMode: true });
  }

  log(info: Record<string | symbol, unknown>, callback?: () => void): void {
    capture(() => winstonRecord(info));
    callback?.();
  }

  override _write(info: Record<string | symbol, unknown>, _enc: BufferEncoding, callback: (err?: Error | null) => void): void {
    this.log(info);
    callback();
  }
}

export function winstonTransport(): VisionaryTransport {
  return new VisionaryTransport();
}

function winstonRecord(info: Record<string | symbol, unknown>): LogRecord | undefined {
  if (!info || typeof info !== "object") return undefined;
  const m = info.message;
  const message = typeof m === "string" ? m : m instanceof Error ? m.message : m === undefined ? "" : json(m);
  const r = logRecord(winstonSev(info[LEVEL] ?? info.level), message, lineTime(info.timestamp));
  const attrs: Record<string, AttrValue> = {};
  for (const [k, v] of Object.entries(info)) {
    if (!WINSTON_BASE.has(k)) flatten(attrs, k, v, 0);
  }
  takeAttrs(r, attrs);
  return r;
}
