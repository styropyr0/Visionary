import assert from "node:assert/strict";
import type { AddressInfo } from "node:net";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { after, test } from "node:test";
import { Agent } from "../src/agent.js";
import { RequestState, runInRequest } from "../src/context.js";
import { wrap } from "../src/integrations/node-http.js";
import { captureConsole, pinoStream, winstonTransport } from "../src/logs/hooks.js";
import type { LogRecord } from "../src/logs/pipeline.js";
import { LogPipeline, logRecord, logValues, setActiveLogs, traceParentId } from "../src/logs/pipeline.js";
import { DEBUG, ERROR, INFO, TRACE, WARN, secretKeys } from "../src/logs/rules.js";
import { LOG_COLUMNS, insertSQL } from "../src/store/sql.js";

/** A pipeline whose written lines land in `out`, with the given settings rows. */
function pipeline(rows: string[][] = [], service = "api") {
  const out: LogRecord[] = [];
  const p = new LogPipeline(service, INFO, async () => rows, (r) => out.push(r));
  p.apply(rows);
  return { p, out, take: () => out.splice(0).map((r) => r.message).join(",") };
}

let clock = Date.now();
/** Lines a millisecond apart, so their times order them. */
const line = (sev: number, msg: string) => logRecord(sev, msg, ++clock);

/** Runs one request: fn logs inside it, then it ends with status after tookMs. */
function serve(p: LogPipeline, route: string, status: number, tookMs: number, user: string, fn: (log: (r: LogRecord) => void) => void) {
  const state = new RequestState("req-" + clock, Date.now(), process.hrtime.bigint(), "GET", route);
  state.route = route;
  runInRequest(state, () => {
    if (user) state.setUser(user);
    fn((r) => p.capture(r));
  });
  state.done = true;
  p.finish(state, route, status >= 500 || state.failed, tookMs);
  return state;
}

const threeLines = (log: (r: LogRecord) => void) => {
  log(line(DEBUG, "debug line"));
  log(line(INFO, "info line"));
  log(line(WARN, "warn line"));
};

/** Messages in the order they were logged (held lines are queued later). */
const ordered = (out: LogRecord[]) =>
  out
    .splice(0)
    .sort((a, b) => a.at - b.at)
    .map((r) => r.message)
    .join(",");

test("debug lines are kept only for failed requests", () => {
  const { p, out } = pipeline();
  serve(p, "/ok", 200, 0, "", threeLines);
  assert.equal(ordered(out), "info line,warn line");
  const state = serve(p, "/fail", 500, 0, "", threeLines);
  const got = out.slice();
  assert.equal(ordered(out), "debug line,info line,warn line");
  assert.ok(got.every((r) => r.requestId === state.requestId));
  // An error captured in the request counts as a failure too.
  const failed = new RequestState("req-f", Date.now(), 0n, "GET", "/caught");
  runInRequest(failed, () => threeLines((r) => p.capture(r)));
  failed.failed = true;
  failed.done = true;
  p.finish(failed, "/caught", failed.failed, 0);
  assert.equal(ordered(out), "debug line,info line,warn line");
});

test("debug lines are kept for slow requests unless the buffer is off", () => {
  let { p, out } = pipeline([["logs.slow_ms", "20"]]);
  serve(p, "/slow", 200, 30, "", threeLines);
  assert.equal(ordered(out), "debug line,info line,warn line");
  ({ p, out } = pipeline([["logs.slow_ms", "20"], ["logs.buffer", "false"]]));
  serve(p, "/slow", 500, 30, "", threeLines);
  assert.equal(ordered(out), "info line,warn line");
  ({ p, out } = pipeline([["logs.buffer_level", "info"]]));
  serve(p, "/fail", 500, 0, "", threeLines);
  assert.equal(ordered(out), "info line,warn line");
});

