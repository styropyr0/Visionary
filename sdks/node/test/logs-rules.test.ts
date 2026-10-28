import assert from "node:assert/strict";
import { test } from "node:test";
import {
  DEBUG,
  ERROR,
  FATAL,
  INFO,
  MAX_ATTRS,
  TRACE,
  WARN,
  dropped,
  encodeAttrs,
  floor,
  holdBelow,
  levelName,
  parseLevel,
  parseLogSettings,
  scrub,
  secretKeys,
  stripLevelWord,
  textLevel,
  truncateBytes,
} from "../src/logs/rules.js";

// The same cases as sdks/go/logrules/logrules_test.go.

test("textLevel", () => {
  const cases: Record<string, number> = {
    "ERROR something": ERROR,
    "2026-10-28 WARN disk": WARN,
    "level=debug msg=x": DEBUG,
    "[info] started": INFO,
    "time=x level=WARN msg=y": WARN,
    "INFO handled": INFO,
    "payment failed": 0,
    "user error rate is low but ok": 0,
    "error: connection refused": ERROR,
    "Error handling request": 0,
    "10:03:44 [ERROR] x": ERROR,
    "2026/10/28 10:03:44 FATAL x": FATAL,
    "<warning> x": WARN,
  };
  for (const [line, want] of Object.entries(cases)) assert.equal(textLevel(line), want, line);
});

test("stripLevelWord", () => {
  const cases: Record<string, string> = {
    "INFO started": "started",
    "WARNING: disk": "disk",
    ERROR: "ERROR",
    "Error handling": "Error handling",
    "[warn] slow": "[warn] slow",
    WARN: "WARN",
    "DEBUG   spaced x": "  spaced x",
  };
  for (const [line, want] of Object.entries(cases)) {
    assert.equal(stripLevelWord(line, textLevel(line) || INFO), want, line);
  }
});

test("scrub", () => {
  const input = `login password=hunter2 token: "abc def" Authorization: Bearer abcdefghijkl key AKIAABCDEFGHIJKLMNOP jwt eyJhbGciOiJ.eyJzdWIiOiIx.c2lnbmF0dXJl -----BEGIN RSA PRIVATE KEY-----
MIIE
-----END RSA PRIVATE KEY----- done`;
  const got = scrub(input);
  for (const leak of ["hunter2", "abc def", "abcdefghijkl", "AKIAABCDEFGHIJKLMNOP", "eyJhbGciOiJ", "MIIE"]) {
    assert.ok(!got.includes(leak), `${leak} leaked in ${got}`);
  }
  assert.ok(got.includes("password=[REDACTED]"), got);
  assert.ok(got.includes("Bearer [REDACTED]"), got);
  assert.ok(got.includes("[REDACTED:aws-key]") && got.includes("[REDACTED:jwt]") && got.includes("[REDACTED:private-key]"), got);
  assert.ok(got.endsWith(" done"), got);
  assert.equal(scrub("plain text"), "plain text");
  // The patterns are global: a second call scrubs the same way.
  assert.equal(scrub("pwd=a pwd=b"), "pwd=[REDACTED] pwd=[REDACTED]");
});

test("encodeAttrs redacts, scrubs and stays under the cap", () => {
  const s = encodeAttrs({ "user.password": "x", n: 1, note: "token=abc", ssn: "1" }, secretKeys(["SSN"]));
  assert.equal(s, `{"n":1,"note":"token=[REDACTED]","ssn":"[REDACTED]","user.password":"[REDACTED]"}`);
  assert.equal(encodeAttrs({}, secretKeys()), "");

  const big: Record<string, string> = {};
  for (let i = 0; i < 40; i++) big["k".repeat(i + 1)] = "v".repeat(2000);
  const cut = encodeAttrs(big, secretKeys());
  assert.ok(Buffer.byteLength(cut) <= MAX_ATTRS, `${Buffer.byteLength(cut)} bytes`);
  const parsed = JSON.parse(cut) as Record<string, unknown>;
  // Long values are cut first; 40 of them still do not fit, so keys go too.
  assert.equal(parsed.k, "v".repeat(1024) + "…");
  assert.equal(parsed._truncated, true);
  const few = encodeAttrs({ a: "v".repeat(20_000), b: "short" }, secretKeys());
  assert.equal(few, JSON.stringify({ a: "v".repeat(1024) + "…", b: "short" }));

  const many: Record<string, string> = {};
  for (let i = 0; i < 2000; i++) {
    const key = "x".repeat(10) + String.fromCharCode(97 + (i % 26)) + "y".repeat(i % 7) + String.fromCharCode(65 + (Math.floor(i / 26) % 26)) + String(Math.floor(i / 676));
    many[key] = "value";
  }
  const kept = encodeAttrs(many, secretKeys());
  assert.ok(Buffer.byteLength(kept) <= MAX_ATTRS);
  const doc = JSON.parse(kept) as Record<string, unknown>;
  assert.equal(doc._truncated, true);
  assert.ok(Object.keys(many).sort()[0]! in doc);
});

