import { useMemo, useState } from "react";
import { formatCount, formatMs, formatPercent } from "../../lib/format";
import { serviceColors, serviceLabel, type MapEdge, type MapNode, type ServiceMap } from "../../lib/traces";
import { KIND_COLOR } from "./Waterfall";

const NODE_W = 204;
const NODE_H = 48;
const COL_GAP = 132;
const ROW_GAP = 22;
const PAD = 16;

const KIND_NAME: Record<string, string> = { db: "database", http: "HTTP host", cache: "cache", queue: "queue" };

interface Placed extends MapNode {
  x: number;
  y: number;
}

/**
 * Columns from left to right: services nothing traced calls, then the
 * services they call, and so on; the databases, hosts and queues they use
 * last. A cycle keeps each service where it was first reached.
 */
function layout(map: ServiceMap): { nodes: Placed[]; width: number; height: number } {
  const services = map.nodes.filter((n) => n.kind === "service");
  const deps = map.nodes.filter((n) => n.kind !== "service");
  const svcEdges = map.edges.filter((e) => e.kind === "service");
  const incoming = new Set(svcEdges.map((e) => e.target));
  const depth = new Map<string, number>();
  let frontier = services.filter((n) => !incoming.has(n.id)).map((n) => n.id);
  if (frontier.length === 0 && services.length > 0) frontier = [services[0].id];
  let d = 0;
  while (frontier.length > 0) {
    const next: string[] = [];
    for (const id of frontier) {
      if (depth.has(id)) continue;
      depth.set(id, d);
      for (const e of svcEdges) if (e.source === id && !depth.has(e.target)) next.push(e.target);
    }
    frontier = next;
    d++;
  }
  for (const n of services) if (!depth.has(n.id)) depth.set(n.id, 0);
  const lastCol = services.length ? Math.max(...services.map((n) => depth.get(n.id) ?? 0)) + 1 : 0;
  for (const n of deps) depth.set(n.id, lastCol);

  const cols = new Map<number, MapNode[]>();
  for (const n of [...services, ...deps]) {
    const c = depth.get(n.id) ?? 0;
    cols.set(c, [...(cols.get(c) ?? []), n]);
  }
  const placed: Placed[] = [];
  let height = 0;
  for (const [c, list] of cols) {
    list.sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name));
    list.forEach((n, i) => placed.push({ ...n, x: PAD + c * (NODE_W + COL_GAP), y: PAD + i * (NODE_H + ROW_GAP) }));
    height = Math.max(height, PAD * 2 + list.length * (NODE_H + ROW_GAP) - ROW_GAP);
  }
  const width = PAD * 2 + (Math.max(0, ...cols.keys()) + 1) * (NODE_W + COL_GAP) - COL_GAP;
  return { nodes: placed, width, height };
}

function edgeColor(e: MapEdge): string {
  if (e.error_rate >= 5) return "var(--critical)";
  if (e.error_rate > 0) return "var(--warning)";
  return "var(--border-strong)";
}

/** Calls per minute, or per hour when fewer than one a minute. */
function rate(perMin: number): string {
  if (perMin >= 1) return `${formatCount(perMin)}/min`;
  const perHour = Math.round(perMin * 60);
  return perHour >= 1 ? `${formatCount(perHour)}/h` : "<1/h";
}

export function edgeSummary(e: MapEdge): string {
  return `${rate(e.per_min)} · ${formatPercent(e.error_rate)} failed${e.p95 !== null ? ` · p95 ${formatMs(e.p95)}` : ""}`;
}

/**
 * Services and what they call, as boxes and arrows. Arrows are as thick as
 * the calls are frequent and turn amber or red with failures; click one to
 * list its traces.
 */
