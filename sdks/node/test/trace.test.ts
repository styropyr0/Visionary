import assert from "node:assert/strict";
import type { IncomingHttpHeaders } from "node:http";
import http, { createServer } from "node:http";
import { createRequire } from "node:module";
import type { AddressInfo } from "node:net";
import { test } from "node:test";
import { Agent } from "../src/agent.js";
import { wrap } from "../src/integrations/node-http.js";
import { background, span } from "../src/telemetry/spans.js";
import { currentSpan, parseTraceparent, requestSpan, traceparent } from "../src/telemetry/trace.js";

const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";
const PARENT = "00f067aa0ba902b7";

test("parseTraceparent", () => {
  assert.deepEqual(parseTraceparent(`00-${TRACE}-${PARENT}-01`), { traceId: TRACE, spanId: PARENT, flags: "01", state: "" });
  assert.equal(parseTraceparent(` 00-${TRACE}-${PARENT}-00 `)?.flags, "00");
  // Later versions may append fields.
  assert.equal(parseTraceparent(`01-${TRACE}-${PARENT}-01-extra`)?.traceId, TRACE);
  for (const bad of [
    "",
    undefined,
    "00-xyz-1-1",
    `00-${TRACE}-${PARENT}-01-extra`,
    `ff-${TRACE}-${PARENT}-01`,
    `00-${TRACE.toUpperCase()}-${PARENT}-01`,
    `00-${"0".repeat(32)}-${PARENT}-01`,
    `00-${TRACE}-${"0".repeat(16)}-01`,
    `00-${TRACE}-${PARENT}-1`,
    `0-${TRACE}-${PARENT}-01`,
  ]) {
    assert.equal(parseTraceparent(bad), undefined, String(bad));
  }
});

test("requestSpan continues or starts a trace", () => {
  const cont = requestSpan(`00-${TRACE}-${PARENT}-00`, "vendor=1");
  assert.equal(cont.span.traceId, TRACE);
  assert.equal(cont.parent, PARENT);
  assert.notEqual(cont.span.spanId, PARENT);
  assert.match(cont.span.spanId, /^[0-9a-f]{16}$/);
  assert.equal(traceparent(cont.span), `00-${TRACE}-${cont.span.spanId}-00`);
  assert.equal(cont.span.state, "vendor=1");

  const fresh = requestSpan("garbage", "vendor=1");
  assert.match(fresh.span.traceId, /^[0-9a-f]{32}$/);
  assert.equal(fresh.parent, "");
  assert.equal(fresh.span.flags, "01");
  assert.equal(fresh.span.state, "");
});

const nodeSqlite = createRequire(import.meta.url)("node:sqlite") as { DatabaseSync?: new (path: string) => any } | undefined;
const quiet = { info() {}, warn() {}, error() {} };

function listen(server: http.Server): Promise<number> {
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve((server.address() as AddressInfo).port)));
}

function get(url: string, headers: Record<string, string> = {}): Promise<void> {
  return new Promise((resolve, reject) => {
    http.get(url, { headers }, (res) => res.resume().on("end", resolve)).on("error", reject);
  });
}

