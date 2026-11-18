import { createHash } from "node:crypto";
import { isIP } from "node:net";

// Rate-limit rules from Security → Rate limits, ported from sdks/go/ratelimit
// (the reference). See "Rate limits" in sdks/SPEC.md.

export const SETTING_VERSION = "ratelimit.version";
export const SETTING_CONFIG = "ratelimit.config";

const MAX_WINDOW = 86400;
export const MAX_COUNTERS = 10_000;
export const MAX_AGGREGATES = 1000;

const DEFAULT_COUNTRY_HEADERS = [
  "CF-IPCountry",
  "CloudFront-Viewer-Country",
  "X-Vercel-IP-Country",
  "Fastly-Geo-Country",
  "X-AppEngine-Country",
  "X-Country-Code",
];
const DEFAULT_REDACT = ["Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token"];

export type Outcome = "limited" | "blocked" | "would_limit" | "would_block";

/** One stored rule; missing fields are undefined. */
export interface Rule {
  id: string;
  name?: string;
  enabled?: boolean;
  mode?: string;
  action: string;
  services?: string[];
  methods?: string[];
  paths?: string[];
  ips?: string[];
  countries?: string[];
  match?: Record<string, string[] | null>;
  key?: string;
  limit?: number;
  window_s?: number;
  algorithm?: string;
  burst?: number;
  limit_by?: string;
  limits?: Record<string, number>;
  status?: number;
  body?: string;
  content_type?: string;
}

