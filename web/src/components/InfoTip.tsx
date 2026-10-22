import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type FocusEvent as ReactFocusEvent, type ReactNode } from "react";
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
function TipBubble({ anchor, id, side, className = "", children }: { anchor: HTMLElement; id: string; side?: "right"; className?: string; children: ReactNode }) {
  const tip = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ top: number; left: number; place: "below" | "above" | "right"; arrow: number } | null>(null);

  useLayoutEffect(() => {
    if (!tip.current) return;
    const a = anchor.getBoundingClientRect();
    const t = tip.current.getBoundingClientRect();
    if (side === "right") {
      const middle = a.top + a.height / 2;
      const top = Math.min(Math.max(middle - t.height / 2, EDGE), window.innerHeight - t.height - EDGE);
      setPos({ top, left: a.right + GAP, place: "right", arrow: Math.min(Math.max(middle - top, 10), t.height - 10) });
      return;
    }
    const center = a.left + a.width / 2;
    const left = Math.min(Math.max(center - t.width / 2, EDGE), window.innerWidth - t.width - EDGE);
    const below = a.bottom + GAP + t.height <= window.innerHeight - EDGE || a.top - GAP - t.height < EDGE;
    const top = below ? a.bottom + GAP : a.top - GAP - t.height;
    setPos({ top, left, place: below ? "below" : "above", arrow: Math.min(Math.max(center - left, 10), t.width - 10) });
  }, [anchor, side, children]);

  return createPortal(
    <div
      ref={tip}
      id={id}
      role="tooltip"
      className={`info-tip ${pos?.place ?? ""} ${className}`}
      style={
        pos
          ? ({ top: pos.top, left: pos.left, [pos.place === "right" ? "--arrow-y" : "--arrow-x"]: `${pos.arrow}px` } as CSSProperties)
          : { top: 0, left: 0, visibility: "hidden" }
      }
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
 * A richer tip than a title, for an element that is not an info icon: the
 * sidebar's items, which show it to their right.
 */
export function useHoverTip<T extends HTMLElement>(content: ReactNode, side?: "right") {
  const id = useId();
  const anchor = useRef<T>(null);
  const timer = useRef<number>();
  const [open, setOpen] = useState(false);
  const hide = useRef(() => {
    window.clearTimeout(timer.current);
    setOpen(false);
  }).current;
  const show = () => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setOpen(true), DELAY);
  };
  useDismiss(open, hide);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  return {
    props: {
      ref: anchor,
      "aria-describedby": open ? id : undefined,
      onMouseEnter: show,
      onMouseLeave: hide,
      onFocus: (e: ReactFocusEvent<T>) => e.currentTarget.matches(":focus-visible") && show(),
      onBlur: hide,
      onClick: hide,
    },
    bubble:
      open && anchor.current ? (
        <TipBubble anchor={anchor.current} id={id} side={side} className="rich">
          {content}
        </TipBubble>
      ) : null,
  };
}

/** Where a title's text lives once it is taken off the element. */
const TIP_ATTR = "data-tip";
/** Marks an aria-description this file added, so it goes with the title. */
const DESC_MARK = "data-tip-desc";

/**
 * Keeps the browser from ever showing its own tooltip: every title the app
 * sets (React props, later updates, injected HTML) moves to data-tip, which
 * TitleTips shows in the app's bubble. Moving it on hover is not enough: a
 * disabled button gets no pointer events in some browsers, so its title would
 * still show natively. The text stays available to screen readers as an
 * aria-description. Call once, before the first render.
 */
