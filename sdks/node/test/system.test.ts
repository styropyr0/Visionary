import assert from "node:assert/strict";
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { SYSTEM_COLUMNS, type Value } from "../src/store/sql.js";
import { CgroupReader } from "../src/system/cgroup.js";
import {
  HostStats,
  parseCgroupMemEvents,
  parseCgroupMemStat,
  parseMemInfo,
  parsePressure,
  parseVMStat,
} from "../src/system/hoststats.js";
import { systemRowStmts } from "../src/system/recorder.js";

const FIXTURES = path.resolve("test/fixtures/linux");
const fixture = (name: string) => readFileSync(path.join(FIXTURES, name), "utf8");

const temps: string[] = [];
after(() => temps.forEach((d) => rmSync(d, { recursive: true, force: true })));

function tempDir(): string {
  const d = mkdtempSync(path.join(os.tmpdir(), "visionary-sys-"));
  temps.push(d);
  return d;
}

function replace(file: string, from: string, to: string): void {
  const text = readFileSync(file, "utf8");
  assert.ok(text.includes(from), `${file} has no ${from}`);
  writeFileSync(file, text.replace(from, to));
}

test("parsers read the Go fixtures", () => {
  assert.deepEqual(parsePressure(fixture("proc/pressure/memory")), { someAvg10: 4.2, someAvg60: 2.1, fullAvg10: 1.5 });
  assert.equal(parsePressure("garbage\n"), undefined);
  assert.deepEqual(parseVMStat(fixture("proc/vmstat")), {
    pgfault: 50000000,
    pgmajfault: 20000,
    pswpin: 1000,
    pswpout: 2000,
    pgscan: 69000,
    pgsteal: 35000,
    oomKill: 3,
  });
  assert.deepEqual(parseMemInfo(fixture("proc/meminfo")), {
    dirty: 10240,
    writeback: 2048,
    slab: 819200,
    shmem: 524288,
    committed: 20971520,
    commitLimit: 12345678,
  });
  assert.deepEqual(parseCgroupMemStat("anon 123\npgfault 900\npgmajfault 12\n"), { faults: 900, major: 12 });
  assert.deepEqual(parseCgroupMemStat("cache 1\npgfault 5\ntotal_pgfault 800\ntotal_pgmajfault 7\n"), { faults: 800, major: 7 });
  assert.deepEqual(parseCgroupMemEvents("low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n"), { high: 14, max: 3 });
});

test("host pressure and virtual memory from a fake /proc", () => {
  const root = tempDir();
  cpSync(FIXTURES, root, { recursive: true });
  const h = new HostStats(undefined, root);
  h.prime();
  const vmstat = path.join(root, "proc", "vmstat");
  replace(vmstat, "pgfault 50000000", "pgfault 50030000");
  replace(vmstat, "pgmajfault 20000", "pgmajfault 20150");
  replace(vmstat, "pswpin 1000", "pswpin 1300");
  replace(vmstat, "pgscan_kswapd 60000", "pgscan_kswapd 61500");
  replace(vmstat, "oom_kill 3", "oom_kill 4");
  const v: Record<string, Value | undefined> = {};
  h.sample(v, 15);
  assert.deepEqual(
    { ...v },
    {
      psi_cpu_some_avg10: 12.5, psi_cpu_some_avg60: 8.25, psi_mem_some_avg10: 4.2, psi_mem_some_avg60: 2.1, psi_mem_full_avg10: 1.5,
      psi_io_some_avg10: 30, psi_io_some_avg60: 22.4, psi_io_full_avg10: 18.75,
      minor_faults_ps: 1990, major_faults_ps: 10, swap_in_ps: 20, swap_out_ps: 0, pages_scanned_ps: 100, pages_stolen_ps: 0,
      host_oom_kills: 1,
      mem_dirty_mb: 10, mem_writeback_mb: 2, mem_slab_mb: 800, mem_shmem_mb: 512, mem_committed_mb: 20480, mem_commit_limit_mb: 12056.3,
    },
  );
});

test("a container reads its cgroup's pressure, faults and memory events", () => {
  const root = tempDir();
  const cg = path.join(root, "sys", "fs", "cgroup");
  mkdirSync(path.join(root, "proc", "self"), { recursive: true });
  mkdirSync(cg, { recursive: true });
  cpSync(path.join(FIXTURES, "proc"), path.join(root, "proc"), { recursive: true });
  cpSync(path.join(FIXTURES, "cgroup"), cg, { recursive: true });
  writeFileSync(path.join(root, ".dockerenv"), "");
  writeFileSync(path.join(root, "proc", "self", "cgroup"), "0::/\n");
  writeFileSync(path.join(cg, "cpu.stat"), "usage_usec 1000\nnr_periods 0\nnr_throttled 0\n");
  writeFileSync(path.join(cg, "cpu.max"), "max 100000\n");
  writeFileSync(path.join(cg, "memory.max"), "max\n");
  writeFileSync(path.join(cg, "memory.current"), "104857600\n");
  writeFileSync(path.join(cg, "memory.stat"), "anon 123\ninactive_file 0\npgfault 900\npgmajfault 12\n");
  writeFileSync(path.join(cg, "memory.events"), "low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n");

  const c = CgroupReader.open(root)!;
  assert.equal(path.resolve(c.pressureDir()!), cg);
  c.prime();
  replace(path.join(cg, "memory.stat"), "pgfault 900\npgmajfault 12", "pgfault 1900\npgmajfault 112");
  replace(path.join(cg, "memory.events"), "high 14", "high 20");
  const v: Record<string, Value | undefined> = {};
  c.sample(v);
  assert.equal(typeof v.container_minor_faults_ps, "number");
  assert.ok((v.container_major_faults_ps as number) > 0);
  assert.equal(v.container_mem_high_events, 6);
  assert.equal(v.container_mem_max_events, 0);

  const h = new HostStats(c.pressureDir(), root);
  h.prime();
  const p: Record<string, Value | undefined> = {};
  h.sample(p, 15);
  assert.equal(p.psi_cpu_some_avg10, 55.1);
  assert.equal(p.psi_mem_some_avg10, 0);
  assert.equal(p.psi_io_full_avg10, 0.9);

  const v1: Record<string, Value | undefined> = {};
  new HostStats("", root).sample(v1, 15);
  assert.equal(v1.psi_cpu_some_avg10, undefined, "a v1 container read the host's pressure");
  assert.equal(v1.mem_dirty_mb, 10);
});

test("D1 rows split after 100 params", () => {
  const row = SYSTEM_COLUMNS.map((_, i) => i);
  assert.ok(SYSTEM_COLUMNS.length > 100);
  const [ins, upd] = systemRowStmts("INSERT", true, row);
  assert.equal(ins!.args.length, 100);
  assert.equal(ins!.sql.split("NULLIF").length - 1, 100);
  assert.equal(upd!.args.length, SYSTEM_COLUMNS.length - 100);
  assert.equal(upd!.args[0], 100);
  assert.ok(upd!.sql.startsWith(`UPDATE system_metrics SET ${SYSTEM_COLUMNS[100]} = NULLIF(?, '')`));
  assert.ok(upd!.sql.endsWith("WHERE id = last_insert_rowid()"));
  assert.deepEqual(systemRowStmts("INSERT", false, row), [{ sql: "INSERT", args: row }]);
});
