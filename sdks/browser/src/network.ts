import type { Report } from "./types";
import { normalizePath, now, round, safe } from "./util";

/**
 * Times fetch and XMLHttpRequest calls. The name is "METHOD /path" with ids
 * as :id; the URL loses its query string; status 0 is a network failure.
 * Aborted calls are not reported.
 */
export function trackNetwork(report: Report, skip: (url: string) => boolean) {
  const done = safe((method: string, raw: string, status: number, t0: number, requestId?: string | null) => {
    const u = new URL(raw, location.href);
    if (!/^https?:$/.test(u.protocol)) return;
    method = method.toUpperCase();
    report({
      kind: "api",
      name: `${method} ${normalizePath(u.pathname)}`,
      url: u.origin + u.pathname,
      method,
      status,
      duration_ms: round(now() - t0),
      // The server's X-Request-ID links the call to its request and logs.
      ...(requestId ? { attrs: { request_id: requestId.slice(0, 128) } } : {}),
    });
  });

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
      const t0 = now();
      return originalFetch.call(this, input, init).then(
        (res) => {
          done(method, url, res.status, t0, res.headers?.get("x-request-id"));
          return res;
        },
        (err) => {
          if ((err as Error)?.name !== "AbortError") done(method, url, 0, t0);
          throw err;
        },
      );
    };
  }

  const proto = XMLHttpRequest.prototype;
  const open = proto.open;
  const send = proto.send;
  type Tracked = XMLHttpRequest & { __vis?: { m: string; u: string } };
  proto.open = function (this: Tracked, method: string, url: string | URL, ...rest: unknown[]) {
    this.__vis = { m: method, u: String(url) };
    return (open as (...a: unknown[]) => void).call(this, method, url, ...rest);
  } as typeof proto.open;
  proto.send = function (this: Tracked, body?: Document | XMLHttpRequestBodyInit | null) {
    const v = this.__vis;
    if (v && !skip(v.u)) {
      const t0 = now();
      let aborted = false;
      this.addEventListener("abort", () => (aborted = true));
      this.addEventListener("loadend", () => {
        if (!aborted) done(v.m, v.u, this.status, t0, xhrRequestId(this));
      });
    }
    return send.call(this, body);
  };
}

/**
 * The X-Request-ID of an XHR response. getAllResponseHeaders lists only
 * headers the page may read, without the console error getResponseHeader
 * logs for the rest.
 */
function xhrRequestId(x: XMLHttpRequest): string | null {
  try {
    const m = /^x-request-id:\s*(.+)$/im.exec(x.getAllResponseHeaders());
    return m ? m[1].trim() : null;
  } catch {
    return null;
  }
}