/** The ratelimit.config document. */
export interface RateLimitConfig {
  enabled?: boolean;
  ip_header?: string;
  country_headers?: string[];
  rules: Rule[];
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const isStrList = (v: unknown) => v === null || v === undefined || (Array.isArray(v) && v.every((x) => typeof x === "string"));
const optional = (v: unknown, type: string) => v === null || v === undefined || typeof v === type;

/** A rule whose fields have the right JSON types, else undefined (Go's decode). */
function decodeRule(v: unknown): Rule | undefined {
  if (!isObj(v)) return undefined;
  for (const k of ["id", "name", "mode", "action", "key", "algorithm", "limit_by", "body", "content_type"]) {
    if (!optional(v[k], "string")) return undefined;
  }
  for (const k of ["limit", "window_s", "burst"]) if (!optional(v[k], "number")) return undefined;
  if (!optional(v.enabled, "boolean")) return undefined;
  if (!optional(v.status, "number") || (typeof v.status === "number" && !Number.isInteger(v.status))) return undefined;
  for (const k of ["services", "methods", "paths", "ips", "countries"]) if (!isStrList(v[k])) return undefined;
  if (v.match !== null && v.match !== undefined) {
    if (!isObj(v.match) || !Object.values(v.match).every(isStrList)) return undefined;
  }
  if (v.limits !== null && v.limits !== undefined) {
    if (!isObj(v.limits) || !Object.values(v.limits).every((n) => n === null || typeof n === "number")) return undefined;
  }
  const out: Record<string, unknown> = {};
  for (const [k, x] of Object.entries(v)) if (x !== null) out[k] = x;
  out.id ??= "";
  out.action ??= "";
  return out as unknown as Rule;
}

/** Reads a stored document leniently: rules that do not decode are dropped.
 * Throws only on invalid JSON. */
export function parseConfig(raw: string): RateLimitConfig {
  const doc: unknown = JSON.parse(raw);
  const c: RateLimitConfig = { rules: [] };
  if (!isObj(doc)) return c;
  if (typeof doc.enabled === "boolean") c.enabled = doc.enabled;
  if (typeof doc.ip_header === "string") c.ip_header = doc.ip_header;
  if (isStrList(doc.country_headers) && doc.country_headers) c.country_headers = doc.country_headers as string[];
  if (Array.isArray(doc.rules)) {
    for (const r of doc.rules) {
      const rule = decodeRule(r);
      if (rule) c.rules.push(rule);
    }
  }
  return c;
}

type PropKind = "ip" | "country" | "method" | "path" | "header" | "query" | "global" | "user" | "role" | "attr";

interface Prop {
  kind: PropKind;
  /** Header (lower case), query or attribute name. */
  name: string;
}

const userPhase = (p: Prop) => p.kind === "user" || p.kind === "role" || p.kind === "attr";

function parseProp(s: string): Prop | undefined {
  for (const kind of ["header", "query", "attr"] as const) {
    if (s.startsWith(kind + ":") && s.length > kind.length + 1) {
      const name = s.slice(kind.length + 1);
      return { kind, name: kind === "header" ? name.toLowerCase() : name };
    }
  }
  switch (s) {
    case "ip":
    case "country":
    case "method":
    case "path":
    case "global":
    case "user":
    case "role":
      return { kind: s, name: "" };
  }
  return undefined;
}

type Alg = "sliding" | "fixed" | "token_bucket";

interface Counter {
  win: number | null;
  curr: number;
  prev: number;
  tokens: number;
  last: number;
}

interface Compiled {
  id: string;
  name: string;
  monitor: boolean;
  action: string;
  methods: string[];
  paths: string[];
  hasIPs: boolean;
  prefixes: Prefix[];
  countries: string[];
  match: { p: Prop; values: string[] }[];
  key: Prop;
  limit: number;
  window: number;
  alg: Alg;
  burst: number;
  limitBy?: Prop;
  limits: Map<string, number>;
  status: number;
  body: string;
  contentType: string;
  userPhase: boolean;
  counters: Map<string, Counter>;
}

const shape = (r: Compiled) => `${r.key.kind}|${r.key.name}|${r.window}|${r.alg}|${r.burst}`;

interface RuleSet {
  ipHeader: string;
  countryHeaders: string[];
  request: Compiled[];
  user: Compiled[];
  byId: Map<string, Compiled>;
}

const VALID_ID = /^[A-Za-z0-9_-]{1,32}$/;

function compile(c: RateLimitConfig, service: string): RuleSet {
  const countryHeaders = (c.country_headers ?? []).map((h) => h.trim()).filter(Boolean);
  const rs: RuleSet = {
    ipHeader: (c.ip_header ?? "").trim(),
    countryHeaders: countryHeaders.length > 0 ? countryHeaders : DEFAULT_COUNTRY_HEADERS,
    request: [],
    user: [],
    byId: new Map(),
  };
  if (c.enabled === false) return rs;
  for (const src of c.rules) {
    const r = compileRule(src);
    if (!r || src.enabled === false || rs.byId.has(r.id) || !servesService(src.services, service)) continue;
    rs.byId.set(r.id, r);
    (r.userPhase ? rs.user : rs.request).push(r);
  }
  return rs;
}

function servesService(list: string[] | undefined, service: string): boolean {
  if (!list || list.length === 0) return true;
  return service !== "" && list.includes(service);
}

function compileRule(src: Rule): Compiled | undefined {
  if (!VALID_ID.test(src.id)) return undefined;
  const mode = src.mode ?? "";
  if (mode !== "" && mode !== "enforce" && mode !== "monitor") return undefined;
  if (src.action !== "limit" && src.action !== "block" && src.action !== "allow") return undefined;
  const r: Compiled = {
    id: src.id,
    name: src.name || src.id,
    monitor: mode === "monitor",
    action: src.action,
    methods: (src.methods ?? []).map((m) => m.trim().toUpperCase()),
    paths: src.paths ?? [],
    hasIPs: (src.ips ?? []).length > 0,
    prefixes: [],
    countries: (src.countries ?? []).map((c) => c.trim().toUpperCase()),
    match: [],
    key: { kind: "ip", name: "" },
    limit: 0,
    window: 0,
    alg: "sliding",
    burst: 0,
    limits: new Map(),
    status: src.status ?? 0,
    body: src.body ?? "",
    contentType: src.content_type ?? "",
    userPhase: false,
    counters: new Map(),
  };
  for (const s of src.ips ?? []) {
    const p = parsePrefix(s);
    if (p) r.prefixes.push(p);
  }
  for (const [name, values] of Object.entries(src.match ?? {})) {
    const p = parseProp(name);
    if (!p || p.kind === "global") return undefined;
    r.match.push({ p, values: values ?? [] });
    r.userPhase ||= userPhase(p);
  }
  if (src.action !== "limit") return r;
  const key = parseProp(src.key || "ip");
  if (!key) return undefined;
  r.key = key;
  r.userPhase ||= userPhase(key);
  const w = src.window_s ?? 0;
  if (!(w >= 1 && w <= MAX_WINDOW)) return undefined;
  r.window = w;
  r.limit = src.limit ?? 0;
  r.burst = src.burst ?? 0;
  const alg = src.algorithm || "sliding";
  if (alg !== "sliding" && alg !== "fixed" && alg !== "token_bucket") return undefined;
  r.alg = alg;
  if (src.limit_by) {
    const p = parseProp(src.limit_by);
    if (!p || p.kind === "global") return undefined;
    r.limitBy = p;
    for (const [v, n] of Object.entries(src.limits ?? {})) if (typeof n === "number") r.limits.set(v, n);
    r.userPhase ||= userPhase(p);
  }
  return r;
}

/** What the limiter sees of a request; header() and query() return the
 * first value, "" when absent. Header names come lower-cased. */
export interface LimitRequest {
  method: string;
  path: string;
  remoteIp: string;
  header(name: string): string;
  query(name: string): string;
}

export interface LimitUser {
  id: string;
  role: string;
}

/** The answer for a limited or blocked request. */
export interface Denial {
  status: number;
  ruleId: string;
  /** Seconds; 0 for block. */
  retryAfter: number;
  body: string;
  contentType: string;
}

/** Carries one request between the two phases. */
export interface LimitState {
  set?: RuleSet;
  ip: string;
  country: string;
  allowed: boolean;
  userDone: boolean;
  denial?: Denial;
}

/** One rate_limit_events row. */
export interface LimitEvent {
  createdAt: number;
  ruleId: string;
  ruleName: string;
  outcome: Outcome;
  keyValue: string;
  ip: string;
  country: string;
  method: string;
  path: string;
  userId: string;
  hits: number;
}

const passing = (): LimitState => ({ ip: "", country: "", allowed: false, userDone: false });

/** Evaluates the rules in force and keeps the counters. Times are seconds. */
export class Limiter {
  private set?: RuleSet;
  private shapes = new Map<string, string>();
  private redact = new Set<string>();
  private agg = new Map<string, LimitEvent>();
  /** @internal Sees every recorded outcome (tests). */
  trace?: (ruleId: string, outcome: Outcome) => void;

