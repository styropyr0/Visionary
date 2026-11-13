import { useMemo } from "react";
import { ChevronRight } from "lucide-react";
import type { Result } from "../lib/api";
import { formatBytes, formatCount, toNumber } from "../lib/format";
import { formatFull, formatRelative } from "../lib/time";
import { Section } from "../components/Section";

/**
 * What fills each disk, from the agent's storage scan: the biggest top-level
 * entries of every mount and what is inside the largest of them, and what the
 * container engine's images, containers, volumes and build cache take.
 */

interface Item {
  mount: string;
  kind: string;
  path: string;
  bytes: number;
  files: number | null;
}

interface Part {
  key: string;
  label: string;
  bytes: number;
  color: string;
  files?: number | null;
  children?: Item[];
}

const COLORS = Array.from({ length: 7 }, (_, i) => `var(--series-${i + 1})`);
const OTHER = "var(--text-3)";

function rows(r: Result | undefined): Record<string, unknown>[] {
  if (!r) return [];
  return r.rows.map((row) => Object.fromEntries(r.columns.map((c, i) => [c, row[i]])));
}

export function MachineStorage({ storage, mounts }: { storage?: Result; mounts: Result }) {
  const items = useMemo<Item[]>(
    () =>
      rows(storage).map((r) => ({
        mount: String(r.mount ?? ""),
        kind: String(r.kind),
        path: String(r.path),
        bytes: toNumber(r.bytes) ?? 0,
        files: toNumber(r.files),
      })),
    [storage],
  );
  const first = rows(storage)[0];
  const text =
    "What fills each disk, from the agent's storage scan. It runs every 6 hours at the lowest CPU and disk priority, paced and within a time budget; set VISIONARY_AGENT_STORAGE_SCAN to change how often, or off. Container engine data is measured by the engine, not walked.";
  if (!storage) return null;
  if (items.length === 0) {
    return (
      <>
        <Section title="Storage" text={text} />
        <div className="span-12 panel-state storage-empty">No scan yet. The first runs a few minutes after the agent starts; agents before this version don't scan.</div>
      </>
    );
  }
  const at = String(first?.created_at ?? "");
  const partial = toNumber(first?.partial) === 1;
  const used = new Map(
    rows(mounts).map((m) => [String(m.mount), ((toNumber(m.total_gb) ?? 0) * (toNumber(m.used_percent) ?? 0) * 1073741824) / 100] as const),
  );
  const byMount = new Map<string, Item[]>();
  for (const it of items) {
    if (it.kind !== "dir") continue;
    byMount.set(it.mount, [...(byMount.get(it.mount) ?? []), it]);
  }
  const engine = items.filter((it) => it.kind.startsWith("docker_"));
  const cards = byMount.size + (engine.length > 0 ? 1 : 0);
  const span = cards > 1 ? "span-6" : "span-12";

  return (
    <>
      <Section title="Storage" text={text} />
      <p className="span-12 storage-meta dim">
        <span title={formatFull(new Date(at))}>Scanned {formatRelative(at)}</span>
        {partial && <span className="flag warn">Stopped at its budget: sizes are at least these</span>}
      </p>
      {[...byMount.entries()].map(([mount, list]) => (
        <MountCard key={mount} mount={mount} items={list} used={used.get(mount) ?? 0} className={span} />
      ))}
      {engine.length > 0 && <EngineCard items={engine} className={span} />}
    </>
  );
}

