import { AsyncLocalStorage } from "node:async_hooks";
import { randomFillSync } from "node:crypto";

/** https://www.w3.org/TR/trace-context/ */
export interface SpanContext {
  traceId: string;
  spanId: string;
  /** Trace-flags in hex, "01" is sampled */
  flags: string;
  /** The caller's tracestate, passed on unchanged */
  state: string;
}

const spans = new AsyncLocalStorage<SpanContext>();

export function currentSpan(): SpanContext | undefined {
  return spans.getStore();
}

export function runInSpan<T>(sc: SpanContext, fn: () => T): T {
  return spans.run(sc, fn);
}

const HEX = /^[0-9a-f]+$/;
const ZERO = /^0+$/;

export function parseTraceparent(h: string | undefined): SpanContext | undefined {
  const parts = (h ?? "").trim().split("-");
  if (parts.length < 4) return undefined;
  const [version, traceId, spanId, flags] = parts as [string, string, string, string];
  if (version.length !== 2 || version === "ff" || traceId.length !== 32 || spanId.length !== 16 || flags.length !== 2) {
    return undefined;
  }
  // Version 00 has exactly four fields, later versions may add more
  if (version === "00" && parts.length !== 4) return undefined;
  if (![version, traceId, spanId, flags].every((p) => HEX.test(p))) return undefined;
  if (ZERO.test(traceId) || ZERO.test(spanId)) return undefined;
  return { traceId, spanId, flags, state: "" };
}

export function traceparent(sc: SpanContext): string {
  return `00-${sc.traceId}-${sc.spanId}-${sc.flags || "01"}`;
}

// A pooled random buffer gives crypto ids without a syscall each
const pool = Buffer.alloc(4096);
let poolPos = pool.length;

function randomHex(bytes: number): string {
  for (;;) {
    if (poolPos + bytes > pool.length) {
      randomFillSync(pool);
      poolPos = 0;
    }
    const id = pool.toString("hex", poolPos, poolPos + bytes);
    poolPos += bytes;
    if (!ZERO.test(id)) return id;
  }
}

export function newTraceId(): string {
  return randomHex(16);
}

export function newSpanId(): string {
  return randomHex(8);
}

/** `parent` is the caller's span id, or "" */
export function requestSpan(traceparentHeader: string, tracestate: string): { span: SpanContext; parent: string } {
  const incoming = parseTraceparent(traceparentHeader);
  if (incoming) {
    return { span: { ...incoming, spanId: newSpanId(), state: tracestate }, parent: incoming.spanId };
  }
  return { span: { traceId: newTraceId(), spanId: newSpanId(), flags: "01", state: "" }, parent: "" };
}
