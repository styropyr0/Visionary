import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type FocusEvent as ReactFocusEvent, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import { Maximize2, Minus, Plus, RotateCcw } from "lucide-react";
import { formatCount, formatMs, formatPercent } from "../../lib/format";
import { serviceColors, serviceLabel, type MapEdge, type MapNode, type ServiceMap } from "../../lib/traces";
import { TIP_DELAY, TipBubble } from "../InfoTip";
import { KIND_COLOR } from "./Waterfall";

const NODE_W = 204;
const NODE_H = 48;
const COL_GAP = 132;
const ROW_GAP = 22;
const PAD = 16;
const MIN_ZOOM = 0.2;
const MAX_ZOOM = 3;
const FIT_MARGIN = 32;
// Pointer travel below this is a click, not a drag.
const CLICK_SLOP = 4;

const KIND_NAME: Record<string, string> = { db: "database", http: "HTTP host", cache: "cache", queue: "queue" };

interface Placed extends MapNode {
  x: number;
  y: number;
}

interface View {
  x: number;
  y: number;
  k: number;
}

type Drag =
  | { mode: "pan"; startX: number; startY: number; view: View; moved: boolean }
  | { mode: "node"; id: string; startX: number; startY: number; nodeX: number; nodeY: number; moved: boolean };

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

const clampZoom = (k: number) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, k));

function bounds(nodes: Placed[]): { x: number; y: number; w: number; h: number } {
  if (nodes.length === 0) return { x: 0, y: 0, w: 1, h: 1 };
  const xs = nodes.map((n) => n.x);
  const ys = nodes.map((n) => n.y);
  const x = Math.min(...xs);
  const y = Math.min(...ys);
  return { x, y, w: Math.max(...xs) + NODE_W - x, h: Math.max(...ys) + NODE_H - y };
}

/** Whole map in view, never enlarged past 100%. */
function fitView(nodes: Placed[], width: number, height: number): View {
  const b = bounds(nodes);
  const k = clampZoom(Math.min(1, (width - FIT_MARGIN * 2) / b.w, (height - FIT_MARGIN * 2) / b.h));
  return { k, x: (width - b.w * k) / 2 - b.x * k, y: (height - b.h * k) / 2 - b.y * k };
}

/**
 * Services and what they call, as boxes and arrows on a canvas: drag to pan,
 * scroll or pinch to zoom, drag a box to move it. Arrows are as thick as the
 * calls are frequent and turn amber or red with failures; click one to list
 * its traces.
 */
