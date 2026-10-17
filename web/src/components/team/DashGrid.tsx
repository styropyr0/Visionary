import { useEffect, useLayoutEffect, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import type { DashPanel } from "../../lib/team";

export const COLS = 12;
export const ROW_H = 40;
export const GAP = 12;
const MIN_W = 2;
const MIN_H = 2;
const MAX_H = 24;

type Box = Pick<DashPanel, "x" | "y" | "w" | "h">;

function overlaps(a: Box, b: Box): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
}

/**
 * Puts `moved` where it was dropped, pushes anything it covers down, then
 * floats every panel up as far as it goes, so the grid never has holes.
 */
export function resolveLayout(items: DashPanel[], movedId?: string): DashPanel[] {
  const moved = items.find((i) => i.id === movedId);
  const rest = items.filter((i) => i.id !== movedId).sort((a, b) => a.y - b.y || a.x - b.x);
  const placed: DashPanel[] = moved ? [{ ...moved }] : [];
  for (const item of rest) {
    const c = { ...item };
    for (let blocker = placed.find((p) => overlaps(p, c)); blocker; blocker = placed.find((p) => overlaps(p, c))) {
      c.y = blocker.y + blocker.h;
    }
    placed.push(c);
  }
  const out: DashPanel[] = [];
  for (const item of [...placed].sort((a, b) => a.y - b.y || a.x - b.x)) {
    const c = { ...item };
    while (c.y > 0 && !out.some((p) => overlaps(p, { ...c, y: c.y - 1 }))) c.y -= 1;
    out.push(c);
  }
  return out;
}

interface Gesture {
  id: string;
  mode: "move" | "resize";
  startX: number;
  startY: number;
  origin: Box;
  base: DashPanel[];
}

interface Props {
  items: DashPanel[];
  editing: boolean;
  onChange: (items: DashPanel[]) => void;
  render: (item: DashPanel, size: { width: number; height: number }) => ReactNode;
}

/**
 * A 12-column grid. In edit mode a panel moves by dragging its header and
 * resizes from its bottom-right corner; positions snap to whole cells.
 */
export function DashGrid({ items, editing, onChange, render }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [gesture, setGesture] = useState<Gesture | null>(null);
  const [preview, setPreview] = useState<DashPanel[] | null>(null);
  const [drag, setDrag] = useState<{ dx: number; dy: number } | null>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.clientWidth);
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const colW = Math.max(1, (width - GAP * (COLS - 1)) / COLS);
  const px = (b: Box) => ({
    left: b.x * (colW + GAP),
    top: b.y * (ROW_H + GAP),
    width: b.w * colW + (b.w - 1) * GAP,
    height: b.h * ROW_H + (b.h - 1) * GAP,
  });

  const shown = preview ?? items;

  useEffect(() => {
    if (!gesture) return;
    const onMove = (e: PointerEvent) => {
      const dx = e.clientX - gesture.startX;
      const dy = e.clientY - gesture.startY;
      const o = gesture.origin;
      let next: Box;
      if (gesture.mode === "move") {
        const x = Math.min(COLS - o.w, Math.max(0, Math.round(o.x + dx / (colW + GAP))));
        const y = Math.max(0, Math.round(o.y + dy / (ROW_H + GAP)));
        next = { ...o, x, y };
        setDrag({ dx, dy });
      } else {
        const w = Math.min(COLS - o.x, Math.max(MIN_W, Math.round(o.w + dx / (colW + GAP))));
        const h = Math.min(MAX_H, Math.max(MIN_H, Math.round(o.h + dy / (ROW_H + GAP))));
        next = { ...o, w, h };
      }
      setPreview(resolveLayout(gesture.base.map((i) => (i.id === gesture.id ? { ...i, ...next } : i)), gesture.id));
    };
    const onUp = () => {
      setGesture(null);
      setDrag(null);
      setPreview((p) => {
        if (p) onChange(p);
        return null;
      });
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
    return () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
    };
  }, [gesture, colW, onChange]);

  const start = (e: ReactPointerEvent, item: DashPanel, mode: Gesture["mode"]) => {
    if (!editing || e.button !== 0) return;
    if (mode === "move") {
      const t = e.target as HTMLElement;
      // Only the header drags, and its buttons keep working.
      if (!t.closest(".panel-head") || t.closest("button, a, input")) return;
    }
    e.preventDefault();
    e.stopPropagation();
    setGesture({ id: item.id, mode, startX: e.clientX, startY: e.clientY, origin: { x: item.x, y: item.y, w: item.w, h: item.h }, base: items });
  };

  const rows = shown.reduce((m, i) => Math.max(m, i.y + i.h), 0);
  const height = Math.max(rows * (ROW_H + GAP) - GAP, editing ? 6 * (ROW_H + GAP) : 0);
  const active = gesture?.id;
  const original = items.find((i) => i.id === active);

  return (
    <div ref={ref} className={`dash-grid ${editing ? "editing" : ""} ${gesture ? "busy" : ""}`} style={{ height }}>
      {editing && gesture && (() => {
        const target = shown.find((i) => i.id === active);
        return target ? <div className="dash-ghost" style={px(target)} aria-hidden /> : null;
      })()}
      {width > 0 &&
        shown.map((item) => {
          const moving = item.id === active && gesture?.mode === "move" && drag && original;
          // The dragged panel follows the pointer; the ghost shows where it lands.
          const box = moving ? { ...px(original), left: px(original).left + drag.dx, top: px(original).top + drag.dy } : px(item);
          return (
            <div
              key={item.id}
              className={`dash-item ${item.id === active ? "active" : ""}`}
              style={{ left: box.left, top: box.top, width: box.width, height: box.height }}
              onPointerDown={(e) => start(e, item, "move")}
            >
              {render(item, { width: box.width, height: box.height })}
              {editing && (
                <span
                  className="dash-resize"
                  role="separator"
                  aria-label="Resize panel"
                  title="Drag to resize"
                  onPointerDown={(e) => start(e, item, "resize")}
                />
              )}
            </div>
          );
        })}
    </div>
  );
}
