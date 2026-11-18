import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type FocusEvent as ReactFocusEvent, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import { Bell, Cpu, Maximize2, Minus, Plus, Rocket, RotateCcw, Siren } from "lucide-react";
import { formatCount, formatMs, formatPercent } from "../../lib/format";
import { change, edgeKey, serviceLabel, type EdgePoint, type ErrorRole, type MapEdge, type MapHealth, type MapNode, type ServiceMap } from "../../lib/traces";
import { formatRelative } from "../../lib/time";
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
// Pointer travel below this is a click, not a drag
const CLICK_SLOP = 4;
const LAYOUT_KEY = "va.smap.layout";
// Machine use at or above this is pressure worth a badge
const PRESSURE = 85;

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

type Positions = Map<string, { x: number; y: number }>;

function loadLayout(): Positions {
  try {
    const v = JSON.parse(localStorage.getItem(LAYOUT_KEY) ?? "{}") as Record<string, { x: number; y: number }>;
    return new Map(Object.entries(v).filter(([, p]) => Number.isFinite(p?.x) && Number.isFinite(p?.y)));
  } catch {
    return new Map();
  }
}

function saveLayout(m: Positions) {
  try {
    if (m.size === 0) localStorage.removeItem(LAYOUT_KEY);
    else localStorage.setItem(LAYOUT_KEY, JSON.stringify(Object.fromEntries(m)));
  } catch {
    // Private windows and full storage only lose the remembered layout
  }
}

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

export function rate(perMin: number): string {
  if (perMin >= 1) return `${formatCount(perMin)}/min`;
  const perHour = Math.round(perMin * 60);
  return perHour >= 1 ? `${formatCount(perHour)}/h` : "<1/h";
}

export function edgeSummary(e: MapEdge): string {
  return `${rate(e.per_min)} · ${formatPercent(e.error_rate)} failed${e.p95 !== null ? ` · p95 ${formatMs(e.p95)}` : ""}`;
}

/** A change as "+40%", or "" when it is too small to mention. */
export function changeText(c: number | null, min = 0.1): string {
  if (c === null || Math.abs(c) < min) return "";
  const pct = Math.round(c * 100);
  return `${pct > 0 ? "+" : ""}${pct}%`;
}

/** How a box or arrow moved against the window before, for tooltips. */
function beforeLines(now: { rate: number; failed: number; p95: number | null }, before: { rate: number; failed: number; p95: number | null } | undefined, unit: string): ReactNode {
  if (!before) return <div className="dim">New in this range: none the window before</div>;
  const parts = [
    changeText(change(now.rate, before.rate)) && `${unit} ${changeText(change(now.rate, before.rate))}`,
    Math.abs(now.failed - before.failed) >= 0.5 && `failed ${formatPercent(before.failed)} → ${formatPercent(now.failed)}`,
    changeText(change(now.p95, before.p95)) && `p95 ${changeText(change(now.p95, before.p95))}`,
  ].filter(Boolean);
  return <div className="dim">{parts.length ? `Against before: ${parts.join(" · ")}` : "About the same as the window before"}</div>;
}

function Spark({ points }: { points: EdgePoint[] }) {
  if (points.length < 2) return null;
  const w = 160;
  const h = 32;
  const t0 = points[0][0];
  const t1 = points[points.length - 1][0];
  const hi = Math.max(1, ...points.map((p) => p[1]));
  const x = (t: number) => ((t - t0) / (t1 - t0 || 1)) * w;
  const y = (v: number) => h - 2 - (v / hi) * (h - 4);
  const line = points.map((p, i) => `${i ? "L" : "M"}${x(p[0]).toFixed(1)},${y(p[1]).toFixed(1)}`).join("");
  return (
    <svg className="smap-spark" viewBox={`0 0 ${w} ${h}`} width={w} height={h} aria-hidden>
      <path d={line} className="smap-spark-calls" />
      {points.map((p) =>
        p[2] > 0 ? <line key={p[0]} x1={x(p[0])} x2={x(p[0])} y1={h - 1} y2={y(p[2])} className="smap-spark-failed" /> : null,
      )}
    </svg>
  );
}

interface Badge {
  key: string;
  icon: typeof Rocket;
  className: string;
  text: string;
}