  constructor(private readonly service = "") {
    this.setRedactHeaders(DEFAULT_REDACT);
  }

  /** Headers whose values are never recorded as keys. */
  setRedactHeaders(names: string[]): void {
    this.redact = new Set(names.map((n) => n.trim().toLowerCase()));
  }

  /** Puts a config in force, keeping the counters of rules whose key,
   * window, algorithm and burst did not change. */
  load(c: RateLimitConfig): void {
    const next = compile(c, this.service);
    const old = this.set;
    const shapes = new Map<string, string>();
    for (const [id, r] of next.byId) {
      const s = shape(r);
      shapes.set(id, s);
      const prev = old?.byId.get(id);
      if (prev && this.shapes.get(id) === s) r.counters = prev.counters;
    }
    this.shapes = shapes;
    this.set = next;
  }

  clear(): void {
    this.load({ rules: [] });
  }

  get active(): boolean {
    return !!this.set && this.set.byId.size > 0;
  }

  /** The request phase; the state goes to checkUser later. */
  checkRequest(req: LimitRequest, t: number): LimitState {
    const set = this.set;
    if (!set || set.byId.size === 0) return passing();
    try {
      const st: LimitState = { set, ip: clientIp(req, set.ipHeader), country: country(req, set.countryHeaders), allowed: false, userDone: false };
      this.run(set.request, st, req, undefined, undefined, t);
      return st;
    } catch {
      return passing();
    }
  }

