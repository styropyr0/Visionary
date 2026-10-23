// Laying out stored statements for reading. SDKs store a statement on one
// line with its values replaced by ? (sanitized), so the dashboard rebuilds
// the shape: each clause on its own line, subqueries and CTE bodies indented,
// select lists and conditions one per line. Only whitespace changes; the
// tokens are the stored ones, so copying the formatted text runs the same.

export type SqlTokenKind = "kw" | "fn" | "ident" | "quoted" | "str" | "num" | "param" | "comment" | "op" | "punct" | "word";

export interface SqlToken {
  kind: SqlTokenKind;
  text: string;
  /** Whitespace stood before it in the stored text. */
  space: boolean;
}

export interface SqlLine {
  indent: number;
  tokens: SqlToken[];
}

const KEYWORDS = new Set(
  (
    "ADD ALL ALTER AND ANY ARRAY AS ASC BETWEEN BY CASCADE CASE CAST COLLATE CONFLICT CREATE CROSS CURRENT_DATE " +
    "CURRENT_TIMESTAMP DEFAULT DELETE DESC DISTINCT DO DROP ELSE END EXCEPT EXISTS FALSE FETCH FILTER FIRST FOR FROM " +
    "FULL GROUP HAVING ILIKE IN INNER INSERT INTERSECT INTERVAL INTO IS JOIN LATERAL LEFT LIKE LIMIT LOCKED MATERIALIZED " +
    "NATURAL NEXT NOT NOTHING NULL NULLS OFFSET ON ONLY OR ORDER ORDINALITY OUTER OVER PARTITION RECURSIVE RETURNING " +
    "RIGHT ROW ROWS SELECT SET SHARE SIMILAR SKIP SOME TABLE THEN TRUE UNION UNNEST UPDATE USING VALUES WHEN WHERE " +
    "WINDOW WITH WITHIN NOWAIT DATE TIMESTAMP TIMESTAMPTZ TIME"
  ).split(" "),
);

/** Clause keywords that start a line at the statement's own depth. */
const CLAUSES = new Set(["SELECT", "FROM", "WHERE", "GROUP", "ORDER", "HAVING", "LIMIT", "OFFSET", "UNION", "INTERSECT", "EXCEPT", "VALUES", "SET", "RETURNING", "INSERT", "UPDATE", "DELETE", "WINDOW", "FETCH", "ON CONFLICT"]);
const JOIN_WORDS = new Set(["LEFT", "RIGHT", "INNER", "FULL", "CROSS", "NATURAL", "OUTER", "LATERAL"]);