function badgesOf(h: MapHealth | undefined): Badge[] {
  if (!h) return [];
  const out: Badge[] = [];
  if (h.incidents.length) out.push({ key: "inc", icon: Siren, className: "bad", text: `Open incident: ${h.incidents.map((i) => i.title).join("; ")}` });
  const slo = h.alerts.filter((a) => a.slo);
  const other = h.alerts.filter((a) => !a.slo);
  if (slo.length) out.push({ key: "slo", icon: Bell, className: "bad", text: `SLO burning: ${slo.map((a) => a.name).join("; ")}` });
  if (other.length) out.push({ key: "alert", icon: Bell, className: "warn", text: `Firing: ${other.map((a) => a.name).join("; ")}` });
  const hot = [
    h.cpu !== null && h.cpu >= PRESSURE && `CPU ${formatPercent(h.cpu)}`,
    h.memory !== null && h.memory >= PRESSURE && `memory ${formatPercent(h.memory)}`,
    h.container_memory !== null && h.container_memory >= PRESSURE && `container memory ${formatPercent(h.container_memory)} of its limit`,
  ].filter(Boolean);
  if (hot.length) out.push({ key: "cpu", icon: Cpu, className: "warn", text: `Machine under pressure: ${hot.join(", ")}` });
  if (h.deploys.length) out.push({ key: "dep", icon: Rocket, className: "info", text: `Deployed ${h.deploys[0].version} ${formatRelative(h.deploys[0].at)}${h.deploys.length > 1 ? `, ${h.deploys.length} versions in the range` : ""}` });
  return out;
}

const ROLE_TEXT: Record<ErrorRole, string> = {
  origin: "Likely where failures start: its own calls do not fail",
  downstream: "Fails while calls it makes fail too: likely passing failures on",
};

// Roughly the cursor's height, so the tip opens below the pointer
const CURSOR_H = 18;

const pointerAnchor = (p: { x: number; y: number }) => ({ getBoundingClientRect: () => new DOMRect(p.x, p.y, 0, CURSOR_H) });

const clampZoom = (k: number) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, k));

function bounds(nodes: Placed[]): { x: number; y: number; w: number; h: number } {
  if (nodes.length === 0) return { x: 0, y: 0, w: 1, h: 1 };
  const xs = nodes.map((n) => n.x);
  const ys = nodes.map((n) => n.y);
  const x = Math.min(...xs);
  const y = Math.min(...ys);
  return { x, y, w: Math.max(...xs) + NODE_W - x, h: Math.max(...ys) + NODE_H - y };
}

function fitView(nodes: Placed[], width: number, height: number): View {
  const b = bounds(nodes);
  const k = clampZoom(Math.min(1, (width - FIT_MARGIN * 2) / b.w, (height - FIT_MARGIN * 2) / b.h));
  return { k, x: (width - b.w * k) / 2 - b.x * k, y: (height - b.h * k) / 2 - b.y * k };
}

interface Props {
  map: ServiceMap;
  onEdge: (e: MapEdge) => void;
  onNode: (n: MapNode) => void;
  roles?: Map<string, ErrorRole>;
  overlay?: Record<string, MapHealth>;
  series?: Record<string, EdgePoint[]>;
  focus?: string;
}

