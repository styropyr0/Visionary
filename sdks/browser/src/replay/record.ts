/**
 * The DOM recorder: a full snapshot of the page, then its mutations, mouse,
 * scroll, input and viewport changes. Each event is [time ms, type, data];
 * the dashboard's player reads the same shapes (web/src/lib/replay.ts).
 */

export type Ev = [number, number, unknown];

export const FULL = 1;
export const MUTATION = 2;
export const MOVE = 3;
export const CLICK = 4;
export const SCROLL = 5;
export const INPUT = 6;
export const VIEWPORT = 7;

/** A serialized node: t is the node type (1 element, 3 text, 8 anything else). */
export interface SNode {
  i: number;
  t: 1 | 3 | 8;
  n?: string;
  a?: Record<string, string>;
  c?: SNode[];
  x?: string;
  s?: 1; // SVG
  b?: [number, number]; // blocked: drawn as an empty box of this size
}

export interface RecordOptions {
  maskInputs: boolean;
  maskAllText: boolean;
  blockSelector?: string;
}

const SVG_NS = "http://www.w3.org/2000/svg";
const BLOCK = "[data-visionary-block]";
const MASK = "[data-visionary-mask]";
const SECRET_NAME = /pass|card|cc-?(num|csc|exp)|cvv|cvc|ssn|iban|secret|token|otp|\bpin\b/i;
const MASKED_ATTRS = ["title", "alt", "placeholder", "aria-label"];

/** Fields whose value is never recorded, whatever the options say. */
export function secretField(el: Element): boolean {
  const type = (el.getAttribute("type") || "").toLowerCase();
  if (type === "password" || type === "email" || type === "tel" || type === "hidden") return true;
  if (/cc-|password|one-time-code|email|tel/.test((el.getAttribute("autocomplete") || "").toLowerCase())) return true;
  return SECRET_NAME.test(`${el.getAttribute("name") || ""} ${el.id}`);
}

export const maskText = (s: string) => s.replace(/\S/g, "*");

function validSelector(sel?: string): string | undefined {
  if (!sel) return undefined;
  try {
    document.createDocumentFragment().querySelector(sel);
    return sel;
  } catch {
    return undefined;
  }
}

function throttle<A extends unknown[]>(ms: number, fn: (...a: A) => void): (...a: A) => void {
  let last = 0;
  return (...a) => {
    const now = Date.now();
    if (now - last < ms) return;
    last = now;
    fn(...a);
  };
}

export class Recorder {
  private ids = new WeakMap<Node, number>();
  private next = 1;
  /** Elements whose children are not recorded: blocked ones, scripts, textareas. */
  private opaque = new WeakSet<Node>();
  private observer: MutationObserver | null = null;
  private off: (() => void)[] = [];
  private block: string | undefined;

  constructor(
    private o: RecordOptions,
    private emit: (e: Ev) => void,
  ) {
    this.block = validSelector(o.blockSelector);
  }

  start() {
    this.snapshot();
    this.observer = new MutationObserver((r) => this.mutations(r));
    this.observer.observe(document.documentElement, { childList: true, subtree: true, attributes: true, characterData: true });
    const on = (target: EventTarget, type: string, fn: (e: Event) => void) => {
      const h = (e: Event) => {
        try {
          fn(e);
        } catch {
          // Never break the page.
        }
      };
      target.addEventListener(type, h, { capture: true, passive: true });
      this.off.push(() => target.removeEventListener(type, h, { capture: true }));
    };
    on(document, "mousemove", throttle(50, (e) => this.emit([Date.now(), MOVE, [(e as MouseEvent).clientX, (e as MouseEvent).clientY]])));
    on(document, "click", (e) => this.emit([Date.now(), CLICK, [(e as MouseEvent).clientX, (e as MouseEvent).clientY]]));
    on(document, "touchstart", (e) => {
      const t = (e as TouchEvent).touches[0];
      if (t) this.emit([Date.now(), CLICK, [t.clientX, t.clientY]]);
    });
    const scrolled = new Map<EventTarget, number>();
    on(document, "scroll", (e) => {
      const t = e.target as Node;
      const now = Date.now();
      if (now - (scrolled.get(t) ?? 0) < 100) return;
      scrolled.set(t, now);
      if (t === document) this.emit([now, SCROLL, [this.ids.get(document.documentElement), scrollX, scrollY]]);
      else if (this.ids.has(t)) this.emit([now, SCROLL, [this.ids.get(t), (t as Element).scrollLeft, (t as Element).scrollTop]]);
    });
    on(window, "resize", throttle(200, () => this.emit([Date.now(), VIEWPORT, [innerWidth, innerHeight]])));
    on(document, "input", (e) => this.input(e.target as Element));
    on(document, "change", (e) => this.input(e.target as Element));
  }

