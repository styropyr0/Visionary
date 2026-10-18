import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Info } from "lucide-react";

const GAP = 8;
const EDGE = 12;

/**
 * An icon that explains something on hover or keyboard focus, in the app's own
 * tooltip style rather than the browser's. The tip renders into the body so
 * panels with overflow: hidden never clip it, and flips above the icon when
 * there is no room below.
 */
export function InfoTip({
  text,
  icon,
  size = 13,
  className = "",
  style,
}: {
  text: ReactNode;
  icon?: ReactNode;
  size?: number;
  className?: string;
  style?: CSSProperties;
}) {
  const id = useId();
  const anchor = useRef<HTMLSpanElement>(null);
  const tip = useRef<HTMLDivElement>(null);
  const timer = useRef<number>();
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number; below: boolean; arrow: number } | null>(null);

  const show = () => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setOpen(true), 120);
  };
  const hide = () => {
    window.clearTimeout(timer.current);
    setOpen(false);
    setPos(null);
  };

  useLayoutEffect(() => {
    if (!open || !anchor.current || !tip.current) return;
    const a = anchor.current.getBoundingClientRect();
    const t = tip.current.getBoundingClientRect();
    const center = a.left + a.width / 2;
    const left = Math.min(Math.max(center - t.width / 2, EDGE), window.innerWidth - t.width - EDGE);
    const below = a.bottom + GAP + t.height <= window.innerHeight - EDGE || a.top - GAP - t.height < EDGE;
    const top = below ? a.bottom + GAP : a.top - GAP - t.height;
    setPos({ top, left, below, arrow: Math.min(Math.max(center - left, 10), t.width - 10) });
  }, [open, text]);

  // Scrolling or Escape dismisses it, like the browser's own tooltip.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && hide();
    window.addEventListener("keydown", onKey);
    window.addEventListener("scroll", hide, true);
    window.addEventListener("resize", hide);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", hide, true);
      window.removeEventListener("resize", hide);
    };
  }, [open]);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  return (
    <>
      <span
        ref={anchor}
        className={`panel-info ${className}`}
        style={style}
        tabIndex={0}
        aria-describedby={open ? id : undefined}
        onMouseEnter={show}
        onMouseLeave={hide}
        onFocus={show}
        onBlur={hide}
      >
        {icon ?? <Info size={size} />}
      </span>
      {open &&
        createPortal(
          <div
            ref={tip}
            id={id}
            role="tooltip"
            className={`info-tip ${pos ? (pos.below ? "below" : "above") : ""}`}
            style={pos ? { top: pos.top, left: pos.left, "--arrow-x": `${pos.arrow}px` } as CSSProperties : { top: 0, left: 0, visibility: "hidden" }}
          >
            {text}
          </div>,
          document.body,
        )}
    </>
  );
}
