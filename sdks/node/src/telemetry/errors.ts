import { createHash } from "node:crypto";
import { relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

/** "Node.js 22.11.0" */
export const RUNTIME = "Node.js " + process.versions.node;

export interface Frame {
  fn: string;
  file: string;
}

// The SDK's own files, recognised from a stack captured inside this module.
const SDK_FILE = (() => {
  const frames = parseFrames(new Error().stack ?? "");
  return frames[0]?.file ?? "";
})();

/** V8 frames ("at fn (file:line:col)", "at file:line:col") without line numbers. */
export function parseFrames(stack: string): Frame[] {
  const out: Frame[] = [];
  for (const raw of stack.split("\n")) {
    const line = raw.trim();
    if (!line.startsWith("at ")) continue;
    const body = line.slice(3).replace(/^async /, "");
    let fn = "<anonymous>";
    let loc = body;
    const paren = /^(.*?) \((.*)\)$/.exec(body);
    if (paren) {
      fn = paren[1]!;
      loc = paren[2]!;
    }
    // eval frames: "eval at fn (file:1:2), <anonymous>:1:2"
    if (loc.startsWith("eval at ")) loc = /\((.*?)\)/.exec(loc)?.[1] ?? loc;
    const file = loc.replace(/:\d+(?::\d+)?$/, "");
    out.push({ fn, file: file.startsWith("file://") ? safePath(file) : file });
  }
  return out;
}

function safePath(url: string): string {
  try {
    return fileURLToPath(url);
  } catch {
    return url;
  }
}

/** Frames from the application's own code: not Node internals, installed
 * packages or the SDK. */
function isAppFrame(f: Frame): boolean {
  const file = f.file;
  if (!file || file === "native" || file === "<anonymous>") return false;
  if (file.startsWith("node:") || file.startsWith("internal/")) return false;
  if (/[\\/]node_modules[\\/]/.test(file)) return false;
  if (SDK_FILE && file === SDK_FILE) return false;
  return true;
}

/** File paths relative to the working directory, so the same code deployed
 * elsewhere gets the same fingerprint. */
function stableFile(file: string): string {
  const cwd = process.cwd();
  if (file.startsWith(cwd + sep) || file.startsWith(cwd + "/")) return relative(cwd, file).split(sep).join("/");
  return file.split(sep).join("/");
}

/**
 * The first 16 hex characters of SHA-1 over the type and the top five
 * application frames (`function@file`), or over the type and the message
 * with digit runs replaced by 0 when there are no frames.
 */
export function fingerprint(type: string, stack: string, message: string): string {
  const frames = parseFrames(stack);
  const app = frames.filter(isAppFrame);
  const top = (app.length > 0 ? app : frames).slice(0, 5);
  const material =
    top.length > 0
      ? type + "\n" + top.map((f) => `${f.fn}@${stableFile(f.file)}`).join("\n")
      : type + "\n" + message.replace(/\d+/g, "0");
  return createHash("sha1").update(material).digest("hex").slice(0, 16);
}

export interface Described {
  type: string;
  message: string;
  stack: string;
}

/** Type, message and stack of anything thrown. */
export function describe(err: unknown): Described {
  if (err instanceof Error) {
    const type = err.name && err.name !== "Error" ? err.name : err.constructor?.name || "Error";
    return { type, message: err.message ?? "", stack: err.stack ?? "" };
  }
  if (err !== null && typeof err === "object") {
    const o = err as { name?: unknown; message?: unknown; stack?: unknown };
    return {
      type: typeof o.name === "string" && o.name ? o.name : err.constructor?.name || "Object",
      message: typeof o.message === "string" ? o.message : safeJson(err),
      stack: typeof o.stack === "string" ? o.stack : "",
    };
  }
  return { type: typeof err, message: String(err), stack: "" };
}

function safeJson(v: unknown): string {
  try {
    return JSON.stringify(v) ?? "";
  } catch {
    return String(v);
  }
}

/** Status an error asks for (http-errors, Fastify, Koa, NestJS); errors
 * below 500 are the app answering a bad request, not failures. */
export function errorStatus(err: unknown): number | undefined {
  const e = err as { status?: unknown; statusCode?: unknown; getStatus?: unknown } | null;
  if (!e || typeof e !== "object") return undefined;
  if (typeof e.getStatus === "function") {
    try {
      const s = (e.getStatus as () => unknown)();
      if (typeof s === "number") return s;
    } catch {
      // fall through
    }
  }
  const s = typeof e.statusCode === "number" ? e.statusCode : e.status;
  return typeof s === "number" ? s : undefined;
}

/** At most 20 rows per fingerprint per minute. */
export class ErrorLimiter {
  private readonly windows = new Map<string, { start: number; count: number }>();

  allow(fp: string, now = Date.now()): boolean {
    let w = this.windows.get(fp);
    if (!w || now - w.start >= 60_000) {
      if (this.windows.size > 10_000) this.prune(now);
      w = { start: now, count: 0 };
      this.windows.set(fp, w);
    }
    w.count++;
    return w.count <= 20;
  }

  private prune(now: number): void {
    for (const [k, w] of this.windows) if (now - w.start >= 60_000) this.windows.delete(k);
  }
}
