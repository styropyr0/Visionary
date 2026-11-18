import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import type { IncomingMessage, ServerResponse } from "node:http";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import type { AddressInfo } from "node:net";
import { test } from "node:test";
import { Agent } from "../src/agent.js";
import { enterRequest } from "../src/capture.js";
import { resolveConfig } from "../src/config.js";
import { setUser } from "../src/context.js";
import { express } from "../src/integrations/express.js";
import { checkRequest, rateLimited } from "../src/ratelimit/http.js";
import type { LimitRequest, Outcome } from "../src/ratelimit/limiter.js";
import { Limiter, glob, parseConfig } from "../src/ratelimit/limiter.js";
import { RateLimits } from "../src/ratelimit/poller.js";
import type { Stmt } from "../src/store/sql.js";
import { RATE_LIMIT_COLUMNS } from "../src/store/sql.js";
import type { Store } from "../src/store/store.js";

const quiet = { info() {}, warn() {}, error() {} };

interface Step {
  t: number;
  method?: string;
  path?: string;
  remote_ip?: string;
  headers?: Record<string, string>;
  query?: Record<string, string>;
  user?: { id?: string; role?: string };
  attrs?: Record<string, string>;
  expect: { status: number; rule?: string; retry_after?: number; body?: string; content_type?: string };
  records?: string[];
}

interface Case {
  name: string;
  service?: string;
  config: unknown;
  steps: Step[];
}

function stepRequest(s: Step): LimitRequest {
  return {
    method: s.method || "GET",
    path: s.path || "/",
    remoteIp: s.remote_ip || "203.0.113.1",
    header: (name) => Object.entries(s.headers ?? {}).find(([k]) => k.toLowerCase() === name.toLowerCase())?.[1] ?? "",
    query: (name) => s.query?.[name] ?? "",
  };
}

// The cases every SDK runs (sdks/go/ratelimit/testdata/cases.json).
const cases = JSON.parse(
  readFileSync(new URL("../../go/ratelimit/testdata/cases.json", import.meta.url), "utf8"),
) as Case[];

test("shared rate limit cases", async (t) => {
  assert.equal(cases.length, 25);
  for (const c of cases) {
    await t.test(c.name, () => {
      const l = new Limiter(c.service ?? "");
      let records: string[] = [];
      l.trace = (id: string, outcome: Outcome) => records.push(`${id}:${outcome}`);
      l.load(parseConfig(JSON.stringify(c.config)));
      c.steps.forEach((s, i) => {
        records = [];
        const req = stepRequest(s);
        const st = l.checkRequest(req, s.t);
        l.checkUser(st, req, { id: s.user?.id ?? "", role: s.user?.role ?? "" }, s.attrs, s.t);
        const d = st.denial;
        assert.equal(d?.status ?? 0, s.expect.status, `step ${i}: status`);
        if (d) {
          if (s.expect.rule !== undefined) assert.equal(d.ruleId, s.expect.rule, `step ${i}: rule`);
          if (s.expect.retry_after !== undefined) assert.equal(d.retryAfter, s.expect.retry_after, `step ${i}: retry after`);
          if (s.expect.body !== undefined) assert.equal(d.body, s.expect.body, `step ${i}: body`);
          if (s.expect.content_type !== undefined) assert.equal(d.contentType, s.expect.content_type, `step ${i}: content type`);
        }
        assert.deepEqual(records, s.records ?? [], `step ${i}: records`);
      });
    });
  }
});

const req = (ip: string, path = "/"): LimitRequest => ({ method: "GET", path, remoteIp: ip, header: () => "", query: () => "" });

test("counters survive a reload unless the shape changes", () => {
  const l = new Limiter();
  const rule = { id: "r", action: "limit", limit: 1, window_s: 60, algorithm: "fixed" } as Record<string, unknown>;
  l.load(parseConfig(JSON.stringify({ rules: [rule] })));
  l.checkRequest(req("1.1.1.1"), 6000);
  l.load(parseConfig(JSON.stringify({ rules: [{ ...rule, name: "renamed" }] })));
  assert.ok(l.checkRequest(req("1.1.1.1"), 6000).denial, "counter lost on a rename");
  l.load(parseConfig(JSON.stringify({ rules: [{ ...rule, window_s: 30 }] })));
  assert.equal(l.checkRequest(req("1.1.1.1"), 6000).denial, undefined, "counter kept after the window changed");
});

