/**
 * The rules of application logs (app_logs) that every SDK applies the same
 * way: level names, the level a plain-text line states, secret scrubbing,
 * attribute encoding and what Settings → Logs keeps. A port of
 * sdks/go/logrules; SPEC.md, "Application logs", is the contract.
 */
import { parseBool } from "../config.js";
import { DEFAULT_REDACT_FIELDS, REDACTED } from "../redact.js";

/** Severities as stored in app_logs.severity. */
export const TRACE = 1;
export const DEBUG = 2;
export const INFO = 3;
export const WARN = 4;
export const ERROR = 5;
export const FATAL = 6;

export const MAX_MESSAGE = 8 << 10;
export const MAX_ATTRS = 16 << 10;

const LEVEL_NAMES = ["", "trace", "debug", "info", "warn", "error", "fatal"];

/** The stored name of a severity; out of range is info. */
export function levelName(sev: number): string {
  return sev >= TRACE && sev <= FATAL ? LEVEL_NAMES[sev]! : "info";
}

const LEVELS: Record<string, number> = {};
for (const [sev, names] of [
  [TRACE, "trace trc finest finer verbose vrb"],
  [DEBUG, "debug dbg fine config"],
  [INFO, "info inf information informational notice log"],
  [WARN, "warn warning wrn"],
  [ERROR, "error err eror severe"],
  [FATAL, "fatal critical crit alert emerg emergency panic ftl"],
] as const) {
  for (const n of names.split(" ")) LEVELS[n] = sev;
}

/** Reads a level name the way every source spells them (trace, debug, info,
 * notice, warn, warning, error, err, critical, fatal, panic...); 0 when
 * unknown. */
export function parseLevel(s: string): number {
  return LEVELS[String(s ?? "").trim().toLowerCase()] ?? 0;
}

const LEVEL_FIELD = /\b(?:level|lvl|severity)=["']?([a-z]+)/i;
/** A date, a time or both, as loggers print them. */
const TIMESTAMP = /^[[(]?[0-9][0-9/:.,\-TZ+]*[\])]?$/;

/**
 * The level a plain-text line states at its start, the way loggers print
 * it: "ERROR ...", "[warn] ...", "error: ...", "2026-10-28 09:30:12 WARN ...",
 * or a level=/lvl=/severity= field in the first 200 bytes. A bare lower-case
 * word ("error rate is low") is not a level. 0 when the line states none.
 */
export function textLevel(s: string): number {
  const head = s.length > 200 ? s.slice(0, 200) : s;
  const m = LEVEL_FIELD.exec(head);
  if (m) {
    const n = parseLevel(m[1]!);
    if (n > 0) return n;
  }
  const fields = head.split(/\s+/).filter(Boolean);
  for (let i = 0; i < fields.length && i <= 3; i++) {
    const f = fields[i]!;
    if (TIMESTAMP.test(f)) continue;
    const word = f.replace(/^[[\]<>():|]+|[[\]<>():|]+$/g, "");
    const n = parseLevel(word);
    if (n === 0) return 0;
    const marked = /^[[<(]/.test(f) || /[:\]|]$/.test(f);
    return marked || word === word.toUpperCase() ? n : 0;
  }
  return 0;
}

/** Removes a leading upper-case level word that textLevel read as sev
 * ("INFO started" → "started", "WARNING: disk" → "disk"): the level has its
 * own column. */
export function stripLevelWord(s: string, sev: number): string {
  const i = s.indexOf(" ");
  if (i < 0) return s;
  const word = s.slice(0, i);
  const rest = s.slice(i + 1);
  if (rest.trim() === "" || word !== word.toUpperCase()) return s;
  const w = word.replace(/:$/, "").toLowerCase();
  return w === levelName(sev) || w === "warning" ? rest : s;
}

/** Secrets in free text, replaced before a line is stored, in this order. */
const SECRET_PATTERNS: [RegExp, string][] = [
  [/-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)/g, "[REDACTED:private-key]"],
  [/\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}/g, "[REDACTED:jwt]"],
  [/\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}/gi, "$1 [REDACTED]"],
  [/\b(AKIA|ASIA)[0-9A-Z]{16}\b/g, "[REDACTED:aws-key]"],
  [
    /\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret)\b(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)/gi,
    "$1$2[REDACTED]",
  ],
];

