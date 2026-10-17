import { useEffect, useReducer } from "react";
import { api, rangeQuery } from "../../lib/api";
import { useApp } from "../../lib/state";
import type { Release } from "../../lib/telemetry";

// Deploys in the dashboard range, shared by every chart on the page: one
// request per range (and refresh), however many charts draw markers.
let current: { key: string; releases: Release[] | null } = { key: "", releases: null };
let lastGood: Release[] = [];
const listeners = new Set<() => void>();

function load(key: string, from: Date, to: Date) {
  if (current.key === key) return;
  current = { key, releases: null };
  api<{ releases: Release[] }>(`/api/telemetry/releases?${rangeQuery(from, to)}`)
    .then((r) => r.releases ?? [])
    // A server without telemetry, or a failed request, just draws no markers.
    .catch(() => [] as Release[])
    .then((releases) => {
      if (current.key !== key) return;
      current = { key, releases };
      lastGood = releases;
      listeners.forEach((l) => l());
    });
}

/** Deploys inside the current range; the previous range's while loading. */
export function useDeploys(): Release[] {
  const { resolved } = useApp();
  const [, rerender] = useReducer((n: number) => n + 1, 0);
  const key = `${resolved.from.toISOString()}|${resolved.to.toISOString()}`;
  useEffect(() => {
    listeners.add(rerender);
    return () => {
      listeners.delete(rerender);
    };
  }, []);
  useEffect(() => load(key, resolved.from, resolved.to), [key, resolved]);
  return current.key === key && current.releases ? current.releases : lastGood;
}