  stop() {
    this.observer?.disconnect();
    this.observer = null;
    this.off.forEach((f) => f());
    this.off = [];
  }

  /** Records what the observer has not delivered yet. */
  takeRecords() {
    const r = this.observer?.takeRecords();
    if (r?.length) this.mutations(r);
  }

  snapshot() {
    this.takeRecords();
    const node = this.serialize(document.documentElement, this.o.maskAllText);
    this.emit([Date.now(), FULL, { node, w: innerWidth, h: innerHeight, x: scrollX, y: scrollY, href: location.origin + location.pathname }]);
  }

  private blocked(el: Element): boolean {
    const tag = el.tagName;
    return tag === "IFRAME" || tag === "OBJECT" || tag === "EMBED" || el.matches(BLOCK) || (!!this.block && el.matches(this.block));
  }

  private masked(el: Element | null): boolean {
    return this.o.maskAllText || !!el?.closest?.(MASK);
  }

  private value(el: Element, v: string): string {
    const type = (el.getAttribute("type") || "").toLowerCase();
    if (secretField(el)) return "";
    if (type === "button" || type === "submit" || type === "reset") return v;
    return this.o.maskInputs ? "*".repeat(Math.min(v.length, 24)) : v;
  }

  private attr(el: Element, name: string, v: string, masked: boolean): string | null {
    if (/^on/i.test(name)) return null;
    const tag = el.tagName;
    if (name === "value" && (tag === "INPUT" || tag === "TEXTAREA" || tag === "OPTION")) return tag === "OPTION" ? v : this.value(el, v);
    if (masked && MASKED_ATTRS.includes(name)) return maskText(v);
    return v;
  }

  private serialize(n: Node, masked: boolean): SNode {
    const i = this.next++;
    this.ids.set(n, i);
    if (n.nodeType === 3) {
      const p = n.parentNode;
      let x = n.textContent || "";
      if (p?.nodeName !== "STYLE" && masked) x = maskText(x);
      return { i, t: 3, x };
    }
    if (n.nodeType !== 1) return { i, t: 8 };
    const el = n as Element;
    const tag = el.tagName.toLowerCase();
    // Scripts never run in the player; noscript would show there.
    if (tag === "script" || tag === "noscript") {
      this.opaque.add(el);
      return { i, t: 8 };
    }
    const s: SNode = { i, t: 1, n: tag };
    if (el.namespaceURI === SVG_NS) s.s = 1;
    if (this.blocked(el)) {
      this.opaque.add(el);
      const r = el.getBoundingClientRect();
      s.b = [Math.round(r.width), Math.round(r.height)];
      return s;
    }
    masked = masked || el.hasAttribute("data-visionary-mask");
    const a: Record<string, string> = {};
    for (const at of Array.from(el.attributes)) {
      const v = this.attr(el, at.name, at.value, masked);
      if (v !== null) a[at.name] = v;
    }
    if (tag === "input" || tag === "textarea") {
      const f = el as HTMLInputElement;
      if (f.type === "checkbox" || f.type === "radio") {
        if (f.checked) a.checked = "";
        else delete a.checked;
      } else {
        a.value = this.value(el, f.value);
      }
    }
    s.a = a;
    if (tag === "textarea") {
      this.opaque.add(el);
      return s;
    }
    s.c = [];
    if (tag === "style" && !el.textContent?.trim()) {
      // CSS-in-JS libraries insert rules without text; keep them as text.
      try {
        const css = Array.from((el as HTMLStyleElement).sheet?.cssRules ?? [], (r) => r.cssText).join("\n");
        if (css) {
          s.c.push({ i: this.next++, t: 3, x: css });
          return s;
        }
      } catch {
        // A cross-origin sheet: nothing to read.
      }
    }
    for (let c = el.firstChild; c; c = c.nextSibling) s.c.push(this.serialize(c, masked));
    return s;
  }

