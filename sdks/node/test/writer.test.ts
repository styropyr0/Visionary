import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { test } from "node:test";
import { Agent } from "../src/agent.js";
import { resolveConfig } from "../src/config.js";
import type { Stmt, Table, Value } from "../src/store/sql.js";
import { LOG_COLUMNS, MAX_PARAMS, SPAN_COLUMNS, insertRows, insertSQL } from "../src/store/sql.js";
import type { Store } from "../src/store/store.js";
import type { Item } from "../src/writer.js";
import { Writer } from "../src/writer.js";

const quiet = { info() {}, warn() {}, error() {} };

/** Records what it is asked to write. */
function fakeStore(dialect: Store["dialect"], stringParams = false, fail = false): Store & { written: Stmt[][] } {
  const written: Stmt[][] = [];
  return {
    dialect,
    stringParams,
    written,
    async execScript() {},
    async batch(stmts) {
      if (fail) throw new Error("down");
      written.push(stmts);
    },
    async execChanges() {
      return 0;
    },
    async columns() {
      return [];
    },
    async rows() {
      return [];
    },
    async close() {},
  };
}

const span = (i: number): Value[] => SPAN_COLUMNS.map((_, c) => (c === 1 ? `r${i}` : c === 7 ? null : c));
const logRow = (i: number): Value[] => LOG_COLUMNS.map((_, c) => (c === 10 ? `line ${i}` : null));

test("a table's rows split into multi-row INSERTs under the parameter limit", () => {
  const rows = Array.from({ length: 3000 }, (_, i) => logRow(i));
  const stmts = insertRows("app_logs", rows);
  const per = Math.floor(MAX_PARAMS / LOG_COLUMNS.length);
  assert.deepEqual(stmts.map((s) => s.rows), [per, per, 3000 - 2 * per]);
  for (const s of stmts) {
    assert.ok(s.args.length <= MAX_PARAMS);
    assert.equal(s.args.length, s.rows! * LOG_COLUMNS.length);
    assert.equal(s.sql.split("?").length - 1, s.args.length);
    assert.ok(s.sql.startsWith(`INSERT INTO app_logs (${LOG_COLUMNS.join(", ")}) VALUES (?, `));
    assert.ok(!s.sql.includes("NULLIF"));
  }
  assert.deepEqual(stmts[1]!.args.slice(0, LOG_COLUMNS.length), logRow(per));
  assert.equal(stmts[0]!.args[0], null);
});

test("SQL batches group rows by table in a few statements", async () => {
  const store = fakeStore("postgres");
  const w = new Writer(resolveConfig({}, {}), store, async () => true, quiet, { logRow: () => logRow(0) });
  assert.equal(w.batchSize, 500);
  assert.equal(w.parallel, 4);
  const batch: Item[] = [];
  for (let i = 0; i < 400; i++) batch.push({ table: "spans", args: span(i) }, { table: "errors", args: [i] });
  const stmts = await w.statements(batch);
  assert.deepEqual(stmts.map((s) => [s.sql.slice(0, 18), s.rows]), [["INSERT INTO spans ", 400], ["INSERT INTO errors", 400]]);
  assert.deepEqual(stmts[0]!.args.slice(SPAN_COLUMNS.length, 2 * SPAN_COLUMNS.length), span(1));
  assert.equal(stmts[0]!.args[7], null);
});

test("D1 keeps one NULLIF insert per row, 50 to a batch, one at a time", async () => {
  const store = fakeStore("sqlite", true);
  const w = new Writer(resolveConfig({ batchSize: 500 }, {}), store, async () => true, quiet);
  assert.equal(w.batchSize, 50);
  assert.equal(w.parallel, 1);
  const stmts = await w.statements([{ table: "spans", args: span(1) }, { table: "spans", args: span(2) }]);
  assert.deepEqual(stmts, [
    { sql: insertSQL("spans", true), args: span(1) },
    { sql: insertSQL("spans", true), args: span(2) },
  ]);
  assert.ok(stmts[0]!.sql.includes("NULLIF(?, '')"));
});

test("batch size: app choice up to 2000, SQLite one write at a time", () => {
  const sqlite = fakeStore("sqlite");
  assert.equal(new Writer(resolveConfig({ batchSize: 2000 }, {}), sqlite, async () => true, quiet).batchSize, 2000);
  assert.equal(new Writer(resolveConfig({ batchSize: 5000 }, {}), sqlite, async () => true, quiet).batchSize, 500);
  assert.equal(new Writer(resolveConfig({}, {}), sqlite, async () => true, quiet).parallel, 1);
});

test("stats count queued, written and dropped rows", async () => {
  const store = fakeStore("mysql");
  const w = new Writer(resolveConfig({ batchSize: 10 }, {}), store, async () => true, quiet, { queueSize: 25 });
  for (let i = 0; i < 30; i++) w.push({ table: "spans", args: span(i) });
  assert.deepEqual(w.stats(), { queued: 25, written: 0, dropped: 5 });
  await w.close(5000);
  assert.deepEqual(w.stats(), { queued: 0, written: 25, dropped: 5 });
  assert.deepEqual(store.written.map((b) => b[0]!.rows), [10, 10, 5]);
});

const nodeSqlite = createRequire(import.meta.url)("node:sqlite") as { DatabaseSync?: new (path: string) => any } | undefined;

test("rows land in SQLite with NULLs intact", { skip: !nodeSqlite?.DatabaseSync && "needs node:sqlite" }, async () => {
  const db = new nodeSqlite!.DatabaseSync!(":memory:");
  const agent = new Agent({
    db,
    service: "shop",
    instance: "i-1",
    logRequests: false,
    systemMetrics: false,
    logs: false,
    metrics: false,
    captureUncaught: false,
    profile: false,
    instrumentHttp: false,
    logger: quiet,
  }).start();
  const writer = (agent as unknown as { writer: Writer }).writer;
  const table: Table = "spans";
  for (let i = 0; i < 1200; i++) {
    writer.push({ table, args: [`2026-01-01T00:00:00.000Z`, `r${i}`, null, "i-1", "db", "SELECT 1", null, null, i, 1.5, "ok", null, null, null, null, null] });
  }
  await agent.close(10_000);
  const [count] = db.prepare("SELECT COUNT(*) AS n, COUNT(service) AS s, SUM(offset_ms) AS o FROM spans").all();
  assert.deepEqual({ ...count }, { n: 1200, s: 0, o: (1199 * 1200) / 2 });
  assert.deepEqual(agent.writerStats(), { queued: 0, written: 1200, dropped: 0 });
});

test("a batch that fails every retry counts as dropped", { timeout: 10_000 }, async () => {
  const w = new Writer(resolveConfig({}, {}), fakeStore("postgres", false, true), async () => true, quiet);
  w.push({ table: "spans", args: span(1) });
  await w.close(8000);
  assert.deepEqual(w.stats(), { queued: 0, written: 0, dropped: 1 });
});
