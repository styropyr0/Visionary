import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { DEFAULT_RANGE, resolveRange, type Resolved, type TimeRange } from "./time";
import { prefs, setPrefs } from "./prefs";

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

const deviceTheme = (): Theme => (window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark");

function initialTheme(): Theme {
  const mode = prefs().theme;
  const stored = load<Theme | null>("va.theme", null);
  const t = mode === "dark" || mode === "light" ? mode : mode === "system" ? deviceTheme() : stored === "dark" || stored === "light" ? stored : deviceTheme();
  applyTheme(t);
  return t;
}

// A visit starts with the preferred range and auto-refresh, or the last used.
function initialRange(): TimeRange {
  const p = prefs().range;
  return p && p !== "last" ? { kind: "relative", key: p } : load("va.range", DEFAULT_RANGE);
}

function initialAutoRefresh(): number {
  const p = prefs().autoRefresh;
  return typeof p === "number" ? p : load("va.auto", 0);
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [range, setRangeState] = useState<TimeRange>(initialRange);
  const [tick, setTick] = useState(0);
  const [autoRefresh, setAutoState] = useState<number>(initialAutoRefresh);
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
    // Picking a theme by hand ends "follow the device".
    if (prefs().theme === "system") setPrefs({ theme: t });
  }, []);

  // "Follow the device": change with the system's light or dark mode.
  useEffect(() => {
    const mq = window.matchMedia?.("(prefers-color-scheme: light)");
    if (!mq) return;
    const on = () => {
      if (prefs().theme !== "system") return;
      const t = deviceTheme();
      applyTheme(t);
      setThemeState(t);
    };
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
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
