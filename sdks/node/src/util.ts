import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";

/** `YYYY-MM-DDTHH:MM:SS.mmmZ` in UTC, so string order is time order. */
export function formatTs(epochMs: number): string {
  return new Date(epochMs).toISOString();
}

export function round(v: number, digits: number): number {
  const p = 10 ** digits;
  return Math.round(v * p) / p;
}

/** Milliseconds between two process.hrtime.bigint() readings. */
export function msBetween(start: bigint, end: bigint): number {
  return Number(end - start) / 1e6;
}

const requireFromSdk = createRequire(import.meta.url);

/**
 * Loads an optional dependency the application installed (pg, mysql2,
 * better-sqlite3), looking next to the SDK first and then from the working
 * directory. Undefined when it is not installed.
 */
export function optionalRequire<T = any>(name: string): T | undefined {
  try {
    return requireFromSdk(name) as T;
  } catch {
    // fall through
  }
  try {
    return createRequire(pathToFileURL(process.cwd() + "/").href)(name) as T;
  } catch {
    return undefined;
  }
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