export function captureTitles() {
  const proto = Element.prototype as Element & { __tipsCaptured?: boolean };
  if (proto.__tipsCaptured) return;
  proto.__tipsCaptured = true;
  const set = proto.setAttribute;
  const remove = proto.removeAttribute;
  // An iframe's title names the frame for screen readers and never shows.
  const isTitle = (el: Element, name: string) => name.toLowerCase() === "title" && !(el instanceof HTMLIFrameElement);

  const stash = (el: Element, value: string) => {
    if (value === "") return unstash(el);
    set.call(el, TIP_ATTR, value);
    if (!el.hasAttribute("aria-description") || el.hasAttribute(DESC_MARK)) {
      set.call(el, "aria-description", value);
      set.call(el, DESC_MARK, "");
    }
  };
  const unstash = (el: Element) => {
    remove.call(el, TIP_ATTR);
    if (el.hasAttribute(DESC_MARK)) {
      remove.call(el, "aria-description");
      remove.call(el, DESC_MARK);
    }
  };
  const take = (el: Element) => {
    const v = el.getAttribute("title");
    if (v === null || el instanceof HTMLIFrameElement) return;
    remove.call(el, "title");
    stash(el, v);
  };

  proto.setAttribute = function (this: Element, name: string, value: string) {
    if (isTitle(this, name)) return stash(this, String(value));
    return set.call(this, name, value);
  };
  proto.removeAttribute = function (this: Element, name: string) {
    if (isTitle(this, name)) unstash(this);
    return remove.call(this, name);
  };

  // Anything that bypasses setAttribute: el.title = "…", innerHTML.
  const sweep = (root: Element) => {
    if (root.hasAttribute("title")) take(root);
    root.querySelectorAll("[title]").forEach(take);
  };
  new MutationObserver((records) => {
    for (const r of records) {
      if (r.type === "attributes" && r.target instanceof Element) take(r.target);
      r.addedNodes.forEach((n) => n instanceof Element && sweep(n));
    }
  }).observe(document.documentElement, { subtree: true, childList: true, attributes: true, attributeFilter: ["title"] });
  sweep(document.documentElement);
}

/**
 * Shows every title in the app's tooltip; captureTitles has already moved
 * each one to data-tip. Mounted once, for hover and keyboard focus.
 */
export function TitleTips() {
  const id = useId();
  const timer = useRef<number>();
  const active = useRef<HTMLElement | null>(null);
  const [tip, setTip] = useState<{ el: HTMLElement; text: string } | null>(null);

  const hide = useRef(() => {
    window.clearTimeout(timer.current);
    const el = active.current;
    if (el?.getAttribute("aria-describedby") === id) el.removeAttribute("aria-describedby");
    active.current = null;
    setTip(null);
  }).current;
  // A click, scroll or Escape closes the tip until the pointer moves on.
  const dismiss = useRef(() => {
    window.clearTimeout(timer.current);
    setTip(null);
  }).current;

  useEffect(() => {
    const take = (el: HTMLElement, focus: boolean) => {
      if (active.current === el) return;
      hide();
      active.current = el;
      if (focus && !el.hasAttribute("aria-describedby")) el.setAttribute("aria-describedby", id);
      timer.current = window.setTimeout(() => {
        // Read at show time, so a title changed meanwhile is the one shown.
        const text = el.getAttribute(TIP_ATTR);
        if (active.current === el && text) setTip({ el, text });
      }, DELAY);
    };
    const tipped = (target: EventTarget | null) => (target instanceof Element ? (target.closest(`[${TIP_ATTR}]`) as HTMLElement | null) : null);

    const onOver = (e: PointerEvent) => {
      if (e.pointerType === "touch") return;
      const el = tipped(e.target);
      if (el) take(el, false);
      else hide();
    };
    const onOut = (e: PointerEvent) => {
      const el = active.current;
      if (el && !(e.relatedTarget instanceof Node && el.contains(e.relatedTarget))) hide();
    };
    const onFocusIn = (e: FocusEvent) => {
      const el = e.target instanceof HTMLElement && e.target.matches(":focus-visible") ? tipped(e.target) : null;
      if (el && el === e.target) take(el, true);
    };
    const onFocusOut = (e: FocusEvent) => active.current === e.target && hide();

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
