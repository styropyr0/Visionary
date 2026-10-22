import type { RequestState } from "../context.js";
import { currentRequest, isQuiet } from "../context.js";
import type { Value } from "../store/sql.js";
import { formatTs, msBetween, round } from "../util.js";

/** A span's detail (a statement, a URL, a GraphQL document) is cut here: long
 * enough that the statement can still be planned with EXPLAIN. */
export const MAX_DETAIL = 8000;

export type SpanKind = "db" | "http" | "cache" | "queue" | "custom" | "graphql" | "background";

/** A finished span, in SPAN_COLUMNS order minus the per-process columns. */
export interface SpanRow {
  slot: number;
  createdAt: string;
  kind: SpanKind;
  name: string;
  target: string;
  detail: string;
  offsetMs: number | null;
  durationMs: number;
  status: "ok" | "error";
  error: string;
  rowCount: number | null;
}

export interface SpanSink {
  readonly thresholdMs: number;
  readonly max: number;
  /** A span outside any request that took at least the threshold. */
  standalone(row: SpanRow): void;
  /** A span of background work that ended after its request was logged. */
  late(row: SpanRow, requestId: string): void;
}

let sink: SpanSink | undefined;
/** Set once any database instrumentation is installed: db_count/db_ms are
 * NULL on requests while the SDK cannot see database calls. */
let dbVisible = false;
let httpVisible = false;

export function setSpanSink(s: SpanSink | undefined): void {
  sink = s;
}

export function markVisible(kind: "db" | "http"): void {
  if (kind === "db") dbVisible = true;
  else httpVisible = true;
}

export function visibility(): { db: boolean; http: boolean } {
  return { db: dbVisible && !!sink, http: httpVisible && !!sink };
}

export interface SpanInit {
  kind: SpanKind;
  name: string;
  target?: string;
  detail?: string;
}

/** An in-flight span. `end` records it once; later calls do nothing. */
export class ActiveSpan {
  private readonly startHr = process.hrtime.bigint();
  private readonly startedAt = Date.now();
  private readonly state: RequestState | undefined;
  private readonly slot: number;
  private ended = false;

  constructor(private readonly init: SpanInit) {
    this.state = currentRequest();
    this.slot = this.state && !this.state.done ? this.state.nextSlot++ : -1;
  }

  end(error?: unknown, rowCount?: number | null, failed = error !== undefined && error !== null): void {
    if (this.ended || !sink) return;
    this.ended = true;
    const durationMs = round(msBetween(this.startHr, process.hrtime.bigint()), 3);
    const state = this.state;
    // Background work the request started, ending after it was logged: still
    // its call, whatever it took, up to the cap; past it only slow calls.
    const late = !!state?.done;
    if (late) {
      if (state!.late >= sink.max && durationMs < sink.thresholdMs) return;
      state!.late++;
    } else if (state) {
      if (this.init.kind === "db") {
        state.dbCount++;
        state.dbMs += durationMs;
      } else if (this.init.kind === "http") {
        state.httpCount++;
        state.httpMs += durationMs;
      }
      if (this.slot >= sink.max) return;
    } else if (durationMs < sink.thresholdMs) {
      return;
    }
    const row: SpanRow = {
      slot: this.slot,
      createdAt: formatTs(this.startedAt),
      kind: this.init.kind,
      name: this.init.name.slice(0, 255),
      target: (this.init.target ?? "").slice(0, 255),
      detail: (this.init.detail ?? "").slice(0, MAX_DETAIL),
      offsetMs: state ? round(msBetween(state.startHr, this.startHr), 3) : null,
      durationMs,
      status: failed ? "error" : "ok",
      error: failed ? errorText(error) : "",
      rowCount: rowCount ?? null,
    };
    if (late) sink.late(row, state!.requestId);
    else if (state) state.spans.push(row);
    else sink.standalone(row);
  }
}

function errorText(e: unknown): string {
  if (e === undefined || e === null || e === true) return "";
  const msg = e instanceof Error ? e.message || e.name : String(e);
  return msg.slice(0, 500);
}

/** Starts a span, or returns undefined when spans are off or the call is the
 * SDK's own. */
export function startSpan(init: SpanInit): ActiveSpan | undefined {
  if (!sink || isQuiet()) return undefined;
  return new ActiveSpan(init);
}

/** The span rows of a finished request, in start order, as typed values for
 * the spans table. */
export function spanValues(row: SpanRow, requestId: string | null, service: string, instance: string): Value[] {
  return [
    row.createdAt,
    requestId,
    service || null,
    instance,
    row.kind,
    row.name,
    row.target || null,
    row.detail || null,
    row.offsetMs,
    row.durationMs,
    row.status,
    row.error || null,
    row.rowCount,
  ];
}

/**
 * Runs background work the request does not wait for, recorded as one span of
 * kind background from start to finish. Calls made inside it stay in the
 * request's breakdown even after the response is sent. Returns the work's
 * promise; a rejection is recorded and passed on.
 *
 *     visionary.background("send welcome email", () => mailer.send(user.email));
 */
export function background<T>(name: string, fn: () => T | Promise<T>): Promise<T> {
  return Promise.resolve().then(() => span("background", name, fn));
}

/**
 * Records a span around `fn` (sync or async): a cache or queue call, or any
 * operation worth seeing on the request's timeline.
 *
 *     const user = await visionary.span("cache", "GET user", () => redis.get(key));
 */
export function span<T>(kind: SpanKind, name: string, fn: () => T, opts: { target?: string; detail?: string } = {}): T {
  const s = startSpan({ kind, name, ...opts });
  if (!s) return fn();
  let result: T;
  try {
    result = fn();
  } catch (e) {
    s.end(e);
    throw e;
  }
  if (result && typeof (result as any).then === "function") {
    (result as any).then(
      () => s.end(),
      (e: unknown) => s.end(e ?? new Error("rejected")),
    );
  } else {
    s.end();
  }
  return result;
}
