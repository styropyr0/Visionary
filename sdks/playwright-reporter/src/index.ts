import { randomBytes } from "node:crypto";
import { readFile, stat } from "node:fs/promises";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import type { PWAnnotation, PWAttachment, PWStep, PWTestCase, PWTestResult } from "./types.js";

export type { PWTestCase, PWTestResult } from "./types.js";

declare const __REPORTER_VERSION__: string;

export interface VisionaryReporterOptions {
  /** Visionary's address. Default: VISIONARY_URL. */
  server?: string;
  /** An API key with the CI scope. Default: VISIONARY_KEY. */
  key?: string;
  /** How often runs arrive, in seconds or "10m", "1h", "1d"; a check with no run for longer goes stale. Applies to new checks only. */
  expectedInterval?: string | number;
  /** Shown with each run. Default: VISIONARY_RELEASE. */
  release?: string;
  /** Shown with each run. Default: VISIONARY_COMMIT, then the CI's commit variable. */
  commit?: string;
  /** Upload a failed test's screenshot when it is at most maxScreenshotBytes. Default true. */
  screenshots?: boolean;
  /** Upload the trace when it is at most maxTraceBytes. Default true. */
  traces?: boolean;
  maxScreenshotBytes?: number;
  maxTraceBytes?: number;
  /** Per request. Default 15000. */
  timeoutMs?: number;
  /** Print nothing but problems. Default false. */
  quiet?: boolean;
  /** For tests. */
  fetch?: typeof fetch;
}

/** The server keeps screenshots and traces up to these sizes. */
const SCREENSHOT_LIMIT = 256 * 1024;
const TRACE_LIMIT = 768 * 1024;
const MAX_STEPS = 100;
const PARALLEL = 4;

/** One test run as the server takes it (POST /api/uptime/playwright). */
export interface RunPayload {
  project: string;
  title: string;
  ok: boolean;
  status: string;
  outcome: string;
  duration_ms: number;
  started_at: string;
  retry: number;
  error?: string;
  steps: { title: string; category: string; duration_ms: number; error?: string; depth?: number }[];
  attachments: { name: string; content_type: string; size: number; body?: string }[];
  browser?: string;
  trace_id?: string;
  url?: string;
  release?: string;
  commit?: string;
  expected_interval_s?: number;
  tags?: string[];
}

/**
 * Sends each test's final result (after its retries) to Visionary, where it
 * becomes a check of kind playwright keyed by project and title path.
 * Reporting never fails the test run: problems are printed and skipped.
 */
export default class VisionaryReporter {
  private readonly opts: VisionaryReporterOptions;
  private readonly server: string;
  private readonly key: string;
  private readonly send: typeof fetch;
  private readonly queue: (() => Promise<void>)[] = [];
  private active = 0;
  private idle: (() => void)[] = [];
  private sent = 0;
  private failed = 0;
  private disabled = "";

  constructor(options: VisionaryReporterOptions = {}) {
    this.opts = options;
    this.server = (options.server ?? process.env.VISIONARY_URL ?? "").trim().replace(/\/+$/, "");
    this.key = (options.key ?? process.env.VISIONARY_KEY ?? "").trim();
    this.send = options.fetch ?? post;
    if (!this.server) this.disabled = "set server in the reporter options or VISIONARY_URL";
    else if (!this.key) this.disabled = "set key in the reporter options or VISIONARY_KEY (an API key with the CI scope)";
  }

  printsToStdio(): boolean {
    return false;
  }

  onBegin(): void {
    if (this.disabled) this.log(`not reporting to Visionary: ${this.disabled}`, true);
  }

  onTestEnd(test: PWTestCase, result: PWTestResult): void {
    if (this.disabled || result.status === "skipped" || !isFinal(test, result)) return;
    this.enqueue(async () => {
      const payload = await buildPayload(test, result, this.opts);
      await this.post(payload);
    });
  }

  async onEnd(): Promise<void> {
    await this.drain();
    if (!this.disabled && (this.sent || this.failed)) {
      this.log(`reported ${this.sent} test${this.sent === 1 ? "" : "s"} to ${this.server}${this.failed ? `, ${this.failed} not reported` : ""}`, this.failed > 0);
    }
  }

  private enqueue(job: () => Promise<void>): void {
    this.queue.push(job);
    this.pump();
  }

  private pump(): void {
    while (this.active < PARALLEL && this.queue.length) {
      const job = this.queue.shift()!;
      this.active++;
      job()
        .catch((err: unknown) => {
          this.failed++;
          this.log(`a test was not reported: ${err instanceof Error ? err.message : String(err)}`, true);
        })
        .finally(() => {
          this.active--;
          this.pump();
          if (this.active === 0 && this.queue.length === 0) this.idle.splice(0).forEach((f) => f());
        });
    }
  }

  private drain(): Promise<void> {
    if (this.active === 0 && this.queue.length === 0) return Promise.resolve();
    return new Promise((resolve) => this.idle.push(resolve));
  }