test("spans nest in the request's trace and outbound calls carry it", { skip: !nodeSqlite?.DatabaseSync && "needs node:sqlite" }, async () => {
  const db = new nodeSqlite!.DatabaseSync!(":memory:");
  const agent = new Agent({
    db,
    service: "front",
    systemMetrics: false,
    captureUncaught: false,
    profile: false,
    logs: false,
    metrics: false,
    logger: quiet,
  }).start();

  const seen: Record<string, IncomingHttpHeaders> = {};
  const back = createServer(
    wrap((req, res) => {
      seen[req.url!] = req.headers;
      res.end("ok");
    }, agent),
  );
  const backPort = await listen(back);
  const backUrl = `http://127.0.0.1:${backPort}`;
  let bg: Promise<unknown> | undefined;
  const front = createServer(
    wrap(async (req, res) => {
      if (req.url === "/own") {
        await get(`${backUrl}/own`, { traceparent: `00-${TRACE}-${PARENT}-01` });
      } else {
        await span("custom", "build report", async () => {
          await (await fetch(`${backUrl}/fetch`)).text();
          await get(`${backUrl}/get`);
        });
        bg = background("send email", () => fetch(`${backUrl}/bg`).then((r) => r.text()));
      }
      res.end("done");
    }, agent),
  );
  const frontPort = await listen(front);
  try {
    await get(`http://127.0.0.1:${frontPort}/report`, { traceparent: `00-${TRACE}-${PARENT}-01`, tracestate: "vendor=7" });
    await bg;
    await get(`http://127.0.0.1:${frontPort}/own`);
    // Outside any request a call starts a trace of its own.
    await get(`${backUrl}/outside`);
  } finally {
    await new Promise((r) => front.close(r));
    await new Promise((r) => back.close(r));
    await agent.close(5000);
  }

  const requests = db.prepare("SELECT path, trace_id, span_id, parent_span_id FROM api_logs").all() as Record<string, string | null>[];
  const req = (path: string) => requests.find((r) => r.path === path)!;
  const spans = db
    .prepare("SELECT kind, name, detail, trace_id, span_id, parent_span_id FROM spans")
    .all() as Record<string, string | null>[];
  const spanOf = (kind: string, detailEnd = "") => spans.find((s) => s.kind === kind && (s.detail ?? "").endsWith(detailEnd))!;

  const report = req("/report");
  assert.equal(report.trace_id, TRACE);
  assert.equal(report.parent_span_id, PARENT);

  const custom = spanOf("custom");
  assert.equal(custom.trace_id, TRACE);
  assert.equal(custom.parent_span_id, report.span_id);

  for (const path of ["/fetch", "/get"]) {
    const call = spanOf("http", path);
    assert.equal(call.trace_id, TRACE, path);
    assert.equal(call.parent_span_id, custom.span_id, path);
    // The service reached continues the trace under the http span.
    assert.equal(req(path).trace_id, TRACE, path);
    assert.equal(req(path).parent_span_id, call.span_id, path);
    assert.equal(seen[path]!.traceparent, `00-${TRACE}-${call.span_id}-01`);
    assert.equal(seen[path]!.tracestate, "vendor=7");
  }

  const bgSpan = spanOf("background");
  assert.equal(bgSpan.parent_span_id, report.span_id);
  const bgCall = spanOf("http", "/bg");
  assert.equal(bgCall.parent_span_id, bgSpan.span_id);
  assert.equal(req("/bg").parent_span_id, bgCall.span_id);

  // A traceparent the application set itself is left alone.
  assert.equal(seen["/own"]!.traceparent, `00-${TRACE}-${PARENT}-01`);

  const outside = req("/outside");
  assert.match(outside.trace_id ?? "", /^[0-9a-f]{32}$/);
  assert.notEqual(outside.trace_id, TRACE);
});

test("with spans off the trace still goes on, naming the request's span", { skip: !nodeSqlite?.DatabaseSync && "needs node:sqlite" }, async () => {
  const db = new nodeSqlite!.DatabaseSync!(":memory:");
  const agent = new Agent({
    db,
    spans: false,
    systemMetrics: false,
    captureUncaught: false,
    profile: false,
    logs: false,
    metrics: false,
    // Not logged: the trace is carried on all the same.
    pathPrefix: "/api",
    logger: quiet,
  }).start();
  let header = "";
  let inner: string | undefined;
  const back = createServer((req, res) => {
    header = String(req.headers.traceparent ?? "");
    res.end();
  });
  const backPort = await listen(back);
  const front = createServer(
    wrap(async (_req, res) => {
      inner = currentSpan()?.spanId;
      await (await fetch(`http://127.0.0.1:${backPort}/`)).text();
      res.end();
    }, agent),
  );
  const frontPort = await listen(front);
  try {
    await get(`http://127.0.0.1:${frontPort}/`, { traceparent: `00-${TRACE}-${PARENT}-01` });
  } finally {
    await new Promise((r) => front.close(r));
    await new Promise((r) => back.close(r));
    await agent.close(5000);
  }
  assert.ok(inner);
  assert.equal(header, `00-${TRACE}-${inner}-01`);
});
