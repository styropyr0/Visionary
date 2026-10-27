import { useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, Info, RefreshCw, Sparkles } from "lucide-react";
import { api } from "../../lib/api";
import type { ExplainFinding, ExplainRequest } from "../../lib/insights";
import { formatLogTime } from "../../lib/time";
import { CopyButton } from "../Modal";
import { RichText } from "../RichText";
import { PlanView } from "./PlanView";

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

      {req?.status === "failed" && <PlanFailure error={req.error || ""} />}

      {req?.status === "done" && (
        <>
          {summary && (
            <div className="qp-summary">
              <RichText text={summary} />
            </div>
          )}
          <Findings findings={req.findings ?? []} />
          <PlanText plan={req.plan || ""} />
        </>
      )}
    </div>
  );
}

/** "Full scan of search_listings" with the table set as code. */
function FindingTitle({ f }: { f: ExplainFinding }) {
  if (!f.table || !f.title.endsWith(f.table)) return <>{f.title}</>;
  return (
    <>
      {f.title.slice(0, -f.table.length)}
      <code>{f.table}</code>
    </>
  );
}

/**
 * What the plan says about speed: a verdict, then each problem with what to
 * do about it (an index to add, when the filter is simple enough to name
 * one). Small tables read in full cost nothing worth fixing, so they share
 * one line.
 */
function Findings({ findings }: { findings: ExplainFinding[] }) {
  const small = findings.filter((f) => f.severity === "info" && f.table);
  const rest = findings.filter((f) => !small.includes(f));
  const warnings = rest.filter((f) => f.severity === "warning").length;
  return (
    <div className="qp-review">
      <div className={`qp-verdict ${warnings ? "warn" : "ok"}`}>
        {warnings ? <AlertTriangle size={14} aria-hidden /> : <CheckCircle2 size={14} aria-hidden />}
        {warnings ? `${warnings} ${warnings === 1 ? "thing" : "things"} worth a look` : "Nothing in this plan stands out"}
      </div>
      {rest.length > 0 && (
        <ul className="qp-findings">
          {rest.map((f, i) => (
            <li key={i} className={`qp-finding ${f.severity}`}>
              <div className="qp-finding-head">
                {f.severity === "warning" ? <AlertTriangle size={13} aria-hidden /> : <Info size={13} aria-hidden />}
                <strong>
                  <FindingTitle f={f} />
                </strong>
                {f.rows ? <span className="pill qp-rows" title="The planner's estimate of the rows this step returns">~{f.rows.toLocaleString()} rows</span> : null}
              </div>
              <p className="qp-finding-detail">{f.detail}</p>
              {f.index && (
                <div className="qp-index">
                  <code className="mono">{f.index}</code>
                  <CopyButton text={f.index} />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      {small.length > 0 && (
        <p className="dim qp-note qp-icon-note">
          <Info size={13} aria-hidden />
          <span>
            Read in full but small enough that an index would not help:{" "}
            {small.map((f, i) => (
              <span key={f.table}>
                {i > 0 && ", "}
                <code>{f.table}</code>
                {f.rows ? ` (~${f.rows.toLocaleString()} rows)` : ""}
              </span>
            ))}
            .
          </span>
        </p>
      )}
    </div>
  );
}

/** SDKs start an approximate plan with a "-- Planned with ..., so it is approximate." line; it reads better as a note. */
function PlanText({ plan }: { plan: string }) {
  const m = /^-- (Planned with [^\n]*)\n/.exec(plan);
  // "values used in arithmetic set to 1" → "Values used in arithmetic were set to 1"
  const how = m && /^Planned with (.+?) set to (.+?),? so it is approximate\.?$/.exec(m[1]);
  return (
    <>
      {m && (
        <p className="dim qp-note qp-icon-note">
          <Info size={13} aria-hidden />
          <span>
            <strong className="qp-approx">Approximate plan.</strong>{" "}
            {how
              ? `${how[1].charAt(0).toUpperCase()}${how[1].slice(1)} were set to ${how[2]} in place of the real ones, so row estimates can differ from real calls.`
              : `${m[1]} Row estimates can differ from real calls.`}
          </span>
        </p>
      )}
      <PlanView plan={m ? plan.slice(m[0].length) : plan} />
    </>
  );
}

/**
 * Why a plan could not be made, in three parts: what happened, what to do,
 * and the database's own message. SDK messages put the database error last
 * in parentheses.
 */
function PlanFailure({ error }: { error: string }) {
  const raw = /\((ERROR:[^]*|[^()]*SQLSTATE[^]*)\)\s*$/.exec(error);
  const reason = (raw ? error.slice(0, raw.index) : error).trim().replace(/,?\s*so it cannot be planned$/, "");
  const noPlan = /statements have no plan$|^one statement only$|^not a statement$/.test(reason);
  if (noPlan) {
    return (
      <p className="dim qp-msg qp-icon-note">
        <Info size={13} aria-hidden />
        <span>{reason.charAt(0).toUpperCase() + reason.slice(1)}: only SELECT, INSERT, UPDATE, DELETE and WITH statements can be planned.</span>
      </p>
    );
  }
  let title = "The database could not plan this statement";
  let hint = "Try again after a deploy, or run EXPLAIN on it yourself to see the full error.";
  if (/values were removed/.test(reason)) {
    title = "The database could not tell the types of the removed values";
    hint = "Copy the statement, put a real value in place of each ?, and run EXPLAIN in your database client. Plans from the SDK guess the values, and this statement uses them in a way that cannot be guessed.";
  } else if (/cut short/.test(reason)) {
    title = "The stored statement is incomplete";
    hint = "Statements longer than 10,000 characters are cut when stored. Run EXPLAIN on the full query from your code instead.";
  } else if (/does not exist/.test(error)) {
    title = "A table or column in this statement does not exist on that database";
    hint = "The explaining service may point at a different database or schema than the one that ran the query.";
  }
  return (
    <div className="qp-failed" role="status">
      <AlertTriangle size={14} aria-hidden />
      <div>
        <div className="qp-failed-title">{title}</div>
        <div className="dim">{hint}</div>
        {(raw || reason) && <div className="qp-failed-raw">{raw ? raw[1] : reason}</div>}
      </div>
    </div>
  );
}
