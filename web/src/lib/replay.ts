/**
 * Plays back the browser SDK's recordings in a sandboxed iframe. They come from
 * browsers with a public key, so scripts, frames and meta refreshes are never built.
 */

export type ReplayEvent = [number, number, unknown];

export const FULL = 1;
export const MUTATION = 2;
export const MOVE = 3;
export const CLICK = 4;
export const SCROLL = 5;
export const INPUT = 6;
export const VIEWPORT = 7;

/** Events a person caused; long gaps between them are what skipping jumps over. */
export const USER_EVENTS = new Set([FULL, MOVE, CLICK, SCROLL, INPUT, VIEWPORT]);

interface SNode {
  i: number;
  t: 1 | 3 | 8;
  n?: string;
  a?: Record<string, string>;
  c?: SNode[];
  x?: string;
  s?: 1;
  b?: [number, number];
}

interface Snapshot {
  node: SNode;
  w: number;
  h: number;
  x: number;
  y: number;
  href: string;
}

interface Mutation {
  r: number[];
  a: { p: number; n: number | null; s: SNode }[];
  at: { i: number; k: string; v: string | null }[];
  tx: { i: number; x: string }[];
}

const SVG_NS = "http://www.w3.org/2000/svg";
const UNSAFE_TAGS = new Set(["script", "iframe", "frame", "frameset", "object", "embed", "base", "portal", "applet"]);
const BLOCKED_STYLE = "background: repeating-linear-gradient(45deg, #8884 0 6px, #8882 6px 12px); display: inline-block;";

function safeAttr(name: string, value: string): boolean {
  const n = name.toLowerCase();
  if (n.startsWith("on") || n === "srcdoc" || n === "http-equiv") return false;
  return !((n === "href" || n === "src" || n === "xlink:href" || n === "action" || n === "formaction") && /^\s*(javascript|vbscript|data:text\/html)/i.test(value));
}

export class ReplayEngine {
  private nodes = new Map<number, Node>();
  private pos = 0;
  /** Recorded viewport, in CSS pixels. */
  width = 1280;
  height = 800;
  onViewport: (w: number, h: number) => void = () => {};
  onPointer: (x: number, y: number, click: boolean) => void = () => {};

  constructor(
    private frame: HTMLIFrameElement,
    private events: ReplayEvent[],
  ) {}

  get start() {
    return this.events[0]?.[0] ?? 0;
  }

  get end() {
    return this.events[this.events.length - 1]?.[0] ?? 0;
  }

  /** Draws the page as it was at t: the last snapshot before it, then what followed. */
  seek(t: number) {
    let from = this.events.findIndex((e) => e[1] === FULL);
    if (from < 0) return;
    for (let i = from; i < this.events.length && this.events[i][0] <= t; i++) if (this.events[i][1] === FULL) from = i;
    this.pos = from;
    this.advance(t);
  }

  /** Applies the events up to t; going backwards needs seek. */
  advance(t: number) {
    while (this.pos < this.events.length && this.events[this.pos][0] <= t) {
      try {
        this.apply(this.events[this.pos]);
      } catch {
        // One damaged event should not stop the rest.
      }
      this.pos++;
    }
  }

  private get doc(): Document | null {
    return this.frame.contentDocument;
  }

