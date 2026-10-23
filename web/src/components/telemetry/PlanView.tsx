import { useMemo, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { formatCount } from "../../lib/format";
import { parsePlan, selfCost, stepTone, walkPlan, type PlanNode } from "../../lib/plan";
import { InfoTip } from "../InfoTip";
import { CopyButton } from "../Modal";

function cost(n: number): string {
  return n >= 1000 ? formatCount(Math.round(n)) : n >= 10 ? n.toFixed(0) : n.toFixed(2);
}

/**
 * A query plan as a tree of steps, each with the planner's row estimate and
 * a bar for the part of the cost it adds itself, so the expensive step
 * stands out. Plans that are not PostgreSQL text (MySQL, SQLite) show as
 * text.
 */
export function PlanView({ plan }: { plan: string }) {
  const root = useMemo(() => parsePlan(plan), [plan]);
  const [text, setText] = useState(false);

  const stats = useMemo(() => {
    if (!root) return null;
    let steps = 0;
    let heaviest: PlanNode = root;
    walkPlan(root, (n) => {
      steps++;
      if (selfCost(n) > selfCost(heaviest)) heaviest = n;
    });
    return { steps, heaviest, max: Math.max(...flatten(root).map(selfCost), 0.0001) };
  }, [root]);

  if (!root || !stats) return <pre className="code mono wf-sql qp-plan">{plan}</pre>;

  return (
    <div className="pv">
      <div className="pv-summary">
        <div className="pv-stat">
          <div className="pv-stat-label">
            Estimated cost{" "}
            <InfoTip text="In the planner's own units, not milliseconds. Useful to compare two versions of a statement, or the same one before and after an index." size={12} />
          </div>
          <div className="pv-stat-value">{cost(root.total)}</div>
        </div>
        <div className="pv-stat">
          <div className="pv-stat-label">Rows returned</div>
          <div className="pv-stat-value">~{formatCount(root.rows)}</div>
        </div>
        <div className="pv-stat">
          <div className="pv-stat-label">Steps</div>
          <div className="pv-stat-value">{stats.steps}</div>
        </div>
        {stats.steps > 1 && (
          <div className="pv-stat pv-stat-wide">
            <div className="pv-stat-label">Most of the cost</div>
            <div className="pv-stat-value pv-stat-op" title={opText(stats.heaviest)}>
              {opText(stats.heaviest)}
            </div>
          </div>
        )}
        <div className="pv-tools">
          <div className="segmented" role="radiogroup" aria-label="Plan view">
            <button type="button" role="radio" aria-checked={!text} className={!text ? "on" : ""} onClick={() => setText(false)}>
              Steps
            </button>
            <button type="button" role="radio" aria-checked={text} className={text ? "on" : ""} onClick={() => setText(true)}>
              Text
            </button>
          </div>
          <CopyButton text={plan} />
        </div>
      </div>

      {text ? (
        <pre className="code mono wf-sql qp-plan">{plan}</pre>
      ) : (
        <div className="pv-tree" role="tree" aria-label="Plan steps">
          <div className="pv-head" aria-hidden>
            <span>Step</span>
            <span className="num">Rows</span>
            <span className="num">Own cost</span>
          </div>
          <Step node={root} max={stats.max} heaviest={stats.heaviest} />
        </div>
      )}
    </div>
  );
}

function flatten(n: PlanNode): PlanNode[] {
  const out: PlanNode[] = [];
  walkPlan(n, (x) => out.push(x));
  return out;
}

function opText(n: PlanNode): string {
  return n.on ? `${n.op} on ${n.on}` : n.op;
}

function Step({ node, max, heaviest }: { node: PlanNode; max: number; heaviest: PlanNode }) {
  const own = selfCost(node);
  const tone = stepTone(node);
  return (
    <div className="pv-node" role="treeitem" aria-expanded={node.children.length ? true : undefined}>
      <div className={`pv-row ${node === heaviest ? "heaviest" : ""} ${node.label ? "labelled" : ""}`}>
        <div className="pv-op" style={{ paddingLeft: `${node.depth * 18}px` }}>
          {node.depth > 0 && <span className="pv-elbow" aria-hidden />}
          <div className="pv-op-text">
            {node.label && <span className="pv-label">{node.label}</span>}
            <span className="pv-name">
              {tone === "warn" && <AlertTriangle size={12} className="pv-warn" aria-label="Often slow" />}
              {node.op}
              {node.on && (
                <>
                  {" "}
                  <span className="dim">on</span> <span className="mono pv-rel">{node.on}</span>
                </>
              )}
            </span>
            {node.details.map((d, i) => (
              <span key={i} className="mono pv-detail">
                {d}
              </span>
            ))}
          </div>
        </div>
        <span className="num pv-rows">{formatCount(node.rows)}</span>
        <span className="num pv-cost" title={`Startup ${cost(node.startup)}, total ${cost(node.total)} including its inputs`}>
          {cost(own)}
          <span className="track" aria-hidden>
            <span className="fill" style={{ width: `${Math.max(2, (100 * own) / max)}%` }} />
          </span>
        </span>
      </div>
      {node.children.map((c, i) => (
        <Step key={i} node={c} max={max} heaviest={heaviest} />
      ))}
    </div>
  );
}