  /** The user phase, once per request; later calls repeat the first outcome. */
  checkUser(st: LimitState, req: LimitRequest, user: LimitUser, attrs: Record<string, string> | undefined, t: number): Denial | undefined {
    if (!st.set) return undefined;
    if (st.userDone || st.allowed || st.denial) return st.denial;
    st.userDone = true;
    if (st.set.user.length === 0) return undefined;
    try {
      this.run(st.set.user, st, req, user, attrs ?? {}, t);
      return st.denial;
    } catch {
      st.denial = undefined;
      return undefined;
    }
  }

  private run(rules: Compiled[], st: LimitState, req: LimitRequest, user: LimitUser | undefined, attrs: Record<string, string> | undefined, t: number): void {
    for (const r of rules) {
      if (!matches(r, st, req, user, attrs)) continue;
      if (r.action === "allow") {
        if (!r.monitor) {
          st.allowed = true;
          return;
        }
      } else if (r.action === "block") {
        if (r.monitor) {
          this.record(r, "would_block", "", st, req, user, t);
          continue;
        }
        this.record(r, "blocked", "", st, req, user, t);
        st.denial = deny(r, 0);
        return;
      } else {
        const key = value(r.key, st, req, user, attrs);
        if (key === "") continue;
        let limit = r.limit;
        if (r.limitBy) {
          const n = r.limits.get(value(r.limitBy, st, req, user, attrs));
          if (n !== undefined) limit = n;
        }
        if (limit < 0) continue;
        const retry = hit(r, key, limit, t);
        if (retry === undefined) continue;
        if (r.monitor) {
          this.record(r, "would_limit", key, st, req, user, t);
          continue;
        }
        this.record(r, "limited", key, st, req, user, t);
        st.denial = deny(r, retry);
        return;
      }
    }
  }

  /** Drops idle counters of every rule. */
  sweep(t: number): void {
    for (const r of this.set?.byId.values() ?? []) sweepRule(r, t);
  }

  private record(r: Compiled, outcome: Outcome, key: string, st: LimitState, req: LimitRequest, user: LimitUser | undefined, t: number): void {
    this.trace?.(r.id, outcome);
    if (key !== "" && r.key.kind === "header" && this.redact.has(r.key.name)) {
      key = "sha256:" + createHash("sha256").update(key).digest("hex").slice(0, 12);
    }
    key = clip(key, 200);
    let ip = st.ip;
    let id = `${r.id}\0${outcome}\0${key}\0${ip}`;
    let e = this.agg.get(id);
    if (!e && this.agg.size >= MAX_AGGREGATES) {
      key = "(other)";
      ip = "";
      id = `${r.id}\0${outcome}\0${key}\0`;
      e = this.agg.get(id);
    }
    if (!e) {
      e = {
        createdAt: Math.round(t * 1000),
        ruleId: r.id,
        ruleName: r.name,
        outcome,
        keyValue: key,
        ip,
        country: st.country,
        method: req.method.toUpperCase(),
        path: clip(req.path, 300),
        userId: user?.id ?? "",
        hits: 0,
      };
      this.agg.set(id, e);
    }
    e.hits++;
  }

