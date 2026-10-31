import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { beforeEach, test } from "node:test";
import { Agent } from "../src/agent.js";
import {
  collectMetrics,
  configureMetrics,
  counter,
  describeMetric,
  gauge,
  histogram,
  metricSeries,
  resetMetrics,
} from "../src/metrics.js";
import { METRIC_COLUMNS, insertSQL } from "../src/store/sql.js";

beforeEach(() => resetMetrics());

test("series id and canonical labels", () => {
  assert.deepEqual(metricSeries("orders_placed", { region: "eu" }), ['{"region":"eu"}', "da4f5c6244c4f3e4"]);
  assert.equal(metricSeries("x", {})[0], "{}");
  const [text] = metricSeries("x", { b: 'q"\\\n<&é', a: "1", " ": "dropped", [" c "]: "" });
  assert.equal(text, '{"a":"1","b":"q\\"\\\\\\u000a<&é","c":""}');
  const many = Object.fromEntries(Array.from({ length: 25 }, (_, i) => [`k${String(i).padStart(2, "0")}`, "v"]));
  assert.equal(Object.keys(JSON.parse(metricSeries("x", many)[0])).length, 20);
  const long = metricSeries("x", { k: "é".repeat(150) })[0];
  assert.equal(JSON.parse(long).k, "é".repeat(100));
});

test("counters, gauges and histograms collect one row per series per interval", () => {
  const eu = counter("orders", { region: "eu" });
  eu.add(2);
  eu.inc();
  counter("orders", { region: "eu" }).add(1);
  eu.add(-1);
  eu.add(Number.NaN);
  eu.add(Infinity);
  counter("orders", { region: "us" });
  gauge("queue", { q: "mail" }).add(3);
  gauge("queue", { q: "mail" }).set(7);
  gauge("queue", { q: "mail" }).set(Number.NaN);
  describeMetric("took", { unit: "ms", buckets: [100, 10, 10] });
  const h = histogram("took");
  for (const v of [5, 10, 11, 500]) h.observe(v);
  counter("", { a: "1" }).add(1);

  const { rows } = collectMetrics(Date.UTC(2026, 0, 1));
  const byName = Object.fromEntries(rows.map((r) => [r[1], r]));
  assert.equal(rows.length, 3);
  assert.deepEqual(byName.orders, ["2026-01-01T00:00:00.000Z", "orders", "counter", null, '{"region":"eu"}', metricSeries("orders", { region: "eu" })[1], 4, null, null, null, null, null]);
  assert.deepEqual(byName.queue!.slice(2, 7), ["gauge", null, '{"q":"mail"}', metricSeries("queue", { q: "mail" })[1], 7]);
  assert.deepEqual(byName.took!.slice(2, 4), ["histogram", "ms"]);
  assert.deepEqual(byName.took!.slice(6), [131.5, 4, 526, 5, 500, '{"bounds":[10,100],"counts":[2,1,1]}']);
  assert.equal(byName.orders!.length + 2, METRIC_COLUMNS.length);

  const next = collectMetrics(Date.UTC(2026, 0, 1, 0, 0, 15)).rows;
  assert.deepEqual(next.map((r) => r[1]), ["queue"]);
});

test("new label sets past the cap are dropped and counted", () => {
  configureMetrics(true, 2);
  for (let i = 0; i < 5; i++) counter("hits", { user: String(i) }).add(1);
  counter("hits", { user: "0" }).add(1);
  gauge("other").set(1);
  const { rows, dropped } = collectMetrics(Date.now());
  assert.equal(rows.filter((r) => r[1] === "hits").length, 2);
  assert.deepEqual([...dropped], [["hits", 3]]);
  assert.equal(collectMetrics(Date.now()).dropped.size, 0);
});

test("VISIONARY_METRICS off makes every call a no-op", () => {
  configureMetrics(false, 0);
  counter("orders").add(1);
  assert.equal(collectMetrics(Date.now()).rows.length, 0);
});

const nodeSqlite = createRequire(import.meta.url)("node:sqlite") as { DatabaseSync?: new (path: string) => any } | undefined;

test("the agent writes the last interval on close", { skip: !nodeSqlite?.DatabaseSync && "needs node:sqlite" }, async () => {
  assert.equal(insertSQL("metrics", false).split("?").length - 1, 14);
  const db = new nodeSqlite!.DatabaseSync!(":memory:");
  const agent = new Agent({
    db,
    service: "shop",
    instance: "i-1",
    logRequests: false,
    spans: false,
    errors: false,
    systemMetrics: false,
    logs: false,
    captureUncaught: false,
    profile: false,
    instrumentHttp: false,
    logger: { info() {}, warn() {}, error() {} },
  }).start();
  counter("orders_placed", { region: "eu" }).add(3);
  await agent.close(5000);
  const rows = db.prepare("SELECT service, instance, name, kind, labels, series, value, count FROM metrics").all();
  assert.deepEqual(
    rows.map((r: Record<string, unknown>) => ({ ...r })),
    [{ service: "shop", instance: "i-1", name: "orders_placed", kind: "counter", labels: '{"region":"eu"}', series: "da4f5c6244c4f3e4", value: 3, count: null }],
  );
});