  private input(el: Element) {
    const i = this.ids.get(el);
    if (i === undefined || (this.opaque.has(el) && el.tagName !== "TEXTAREA")) return;
    const f = el as HTMLInputElement;
    if (el.tagName === "SELECT") {
      if (!this.o.maskInputs) this.emit([Date.now(), INPUT, { i, si: (el as HTMLSelectElement).selectedIndex }]);
    } else if (f.type === "checkbox" || f.type === "radio") {
      this.emit([Date.now(), INPUT, { i, c: f.checked }]);
      // The other radios of a group change without an event.
      if (f.type === "radio" && f.name) {
        document.querySelectorAll(`input[type=radio][name="${CSS.escape(f.name)}"]`).forEach((r) => {
          const j = this.ids.get(r);
          if (r !== el && j !== undefined) this.emit([Date.now(), INPUT, { i: j, c: (r as HTMLInputElement).checked }]);
        });
      }
    } else if ("value" in f) {
      this.emit([Date.now(), INPUT, { i, v: this.value(el, f.value) }]);
    }
  }

  private mutations(records: MutationRecord[]) {
    const removed = new Set<number>();
    const added = new Set<Node>();
    const attrs = new Map<Element, Set<string>>();
    const texts = new Set<Node>();
    for (const r of records) {
      if (r.type === "childList") {
        r.removedNodes.forEach((n) => {
          const i = this.ids.get(n);
          if (i !== undefined) removed.add(i);
          added.delete(n);
        });
        r.addedNodes.forEach((n) => added.add(n));
      } else if (r.type === "attributes") {
        const el = r.target as Element;
        if (!attrs.has(el)) attrs.set(el, new Set());
        attrs.get(el)!.add(r.attributeName!);
      } else {
        texts.add(r.target);
      }
    }
    const adds: { p: number; n: number | null; s: SNode }[] = [];
    const tops: Node[] = [];
    for (const n of added) {
      if (!n.isConnected) continue;
      let top = true;
      for (let p = n.parentNode; p; p = p.parentNode) {
        if (added.has(p)) {
          top = false;
          break;
        }
      }
      if (top) tops.push(n);
    }
    // Last first, so each node's next sibling is already known to the player.
    tops.sort((a, b) => (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? 1 : -1));
    for (const n of tops) {
      const parent = n.parentNode as Element;
      const p = this.ids.get(parent);
      if (p === undefined || this.opaque.has(parent)) continue;
      let sib = n.nextSibling;
      while (sib && !this.ids.has(sib)) sib = sib.nextSibling;
      adds.push({ p, n: sib ? this.ids.get(sib)! : null, s: this.serialize(n, this.masked(parent)) });
    }
    const at: { i: number; k: string; v: string | null }[] = [];
    attrs.forEach((names, el) => {
      const i = this.ids.get(el);
      if (i === undefined || !el.isConnected || this.opaque.has(el)) return;
      const masked = this.masked(el);
      names.forEach((k) => {
        const raw = el.getAttribute(k);
        const v = raw === null ? null : this.attr(el, k, raw, masked);
        if (raw === null || v !== null) at.push({ i, k, v });
      });
    });
    const tx: { i: number; x: string }[] = [];
    texts.forEach((n) => {
      const i = this.ids.get(n);
      const p = n.parentNode as Element | null;
      if (i === undefined || !n.isConnected || !p || this.opaque.has(p)) return;
      const x = n.textContent || "";
      tx.push({ i, x: p.nodeName !== "STYLE" && this.masked(p) ? maskText(x) : x });
    });
    if (removed.size || adds.length || at.length || tx.length) {
      this.emit([Date.now(), MUTATION, { r: Array.from(removed), a: adds, at, tx }]);
    }
  }
}
