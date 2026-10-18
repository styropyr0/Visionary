import { Fragment, useMemo, useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react";
import type { Result } from "../../lib/api";
import { formatCount, formatMs, toNumber } from "../../lib/format";
import { N_PLUS_ONE, records } from "../../lib/telemetry";
import "./telemetry.css";

export interface SpanRow {
  id: unknown;
  kind: string;
  name: string;
  target: string;
  detail: string;
  offset: number;
  duration: number;
  status: string;
  error: string;
  rowCount: number | null;
}

/** Categorical slots in a fixed order, so a kind keeps its color everywhere. */
export const KIND_COLOR: Record<string, string> = {
  db: "var(--series-1)",
  http: "var(--series-2)",
  cache: "var(--series-3)",
  queue: "var(--series-4)",
  custom: "var(--series-7)",
};

const KIND_LABEL: Record<string, string> = { db: "Database", http: "HTTP", cache: "Cache", queue: "Queue", custom: "Custom" };

export function toSpans(res: Result | undefined): SpanRow[] {
  return records(res).map((r) => ({
    id: r.id,
    kind: String(r.kind ?? "custom"),
    name: String(r.name ?? ""),
    target: String(r.target ?? ""),
    detail: String(r.detail ?? ""),
    offset: toNumber(r.offset_ms) ?? 0,
    duration: toNumber(r.duration_ms) ?? 0,
    status: String(r.status ?? "ok"),
    error: String(r.error ?? ""),
    rowCount: toNumber(r.row_count),
  }));
}

/** Span names repeated at least N_PLUS_ONE times, most repeated first. */
export function repeatedNames(spans: SpanRow[]): { name: string; kind: string; count: number; ms: number }[] {
  const by = new Map<string, { name: string; kind: string; count: number; ms: number }>();
  for (const s of spans) {
    const e = by.get(s.name) ?? { name: s.name, kind: s.kind, count: 0, ms: 0 };
    e.count++;
    e.ms += s.duration;
    by.set(s.name, e);
  }
  return [...by.values()].filter((e) => e.count >= N_PLUS_ONE).sort((a, b) => b.count - a.count);
}

function ticks(total: number): number[] {
  return [0, 0.25, 0.5, 0.75, 1].map((f) => f * total);
}

/**
 * The calls a request made, on the request's own time axis: where each one
 * started (offset) and how long it took. Click a row for the statement or URL.
 */
export function Waterfall({ spans, requestMs }: { spans: SpanRow[]; requestMs: number }) {
  const [open, setOpen] = useState<Set<number>>(new Set());
  const total = useMemo(() => Math.max(requestMs, ...spans.map((s) => s.offset + s.duration), 1), [spans, requestMs]);
  const kinds = useMemo(() => [...new Set(spans.map((s) => s.kind))].sort((a, b) => Object.keys(KIND_COLOR).indexOf(a) - Object.keys(KIND_COLOR).indexOf(b)), [spans]);
  const repeated = useMemo(() => new Set(repeatedNames(spans).map((r) => r.name)), [spans]);

  const toggle = (i: number) =>
    setOpen((o) => {
      const n = new Set(o);
      if (n.has(i)) n.delete(i);
      else n.add(i);
      return n;
    });

  return (
    <div className="waterfall">
      <div className="wf-legend">
        {kinds.map((k) => (
          <span key={k} className="wf-legend-item">
            <span className="wf-swatch" style={{ background: KIND_COLOR[k] ?? KIND_COLOR.custom }} />
            {KIND_LABEL[k] ?? k}
          </span>
        ))}
        <span className="dim" style={{ marginLeft: "auto" }}>
          {formatCount(spans.length)} {spans.length === 1 ? "call" : "calls"} · request {formatMs(requestMs)}
        </span>
      </div>
      <div className="wf-grid wf-head" aria-hidden>
        <span />
        <div className="wf-axis">
          {ticks(total).map((t, i) => (
            <span key={i} style={{ left: `${(100 * t) / total}%` }} className={i === 4 ? "end" : i === 0 ? "start" : ""}>
              {t === 0 ? "0" : formatMs(t)}
            </span>
          ))}
        </div>
        <span />
      </div>
      <div className="wf-rows" role="list">
        {spans.map((s, i) => {
          const left = (100 * s.offset) / total;
          const width = Math.max((100 * s.duration) / total, 0.25);
          const isOpen = open.has(i);
          const failed = s.status === "error";
          return (
            <Fragment key={String(s.id ?? i)}>
              <button type="button" className={`wf-grid wf-row ${isOpen ? "open" : ""}`} role="listitem" onClick={() => toggle(i)} aria-expanded={isOpen}>
                <span className="wf-name" title={s.name}>
                  {isOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                  <span className="wf-swatch" style={{ background: KIND_COLOR[s.kind] ?? KIND_COLOR.custom }} />
                  <span className="wf-label mono">{s.name}</span>
                  {repeated.has(s.name) && <span className="wf-repeat" title={`Repeated ${N_PLUS_ONE}+ times in this request`}>N+1</span>}
                </span>
                <span className="wf-track">
                  {[25, 50, 75].map((g) => (
                    <span key={g} className="wf-gridline" style={{ left: `${g}%` }} />
                  ))}
                  <span
                    className={`wf-bar ${failed ? "failed" : ""}`}
                    style={{ left: `${Math.min(left, 100 - width)}%`, width: `${width}%`, background: KIND_COLOR[s.kind] ?? KIND_COLOR.custom }}
                  />
                </span>
                <span className="wf-dur">
                  {failed && (
                    <span className="wf-err" title={s.error || "error"}>
                      <AlertTriangle size={12} aria-label="Failed" />
                    </span>
                  )}
                  {formatMs(s.duration)}
                </span>
              </button>
              {isOpen && (
                <div className="wf-detail">
                  <dl className="kv wf-kv">
                    <dt>Started</dt>
                    <dd>+{formatMs(s.offset)}</dd>
                    {s.target && (
                      <>
                        <dt>Target</dt>
                        <dd className="mono">{s.target}</dd>
                      </>
                    )}
                    <dt>Status</dt>
                    <dd>
                      {failed ? (
                        <span className="flag bad">
                          <AlertTriangle size={12} /> error
                        </span>
                      ) : (
                        "ok"
                      )}
                      {s.error && <span className="mono" style={{ marginLeft: 8 }}>{s.error}</span>}
                    </dd>
                    {s.rowCount !== null && (
                      <>
                        <dt>Rows</dt>
                        <dd>{formatCount(s.rowCount)}</dd>
                      </>
                    )}
                  </dl>
                  {s.detail && <pre className="code mono wf-sql">{s.detail}</pre>}
                </div>
              )}
            </Fragment>
          );
        })}
      </div>
    </div>
  );
}
