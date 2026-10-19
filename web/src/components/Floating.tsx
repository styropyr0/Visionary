import { useLayoutEffect, useState, type CSSProperties, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";

const GAP = 6;
const MARGIN = 8;

/**
 * A menu drawn over everything, next to its anchor. It lives in a portal with
 * fixed positioning, so the scrolling body of a drawer or modal cannot clip
 * it, and opens upward when there is more room above than below.
 */
export function FloatingMenu({
  anchor,
  menuRef,
  align = "right",
  matchWidth,
  maxHeight = 320,
  className = "",
  style,
  role,
  children,
}: {
  anchor: RefObject<HTMLElement>;
  menuRef: RefObject<HTMLDivElement>;
  align?: "left" | "right";
  /** Make the menu as wide as the anchor (a combobox's suggestions). */
  matchWidth?: boolean;
  maxHeight?: number;
  className?: string;
  style?: CSSProperties;
  role?: string;
  children: ReactNode;
}) {
  const [pos, setPos] = useState<CSSProperties | null>(null);

  useLayoutEffect(() => {
    const place = () => {
      const el = anchor.current;
      if (!el) return;
      const r = el.getBoundingClientRect();
      const below = window.innerHeight - r.bottom - GAP - MARGIN;
      const above = r.top - GAP - MARGIN;
      const menuH = Math.min(maxHeight, menuRef.current?.scrollHeight ?? maxHeight);
      const up = below < menuH && above > below;
      // .popover anchors to its parent's corner; every edge is set here instead.
      const p: CSSProperties = { position: "fixed", top: "auto", bottom: "auto", left: "auto", right: "auto", maxHeight: Math.max(120, Math.min(maxHeight, up ? above : below)) };
      if (up) p.bottom = window.innerHeight - r.top + GAP;
      else p.top = r.bottom + GAP;
      if (matchWidth) p.width = r.width;
      if (align === "left" || matchWidth) p.left = Math.max(MARGIN, r.left);
      else p.right = Math.max(MARGIN, window.innerWidth - r.right);
      setPos(p);
    };
    place();
    // Capture: a scroll in any container (the drawer's body) moves the anchor.
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [anchor, menuRef, align, matchWidth, maxHeight]);

  return createPortal(
    <div
      ref={menuRef}
      className={`popover floating ${className}`}
      role={role}
      // Hidden until placed, so it never flashes at the wrong spot.
      style={{ ...style, ...(pos ?? { position: "fixed", visibility: "hidden" }) }}
    >
      {children}
    </div>,
    document.body,
  );
}