export function tokenizeSql(q: string): SqlToken[] {
  const out: SqlToken[] = [];
  let i = 0;
  let space = false;
  const push = (kind: SqlTokenKind, text: string) => {
    out.push({ kind, text, space });
    space = false;
  };
  while (i < q.length) {
    const c = q[i];
    const rest = q.slice(i);
    let m: RegExpMatchArray | null;
    if (/\s/.test(c)) {
      space = true;
      i++;
    } else if (rest.startsWith("--")) {
      const end = q.indexOf("\n", i);
      const j = end < 0 ? q.length : end;
      push("comment", q.slice(i, j));
      i = j;
    } else if (rest.startsWith("/*")) {
      const end = q.indexOf("*/", i + 2);
      const j = end < 0 ? q.length : end + 2;
      push("comment", q.slice(i, j));
      i = j;
    } else if (c === "'") {
      let j = i + 1;
      while (j < q.length && !(q[j] === "'" && q[j + 1] !== "'")) j += q[j] === "'" ? 2 : 1;
      push("str", q.slice(i, j + 1));
      i = j + 1;
    } else if (c === '"' || c === "`") {
      const end = q.indexOf(c, i + 1);
      const j = end < 0 ? q.length : end + 1;
      push("quoted", q.slice(i, j));
      i = j;
    } else if ((m = rest.match(/^(\?|\$\d+|:[A-Za-z_]\w*|%s|%\(\w+\)s)/)) && !(c === ":" && q[i - 1] === ":")) {
      push("param", m[0]);
      i += m[0].length;
    } else if ((m = rest.match(/^\d+(\.\d+)?([eE][+-]?\d+)?/))) {
      push("num", m[0]);
      i += m[0].length;
    } else if ((m = rest.match(/^[A-Za-z_][\w$]*/))) {
      const word = m[0];
      const next = q.slice(i + word.length).match(/^\s*\(/);
      const upper = word.toUpperCase();
      push(KEYWORDS.has(upper) ? "kw" : next ? "fn" : "ident", word);
      i += word.length;
    } else if ((m = rest.match(/^(::|<>|<=|>=|!=|\|\||->>|->|#>>|#>|@>|<@|&&|[-+*/%=<>!~^|&#@])/))) {
      push("op", m[0]);
      i += m[0].length;
    } else {
      push("punct", c);
      i++;
    }
  }
  return out;
}

interface Frame {
  /** Indent of the clauses in this block. */
  base: number;
  /** The paren opening it starts a subquery (block) or is inline (a call). */
  block: boolean;
  clause: string;
  caseDepth: number;
  between: boolean;
}

const upper = (t: SqlToken | undefined) => (t && t.kind === "kw" ? t.text.toUpperCase() : "");

/** The statement laid out in lines. Short statements stay on one line. */
export function formatSql(q: string): SqlLine[] {
  const toks = tokenizeSql(q);
  if (q.length <= 80) return [{ indent: 0, tokens: toks }];
  const lines: SqlLine[] = [];
  let line: SqlLine = { indent: 0, tokens: [] };
  const stack: Frame[] = [{ base: 0, block: true, clause: "", caseDepth: 0, between: false }];
  const newLine = (indent: number) => {
    if (line.tokens.length) lines.push(line);
    line = { indent, tokens: [] };
  };
  const add = (t: SqlToken) => line.tokens.push(line.tokens.length ? t : { ...t, space: false });
  const blockFrame = () => {
    for (let k = stack.length - 1; k >= 0; k--) if (stack[k].block) return { f: stack[k], inline: k < stack.length - 1 };
    return { f: stack[0], inline: false };
  };
  const nextSignificant = (i: number) => {
    for (let j = i + 1; j < toks.length; j++) if (toks[j].kind !== "comment") return toks[j];
    return undefined;
  };

  for (let i = 0; i < toks.length; i++) {
    const t = toks[i];
    const w = upper(t);
    const top = stack[stack.length - 1];
    const { f, inline } = blockFrame();
    const atBlock = !inline && top === f;

    if (t.kind === "comment") {
      if (t.text.startsWith("--")) {
        add(t);
        newLine(f.base + 1);
      } else add(t);
      continue;
    }
    if (t.text === "(") {
      const n = nextSignificant(i);
      const sub = upper(n) === "SELECT" || upper(n) === "WITH" || upper(n) === "VALUES";
      add(t);
      if (sub) {
        stack.push({ base: line.indent + 1, block: true, clause: "", caseDepth: 0, between: false });
        newLine(line.indent + 1);
      } else stack.push({ base: f.base, block: false, clause: "", caseDepth: 0, between: false });
      continue;
    }
    if (t.text === ")") {
      if (stack.length > 1) {
        const closed = stack.pop()!;
        if (closed.block) newLine(closed.base - 1);
      }
      add(t);
      continue;
    }
    if (w === "CASE") top.caseDepth++;
    if (w === "END" && top.caseDepth > 0) top.caseDepth--;
    if (w === "BETWEEN") top.between = true;

    if (atBlock && f.caseDepth === 0) {
      const prev = upper(toks[i - 1]);
      const clause =
        (CLAUSES.has(w) && !(w === "SET" && prev === "DO") && !((w === "GROUP" || w === "ORDER") && upper(toks[i + 1]) !== "BY")) ||
        (w === "WITH" && line.tokens.length === 0 && lines.length === 0) ||
        (w === "WITH" && f.clause === "" && line.tokens.length === 0);
      const join = w === "JOIN" ? !JOIN_WORDS.has(prev) : JOIN_WORDS.has(w) && !JOIN_WORDS.has(prev) && toks.slice(i + 1, i + 4).some((x) => upper(x) === "JOIN");
      const unionAll = (w === "ALL" || w === "DISTINCT") && (prev === "UNION" || prev === "INTERSECT" || prev === "EXCEPT");
      if ((clause || join) && !unionAll && !(w === "SELECT" && prev === "UNION")) {
        // A set operator sits on its own line between the two selects.
        if (prev !== "UNION" && prev !== "ALL" && prev !== "INTERSECT" && prev !== "EXCEPT") newLine(f.base);
        f.clause = join ? "JOIN" : w === "WITH" ? "WITH" : w;
        add(t);
        if (w === "UNION" || w === "INTERSECT" || w === "EXCEPT") {
          if (upper(toks[i + 1]) === "ALL" || upper(toks[i + 1]) === "DISTINCT") add(toks[++i]);
          newLine(f.base);
        }
        continue;
      }
      if ((w === "AND" || w === "OR") && (f.clause === "WHERE" || f.clause === "HAVING" || f.clause === "JOIN")) {
        if (w === "AND" && f.between) {
          f.between = false;
        } else {
          newLine(f.base + 1);
          add(t);
          continue;
        }
      }
      if (t.text === ",") {
        add(t);
        if (f.clause === "SELECT" || f.clause === "SET" || f.clause === "RETURNING") newLine(f.base + 1);
        else if (f.clause === "WITH") newLine(f.base);
        continue;
      }
    } else if (w === "AND" && top.between) top.between = false;
    add(t);
  }
  newLine(0);
  return lines;
}

/** The lines back as text, two spaces per level. */
export function sqlLinesText(lines: SqlLine[]): string {
  return lines
    .map((l) => {
      let s = "  ".repeat(l.indent);
      l.tokens.forEach((t, i) => {
        const prev = l.tokens[i - 1];
        if (i > 0 && joins(prev, t)) s += " ";
        s += t.text;
      });
      return s;
    })
    .join("\n");
}

/** Whether a space goes between two tokens on one line. */
export function joins(prev: SqlToken, t: SqlToken): boolean {
  if (t.text === "," || t.text === ")" || t.text === "." || t.text === "::" || prev.text === "." || prev.text === "::" || prev.text === "(") return false;
  return t.space || prev.text === ",";
}

/** How many values the SDK removed (? placeholders), and the app's own ($n). */
export function countParams(q: string): { removed: number; app: number } {
  let removed = 0;
  const app = new Set<string>();
  for (const t of tokenizeSql(q)) {
    if (t.kind !== "param") continue;
    if (t.text === "?") removed++;
    else app.add(t.text);
  }
  return { removed, app: app.size };
}
