import { useMemo, useState } from "react";
import { X } from "lucide-react";
import { formatCount, formatPercent } from "../lib/format";
import type { FlameNode } from "../lib/insights";

const ROW = 18;
/** Frames narrower than this share of the view are not drawn. */
const MIN_WIDTH = 0.002;

type Category = "app" | "lib" | "std" | "vm";

/**
 * Whose code a frame is, from its "fn file:line" name (sdks/SPEC.md): the
 * SDKs prefix third-party files with lib: (Node: npm:) and the language's
 * own with std: (Node: node:); a frame with no file is native or runtime.
 */
function category(name: string): Category {
  if (name.startsWith("(")) return "vm"; // (garbage collector), (program), (other)
  const space = name.lastIndexOf(" ");
  if (space < 0) return "vm";
  const file = name.slice(space + 1);
  if (file.startsWith("std:") || file.startsWith("node:")) return "std";
  if (file.startsWith("lib:") || file.startsWith("npm:")) return "lib";
  if (!file.includes(".") && !file.includes("/")) return "vm";
  return "app";
}

const CATEGORY_LABEL: Record<Category, string> = { app: "Your code", lib: "Libraries", std: "Standard library", vm: "Runtime, native and GC" };

interface Bar {
  node: FlameNode;
  depth: number;
  x: number; // share of the view, 0..1
  w: number;
  path: number[];
}

function layout(root: FlameNode, path: number[]): Bar[] {
  const out: Bar[] = [];
  const total = root.value || 1;
  const walk = (n: FlameNode, depth: number, x: number, p: number[]) => {
    const w = n.value / total;
    if (w < MIN_WIDTH) return;
    out.push({ node: n, depth, x, w, path: p });
    let cx = x;
    (n.children ?? []).forEach((c, i) => {
      walk(c, depth + 1, cx, [...p, i]);
      cx += c.value / total;
    });
  };
  walk(root, 0, 0, path);
  return out;
}

function nodeAt(root: FlameNode, path: number[]): FlameNode {
  let n = root;
  for (const i of path) n = n.children?.[i] ?? n;
  return n;
}

/** The heaviest functions by their own (self) samples across the tree. */
function topSelf(root: FlameNode, limit = 25): { name: string; self: number }[] {
  const by = new Map<string, number>();
  const walk = (n: FlameNode) => {
    if (n.self > 0 && n.name !== "all") by.set(n.name, (by.get(n.name) ?? 0) + n.self);
    n.children?.forEach(walk);
  };
  walk(root);
  return [...by].map(([name, self]) => ({ name, self })).sort((a, b) => b.self - a.self).slice(0, limit);
}

/**
 * A top-down flame graph: callers above, the functions they call below, each
 * as wide as its share of CPU samples. Click a frame to zoom into it.
 */
export function FlameGraph({ root }: { root: FlameNode }) {
  const [zoom, setZoom] = useState<number[]>([]);
  const [search, setSearch] = useState("");
  const view = nodeAt(root, zoom);
  const bars = useMemo(() => layout(view, zoom), [view, zoom]);
  const depth = bars.reduce((d, b) => Math.max(d, b.depth), 0) + 1;
  const total = root.value || 1;
  const needle = search.trim().toLowerCase();
  const matched = useMemo(() => {
    if (!needle) return 0;
    let n = 0;
    const walk = (x: FlameNode) => {
      if (x.name.toLowerCase().includes(needle)) n += x.value;
      else x.children?.forEach(walk);
    };
    walk(root);
    return n;
  }, [root, needle]);
  const top = useMemo(() => topSelf(root), [root]);

  // The frames from the top down to the zoomed one, for the breadcrumb.
  const trail: { name: string; path: number[] }[] = [];
  for (let i = 0; i <= zoom.length; i++) trail.push({ name: nodeAt(root, zoom.slice(0, i)).name, path: zoom.slice(0, i) });

  return (
    <div className="flame">
      <div className="flame-bar">
        <div className="flame-trail">
          {trail.map((t, i) => (
            <span key={i}>
              {i > 0 && <span className="dim"> › </span>}
              <button type="button" className="linkish mono" onClick={() => setZoom(t.path)} disabled={i === trail.length - 1}>
                {i === 0 ? "All" : t.name.split(" ")[0]}
              </button>
            </span>
          ))}
        </div>
        <div className="flame-search">
          <input className="input" placeholder="Find a function or file" value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Find in the flame graph" />
          {search && (
            <button type="button" className="icon-btn" title="Clear" aria-label="Clear search" onClick={() => setSearch("")}>
              <X size={13} />
            </button>
          )}
          {needle && <span className="dim">{formatPercent((100 * matched) / total)} of samples</span>}
        </div>
      </div>
      <div className="flame-legend">
        {(Object.keys(CATEGORY_LABEL) as Category[]).map((c) => (
          <span key={c}>
            <i className={`flame-swatch ${c}`} /> {CATEGORY_LABEL[c]}
          </span>
        ))}
      </div>
      <div className="flame-canvas" style={{ height: depth * ROW }} role="img" aria-label="Flame graph of CPU samples">
        {bars.map((b) => {
          const n = b.node;
          const hit = needle && n.name.toLowerCase().includes(needle);
          const cat = b.depth === 0 && zoom.length === 0 ? "vm" : category(n.name);
          return (
            <button
              type="button"
              key={b.path.join(".") + ":" + b.depth}
              className={`flame-frame ${cat} ${needle ? (hit ? "hit" : "dimmed") : ""}`}
              style={{ left: `${b.x * 100}%`, width: `${b.w * 100}%`, top: b.depth * ROW }}
              title={`${n.name}\n${formatCount(n.value)} samples, ${formatPercent((100 * n.value) / total)} of all · ${formatCount(n.self)} in the function itself`}
              onClick={() => setZoom(b.path)}
            >
              {b.w > 0.04 ? (b.depth === 0 && zoom.length === 0 ? `All samples (${formatCount(n.value)})` : n.name) : ""}
            </button>
          );
        })}
      </div>
      <div className="flame-top">
        <div className="rt-title">Where the CPU time is spent</div>
        <table className="data">
          <thead>
            <tr>
              <th>Function</th>
              <th className="num" style={{ width: 90 }}>
                Self
              </th>
              <th className="num" style={{ width: 80 }}>
                Share
              </th>
            </tr>
          </thead>
          <tbody>
            {top.map((t) => (
              <tr key={t.name} className="clickable" onClick={() => setSearch(t.name.split(" ")[0] ?? t.name)}>
                <td className="mono">
                  <i className={`flame-swatch ${category(t.name)}`} /> {t.name}
                </td>
                <td className="num">{formatCount(t.self)}</td>
                <td className="num">{formatPercent((100 * t.self) / total)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