/** Replaces credentials in s. */
export function scrub(s: string): string {
  for (const [re, with_] of SECRET_PATTERNS) s = s.replace(re, with_);
  return s;
}

/** The request redaction defaults plus extra, lower case: attribute names
 * whose values are never stored. */
export function secretKeys(extra: string[] = []): Set<string> {
  return new Set([...DEFAULT_REDACT_FIELDS, ...extra].map((f) => f.trim().toLowerCase()).filter(Boolean));
}

/** Cuts s to at most n UTF-8 bytes without splitting a character. */
export function truncateBytes(s: string, n: number): string {
  if (s.length <= n / 3 || Buffer.byteLength(s) <= n) return s;
  const b = Buffer.from(s);
  while (n > 0 && (b[n]! & 0xc0) === 0x80) n--;
  return b.subarray(0, n).toString("utf8");
}

export type AttrValue = string | number | boolean;

const lastKey = (k: string) => k.slice(k.lastIndexOf(".") + 1);
const pair = (k: string, v: AttrValue) => Buffer.byteLength(JSON.stringify(k)) + 1 + Buffer.byteLength(JSON.stringify(v));

/** JSON with the keys in sorted order, as every SDK writes attrs. */
function sortedJSON(m: Map<string, AttrValue>): string {
  const o: Record<string, AttrValue> = {};
  for (const k of [...m.keys()].sort()) o[k] = m.get(k)!;
  return JSON.stringify(o);
}

/**
 * Writes attrs as one flat JSON object: values under a secret key (its last
 * dotted part) redacted, strings scrubbed, at most MAX_ATTRS bytes (long
 * values are cut to 1024 bytes first, then keys kept in sorted order while
 * they fit, with "_truncated": true). "" when there are none.
 */
export function encodeAttrs(attrs: Record<string, AttrValue>, secret: Set<string>): string {
  const out = new Map<string, AttrValue>();
  for (const [k, v] of Object.entries(attrs)) {
    if (secret.has(lastKey(k).toLowerCase())) out.set(k, REDACTED);
    else out.set(k, typeof v === "string" ? scrub(v) : v);
  }
  if (out.size === 0) return "";
  let s = sortedJSON(out);
  if (Buffer.byteLength(s) <= MAX_ATTRS) return s;
  for (const [k, v] of out) {
    if (typeof v === "string" && Buffer.byteLength(v) > 1024) out.set(k, truncateBytes(v, 1024) + "…");
  }
  s = sortedJSON(out);
  if (Buffer.byteLength(s) <= MAX_ATTRS) return s;
  const kept = new Map<string, AttrValue>([["_truncated", true]]);
  let size = 2 + pair("_truncated", true);
  for (const k of [...out.keys()].sort()) {
    const n = 1 + pair(k, out.get(k)!);
    if (size + n > MAX_ATTRS) break;
    kept.set(k, out.get(k)!);
    size += n;
  }
  return sortedJSON(kept);
}

/** Changes the level for part of the traffic until a time. */
export interface LogOverride {
  service: string;
  route: string;
  user: string;
  sev: number;
  /** epoch ms; 0 is no end */
  until: number;
}

/** Drops the lines it matches; empty fields match anything, and sev (when
 * set) drops only lines at or below it. */
export interface LogDrop {
  service: string;
  logger: string;
  contains: string;
  sev: number;
}

/** What Settings → Logs asks for (visionary_settings logs.*). */
export interface LogSettings {
  enabled: boolean;
  level: number;
  buffer: boolean;
  bufferLevel: number;
  slowMs: number;
  rate: number;
  overrides: LogOverride[];
  drops: LogDrop[];
}

/** The settings before any are read; level 0 means info. */
export function defaultLogSettings(level = 0): LogSettings {
  return {
    enabled: true,
    level: level || INFO,
    buffer: true,
    bufferLevel: DEBUG,
    slowMs: 1000,
    rate: 6000,
    overrides: [],
    drops: [],
  };
}

const text = (v: unknown): string => (typeof v === "string" ? v : "");