function depth(mount: string, path: string): number {
  const rel = path.slice(mount.replace(/\/$/, "").length).replace(/^\//, "");
  return rel ? rel.split("/").length : 0;
}

function MountCard({ mount, items, used, className }: { mount: string; items: Item[]; used: number; className: string }) {
  const top = items.filter((it) => depth(mount, it.path) === 1);
  const parts: Part[] = top.slice(0, COLORS.length).map((it, i) => ({
    key: it.path,
    label: it.path,
    bytes: it.bytes,
    files: it.files,
    color: COLORS[i],
    children: items.filter((c) => depth(mount, c.path) === 2 && c.path.startsWith(it.path + "/")),
  }));
  const listed = parts.reduce((n, p) => n + p.bytes, 0);
  const total = Math.max(used, listed);
  if (total - listed > total * 0.005) {
    parts.push({
      key: "other",
      label: top.length > COLORS.length ? "Everything else" : "Not walked or not attributed",
      bytes: total - listed,
      color: OTHER,
    });
  }
  return (
    <section className={`panel storage-card ${className}`}>
      <header className="storage-head">
        <h3 className="panel-title mono">{mount}</h3>
        <span className="dim">{used ? `${formatBytes(used)} used` : `${formatBytes(listed)} found`}</span>
      </header>
      <StackBar parts={parts} total={total} />
      <PartList parts={parts} total={total} />
    </section>
  );
}

function EngineCard({ items, className }: { items: Item[]; className: string }) {
  const sum = (kind: string) => items.filter((it) => it.kind === kind).reduce((n, it) => n + it.bytes, 0);
  const of = (kind: string) => items.filter((it) => it.kind === kind);
  const parts: Part[] = [
    { key: "images", label: "Images", bytes: sum("docker_images"), color: COLORS[0], children: of("docker_image") },
    { key: "containers", label: "Container writable layers", bytes: sum("docker_container"), color: COLORS[1], children: of("docker_container") },
    { key: "volumes", label: "Volumes", bytes: sum("docker_volume"), color: COLORS[2], children: of("docker_volume") },
    { key: "cache", label: "Build cache", bytes: sum("docker_build_cache"), color: COLORS[3] },
  ].filter((p) => p.bytes > 0);
  const total = parts.reduce((n, p) => n + p.bytes, 0);
  return (
    <section className={`panel storage-card ${className}`}>
      <header className="storage-head">
        <h3 className="panel-title">Container engine</h3>
        <span className="dim">{formatBytes(total)}</span>
      </header>
      <StackBar parts={parts} total={total} />
      <PartList parts={parts} total={total} childLabel={(it) => it.path} />
    </section>
  );
}

function StackBar({ parts, total }: { parts: Part[]; total: number }) {
  if (total <= 0) return null;
  return (
    <div className="storage-bar" role="img" aria-label={parts.map((p) => `${p.label} ${formatBytes(p.bytes)}`).join(", ")}>
      {parts.map((p) => (
        <span key={p.key} style={{ flexGrow: p.bytes, background: p.color }} title={`${p.label}: ${formatBytes(p.bytes)} (${share(p.bytes, total)})`} />
      ))}
    </div>
  );
}

function PartList({ parts, total, childLabel }: { parts: Part[]; total: number; childLabel?: (it: Item) => string }) {
  return (
    <ul className="storage-list">
      {parts.map((p) => {
        const row = (
          <>
            <span className="storage-swatch" style={{ background: p.color }} aria-hidden />
            <span className="storage-name mono">{p.label}</span>
            {p.files ? <span className="dim storage-files">{formatCount(p.files)} files</span> : null}
            <span className="storage-size">{formatBytes(p.bytes)}</span>
            <span className="dim storage-share">{share(p.bytes, total)}</span>
          </>
        );
        if (!p.children?.length) {
          return (
            <li key={p.key}>
              <div className="storage-row">
                <span className="storage-chev" aria-hidden />
                {row}
              </div>
            </li>
          );
        }
        return (
          <li key={p.key}>
            <details>
              <summary className="storage-row">
                <ChevronRight size={14} className="storage-chev" aria-hidden />
                {row}
              </summary>
              <ul className="storage-children">
                {p.children.map((c) => (
                  <li key={c.path} className="storage-row">
                    <span className="storage-name mono">{childLabel ? childLabel(c) : c.path.split("/").pop()}</span>
                    {c.files ? <span className="dim storage-files">{formatCount(c.files)} files</span> : null}
                    <span className="storage-size">{formatBytes(c.bytes)}</span>
                    <span className="dim storage-share">{share(c.bytes, p.bytes)}</span>
                  </li>
                ))}
              </ul>
            </details>
          </li>
        );
      })}
    </ul>
  );
}

function share(n: number, total: number): string {
  if (total <= 0) return "";
  const v = (100 * n) / total;
  return v < 1 ? "<1%" : `${v.toFixed(0)}%`;
}