  private apply([, type, data]: ReplayEvent) {
    const doc = this.doc;
    if (!doc) return;
    switch (type) {
      case FULL: {
        const d = data as Snapshot;
        this.nodes.clear();
        // Frame nodes are not instances of this window's Element, so check nodeType.
        const root = this.build(doc, d.node) as Element;
        if (root.nodeType !== 1) return;
        let head = root.querySelector("head");
        if (!head) {
          head = doc.createElement("head");
          root.insertBefore(head, root.firstChild);
        }
        const base = doc.createElement("base");
        base.setAttribute("href", d.href);
        head.insertBefore(base, head.firstChild);
        doc.replaceChild(root, doc.documentElement);
        this.viewport(d.w, d.h);
        doc.defaultView?.scrollTo(d.x, d.y);
        break;
      }
      case MUTATION: {
        const m = data as Mutation;
        for (const id of m.r ?? []) {
          const n = this.nodes.get(id);
          n?.parentNode?.removeChild(n);
          this.nodes.delete(id);
        }
        for (const a of m.a ?? []) {
          const parent = this.nodes.get(a.p);
          if (!parent) continue;
          const node = this.build(doc, a.s);
          const next = a.n == null ? null : this.nodes.get(a.n);
          parent.insertBefore(node, next && next.parentNode === parent ? next : null);
        }
        for (const a of m.at ?? []) {
          const el = this.nodes.get(a.i) as Element | undefined;
          if (el?.nodeType !== 1) continue;
          if (a.v === null) el.removeAttribute(a.k);
          else this.setAttr(el, a.k, a.v);
        }
        for (const t of m.tx ?? []) {
          const n = this.nodes.get(t.i);
          if (n) n.textContent = t.x;
        }
        break;
      }
      case MOVE:
      case CLICK: {
        const [x, y] = data as [number, number];
        this.onPointer(x, y, type === CLICK);
        break;
      }
      case SCROLL: {
        const [id, x, y] = data as [number, number, number];
        const n = this.nodes.get(id);
        if (n === doc.documentElement) doc.defaultView?.scrollTo(x, y);
        else if (n?.nodeType === 1) {
          (n as Element).scrollLeft = x;
          (n as Element).scrollTop = y;
        }
        break;
      }
      case INPUT: {
        const d = data as { i: number; v?: string; c?: boolean; si?: number };
        const el = this.nodes.get(d.i) as HTMLInputElement & HTMLSelectElement;
        if (!el) break;
        if (d.v !== undefined) el.value = d.v;
        if (d.c !== undefined) el.checked = d.c;
        if (d.si !== undefined) el.selectedIndex = d.si;
        break;
      }
      case VIEWPORT: {
        const [w, h] = data as [number, number];
        this.viewport(w, h);
        break;
      }
    }
  }

  private viewport(w: number, h: number) {
    if (!(w > 0 && h > 0)) return;
    this.width = w;
    this.height = h;
    this.onViewport(w, h);
  }

  private setAttr(el: Element, k: string, v: string) {
    if (!safeAttr(k, v)) return;
    try {
      el.setAttribute(k, v);
    } catch {
      // Not a valid attribute name here.
    }
    if (k === "value" && "value" in el) (el as HTMLInputElement).value = v;
  }

  private build(doc: Document, s: SNode): Node {
    let n: Node;
    if (s.t === 3) n = doc.createTextNode(s.x ?? "");
    else if (s.t !== 1 || !s.n) n = doc.createComment("");
    else {
      const tag = s.n.toLowerCase();
      const meta = tag === "meta" && s.a && Object.keys(s.a).some((k) => k.toLowerCase() === "http-equiv");
      let el: Element;
      try {
        el = s.s ? doc.createElementNS(SVG_NS, s.n) : doc.createElement(UNSAFE_TAGS.has(tag) || meta ? "div" : tag);
      } catch {
        el = doc.createElement("div");
      }
      if (s.b) {
        el.setAttribute("style", `${BLOCKED_STYLE} width: ${s.b[0]}px; height: ${s.b[1]}px;`);
      } else {
        for (const [k, v] of Object.entries(s.a ?? {})) this.setAttr(el, k, v);
        if (s.a && "checked" in s.a) (el as HTMLInputElement).checked = true;
        if (UNSAFE_TAGS.has(tag)) el.setAttribute("hidden", "");
        for (const c of s.c ?? []) el.appendChild(this.build(doc, c));
      }
      n = el;
    }
    this.nodes.set(s.i, n);
    return n;
  }
}

/** Splits [start, end] into the stretches with something to watch, for skipping the rest. */
export function idleGaps(events: ReplayEvent[], minGap = 5000): [number, number][] {
  const out: [number, number][] = [];
  let last = events[0]?.[0] ?? 0;
  for (const e of events) {
    if (!USER_EVENTS.has(e[1])) continue;
    if (e[0] - last > minGap) out.push([last + 1000, e[0] - 500]);
    last = e[0];
  }
  return out;
}