test("truncateBytes cuts on a character boundary", () => {
  assert.equal(truncateBytes("héllo", 2), "h");
  assert.equal(truncateBytes("abc", 5), "abc");
  assert.equal(truncateBytes("a€b", 3), "a");
});

test("parseLogSettings and floors", () => {
  const iso = (ms: number) => new Date(ms).toISOString();
  const overrides = JSON.stringify([
    { route: "/checkout", level: "debug", until: iso(Date.now() + 3600_000) },
    { user: "42", level: "trace" },
    { route: "/health", level: "warn" },
    { route: "/admin/*", level: "debug" },
    { route: "/old", level: "debug", until: iso(Date.now() - 3600_000) },
    { service: "other", level: "debug" },
    { level: "nonsense" },
    { route: "/bad", level: "debug", until: "tomorrow" },
  ]);
  const s = parseLogSettings(
    [
      ["logs.level", "warning"],
      ["logs.overrides", overrides],
      ["logs.rate", "-1"],
      ["logs.slow_ms", "250"],
      ["logs.buffer", "nope"],
      ["logs.enabled", "false"],
    ],
    0,
  );
  assert.equal(s.level, WARN);
  assert.equal(s.rate, 6000);
  assert.equal(s.slowMs, 250);
  assert.equal(s.buffer, true);
  assert.equal(s.enabled, false);
  assert.equal(s.overrides.length, 6);

  const now = Date.now();
  const cases: [string, string, number][] = [
    ["/checkout", "", DEBUG],
    ["/health", "", WARN],
    ["/admin/users", "", DEBUG],
    ["/old", "", WARN],
    ["/any", "42", TRACE],
    ["/health", "42", TRACE],
    ["/any", "", WARN],
  ];
  for (const [route, user, want] of cases) assert.equal(floor(s, "api", route, user, now), want, `${route} ${user}`);
  assert.equal(floor(s, "other", "", "", now), DEBUG);
  assert.equal(holdBelow(s, "api", now), WARN);
});

test("settings defaults and the base level", () => {
  const d = parseLogSettings([], 0);
  assert.deepEqual(
    [d.enabled, d.level, d.buffer, d.bufferLevel, d.slowMs, d.rate],
    [true, INFO, true, DEBUG, 1000, 6000],
  );
  assert.equal(parseLogSettings([], DEBUG).level, DEBUG);
  assert.equal(parseLogSettings([["logs.level", "loud"]], WARN).level, WARN);
  assert.equal(parseLogSettings([["logs.rate", "0"]]).rate, 0);
  assert.equal(parseLogSettings([["logs.overrides", "not json"]]).overrides.length, 0);
  // A quieter service-wide override raises the floor; a route override
  // that keeps more holds everything below the highest level any could give.
  const s = parseLogSettings([["logs.overrides", JSON.stringify([{ level: "error" }, { route: "/x", level: "debug" }])]]);
  assert.equal(holdBelow(s, "api", Date.now()), ERROR);
  assert.equal(floor(s, "api", "/y", "", Date.now()), ERROR);
  assert.equal(floor(s, "api", "/x", "", Date.now()), DEBUG);
});

test("dropped", () => {
  const drops = JSON.stringify([{ contains: "healthcheck" }, { logger: "noisy", level: "info" }, { service: "web", level: "debug" }, {}]);
  const s = parseLogSettings([["logs.drop", drops]], INFO);
  assert.equal(s.drops.length, 3);
  const cases: [string, string, string, number, boolean][] = [
    ["api", "", "healthcheck ok", ERROR, true],
    ["api", "noisy", "x", INFO, true],
    ["api", "noisy", "x", WARN, false],
    ["web", "", "x", DEBUG, true],
    ["web", "", "x", INFO, false],
    ["api", "", "x", TRACE, false],
  ];
  for (const [service, logger, msg, sev, want] of cases) {
    assert.equal(dropped(s, service, logger, msg, sev), want, `${service} ${logger} ${msg} ${sev}`);
  }
});

test("parseLevel", () => {
  const cases: Record<string, number> = { WARNING: WARN, crit: FATAL, notice: INFO, Severe: ERROR, "": 0, loud: 0 };
  for (const [name, want] of Object.entries(cases)) assert.equal(parseLevel(name), want, name);
  assert.equal(levelName(0), "info");
  assert.equal(levelName(FATAL), "fatal");
});
