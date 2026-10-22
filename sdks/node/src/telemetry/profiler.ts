import { Session } from "node:inspector";
import { cwd } from "node:process";
import { requestTally } from "../context.js";
import type { Logger } from "../log.js";
import type { Value } from "../store/sql.js";
import { formatTs } from "../util.js";

export interface ProfilerConfig {
  service: string;
  instance: string;
  version: string;
}

/** Folded stacks are capped at this many bytes, so a row stays small for any
 * database. The heaviest are kept whole up to KEEP_WHOLE; the rest are
 * rolled up into their callers (see foldLines). */
const MAX_FOLDED = 90_000;
const KEEP_WHOLE = 72_000;
const MIN_SAMPLES = 20;
/** How often the dashboard's settings are read again. */
const POLL_MS = 30_000;
const TICK_MS = 5_000;
/** A "Profile now" older than this is ignored (the SDK was down meanwhile). */
const NOW_FRESH_MS = 5 * 60_000;

interface CpuNode {
  id: number;
  callFrame: { functionName: string; url: string; lineNumber: number };
  children?: number[];
}

/** What the dashboard asks for (Settings → Profiling), from visionary_settings. */
export interface ProfileSettings {
  enabled: boolean;
  services: string[];
  intervalS: number;
  durationMs: number;
  now: string;
}

export function parseSettings(rows: string[][]): ProfileSettings {
  const m = new Map(rows.map(([k, v]) => [k, v ?? ""]));
  let services: string[] = [];
  try {
    const v = JSON.parse(m.get("profile.services") || "[]");
    if (Array.isArray(v)) services = v.map(String);
  } catch {
    // a broken list means every service
  }
  const num = (k: string, d: number) => {
    const n = Number(m.get(k));
    return Number.isFinite(n) && n > 0 ? n : d;
  };
  return {
    enabled: m.get("profile.enabled") === "true",
    services,
    intervalS: num("profile.interval_s", 300),
    durationMs: num("profile.duration_ms", 10_000),
    now: m.get("profile.now") ?? "",
  };
}

/** Whether a service takes part: an empty list means every service. */
export function servesProfile(settings: ProfileSettings, service: string): boolean {
  return settings.services.length === 0 || (service !== "" && settings.services.includes(service));
}

/**
 * Samples the process's CPU now and then, while the dashboard has profiling
 * on (Settings → Profiling) and only while requests are being served: it
 * skips a turn when no request arrived since the last one, and drops a
 * profile during which none finished. "Profile now" in the dashboard takes
 * one at once. Each profile is stored as folded stacks ("frame;frame;frame
 * samples" per line) with the time each route's requests took while it ran.
 */
export class Profiler {
  private timer?: NodeJS.Timeout;
  private running = false;
  private stopped = false;
  private lastStarted = 0;
  private settings?: ProfileSettings;
  private polledAt = 0;
  private nextAt = 0;
  private seenNow = "";
  private on = false;
  private readonly startedAt = Date.now();

  constructor(
    private readonly cfg: ProfilerConfig,
    private readonly read: () => Promise<string[][]>,
    private readonly push: (args: Value[]) => void,
    private readonly logger: Logger,
  ) {}

  start(): void {
    this.timer = setInterval(() => void this.tick(), TICK_MS);
    this.timer.unref();
    void this.tick();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearInterval(this.timer);
  }

  private async tick(): Promise<void> {
    if (this.stopped || this.running) return;
    const now = Date.now();
    if (now - this.polledAt >= POLL_MS) {
      this.polledAt = now;
      try {
        this.settings = parseSettings(await this.read());
      } catch {
        this.settings = undefined; // no table yet: the Visionary server has not started
      }
    }
    const s = this.settings;
    const mine = !!s && servesProfile(s, this.cfg.service);
    const on = mine && !!s?.enabled;
    if (on !== this.on) {
      this.on = on;
      this.logger.info(on ? `visionary: CPU profiles on, every ${s!.intervalS}s while serving requests` : "visionary: CPU profiles off");
      this.nextAt = now;
    }
    if (!s || !mine) return;
    // "Profile now": a newer request than the last one seen, made while this
    // process was up and not long ago.
    if (s.now && s.now !== this.seenNow) {
      this.seenNow = s.now;
      const at = Date.parse(s.now);
      if (at >= this.startedAt && now - at < NOW_FRESH_MS) {
        await this.capture(s.durationMs);
        return;
      }
    }
    if (!on || now < this.nextAt) return;
    this.nextAt = now + s.intervalS * 1000;
    if (requestTally.started === this.lastStarted) return;
    await this.capture(s.durationMs);
  }

  private async capture(durationMs: number): Promise<void> {
    if (this.running) return;
    this.lastStarted = requestTally.started;
    this.running = true;
    const session = new Session();
    const post = (method: string, params?: object) =>
      new Promise<any>((resolve, reject) => session.post(method, params ?? {}, (err, res) => (err ? reject(err) : resolve(res))));
    requestTally.busy = {};
    const started = Date.now();
    try {
      session.connect();
      await post("Profiler.enable");
      await post("Profiler.setSamplingInterval", { interval: 1000 });
      await post("Profiler.start");
      await new Promise((r) => setTimeout(r, durationMs).unref());
      const { profile } = await post("Profiler.stop");
      await post("Profiler.disable");
      const { folded, samples } = fold(profile);
      const routes: Record<string, number> = {};
      for (const [k, ms] of Object.entries(requestTally.busy ?? {})) routes[k] = Math.max(1, Math.round(ms));
      if (samples < MIN_SAMPLES || Object.keys(routes).length === 0) return;
      this.push([
        formatTs(started),
        this.cfg.service || null,
        this.cfg.instance,
        this.cfg.version || null,
        "node",
        Date.now() - started,
        samples,
        JSON.stringify(routes),
        folded,
      ]);
    } catch (e) {
      this.logger.warn(`visionary: CPU profile failed: ${(e as Error).message}`);
    } finally {
      requestTally.busy = null;
      try {
        session.disconnect();
      } catch {
        // already closed
      }
      this.running = false;
    }
  }
}