export function ServiceMapGraph({ map, onEdge, onNode }: { map: ServiceMap; onEdge: (e: MapEdge) => void; onNode: (n: MapNode) => void }) {
  const { nodes, width, height } = useMemo(() => layout(map), [map]);
  const at = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const colors = useMemo(() => serviceColors(map.nodes.filter((n) => n.kind === "service").map((n) => n.name)), [map]);
  const maxCalls = Math.max(1, ...map.edges.map((e) => e.calls));
  const [hover, setHover] = useState<string | null>(null);

  return (
    <div className="smap-scroll">
      <svg className="smap" width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label="Service map">
        <defs>
          <marker id="smap-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="9" markerHeight="9" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--text-3)" />
          </marker>
        </defs>
        {map.edges.map((e) => {
          const a = at.get(e.source);
          const b = at.get(e.target);
          if (!a || !b) return null;
          const key = `${e.source}>${e.target}`;
          const x1 = a.x + NODE_W;
          const y1 = a.y + NODE_H / 2;
          const back = b.x <= a.x;
          const x2 = back ? b.x + NODE_W / 2 : b.x;
          const y2 = back ? b.y + (b.y > a.y ? 0 : NODE_H) : b.y + NODE_H / 2;
          const bend = back ? 60 : Math.max(40, (x2 - x1) / 2);
          const path = back ? `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2} ${y2 + (b.y > a.y ? -bend : bend)}, ${x2} ${y2}` : `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`;
          const w = 1.5 + (4 * e.calls) / maxCalls;
          const mx = (x1 + x2) / 2;
          const my = (y1 + y2) / 2;
          const on = hover === key;
          return (
            <g key={key} className={`smap-edge ${on ? "on" : ""}`} onMouseEnter={() => setHover(key)} onMouseLeave={() => setHover(null)} onClick={() => onEdge(e)} tabIndex={0} role="button" aria-label={`${a.name} to ${b.name}: ${edgeSummary(e)}`} onKeyDown={(ev) => ev.key === "Enter" && onEdge(e)}>
              <path d={path} className="smap-hit" />
              <path d={path} fill="none" stroke={edgeColor(e)} strokeWidth={on ? w + 1 : w} markerEnd="url(#smap-arrow)" />
              {!back && (
                <text x={mx} y={my - 6} textAnchor="middle" className="smap-edge-label">
                  {rate(e.per_min)}{e.error_rate > 0 ? ` · ${formatPercent(e.error_rate)}` : ""}
                </text>
              )}
              <title>{`${serviceLabel(a.name)} → ${b.name}\n${edgeSummary(e)}\n${formatCount(e.calls)} calls`}</title>
            </g>
          );
        })}
        {nodes.map((n) => {
          const svc = n.kind === "service";
          const color = svc ? (colors.get(n.name) ?? "var(--series-1)") : (KIND_COLOR[n.kind] ?? "var(--text-3)");
          const failed = svc && n.requests > 0 ? (100 * n.errors) / n.requests : 0;
          return (
            <g key={n.id} className="smap-node" transform={`translate(${n.x}, ${n.y})`} onClick={() => onNode(n)} tabIndex={0} role="button" aria-label={`${svc ? serviceLabel(n.name) : n.name}`} onKeyDown={(ev) => ev.key === "Enter" && onNode(n)}>
              <rect width={NODE_W} height={NODE_H} rx={6} className={`smap-box ${svc ? "" : "dep"}`} />
              <circle cx={16} cy={16} r={5} fill={color} />
              <text x={27} y={20} className="smap-name">
                {truncate(svc ? serviceLabel(n.name) : n.name, 21)}
              </text>
              <text x={27} y={37} className={`smap-sub ${failed >= 5 ? "bad" : ""}`}>
                {svc
                  ? n.requests > 0
                    ? `${formatCount(n.requests)} req · ${formatPercent(failed)}${n.p95 !== null ? ` · p95 ${formatMs(n.p95)}` : ""}`
                    : "no requests"
                  : (KIND_NAME[n.kind] ?? n.kind)}
              </text>
              <title>{svc ? serviceLabel(n.name) : `${n.name} (${KIND_NAME[n.kind] ?? n.kind})`}</title>
            </g>
          );
        })}
      </svg>
    </div>
  );
}

function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}