test("events aggregate, redact header keys and overflow into (other)", () => {
  const l = new Limiter("api");
  l.load({ rules: [{ id: "k", name: "Keys", action: "limit", key: "header:X-Api-Key", limit: 0, window_s: 60 }] });
  const r: LimitRequest = { method: "post", path: "/v1/x", remoteIp: "1.1.1.1", header: (n) => (n === "x-api-key" ? "secret-value" : ""), query: () => "" };
  for (let i = 0; i < 3; i++) l.checkRequest(r, 6000);
  const ev = l.drain();
  assert.equal(ev.length, 1);
  assert.equal(ev[0]!.hits, 3);
  assert.equal(ev[0]!.method, "POST");
  assert.match(ev[0]!.keyValue, /^sha256:[0-9a-f]{12}$/);
  assert.equal(l.drain().length, 0);

  const b = new Limiter();
  b.load({ rules: [{ id: "b", action: "block" }] });
  for (let i = 0; i < 1005; i++) b.checkRequest(req(`10.0.${Math.floor(i / 250)}.${i % 250}`), 1);
  const all = b.drain();
  assert.equal(all.length, 1001);
  assert.deepEqual([all[1000]!.keyValue, all[1000]!.ip, all[1000]!.hits], ["(other)", "", 5]);
});

test("the counter cap drops idle counters first", () => {
  const l = new Limiter();
  l.load({ rules: [{ id: "c", action: "limit", limit: 5, window_s: 1, algorithm: "fixed" }] });
  for (let i = 0; i < 10_000; i++) l.checkRequest(req(`k${i}`), 100);
  l.checkRequest(req("new"), 105);
  const rule = (l as unknown as { set: { byId: Map<string, { counters: Map<string, unknown> }> } }).set.byId.get("c")!;
  assert.equal(rule.counters.size, 1);
});

test("parseConfig keeps the rules that decode", () => {
  const c = parseConfig(`{"rules":[{"id":"a","action":"block","limit":"x"},{"id":"b","action":"block"}]}`);
  assert.deepEqual(c.rules.map((r) => r.id), ["b"]);
  assert.throws(() => parseConfig(`{"rules":`));
  assert.ok(glob("/a**b", "/ab") && glob("*.json", "/x/y.json") && !glob("/a", "/a/"));
});

/** A store that answers the two settings reads and keeps what is written. */
function settingsStore(stringParams = false) {
  const s = {
    version: "" as string | null,
    config: "",
    fail: false,
    configReads: 0,
    written: [] as Stmt[][],
  };
  const store: Store = {
    dialect: "sqlite",
    stringParams,
    async execScript() {},
    async batch(stmts) {
      s.written.push(stmts);
    },
    async execChanges() {
      return 0;
    },
    async columns() {
      return [];
    },
    async rows(sql) {
      if (s.fail) throw new Error("down");
      if (sql.includes("'ratelimit.version'")) return s.version === null ? [] : [[s.version]];
      if (sql.includes("'ratelimit.config'")) {
        s.configReads++;
        return [[s.config]];
      }
      return [];
    },
    async close() {},
  };
  return { s, store };
}

test("the config is read only when the version changes", async () => {
  const { s, store } = settingsStore();
  const warned: string[] = [];
  const rl = new RateLimits(store, { service: "", instance: "i" }, [], { ...quiet, warn: (m: string) => warned.push(m) });
  s.version = "v1";
  s.config = JSON.stringify({ rules: [{ id: "b", action: "block" }] });
  await rl.poll();
  await rl.poll();
  await rl.poll();
  assert.equal(s.configReads, 1);
  assert.ok(rl.limiter.active);

  s.fail = true;
  await rl.poll();
  assert.ok(rl.limiter.active, "a failed read keeps the rules");
  s.fail = false;

  s.version = "v2";
  s.config = "{not json";
  await rl.poll();
  await rl.poll();
  assert.equal(s.configReads, 3);
  assert.equal(warned.length, 1, "invalid JSON is logged once per version");
  assert.ok(rl.limiter.active, "invalid JSON keeps the rules");

  s.version = "v3";
  s.config = JSON.stringify({ rules: [] });
  await rl.poll();
  assert.equal(s.configReads, 4);
  assert.ok(!rl.limiter.active);

  s.version = "v4";
  s.config = JSON.stringify({ rules: [{ id: "b", action: "block" }] });
  await rl.poll();
  assert.ok(rl.limiter.active);
  s.version = null;
  await rl.poll();
  assert.ok(!rl.limiter.active, "no version row means no rules");
});

