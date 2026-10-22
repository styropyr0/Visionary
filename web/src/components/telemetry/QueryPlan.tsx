import { useEffect, useState } from "react";
import { AlertTriangle, Info, RefreshCw, Sparkles } from "lucide-react";
import { api } from "../../lib/api";
import type { ExplainRequest } from "../../lib/insights";
import { formatLogTime } from "../../lib/time";
import { RichText } from "../RichText";

/** After this long without an SDK picking the request up, say how to enable it. */
const UNCLAIMED_HINT_MS = 30_000;
const POLL_MS = 3000;

function age(r: ExplainRequest): number {
  return Date.now() - new Date(r.created_at).getTime();
}

/**
 * The plan of one statement, asked for on demand. Visionary queues the
 * request; an SDK with an Explainer for this database runs EXPLAIN and writes
 * the plan back, so this polls while the request waits.
 */
export function QueryPlan({ target, statement }: { target: string; statement: string }) {
  const [req, setReq] = useState<ExplainRequest | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [summary, setSummary] = useState("");
  const [explaining, setExplaining] = useState(false);
  const qs = new URLSearchParams({ target, statement }).toString();

  // The latest request for this statement, if any.
  useEffect(() => {
    let alive = true;
    api<{ request: ExplainRequest | null }>(`/api/insights/explain?${qs}`)
      .then((r) => alive && (setReq(r.request), setLoaded(true)))
      .catch((e: Error) => alive && (setError(e.message), setLoaded(true)));
    return () => {
      alive = false;
    };
  }, [qs]);

  // Poll while an SDK has yet to finish it.
  const waiting = req?.status === "pending" || req?.status === "running";
  useEffect(() => {
    if (!waiting) return;
    const t = window.setInterval(() => {
      api<{ request: ExplainRequest | null }>(`/api/insights/explain?${qs}`)
        .then((r) => setReq(r.request))
        .catch(() => undefined);
    }, POLL_MS);
    return () => window.clearInterval(t);
  }, [waiting, qs]);

  const ask = (again = false) => {
    setError("");
    setSummary("");
    api<{ request: ExplainRequest }>(`/api/insights/explain${again ? "?again=1" : ""}`, { method: "POST", json: { target, statement } })
      .then((r) => setReq(r.request))
      .catch((e: Error) => setError(e.message));
  };

  const explain = () => {
    if (!req) return;
    setExplaining(true);
    setError("");
    api<{ summary: string }>(`/api/insights/explain/${req.id}/summary`, { method: "POST", json: {} })
      .then((r) => setSummary(r.summary))
      .catch((e: Error) => setError(e.message))
      .finally(() => setExplaining(false));
  };

  if (!loaded) return <div className="qp dim">Loading…</div>;

  return (
    <div className="qp">
      <div className="qp-head">
        <span className="qp-title">Query plan</span>
        {req?.status === "done" && <span className="dim qp-when">planned {formatLogTime(req.finished_at || req.created_at)}</span>}
        <span className="qp-actions">
          {req?.status === "done" && (
            <>
              <button type="button" className="btn sm" onClick={explain} disabled={explaining}>
                <Sparkles size={13} /> {explaining ? "Explaining…" : "Explain in plain words"}
              </button>
              <button type="button" className="btn ghost sm" onClick={() => ask(true)} title="Ask for a fresh plan, e.g. after adding an index">
                <RefreshCw size={13} /> Plan again
              </button>
            </>
          )}
          {(!req || req.status === "failed") && (
            <button type="button" className="btn sm" onClick={() => ask(req?.status === "failed")}>
              {req ? "Try again" : "Get the query plan"}
            </button>
          )}
        </span>
      </div>

      {error && <div className="notice error qp-msg">{error}</div>}

      {!req && (
        <p className="dim qp-msg">
          Runs <code>EXPLAIN</code> on your database through the SDK, which plans the statement without executing it. Needs an SDK with an explainer for{" "}
          <code>{target}</code>.
        </p>
      )}

      {waiting && (
        <p className="dim qp-msg">
          {req!.status === "running"
            ? `Planning on ${req!.claimed_by || "an instance"}…`
            : age(req!) < UNCLAIMED_HINT_MS
              ? "Waiting for an SDK to pick it up…"
              : null}
          {req!.status === "pending" && age(req!) >= UNCLAIMED_HINT_MS && (
            <>
              No SDK has picked this up. Give the service that talks to <code>{target}</code> an explainer: in Go{" "}
              <code>cfg.Explain = []*visionary.Explainer{"{"}visionary.PgxExplainer(pool){"}"}</code>, in Node{" "}
              <code>visionary.init({"{"} explain: [visionary.explainWith(pool)] {"}"})</code>.
            </>
          )}
        </p>
      )}

      {req?.status === "failed" && <div className="notice error qp-msg">The database could not plan it: {req.error || "unknown error"}</div>}

      {req?.status === "done" && (
        <>
          {summary && (
            <div className="qp-summary">
              <RichText text={summary} />
            </div>
          )}
          {(req.findings?.length ?? 0) > 0 && (
            <ul className="qp-findings">
              {req.findings!.map((f, i) => (
                <li key={i} className={f.severity}>
                  {f.severity === "warning" ? <AlertTriangle size={13} aria-hidden /> : <Info size={13} aria-hidden />}
                  <span>
                    <strong>{f.title}.</strong> {f.detail}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <pre className="code mono wf-sql qp-plan">{req.plan}</pre>
        </>
      )}
    </div>
  );
}
