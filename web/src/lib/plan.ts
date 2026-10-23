// PostgreSQL's text EXPLAIN output as a tree. Each step is a line with a
// cost; the lines under it (Filter:, Index Cond:, Sort Key:) are its details,
// and lines like "CTE words" or "SubPlan 1" label the step that follows.

export interface PlanNode {
  op: string;
  /** The relation or index the step reads, when it names one. */
  on?: string;
  startup: number;
  total: number;
  rows: number;
  width: number;
  details: string[];
  /** "CTE words", "InitPlan 1": what this step computes for its parent. */
  label?: string;
  children: PlanNode[];
  depth: number;
}

const NODE = /^(\s*)(?:->\s+)?(.+?)\s+\(cost=([\d.]+)\.\.([\d.]+) rows=(\d+) width=(\d+)\)\s*$/;
const LABEL = /^\s*(CTE \S+|InitPlan \d+.*|SubPlan \d+.*)$/;

/** The plan's root, or null when it is not a PostgreSQL text plan. */
export function parsePlan(text: string): PlanNode | null {
  let root: PlanNode | null = null;
  const stack: { indent: number; node: PlanNode }[] = [];
  let label: string | undefined;
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim() || line.trimStart().startsWith("--")) continue;
    const m = NODE.exec(line);
    if (m) {
      const indent = m[1].length;
      const [op, on] = splitOp(m[2]);
      const node: PlanNode = { op, on, startup: +m[3], total: +m[4], rows: +m[5], width: +m[6], details: [], label, children: [], depth: 0 };
      label = undefined;
      while (stack.length && stack[stack.length - 1].indent >= indent) stack.pop();
      if (!stack.length) {
        if (root) return root; // a second statement; keep the first
        root = node;
      } else {
        const parent = stack[stack.length - 1].node;
        node.depth = parent.depth + 1;
        parent.children.push(node);
      }
      stack.push({ indent, node });
      continue;
    }
    const l = LABEL.exec(line);
    if (l) {
      label = l[1];
      continue;
    }
    if (stack.length) stack[stack.length - 1].node.details.push(line.trim());
  }
  return root;
}

/** "Index Scan using users_pkey on users" → op and what it reads. */
function splitOp(s: string): [string, string | undefined] {
  const m = /^(.*?)\s+(?:using\s+(\S+)\s+)?on\s+(\S+(?:\s+\S+)?)$/.exec(s);
  if (m && !/\bJoin$/.test(m[1])) return [m[1] + (m[2] ? ` using ${m[2]}` : ""), m[3]];
  const scan = /^(Function Scan|CTE Scan|Subquery Scan|Values Scan|WorkTable Scan)\s+on\s+(.+)$/.exec(s);
  if (scan) return [scan[1], scan[2]];
  return [s, undefined];
}

export function walkPlan(n: PlanNode, f: (n: PlanNode) => void) {
  f(n);
  n.children.forEach((c) => walkPlan(c, f));
}

/** A step's own cost: its total less what its inputs cost. */
export function selfCost(n: PlanNode): number {
  return Math.max(0, n.total - n.children.reduce((s, c) => s + c.total, 0));
}

/** Steps that usually explain a slow plan. */
export function stepTone(n: PlanNode): "warn" | "" {
  if (/^Seq Scan/.test(n.op) && n.rows >= 1000) return "warn";
  if (/^Sort/.test(n.op) && n.rows >= 10000) return "warn";
  if (/^Nested Loop/.test(n.op) && n.rows >= 10000) return "warn";
  return "";
}