export function ServiceMapGraph({ map, onEdge, onNode, roles, overlay, series, focus }: Props) {
  const laid = useMemo(() => layout(map), [map]);
  const [moved, setMoved] = useState<Positions>(loadLayout);
  useEffect(() => saveLayout(moved), [moved]);
  const nodes = useMemo(() => laid.nodes.map((n) => ({ ...n, ...moved.get(n.id) })), [laid, moved]);
  const at = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const maxCalls = Math.max(1, ...map.edges.map((e) => e.calls));
  const [hover, setHover] = useState<string | null>(null);
  const prevNodes = useMemo(() => new Map((map.previous?.nodes ?? []).map((n) => [n.id, n])), [map.previous]);
  const prevEdges = useMemo(() => new Map((map.previous?.edges ?? []).map((e) => [edgeKey(e), e])), [map.previous]);
  const gone = useMemo(() => {
    const now = new Set(map.edges.map(edgeKey));
    return (map.previous?.edges ?? []).filter((e) => !now.has(edgeKey(e)));
  }, [map]);

  const box = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [view, setView] = useState<View>({ x: 0, y: 0, k: 1 });
  const viewRef = useRef(view);
  viewRef.current = view;
  const drag = useRef<Drag | null>(null);
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const pinch = useRef<{ dist: number; view: View } | null>(null);
  // A drag that ends over a box or arrow must not count as a click on it
  const suppressClick = useRef(false);

  const tipId = useId();
  const tipTimer = useRef<number>();
  // Tips follow the pointer, since an arrow's bounding box spans its whole curve
  const mouse = useRef({ x: 0, y: 0 });
  const [tip, setTip] = useState<{ el: Element; anchor: Pick<Element, "getBoundingClientRect">; follow: boolean; body: ReactNode } | null>(null);
  const hideTip = useCallback(() => {
    window.clearTimeout(tipTimer.current);
    setTip(null);
  }, []);
  const showTip = (el: Element, body: ReactNode, atPointer: boolean) => {
    window.clearTimeout(tipTimer.current);
    if (pointers.current.size > 0) return;
    tipTimer.current = window.setTimeout(
      () => setTip({ el, anchor: atPointer ? pointerAnchor(mouse.current) : el, follow: atPointer, body }),
      TIP_DELAY,
    );
  };
  const onMouseMove = (e: { clientX: number; clientY: number }) => {
    mouse.current = { x: e.clientX, y: e.clientY };
    setTip((t) => (t?.follow ? { ...t, anchor: pointerAnchor(mouse.current) } : t));
  };
  const tipProps = (body: () => ReactNode, hoverKey?: string) => ({
    "aria-describedby": tip ? tipId : undefined,
    onMouseEnter: (ev: { currentTarget: Element; clientX: number; clientY: number }) => {
      if (hoverKey) setHover(hoverKey);
      mouse.current = { x: ev.clientX, y: ev.clientY };
      showTip(ev.currentTarget, body(), true);
    },
    onMouseLeave: () => {
      if (hoverKey) setHover(null);
      hideTip();
    },
    onFocus: (ev: ReactFocusEvent<SVGGElement>) => ev.currentTarget.matches(":focus-visible") && showTip(ev.currentTarget, body(), false),
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

  // Refit when the boxes change, not on every refresh of the same map
  const shape = useMemo(() => map.nodes.map((n) => n.id).sort().join("\n"), [map]);
  const fitted = useRef<string | null>(null);
  useEffect(() => {
    if (size.w === 0 || fitted.current === shape) return;
    fitted.current = shape;
    setView(fitView(nodes, size.w, size.h));
  }, [shape, nodes, size]);

  const zoomAt = useCallback((factor: number, cx: number, cy: number) => {
    setView((v) => {
      const k = clampZoom(v.k * factor);
      const f = k / v.k;
      return { k, x: cx - (cx - v.x) * f, y: cy - (cy - v.y) * f };
    });
  }, []);

  const zoomCenter = (factor: number) => zoomAt(factor, size.w / 2, size.h / 2);

  // Wheel needs a non-passive listener so the page doesn't scroll instead
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
    pointers.current.set(e.pointerId, local(e));
    if (pointers.current.size === 2) {
      for (const id of pointers.current.keys()) box.current?.setPointerCapture(id);
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
    // Capturing on pointerdown would retarget the click to the box, so nodes
    // and edges would never see it
    if (!d.moved) box.current?.setPointerCapture(e.pointerId);
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
  const comparing = !!map.previous;
  const anyMoved = nodes.some((n) => moved.has(n.id));

  const edgePath = (a: Placed, b: Placed) => {
    const x1 = a.x + NODE_W;
    const y1 = a.y + NODE_H / 2;
    const back = b.x <= a.x + NODE_W / 2;
    const x2 = back ? b.x + NODE_W / 2 : b.x;
    const y2 = back ? b.y + (b.y > a.y ? 0 : NODE_H) : b.y + NODE_H / 2;
    const bend = back ? 60 : Math.max(40, (x2 - x1) / 2);
    const path = back ? `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2} ${y2 + (b.y > a.y ? -bend : bend)}, ${x2} ${y2}` : `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`;
    return { path, back, mx: (x1 + x2) / 2, my: (y1 + y2) / 2 };
  };
  const toName = (n: MapNode) => (n.kind === "service" ? serviceLabel(n.name) : n.name);

  return (
    <div
      ref={box}
      className={`smap-canvas ${panning ? "panning" : ""}`}
      tabIndex={0}
      role="application"
      aria-label="Service map. Drag to pan, scroll to zoom, plus and minus keys zoom, 0 fits the map."
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onMouseMove={onMouseMove}
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
          {gone.map((e) => {
            const a = at.get(e.source);
            const b = at.get(e.target);
            if (!a || !b) return null;
            const key = `gone:${edgeKey(e)}`;
            const { path, back, mx, my } = edgePath(a, b);
            return (
              <g
                key={key}
                className="smap-edge gone"
                {...tipProps(() => (
                  <>
                    <span className="tip-title">
                      {toName(a)} → {toName(b)}
                    </span>
                    <div>No calls in this range</div>
                    <div className="dim">The window before: {edgeSummary(e)}</div>
                  </>
                ))}
                tabIndex={0}
                role="img"
                aria-label={`${a.name} to ${b.name}: stopped, ${edgeSummary(e)} before`}
              >
                <path d={path} className="smap-hit" />
                <path d={path} fill="none" className="smap-gone-line" markerEnd="url(#smap-arrow)" />
                {!back && (
                  <text x={mx} y={my - 6} textAnchor="middle" className="smap-edge-label">
                    stopped
                  </text>
                )}
              </g>
            );
          })}
          {map.edges.map((e) => {
            const a = at.get(e.source);
            const b = at.get(e.target);
            if (!a || !b) return null;
            const key = edgeKey(e);
            const { path, back, mx, my } = edgePath(a, b);
            const w = 1.5 + (4 * e.calls) / maxCalls;
            const on = hover === key;
            const prev = prevEdges.get(key);
            const delta = comparing ? (prev ? changeText(change(e.per_min, prev.per_min), 0.25) : "new") : "";
            const pts = series?.[key];
            return (
              <g
                key={key}
                className={`smap-edge ${on ? "on" : ""}`}
                {...tipProps(() => (
                  <>
                    <span className="tip-title">
                      {toName(a)} → {toName(b)}
                    </span>
                    <div>{edgeSummary(e)}</div>
                    <div>{formatCount(e.calls)} calls</div>
                    {pts && pts.length > 1 && (
                      <>
                        <Spark points={pts} />
                        <div className="dim">Calls over the range; red marks failures</div>
                      </>
                    )}
                    {comparing && beforeLines({ rate: e.per_min, failed: e.error_rate, p95: e.p95 }, prev && { rate: prev.per_min, failed: prev.error_rate, p95: prev.p95 }, "rate")}
                    <div className="dim">Click for its routes and trend</div>
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
                    {delta && <tspan className={delta === "new" ? "smap-delta new" : "smap-delta"}>{` ${delta}`}</tspan>}
                  </text>
                )}
              </g>
            );
          })}
          {nodes.map((n) => {
            const svc = n.kind === "service";
            const color = svc ? KIND_COLOR.request : (KIND_COLOR[n.kind] ?? "var(--text-3)");
            const failed = svc && n.requests > 0 ? (100 * n.errors) / n.requests : 0;
            const role = roles?.get(n.id);
            const badges = svc ? badgesOf(overlay?.[n.name]) : [];
            const prev = prevNodes.get(n.id);
            const prevFailed = prev && prev.requests > 0 ? (100 * prev.errors) / prev.requests : 0;
            return (
              <g
                key={n.id}
                data-node={n.id}
                className="smap-node"
                transform={`translate(${n.x}, ${n.y})`}
                {...tipProps(() => (
                  <>
                    <span className="tip-title">{toName(n)}</span>
                    {svc ? (
                      n.requests > 0 ? (
                        <>
                          <div>
                            {formatCount(n.requests)} requests · {formatPercent(failed)} failed
                          </div>
                          {n.p95 !== null && <div>p95 {formatMs(n.p95)}</div>}
                        </>
                      ) : (
                        <div>No requests in this range</div>
                      )
                    ) : (
                      <div>{KIND_NAME[n.kind] ?? n.kind}</div>
                    )}
                    {svc && comparing && beforeLines({ rate: n.requests, failed, p95: n.p95 }, prev && { rate: prev.requests, failed: prevFailed, p95: prev.p95 }, "requests")}
                    {role && <div className={role === "origin" ? "smap-tip-bad" : "smap-tip-warn"}>{ROLE_TEXT[role]}</div>}
                    {badges.map((b) => (
                      <div key={b.key}>{b.text}</div>
                    ))}
                    <div className="dim">Click for details</div>
                  </>
                ))}
                onClick={clickGuard(() => onNode(n))}
                tabIndex={0}
                role="button"
                aria-label={`${toName(n)}${role ? `, ${ROLE_TEXT[role]}` : ""}${badges.length ? `, ${badges.map((b) => b.text).join(", ")}` : ""}`}
                onKeyDown={(ev) => ev.key === "Enter" && onNode(n)}
              >
                <rect width={NODE_W} height={NODE_H} rx={6} className={`smap-box ${svc ? "" : "dep"} ${role ?? ""} ${focus === n.id ? "focus" : ""}`} />
                <circle cx={16} cy={16} r={5} fill={color} />
                <text x={27} y={20} className="smap-name">
                  {truncate(toName(n), badges.length ? Math.max(8, 21 - 2 * badges.length) : 21)}
                </text>
                {badges.map((b, i) => {
                  const Icon = b.icon;
                  return <Icon key={b.key} x={NODE_W - 20 - i * 18} y={8} width={14} height={14} className={`smap-badge ${b.className}`} aria-hidden />;
                })}
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
        {anyMoved && (
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
        <TipBubble anchor={tip.anchor} id={tipId} className="rich">
          {tip.body}
        </TipBubble>
      )}
    </div>
  );
}

function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}
