import diagnostics from "node:diagnostics_channel";
import http from "node:http";
import https from "node:https";
import { syncBuiltinESMExports } from "node:module";
import { isQuiet } from "../context.js";
import type { ActiveSpan } from "./spans.js";
import { markVisible, startSpan } from "./spans.js";
import type { SpanContext } from "./trace.js";
import { currentSpan, traceparent } from "./trace.js";

/**
 * Outbound HTTP spans: fetch and undici through their diagnostics channels,
 * and the http/https modules (axios, got, node-fetch, SDKs) by wrapping
 * request/get. A span ends when the response headers arrive, like Go's
 * RoundTripper; status is "error" on a transport error or a 5xx. Each
 * request carries a traceparent naming its span
 */
let installed = false;

export function instrumentHttp(): void {
  if (installed) return;
  installed = true;
  subscribeUndici();
  for (const mod of [http, https]) wrapModule(mod);
  // ESM named imports (import { request } from "node:http") see the wrappers too.
  syncBuiltinESMExports();
  markVisible("http");
}

function startHttpSpan(method: string, origin: string, path: string): ActiveSpan | undefined {
  let host = origin;
  let base = origin;
  try {
    const u = new URL(origin);
    host = u.host;
    base = u.origin;
  } catch {
    // keep what we were given
  }
  const m = (method || "GET").toUpperCase();
  return startSpan({ kind: "http", name: `${m} ${host}`, target: host, detail: `${m} ${base}${path.split("?")[0]}` });
}

// Its own http span, else the current span when span capture is off
function outgoing(s: ActiveSpan | undefined): SpanContext | undefined {
  if (s) return s.context();
  return isQuiet() ? undefined : currentSpan();
}

function endWithStatus(s: ActiveSpan, status: number | undefined): void {
  const failed = (status ?? 0) >= 500;
  s.end(failed ? `HTTP ${status}` : undefined, null, failed);
}

// ------------------------------------------------------------ undici

function subscribeUndici(): void {
  const spans = new WeakMap<object, ActiveSpan>();
  diagnostics.subscribe("undici:request:create", (msg: any) => {
    const req = msg?.request;
    if (!req) return;
    const s = startHttpSpan(req.method, String(req.origin ?? ""), String(req.path ?? "/"));
    if (s) spans.set(req, s);
    const sc = outgoing(s);
    if (sc && typeof req.addHeader === "function") {
      try {
        if (!undiciHas(req.headers, "traceparent")) {
          req.addHeader("traceparent", traceparent(sc));
          if (sc.state && !undiciHas(req.headers, "tracestate")) req.addHeader("tracestate", sc.state);
        }
      } catch {
        // never let bookkeeping break the call
      }
    }
  });
  diagnostics.subscribe("undici:request:headers", (msg: any) => {
    const s = msg?.request && spans.get(msg.request);
    if (s) endWithStatus(s, msg.response?.statusCode);
  });
  diagnostics.subscribe("undici:request:error", (msg: any) => {
    const s = msg?.request && spans.get(msg.request);
    if (s) s.end(msg.error ?? new Error("request failed"));
  });
}

// Undici keeps headers as flat [name, value, ...] (v6+) or raw text (v5)
function undiciHas(headers: unknown, name: string): boolean {
  if (Array.isArray(headers)) {
    for (let i = 0; i < headers.length; i += 2) if (String(headers[i]).toLowerCase() === name) return true;
    return false;
  }
  return typeof headers === "string" && headers.toLowerCase().split("\r\n").some((l) => l.startsWith(name + ":"));
}

// ------------------------------------------------------- http / https

function wrapModule(mod: typeof http | typeof https): void {
  const orig = mod.request as (...args: any[]) => http.ClientRequest;
  if ((orig as any).__visionary) return;
  const request = function (this: unknown, ...args: any[]) {
    const req = orig.apply(this, args);
    try {
      observe(req, mod === https ? "https:" : "http:");
    } catch {
      // never let bookkeeping break the call
    }
    return req;
  };
  // get must use the wrapped request so traceparent is set before end()
  const get = function (this: unknown, ...args: any[]) {
    const req = request.apply(this, args);
    req.end();
    return req;
  };
  for (const fn of [request, get]) Object.defineProperty(fn, "__visionary", { value: true });
  (mod as any).request = request;
  (mod as any).get = get;
}

function observe(req: http.ClientRequest, defaultProtocol: string): void {
  const protocol = (req as any).protocol || defaultProtocol;
  // The Host header carries a non-default port; req.host does not.
  const host = req.getHeader("host")?.toString() || req.host || "localhost";
  const origin = `${protocol}//${host}`;
  const s = startHttpSpan(req.method, origin, req.path || "/");
  const sc = outgoing(s);
  if (sc && req.getHeader("traceparent") === undefined) {
    try {
      req.setHeader("traceparent", traceparent(sc));
      if (sc.state && req.getHeader("tracestate") === undefined) req.setHeader("tracestate", sc.state);
    } catch {
      // headers given as a raw array are already rendered
    }
  }
  if (!s) return;
  req.once("response", (res: http.IncomingMessage) => endWithStatus(s, res.statusCode));
  req.once("close", () => s.end(new Error("connection closed before a response")));
  // Only observe errors when the caller does: a listener of our own would
  // otherwise turn an unhandled "error" (which crashes) into a handled one.
  const onError = req.emit;
  req.emit = function (this: http.ClientRequest, event: string | symbol, ...rest: any[]) {
    if (event === "error") s.end(rest[0]);
    return onError.call(this, event, ...rest);
  } as typeof req.emit;
}
