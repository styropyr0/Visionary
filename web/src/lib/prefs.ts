import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { RangeKey } from "./time";

/**
 * Each person's own preferences, kept on the server (so they follow you to
 * another browser) with a copy in localStorage (so the first paint already
 * uses them). Settings → Preferences edits them; usePref reads and writes
 * single values from anywhere.
 */
export interface Preferences {
  /** The time range a visit starts with; "last" keeps the one used last. */
  range?: "last" | RangeKey;
  /** Auto-refresh a visit starts with, in ms; "last" keeps the last one. */
  autoRefresh?: "last" | number;
  theme?: "dark" | "light" | "system";
  /** The page "/" opens; empty for API overview. */
  startPage?: string;
  /** Show times in UTC instead of this device's zone. */
  utc?: boolean;
  /** 12- or 24-hour clock; "auto" follows the language. */
  clock?: "auto" | "12" | "24";
  /** Performance → Slowest requests: routes left out, and grouping. */
  slowExclude?: string[];
  slowGroup?: boolean;
  /** Help: the language shown first. */
  helpLang?: string;
}

const KEY = "va.prefs";

// Its own storage helpers: lib/state reads preferences at startup, so this
// module must not import it.
function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function save(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Private mode or blocked storage: keep the in-memory value only.
  }
}

let current: Preferences = load<Preferences>(KEY, {});
const listeners = new Set<(p: Preferences) => void>();
let timer: number | undefined;

export function prefs(): Preferences {
  return current;
}

function publish(p: Preferences) {
  current = p;
  save(KEY, p);
  listeners.forEach((l) => l(p));
}

/** Changes some preferences, saved to the server a moment later. */
export function setPrefs(patch: Partial<Preferences>) {
  publish({ ...current, ...patch });
  window.clearTimeout(timer);
  timer = window.setTimeout(() => {
    void api("/api/me/preferences", { method: "PUT", json: current }).catch(() => undefined);
  }, 400);
}

/**
 * Loads the server's copy after sign-in. Returns true when the time zone or
 * clock changed, which needs a reload to redraw every time.
 */
export async function loadPrefs(): Promise<boolean> {
  try {
    const res = await api<{ preferences: Preferences }>("/api/me/preferences");
    const server = res.preferences ?? {};
    // A first visit after this feature: keep what this browser had.
    if (Object.keys(server).length === 0) {
      if (Object.keys(current).length > 0) setPrefs({});
      return false;
    }
    const redraw = (server.utc ?? false) !== (current.utc ?? false) || (server.clock ?? "auto") !== (current.clock ?? "auto");
    publish(server);
    return redraw;
  } catch {
    return false;
  }
}

/** One preference, with a setter that saves it. */
export function usePref<K extends keyof Preferences>(key: K, fallback: NonNullable<Preferences[K]>): [NonNullable<Preferences[K]>, (v: NonNullable<Preferences[K]>) => void] {
  const [value, setValue] = useState(() => (current[key] ?? fallback) as NonNullable<Preferences[K]>);
  useEffect(() => {
    const l = (p: Preferences) => setValue((p[key] ?? fallback) as NonNullable<Preferences[K]>);
    listeners.add(l);
    return () => {
      listeners.delete(l);
    };
    // fallback is a constant per call site.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  const set = useCallback((v: NonNullable<Preferences[K]>) => setPrefs({ [key]: v } as Partial<Preferences>), [key]);
  return [value, set];
}

/** All preferences, for the Settings tab. */
export function usePrefs(): Preferences {
  const [p, setP] = useState(current);
  useEffect(() => {
    listeners.add(setP);
    return () => {
      listeners.delete(setP);
    };
  }, []);
  return p;
}
