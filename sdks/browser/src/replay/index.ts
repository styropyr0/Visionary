import type { ReplayOptions } from "../types";
import { onHidden } from "../util";
import { FULL, Recorder, type Ev } from "./record";

/** What the base SDK hands the recorder when it loads it. */
export interface ReplayContext {
  /** .../api/ingest/replay */
  url: string;
  key: string;
  sid: string;
  app: string;
  /** See replayMode in replay-load.ts. */
  mode: string;
  user: () => string | undefined;
  fetch: typeof fetch;
  options: ReplayOptions;
}

export interface ReplayHandle {
  error(): void;
}

const FLUSH_MS = 5000;
const CHECKOUT_MS = 30_000;
/** Under the server's 512 KB limit. */
const MAX_BODY = 500_000;
const BEACON_MAX = 60_000;
const MAX_PAGE_MS = 2 * 3600_000;

function nextSeq(sid: string): number {
  let n = 0;
  try {
    const kept = sessionStorage.getItem("vis.rseq");
    if (kept && kept.startsWith(sid + ":")) n = Number(kept.slice(sid.length + 1)) || 0;
    sessionStorage.setItem("vis.rseq", `${sid}:${n + 1}`);
  } catch {
    n = Date.now();
  }
  return n;
}

async function gzip(text: string): Promise<ArrayBuffer | null> {
  if (typeof CompressionStream === "undefined") return null;
  return new Response(new Blob([text]).stream().pipeThrough(new CompressionStream("gzip"))).arrayBuffer();
}

/**
 * Records the page. A recorded session uploads a chunk every few seconds; an
 * error-sampled one keeps 30 to 60 s in memory and uploads it only when an
 * error happens, then records the rest of the session.
 */
export function record(ctx: ReplayContext): ReplayHandle {
  let full = ctx.mode === "1" || ctx.mode === "x";
  const reason = ctx.mode === "1" ? "sample" : "error";
  let events: Ev[] = [];
  let lastFull = 0;
  let stopped = false;
  let queue: Promise<void> = Promise.resolve();
  const started = Date.now();

  const rec = new Recorder(
    { maskInputs: ctx.options.maskInputs !== false, maskAllText: !!ctx.options.maskAllText, blockSelector: ctx.options.blockSelector },
    (e) => {
      if (stopped) return;
      if (e[1] === FULL) lastFull = events.length;
      events.push(e);
    },
  );

  const stop = () => {
    stopped = true;
    events = [];
    rec.stop();
    clearInterval(timer);
  };

  const query = (batch: Ev[], enc: string) => {
    const q = new URLSearchParams({
      sid: ctx.sid,
      seq: String(nextSeq(ctx.sid)),
      start: String(batch[0][0]),
      end: String(batch[batch.length - 1][0]),
      n: String(batch.length),
      app: ctx.app,
      reason,
      enc,
      url: location.origin + location.pathname,
    });
    const user = ctx.user();
    if (user) q.set("user", user);
    return q;
  };

  const post = (q: URLSearchParams, body: BodyInit, size: number) =>
    ctx.fetch
      .call(window, `${ctx.url}?${q}`, {
        method: "POST",
        body,
        keepalive: size < BEACON_MAX,
        credentials: "omit",
        headers: { "Content-Type": q.get("enc") === "gzip" ? "application/octet-stream" : "text/plain", Authorization: `Bearer ${ctx.key}` },
      })
      .then(
        (r) => {
          // 4xx: replays are off, over a limit or the key is wrong.
          if (r.status >= 400 && r.status < 500) stop();
        },
        () => {},
      );

  const upload = async (batch: Ev[]): Promise<void> => {
    const json = JSON.stringify(batch);
    const gz = await gzip(json).catch(() => null);
    const size = gz ? gz.byteLength : json.length;
    if (size > MAX_BODY) {
      if (batch.length < 2) return stop();
      const half = batch.length >> 1;
      await upload(batch.slice(0, half));
      return upload(batch.slice(half));
    }
    await post(query(batch, gz ? "gzip" : "json"), gz ?? json, size);
  };

  const flush = (beacon: boolean) => {
    if (!full || stopped) return;
    rec.takeRecords();
    if (!events.length) return;
    const batch = events;
    events = [];
    lastFull = 0;
    if (beacon && navigator.sendBeacon) {
      // The page may be unloading, so there's no time to compress.
      const json = JSON.stringify(batch);
      const q = query(batch, "json");
      q.set("key", ctx.key);
      try {
        if (json.length <= BEACON_MAX && navigator.sendBeacon(`${ctx.url}?${q}`, json)) return;
      } catch {
        // Fall through to fetch.
      }
    }
    queue = queue.then(() => upload(batch)).catch(() => {});
  };

  const tick = () => {
    if (stopped) return;
    if (Date.now() - started > MAX_PAGE_MS) return stop();
    if (full) return flush(false);
    events = events.slice(lastFull);
    rec.snapshot();
  };

  let timer: ReturnType<typeof setInterval> | undefined;
  const begin = () => {
    try {
      rec.start();
    } catch {
      return stop();
    }
    timer = setInterval(tick, full ? FLUSH_MS : CHECKOUT_MS);
    onHidden(() => flush(true));
  };
  if (document.readyState === "loading") addEventListener("DOMContentLoaded", begin, { once: true });
  else begin();

  return {
    error() {
      if (full || stopped) return;
      full = true;
      try {
        sessionStorage.setItem("vis.replay", `${ctx.sid}:x`);
      } catch {
        // This page still records.
      }
      clearInterval(timer);
      timer = setInterval(tick, FLUSH_MS);
      // A moment later, so the page's reaction to the error is in it.
      setTimeout(() => flush(false), 1000);
    },
  };
}
