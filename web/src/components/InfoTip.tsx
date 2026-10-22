import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Info } from "lucide-react";

const GAP = 8;
const EDGE = 12;
/** How long the pointer rests before a tip shows, the same for every tip. */
const DELAY = 200;

/**
 * The one tooltip bubble of the app: info icons, warnings and every element
 * with a title all show their text in it. It renders into the body so panels
 * with overflow: hidden never clip it, and flips above the anchor when there
 * is no room below.
 */
function TipBubble({ anchor, id, children }: { anchor: HTMLElement; id: string; children: ReactNode }) {
  const tip = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ top: number; left: number; below: boolean; arrow: number } | null>(null);

  useLayoutEffect(() => {
    if (!tip.current) return;
    const a = anchor.getBoundingClientRect();
    const t = tip.current.getBoundingClientRect();
    const center = a.left + a.width / 2;
    const left = Math.min(Math.max(center - t.width / 2, EDGE), window.innerWidth - t.width - EDGE);
    const below = a.bottom + GAP + t.height <= window.innerHeight - EDGE || a.top - GAP - t.height < EDGE;
    const top = below ? a.bottom + GAP : a.top - GAP - t.height;
    setPos({ top, left, below, arrow: Math.min(Math.max(center - left, 10), t.width - 10) });
  }, [anchor, children]);

  return createPortal(
    <div
      ref={tip}
      id={id}
      role="tooltip"
      className={`info-tip ${pos ? (pos.below ? "below" : "above") : ""}`}
      style={pos ? ({ top: pos.top, left: pos.left, "--arrow-x": `${pos.arrow}px` } as CSSProperties) : { top: 0, left: 0, visibility: "hidden" }}
    >
      {children}
    </div>,
    document.body,
  );
}

// Scrolling, resizing or Escape dismisses a tip, like the browser's own.
function useDismiss(open: boolean, hide: () => void) {
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
  }, [open, hide]);
}

/** An icon that explains something on hover or keyboard focus. */
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
  const timer = useRef<number>();
  const [open, setOpen] = useState(false);

  const show = () => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setOpen(true), DELAY);
  };
  const hide = useRef(() => {
    window.clearTimeout(timer.current);
    setOpen(false);
  }).current;

  useDismiss(open, hide);
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
      {open && anchor.current && (
        <TipBubble anchor={anchor.current} id={id}>
          {text}
        </TipBubble>
      )}
    </>
  );
}

/**
 * Shows every title attribute in the app's tooltip instead of the browser's.
 * Mounted once: while the pointer (or keyboard focus) is on an element with a
 * title, the title moves aside so the browser's tip never appears, and comes
 * back when it leaves, so screen readers and React still see it.
 */
export function TitleTips() {
  const id = useId();
  const timer = useRef<number>();
  const active = useRef<{ el: HTMLElement; text: string; observer: MutationObserver } | null>(null);
  const [tip, setTip] = useState<{ el: HTMLElement; text: string } | null>(null);

  const hide = useRef(() => {
    window.clearTimeout(timer.current);
    const a = active.current;
    if (a) {
      a.observer.disconnect();
      if (!a.el.hasAttribute("title")) a.el.setAttribute("title", a.text);
      if (a.el.getAttribute("aria-describedby") === id) a.el.removeAttribute("aria-describedby");
      active.current = null;
    }
    setTip(null);
  }).current;
  // A click, scroll or Escape closes the tip but keeps the title aside until
  // the pointer leaves, so the browser's tip does not show up in its place.
  const dismiss = useRef(() => {
    window.clearTimeout(timer.current);
    setTip(null);
  }).current;

  useEffect(() => {
    const take = (el: HTMLElement, focus: boolean) => {
      if (active.current?.el === el) return;
      hide();
      const text = el.getAttribute("title") ?? "";
      el.removeAttribute("title");
      // React may set a new title while it is aside; pick it up instead.
      const observer = new MutationObserver(() => {
        const next = el.getAttribute("title");
        if (next === null || !active.current) return;
        el.removeAttribute("title");
        active.current.text = next;
        setTip((t) => (t && t.el === el ? { el, text: next } : t));
      });
      observer.observe(el, { attributes: true, attributeFilter: ["title"] });
      active.current = { el, text, observer };
      if (focus && !el.hasAttribute("aria-describedby")) el.setAttribute("aria-describedby", id);
      timer.current = window.setTimeout(() => active.current && setTip({ el, text: active.current.text }), DELAY);
    };
    const titled = (target: EventTarget | null) =>
      target instanceof Element ? (target.closest("[title]:not(iframe):not([title=''])") as HTMLElement | null) : null;

    const onOver = (e: PointerEvent) => {
      if (e.pointerType === "touch") return;
      if (active.current && e.target instanceof Node && active.current.el.contains(e.target)) return;
      const el = titled(e.target);
      if (el) take(el, false);
      else hide();
    };
    const onOut = (e: PointerEvent) => {
      const a = active.current;
      if (a && !(e.relatedTarget instanceof Node && a.el.contains(e.relatedTarget))) hide();
    };
    const onFocusIn = (e: FocusEvent) => {
      const el = e.target instanceof HTMLElement && e.target.matches(":focus-visible") ? titled(e.target) : null;
      if (el && el === e.target) take(el, true);
    };
    const onFocusOut = (e: FocusEvent) => active.current?.el === e.target && hide();

    document.addEventListener("pointerover", onOver);
    document.addEventListener("pointerout", onOut);
    document.addEventListener("pointerdown", dismiss);
    document.addEventListener("focusin", onFocusIn);
    document.addEventListener("focusout", onFocusOut);
    return () => {
      document.removeEventListener("pointerover", onOver);
      document.removeEventListener("pointerout", onOut);
      document.removeEventListener("pointerdown", dismiss);
      document.removeEventListener("focusin", onFocusIn);
      document.removeEventListener("focusout", onFocusOut);
      hide();
    };
  }, [id, hide, dismiss]);

  useDismiss(tip !== null, dismiss);

  return tip && tip.el.isConnected ? (
    <TipBubble anchor={tip.el} id={id}>
      {tip.text}
    </TipBubble>
  ) : null;
}