  /** The outcomes recorded since the last call, oldest first. */
  drain(): LimitEvent[] {
    const out = [...this.agg.values()];
    this.agg = new Map();
    return out;
  }
}

function matches(r: Compiled, st: LimitState, req: LimitRequest, user: LimitUser | undefined, attrs: Record<string, string> | undefined): boolean {
  if (r.methods.length > 0 && !r.methods.includes(req.method.toUpperCase())) return false;
  if (r.paths.length > 0 && !r.paths.some((g) => glob(g, req.path))) return false;
  if (r.hasIPs && !inPrefixes(r.prefixes, st.ip)) return false;
  if (r.countries.length > 0 && (st.country === "" || !r.countries.includes(st.country))) return false;
  for (const c of r.match) {
    const v = value(c.p, st, req, user, attrs);
    if (c.values.length === 0 ? v === "" : !c.values.includes(v)) return false;
  }
  return true;
}

function value(p: Prop, st: LimitState, req: LimitRequest, user: LimitUser | undefined, attrs: Record<string, string> | undefined): string {
  switch (p.kind) {
    case "ip":
      return st.ip;
    case "country":
      return st.country;
    case "method":
      return req.method.toUpperCase();
    case "path":
      return req.path;
    case "header":
      return req.header(p.name);
    case "query":
      return req.query(p.name);
    case "global":
      return "*";
    case "user":
      return user?.id ?? "";
    case "role":
      return user?.role ?? "";
    case "attr":
      return attrs && Object.prototype.hasOwnProperty.call(attrs, p.name) ? attrs[p.name]! : "";
  }
}

function deny(r: Compiled, retry: number): Denial {
  const d: Denial = { status: r.status || (r.action === "block" ? 403 : 429), ruleId: r.id, retryAfter: retry, body: "", contentType: "application/json" };
  if (r.body !== "") {
    d.body = r.body.split("{retry_after}").join(String(retry));
    const b = r.body.replace(/^[ \t\r\n]+/, "");
    d.contentType = b.startsWith("{") || b.startsWith("[") ? "application/json" : "text/plain; charset=utf-8";
  } else if (r.action === "block") {
    d.body = `{"error":{"code":"blocked","key":"${r.id}","message":"Forbidden"}}`;
  } else {
    d.body = `{"error":{"code":"rate_limited","key":"${r.id}","message":"Too many requests"}}`;
  }
  if (r.contentType !== "") d.contentType = r.contentType;
  return d;
}

const capacity = (r: Compiled, limit: number) => (r.burst > 0 ? r.burst : limit);

/** Takes one request on key's counter; the seconds to wait when over, else undefined. */
function hit(r: Compiled, key: string, limit: number, t: number): number | undefined {
  const W = r.window;
  if (limit === 0) return Math.ceil(W);
  const cs = r.counters;
  let c = cs.get(key);
  if (!c) {
    if (cs.size >= MAX_COUNTERS) {
      sweepRule(r, t);
      if (cs.size >= MAX_COUNTERS) cs.clear();
    }
    c = { win: null, curr: 0, prev: 0, tokens: capacity(r, limit), last: t };
    cs.set(key, c);
  }
  if (r.alg === "token_bucket") {
    const C = capacity(r, limit);
    const R = limit / W;
    if (t > c.last) {
      c.tokens = Math.min(C, c.tokens + (t - c.last) * R);
      c.last = t;
    }
    if (c.tokens < 1) return Math.max(1, Math.ceil((1 - c.tokens) / R));
    c.tokens--;
    return undefined;
  }
  const w = Math.floor(t / W);
  if (c.win === null) {
    c.prev = c.curr = 0;
  } else if (w === c.win + 1) {
    c.prev = c.curr;
    c.curr = 0;
  } else if (w > c.win + 1) {
    c.prev = c.curr = 0;
  }
  c.win = c.win === null ? w : Math.max(c.win, w);
  const over = r.alg === "fixed" ? c.curr >= limit : (c.prev * (W - (t - w * W))) / W + c.curr + 1 > limit;
  if (over) return Math.max(1, Math.ceil((w + 1) * W - t));
  c.curr++;
  return undefined;
}

function sweepRule(r: Compiled, t: number): void {
  if (r.window <= 0) return;
  const w = Math.floor(t / r.window);
  for (const [k, c] of r.counters) {
    if (r.alg === "token_bucket") {
      // the limit may differ per key; the static one is close enough to call it idle
      if (r.limit <= 0 || c.tokens + (t - c.last) * (r.limit / r.window) >= capacity(r, r.limit)) r.counters.delete(k);
    } else if (c.win !== null && c.win < w - 1) {
      r.counters.delete(k);
    }
  }
}

function clientIp(req: LimitRequest, header: string): string {
  if (header !== "") {
    const first = req.header(header.toLowerCase()).split(",")[0]!.trim();
    if (first !== "") return first;
  }
  return req.remoteIp;
}

function country(req: LimitRequest, headers: string[]): string {
  for (const h of headers) {
    const v = req.header(h.toLowerCase()).trim();
    if (v !== "") return v.toUpperCase();
  }
  return "";
}

/** An address as 4 or 16 bytes; IPv4-mapped IPv6 comes back as IPv4. */
function parseAddr(s: string): Uint8Array | undefined {
  const zone = s.indexOf("%");
  if (zone >= 0 && s.includes(":")) s = s.slice(0, zone);
  const v = isIP(s);
  if (v === 4) return Uint8Array.from(s.split(".").map(Number));
  if (v !== 6) return undefined;
  let tail: number[] = [];
  let text = s;
  const dot = s.lastIndexOf(":");
  if (s.includes(".", dot)) {
    tail = s.slice(dot + 1).split(".").map(Number);
    text = s.slice(0, dot + 1) + "0:0";
  }
  const [head, rest] = text.split("::") as [string, string | undefined];
  const groups = (part: string) => (part === "" ? [] : part.split(":").map((g) => parseInt(g, 16)));
  const left = groups(head);
  const right = rest === undefined ? [] : groups(rest);
  const all = [...left, ...Array(8 - left.length - right.length).fill(0), ...right];
  const out = new Uint8Array(16);
  all.forEach((g, i) => {
    out[i * 2] = g >> 8;
    out[i * 2 + 1] = g & 0xff;
  });
  if (tail.length === 4) out.set(tail, 12);
  const mapped = out.subarray(0, 10).every((b) => b === 0) && out[10] === 0xff && out[11] === 0xff;
  return mapped ? out.slice(12) : out;
}

interface Prefix {
  addr: Uint8Array;
  bits: number;
}

function parsePrefix(s: string): Prefix | undefined {
  s = s.trim();
  const slash = s.indexOf("/");
  if (slash < 0) {
    const addr = parseAddr(s);
    return addr && { addr, bits: addr.length * 8 };
  }
  const text = s.slice(0, slash);
  const bitsText = s.slice(slash + 1);
  if (text.includes("%") || !/^\d{1,3}$/.test(bitsText)) return undefined;
  let bits = Number(bitsText);
  const v = isIP(text);
  if (v === 0 || bits > (v === 4 ? 32 : 128)) return undefined;
  let addr = parseAddr(text)!;
  if (v === 6 && addr.length === 4) {
    // ::ffff:a.b.c.d/n compares as IPv4 when n covers the mapping
    if (bits < 96) return { addr: mapTo6(addr), bits };
    bits -= 96;
  }
  return { addr: masked(addr, bits), bits };
}

function mapTo6(a: Uint8Array): Uint8Array {
  const out = new Uint8Array(16);
  out[10] = out[11] = 0xff;
  out.set(a, 12);
  return masked(out, 0);
}

function masked(a: Uint8Array, bits: number): Uint8Array {
  const out = Uint8Array.from(a);
  for (let i = 0; i < out.length; i++) {
    const keep = Math.max(0, Math.min(8, bits - i * 8));
    out[i]! &= (0xff << (8 - keep)) & 0xff;
  }
  return out;
}

function inPrefixes(list: Prefix[], ip: string): boolean {
  const a = parseAddr(ip.trim());
  if (!a) return false;
  return list.some((p) => p.addr.length === a.length && masked(a, p.bits).every((b, i) => b === p.addr[i]));
}

/** Matches a whole path against a pattern where * is any run of characters. */
export function glob(pattern: string, s: string): boolean {
  let p = 0;
  let i = 0;
  let star = -1;
  let mark = 0;
  while (i < s.length) {
    if (p < pattern.length && pattern[p] === "*") {
      star = p++;
      mark = i;
    } else if (p < pattern.length && pattern[p] === s[i]) {
      p++;
      i++;
    } else if (star >= 0) {
      p = star + 1;
      i = ++mark;
    } else {
      return false;
    }
  }
  while (p < pattern.length && pattern[p] === "*") p++;
  return p === pattern.length;
}

function clip(s: string, n: number): string {
  if (Buffer.byteLength(s) <= n) return s;
  const cps = Array.from(s);
  return cps.length <= n ? s : cps.slice(0, n).join("");
}
