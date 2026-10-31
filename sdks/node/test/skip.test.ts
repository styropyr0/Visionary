import assert from "node:assert/strict";
import { test } from "node:test";
import { SKIP_SETTING, SkipList, matchSkip, parseSkip } from "../src/skip.js";

// The same rules as sdks/go/skiplist.

test("parseSkip", () => {
  assert.deepEqual(parseSkip(`[" /health ", "", "  ", "/static/*"]`), ["/health", "/static/*"]);
  assert.deepEqual(parseSkip(""), []);
  assert.deepEqual(parseSkip("not json"), []);
  assert.deepEqual(parseSkip(`{"a": "/x"}`), []);
  assert.deepEqual(parseSkip(`"/x"`), []);
  assert.deepEqual(parseSkip(`["/x", 1]`), []);
});

test("matchSkip", () => {
  const list = ["/health", "/static/*"];
  assert.ok(matchSkip(list, "/health"));
  assert.ok(!matchSkip(list, "/health/deep"));
  assert.ok(!matchSkip(list, "/Health"));
  assert.ok(matchSkip(list, "/static/app.js"));
  assert.ok(matchSkip(list, "/static/"));
  assert.ok(!matchSkip(list, "/static"));
  assert.ok(matchSkip(["*"], "/anything"));
  assert.ok(!matchSkip([], "/health"));
});

test("SkipList keeps the last list when a read fails", async () => {
  let rows: string[][] | Error = [["requests.other", "x"], [SKIP_SETTING, `["/health"]`]];
  const s = new SkipList(async () => {
    if (rows instanceof Error) throw rows;
    return rows;
  });
  assert.ok(!s.has("/health"));
  await s.poll();
  assert.ok(s.has("/health"));
  rows = new Error("no table");
  await s.poll();
  assert.ok(s.has("/health"));
  rows = [];
  await s.poll();
  assert.ok(!s.has("/health"));
});
