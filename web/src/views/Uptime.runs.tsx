import { useEffect, useState } from "react";
import { GitCommitHorizontal, Globe, Play, Rocket, Workflow } from "lucide-react";
import { Modal } from "../components/Modal";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { shortSha } from "../lib/ci";
import { formatMs } from "../lib/format";
import { formatDateTime, formatRelative } from "../lib/time";
import type { UptimeCheck } from "../lib/uptime";
import { getRun, listRuns, runOutcome, startRun, type RunBatch, type RunItem } from "../lib/uptimeRuns";
import { useQuery } from "../lib/useQuery";
import { Outcome } from "./CI.parts";

export function RunsPanel({ checks, tick, canEdit }: { checks: UptimeCheck[]; tick: number; canEdit: boolean }) {
  const [version, setVersion] = useState(0);
  const q = useQuery(`uptime-runs#${tick}#${version}`, (signal) => listRuns(signal));
  const [open, setOpen] = useState<number | null>(null);
  const [starting, setStarting] = useState(false);
  const running = q.data?.batches.some((b) => !b.done);
  useEffect(() => {
    if (!running) return;
    const t = window.setTimeout(() => setVersion((v) => v + 1), 3000);
    return () => window.clearTimeout(t);
  }, [running, q.data]);

  const tags = [...new Set(checks.flatMap((c) => c.tags ?? []))].sort();
  const batches = q.data?.batches ?? [];
  if (batches.length === 0 && !(canEdit && tags.length > 0)) return null;
  return (
    <section className="panel span-12" aria-busy={q.loading || undefined}>
      <header className="panel-head">
        <h3 className="panel-title">Runs from CI</h3>
        <div className="panel-actions">
          {canEdit && tags.length > 0 && (
            <button type="button" className="btn sm" onClick={() => setStarting(true)}>
              <Play size={14} />
              Run a tag
            </button>
          )}
        </div>
      </header>
      {batches.length === 0 ? (
        <div className="panel-body dim">No runs yet. Start one from CI with the visionary-checks CLI, or with Run a tag.</div>
      ) : (
        <div className="table-wrap">
          <BatchTable batches={batches} onOpen={setOpen} />
        </div>
      )}
      {open !== null && <BatchModal id={open} onClose={() => setOpen(null)} />}
      {starting && (
        <StartModal
          tags={tags}
          onClose={() => setStarting(false)}
          onStarted={(id) => {
            setStarting(false);
            setVersion((v) => v + 1);
            setOpen(id);
          }}
        />
      )}
    </section>
  );
}

