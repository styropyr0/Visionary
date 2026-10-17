/**
 * Statement sanitizing and naming for db spans: string and numeric literals
 * become ?, IN lists collapse to IN (?), whitespace collapses. Parameter values
 * are never stored.
 */
export function sanitizeSQL(sql: string): string {
  return sql
    .replace(/'(?:[^']|'')*'/g, "?")
    .replace(/(?<![\w$.?])-?\d+(?:\.\d+)?(?:e[+-]?\d+)?(?![\w])/gi, "?")
    .replace(/\bIN\s*\(\s*\?(?:\s*,\s*\?)*\s*\)/gi, "IN (?)")
    .replace(/\s+/g, " ")
    .trim();
}

const TABLE_AFTER: Record<string, RegExp> = {
  SELECT: /\bFROM\s+([^\s,;()]+)/i,
  DELETE: /\bFROM\s+([^\s,;()]+)/i,
  INSERT: /\bINTO\s+([^\s,;()]+)/i,
  REPLACE: /\bINTO\s+([^\s,;()]+)/i,
  UPDATE: /^UPDATE\s+(?:ONLY\s+)?([^\s,;()]+)/i,
  WITH: /\b(?:FROM|INTO|UPDATE)\s+([^\s,;()]+)/i,
};

/** "SELECT users", "INSERT bookings", "BEGIN": the first keyword and the main table. */
export function sqlName(sql: string): string {
  const text = sql.trim().replace(/^\(+/, "");
  const keyword = (/^[A-Za-z]+/.exec(text)?.[0] ?? "").toUpperCase();
  if (!keyword) return "SQL";
  const m = TABLE_AFTER[keyword]?.exec(text);
  if (!m) return keyword;
  const table = m[1]!.replace(/["`[\]]/g, "");
  return table ? `${keyword} ${table}` : keyword;
}
