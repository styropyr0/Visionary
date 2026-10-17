import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { DEFAULT_RANGE, resolveRange, type Resolved, type TimeRange } from "./time";

export type Theme = "dark" | "light";

interface AppState {
  range: TimeRange;
  setRange: (r: TimeRange) => void;
  /** The range pinned to absolute times; recomputed on every refresh. */
  resolved: Resolved;
  tick: number;
  refresh: () => void;
  autoRefresh: number;
  setAutoRefresh: (ms: number) => void;
  theme: Theme;
  setTheme: (t: Theme) => void;
}

const Ctx = createContext<AppState | null>(null);

// localStorage is a per-viewer convenience; every read and write tolerates it
// being unavailable.
function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

export function save(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Private mode or blocked storage: keep the in-memory value only.
  }
}

export { load };

// The attribute is set before React renders with the new theme, because charts
// read their colors from CSS during render.
function applyTheme(t: Theme) {
  document.documentElement.dataset.theme = t;
}

function initialTheme(): Theme {
  const stored = load<Theme | null>("va.theme", null);
  const t = stored === "dark" || stored === "light" ? stored : window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark";
  applyTheme(t);
  return t;
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [range, setRangeState] = useState<TimeRange>(() => load("va.range", DEFAULT_RANGE));
  const [tick, setTick] = useState(0);
  const [autoRefresh, setAutoState] = useState<number>(() => load("va.auto", 0));
  const [theme, setThemeState] = useState<Theme>(initialTheme);

  const resolved = useMemo(() => resolveRange(range), [range, tick]);

  const setRange = useCallback((r: TimeRange) => {
    setRangeState(r);
    save("va.range", r);
  }, []);

  const refresh = useCallback(() => setTick((t) => t + 1), []);

  const setAutoRefresh = useCallback((ms: number) => {
    setAutoState(ms);
    save("va.auto", ms);
  }, []);

  const setTheme = useCallback((t: Theme) => {
    applyTheme(t);
    setThemeState(t);
    save("va.theme", t);
  }, []);

  // Auto-refresh only makes sense for ranges that end at "now".
  useEffect(() => {
    if (!autoRefresh || range.kind !== "relative") return;
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") refresh();
    }, autoRefresh);
    return () => window.clearInterval(id);
  }, [autoRefresh, range, refresh]);

  const value = useMemo<AppState>(
    () => ({ range, setRange, resolved, tick, refresh, autoRefresh, setAutoRefresh, theme, setTheme }),
    [range, setRange, resolved, tick, refresh, autoRefresh, setAutoRefresh, theme, setTheme],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useApp(): AppState {
  const v = useContext(Ctx);
  if (!v) throw new Error("useApp outside AppProvider");
  return v;
}