  private async post(payload: RunPayload): Promise<void> {
    const body = JSON.stringify(payload);
    let last = "";
    for (let attempt = 0; attempt < 3; attempt++) {
      if (attempt > 0) await new Promise((r) => setTimeout(r, 500 * 2 ** attempt));
      let res: Response;
      try {
        res = await this.send(`${this.server}/api/uptime/playwright`, {
          method: "POST",
          headers: { Authorization: `Bearer ${this.key}`, "Content-Type": "application/json", "User-Agent": `visionary-playwright-reporter/${__REPORTER_VERSION__}` },
          body,
          signal: AbortSignal.timeout(this.opts.timeoutMs ?? 15000),
        });
      } catch (err) {
        last = err instanceof Error ? err.message : String(err);
        continue;
      }
      const text = await res.text();
      if (res.status >= 500 || res.status === 429) {
        last = `HTTP ${res.status}`;
        continue;
      }
      if (!res.ok) throw new Error(`${payload.title}: ${errorOf(text) || `HTTP ${res.status}`}`);
      const out = parse(text) as { results?: { status: string; error?: string }[] } | undefined;
      const r = out?.results?.[0];
      if (r?.status === "error") throw new Error(`${payload.title}: ${r.error}`);
      this.sent++;
      return;
    }
    throw new Error(`${payload.title}: ${last}`);
  }

  private log(msg: string, problem = false): void {
    if (problem || !this.opts.quiet) console.error(`[visionary] ${msg}`);
  }
}

/** Only the last attempt counts: the one that passed, or the last retry. */
export function isFinal(test: PWTestCase, result: PWTestResult): boolean {
  return result.status === test.expectedStatus || result.status === "skipped" || result.retry >= test.retries || result.status === "interrupted";
}

export async function buildPayload(test: PWTestCase, result: PWTestResult, opts: VisionaryReporterOptions = {}): Promise<RunPayload> {
  const project = test.parent.project();
  const projectName = project?.name ?? "";
  const path = test.titlePath().filter(Boolean);
  if (projectName && path[0] === projectName) path.shift();
  const ok = result.status === test.expectedStatus && result.status !== "skipped";
  const p: RunPayload = {
    project: projectName,
    title: path.join(" › "),
    ok,
    status: result.status,
    outcome: test.outcome(),
    duration_ms: Math.max(0, Math.round(result.duration)),
    started_at: result.startTime.toISOString(),
    retry: result.retry,
    steps: flattenSteps(result.steps),
    attachments: await attachments(result.attachments, opts),
  };
  const error = errorText(result);
  if (error && !ok) p.error = error;
  const use = project?.use;
  const browser = use?.channel || use?.browserName || use?.defaultBrowserType;
  if (browser) p.browser = browser;
  if (use?.baseURL) p.url = use.baseURL;
  const trace = traceIdOf([...(test.annotations ?? []), ...(result.annotations ?? [])], result.attachments);
  if (trace) p.trace_id = trace;
  const release = opts.release ?? env("VISIONARY_RELEASE");
  if (release) p.release = release;
  const commit = opts.commit ?? env("VISIONARY_COMMIT", "GITHUB_SHA", "CI_COMMIT_SHA", "BUILDKITE_COMMIT", "CIRCLE_SHA1", "GIT_COMMIT");
  if (commit) p.commit = commit;
  const interval = parseInterval(opts.expectedInterval);
  if (interval) p.expected_interval_s = interval;
  if (test.tags?.length) p.tags = test.tags;
  return p;
}

