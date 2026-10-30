import { Fragment, useMemo, useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react";
import { formatCount, formatMs, toNumber } from "../../lib/format";
import { records } from "../../lib/telemetry";
import { serviceColors, serviceLabel, type TraceResponse } from "../../lib/traces";
import { SqlView } from "./SqlView";
import { QueryViewerLink } from "./QueryViewerLink";
import "./telemetry.css";

/** A request (server span) or a call, placed on the trace's time axis. */
export interface TraceNode {
  key: string;
  spanId: string;
  parentId: string;
  /** "request", or the call's kind. */
  kind: string;
  service: string;
  name: string;
  start: number;
  duration: number;
  failed: boolean;
  status: string;
  error: string;
  target: string;
  detail: string;
  instance: string;
  /** api_logs id, for opening the request. */
  logId: number | null;
  requestId: string;
  depth: number;
}

function ms(iso: unknown): number {
  const t = new Date(String(iso ?? "")).getTime();
  return Number.isFinite(t) ? t : 0;
}

/**
 * The trace's requests and calls as one tree in start order: each under the
 * span it was made in, across services. Spans whose parent was not stored
 * (an untraced caller, a call past the span cap) start their own branch.
 */
export function traceTree(t: TraceResponse | undefined): { nodes: TraceNode[]; start: number; end: number } {
  if (!t) return { nodes: [], start: 0, end: 0 };
  const all: TraceNode[] = [];
  for (const r of records(t.requests)) {
    const status = toNumber(r.status) ?? 0;
    all.push({
      key: `r${r.id}`,
      spanId: String(r.span_id ?? ""),
      parentId: String(r.parent_span_id ?? ""),
      kind: "request",
      service: String(r.service ?? ""),
      name: `${r.method ?? ""} ${r.route || r.path || ""}`.trim(),
      start: ms(r.created_at),
      duration: toNumber(r.duration_ms) ?? 0,
      failed: status >= 500,
      status: String(status),
      error: "",
      target: String(r.path ?? ""),
      detail: "",
      instance: "",
      logId: toNumber(r.id),
      requestId: String(r.request_id ?? ""),
      depth: 0,
    });
  }
  for (const s of records(t.spans)) {
    all.push({
      key: `s${s.id}`,
      spanId: String(s.span_id ?? ""),
      parentId: String(s.parent_span_id ?? ""),
      kind: String(s.kind ?? "custom"),
      service: String(s.service ?? ""),
      name: String(s.name ?? ""),
      start: ms(s.created_at),
      duration: toNumber(s.duration_ms) ?? 0,
      failed: s.status === "error",
      status: String(s.status ?? "ok"),
      error: String(s.error ?? ""),
      target: String(s.target ?? ""),
      detail: String(s.detail ?? ""),
      instance: String(s.instance ?? ""),
      logId: null,
      requestId: String(s.request_id ?? ""),
      depth: 0,
    });
  }
  const byId = new Map(all.filter((n) => n.spanId).map((n) => [n.spanId, n]));
  const children = new Map<string, TraceNode[]>();
  const roots: TraceNode[] = [];
  for (const n of all) {
    const p = n.parentId && n.parentId !== n.spanId ? byId.get(n.parentId) : undefined;
    if (!p) roots.push(n);
    else if (children.has(p.spanId)) children.get(p.spanId)!.push(n);
    else children.set(p.spanId, [n]);
  }
  const order = (a: TraceNode, b: TraceNode) => a.start - b.start || (a.kind === "request" ? -1 : b.kind === "request" ? 1 : 0);
  const nodes: TraceNode[] = [];
  const seen = new Set<TraceNode>();
  const walk = (n: TraceNode, depth: number) => {
    if (seen.has(n)) return;
    seen.add(n);
    n.depth = depth;
    nodes.push(n);
    for (const c of (children.get(n.spanId) ?? []).sort(order)) walk(c, depth + 1);
  };
  for (const r of roots.sort(order)) walk(r, 0);
  const start = Math.min(...all.map((n) => n.start));
  const end = Math.max(...all.map((n) => n.start + n.duration));
  return { nodes, start, end };
}

/** Indentation stops growing past this depth, so deep traces stay readable. */
const MAX_INDENT = 8;

/**
 * Every request and call of a trace on one time axis: one colour per
 * service, nesting by parent, failures outlined.
 */
export function TraceTimeline({ trace, onOpenRequest }: { trace: TraceResponse; onOpenRequest?: (logId: number) => void }) {
  const { nodes, start, end } = useMemo(() => traceTree(trace), [trace]);
  const colors = useMemo(() => serviceColors(nodes.map((n) => n.service)), [nodes]);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const total = Math.max(end - start, 1);
  const toggle = (k: string) =>
    setOpen((o) => {
      const n = new Set(o);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });

  return (
    <div className="waterfall">
      <div className="wf-legend">
        {[...colors].map(([svc, color]) => (
          <span key={svc} className="wf-legend-item">
            <span className="wf-swatch" style={{ background: color }} />
            {serviceLabel(svc)}
          </span>
        ))}
        <span className="dim" style={{ marginLeft: "auto" }}>
          {formatCount(nodes.length)} spans · {formatMs(end - start)}
        </span>
      </div>
      <div className="wf-grid wf-head" aria-hidden>
        <span />
        <div className="wf-axis">
          {[0, 0.25, 0.5, 0.75, 1].map((f, i) => (
            <span key={i} style={{ left: `${100 * f}%` }} className={i === 4 ? "end" : i === 0 ? "start" : ""}>
              {f === 0 ? "0" : formatMs(f * total)}
            </span>
          ))}
        </div>
        <span />
      </div>
      <div className="wf-rows" role="list">
        {nodes.map((n) => {
          const offset = n.start - start;
          const left = (100 * offset) / total;
          const width = Math.max((100 * n.duration) / total, 0.25);
          const isOpen = open.has(n.key);
          const color = colors.get(n.service) ?? "var(--series-1)";
          return (
            <Fragment key={n.key}>
              <button type="button" className={`wf-grid wf-row tr-row ${n.kind === "request" ? "tr-request" : ""} ${isOpen ? "open" : ""}`} role="listitem" onClick={() => toggle(n.key)} aria-expanded={isOpen}>
                <span className="wf-name" style={{ paddingLeft: 2 + 12 * Math.min(n.depth, MAX_INDENT) }} title={`${serviceLabel(n.service)} · ${n.name}`}>
                  {isOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                  <span className="wf-swatch" style={{ background: color }} />
                  <span className="tr-kind">{n.kind === "request" ? serviceLabel(n.service) : n.kind}</span>
                  <span className="wf-label mono">{n.name}</span>
                </span>
                <span className="wf-track">
                  {[25, 50, 75].map((g) => (
                    <span key={g} className="wf-gridline" style={{ left: `${g}%` }} />
                  ))}
                  <span className={`wf-bar ${n.failed ? "failed" : ""} ${n.kind === "request" ? "tr-bar-request" : ""}`} style={{ left: `${Math.min(left, 100 - width)}%`, width: `${width}%`, background: color }} />
                </span>
                <span className="wf-dur">
                  {n.failed && (
                    <span className="wf-err" title={n.error || (n.kind === "request" ? `HTTP ${n.status}` : "Error")}>
                      <AlertTriangle size={12} aria-label="Failed" />
                    </span>
                  )}
                  {formatMs(n.duration)}
                </span>
              </button>
              {isOpen && (
                <div className="wf-detail">
                  <dl className="kv wf-kv">
                    <dt>Service</dt>
                    <dd>
                      {serviceLabel(n.service)}
                      {n.instance && <span className="dim"> · {n.instance}</span>}
                    </dd>
                    <dt>Started</dt>
                    <dd>+{formatMs(offset)}</dd>
                    {n.kind === "request" ? (
                      <>
                        <dt>Status</dt>
                        <dd>{n.failed ? <span className="flag bad">{n.status}</span> : n.status}</dd>
                        <dt>Path</dt>
                        <dd className="mono">{n.target}</dd>
                      </>
                    ) : (
                      <>
                        {n.target && (
                          <>
                            <dt>Target</dt>
                            <dd className="mono">{n.target}</dd>
                          </>
                        )}
                        <dt>Status</dt>
                        <dd>
                          {n.failed ? (
                            <span className="flag bad">
                              <AlertTriangle size={12} /> error
                            </span>
                          ) : (
                            "ok"
                          )}
                          {n.error && <span className="mono" style={{ marginLeft: 8 }}>{n.error}</span>}
                        </dd>
                      </>
                    )}
                    <dt>Span ID</dt>
                    <dd className="mono">{n.spanId || <span className="dim">—</span>}</dd>
                  </dl>
                  {n.detail && (n.kind === "db" ? <SqlView sql={n.detail} compact /> : <pre className="code mono wf-sql">{n.detail}</pre>)}
                  <div className="wf-actions">
                    {n.kind === "db" && n.detail && <QueryViewerLink name={n.name} detail={n.detail} />}
                    {n.logId !== null && onOpenRequest && (
                      <button type="button" className="btn sm" onClick={() => onOpenRequest(n.logId!)}>
                        Open the request
                      </button>
                    )}
                  </div>
                </div>
              )}
            </Fragment>
          );
        })}
      </div>
    </div>
  );
}