test("overrides by route, user and time", () => {
  const iso = (ms: number) => new Date(ms).toISOString();
  const { p, out } = pipeline([
    [
      "logs.overrides",
      JSON.stringify([
        { route: "/checkout", level: "debug", until: iso(Date.now() + 3600_000) },
        { user: "42", level: "trace" },
        { route: "/health", level: "warn" },
        { route: "/old", level: "debug", until: iso(Date.now() - 3600_000) },
        { service: "other", level: "debug" },
      ]),
    ],
  ]);
  serve(p, "/checkout", 200, 0, "", threeLines);
  assert.equal(ordered(out), "debug line,info line,warn line");
  serve(p, "/health", 200, 0, "", threeLines);
  assert.equal(ordered(out), "warn line", "quieter route");
  serve(p, "/old", 200, 0, "", threeLines);
  assert.equal(ordered(out), "info line,warn line", "expired override");
  serve(p, "/any", 200, 0, "42", (log) => {
    log(line(TRACE, "trace line"));
    threeLines(log);
  });
  const got = out.slice().sort((a, b) => a.at - b.at);
  assert.equal(ordered(out), "trace line,debug line,info line,warn line");
  assert.equal(got[0]!.userId, "42");
});

test("held lines are capped at 500, oldest dropped", () => {
  const { p, out } = pipeline();
  const state = serve(p, "/fail", 500, 0, "", (log) => {
    for (let i = 0; i < 510; i++) log(line(DEBUG, `d${i}`));
  });
  assert.equal(out.length, 500);
  assert.equal(out[0]!.message, "d10");
  assert.equal(state.heldLogs, undefined);
});

test("lines after the request ended are decided at once with its route", () => {
  const { p, out } = pipeline([["logs.overrides", JSON.stringify([{ route: "/jobs", level: "debug" }])]]);
  const state = serve(p, "/jobs", 200, 0, "7", () => undefined);
  runInRequest(state, () => {
    p.capture(line(DEBUG, "late debug"));
    p.capture(line(TRACE, "late trace"));
  });
  assert.equal(out.length, 1);
  assert.equal(out[0]!.message, "late debug");
  assert.equal(out[0]!.userId, "7");
  assert.equal(out[0]!.requestId, state.requestId);
});

test("lines outside requests, the SDK's own messages and disabled capture", () => {
  const { p, take } = pipeline([["logs.level", "warn"]]);
  p.capture(line(INFO, "startup"));
  p.capture(line(ERROR, "boom"));
  p.capture(line(ERROR, "visionary: write failed"));
  assert.equal(take(), "boom");
  p.apply([["logs.enabled", "false"]]);
  p.capture(line(ERROR, "off"));
  assert.equal(take(), "");
});

test("drop rules and the rate cap", () => {
  const drops = JSON.stringify([{ contains: "healthcheck" }, { logger: "noisy", level: "info" }]);
  const { p, out, take } = pipeline([["logs.drop", drops], ["logs.rate", "3"]]);
  const at = Math.floor(Date.now() / 60_000) * 60_000;
  const l = (sev: number, msg: string, logger = "") => {
    const r = logRecord(sev, msg, at);
    r.logger = logger;
    p.capture(r);
  };
  l(INFO, "healthcheck ok");
  l(INFO, "chatty", "noisy");
  l(WARN, "kept", "noisy");
  for (let i = 0; i < 5; i++) l(INFO, "flood");
  l(ERROR, "still kept");
  assert.equal(take(), "kept,flood,flood,still kept");
  // The next minute's first line reports the drops.
  p.capture(logRecord(INFO, "next minute", at + 60_000));
  assert.equal(out[0]!.logger, "visionary");
  assert.equal(out[0]!.sev, WARN);
  assert.equal(out[0]!.message, "visionary: 3 log lines dropped over the cap of 3 a minute (Settings → Logs)");
  assert.equal(out[1]!.message, "next minute");
});

test("logValues match the insert's columns", () => {
  const r = logRecord(WARN, "hi password=x", Date.parse("2026-10-28T09:30:00Z"));
  r.attrs = { a: 1, token: "t" };
  r.requestId = "rid";
  const v = logValues(r, { service: "api", instance: "i-1", version: "" }, secretKeys());
  assert.equal(v.length, LOG_COLUMNS.length);
  assert.equal(LOG_COLUMNS.length, 21);
  assert.equal(insertSQL("app_logs", false).match(/\?/g)!.length, 21);
  assert.equal(insertSQL("app_logs", true).match(/NULLIF\(\?, ''\)/g)!.length, 21);
  const row = Object.fromEntries(LOG_COLUMNS.map((c, i) => [c, v[i]]));
  assert.equal(row.created_at, "2026-10-28T09:30:00.000Z");
  assert.equal(row.source, "node");
  assert.equal(row.version, null);
  assert.equal(row.container, null);
  assert.equal(row.level, "warn");
  assert.equal(row.severity, WARN);
  assert.equal(row.message, "hi password=[REDACTED]");
  assert.equal(row.attrs, `{"a":1,"token":"[REDACTED]"}`);
  assert.equal(row.request_id, "rid");
  assert.equal(row.code_file, null);
  assert.equal(row.size, Buffer.byteLength(row.message as string) + Buffer.byteLength(row.attrs as string));
});

