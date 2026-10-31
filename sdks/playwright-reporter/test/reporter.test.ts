import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import VisionaryReporter, { buildPayload, flattenSteps, isFinal, newTraceparent, parseInterval, traceIdOf, type PWTestCase, type PWTestResult } from "../src/index.js";

function testCase(over: Partial<PWTestCase> = {}): PWTestCase {
  return {
    title: "pays by card",
    titlePath: () => ["", "chromium", "checkout.spec.ts", "Checkout", "pays by card"],
    expectedStatus: "passed",
    retries: 1,
    annotations: [],
    tags: ["@smoke"],
    parent: { project: () => ({ name: "chromium", use: { baseURL: "https://shop.example.com", browserName: "chromium", channel: "msedge" } }) },
    outcome: () => "unexpected",
    ...over,
  };
}

function result(over: Partial<PWTestResult> = {}): PWTestResult {
  return {
    retry: 1,
    startTime: new Date("2026-10-31T10:00:00.000Z"),
    duration: 2345.6,
    status: "failed",
    errors: [{ message: "\u001b[31mError: expect(locator).toBeVisible() failed\u001b[39m" }],
    attachments: [],
    steps: [
      {
        title: "Before Hooks",
        category: "hook",
        duration: 300,
        steps: [
          { title: "fixture: browser", category: "fixture", duration: 40, steps: [{ title: "Launch browser", category: "pw:api", duration: 40, steps: [] }] },
          { title: "page.goto(/cart)", category: "pw:api", duration: 250, steps: [] },
        ],
      },
      {
        title: "pay",
        category: "test.step",
        duration: 1800,
        error: { message: "timed out" },
        steps: [
          { title: "page.click(#pay)", category: "pw:api", duration: 100, steps: [] },
          { title: "expect.toBeVisible", category: "expect", duration: 1700, error: { message: "not visible" }, steps: [] },
          { title: "expect.toHaveURL", category: "expect", duration: 5, steps: [] },
        ],
      },
    ],
    ...over,
  };
}

test("only the final attempt is reported", () => {
  const t = testCase();
  assert.equal(isFinal(t, result({ retry: 0, status: "failed" })), false);
  assert.equal(isFinal(t, result({ retry: 1, status: "failed" })), true);
  assert.equal(isFinal(t, result({ retry: 0, status: "passed" })), true);
  assert.equal(isFinal(t, result({ retry: 0, status: "skipped" })), true);
});

test("payload", async () => {
  const dir = mkdtempSync(join(tmpdir(), "vis-pw-"));
  const shot = join(dir, "shot.png");
  const bigTrace = join(dir, "trace.zip");
  writeFileSync(shot, Buffer.from("png bytes"));
  writeFileSync(bigTrace, Buffer.alloc(800 * 1024));
  const tp = newTraceparent();
  const p = await buildPayload(
    testCase({ annotations: [{ type: "traceparent", description: tp.header }] }),
    result({
      attachments: [
        { name: "screenshot", contentType: "image/png", path: shot },
        { name: "trace", contentType: "application/zip", path: bigTrace },
        { name: "video", contentType: "video/webm", path: join(dir, "missing.webm") },
      ],
    }),
    { expectedInterval: "10m", commit: "abc1234", release: "v1.2.3" },
  );
  assert.equal(p.project, "chromium");
  assert.equal(p.title, "checkout.spec.ts › Checkout › pays by card");
  assert.equal(p.ok, false);
  assert.equal(p.outcome, "unexpected");
  assert.equal(p.duration_ms, 2346);
  assert.equal(p.retry, 1);
  assert.equal(p.error, "Error: expect(locator).toBeVisible() failed");
  assert.equal(p.browser, "msedge");
  assert.equal(p.url, "https://shop.example.com");
  assert.equal(p.trace_id, tp.traceId);
  assert.equal(p.expected_interval_s, 600);
  assert.equal(p.commit, "abc1234");
  assert.deepEqual(p.tags, ["@smoke"]);
  assert.equal(p.attachments[0].body, Buffer.from("png bytes").toString("base64"));
  assert.equal(p.attachments[1].body, undefined, "a trace over the cap stays behind");
  assert.equal(p.attachments[1].size, 800 * 1024);
  assert.equal(p.attachments[2].size, 0);
});

