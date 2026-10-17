import type { Attrs, ClientEvent } from "./types";
import { bareUrl } from "./util";

const PER_ERROR = 5; // the same error at most this often per page
const PER_PAGE = 50;
const counts = new Map<string, number>();
let total = 0;

/** Turns anything thrown into an error event, or null when over the caps. */
export function errorEvent(err: unknown, attrs?: Attrs): Omit<ClientEvent, "ts"> | null {
  const o = err && typeof err === "object" ? (err as { name?: unknown; message?: unknown; stack?: unknown }) : null;
  const name = (o?.name ? String(o.name) : "") || "Error";
  const message = (typeof err === "string" ? err : o && o.message != null ? String(o.message) : safeString(err)).slice(0, 1000);
  const key = `${name}:${message}`;
  const n = counts.get(key) ?? 0;
  if (n >= PER_ERROR || total >= PER_PAGE) return null;
  counts.set(key, n + 1);
  total++;
  const stack = typeof o?.stack === "string" ? o.stack.slice(0, 16_000) : undefined;
  return { kind: "error", name, message, stack, url: bareUrl(location.href), attrs };
}

function safeString(v: unknown): string {
  try {
    return typeof v === "object" ? JSON.stringify(v) : String(v);
  } catch {
    return String(v);
  }
}

/** Uncaught errors and unhandled promise rejections. */
export function trackErrors(push: (e: Omit<ClientEvent, "ts">) => void) {
  addEventListener("error", (ev) => {
    // Failed <img>/<script> loads arrive here as plain Events; skip them.
    if (!(ev instanceof ErrorEvent)) return;
    const err = ev.error ?? { name: "Error", message: ev.message };
    const e = errorEvent(err);
    if (!e) return;
    if (!e.stack && ev.filename) e.stack = `    at ${ev.filename}:${ev.lineno}:${ev.colno}`;
    push(e);
  });
  addEventListener("unhandledrejection", (ev) => {
    const reason = ev.reason;
    const e = errorEvent(reason instanceof Error ? reason : { name: "UnhandledRejection", message: safeString(reason) });
    if (e) push(e);
  });
}
