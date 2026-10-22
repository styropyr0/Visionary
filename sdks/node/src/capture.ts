import { randomBytes } from "node:crypto";
import type { IncomingMessage, ServerResponse } from "node:http";
import type { Agent } from "./agent.js";
import { RequestState, requestTally, runInRequest } from "./context.js";

const TRACKED = Symbol.for("visionary.request");

const UNCAPTURABLE = [
  "multipart/",
  "image/",
  "video/",
  "audio/",
  "application/octet-stream",
  "application/zip",
  "application/pdf",
];

/** Binary and multipart payloads are useless as text and costly to store. */
export function capturable(contentType: string): boolean {
  const ct = contentType.trim().toLowerCase();
  return !UNCAPTURABLE.some((p) => ct.startsWith(p));
}

/** Counts every byte that passes and keeps the first `limit` of them. */
class BodyCapture {
  private chunks: Buffer[] = [];
  private kept = 0;
  size = 0;
  truncated = false;

  constructor(
    public enabled: boolean,
    private readonly limit: number,
  ) {}

  feed(chunk: unknown, encoding?: unknown): void {
    if (chunk === null || chunk === undefined || typeof chunk === "function") return;
    const enc = typeof encoding === "string" ? (encoding as BufferEncoding) : "utf8";
    let n: number;
    if (typeof chunk === "string") n = Buffer.byteLength(chunk, enc);
    else if (chunk instanceof Uint8Array) n = chunk.byteLength;
    else return;
    if (n === 0) return;
    this.size += n;
    if (!this.enabled) return;
    const room = this.limit - this.kept;
    if (room <= 0) {
      this.truncated = true;
      return;
    }
    let buf = typeof chunk === "string" ? Buffer.from(chunk, enc) : Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength);
    if (buf.length > room) {
      buf = buf.subarray(0, room);
      this.truncated = true;
    }
    // Copy: the caller may reuse its buffer after write() returns.
    this.chunks.push(Buffer.from(buf));
    this.kept += buf.length;
  }

  data(): Buffer {
    return this.chunks.length === 1 ? this.chunks[0]! : Buffer.concat(this.chunks);
  }
}

function header(v: string | string[] | number | undefined): string {
  if (v === undefined) return "";
  return Array.isArray(v) ? v.join(", ") : String(v);
}

function contentLength(v: string | string[] | number | undefined): number {
  const n = Number(header(v));
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0;
}

/** The first X-Forwarded-For hop, as most deployments sit behind a proxy;
 * informational only. */
function clientIp(req: IncomingMessage): string {
  const fwd = header(req.headers["x-forwarded-for"]);
  if (fwd) return fwd.split(",")[0]!.trim();
  const addr = req.socket?.remoteAddress ?? "";
  return addr.startsWith("::ffff:") ? addr.slice(7) : addr;
}

/** The request being tracked on this IncomingMessage, if any. */
export function trackedState(req: unknown): RequestState | undefined {
  return (req as Record<symbol, RequestState | undefined> | undefined)?.[TRACKED];
}

/**
 * Starts tracking a request: throughput counters always, and when the path is
 * logged, a RequestState plus copies of the bodies (up to the cap) taken as
 * they stream. The row is queued when the response finishes. Request events
 * run inside the state, so code after body parsing still sees setUser.
 *
 * `url` is the full original URL (Express rewrites req.url in sub-routers).
 */
export function observeRequest(
  agent: Agent,
  req: IncomingMessage,
  res: ServerResponse,
  routeOf: () => string | undefined,
  url: string = req.url ?? "/",
  userOf?: () => unknown,
): RequestState | undefined {
  const marked = req as unknown as Record<symbol, unknown>;
  if (TRACKED in marked) return marked[TRACKED] as RequestState | undefined;
  marked[TRACKED] = undefined;
  if (!agent.active) return undefined;

  const counters = agent.http;
  counters.started();
  const q = url.indexOf("?");
  const path = q >= 0 ? url.slice(0, q) : url;
  const query = q >= 0 ? url.slice(q + 1) : "";

  if (!agent.shouldLog(path)) {
    let counted = false;
    const done = () => {
      if (!counted) {
        counted = true;
        counters.finished(res.statusCode);
      }
    };
    res.once("finish", done);
    res.once("close", done);
    return undefined;
  }

  const cfg = agent.config;
  const startHr = process.hrtime.bigint();
  const incoming = header(req.headers["x-request-id"]);
  const requestId = incoming && incoming.length <= 128 ? incoming : randomBytes(16).toString("hex");
  try {
    res.setHeader("X-Request-ID", requestId);
  } catch {
    // headers already sent by an earlier middleware
  }
  const state = new RequestState(requestId, Date.now(), startHr, req.method ?? "GET", path);
  state.routeOf = routeOf;
  state.userOf = userOf;
  marked[TRACKED] = state;
  requestTally.started++;

  const reqType = header(req.headers["content-type"]);
  const reqBody = new BodyCapture(cfg.captureBodies && capturable(reqType), cfg.maxBodyBytes);
  const emit = req.emit;
  req.emit = function (this: IncomingMessage, event: string | symbol, ...args: any[]) {
    if (event === "data") reqBody.feed(args[0]);
    return runInRequest(state, () => emit.call(this, event, ...args));
  } as typeof req.emit;

  const resBody = new BodyCapture(false, cfg.maxBodyBytes);
  let decided = false;
  const decide = () => {
    if (decided) return;
    decided = true;
    const encoding = header(res.getHeader("content-encoding")).toLowerCase();
    resBody.enabled =
      cfg.captureBodies && capturable(header(res.getHeader("content-type"))) && (!encoding || encoding === "identity");
  };
  const write = res.write;
  res.write = function (this: ServerResponse, chunk: any, ...rest: any[]) {
    decide();
    resBody.feed(chunk, rest[0]);
    return (write as Function).call(this, chunk, ...rest);
  } as typeof res.write;
  const end = res.end;
  res.end = function (this: ServerResponse, chunk?: any, ...rest: any[]) {
    decide();
    resBody.feed(chunk, rest[0]);
    return (end as Function).call(this, chunk, ...rest);
  } as typeof res.end;

  const finish = () => {
    if (state.done) return;
    const durationMs = Number(process.hrtime.bigint() - startHr) / 1e6;
    const status = res.statusCode;
    counters.finished(status);
    const route = state.resolveRoute();
    state.resolveUser();
    state.done = true;
    if (requestTally.busy) {
      const key = `${state.method} ${route || state.path}`;
      requestTally.busy[key] = (requestTally.busy[key] ?? 0) + durationMs;
    }
    agent.finishRequest(state, {
      requestId,
      startedAt: state.startedAt,
      method: state.method,
      path,
      route,
      query,
      status,
      durationMs,
      ip: clientIp(req),
      userAgent: header(req.headers["user-agent"]),
      userId: state.userId,
      userHandle: state.userHandle,
      userRole: state.userRole,
      headers: req.headersDistinct ?? req.headers,
      requestContentType: reqType,
      requestBody: reqBody.data(),
      requestTruncated: reqBody.truncated,
      requestSize: Math.max(contentLength(req.headers["content-length"]), reqBody.size),
      responseContentType: header(res.getHeader("content-type")),
      responseBody: resBody.data(),
      responseTruncated: resBody.truncated,
      responseSize: Math.max(contentLength(res.getHeader("content-length")), resBody.size),
      service: cfg.service,
      version: cfg.version,
      dbCount: null,
      dbMs: null,
      httpCount: null,
      httpMs: null,
    });
  };
  res.once("finish", finish);
  res.once("close", finish);
  return state;
}
