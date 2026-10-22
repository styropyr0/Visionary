import { useMemo } from "react";
import { AlertTriangle, Bug, Hourglass } from "lucide-react";
import { api } from "../../lib/api";
import { formatCount, formatMs, formatPercent, toNumber } from "../../lib/format";
import { records, type RequestTelemetry as Telemetry } from "../../lib/telemetry";
import { useQuery } from "../../lib/useQuery";
import { InfoTip } from "../InfoTip";
import { afterResponse, repeatedNames, toSpans, untracked, Waterfall } from "./Waterfall";
import "./telemetry.css";

function Totals({ label, count, ms, requestMs }: { label: string; count: number | null; ms: number | null; requestMs: number }) {
  if (count === null) return null;
  return (
    <div className="rt-total">
      <div className="rt-total-label">{label}</div>
      <div className="rt-total-value">
        {formatCount(count)} {count === 1 ? "call" : "calls"}
        {count > 0 && ms !== null && (
          <>
            {" · "}
            {formatMs(ms)}
            {requestMs > 0 && <span className="dim"> ({formatPercent(Math.min(100, (100 * ms) / requestMs))} of the request)</span>}
          </>
        )}
      </div>
    </div>
  );
}

/**
 * What a request did inside: database and HTTP totals from its log row, the
 * errors it raised, a waterfall of its calls and the time none of them covers.
 */
export function RequestTelemetry({ rec }: { rec: Record<string, unknown> }) {
  const rid = String(rec.request_id ?? "");
  const q = useQuery<Telemetry>(rid ? `req-tel-${rid}` : null, (signal) => api<Telemetry>(`/api/telemetry/requests/${encodeURIComponent(rid)}`, { signal }));
  const spans = useMemo(() => toSpans(q.data?.spans), [q.data]);
  const errors = useMemo(() => records(q.data?.errors), [q.data]);
  const repeated = useMemo(() => repeatedNames(spans), [spans]);
  const requestMs = toNumber(rec.duration_ms) ?? 0;
  const untrackedMs = useMemo(() => untracked(spans, requestMs).ms, [spans, requestMs]);
  const after = useMemo(() => afterResponse(spans, requestMs), [spans, requestMs]);

  const dbCount = toNumber(rec.db_count);
  const httpCount = toNumber(rec.http_count);
  const hasTotals = dbCount !== null || httpCount !== null;
  if (!hasTotals && spans.length === 0 && errors.length === 0) {
    if (q.loading) return null;
    return (
      <p className="dim rt-note">
        {rid
          ? "No calls were recorded for this request: the SDK that logged it does not record database and outbound calls, or VISIONARY_SPANS is off."
          : "This request has no request ID, so its calls cannot be looked up."}
      </p>
    );
  }

  return (
    <div className="rt">
      {errors.length > 0 && (
        <section className="rt-section">
          <h3 className="rt-title">Errors</h3>
          <ul className="rt-errors">
            {errors.map((e) => (
              <li key={String(e.id)}>
                <a href={`#/errors?fp=${encodeURIComponent(String(e.fingerprint))}`} className="rt-error">
                  <Bug size={13} aria-hidden />
                  <span className="mono rt-error-type">{String(e.type)}</span>
                  <span className="rt-error-msg">{String(e.message ?? "")}</span>
                  <span className="pill">{toNumber(e.handled) ? "handled" : "unhandled"}</span>
                </a>
              </li>
            ))}
          </ul>
        </section>
      )}

      {hasTotals && (
        <section className="rt-section">
          <h3 className="rt-title">Calls</h3>
          <div className="rt-totals">
            <Totals label="Database" count={dbCount} ms={toNumber(rec.db_ms)} requestMs={requestMs} />
            <Totals label="Outbound HTTP" count={httpCount} ms={toNumber(rec.http_ms)} requestMs={requestMs} />
            {spans.length > 0 && requestMs > 0 && (
              <div className="rt-total">
                <div className="rt-total-label">
                  Untracked{" "}
                  <InfoTip text="Time no recorded call covers: the app's own code (CPU work, rendering) or calls the SDK cannot see, such as an HTTP client without the Visionary transport. Open the hatched rows below to see when it happened." size={12} />
                </div>
                <div className="rt-total-value">
                  {formatMs(untrackedMs)}
                  <span className="dim"> ({formatPercent(Math.min(100, (100 * untrackedMs) / requestMs))} of the request)</span>
                </div>
              </div>
            )}
          </div>
          {repeated.map((r) => (
            <div key={r.name} className="notice rt-nplus1" role="status">
              <AlertTriangle size={15} aria-hidden />
              <span>
                <strong>
                  N+1: {r.name} ×{r.count}
                </strong>{" "}
                <span className="dim">
                  · {formatMs(r.ms)} in total. One call per item; batching them into one query (an IN list or a join) removes {r.count - 1} round trips.
                </span>
              </span>
            </div>
          ))}
        </section>
      )}

      {(after.background.length > 0 || after.late > 0) && (
        <section className="rt-section">
          <h3 className="rt-title">
            Left running after the response{" "}
            <InfoTip
              text="Work this request started and did not wait for: goroutines, threads or tasks started with the SDK's background helper, and calls that ended after the response was sent. It doesn't slow the response, but it uses the same CPU and database."
              size={12}
            />
          </h3>
          <ul className="rt-bg">
            {after.background.map((b) => {
              const end = b.offset + b.duration;
              const failed = b.status === "error";
              return (
                <li key={String(b.id)} className="rt-bg-item">
                  <Hourglass size={13} aria-hidden />
                  <span className="mono">{b.name}</span>
                  <span className="dim">
                    started at {formatMs(b.offset)}, ran {formatMs(b.duration)},{" "}
                    {end > requestMs + 0.5 ? `finished ${formatMs(end - requestMs)} after the response` : "finished before the response"}
                  </span>
                  {failed && (
                    <span className="flag bad" title={b.error}>
                      <AlertTriangle size={12} /> failed{b.error ? `: ${b.error}` : ""}
                    </span>
                  )}
                </li>
              );
            })}
            {after.late > 0 && (
              <li className="rt-bg-item dim">
                {formatCount(after.late)} {after.late === 1 ? "call" : "calls"} ended after the response; they are marked in the timeline below and not counted in the totals above.
              </li>
            )}
          </ul>
        </section>
      )}

      {spans.length > 0 ? (
        <section className="rt-section">
          <Waterfall spans={spans} requestMs={requestMs} />
        </section>
      ) : (
        hasTotals &&
        (dbCount ?? 0) + (httpCount ?? 0) > 0 &&
        !q.loading && (
          <p className="dim rt-note">
            This request has totals only: it was logged by an SDK version that kept individual calls just for slow and failed requests. Requests logged after updating the SDK keep all their calls.
          </p>
        )
      )}
    </div>
  );
}
