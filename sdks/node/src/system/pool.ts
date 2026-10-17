import type { DbStats } from "./recorder.js";

/**
 * Pool stats from a `pg.Pool` for the db_* system columns:
 *
 *     visionary.init({ dbStats: pgPoolStats(pool) })
 *
 * Acquires are counted from the pool's "acquire" event; an acquire that had
 * to wait (the pool was exhausted) counts as an empty acquire.
 */
export function pgPoolStats(pool: any): () => DbStats {
  let acquires = 0;
  let emptyAcquires = 0;
  pool.on?.("acquire", () => {
    acquires++;
    if (pool.waitingCount > 0) emptyAcquires++;
  });
  return () => {
    const total = Number(pool.totalCount) || 0;
    const idle = Number(pool.idleCount) || 0;
    return {
      total,
      idle,
      inUse: total - idle,
      max: typeof pool.options?.max === "number" ? pool.options.max : undefined,
      acquires,
      emptyAcquires,
    };
  };
}

/** Pool stats from a `mysql2` pool (callback or promise API). */
export function mysqlPoolStats(pool: any): () => DbStats {
  const core = pool?.pool ?? pool;
  let acquires = 0;
  core.on?.("acquire", () => acquires++);
  return () => {
    const total = core._allConnections?.length ?? 0;
    const idle = core._freeConnections?.length ?? 0;
    return { total, idle, inUse: total - idle, max: core.config?.connectionLimit, acquires };
  };
}