test("D1 writes take NULLIF placeholders, at most 50 statements a call", async () => {
  const { s, store } = settingsStore(true);
  const rl = new RateLimits(store, { service: "", instance: "i" }, [], quiet);
  rl.limiter.load({ rules: [{ id: "b", action: "block" }] });
  for (let i = 0; i < 60; i++) rl.limiter.checkRequest(req(`10.0.0.${i}`), 1);
  await rl.flush();
  assert.deepEqual(s.written.map((b) => b.length), [50, 10]);
  assert.match(s.written[0]![0]!.sql, /VALUES \(NULLIF\(\?, ''\)(, NULLIF\(\?, ''\)){12}\)/);
});

const nodeSqlite = createRequire(import.meta.url)("node:sqlite") as { DatabaseSync?: new (path: string) => any } | undefined;

test("VISIONARY_RATE_LIMIT=false turns it off", async () => {
  assert.equal(resolveConfig({}, { VISIONARY_RATE_LIMIT: "false" }).rateLimit, false);
  assert.equal(resolveConfig({}, {}).rateLimit, true);
  if (!nodeSqlite?.DatabaseSync) return;
  const opts = { db: new nodeSqlite.DatabaseSync(":memory:"), logger: quiet };
  assert.equal(new Agent({ ...opts, rateLimit: false }).rateLimits, undefined);
  const on = new Agent(opts);
  assert.ok(on.rateLimits);
  await on.close();
});

test("the middleware answers before the handler, and the user check after setUser", { skip: !nodeSqlite?.DatabaseSync && "needs node:sqlite" }, async () => {
  const db = new nodeSqlite!.DatabaseSync!(":memory:");
  const agent = new Agent({
    db,
    service: "shop",
    instance: "i-1",
    systemMetrics: false,
    spans: false,
    logs: false,
    metrics: false,
    captureUncaught: false,
    profile: false,
    instrumentHttp: false,
    logger: quiet,
  });
  const { s, store } = settingsStore();
  s.version = "1";
  s.config = JSON.stringify({
    rules: [
      { id: "ip1", name: "Export", action: "limit", paths: ["/export"], limit: 1, window_s: 60, algorithm: "fixed" },
      { id: "plan", action: "limit", key: "user", limit: 5, window_s: 60, algorithm: "fixed", limit_by: "attr:plan", limits: { free: 1 } },
    ],
  });
  const rl = new RateLimits(store, { service: "shop", instance: "i-1" }, [], quiet);
  (agent as unknown as { rateLimits: RateLimits }).rateLimits = rl;
  agent.start();
  await rl.poll();
  assert.ok(rl.limiter.active);

  const mw = express(agent);
  let ran = 0;
  let repeats = 0;
  const server = createServer((req, res) =>
    mw(req, res, () => {
      setUser({ id: String(req.headers["x-user"] ?? ""), role: "member" });
      if (rateLimited(req, res, { plan: String(req.headers["x-plan"] ?? "") })) {
        if (rateLimited(req, res, { plan: "free" })) repeats++;
        return;
      }
      ran++;
      res.end("ok");
    }),
  );
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  try {
    assert.equal((await fetch(`${base}/export`)).status, 200);
    const limited = await fetch(`${base}/export`);
    assert.equal(limited.status, 429);
    assert.equal(limited.headers.get("retry-after") !== null && Number(limited.headers.get("retry-after")) > 0, true);
    assert.equal(limited.headers.get("content-type"), "application/json");
    assert.equal(await limited.text(), `{"error":{"code":"rate_limited","key":"ip1","message":"Too many requests"}}`);
    assert.equal(ran, 1, "the handler did not run for the limited request");

    const user = { "x-user": "u1", "x-plan": "free" };
    assert.equal((await fetch(`${base}/me`, { headers: user })).status, 200);
    const second = await fetch(`${base}/me`, { headers: user });
    assert.equal(second.status, 429);
    await second.text();
    assert.equal(ran, 2);
    assert.equal(repeats, 1, "a second check repeats the outcome");
    assert.equal((await fetch(`${base}/me`, { headers: { "x-user": "u2", "x-plan": "pro" } })).status, 200);
  } finally {
    await new Promise((resolve) => server.close(resolve));
    await agent.close(5000);
  }

  const rows = s.written.flat();
  assert.equal(rows.length, 1);
  assert.equal(rows[0]!.sql, `INSERT INTO rate_limit_events (${RATE_LIMIT_COLUMNS.join(", ")}) VALUES (${RATE_LIMIT_COLUMNS.map(() => "?").join(", ")}), (${RATE_LIMIT_COLUMNS.map(() => "?").join(", ")})`);
  assert.deepEqual(RATE_LIMIT_COLUMNS, [
    "created_at", "service", "instance", "rule_id", "rule_name", "outcome", "key_value", "ip",
    "country", "method", "path", "user_id", "hits",
  ]);
  const args = rows[0]!.args;
  const [ip1, plan] = [args.slice(0, 13), args.slice(13)];
  assert.match(String(ip1[0]), /^\d{4}-\d\d-\d\dT/);
  assert.deepEqual(ip1.slice(1), ["shop", "i-1", "ip1", "Export", "limited", "127.0.0.1", "127.0.0.1", null, "GET", "/export", null, 1]);
  assert.deepEqual(plan.slice(1), ["shop", "i-1", "plan", "plan", "limited", "u1", "127.0.0.1", null, "GET", "/me", "u1", 1]);

  // The denied request is still logged.
  const logged = db.prepare("SELECT path, status, error_code FROM api_logs ORDER BY id").all() as Record<string, unknown>[];
  assert.deepEqual(
    logged.map((r) => [r.path, r.status, r.error_code]),
    [
      ["/export", 200, null],
      ["/export", 429, "rate_limited"],
      ["/me", 200, null],
      ["/me", 429, "rate_limited"],
      ["/me", 200, null],
    ],
  );
});

