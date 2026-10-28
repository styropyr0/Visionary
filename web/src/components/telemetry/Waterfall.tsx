import { Fragment, useMemo, useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react";
import type { Result } from "../../lib/api";
import { formatCount, formatMs, titleCase, toNumber } from "../../lib/format";
import { N_PLUS_ONE, records } from "../../lib/telemetry";
import { SqlView } from "./SqlView";
import { QueryViewerLink } from "./QueryViewerLink";
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
  graphql: "var(--series-5)",
  background: "var(--series-6)",
};

const KIND_LABEL: Record<string, string> = { db: "Database", http: "HTTP", cache: "Cache", queue: "Queue", custom: "Custom", graphql: "GraphQL", background: "Background" };

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

export interface Gap {
  offset: number;
  duration: number;
  /** The call that ended just before the gap, if any. */
  after: string;
}

/** Shorter stretches are noise (scheduling, serialization between calls). */
const GAP_MIN_MS = 5;
const GAP_MIN_SHARE = 0.02;

/**
 * The calls the request waited for: not background work it started, nor the
 * calls made inside that work or after the response.
 */
export function waitedFor(spans: SpanRow[], requestMs: number): SpanRow[] {
  const bg = spans.filter((s) => s.kind === "background");
  const inside = (s: SpanRow) => bg.some((b) => b !== s && s.offset >= b.offset && s.offset + s.duration <= b.offset + b.duration);
  return spans.filter((s) => s.kind !== "background" && s.offset < requestMs && !inside(s));
}

/** Background work a request started, and the calls that ended after its response. */
export function afterResponse(spans: SpanRow[], requestMs: number): { background: SpanRow[]; late: number } {
  return {
    background: spans.filter((s) => s.kind === "background"),
    late: spans.filter((s) => s.kind !== "background" && s.offset + s.duration > requestMs + 0.5).length,
  };
}

/**
 * Stretches of the request that no recorded call covers: the app's own code
 * (CPU work, rendering) or calls the SDK cannot see. Overlapping and nested
 * calls (a query and its prepare) count once.
 */
export function untracked(spans: SpanRow[], requestMs: number): { gaps: Gap[]; ms: number } {
  const sorted = waitedFor(spans, requestMs).sort((a, b) => a.offset - b.offset);
  const raw: Gap[] = [];
  let at = 0;
  let after = "";
  for (const s of sorted) {
    if (s.offset > at) raw.push({ offset: at, duration: s.offset - at, after });
    if (s.offset + s.duration > at) {
      at = s.offset + s.duration;
      after = s.name;
    }
  }
  if (requestMs > at) raw.push({ offset: at, duration: requestMs - at, after });
  const ms = raw.reduce((a, g) => a + g.duration, 0);
  const min = Math.max(GAP_MIN_MS, GAP_MIN_SHARE * requestMs);
  return { gaps: raw.filter((g) => g.duration >= min), ms };
}

const GAP_HELP =
  "No recorded call covers this time. It is the app's own code (CPU work such as hashing, parsing or rendering, waiting on a lock) or a call the SDK cannot see: an HTTP client without the Visionary transport, a request made without the handler's context, or a library with no hook. Wrap it in a custom span to name it.";

function ticks(total: number): number[] {
  return [0, 0.25, 0.5, 0.75, 1].map((f) => f * total);
}

export interface LogMark {
  id: number;
  offset: number;
  level: string;
  message: string;
}

/**
 * The calls a request made, on the request's own time axis: where each one
 * started (offset) and how long it took. Click a row for the statement or URL.
 */
