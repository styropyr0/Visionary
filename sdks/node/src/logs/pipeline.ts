import { hostname } from "node:os";
import type { RequestState } from "../context.js";
import { currentRequest, isQuiet } from "../context.js";
import type { Value } from "../store/sql.js";
import { formatTs } from "../util.js";
import type { AttrValue, LogSettings } from "./rules.js";
import {
  ERROR,
  MAX_MESSAGE,
  WARN,
  defaultLogSettings,
  dropped,
  encodeAttrs,
  floor,
  holdBelow,
  levelName,
  parseLogSettings,
  scrub,
  truncateBytes,
} from "./rules.js";

/**
 * App log pipeline. What's kept follows Settings → Logs, re-read every 30 s. Lines below
 * the level are held in their request and written only if it fails or is slow.
 */

const POLL_MS = 30_000;
const MAX_HELD = 500;

const HOST = hostname();

export interface LogRecord {
  /** epoch ms */
  at: number;
  sev: number;
  logger: string;
  message: string;
  attrs?: Record<string, AttrValue>;
  requestId: string;
  traceId: string;
  spanId: string;
  userId: string;
  sessionId: string;
}

export interface HeldLogs {
  lines: LogRecord[];
  dropped: number;
}

export interface LogIdentity {
  service: string;
  instance: string;
  version: string;
}

export function logRecord(sev: number, message: string, at = Date.now()): LogRecord {
  return { at, sev, logger: "", message, requestId: "", traceId: "", spanId: "", userId: "", sessionId: "" };
}

const COLUMN_ATTRS = ["request_id", "trace_id", "span_id", "user_id", "session_id", "logger"] as const;

export function takeAttrs(r: LogRecord, attrs: Record<string, AttrValue>): void {
  for (const k of COLUMN_ATTRS) {
    const v = attrs[k];
    if (typeof v !== "string") continue;
    if (k === "request_id") r.requestId = v;
    else if (k === "trace_id") r.traceId = v;
    else if (k === "span_id") r.spanId = v;
    else if (k === "user_id") r.userId = v;
    else if (k === "session_id") r.sessionId = v;
    else r.logger = v;
    delete attrs[k];
  }
  if (Object.keys(attrs).length > 0) r.attrs = attrs;
}

const orNull = (s: string): string | null => s || null;

export function logValues(r: LogRecord, ident: LogIdentity, secrets: Set<string>): Value[] {
  const msg = truncateBytes(scrub(r.message), MAX_MESSAGE);
  const attrs = r.attrs ? encodeAttrs(r.attrs, secrets) : "";
  return [
    formatTs(r.at),
    orNull(ident.service),
    orNull(ident.instance),
    orNull(ident.version),
    "node",
    orNull(HOST),
    null,
    levelName(r.sev),
    r.sev,
    orNull(r.logger),
    msg,
    orNull(attrs),
    orNull(r.requestId),
    orNull(r.traceId),
    orNull(r.spanId),
    orNull(r.userId),
    orNull(r.sessionId),
    null,
    null,
    null,
    Buffer.byteLength(msg) + Buffer.byteLength(attrs),
  ];
}

export function traceParentId(h: string | undefined): string {
  const parts = (h ?? "").trim().split("-");
  const id = parts[1] ?? "";
  return parts.length >= 4 && /^[0-9a-f]{32}$/.test(id) && /[^0]/.test(id) ? id : "";
}

/** Set by the agent started last; the hooks send their lines here. */
let current: LogPipeline | undefined;

export function activeLogs(): LogPipeline | undefined {
  return current;
}

export function setActiveLogs(p: LogPipeline | undefined): void {
  current = p;
}

export class LogPipeline {
  private settings: LogSettings;
  private timer?: NodeJS.Timeout;
  private polling = false;
  private stopped = false;
  private rateMinute = 0;
  private rateCount = 0;
  private rateDrop = 0;

  constructor(
    private readonly service: string,
    private readonly base: number,
    private readonly read: () => Promise<string[][]>,
    private readonly push: (r: LogRecord) => void,
  ) {
    this.settings = defaultLogSettings(base);
  }

  start(): void {
    this.timer = setInterval(() => void this.poll(), POLL_MS);
    this.timer.unref();
    void this.poll();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearInterval(this.timer);
  }

  current(): LogSettings {
    return this.settings;
  }

  apply(rows: string[][]): void {
    this.settings = parseLogSettings(rows, this.base);
  }

  async poll(): Promise<void> {
    if (this.polling || this.stopped) return;
    this.polling = true;
    try {
      this.apply(await this.read());
    } catch {
      // keep the previous settings
    } finally {
      this.polling = false;
    }
  }

  /** Never throws or blocks. */
  capture(r: LogRecord, state: RequestState | undefined = currentRequest()): void {
    const s = this.settings;
    // Skip the SDK's own messages, or a failed write's log line would queue behind it.
    if (!s.enabled || isQuiet() || r.message.startsWith("visionary: ")) return;
    if (dropped(s, this.service, r.logger, r.message, r.sev)) return;
    if (!state) {
      if (r.sev >= floor(s, this.service, "", "", r.at)) this.emit(r);
      return;
    }
    r.requestId ||= state.requestId;
    r.traceId ||= state.traceId;
    r.userId ||= state.userId;
    if (state.done) {
      // Logged after the request ended, so its route and user are known.
      if (r.sev >= floor(s, this.service, state.resolveRoute(), state.userId, r.at)) this.emit(r);
      return;
    }
    if (r.sev >= holdBelow(s, this.service, r.at)) {
      this.emit(r);
      return;
    }
    const held = (state.heldLogs ??= { lines: [], dropped: 0 });
    if (held.lines.length >= MAX_HELD) {
      held.lines.shift();
      held.dropped++;
    }
    held.lines.push(r);
  }

  finish(state: RequestState, route: string, failed: boolean, tookMs: number): void {
    const held = state.heldLogs;
    state.heldLogs = undefined;
    if (!held || held.lines.length === 0) return;
    const s = this.settings;
    const user = state.userId;
    const level = floor(s, this.service, route, user, Date.now());
    const keepAll = s.buffer && (failed || (s.slowMs > 0 && tookMs >= s.slowMs));
    for (const r of held.lines) {
      r.userId ||= user;
      if (r.sev >= level || (keepAll && r.sev >= s.bufferLevel)) this.emit(r);
    }
  }

  /** Errors and fatals get ten times the per-minute cap, so a flood of info lines never hides them. */
  private emit(r: LogRecord): void {
    const s = this.settings;
    if (s.rate > 0) {
      const minute = Math.floor(r.at / 60_000);
      if (minute !== this.rateMinute) {
        if (this.rateDrop > 0) {
          const line = logRecord(
            WARN,
            `visionary: ${this.rateDrop} log lines dropped over the cap of ${s.rate} a minute (Settings → Logs)`,
          );
          line.logger = "visionary";
          this.push(line);
        }
        this.rateMinute = minute;
        this.rateCount = 0;
        this.rateDrop = 0;
      }
      this.rateCount++;
      if (this.rateCount > s.rate && (r.sev < ERROR || this.rateCount > 10 * s.rate)) {
        this.rateDrop++;
        return;
      }
    }
    this.push(r);
  }
}
