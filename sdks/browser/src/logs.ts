import type { Attrs, LogLevel, Report } from "./types";
import { bareUrl, safe } from "./util";

const ORDER: Record<LogLevel, number> = { debug: 1, info: 2, warn: 3, error: 4 };
const PER_MINUTE = 120;
const MAX_MESSAGE = 8 << 10;

const METHODS: [keyof Console, LogLevel][] = [
  ["debug", "debug"],
  ["log", "info"],
  ["info", "info"],
  ["warn", "warn"],
  ["error", "error"],
];

function text(v: unknown): string {
  if (typeof v === "string") return v;
  if (v instanceof Error) return `${v.name}: ${v.message}`;
  try {
    return JSON.stringify(v) ?? String(v);
  } catch {
    return String(v);
  }
}

export function makeLogger(report: Report) {
  let minute = 0;
  let count = 0;
  let busy = false;
  const send = (level: LogLevel, message: string, attrs?: Attrs) => {
    if (busy || message.startsWith("visionary:")) return;
    const m = Math.floor(Date.now() / 60_000);
    if (m !== minute) {
      minute = m;
      count = 0;
    }
    if (++count > PER_MINUTE && level !== "error") return;
    busy = true;
    try {
      report({ kind: "log", name: level, message: message.slice(0, MAX_MESSAGE), url: bareUrl(location.href), attrs });
    } finally {
      busy = false;
    }
  };
  return send;
}

export function captureConsole(send: (level: LogLevel, message: string) => void, min: LogLevel = "info") {
  for (const [method, level] of METHODS) {
    if (ORDER[level] < ORDER[min]) continue;
    const original = console[method] as (...a: unknown[]) => void;
    if (typeof original !== "function") continue;
    (console as unknown as Record<string, unknown>)[method] = function (this: Console, ...args: unknown[]) {
      safe(() => send(level, args.map(text).join(" ")))();
      return original.apply(this, args);
    };
  }
}
