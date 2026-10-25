import { useState } from "react";
import { Plus, Trash2, X } from "lucide-react";
import { Drawer, Modal } from "../components/Modal";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { deleteCheck, formatInterval, getCheckAlert, INTERVALS, MASK, saveCheck, type CheckInput, type UptimeCheck } from "../lib/uptime";
import { useQuery } from "../lib/useQuery";
import { CheckAlertSection, draftOf, type CheckAlertDraft } from "./Uptime.alerts";

const METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"];

const BLANK: CheckInput = {
  name: "",
  url: "https://",
  method: "GET",
  expect_status: "200-299",
  body_contains: "",
  headers: {},
  interval_s: 60,
  timeout_ms: 10000,
  degraded_ms: 0,
  follow_redirects: true,
  enabled: true,
  on_status_page: false,
  status_show_url: false,
};

interface Props {
  check?: UptimeCheck;
  onClose: () => void;
  onSaved: (c: UptimeCheck) => void;
  onDeleted?: () => void;
}

/** Create or edit a check. Masked header values stay masked unless retyped. */
export function CheckDrawer({ check, onClose, onSaved, onDeleted }: Props) {
  const toast = useToast();
  const [form, setForm] = useState<CheckInput>(() => (check ? { ...check } : { ...BLANK }));
  const [headers, setHeaders] = useState<{ k: string; v: string }[]>(() => Object.entries(check?.headers ?? {}).map(([k, v]) => ({ k, v })));
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const set = <K extends keyof CheckInput>(k: K, v: CheckInput[K]) => setForm((f) => ({ ...f, [k]: v }));
  // How the check is alerted on; a new check gets the rule for every check
  // unless the form says otherwise.
  const current = useQuery(check ? `check-alert-${check.id}` : null, (signal) => getCheckAlert(check!.id, signal));
  const [draft, setDraft] = useState<CheckAlertDraft | null>(null);
  const alertDraft = draft ?? draftOf(current.data?.alert);

  const submit = async () => {
    setErr("");
    const h: Record<string, string> = {};
    for (const { k, v } of headers) {
      if (k.trim()) h[k.trim()] = v;
    }
    setBusy(true);
    try {
      const { touched, ...alert } = alertDraft;
      const { check: saved, alert_error } = await saveCheck({ ...form, headers: h, alert: touched ? alert : undefined }, check?.id);
      if (alert_error) toast("err", `Saved, but its alert was not: ${alert_error}`);
      else toast("ok", check ? `Saved “${saved.name}”` : `Created “${saved.name}”`);
      onSaved(saved);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!check) return;
    try {
      await deleteCheck(check.id);
      toast("ok", `Deleted “${check.name}”`);
      onDeleted?.();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
    setConfirm(false);
  };

  const intervalOptions = INTERVALS.map((s) => ({ value: String(s), label: `Every ${formatInterval(s)}` }));

  return (
    <Drawer title={check ? `Edit ${check.name}` : "New uptime check"} onClose={onClose}>
      <form
        className="up-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <label className="field">
          <span>Name</span>
          <input className="input" value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="Checkout API" maxLength={120} required autoFocus />
        </label>
        <div className="up-row">
          <div className="field" style={{ width: 120 }}>
            <span>Method</span>
            <Select label="Method" value={form.method} align="left" onChange={(v) => set("method", v)} options={METHODS.map((m) => ({ value: m, label: m }))} />
          </div>
          <label className="field" style={{ flex: 1 }}>
            <span>URL</span>
            <input className="input mono" value={form.url} onChange={(e) => set("url", e.target.value)} placeholder="https://api.example.com/health" required />
          </label>
        </div>

        <div className="up-row">
          <label className="field" style={{ flex: 1 }}>
            <span>Expected status</span>
            <input className="input mono" value={form.expect_status} onChange={(e) => set("expect_status", e.target.value)} placeholder="200-299" />
            <small>A code, range or class, comma separated: 200, 200-299, 2xx,301</small>
          </label>
          <label className="field" style={{ flex: 1 }}>
            <span>Response body contains</span>
            <input className="input" value={form.body_contains} onChange={(e) => set("body_contains", e.target.value)} placeholder="Optional, e.g. ok" maxLength={512} />
            <small>Case sensitive; the first 1 MB is searched.</small>
          </label>
        </div>

        <div className="up-row">
          <div className="field" style={{ flex: 1 }}>
            <span>Check every</span>
            <Select label="Interval" value={String(form.interval_s)} align="left" onChange={(v) => set("interval_s", Number(v))} options={intervalOptions} />
          </div>
          <label className="field" style={{ flex: 1 }}>
            <span>Timeout (seconds)</span>
            <input className="input" type="number" min={1} max={60} value={form.timeout_ms / 1000} onChange={(e) => set("timeout_ms", Math.round(Number(e.target.value) * 1000))} />
          </label>
          <label className="field" style={{ flex: 1 }}>
            <span>Degraded above (ms)</span>
            <input
              className="input"
              type="number"
              min={0}
              max={60000}
              value={form.degraded_ms || ""}
              placeholder="Off"
              onChange={(e) => set("degraded_ms", Number(e.target.value) || 0)}
            />
          </label>
        </div>

        <div className="field">
          <span>Request headers</span>
          {headers.map((h, i) => (
            <div className="up-row" key={i}>
              <input className="input mono" style={{ flex: 1 }} value={h.k} placeholder="Header" aria-label="Header name" onChange={(e) => setHeaders((hs) => hs.map((x, j) => (j === i ? { ...x, k: e.target.value } : x)))} />
              <input
                className="input mono"
                style={{ flex: 1.4 }}
                value={h.v}
                placeholder="Value"
                aria-label="Header value"
                type={h.v === MASK ? "password" : "text"}
                onFocus={() => h.v === MASK && setHeaders((hs) => hs.map((x, j) => (j === i ? { ...x, v: "" } : x)))}
                onBlur={() => {
                  const original = check?.headers[h.k];
                  if (h.v === "" && original === MASK) setHeaders((hs) => hs.map((x, j) => (j === i ? { ...x, v: MASK } : x)));
                }}
                onChange={(e) => setHeaders((hs) => hs.map((x, j) => (j === i ? { ...x, v: e.target.value } : x)))}
              />
              <button type="button" className="icon-btn" aria-label="Remove header" onClick={() => setHeaders((hs) => hs.filter((_, j) => j !== i))}>
                <X size={15} />
              </button>
            </div>
          ))}
          <div>
            <button type="button" className="btn ghost sm" onClick={() => setHeaders((hs) => [...hs, { k: "", v: "" }])} disabled={headers.length >= 20}>
              <Plus size={14} />
              Add header
            </button>
          </div>
          <small>Values of headers that look secret (Authorization, tokens, keys, cookies) are stored but never shown again.</small>
        </div>

        <fieldset className="up-checks">
          <label>
            <input type="checkbox" checked={form.follow_redirects} onChange={(e) => set("follow_redirects", e.target.checked)} />
            Follow redirects (up to 5)
          </label>
          <label>
            <input type="checkbox" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} />
            Enabled
          </label>
          <label>
            <input type="checkbox" checked={form.on_status_page} onChange={(e) => setForm((f) => ({ ...f, on_status_page: e.target.checked, status_show_url: e.target.checked && f.status_show_url }))} />
            Show on the public status page
          </label>
          <label className={form.on_status_page ? "" : "off"}>
            <input type="checkbox" disabled={!form.on_status_page} checked={form.status_show_url} onChange={(e) => set("status_show_url", e.target.checked)} />
            Show the URL there too
          </label>
        </fieldset>

        {(!check || current.data) && <CheckAlertSection current={current.data?.alert} draft={alertDraft} onChange={setDraft} />}

        {err && <div className="form-error">{err}</div>}

        <div className="up-form-foot">
          {check && (
            <button type="button" className="btn ghost" onClick={() => setConfirm(true)}>
              <Trash2 size={14} />
              Delete
            </button>
          )}
          <span style={{ flex: 1 }} />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy}>
            {check ? "Save changes" : "Create check"}
          </button>
        </div>
      </form>
      {confirm && check && (
        <Modal
          title="Delete check"
          narrow
          onClose={() => setConfirm(false)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setConfirm(false)}>
                Cancel
              </button>
              <button type="button" className="btn danger" onClick={() => void remove()}>
                Delete check
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>
            Delete “{check.name}” and all of its results? This cannot be undone. To stop it without losing history, pause it instead.
          </p>
        </Modal>
      )}
    </Drawer>
  );
}