function jsonList(v: string): Record<string, unknown>[] {
  try {
    const list = JSON.parse(v);
    return Array.isArray(list) ? list.filter((x) => x && typeof x === "object") : [];
  } catch {
    return [];
  }
}

/** Reads (name, value) rows of logs.*; anything missing or unreadable keeps
 * its default. */
export function parseLogSettings(rows: string[][], base = 0): LogSettings {
  const s = defaultLogSettings(base);
  for (const [name, raw] of rows) {
    const v = (raw ?? "").trim();
    switch (name) {
      case "logs.enabled":
        s.enabled = parseBool(v, s.enabled);
        break;
      case "logs.level":
        s.level = parseLevel(v) || s.level;
        break;
      case "logs.buffer":
        s.buffer = parseBool(v, s.buffer);
        break;
      case "logs.buffer_level":
        s.bufferLevel = parseLevel(v) || s.bufferLevel;
        break;
      case "logs.slow_ms": {
        const n = Number(v);
        if (v !== "" && Number.isFinite(n) && n > 0) s.slowMs = n;
        break;
      }
      case "logs.rate":
        if (/^\d+$/.test(v)) s.rate = Number(v);
        break;
      case "logs.overrides":
        for (const o of jsonList(v)) {
          const sev = parseLevel(text(o.level));
          if (sev === 0) continue;
          let until = 0;
          if (text(o.until)) {
            until = parseTime(text(o.until));
            if (Number.isNaN(until)) continue;
          }
          s.overrides.push({ service: text(o.service), route: text(o.route), user: text(o.user), sev, until });
        }
        break;
      case "logs.drop":
        for (const d of jsonList(v)) {
          const drop = { service: text(d.service), logger: text(d.logger), contains: text(d.contains), sev: parseLevel(text(d.level)) };
          if (drop.service || drop.logger || drop.contains || drop.sev) s.drops.push(drop);
        }
        break;
    }
  }
  return s;
}

/** A dashboard timestamp: created_at format, RFC 3339, or "YYYY-MM-DD
 * HH:MM:SS[.mmm]" in UTC. NaN when it is none of these. */
export function parseTime(v: string): number {
  const m = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2}(?:\.\d+)?)(Z|[+-]\d{2}:\d{2})?$/.exec(v.trim());
  if (!m) return NaN;
  return Date.parse(`${m[1]}T${m[2]}${m[3] ?? "Z"}`);
}

/** Compares a route pattern, allowing a trailing * for prefixes. */
export function routeMatches(pattern: string, route: string): boolean {
  return pattern.endsWith("*") ? route.startsWith(pattern.slice(0, -1)) : pattern === route;
}

const active = (o: LogOverride, service: string, now: number) =>
  (o.until === 0 || now <= o.until) && (o.service === "" || o.service === service);

/**
 * The lowest level kept outright for a line outside any request, or once
 * its request is known: the base level, then service-wide overrides, then
 * the most specific matching override (a user beats a route). Later
 * overrides win among equals.
 */
export function floor(s: LogSettings, service: string, route: string, user: string, now: number): number {
  let level = s.level;
  let rank = 0;
  for (const o of s.overrides) {
    if (!active(o, service, now)) continue;
    if (o.route && !routeMatches(o.route, route)) continue;
    if (o.user && o.user !== user) continue;
    const r = o.user ? 3 : o.route ? 2 : 1;
    if (r >= rank) {
      level = o.sev;
      rank = r;
    }
  }
  return level;
}

/** The level under which a line logged in a request is held until it ends:
 * everything a route or user override could keep stays held. */
export function holdBelow(s: LogSettings, service: string, now: number): number {
  let level = s.level;
  for (const o of s.overrides) if (active(o, service, now) && !o.route && !o.user) level = o.sev;
  for (const o of s.overrides) if (active(o, service, now) && (o.route || o.user)) level = Math.max(level, o.sev);
  return level;
}

/** Whether a drop rule matches the line. */
export function dropped(s: LogSettings, service: string, logger: string, message: string, sev: number): boolean {
  return s.drops.some(
    (d) =>
      (!d.service || d.service === service) &&
      (!d.logger || d.logger === logger) &&
      (!d.sev || sev <= d.sev) &&
      (!d.contains || message.includes(d.contains)),
  );
}
