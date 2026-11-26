import { access, readFile, readdir, realpath } from "node:fs/promises";
import { dirname, join } from "node:path";
import type { Logger } from "../log.js";
import { errMessage } from "../log.js";
import type { Value } from "../store/sql.js";
import { insertRows, insertSQL } from "../store/sql.js";
import type { Store } from "../store/store.js";
import { formatTs } from "../util.js";

export const PACKAGES_DELAY_MS = 30_000;
const MAX_PACKAGES = 5000;
const MAX_DEPTH = 10;

export interface PackageIdentity {
  service: string;
  instance: string;
  version: string;
}

/** The nearest folder above the entry script with a node_modules, else the working directory */
export async function appRoot(entry: string | undefined = process.argv[1]): Promise<string> {
  if (!entry) return process.cwd();
  for (let dir = dirname(entry); ; ) {
    try {
      await access(join(dir, "node_modules"));
      return dir;
    } catch {
      const up = dirname(dir);
      if (up === dir) return process.cwd();
      dir = up;
    }
  }
}

/** Every [name, version] under root's node_modules, nested and scoped ones included */
export async function npmPackages(root: string): Promise<[string, string][]> {
  const found = new Map<string, [string, string]>();
  const walked = new Set<string>();
  const list = (dir: string) => readdir(dir).catch(() => [] as string[]);

  const visit = async (dir: string, depth: number): Promise<void> => {
    try {
      const pkg = JSON.parse(await readFile(join(dir, "package.json"), "utf8"));
      if (typeof pkg.name === "string" && typeof pkg.version === "string" && pkg.name && pkg.version) {
        found.set(`${pkg.name}@${pkg.version}`, [pkg.name, pkg.version]);
      }
    } catch {
      // not a package
    }
    await walk(join(dir, "node_modules"), depth + 1);
  };

  const walk = async (modules: string, depth: number): Promise<void> => {
    if (depth > MAX_DEPTH || found.size >= MAX_PACKAGES) return;
    let real: string;
    try {
      real = await realpath(modules);
    } catch {
      return;
    }
    if (walked.has(real)) return;
    walked.add(real);
    for (const name of await list(real)) {
      if (name === ".pnpm") {
        for (const p of await list(join(real, name))) await walk(join(real, name, p, "node_modules"), depth + 1);
      } else if (name.startsWith("@")) {
        for (const sub of await list(join(real, name))) await visit(join(real, name, sub), depth);
      } else if (!name.startsWith(".")) {
        await visit(join(real, name), depth);
      }
    }
  };

  await walk(join(root, "node_modules"), 0);
  return [...found.values()];
}

export function packageRows(pkgs: [string, string][], at: string, id: PackageIdentity): Value[][] {
  return pkgs
    .slice(0, MAX_PACKAGES)
    .map(([name, version]) => [at, id.service || null, id.instance, id.version || null, "npm", name, version]);
}

/** Writes app_packages once, logging failures instead of throwing */
export async function reportPackages(
  store: Store,
  ready: () => Promise<boolean>,
  id: PackageIdentity,
  logger: Logger,
): Promise<void> {
  try {
    const rows = packageRows(await npmPackages(await appRoot()), formatTs(Date.now()), id);
    if (rows.length === 0) return;
    if (!(await ready())) throw new Error("tables not ready");
    const sql = insertSQL("app_packages", true);
    await store.batch(store.stringParams ? rows.map((args) => ({ sql, args })) : insertRows("app_packages", rows));
  } catch (e) {
    logger.error(`visionary: write packages failed: ${errMessage(e)}`);
  }
}
