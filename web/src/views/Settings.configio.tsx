import { useState } from "react";
import { AlertTriangle, Download, FileUp, Upload } from "lucide-react";
import { api } from "../lib/api";
import { useToast } from "../components/Toast";
import { ErrorNotice } from "../components/ErrorNotice";
import { Card } from "./Settings.profiling";

type Action = "create" | "update" | "unchanged" | "skip";

interface Change {
  type: string;
  name: string;
  action: Action;
  fields?: string[];
  warning?: string;
}

interface ImportResult {
  dry_run: boolean;
  changes: Change[];
  counts: Record<Action, number>;
}

const TYPE_LABEL: Record<string, string> = {
  channel: "Notification channels",
  uptime_location: "Uptime locations",
  uptime_check: "Uptime checks",
  rule: "Alert rules",
  silence: "Silences",
  saved_panel: "Saved panels",
  dashboard: "Dashboards",
  log_view: "Saved log searches",
  pages: "Pages",
};

const ACTION_LABEL: Record<Action, string> = { create: "Create", update: "Update", unchanged: "Unchanged", skip: "Skip" };

export function ConfigIOTab() {
  const toast = useToast();
  const [doc, setDoc] = useState("");
  const [fileName, setFileName] = useState("");
  const [preview, setPreview] = useState<ImportResult | null>(null);
  const [applied, setApplied] = useState<ImportResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<Error | null>(null);
  const [showUnchanged, setShowUnchanged] = useState(false);

  const load = async (f: File) => {
    setDoc(await f.text());
    setFileName(f.name);
    setPreview(null);
    setApplied(null);
    setErr(null);
  };

  const run = async (dryRun: boolean) => {
    setBusy(true);
    setErr(null);
    try {
      const res = await api<ImportResult>("/api/config/import", { method: "POST", json: { document: doc, dry_run: dryRun } });
      if (dryRun) {
        setPreview(res);
        setApplied(null);
      } else {
        setApplied(res);
        setPreview(null);
        toast("ok", `Imported: ${res.counts.create} created, ${res.counts.update} updated`);
      }
    } catch (e) {
      setErr(e instanceof Error ? e : new Error(String(e)));
    }
    setBusy(false);
  };

  const shown = applied ?? preview;
  const pending = preview ? preview.counts.create + preview.counts.update : 0;

  return (
    <div className="settings-stack">
      <Card
        title="Export"
        description="Alert rules, notification channels, silences, uptime checks (with their alerts), saved panels, dashboards, saved log searches and hidden pages, as one file. Things are matched by name, so the file moves between installs and reads well in a review. Channel URLs and keys, secret header values and browser check secrets are left out."
        actions={
          <>
            <a className="btn" href="/api/config/export?format=yaml" download>
              <Download size={14} />
              YAML
            </a>
            <a className="btn ghost" href="/api/config/export?format=json" download>
              <Download size={14} />
              JSON
            </a>
          </>
        }
      >
        <div className="settings-body dim">
          To manage the same things from Terraform instead, see <strong>Help → Configuration as code</strong>.
        </div>
      </Card>

      <Card
        title="Import"
        description="Preview first: each item is created, updated or left as it is. Nothing is deleted, and anything not in the file stays. A channel that needs its URL or key, or a check whose secrets were left out, is skipped until the file has them."
        actions={
          <label className="btn">
            <FileUp size={14} />
            Choose file
            <input type="file" accept=".yaml,.yml,.json,application/json,application/yaml" hidden onChange={(e) => e.target.files?.[0] && void load(e.target.files[0])} />
          </label>
        }
      >
        <div className="settings-body settings-form">
          <label className="field">
            <span>{fileName ? `File: ${fileName}` : "Or paste the file"}</span>
            <textarea
              className="textarea code cio-doc"
              rows={8}
              spellCheck={false}
              value={doc}
              placeholder={"kind: visionary-config\nversion: 1\nrules: …"}
              onChange={(e) => {
                setDoc(e.target.value);
                setPreview(null);
              }}
            />
          </label>
          <div className="cio-actions">
            <button type="button" className="btn" disabled={busy || !doc.trim()} onClick={() => void run(true)}>
              Preview changes
            </button>
            <button type="button" className="btn primary" disabled={busy || !preview || pending === 0} onClick={() => void run(false)}>
              <Upload size={14} />
              Apply {preview ? `${pending} change${pending === 1 ? "" : "s"}` : ""}
            </button>
          </div>
          {err && <ErrorNotice title="Could not import this file" error={err} onRetry={null} />}
        </div>

        {shown && (
          <div className="settings-body cio-result">
            <div className="cio-counts">
              <strong>{applied ? "Applied" : "Preview"}:</strong>
              {(Object.keys(ACTION_LABEL) as Action[]).map((a) => (
                <span key={a} className={`cio-badge ${a}`}>
                  {shown.counts[a] ?? 0} {ACTION_LABEL[a].toLowerCase()}
                </span>
              ))}
              {shown.counts.unchanged > 0 && (
                <button type="button" className="btn ghost sm" onClick={() => setShowUnchanged(!showUnchanged)}>
                  {showUnchanged ? "Hide unchanged" : "Show unchanged"}
                </button>
              )}
            </div>
            <ChangeTable changes={shown.changes.filter((c) => showUnchanged || c.action !== "unchanged")} />
          </div>
        )}
      </Card>
    </div>
  );
}

function ChangeTable({ changes }: { changes: Change[] }) {
  if (changes.length === 0) return <div className="settings-empty">Nothing to change: the file matches this install.</div>;
  const order = Object.keys(TYPE_LABEL);
  const sorted = [...changes].sort((a, b) => order.indexOf(a.type) - order.indexOf(b.type));
  return (
    <div className="table-wrap settings-scroll">
      <table className="data">
        <thead>
          <tr>
            <th>Kind</th>
            <th>Name</th>
            <th>Action</th>
            <th>Details</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((c, i) => (
            <tr key={i}>
              <td className="dim">{TYPE_LABEL[c.type] ?? c.type}</td>
              <td>{c.name || <span className="dim">(no name)</span>}</td>
              <td>
                <span className={`cio-badge ${c.action}`}>{ACTION_LABEL[c.action]}</span>
              </td>
              <td>
                {c.fields && c.fields.length > 0 && <span className="mono">{c.fields.join(", ")}</span>}
                {c.warning && (
                  <span className="cio-warning">
                    <AlertTriangle size={13} />
                    {c.warning}
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
