import { useMemo } from "react";
import { AlertTriangle, Bug } from "lucide-react";
import { api } from "../../lib/api";
import { formatCount, formatMs, formatPercent, toNumber } from "../../lib/format";
import { records, type RequestTelemetry as Telemetry } from "../../lib/telemetry";
import { useQuery } from "../../lib/useQuery";
import { repeatedNames, toSpans, Waterfall } from "./Waterfall";
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
 * errors it raised and, when the SDK kept them, a waterfall of its calls.
 * Rows from older SDKs have none of this and render nothing.
 */
export function RequestTelemetry({ rec }: { rec: Record<string, unknown> }) {
  const rid = String(rec.request_id ?? "");
  const q = useQuery<Telemetry>(rid ? `req-tel-${rid}` : null, (signal) => api<Telemetry>(`/api/telemetry/requests/${encodeURIComponent(rid)}`, { signal }));
  const spans = useMemo(() => toSpans(q.data?.spans), [q.data]);
  const errors = useMemo(() => records(q.data?.errors), [q.data]);
  const repeated = useMemo(() => repeatedNames(spans), [spans]);

  const requestMs = toNumber(rec.duration_ms) ?? 0;
  const dbCount = toNumber(rec.db_count);
  const httpCount = toNumber(rec.http_count);
  const hasTotals = dbCount !== null || httpCount !== null;
  if (!hasTotals && spans.length === 0 && errors.length === 0) return null;

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

      {spans.length > 0 ? (
        <section className="rt-section">
          <Waterfall spans={spans} requestMs={requestMs} />
        </section>
      ) : (
        hasTotals &&
        (dbCount ?? 0) + (httpCount ?? 0) > 0 &&
        !q.loading && (
          <p className="dim rt-note">
            Individual calls are kept only for requests slower than the SDK's span threshold (100 ms by default) or answering 5xx, so this one has totals only.
          </p>
        )
      )}
    </div>
  );
}
