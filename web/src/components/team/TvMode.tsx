import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Minimize2 } from "lucide-react";
import { load, save } from "../../lib/state";
import { dashboardLink, type Dashboard } from "../../lib/team";
import { Select } from "../Select";

interface Prefs {
  refresh: number;
  cycle: number;
}

const REFRESH = [
  { value: "15000", label: "Refresh every 15s" },
  { value: "30000", label: "Refresh every 30s" },
  { value: "60000", label: "Refresh every 1m" },
  { value: "300000", label: "Refresh every 5m" },
];

const CYCLE = [
  { value: "0", label: "Stay on this dashboard" },
  { value: "30000", label: "Next dashboard every 30s" },
  { value: "60000", label: "Next dashboard every 1m" },
  { value: "120000", label: "Next dashboard every 2m" },
  { value: "300000", label: "Next dashboard every 5m" },
];

const clockFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });

/**
 * Kiosk view for wall screens: full screen, no chrome, refreshes on its own
 * and can move through every dashboard in turn.
 */
export function TvMode({
  dash,
  all,
  rangeText,
  variables,
  onTick,
  onExit,
  children,
}: {
  dash: Dashboard;
  all: Dashboard[];
  rangeText: string;
  variables?: ReactNode;
  onTick: () => void;
  onExit: () => void;
  children: ReactNode;
}) {
  const [prefs, setPrefs] = useState<Prefs>(() => load("va.tv", { refresh: 30000, cycle: 0 }));
  const [now, setNow] = useState(new Date());

  const update = (p: Partial<Prefs>) => {
    const next = { ...prefs, ...p };
    setPrefs(next);
    save("va.tv", next);
  };

  useEffect(() => {
    document.documentElement.requestFullscreen?.().catch(() => undefined);
    return () => {
      if (document.fullscreenElement) document.exitFullscreen().catch(() => undefined);
    };
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onExit();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onExit]);

  useEffect(() => {
    const id = window.setInterval(() => {
      setNow(new Date());
      onTick();
    }, prefs.refresh);
    return () => window.clearInterval(id);
  }, [prefs.refresh, onTick]);

  useEffect(() => {
    const clock = window.setInterval(() => setNow(new Date()), 15_000);
    return () => window.clearInterval(clock);
  }, []);

  useEffect(() => {
    if (!prefs.cycle || all.length < 2) return;
    const i = all.findIndex((d) => d.id === dash.id);
    const next = all[(i + 1) % all.length];
    const id = window.setTimeout(() => {
      window.location.hash = `${dashboardLink(next.id)}?tv=1`;
    }, prefs.cycle);
    return () => window.clearTimeout(id);
  }, [prefs.cycle, all, dash.id]);

  const position = all.length > 1 ? `${all.findIndex((d) => d.id === dash.id) + 1} of ${all.length}` : "";

  return createPortal(
    <div className="tv" role="dialog" aria-label={`${dash.name}, TV mode`}>
      <header className="tv-head">
        <div className="tv-title">
          <h1>{dash.name}</h1>
          <span className="dim">
            {rangeText}
            {prefs.cycle > 0 && position && ` · ${position}`}
          </span>
        </div>
        {variables}
        <div className="tv-actions">
          <Select label="Refresh" value={String(prefs.refresh)} options={REFRESH} onChange={(v) => update({ refresh: Number(v) })} />
          <Select label="Cycle" value={String(prefs.cycle)} options={CYCLE} onChange={(v) => update({ cycle: Number(v) })} />
          <span className="tv-clock">{clockFmt.format(now)}</span>
          <button className="btn" onClick={onExit} title="Leave TV mode (Esc)">
            <Minimize2 size={14} />
            Exit
          </button>
        </div>
      </header>
      <div className="tv-body">{children}</div>
    </div>,
    document.body,
  );
}