test("traceParentId", () => {
  assert.equal(traceParentId("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"), "4bf92f3577b34da6a3ce929d0e0e4736");
  for (const bad of ["", undefined, "00-xyz-1-1", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"]) {
    assert.equal(traceParentId(bad), "");
  }
});

test("captureConsole still prints and captures with levels", () => {
  const { p, out } = pipeline([["logs.level", "debug"]]);
  setActiveLogs(p);
  const printed: unknown[][] = [];
  const saved = { log: console.log, warn: console.warn, debug: console.debug, error: console.error };
  console.log = (...args: unknown[]) => void printed.push(args);
  console.warn = (...args: unknown[]) => void printed.push(args);
  console.debug = (...args: unknown[]) => void printed.push(args);
  console.error = (...args: unknown[]) => void printed.push(args);
  const restore = captureConsole();
  try {
    assert.equal(captureConsole(), restore, "a second call returns the same restore");
    console.log("platform: listening on :%d", 8080);
    console.log("ERROR payment failed for order", 7);
    console.log("INFO visionary: profiling settings unavailable");
    console.log("user error rate is low but ok");
    console.warn("disk %s", "low");
    console.debug("cache miss", { key: "a" });
    console.error(new Error("kaput"));
    console.log("");
  } finally {
    restore();
    Object.assign(console, saved);
    setActiveLogs(undefined);
  }
  assert.equal(printed.length, 8);
  assert.deepEqual(printed[0], ["platform: listening on :%d", 8080]);
  const got = out.map((r) => [r.sev, r.message.split("\n")[0]]);
  assert.deepEqual(got, [
    [INFO, "platform: listening on :8080"],
    [ERROR, "payment failed for order 7"],
    [INFO, "user error rate is low but ok"],
    [WARN, "disk low"],
    [DEBUG, "cache miss { key: 'a' }"],
    [ERROR, "Error: kaput"],
  ]);
  // Restored: nothing more is captured.
  console.log = () => undefined;
  console.log("after");
  console.log = saved.log;
  assert.equal(out.length, 6);
});

test("pinoStream parses JSON lines and forwards them unchanged", () => {
  const { p, out } = pipeline([["logs.level", "trace"]]);
  setActiveLogs(p);
  const written: string[] = [];
  const stream = pinoStream({ write: (s: string) => written.push(s) });
  const lines = [
    { level: 30, time: 1761643800000, pid: 1, hostname: "h", msg: "order placed", order: { id: 7, items: [1, 2] }, user_id: "u1" },
    { level: 50, time: 1761643800001, pid: 1, hostname: "h", name: "billing", msg: "charge failed", err: { type: "Error", message: "card declined", stack: "Error: card declined" } },
    { level: 10, time: 1761643800002, msg: "tick" },
    { level: 20, msg: "dbg" },
    { level: 40, msg: "w" },
    { level: 60, msg: "f" },
    { level: "warn", time: "2026-10-28T09:30:00.000Z", msg: "string level" },
  ];
  try {
    for (const l of lines) stream.write(JSON.stringify(l) + "\n");
    stream.write("not json\n");
  } finally {
    setActiveLogs(undefined);
  }
  assert.equal(written.length, 8);
  assert.equal(written[0], JSON.stringify(lines[0]) + "\n");
  assert.deepEqual(
    out.map((r) => r.sev),
    [INFO, ERROR, TRACE, DEBUG, WARN, 6, WARN],
  );
  const [placed, failed] = out;
  assert.equal(placed!.message, "order placed");
  assert.equal(placed!.at, 1761643800000);
  assert.equal(placed!.userId, "u1");
  assert.deepEqual(placed!.attrs, { "order.id": 7, "order.items": "[1,2]" });
  assert.equal(failed!.logger, "billing");
  assert.deepEqual(failed!.attrs, { "err.type": "Error", "err.message": "card declined", "err.stack": "Error: card declined" });
  assert.equal(out[6]!.at, Date.parse("2026-10-28T09:30:00.000Z"));
});

test("winstonTransport takes winston's info objects", async () => {
  const { p, out } = pipeline([["logs.level", "trace"]]);
  setActiveLogs(p);
  const t = winstonTransport();
  try {
    t.log({ level: "info", message: "hello", [Symbol.for("level")]: "info", service: "billing", when: new Date(0) });
    t.log({ level: "\u001b[31merror\u001b[39m", [Symbol.for("level")]: "error", message: "bad", request_id: "r1" }, () => undefined);
    await new Promise<void>((resolve) => t.write({ level: "silly", message: "s" }, () => resolve()));
    t.log({ level: "http", message: "GET /" });
    t.log({ level: "verbose", message: "v" });
  } finally {
    setActiveLogs(undefined);
  }
  assert.ok(t.writable && (t as unknown as { _writableState: { objectMode: boolean } })._writableState.objectMode);
  assert.deepEqual(
    out.map((r) => [r.sev, r.message]),
    [
      [INFO, "hello"],
      [ERROR, "bad"],
      [TRACE, "s"],
      [INFO, "GET /"],
      [TRACE, "v"],
    ],
  );
  assert.deepEqual(out[0]!.attrs, { service: "billing", when: "1970-01-01T00:00:00.000Z" });
  assert.equal(out[1]!.requestId, "r1");
  assert.equal(out[1]!.attrs, undefined);
});

// End to end: a real agent on node:sqlite, settings read from
// visionary_settings, a request through wrap() logging with console.
const nodeSqlite = createRequire(import.meta.url)("node:sqlite") as { DatabaseSync?: new (path: string) => any } | undefined;

test("requests write their lines to app_logs", { skip: !nodeSqlite?.DatabaseSync && "needs node:sqlite" }, async () => {
  const db = new nodeSqlite!.DatabaseSync!(":memory:");
  db.exec("CREATE TABLE visionary_settings (name TEXT PRIMARY KEY, value TEXT, updated_at TEXT)");
  db.exec("INSERT INTO visionary_settings VALUES ('logs.level', 'info', ''), ('logs.drop', '[{\"contains\":\"noise\"}]', '')");
  const quiet = { info() {}, warn() {}, error() {} };
  const agent = new Agent({
    db,
    service: "shop",
    systemMetrics: false,
    spans: false,
    instrumentHttp: false,
    captureUncaught: false,
    profile: false,
    logger: quiet,
  }).start();
  const saved = { log: console.log, debug: console.debug, error: console.error };
  console.log = console.debug = console.error = () => undefined;
  const restore = captureConsole();
  const server = createServer(
    wrap((req, res) => {
      console.debug("looking up cart");
      console.log("cart loaded");
      console.log("noise");
      res.statusCode = req.url === "/fail" ? 500 : 200;
      res.end("ok");
    }, agent),
  );
  const trace = "4bf92f3577b34da6a3ce929d0e0e4736";
  after(() => {
    restore();
    Object.assign(console, saved);
  });
  try {
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const port = (server.address() as AddressInfo).port;
    // Let the first settings read finish.
    await new Promise((resolve) => setTimeout(resolve, 50));
    await (await fetch(`http://127.0.0.1:${port}/ok`, { headers: { traceparent: `00-${trace}-00f067aa0ba902b7-01` } })).text();
    await (await fetch(`http://127.0.0.1:${port}/fail`)).text();
    console.log("outside");
  } finally {
    await new Promise((resolve) => server.close(resolve));
    restore();
    Object.assign(console, saved);
    await agent.close(5000);
  }
  const rows = db
    .prepare("SELECT message, level, severity, source, service, request_id, trace_id FROM app_logs ORDER BY id")
    .all() as Record<string, unknown>[];
  const requests = db.prepare("SELECT request_id, path FROM api_logs ORDER BY id").all() as Record<string, string>[];
  const id = (path: string) => requests.find((r) => r.path === path)!.request_id;
  assert.deepEqual(
    rows.map((r) => [r.message, r.level, r.request_id]),
    [
      ["cart loaded", "info", id("/ok")],
      ["cart loaded", "info", id("/fail")],
      ["looking up cart", "debug", id("/fail")],
      ["outside", "info", null],
    ],
  );
  assert.equal(rows[0]!.trace_id, trace);
  assert.equal(rows[0]!.source, "node");
  assert.equal(rows[0]!.service, "shop");
  assert.equal(rows[1]!.trace_id, null);
});
