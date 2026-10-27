import { Database } from "lucide-react";

/** The Queries view with one statement's detail open. */
export function queryViewerHref(name: string, detail: string): string {
  return `#/queries?${new URLSearchParams({ name, detail })}`;
}

/** Reads the statement a link to the Queries view asks to open, if any. */
export function statementFromHash(): { name: string; detail: string } | null {
  const p = new URLSearchParams(window.location.hash.split("?")[1] ?? "");
  const name = p.get("name");
  return name ? { name, detail: p.get("detail") ?? "" } : null;
}

/** Opens a statement in the Queries view: its timeline, routes, slowest calls and plan. */
export function QueryViewerLink({ name, detail }: { name: string; detail: string }) {
  return (
    <a className="btn sm" href={queryViewerHref(name, detail)} onClick={(e) => e.stopPropagation()}>
      <Database size={13} aria-hidden /> Open in Query Viewer
    </a>
  );
}