test("steps", () => {
  const steps = flattenSteps(result().steps);
  assert.deepEqual(
    steps.map((s) => [s.title, s.depth ?? 0, s.error ?? ""]),
    [
      ["page.goto(/cart)", 0, ""],
      ["pay", 0, "timed out"],
      ["page.click(#pay)", 1, ""],
      ["expect.toBeVisible", 1, "not visible"],
    ],
  );
});

test("trace ids and intervals", () => {
  assert.equal(traceIdOf([{ type: "trace_id", description: "4BF92F3577B34DA6A3CE929D0E0E4736" }]), "4bf92f3577b34da6a3ce929d0e0e4736");
  assert.equal(traceIdOf([{ type: "traceparent", description: "00-00000000000000000000000000000000-00f067aa0ba902b7-01" }]), undefined);
  assert.equal(traceIdOf([], [{ name: "traceparent", contentType: "text/plain", body: Buffer.from("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01") }]), "4bf92f3577b34da6a3ce929d0e0e4736");
  assert.equal(parseInterval("15m"), 900);
  assert.equal(parseInterval("2h"), 7200);
  assert.equal(parseInterval(600), 600);
  assert.equal(parseInterval("soon"), undefined);
});

test("reporter posts final results and survives a failing server", async () => {
  const posts: { url: string; auth: string; body: Record<string, unknown> }[] = [];
  let calls = 0;
  const fake: typeof fetch = async (url, init) => {
    calls++;
    if (calls === 1) return new Response("busy", { status: 503 });
    const body = JSON.parse(String(init?.body));
    posts.push({ url: String(url), auth: String((init?.headers as Record<string, string>).Authorization), body });
    return new Response(JSON.stringify({ results: [{ status: "recorded", check_id: 1 }] }), { status: 200 });
  };
  const r = new VisionaryReporter({ server: "https://vis.example.com/", key: "vis_ci", fetch: fake, quiet: true });
  r.onBegin();
  const t = testCase();
  r.onTestEnd(t, result({ retry: 0 }));
  r.onTestEnd(t, result({ retry: 1, status: "passed", errors: [] }));
  await r.onEnd();
  assert.equal(posts.length, 1);
  assert.equal(posts[0].url, "https://vis.example.com/api/uptime/playwright");
  assert.equal(posts[0].auth, "Bearer vis_ci");
  assert.equal(posts[0].body.ok, true);
  assert.equal(posts[0].body.error, undefined);
});

test("the default transport posts over plain HTTP", async () => {
  const seen: { auth?: string; body: string }[] = [];
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      seen.push({ auth: req.headers.authorization, body });
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ results: [{ status: "created", check_id: 7 }] }));
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  try {
    const r = new VisionaryReporter({ server: `http://127.0.0.1:${port}`, key: "vis_ci", quiet: true });
    r.onTestEnd(testCase(), result({ retry: 1 }));
    r.onTestEnd(testCase(), result({ retry: 0, status: "skipped" }));
    await r.onEnd();
  } finally {
    server.close();
  }
  assert.equal(seen.length, 1);
  assert.equal(seen[0].auth, "Bearer vis_ci");
  assert.equal(JSON.parse(seen[0].body).title, "checkout.spec.ts › Checkout › pays by card");
});

test("reporter without a server does nothing", async () => {
  delete process.env.VISIONARY_URL;
  let called = false;
  const r = new VisionaryReporter({ key: "vis_ci", fetch: async () => ((called = true), new Response("{}")), quiet: true });
  const errors: string[] = [];
  const orig = console.error;
  console.error = (m: string) => errors.push(m);
  try {
    r.onBegin();
    r.onTestEnd(testCase(), result());
    await r.onEnd();
  } finally {
    console.error = orig;
  }
  assert.equal(called, false);
  assert.match(errors[0], /VISIONARY_URL/);
});
