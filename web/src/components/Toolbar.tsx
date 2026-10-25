import { useEffect, useRef, useState, type ReactNode } from "react";
import { Check, ChevronDown, Clock, RefreshCw } from "lucide-react";
import { useApp } from "../lib/state";
import { HelpButton } from "./Help";
import { PRESETS, rangeLabel, toLocalInput } from "../lib/time";

export function usePopover() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  // A FloatingMenu lives in a portal, outside ref: clicks in it are inside too.
  const menuRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (ref.current && !ref.current.contains(t) && !menuRef.current?.contains(t)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return { open, setOpen, ref, menuRef };
}

/** Preset rows first; a custom range sits behind a hairline in the footer. */
export function TimeRangePicker() {
  const { range, setRange, resolved, refresh } = useApp();
  const { open, setOpen, ref } = usePopover();
  const [from, setFrom] = useState(toLocalInput(resolved.from));
  const [to, setTo] = useState(toLocalInput(resolved.to));
  const [err, setErr] = useState("");

  useEffect(() => {
    if (open) {
      setFrom(toLocalInput(resolved.from));
      setTo(toLocalInput(resolved.to));
      setErr("");
    }
  }, [open, resolved]);

  const apply = () => {
    const f = new Date(from);
    const t = new Date(to);
    if (Number.isNaN(f.getTime()) || Number.isNaN(t.getTime()) || t <= f) {
      setErr("End must be after start.");
      return;
    }
    setRange({ kind: "absolute", from: f.toISOString(), to: t.toISOString() });
    setOpen(false);
  };

  return (
    <div className="popover-anchor" ref={ref}>
      <button type="button" className="btn" onClick={() => setOpen(!open)} aria-haspopup="menu" aria-expanded={open}>
        <Clock size={14} />
        <span>{rangeLabel(range)}</span>
        <ChevronDown size={14} />
      </button>
      {open && (
        <div className="popover" role="menu" style={{ width: 280 }}>
          {PRESETS.map((p) => {
            const selected = range.kind === "relative" && range.key === p.key;
            return (
              <button type="button"
                key={p.key}
                role="menuitemradio"
                aria-checked={selected}
                className={`menu-row ${selected ? "selected" : ""}`}
                onClick={() => {
                  setRange({ kind: "relative", key: p.key });
                  refresh();
                  setOpen(false);
                }}
              >
                <span className="check">{selected && <Check size={16} strokeWidth={3} />}</span>
                {p.label}
              </button>
            );
          })}
          <div className="menu-sep" />
          <div className="menu-foot">
            <span className="section-label">Custom range</span>
            <label className="field">
              <span>From</span>
              <input className="input" type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
            </label>
            <label className="field">
              <span>To</span>
              <input className="input" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
            </label>
            {err && <span className="form-error">{err}</span>}
            <button type="button" className="btn primary sm" onClick={apply}>
              Apply range
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

const INTERVALS = [
  { ms: 0, label: "Off" },
  { ms: 30_000, label: "30s" },
  { ms: 60_000, label: "1m" },
  { ms: 300_000, label: "5m" },
  { ms: 900_000, label: "15m" },
];

export function RefreshControl({ busy }: { busy?: boolean }) {
  const { refresh, autoRefresh, setAutoRefresh, range } = useApp();
  const { open, setOpen, ref } = usePopover();
  const current = INTERVALS.find((i) => i.ms === autoRefresh) ?? INTERVALS[0];
  const autoAllowed = range.kind === "relative";

  return (
    <div className="btn-group popover-anchor" ref={ref}>
      <button type="button" className="btn" onClick={refresh} title="Refresh" aria-label="Refresh">
        <RefreshCw size={14} className={busy ? "spin" : undefined} />
      </button>
      <button type="button"
        className="btn"
        onClick={() => setOpen(!open)}
        title={autoAllowed ? "Auto-refresh" : "Auto-refresh needs a relative range"}
        disabled={!autoAllowed}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        {autoAllowed && autoRefresh ? current.label : "Off"}
        <ChevronDown size={14} />
      </button>
      {open && (
        <div className="popover" role="menu" style={{ minWidth: 160 }}>
          <div className="menu-title">Auto-refresh</div>
          {INTERVALS.map((i) => (
            <button type="button"
              key={i.ms}
              role="menuitemradio"
              aria-checked={i.ms === autoRefresh}
              className={`menu-row ${i.ms === autoRefresh ? "selected" : ""}`}
              onClick={() => {
                setAutoRefresh(i.ms);
                setOpen(false);
              }}
            >
              <span className="check">{i.ms === autoRefresh && <Check size={16} strokeWidth={3} />}</span>
              {i.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** help={false} drops the Help button, on pages that are help themselves. */
export function TopBar({ title, section, actions, help = true }: { title: string; section?: string; actions?: ReactNode; help?: boolean }) {
  return (
    <header className="topbar">
      <div className="crumbs">
        <span className="muted">Visionary</span>
        <span className="muted sep">/</span>
        {section && (
          <>
            <span className="muted">{section}</span>
            <span className="muted sep">/</span>
          </>
        )}
        <h1>{title}</h1>
      </div>
      <div className="topbar-actions">
        {actions}
        {help && <HelpButton />}
      </div>
    </header>
  );
}