function fakeAgent(config: unknown) {
  const limiter = new Limiter();
  limiter.load(parseConfig(JSON.stringify(config)));
  return { rateLimits: { limiter } } as unknown as Agent;
}

const fakeReq = (url: string) =>
  ({ method: "GET", url, headers: {}, socket: { remoteAddress: "::ffff:10.0.0.9" } }) as unknown as IncomingMessage;

test("Fastify replies and Koa contexts get the answer", () => {
  const agent = fakeAgent({
    rules: [
      { id: "blk", action: "block", ips: ["10.0.0.0/8"], paths: ["/admin"] },
      { id: "usr", action: "limit", key: "user", limit: 0, window_s: 9, match: { "attr:plan": ["free"] } },
    ],
  });
  const sent: unknown[] = [];
  const reply = {
    raw: {} as ServerResponse,
    code(n: number) {
      sent.push(n);
      return reply;
    },
    header(k: string, v: string) {
      sent.push(`${k}: ${v}`);
      return reply;
    },
    send(body: string) {
      sent.push(body);
    },
  };
  assert.ok(checkRequest(agent, fakeReq("/admin?x=1"), reply, "/admin?x=1"));
  assert.deepEqual(sent, [403, "Content-Type: application/json", `{"error":{"code":"blocked","key":"blk","message":"Forbidden"}}`]);

  // The user check on a request the SDK does not log: setUser still reaches it.
  const raw = fakeReq("/me");
  assert.ok(!checkRequest(agent, raw, reply, "/me"));
  enterRequest(raw, () => setUser("u7"));
  const request = { raw };
  assert.ok(!rateLimited(request, reply, { plan: "pro" }));
  assert.ok(!rateLimited(request, reply, { plan: "free" }), "only the first call counts");

  const raw2 = fakeReq("/me");
  assert.ok(!checkRequest(agent, raw2, {}, "/me"));
  const headers: Record<string, string> = {};
  const ctx = { req: raw2, res: {}, status: 404, body: undefined as unknown, set: (k: string, v: string) => (headers[k] = v) };
  enterRequest(raw2, () => setUser("u8"));
  assert.ok(rateLimited(ctx, ctx, { plan: "free" }));
  assert.deepEqual([ctx.status, headers["Retry-After"], ctx.body], [429, "9", `{"error":{"code":"rate_limited","key":"usr","message":"Too many requests"}}`]);
  assert.ok(rateLimited(ctx, ctx, { plan: "free" }));

  assert.ok(!rateLimited(fakeReq("/x"), {}), "false outside a checked request");
});
