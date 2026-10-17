import type { ClientEvent } from "./types";

const MAX_EVENTS = 100; // per request, the server's limit
const MAX_BYTES = 60_000; // under the server's 64 KB, with room for the envelope
const FLUSH_AT = 30;
const FLUSH_MS = 5000;
const RETRY_MS = 15_000;

export interface Envelope {
  app: string;
  app_version?: string;
  version?: string;
  session_id: string;
  user_id?: string;
  device: string;
  os: string;
}

/**
 * Batches events and posts them. Normal flushes use fetch with the key in
 * the Authorization header; when the page is going away, sendBeacon carries
 * the batch as text/plain with the key in the query string, since a beacon
 * cannot set headers. The server's 4xx answers drop a batch; a 5xx or a
 * network failure is retried once.
 */
export class Transport {
  private queue: ClientEvent[] = [];
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private url: string,
    private key: string,
    public envelope: Envelope,
    private fetchFn: typeof fetch,
  ) {}

  push(e: ClientEvent) {
    if (this.queue.length >= 1000) return; // offline or blocked: stop growing
    this.queue.push(e);
    if (this.queue.length >= FLUSH_AT) this.flush();
    else if (!this.timer) this.timer = setTimeout(() => this.flush(), FLUSH_MS);
  }

  flush(beacon = false) {
    clearTimeout(this.timer);
    this.timer = undefined;
    while (this.queue.length) {
      const batch = this.queue.splice(0, MAX_EVENTS);
      for (const body of this.encode(batch)) this.send(body, beacon, false);
    }
  }

  /** Splits a batch until every request body fits under the size cap. */
  private encode(events: ClientEvent[]): string[] {
    const body = JSON.stringify({ ...this.envelope, events });
    if (body.length <= MAX_BYTES) return [body];
    if (events.length === 1) {
      const e = { ...events[0], stack: events[0].stack?.slice(0, 4000), message: events[0].message?.slice(0, 500), attrs: undefined };
      const small = JSON.stringify({ ...this.envelope, events: [e] });
      return small.length <= MAX_BYTES ? [small] : [];
    }
    const half = events.length >> 1;
    return [...this.encode(events.slice(0, half)), ...this.encode(events.slice(half))];
  }

  private send(body: string, beacon: boolean, retried: boolean) {
    if (beacon && navigator.sendBeacon) {
      try {
        if (navigator.sendBeacon(`${this.url}?key=${encodeURIComponent(this.key)}`, body)) return;
      } catch {
        // Fall through to fetch.
      }
    }
    const retry = () => {
      if (!retried) setTimeout(() => this.send(body, false, true), RETRY_MS);
    };
    try {
      this.fetchFn
        .call(window, this.url, {
          method: "POST",
          body,
          keepalive: body.length < MAX_BYTES,
          credentials: "omit",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${this.key}` },
        })
        .then((r) => {
          if (r.status >= 500) retry();
        }, retry);
    } catch {
      retry();
    }
  }
}