function BatchTable({ batches, onOpen }: { batches: RunBatch[]; onOpen: (id: number) => void }) {
  return (
    <table className="data">
      <thead>
        <tr>
          <th style={{ width: 110 }}>Result</th>
          <th>Checks</th>
          <th>Release</th>
          <th style={{ width: 140 }}>Started</th>
        </tr>
      </thead>
      <tbody>
        {batches.map((b) => (
          <tr key={b.id} className="clickable" onClick={() => onOpen(b.id)}>
            <td>
              <Outcome outcome={runOutcome(b.status)} />
            </td>
            <td>
              <div>
                {b.tags.length ? b.tags.join(", ") : "Picked checks"}
                <span className="dim"> · {counts(b)}</span>
              </div>
              {b.environment_url && <div className="up-sub mono">against {b.environment_url}</div>}
            </td>
            <td>
              <ReleaseLine batch={b} />
            </td>
            <td className="dim" title={formatDateTime(new Date(b.created_at))}>
              {formatRelative(b.created_at)}
              {b.created_by && <div className="up-sub">{b.created_by}</div>}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function counts(b: RunBatch) {
  const parts = [`${b.passed} of ${b.total} passed`];
  if (b.failed) parts.push(`${b.failed} failed`);
  if (b.skipped) parts.push(`${b.skipped} skipped`);
  return parts.join(", ");
}

function ReleaseLine({ batch: b }: { batch: RunBatch }) {
  if (!b.release && !b.commit) return <span className="dim">—</span>;
  return (
    <span className="up-run-release">
      {b.release && (
        <span>
          <Rocket size={13} aria-hidden /> <span className="mono">{b.release}</span>
        </span>
      )}
      {b.commit && (
        <span title={b.commit}>
          <GitCommitHorizontal size={13} aria-hidden /> <span className="mono">{shortSha(b.commit)}</span>
        </span>
      )}
    </span>
  );
}

function BatchModal({ id, onClose }: { id: number; onClose: () => void }) {
  const [version, setVersion] = useState(0);
  const q = useQuery(`uptime-run-${id}#${version}`, (signal) => getRun(id, signal));
  const b = q.data?.batch;
  useEffect(() => {
    if (!b || b.done) return;
    const t = window.setTimeout(() => setVersion((v) => v + 1), 2000);
    return () => window.clearTimeout(t);
  }, [b]);
  return (
    <Modal title={`Run #${id}`} onClose={onClose}>
      {q.error && !b && <div className="form-error">{q.error.message}</div>}
      {b && <BatchBody batch={b} />}
    </Modal>
  );
}

export function BatchBody({ batch: b }: { batch: RunBatch }) {
  return (
    <div className="up-run">
      <div className="up-run-meta">
        <Outcome outcome={runOutcome(b.status)} />
        <span className="dim">{counts(b)}</span>
        <ReleaseLine batch={b} />
        {b.environment_url && (
          <span className="dim">
            <Globe size={13} aria-hidden /> <span className="mono">{b.environment_url}</span>
          </span>
        )}
      </div>
      {b.environment_url && <p className="dim up-run-note">Run against another origin, so these results are not part of the checks' uptime.</p>}
      <ItemTable items={b.items ?? []} />
    </div>
  );
}

function ItemTable({ items }: { items: RunItem[] }) {
  return (
    <div className="table-wrap">
      <table className="data">
        <thead>
          <tr>
            <th style={{ width: 110 }}>Result</th>
            <th>Check</th>
            <th className="num" style={{ width: 90 }}>
              Time
            </th>
          </tr>
        </thead>
        <tbody>
          {items.map((it) => (
            <tr key={it.check_id}>
              <td>
                <Outcome outcome={runOutcome(it.status)} />
              </td>
              <td className="up-name">
                <a href={`#/uptime?check=${it.check_id}`}>{it.check_name}</a>
                {it.url && (
                  <span className="mono dim" title={it.url}>
                    {it.url}
                  </span>
                )}
                {it.error && <div className="up-run-error">{failedStep(it) ? `${failedStep(it)}: ${it.error}` : it.error}</div>}
              </td>
              <td className="num">{it.duration_ms === null || !it.finished_at ? <span className="dim">—</span> : formatMs(it.duration_ms)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function failedStep(it: RunItem): string {
  const d = it.detail;
  if (!d?.steps || d.failed_step === undefined || d.failed_step < 0) return "";
  return d.steps[d.failed_step]?.label ?? "";
}

function StartModal({ tags, onClose, onStarted }: { tags: string[]; onClose: () => void; onStarted: (id: number) => void }) {
  const toast = useToast();
  const [tag, setTag] = useState(tags[0]);
  const [override, setOverride] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const start = async () => {
    setBusy(true);
    setErr("");
    try {
      const { batch } = await startRun({ tags: [tag], environment_url_override: override.trim() || undefined });
      toast("ok", `Started ${batch.total} ${batch.total === 1 ? "check" : "checks"}`);
      onStarted(batch.id);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Run a tag"
      narrow
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={busy} onClick={() => void start()}>
            <Workflow size={14} />
            Run now
          </button>
        </>
      }
    >
      <div className="up-form">
        <div className="field">
          <span>Checks tagged</span>
          <Select label="Tag" align="left" value={tag} options={tags.map((t) => ({ value: t, label: t }))} onChange={setTag} />
        </div>
        <label className="field">
          <span>Against another origin</span>
          <input className="input mono" value={override} onChange={(e) => setOverride(e.target.value)} placeholder="Optional, e.g. https://pr-12.example.com" />
          <small>Runs the checks against this origin instead of their own, such as a preview deployment. Those results are kept with the run only.</small>
        </label>
        {err && <div className="form-error">{err}</div>}
      </div>
    </Modal>
  );
}

export function CommitChecks({ batches }: { batches: RunBatch[] }) {
  if (batches.length === 0) return null;
  return (
    <section className="panel ci-section">
      <h3 className="ci-section-title">
        <Globe size={14} aria-hidden /> Uptime checks
      </h3>
      <p className="dim ci-section-text">Checks run from CI for this commit, newest first.</p>
      {batches.map((b) => (
        <div className="up-run-block" key={b.id}>
          <div className="up-run-head dim">
            Run #{b.id}
            {b.tags.length > 0 && ` · ${b.tags.join(", ")}`} · {formatRelative(b.created_at)}
          </div>
          <BatchBody batch={b} />
        </div>
      ))}
    </section>
  );
}
