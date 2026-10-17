import diagnostics from "node:diagnostics_channel";
import http from "node:http";
import https from "node:https";
import { syncBuiltinESMExports } from "node:module";
import type { ActiveSpan } from "./spans.js";
import { markVisible, startSpan } from "./spans.js";

/**
 * Outbound HTTP spans: fetch and undici through their diagnostics channels,
 * and the http/https modules (axios, got, node-fetch, SDKs) by wrapping
 * request/get. A span ends when the response headers arrive, like Go's
 * RoundTripper; status is "error" on a transport error or a 5xx.
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

// ------------------------------------------------------- http / https

function wrapModule(mod: typeof http | typeof https): void {
  for (const method of ["request", "get"] as const) {
    const orig = mod[method] as (...args: any[]) => http.ClientRequest;
    if ((orig as any).__visionary) continue;
    const wrapped = function (this: unknown, ...args: any[]) {
      const req = orig.apply(this, args);
      try {
        observe(req, mod === https ? "https:" : "http:");
      } catch {
        // never let bookkeeping break the call
      }
      return req;
    };
    Object.defineProperty(wrapped, "__visionary", { value: true });
    (mod as any)[method] = wrapped;
  }
}

function observe(req: http.ClientRequest, defaultProtocol: string): void {
  const protocol = (req as any).protocol || defaultProtocol;
  // The Host header carries a non-default port; req.host does not.
  const host = req.getHeader("host")?.toString() || req.host || "localhost";
  const origin = `${protocol}//${host}`;
  const s = startHttpSpan(req.method, origin, req.path || "/");
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
