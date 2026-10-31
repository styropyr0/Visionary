import { useRef, useState } from "react";
import { AlertTriangle, FileUp } from "lucide-react";
import { Modal } from "../components/Modal";
import { importRecording, type RecorderImport } from "../lib/uptime";

/** Turns a Chrome DevTools Recorder export into steps; what has no equivalent is listed. */
export function RecorderDialog({ onClose, onImport }: { onClose: () => void; onImport: (r: RecorderImport) => void }) {
  const [text, setText] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<RecorderImport | null>(null);
  const file = useRef<HTMLInputElement>(null);

  const convert = async (json: string) => {
    setErr("");
    setResult(null);
    setBusy(true);
    try {
      const r = await importRecording(json);
      if (r.unsupported.length === 0) {
        onImport(r);
        return;
      }
      setResult(r);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="Import from Chrome Recorder"
      onClose={onClose}
      footer={
        result ? (
          <>
            <button type="button" className="btn" onClick={() => setResult(null)}>
              Back
            </button>
            <button type="button" className="btn primary" onClick={() => onImport(result)}>
              Use {result.steps.length} steps
            </button>
          </>
        ) : (
          <>
            <button type="button" className="btn" onClick={onClose}>
              Cancel
            </button>
            <button type="button" className="btn primary" disabled={busy || !text.trim()} onClick={() => void convert(text)}>
              Convert
            </button>
          </>
        )
      }
    >
      {result ? (
        <div className="up-form">
          <div className="notice up-coverage" role="status">
            <AlertTriangle size={16} aria-hidden />
            <div className="up-coverage-text">
              <strong>
                {result.steps.length} steps converted; {result.unsupported.length} {result.unsupported.length === 1 ? "step has" : "steps have"} no equivalent and {result.unsupported.length === 1 ? "is" : "are"} left out
              </strong>
              <ul className="up-run-list">
                {result.unsupported.map((u) => (
                  <li key={u}>{u}</li>
                ))}
              </ul>
            </div>
          </div>
          <small className="dim">Using them replaces the steps in the form; nothing is saved until you save the check.</small>
        </div>
      ) : (
        <div className="up-form">
          <p style={{ margin: 0 }}>
            In Chrome DevTools, open <strong>Recorder</strong>, record the flow, then <strong>Export → JSON</strong>. Paste the file here or choose it. Steps keep the best selector Chrome offers: a CSS id, test id or name, else the accessible name or text, else other CSS. XPath is not used. The form's steps are replaced.
          </p>
          <textarea className="textarea mono" rows={10} value={text} placeholder='{ "title": "…", "steps": [ … ] }' aria-label="Recorder JSON" onChange={(e) => setText(e.target.value)} />
          <div>
            <button type="button" className="btn ghost sm" onClick={() => file.current?.click()}>
              <FileUp size={14} />
              Choose a file
            </button>
            <input
              ref={file}
              type="file"
              accept=".json,application/json"
              hidden
              onChange={async (e) => {
                const f = e.target.files?.[0];
                e.target.value = "";
                if (!f) return;
                const body = await f.text();
                setText(body);
                void convert(body);
              }}
            />
          </div>
          {err && <div className="form-error">{err}</div>}
        </div>
      )}
    </Modal>
  );
}
