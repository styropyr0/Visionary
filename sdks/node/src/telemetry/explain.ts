import { quietly } from "../context.js";
import type { Logger } from "../log.js";
import type { Store } from "../store/store.js";
import { formatTs } from "../util.js";
import { pgTarget } from "./db.js";
import { MAX_DETAIL } from "./spans.js";

/**
 * Plans statements on one database for the dashboard. Visionary never
 * connects to the application's database: the server queues a request in
 * explain_requests, and an Agent given an Explainer whose target matches
 * claims it, runs EXPLAIN (never EXPLAIN ANALYZE: nothing is executed) and
 * writes the plan back.
 */
export interface Explainer {
  /** Must match the target the database's spans carry ("postgresql/shop"). */
  target: string;
  run(statement: string): Promise<string>;
}

const NULL_NOTE = "-- Planned with values set to NULL, so it is approximate.\n";
const NUMBER_NOTE = "-- Planned with values used in arithmetic set to 1, so it is approximate.\n";

/** One statement EXPLAIN can plan; anything else is refused. */
export function explainable(statement: string): string {
  const s = statement.trim().replace(/;+\s*$/, "").trim();
  if (!s || s.includes(";")) throw new Error("one statement only");
  const word = /^\s*\(*\s*([A-Za-z]+)/.exec(s)?.[1]?.toUpperCase() ?? "";
  if (!["SELECT", "WITH", "INSERT", "UPDATE", "DELETE", "VALUES", "TABLE"].includes(word)) {
    throw new Error(`${word || "this"} statements have no plan`);
  }
  // INTERVAL '30 days' is stored as INTERVAL ?, and a parameter cannot go there.
  return s.replace(/\b(INTERVAL|DATE|TIMESTAMPTZ|TIMESTAMP|TIME)\s+\?/gi, (_, t: string) => TYPED_CONSTANTS[t.toUpperCase()] ?? _);
}

const TYPED_CONSTANTS: Record<string, string> = {
  INTERVAL: "INTERVAL '1 day'",
  DATE: "DATE '2000-01-01'",
  TIMESTAMP: "TIMESTAMP '2000-01-01'",
  TIMESTAMPTZ: "TIMESTAMPTZ '2000-01-01'",
  TIME: "TIME '00:00'",
};

/** Each ? (a value the SDK removed) becomes the next free $n. */
export function numbered(s: string): string {
  let next = 0;
  for (const m of s.matchAll(/\$(\d+)/g)) next = Math.max(next, Number(m[1]));
  return s.replace(/\?/g, () => "$" + ++next);
}

// A removed value is a number when arithmetic, a CASE branch, LIMIT, BETWEEN
// or a numeric function's first argument surrounds it.
const NUMBER_BEFORE = /(?:[-+*/%]|\b(?:THEN|ELSE|LIMIT|OFFSET|BETWEEN)|\b(?:chr|least|greatest|round|power|mod|abs)\(|\bBETWEEN\s+\S+\s+AND)\s*$/i;
const NUMBER_AFTER = /^\s*[-+*/%]/;

/**
 * numbered, except that a ? that can only be a number becomes 1: PostgreSQL
 * cannot choose an operator for -$1 or $1 * x when nothing says $1 is a
 * number. 1, not 0, so chr(?) and x / ? stay valid.
 */
export function numbersGuessed(s: string): string {
  const parts = s.split("?");
  let out = "";
  parts.forEach((p, i) => {
    out += p;
    if (i < parts.length - 1) out += NUMBER_BEFORE.test(out) || NUMBER_AFTER.test(parts[i + 1]) ? "1" : "?";
  });
  return numbered(out);
}

function withNulls(s: string): string {
  return s.replace(/\?/g, "NULL").replace(/\$\d+/g, "NULL");
}

/**
 * An Explainer for a `pg` Pool or Client, or a `mysql2` pool. The target is
 * worked out the way the query hooks name it; pass `target` when it differs.
 *
 *     visionary.init({ explain: [visionary.explainWith(pool)] });
 */
export function explainWith(db: any, opts: { target?: string } = {}): Explainer {
  const isMysql = typeof db?.execute === "function" && !("connectionParameters" in (db ?? {}));
  if (isMysql) {
    const database = db?.config?.connectionConfig?.database ?? db?.config?.database ?? "";
    return {
      target: opts.target ?? "mysql" + (database ? "/" + database : ""),
      async run(statement) {
        const [rows] = await db.query("EXPLAIN " + withNulls(explainable(statement)));
        const text = (rows as Record<string, unknown>[])
          .map((r) => Object.entries(r).map(([k, v]) => `${k}: ${v ?? "NULL"}`).join("\n"))
          .join("\n\n");
        return NULL_NOTE + text;
      },
    };
  }
  return {
    target: opts.target ?? pgTarget(db),
    async run(statement) {
      const stmt = explainable(statement);
      // No values: pg sends a simple query, so $n reach the server as written.
      const lines = async (q: string) => ((await db.query(q)).rows as Record<string, unknown>[]).map((r) => String(Object.values(r)[0]));
      // A generic plan with every removed value as a parameter; then one
      // keeping the application's own $n (real types) with removed values as
      // NULL, for parameters whose type cannot be guessed (GREATEST(?, 0.5));
      // then the generic plan with values that can only be numbers set to 1,
      // for arithmetic on them (-?, ? * x); then, before PostgreSQL 16, a plan
      // with NULLs.
      let first: unknown;
      try {
        return (await lines("EXPLAIN (GENERIC_PLAN) " + numbered(stmt))).join("\n");
      } catch (e) {
        first = e;
      }
      const tries: [string, string][] = [
        [NULL_NOTE, "EXPLAIN (GENERIC_PLAN) " + stmt.replace(/\?/g, "NULL")],
        [NUMBER_NOTE, "EXPLAIN (GENERIC_PLAN) " + numbersGuessed(stmt)],
        [NULL_NOTE, "EXPLAIN " + withNulls(stmt)],
      ];
      for (const [note, q] of tries) {
        try {
          return note + (await lines(q)).join("\n");
        } catch {
          // the next way
        }
      }
      throw planError(first);
    },
  };
}

/** Why a stored statement could not be planned, said plainly. */
function planError(e: unknown): unknown {
  const msg = String((e as Error)?.message ?? e);
  if (msg.includes("end of input")) {
    return new Error(`the stored statement looks cut short (spans keep up to ${MAX_DETAIL} characters; older SDK versions kept 2000), so it cannot be planned`);
  }
  if (/cannot be matched|could not determine|operator does not exist|is not unique/.test(msg)) {
    return new Error(`the statement's values were removed when it was stored, and the database cannot tell their types here, so it cannot be planned (${msg})`);
  }
  return e;
}

const EVERY_MS = 10_000;

/** Claims queued plan requests for its explainers until stopped. */
export class ExplainWorker {
  private timer?: NodeJS.Timeout;
  private busy = false;
  private readonly byTarget = new Map<string, Explainer>();

  constructor(
    explainers: Explainer[],
    private readonly store: Store,
    private readonly instance: string,
    private readonly logger: Logger,
  ) {
    for (const e of explainers) if (e?.target) this.byTarget.set(e.target, e);
  }

  start(): void {
    if (this.byTarget.size === 0) return;
    this.timer = setInterval(() => void this.poll(), EVERY_MS);
    this.timer.unref();
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
  }

  private async poll(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    try {
      let pending: string[][];
      try {
        pending = await this.store.rows(`SELECT id, target, statement FROM explain_requests WHERE status = 'pending' ORDER BY id LIMIT 5`, []);
      } catch {
        return; // the table appears once the Visionary server has started
      }
      for (const [id, target, statement] of pending) {
        const e = this.byTarget.get(target ?? "");
        if (!e || !id || statement === undefined) continue;
        const claimed = await this.store.execChanges(`UPDATE explain_requests SET status = 'running', claimed_by = ? WHERE id = ? AND status = 'pending'`, [this.instance, Number(id)]);
        if (claimed !== 1) continue;
        let status = "done";
        let plan = "";
        let error = "";
        try {
          plan = await quietly(() =>
            Promise.race([e.run(statement), new Promise<string>((_, reject) => setTimeout(() => reject(new Error("timed out after 15 s")), 15_000).unref())]),
          );
        } catch (err) {
          status = "failed";
          error = ((err as Error)?.message ?? String(err)).slice(0, 500);
        }
        await this.store
          .execChanges(`UPDATE explain_requests SET status = ?, plan = ?, error = ?, finished_at = ? WHERE id = ?`, [status, plan, error, formatTs(Date.now()), Number(id)])
          .catch((err) => this.logger.warn(`visionary: writing a query plan failed: ${(err as Error).message}`));
      }
    } finally {
      this.busy = false;
    }
  }
}
