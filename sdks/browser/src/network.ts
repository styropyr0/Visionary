import type { Report } from "./types";
import { normalizePath, now, randomId, round, safe } from "./util";

/**
 * Times fetch and XMLHttpRequest calls. The name is "METHOD /path" with ids
 * as :id; the URL loses its query string; status 0 is a network failure.
 * Aborted calls are not reported.
 */
export function trackNetwork(report: Report, skip: (url: string) => boolean, traced: (origin: string) => boolean) {
  const done = safe((method: string, raw: string, status: number, t0: number, requestId?: string | null, traceId?: string) => {
    const u = new URL(raw, location.href);
    if (!/^https?:$/.test(u.protocol)) return;
    method = method.toUpperCase();
    const attrs: Record<string, string> = {};
    if (requestId) attrs.request_id = requestId.slice(0, 128);
    if (traceId) attrs.trace_id = traceId;
    report({
      kind: "api",
      name: `${method} ${normalizePath(u.pathname)}`,
      url: u.origin + u.pathname,
      method,
      status,
      duration_ms: round(now() - t0),
      ...(requestId || traceId ? { attrs } : {}),
    });
  });

  const traceFor = (url: string): { header: string; traceId: string } | null => {
    try {
      const u = new URL(url, location.href);
      if (!/^https?:$/.test(u.protocol) || !traced(u.origin)) return null;
      const traceId = randomId();
      return { header: `00-${traceId}-${randomId().slice(0, 16)}-01`, traceId };
    } catch {
      return null;
    }
  };

  const originalFetch = window.fetch;
  if (originalFetch) {
    window.fetch = function (input: RequestInfo | URL, init?: RequestInit) {
      let url = "";
      let method = "GET";
      try {
        url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        method = init?.method ?? (input instanceof Request ? input.method : "GET");
      } catch {
        // Unusual input: let fetch itself deal with it.
      }
      if (!url || skip(url)) return originalFetch.call(this, input, init);
      let traceId: string | undefined;
      try {
        const t = traceFor(url);
        if (t) {
          const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined));
          if (!headers.has("traceparent")) {
            headers.set("traceparent", t.header);
            init = { ...init, headers };
            traceId = t.traceId;
          }
        }
      } catch {
        // Headers it cannot copy, so send the call as it was
      }
      const t0 = now();
      return originalFetch.call(this, input, init).then(
        (res) => {
          done(method, url, res.status, t0, res.headers?.get("x-request-id"), traceId);
          return res;
        },
        (err) => {
          if ((err as Error)?.name !== "AbortError") done(method, url, 0, t0, null, traceId);
          throw err;
        },
      );
    };
  }

  const proto = XMLHttpRequest.prototype;
  const open = proto.open;
  const send = proto.send;
  const setHeader = proto.setRequestHeader;
  type Tracked = XMLHttpRequest & { __vis?: { m: string; u: string; own?: boolean } };
  proto.open = function (this: Tracked, method: string, url: string | URL, ...rest: unknown[]) {
    this.__vis = { m: method, u: String(url) };
    return (open as (...a: unknown[]) => void).call(this, method, url, ...rest);
  } as typeof proto.open;
  proto.setRequestHeader = function (this: Tracked, name: string, value: string) {
    if (this.__vis && String(name).toLowerCase() === "traceparent") this.__vis.own = true;
    return setHeader.call(this, name, value);
  };
  proto.send = function (this: Tracked, body?: Document | XMLHttpRequestBodyInit | null) {
    const v = this.__vis;
    if (v && !skip(v.u)) {
      let traceId: string | undefined;
      const t = v.own ? null : traceFor(v.u);
      if (t) {
        try {
          setHeader.call(this, "traceparent", t.header);
          traceId = t.traceId;
        } catch {
          // not opened, send fails on its own
        }
      }
      const t0 = now();
      let aborted = false;
      this.addEventListener("abort", () => (aborted = true));
      this.addEventListener("loadend", () => {
        if (!aborted) done(v.m, v.u, this.status, t0, xhrRequestId(this), traceId);
      });
    }
    return send.call(this, body);
  };
}

// getAllResponseHeaders skips the console error getResponseHeader logs for headers the page can't read.
function xhrRequestId(x: XMLHttpRequest): string | null {
  try {
    const m = /^x-request-id:\s*(.+)$/im.exec(x.getAllResponseHeaders());
    return m ? m[1].trim() : null;
  } catch {
    return null;
  }
}