/** A file path without the file:// scheme or leading slash, with / separators. */
function plainPath(p: string): string {
  return p.replace(/^file:\/\//, "").replaceAll("\\", "/").replace(/^\/+/, "");
}

const root = plainPath(cwd()).toLowerCase();

/** "fn file:line", with paths cut to what identifies the code: relative to
 * the working directory, or "npm:package/file" inside node_modules. */
function frameName(n: CpuNode): string {
  const f = n.callFrame;
  const fn = f.functionName || "(anonymous)";
  if (!f.url) return fn;
  let url = plainPath(f.url);
  const nm = url.lastIndexOf("node_modules/");
  if (nm >= 0) {
    const rest = url.slice(nm + "node_modules/".length).split("/");
    const pkg = rest[0]?.startsWith("@") ? rest.slice(0, 2).join("/") : rest[0];
    url = `npm:${pkg}/${rest[rest.length - 1]}`;
  } else if (url.toLowerCase().startsWith(root + "/")) {
    url = url.slice(root.length + 1);
  }
  // ";" separates frames in the folded format.
  return `${fn} ${url}:${f.lineNumber + 1}`.replaceAll(";", ",");
}

/** A V8 CPU profile as folded stacks, idle time left out. */
export function fold(profile: { nodes: CpuNode[]; samples?: number[] }): { folded: string; samples: number } {
  const byId = new Map<number, CpuNode>();
  const parent = new Map<number, number>();
  for (const n of profile.nodes) {
    byId.set(n.id, n);
    for (const c of n.children ?? []) parent.set(c, n.id);
  }
  const counts = new Map<number, number>();
  for (const id of profile.samples ?? []) counts.set(id, (counts.get(id) ?? 0) + 1);
  const lines = new Map<string, number>();
  let samples = 0;
  for (const [id, count] of counts) {
    const leaf = byId.get(id);
    if (!leaf || leaf.callFrame.functionName === "(idle)") continue;
    const stack: string[] = [];
    for (let cur: number | undefined = id; cur !== undefined; cur = parent.get(cur)) {
      const n = byId.get(cur);
      if (!n || n.callFrame.functionName === "(root)") break;
      stack.push(frameName(n));
    }
    const key = stack.reverse().join(";");
    lines.set(key, (lines.get(key) ?? 0) + count);
    samples += count;
  }
  return { folded: foldLines(lines), samples };
}

/** Heaviest first, ties in text order. */
function heaviest(a: [string, number], b: [string, number]): number {
  return b[1] - a[1] || (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0);
}

/** Folded lines within MAX_FOLDED bytes (sdks/SPEC.md, "CPU profiles"):
 * whole lines up to KEEP_WHOLE, then the rest cut after the deepest frame
 * they share with a kept stack and ending in "(other)", then one "(other) n"
 * for whatever is left. */
export function foldLines(lines: Map<string, number>): string {
  const sorted = [...lines].sort(heaviest);
  let out = "";
  let size = 0;
  // Whole lines first, leaving room for the rolled-up ones.
  let i = 0;
  for (; i < sorted.length; i++) {
    const line = `${sorted[i][0]} ${sorted[i][1]}\n`;
    const len = Buffer.byteLength(line);
    if (size + len > KEEP_WHOLE) break;
    out += line;
    size += len;
  }
  if (i === sorted.length) return out;
  // The rest are cut after the deepest frame they share with a kept stack
  // and end in "(other)", so their time stays under the right caller.
  const prefixes = new Set<string>();
  for (const [k] of sorted.slice(0, i)) {
    for (let j = k.indexOf(";"); j >= 0; j = k.indexOf(";", j + 1)) prefixes.add(k.slice(0, j));
    prefixes.add(k);
  }
  const rolled = new Map<string, number>();
  let other = 0;
  for (const [k, n] of sorted.slice(i)) {
    let cut = "";
    for (let j = 0; j <= k.length; j++) {
      if ((j === k.length || k[j] === ";") && prefixes.has(k.slice(0, j))) cut = k.slice(0, j);
    }
    if (cut === "") {
      other += n;
      continue;
    }
    // The whole stack leads into a kept one: nothing to cut.
    const key = cut === k ? k : `${cut};(other)`;
    rolled.set(key, (rolled.get(key) ?? 0) + n);
  }
  for (const [k, n] of [...rolled].sort(heaviest)) {
    const line = `${k} ${n}\n`;
    const len = Buffer.byteLength(line);
    if (size + len > MAX_FOLDED - 32) {
      other += n; // room for the last "(other) n"
      continue;
    }
    out += line;
    size += len;
  }
  if (other > 0) out += `(other) ${other}\n`;
  return out;
}