const ANSI = /\u001b\[[0-9;]*[A-Za-z]/g;

function errorText(result: PWTestResult): string {
  const errs = result.errors?.length ? result.errors : result.error ? [result.error] : [];
  return errs
    .map((e) => (e.message ?? e.value ?? "").replace(ANSI, "").trim())
    .filter(Boolean)
    .join("\n\n")
    .slice(0, 4000);
}

/**
 * Test steps and the actions in them, in order. Hooks are left out but their
 * actions kept; fixtures only when they failed. Depth is how deep a step sits
 * in test.step().
 */
export function flattenSteps(steps: PWStep[]): RunPayload["steps"] {
  const out: RunPayload["steps"] = [];
  const walk = (list: PWStep[], depth: number) => {
    for (const s of list) {
      if (out.length >= MAX_STEPS) return;
      const shown = s.category === "test.step" || s.category === "pw:api" || (s.category === "expect" && !!s.error);
      if (shown) {
        const step: RunPayload["steps"][number] = { title: s.title.slice(0, 300), category: s.category, duration_ms: Math.max(0, Math.round(s.duration)) };
        const err = (s.error?.message ?? s.error?.value ?? "").replace(ANSI, "").trim();
        if (err) step.error = err.slice(0, 1000);
        if (depth) step.depth = depth;
        out.push(step);
      }
      if (s.steps?.length && (s.category !== "fixture" || s.error)) walk(s.steps, shown && s.category === "test.step" ? depth + 1 : depth);
    }
  };
  walk(steps, 0);
  return out;
}

async function attachments(list: PWAttachment[], opts: VisionaryReporterOptions): Promise<RunPayload["attachments"]> {
  const maxShot = Math.min(opts.maxScreenshotBytes ?? SCREENSHOT_LIMIT, SCREENSHOT_LIMIT);
  const maxTrace = Math.min(opts.maxTraceBytes ?? TRACE_LIMIT, TRACE_LIMIT);
  let shot = false;
  let trace = false;
  const out: RunPayload["attachments"] = [];
  for (const a of list.slice(0, 20)) {
    let size = a.body?.length ?? 0;
    if (!a.body && a.path) {
      try {
        size = (await stat(a.path)).size;
      } catch {
        size = 0;
      }
    }
    const item: RunPayload["attachments"][number] = { name: a.name, content_type: a.contentType, size };
    const isShot = !shot && opts.screenshots !== false && (a.contentType === "image/png" || a.contentType === "image/jpeg") && size > 0 && size <= maxShot;
    const isTrace = !trace && opts.traces !== false && a.name === "trace" && a.contentType === "application/zip" && size > 0 && size <= maxTrace;
    if (isShot || isTrace) {
      const body = a.body ?? (a.path ? await readFile(a.path).catch(() => undefined) : undefined);
      if (body) {
        item.body = body.toString("base64");
        if (isShot) shot = true;
        else trace = true;
      }
    }
    out.push(item);
  }
  return out;
}

const TRACEPARENT = /^[0-9a-f]{2}-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}/i;

/** The trace id from a traceparent or trace_id annotation, or a traceparent attachment. */
export function traceIdOf(annotations: PWAnnotation[], atts: PWAttachment[] = []): string | undefined {
  const values: string[] = [];
  for (const a of annotations) {
    if ((a.type === "traceparent" || a.type === "trace_id") && a.description) values.push(a.description.trim());
  }
  for (const a of atts) {
    if (a.name === "traceparent" && a.body) values.push(a.body.toString("utf8").trim());
  }
  for (const v of values) {
    const m = TRACEPARENT.exec(v);
    if (m && !/^0+$/.test(m[1])) return m[1].toLowerCase();
    if (/^[0-9a-f]{32}$/i.test(v) && !/^0+$/.test(v)) return v.toLowerCase();
  }
  return undefined;
}

/**
 * A new W3C traceparent for a test: send `header` with the test's requests
 * (extraHTTPHeaders) and add it as a `traceparent` annotation, and the run
 * links to the trace your services recorded.
 */
export function newTraceparent(): { header: string; traceId: string } {
  const traceId = randomBytes(16).toString("hex");
  return { header: `00-${traceId}-${randomBytes(8).toString("hex")}-01`, traceId };
}

/** Seconds from 600, "600", "10m", "1h" or "1d". */
export function parseInterval(v: string | number | undefined): number | undefined {
  if (v === undefined || v === "") return undefined;
  if (typeof v === "number") return v > 0 ? Math.round(v) : undefined;
  const m = /^\s*(\d+)\s*([smhd]?)\s*$/i.exec(v);
  if (!m) return undefined;
  const mult = { "": 1, s: 1, m: 60, h: 3600, d: 86400 }[m[2].toLowerCase() as "" | "s" | "m" | "h" | "d"];
  return Number(m[1]) * mult || undefined;
}

/**
 * A POST without keep-alive. Playwright exits right after onEnd, and on
 * Windows the global fetch's pooled sockets closing at that moment can crash
 * Node and replace the test run's exit code.
 */
const post: typeof fetch = (input, init) =>
  new Promise<Response>((resolve, reject) => {
    const url = new URL(String(input));
    const send = url.protocol === "https:" ? httpsRequest : httpRequest;
    const body = String(init?.body ?? "");
    const headers = { ...(init?.headers as Record<string, string>), "Content-Length": String(Buffer.byteLength(body)), Connection: "close" };
    const req = send(url, { method: init?.method ?? "POST", headers, agent: false }, (res) => {
      const chunks: Buffer[] = [];
      res.on("data", (c: Buffer) => chunks.push(c));
      res.on("end", () => resolve(new Response(Buffer.concat(chunks).toString("utf8"), { status: res.statusCode ?? 0 })));
      res.on("error", reject);
    });
    req.on("error", reject);
    const signal = init?.signal;
    if (signal) {
      if (signal.aborted) req.destroy(new Error("timed out"));
      signal.addEventListener("abort", () => req.destroy(new Error("timed out")), { once: true });
    }
    req.end(body);
  });

function env(...names: string[]): string | undefined {
  for (const n of names) {
    const v = process.env[n]?.trim();
    if (v) return v;
  }
  return undefined;
}

function parse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function errorOf(text: string): string {
  const v = parse(text) as { error?: string } | undefined;
  return v?.error ?? "";
}
