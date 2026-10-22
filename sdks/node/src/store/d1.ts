import { quietly } from "../context.js";
import type { Stmt, Value } from "./sql.js";
import { statements } from "./sql.js";
import type { Store } from "./store.js";

const DEFAULT_API_BASE = "https://api.cloudflare.com/client/v4";
const MAX_BATCH = 50;
const RESPONSE_LIMIT = 1 << 20;

export class D1Error extends Error {}

/** Cloudflare D1 over its REST API. Params are always strings. */
export class D1Store implements Store {
  readonly dialect = "sqlite" as const;
  readonly stringParams = true;
  private readonly queryUrl: string;
  private readonly rawUrl: string;

  constructor(accountId: string, databaseId: string, private readonly token: string, apiBase = "") {
    const base = (apiBase || DEFAULT_API_BASE).trim().replace(/\/+$/, "");
    const db = `${base}/accounts/${accountId.trim()}/d1/database/${databaseId.trim()}`;
    this.queryUrl = db + "/query";
    this.rawUrl = db + "/raw";
    this.token = token.trim();
  }

  async execScript(script: string): Promise<void> {
    await this.batch(statements(script).map((sql) => ({ sql, args: [] })));
  }

  async batch(stmts: Stmt[]): Promise<void> {
    for (let i = 0; i < stmts.length; i += MAX_BATCH) {
      const chunk = stmts.slice(i, i + MAX_BATCH);
      await this.post(this.queryUrl, { batch: chunk.map((s) => ({ sql: s.sql, params: s.args.map(d1Param) })) });
    }
  }

  async execChanges(sql: string, args: Value[]): Promise<number> {
    const result = (await this.post(this.queryUrl, { sql, params: args.map(d1Param) })) as any;
    return Number(result?.[0]?.meta?.changes) || 0;
  }

  async rows(sql: string, args: Value[]): Promise<string[][]> {
    const result = (await this.post(this.rawUrl, { sql, params: args.map(d1Param) })) as any;
    const rows: unknown[][] = result?.[0]?.results?.rows ?? [];
    return rows.map((r) => r.map((v) => (v === null || v === undefined ? "" : String(v))));
  }

  async columns(sql: string): Promise<string[]> {
    const result = (await this.post(this.rawUrl, { sql })) as any;
    const rows: unknown[][] = result?.[0]?.results?.rows ?? [];
    return rows.filter((r) => r.length > 0).map((r) => String(r[0]));
  }

  async close(): Promise<void> {}

  private post(url: string, body: unknown): Promise<unknown> {
    // The SDK's own calls must not show up as outbound spans.
    return quietly(async () => {
      let res: Response;
      try {
        res = await fetch(url, {
          method: "POST",
          headers: { Authorization: "Bearer " + this.token, "Content-Type": "application/json" },
          body: JSON.stringify(body),
          signal: AbortSignal.timeout(20_000),
        });
      } catch (e) {
        throw new D1Error("d1: request: " + ((e as Error)?.message ?? e));
      }
      const text = await res.text();
      if (text.length > RESPONSE_LIMIT) throw new D1Error(`d1: response larger than ${RESPONSE_LIMIT} bytes`);
      let env: any;
      try {
        env = JSON.parse(text);
      } catch {
        throw new D1Error(`d1: status ${res.status}: unreadable response`);
      }
      if (res.status >= 300 || !env?.success) {
        const msgs = Array.isArray(env?.errors) ? env.errors.map((e: any) => String(e?.message ?? "")).filter(Boolean) : [];
        throw new D1Error(`d1: status ${res.status}: ${msgs.join("; ") || "unknown D1 error"}`);
      }
      return env.result;
    });
  }
}

function d1Param(v: Value): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "number") return Number.isFinite(v) ? String(v) : "";
  return v;
}
