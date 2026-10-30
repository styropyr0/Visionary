import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { AlertTriangle } from "lucide-react";
import { FloatingMenu } from "./Floating";
import { usePopover } from "./Toolbar";
import { PROM_AGGREGATIONS, PROM_FUNCTIONS, PROM_KEYWORDS, completionAt, errorPosition, lineAt, promLabels, usePromNames, type Completion } from "../lib/promql";
import "./PromQLEditor.css";

interface Suggestion {
  text: string;
  /** What is inserted; a function gets its opening parenthesis. */
  insert: string;
  hint: string;
}

const MAX_SHOWN = 40;

/**
 * A PromQL text box that completes metric names, functions, label names and
 * label values from the server, runs on Ctrl+Enter, and points at the
 * character an error names.
 */
export function PromQLEditor({
  value,
  onChange,
  onRun,
  error,
  rows = 3,
  placeholder = 'sum by (route) (rate(visionary_http_requests_total[5m]))',
  label = "PromQL query",
}: {
  value: string;
  onChange: (v: string) => void;
  onRun?: () => void;
  error?: Error;
  rows?: number;
  placeholder?: string;
  label?: string;
}) {
  const { open, setOpen, ref, menuRef } = usePopover();
  const area = useRef<HTMLTextAreaElement>(null);
  const names = usePromNames();
  const [comp, setComp] = useState<Completion | null>(null);
  const [labels, setLabels] = useState<string[]>([]);
  const [active, setActive] = useState(0);
  const typed = useRef(value);

  // A value set from outside (an example, a link) closes the suggestions.
  useEffect(() => {
    if (value !== typed.current) {
      typed.current = value;
      setOpen(false);
    }
  }, [value, setOpen]);

  // Label names and values come from the server for the metric before the {.
  useEffect(() => {
    if (!comp || comp.kind === "name") return;
    let live = true;
    promLabels(comp.metric, comp.kind === "value" ? comp.label : undefined).then((l) => live && setLabels(l));
    return () => {
      live = false;
    };
  }, [comp?.kind, comp && comp.kind !== "name" ? comp.metric : "", comp?.kind === "value" ? comp.label : ""]);

  const suggestions = useMemo<Suggestion[]>(() => {
    if (!comp) return [];
    const p = comp.prefix.toLowerCase();
    const starts = (s: string) => s.toLowerCase().startsWith(p) && s !== comp.prefix;
    if (comp.kind === "name") {
      const meta = names.data ?? {};
      const metrics = Object.keys(meta)
        .filter((n) => n.toLowerCase().includes(p) && n !== comp.prefix)
        .sort((a, b) => Number(!a.toLowerCase().startsWith(p)) - Number(!b.toLowerCase().startsWith(p)) || a.localeCompare(b))
        .map((n) => ({ text: n, insert: n, hint: meta[n]?.[0]?.type ?? "" }));
      const fns = [...PROM_AGGREGATIONS, ...PROM_FUNCTIONS].filter(starts).map((f) => ({ text: f, insert: `${f}(`, hint: "function" }));
      const kws = PROM_KEYWORDS.filter(starts).map((k) => ({ text: k, insert: `${k} `, hint: "keyword" }));
      return [...fns, ...metrics, ...kws].slice(0, MAX_SHOWN);
    }
    if (comp.kind === "label") {
      return labels
        .filter((l) => l !== "__name__" && starts(l))
        .slice(0, MAX_SHOWN)
        .map((l) => ({ text: l, insert: `${l}="`, hint: "label" }));
    }
    return labels
      .filter((v) => v.toLowerCase().includes(p) && v !== comp.prefix)
      .slice(0, MAX_SHOWN)
      .map((v) => ({ text: v, insert: `${v.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`, hint: comp.label }));
  }, [comp, names.data, labels]);

  const track = (text: string, cursor: number) => {
    const c = completionAt(text, cursor);
    setComp(c);
    setActive(0);
    setOpen(!!c && (c.kind !== "name" || c.prefix.length > 0));
  };

  const pick = (sg: Suggestion) => {
    const el = area.current;
    if (!el || !comp) return;
    const cursor = el.selectionStart;
    // A value's closing quote may already be there.
    let end = cursor;
    if (comp.kind === "value" && value[end] === '"') end++;
    const next = value.slice(0, comp.from) + sg.insert + value.slice(end);
    const at = comp.from + sg.insert.length;
    typed.current = next;
    onChange(next);
    setOpen(false);
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(at, at);
      track(next, at);
    });
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      setOpen(false);
      onRun?.();
      return;
    }
    const shown = open && suggestions.length > 0;
    if (shown && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((i) => (i + step + suggestions.length) % suggestions.length);
    } else if (shown && (e.key === "Tab" || e.key === "Enter")) {
      e.preventDefault();
      pick(suggestions[active] ?? suggestions[0]);
    } else if (e.key === "Escape") {
      setOpen(false);
    }
  };

  const pos = errorPosition(error);
  const where = pos !== null ? lineAt(value, pos) : null;
  const goTo = () => {
    const el = area.current;
    if (!el || pos === null) return;
    el.focus();
    el.setSelectionRange(pos, pos);
  };

  return (
    <div className="pq-editor">
      <div className="popover-anchor" ref={ref}>
        <textarea
          ref={area}
          className={`textarea code pq-input ${error ? "invalid" : ""}`}
          rows={rows}
          value={value}
          placeholder={placeholder}
          aria-label={label}
          aria-invalid={!!error || undefined}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => {
            typed.current = e.target.value;
            onChange(e.target.value);
            track(e.target.value, e.target.selectionStart);
          }}
          onClick={(e) => track(value, e.currentTarget.selectionStart)}
          onKeyDown={onKey}
          onBlur={() => setOpen(false)}
        />
        {open && suggestions.length > 0 && (
          <FloatingMenu anchor={ref} menuRef={menuRef} matchWidth className="menu-scroll combobox-menu pq-menu" role="listbox">
            {suggestions.map((sg, i) => (
              <button
                key={sg.text}
                type="button"
                role="option"
                aria-selected={i === active}
                className={`menu-row ${i === active ? "active" : ""}`}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => pick(sg)}
              >
                <span className="menu-label mono">{sg.text}</span>
                <span className="pq-hint">{sg.hint}</span>
              </button>
            ))}
          </FloatingMenu>
        )}
      </div>
      {error && (
        <div className="pq-error" role="alert">
          <AlertTriangle size={14} />
          <div className="pq-error-body">
            <span>{error.message}</span>
            {where && (
              <button type="button" className="pq-where mono" onClick={goTo} title="Put the cursor there">
                <span className="pq-where-line">{where.line || " "}</span>
                <span className="pq-caret" aria-hidden>
                  {" ".repeat(Math.max(where.col, 0))}^
                </span>
              </button>
            )}
          </div>
        </div>
      )}
      <p className="pq-hint-line dim">
        <kbd className="kbd">Tab</kbd> completes
        {onRun && (
          <>
            {" · "}
            <kbd className="kbd">Ctrl</kbd>+<kbd className="kbd">Enter</kbd> runs
          </>
        )}
      </p>
    </div>
  );
}
