import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { PACKAGE_COLUMNS } from "../src/store/sql.js";
import { appRoot, npmPackages, packageRows } from "../src/telemetry/packages.js";

async function pkg(dir: string, name: string, version: string): Promise<void> {
  await mkdir(dir, { recursive: true });
  await writeFile(join(dir, "package.json"), JSON.stringify({ name, version }));
}

test("walks node_modules, nested and scoped, once per name and version", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "visionary-pkgs-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const nm = join(root, "node_modules");
  await pkg(join(nm, "express"), "express", "4.21.2");
  await pkg(join(nm, "express", "node_modules", "debug"), "debug", "2.6.9");
  await pkg(join(nm, "debug"), "debug", "4.3.7");
  await pkg(join(nm, "other", "node_modules", "debug"), "debug", "4.3.7");
  await pkg(join(nm, "other"), "other", "1.0.0");
  await pkg(join(nm, "@scope", "thing"), "@scope/thing", "0.2.0");
  await pkg(join(nm, "noversion"), "noversion", "");
  await mkdir(join(nm, ".bin"), { recursive: true });
  await symlink(nm, join(nm, "other", "node_modules", "loop"), "junction").catch(() => undefined);
  await mkdir(join(root, "src"), { recursive: true });

  assert.equal(await appRoot(join(root, "src", "server.js")), root);
  const found = (await npmPackages(root)).map(([n, v]) => `${n}@${v}`).sort();
  assert.deepEqual(found, ["@scope/thing@0.2.0", "debug@2.6.9", "debug@4.3.7", "express@4.21.2", "other@1.0.0"]);
});

test("rows carry the identity and cap at 5000", () => {
  const rows = packageRows([["a", "1.0.0"]], "2026-01-01T00:00:00.000Z", { service: "", instance: "i", version: "abc" });
  assert.deepEqual(rows, [["2026-01-01T00:00:00.000Z", null, "i", "abc", "npm", "a", "1.0.0"]]);
  assert.equal(rows[0]!.length, PACKAGE_COLUMNS.length);
  const many = Array.from({ length: 6000 }, (_, i): [string, string] => [`p${i}`, "1.0.0"]);
  assert.equal(packageRows(many, "", { service: "", instance: "i", version: "" }).length, 5000);
});