export function Waterfall({ spans, requestMs, logs = [], onOpenLog }: { spans: SpanRow[]; requestMs: number; logs?: LogMark[]; onOpenLog?: (id: number) => void }) {
  const [open, setOpen] = useState<Set<number>>(new Set());
  const total = useMemo(() => Math.max(requestMs, ...spans.map((s) => s.offset + s.duration), ...logs.map((l) => l.offset), 1), [spans, requestMs, logs]);
  const kinds = useMemo(() => [...new Set(spans.map((s) => s.kind))].sort((a, b) => Object.keys(KIND_COLOR).indexOf(a) - Object.keys(KIND_COLOR).indexOf(b)), [spans]);
  const repeated = useMemo(() => new Set(repeatedNames(spans).map((r) => r.name)), [spans]);
  // Calls and untracked stretches on one timeline, in start order.
  const rows = useMemo(() => {
    const items: ({ span: SpanRow; gap?: undefined; log?: undefined } | { gap: Gap; span?: undefined; log?: undefined } | { log: LogMark; span?: undefined; gap?: undefined })[] =
      spans.map((span) => ({ span }));
    for (const gap of untracked(spans, requestMs).gaps) items.push({ gap });
    for (const log of logs) items.push({ log });
    const at = (r: (typeof items)[number]) => (r.span ?? r.gap ?? r.log)!.offset;
    return items.sort((a, b) => at(a) - at(b) || (a.gap ? -1 : b.gap ? 1 : a.log ? -1 : 1));
  }, [spans, requestMs, logs]);
  const hasGaps = rows.some((r) => r.gap);
  // Work that outlived the response: mark where the response was sent.
  const responseAt = total > requestMs + 0.5 && requestMs > 0 ? (100 * requestMs) / total : null;
  const responseLine = responseAt !== null && <span className="wf-response" style={{ left: `${responseAt}%` }} aria-hidden />;

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
            {KIND_LABEL[k] ?? titleCase(k)}
          </span>
        ))}
        {hasGaps && (
          <span className="wf-legend-item">
            <span className="wf-swatch wf-gap-fill" />
            Untracked
          </span>
        )}
        {logs.length > 0 && (
          <span className="wf-legend-item">
            <span className="wf-log-swatch" aria-hidden />
            Log line
          </span>
        )}
        {responseAt !== null && (
          <span className="wf-legend-item">
            <span className="wf-response-swatch" aria-hidden />
            Response sent
          </span>
        )}
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
        {rows.map((r, i) => {
          if (r.gap) return <GapRow key={`gap-${r.gap.offset}`} gap={r.gap} total={total} open={open.has(i)} onToggle={() => toggle(i)} />;
          if (r.log) return <LogRow key={`log-${r.log.id}`} log={r.log} total={total} open={open.has(i)} onToggle={() => toggle(i)} onOpen={onOpenLog} />;
          const s = r.span;
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
                  {s.kind !== "background" && responseAt !== null && s.offset + s.duration > requestMs + 0.5 && (
                    <span className="wf-late" title="Ended after the response was sent: work the request left running.">
                      after response
                    </span>
                  )}
                </span>
                <span className="wf-track">
                  {[25, 50, 75].map((g) => (
                    <span key={g} className="wf-gridline" style={{ left: `${g}%` }} />
                  ))}
                  {responseLine}
                  <span
                    className={`wf-bar ${failed ? "failed" : ""}`}
                    style={{ left: `${Math.min(left, 100 - width)}%`, width: `${width}%`, background: KIND_COLOR[s.kind] ?? KIND_COLOR.custom }}
                  />
                </span>
                <span className="wf-dur">
                  {failed && (
                    <span className="wf-err" title={s.error || "Error"}>
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
                  {s.detail && (s.kind === "db" ? <SqlView sql={s.detail} compact /> : <pre className="code mono wf-sql">{s.detail}</pre>)}
                  {s.kind === "db" && s.detail && (
                    <div className="wf-actions">
                      <QueryViewerLink name={s.name} detail={s.detail} />
                    </div>
                  )}
                </div>
              )}
            </Fragment>
          );
        })}
      </div>
    </div>
  );
}

function LogRow({ log, total, open, onToggle, onOpen }: { log: LogMark; total: number; open: boolean; onToggle: () => void; onOpen?: (id: number) => void }) {
  const first = log.message.split("\n")[0];
  return (
    <>
      <button type="button" className={`wf-grid wf-row wf-logrow ${open ? "open" : ""}`} role="listitem" onClick={onToggle} aria-expanded={open}>
        <span className="wf-name" title={log.message}>
          {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
          <span className={`wf-loglevel lv-${log.level}`}>{log.level}</span>
          <span className="wf-label mono">{first}</span>
        </span>
        <span className="wf-track">
          {[25, 50, 75].map((g) => (
            <span key={g} className="wf-gridline" style={{ left: `${g}%` }} />
          ))}
          <span className={`wf-logmark lv-mark-${log.level}`} style={{ left: `${Math.min((100 * log.offset) / total, 99.5)}%` }} />
        </span>
        <span className="wf-dur dim">+{formatMs(log.offset)}</span>
      </button>
      {open && (
        <div className="wf-detail">
          <pre className="code mono wf-sql">{log.message}</pre>
          {onOpen && (
            <div className="wf-actions">
              <button type="button" className="btn sm" onClick={() => onOpen(log.id)}>
                Open the line
              </button>
            </div>
          )}
        </div>
      )}
    </>
  );
}

function GapRow({ gap, total, open, onToggle }: { gap: Gap; total: number; open: boolean; onToggle: () => void }) {
  const left = (100 * gap.offset) / total;
  const width = Math.max((100 * gap.duration) / total, 0.25);
  return (
    <>
      <button type="button" className={`wf-grid wf-row wf-gap ${open ? "open" : ""}`} role="listitem" onClick={onToggle} aria-expanded={open}>
        <span className="wf-name">
          {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
          <span className="wf-swatch wf-gap-fill" />
          <span className="wf-label">Untracked</span>
        </span>
        <span className="wf-track">
          {[25, 50, 75].map((g) => (
            <span key={g} className="wf-gridline" style={{ left: `${g}%` }} />
          ))}
          <span className="wf-bar wf-gap-fill" style={{ left: `${Math.min(left, 100 - width)}%`, width: `${width}%` }} />
        </span>
        <span className="wf-dur">{formatMs(gap.duration)}</span>
      </button>
      {open && (
        <div className="wf-detail">
          <dl className="kv wf-kv">
            <dt>Started</dt>
            <dd>
              +{formatMs(gap.offset)}
              {gap.after && <span className="dim"> · after {gap.after}</span>}
            </dd>
          </dl>
          <p className="dim wf-gap-help">{GAP_HELP}</p>
        </div>
      )}
    </>
  );
}