export function ServiceMapGraph({ map, onEdge, onNode }: { map: ServiceMap; onEdge: (e: MapEdge) => void; onNode: (n: MapNode) => void }) {
  const laid = useMemo(() => layout(map), [map]);
  const [moved, setMoved] = useState<Map<string, { x: number; y: number }>>(new Map());
  const nodes = useMemo(() => laid.nodes.map((n) => ({ ...n, ...moved.get(n.id) })), [laid, moved]);
  const at = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const colors = useMemo(() => serviceColors(map.nodes.filter((n) => n.kind === "service").map((n) => n.name)), [map]);
  const maxCalls = Math.max(1, ...map.edges.map((e) => e.calls));
  const [hover, setHover] = useState<string | null>(null);

  const box = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [view, setView] = useState<View>({ x: 0, y: 0, k: 1 });
  const viewRef = useRef(view);
  viewRef.current = view;
  const drag = useRef<Drag | null>(null);
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const pinch = useRef<{ dist: number; view: View } | null>(null);
  // A drag that ends over a box or arrow must not count as a click on it.
  const suppressClick = useRef(false);

  const tipId = useId();
  const tipTimer = useRef<number>();
  const [tip, setTip] = useState<{ el: Element; body: ReactNode } | null>(null);
  const hideTip = useCallback(() => {
    window.clearTimeout(tipTimer.current);
    setTip(null);
  }, []);
  const showTip = (el: Element, body: ReactNode) => {
    window.clearTimeout(tipTimer.current);
    if (pointers.current.size > 0) return;
    tipTimer.current = window.setTimeout(() => setTip({ el, body }), TIP_DELAY);
  };
  const tipProps = (body: () => ReactNode, hoverKey?: string) => ({
    "aria-describedby": tip ? tipId : undefined,
    onMouseEnter: (ev: { currentTarget: Element }) => {
      if (hoverKey) setHover(hoverKey);
      showTip(ev.currentTarget, body());
    },
    onMouseLeave: () => {
      if (hoverKey) setHover(null);
      hideTip();
    },
    onFocus: (ev: ReactFocusEvent<SVGGElement>) => ev.currentTarget.matches(":focus-visible") && showTip(ev.currentTarget, body()),
    onBlur: hideTip,
  });
  useEffect(() => () => window.clearTimeout(tipTimer.current), []);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setSize({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    setSize({ w: el.clientWidth, h: el.clientHeight });
    return () => ro.disconnect();
  }, []);

  const fit = useCallback(() => {
    if (size.w > 0 && size.h > 0) setView(fitView(nodes, size.w, size.h));
  }, [nodes, size]);

  // A new map, or the canvas getting its first size, starts fitted.
  const fitted = useRef<ServiceMap | null>(null);
  useEffect(() => {
    if (size.w === 0 || fitted.current === map) return;
    fitted.current = map;
    setMoved(new Map());
    setView(fitView(laid.nodes, size.w, size.h));
  }, [map, laid, size]);

  const zoomAt = useCallback((factor: number, cx: number, cy: number) => {
    setView((v) => {
      const k = clampZoom(v.k * factor);
      const f = k / v.k;
      return { k, x: cx - (cx - v.x) * f, y: cy - (cy - v.y) * f };
    });
  }, []);

  const zoomCenter = (factor: number) => zoomAt(factor, size.w / 2, size.h / 2);

  // Wheel needs a non-passive listener so the page doesn't scroll instead.
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      hideTip();
      const r = el.getBoundingClientRect();
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? r.height : 1;
      zoomAt(Math.exp((-e.deltaY * unit) / (e.ctrlKey ? 100 : 600)), e.clientX - r.left, e.clientY - r.top);
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoomAt, hideTip]);

  const local = (e: { clientX: number; clientY: number }) => {
    const r = box.current!.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    hideTip();
    box.current?.setPointerCapture(e.pointerId);
    pointers.current.set(e.pointerId, local(e));
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()];
      pinch.current = { dist: Math.hypot(a.x - b.x, a.y - b.y), view: viewRef.current };
      drag.current = null;
      return;
    }
    const nodeEl = (e.target as Element).closest<SVGGElement>("[data-node]");
    const id = nodeEl?.dataset.node;
    const n = id ? at.get(id) : undefined;
    suppressClick.current = false;
    drag.current = n
      ? { mode: "node", id: n.id, startX: e.clientX, startY: e.clientY, nodeX: n.x, nodeY: n.y, moved: false }
      : { mode: "pan", startX: e.clientX, startY: e.clientY, view: viewRef.current, moved: false };
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!pointers.current.has(e.pointerId)) return;
    pointers.current.set(e.pointerId, local(e));
    if (pinch.current && pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()];
      const start = pinch.current.view;
      const k = clampZoom((start.k * Math.hypot(a.x - b.x, a.y - b.y)) / Math.max(1, pinch.current.dist));
      const cx = (a.x + b.x) / 2;
      const cy = (a.y + b.y) / 2;
      setView({ k, x: cx - (cx - start.x) * (k / start.k), y: cy - (cy - start.y) * (k / start.k) });
      suppressClick.current = true;
      return;
    }
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.startX;
    const dy = e.clientY - d.startY;
    if (!d.moved && Math.hypot(dx, dy) < CLICK_SLOP) return;
    d.moved = true;
    suppressClick.current = true;
    if (d.mode === "pan") {
      setView({ ...d.view, x: d.view.x + dx, y: d.view.y + dy });
    } else {
      const k = viewRef.current.k;
      setMoved((m) => new Map(m).set(d.id, { x: d.nodeX + dx / k, y: d.nodeY + dy / k }));
    }
  };

  const onPointerUp = (e: ReactPointerEvent<HTMLDivElement>) => {
    pointers.current.delete(e.pointerId);
    if (pointers.current.size < 2) pinch.current = null;
    if (pointers.current.size === 0) drag.current = null;
  };

  const clickGuard = (fn: () => void) => () => {
    if (suppressClick.current) {
      suppressClick.current = false;
      return;
    }
    fn();
  };

  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Escape") hideTip();
    if (e.target !== box.current) return;
    const step = 60;
    const keys: Record<string, () => void> = {
      "+": () => zoomCenter(1.2),
      "=": () => zoomCenter(1.2),
      "-": () => zoomCenter(1 / 1.2),
      "0": fit,
      ArrowLeft: () => setView((v) => ({ ...v, x: v.x + step })),
      ArrowRight: () => setView((v) => ({ ...v, x: v.x - step })),
      ArrowUp: () => setView((v) => ({ ...v, y: v.y + step })),
      ArrowDown: () => setView((v) => ({ ...v, y: v.y - step })),
    };
    const fn = keys[e.key];
    if (fn) {
      e.preventDefault();
      fn();
    }
  };

  const panning = drag.current?.mode === "pan" && drag.current.moved;

  return (
    <div
      ref={box}
      className={`smap-canvas ${panning ? "panning" : ""}`}
      tabIndex={0}
      role="application"
      aria-label="Service map. Drag to pan, scroll to zoom, plus and minus keys zoom, 0 fits the map."
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onDoubleClick={(e) => {
        if ((e.target as Element).closest("[data-node], .smap-edge")) return;
        const p = local(e);
        zoomAt(1.5, p.x, p.y);
      }}
      onKeyDown={onKeyDown}
      style={{ backgroundSize: `${20 * view.k}px ${20 * view.k}px`, backgroundPosition: `${view.x}px ${view.y}px` }}
    >
      <svg className="smap" width="100%" height="100%" role="img" aria-label="Service map">
        <defs>
          <marker id="smap-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="9" markerHeight="9" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--text-3)" />
          </marker>
        </defs>
        <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
          {map.edges.map((e) => {
            const a = at.get(e.source);
            const b = at.get(e.target);
            if (!a || !b) return null;
            const key = `${e.source}>${e.target}`;
            const x1 = a.x + NODE_W;
            const y1 = a.y + NODE_H / 2;
            const back = b.x <= a.x + NODE_W / 2;
            const x2 = back ? b.x + NODE_W / 2 : b.x;
            const y2 = back ? b.y + (b.y > a.y ? 0 : NODE_H) : b.y + NODE_H / 2;
            const bend = back ? 60 : Math.max(40, (x2 - x1) / 2);
            const path = back ? `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2} ${y2 + (b.y > a.y ? -bend : bend)}, ${x2} ${y2}` : `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`;
            const w = 1.5 + (4 * e.calls) / maxCalls;
            const mx = (x1 + x2) / 2;
            const my = (y1 + y2) / 2;
            const on = hover === key;
            return (
              <g
                key={key}
                className={`smap-edge ${on ? "on" : ""}`}
                {...tipProps(() => (
                  <>
                    <span className="tip-title">
                      {serviceLabel(a.name)} → {b.kind === "service" ? serviceLabel(b.name) : b.name}
                    </span>
                    <div>{edgeSummary(e)}</div>
                    <div>{formatCount(e.calls)} calls</div>
                  </>
                ), key)}
                onClick={clickGuard(() => onEdge(e))}
                tabIndex={0}
                role="button"
                aria-label={`${a.name} to ${b.name}: ${edgeSummary(e)}`}
                onKeyDown={(ev) => ev.key === "Enter" && onEdge(e)}
              >
                <path d={path} className="smap-hit" />
                <path d={path} fill="none" stroke={edgeColor(e)} strokeWidth={on ? w + 1 : w} markerEnd="url(#smap-arrow)" />
                {!back && (
                  <text x={mx} y={my - 6} textAnchor="middle" className="smap-edge-label">
                    {rate(e.per_min)}
                    {e.error_rate > 0 ? ` · ${formatPercent(e.error_rate)}` : ""}
                  </text>
                )}
              </g>
            );
          })}
          {nodes.map((n) => {
            const svc = n.kind === "service";
            const color = svc ? (colors.get(n.name) ?? "var(--series-1)") : (KIND_COLOR[n.kind] ?? "var(--text-3)");
            const failed = svc && n.requests > 0 ? (100 * n.errors) / n.requests : 0;
            return (
              <g
                key={n.id}
                data-node={n.id}
                className="smap-node"
                transform={`translate(${n.x}, ${n.y})`}
                {...tipProps(() => (
                  <>
                    <span className="tip-title">{svc ? serviceLabel(n.name) : n.name}</span>
                    {svc ? (
                      n.requests > 0 ? (
                        <>
                          <div>{formatCount(n.requests)} requests · {formatPercent(failed)} failed</div>
                          {n.p95 !== null && <div>p95 {formatMs(n.p95)}</div>}
                        </>
                      ) : (
                        <div>No requests in this range</div>
                      )
                    ) : (
                      <div>{KIND_NAME[n.kind] ?? n.kind}</div>
                    )}
                    <div className="dim">Click to list its traces</div>
                  </>
                ))}
                onClick={clickGuard(() => onNode(n))}
                tabIndex={0}
                role="button"
                aria-label={`${svc ? serviceLabel(n.name) : n.name}`}
                onKeyDown={(ev) => ev.key === "Enter" && onNode(n)}
              >
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
              </g>
            );
          })}
        </g>
      </svg>
      <div className="smap-hint dim" aria-hidden>
        Drag to pan · scroll to zoom · drag a box to move it
      </div>
      <div className="smap-controls" onPointerDown={(e) => e.stopPropagation()} onDoubleClick={(e) => e.stopPropagation()}>
        <button type="button" className="icon-btn" title="Zoom in" aria-label="Zoom in" onClick={() => zoomCenter(1.25)}>
          <Plus size={15} />
        </button>
        <button type="button" className="btn ghost smap-zoom" title="Zoom to 100%" aria-label={`Zoom ${Math.round(view.k * 100)}%, set to 100%`} onClick={() => zoomCenter(1 / view.k)}>
          {Math.round(view.k * 100)}%
        </button>
        <button type="button" className="icon-btn" title="Zoom out" aria-label="Zoom out" onClick={() => zoomCenter(1 / 1.25)}>
          <Minus size={15} />
        </button>
        <button type="button" className="icon-btn" title="Fit the map" aria-label="Fit the map" onClick={fit}>
          <Maximize2 size={14} />
        </button>
        {moved.size > 0 && (
          <button
            type="button"
            className="icon-btn"
            title="Put the boxes back"
            aria-label="Put the boxes back"
            onClick={() => {
              setMoved(new Map());
              if (size.w > 0) setView(fitView(laid.nodes, size.w, size.h));
            }}
          >
            <RotateCcw size={14} />
          </button>
        )}
      </div>
      {tip && tip.el.isConnected && (
        <TipBubble anchor={tip.el} id={tipId} className="rich">
          {tip.body}
        </TipBubble>
      )}
    </div>
  );
}

function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}
