import { useState } from "react";
import { Clapperboard, Plus, Trash2, X } from "lucide-react";
import { Drawer, Modal } from "../components/Modal";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { alertGap, deleteCheck, formatInterval, getCheckAlert, INTERVALS, MASK, saveCheck, type BrowserStep, type CheckInput, type CheckKind, type UptimeCheck } from "../lib/uptime";
import { useQuery } from "../lib/useQuery";
import { CheckAlertSection, draftOf, type CheckAlertDraft } from "./Uptime.alerts";
import { RecorderDialog } from "./Uptime.recorder";
import { DeviceSettings, LimitSettings, TraceSettings } from "./Uptime.settings";
import { BrowserNotice, FilesEditor, SecretsEditor, StepsEditor } from "./Uptime.steps";
import { ApiRequestsEditor } from "./Uptime.api";
import { DnsFields, HeartbeatFields, KindPicker, kindDefaults, PingFields, PingNotice, SETTINGS_KINDS, SslFields, TcpFields } from "./Uptime.kinds";
import { TagsField } from "./Uptime.tags";
import { BLANK_PLAYWRIGHT, PlaywrightFields } from "./Uptime.playwright";
import { LocationsField } from "./Uptime.locations";

const METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"];

const BLANK: CheckInput = {
  name: "",
  kind: "http",
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
  locations: ["server"],
  location_rule: "majority",
  status_show_locations: false,
};

const BLANK_BROWSER: Partial<CheckInput> = {
  kind: "browser",
  interval_s: 300,
  timeout_ms: 30000,
  final_screenshot: false,
};

const NAME_HINT: Record<CheckKind, string> = {
  http: "Checkout API",
  browser: "Sign in and open the dashboard",
  api: "Sign in and list orders",
  tcp: "Postgres primary",
  dns: "example.com resolves",
  ping: "Office gateway",
  ssl: "example.com certificate",
  heartbeat: "Nightly backup",
  playwright: "Checkout pays by card",
};

const FIRST_STEPS: BrowserStep[] = [
  { action: "goto", value: "https://" },
  { action: "assert_text", text: "" },
];

interface Props {
  check?: UptimeCheck;
  onClose: () => void;
  onSaved: (c: UptimeCheck) => void;
  onDeleted?: () => void;
}

/** Create or edit a check. Masked header values stay masked unless retyped. */
export function CheckDrawer({ check, onClose, onSaved, onDeleted }: Props) {
  const toast = useToast();
  const [form, setForm] = useState<CheckInput>(() => (check ? { ...BLANK, ...check, locations: check.locations?.length ? check.locations : ["server"] } : { ...BLANK }));
  const [headers, setHeaders] = useState<{ k: string; v: string }[]>(() => Object.entries(check?.headers ?? {}).map(([k, v]) => ({ k, v })));
  const [steps, setSteps] = useState<BrowserStep[]>(() => (check?.steps?.length ? check.steps : FIRST_STEPS));
  const [secrets, setSecrets] = useState<{ k: string; v: string }[]>(() => Object.entries(check?.secrets ?? {}).map(([k, v]) => ({ k, v })));
  const browser = form.kind === "browser";
  const http = form.kind === "http";
  const playwright = form.kind === "playwright";
  const setKind = (k: CheckKind) =>
    setForm((f) =>
      k === "browser"
        ? { ...f, ...BLANK_BROWSER }
        : k === "playwright"
          ? { ...f, ...BLANK_PLAYWRIGHT }
          : k === "http"
            ? { ...f, kind: "http", interval_s: BLANK.interval_s, timeout_ms: BLANK.timeout_ms }
            : { ...f, kind: k, ...kindDefaults(k) },
    );
  const settingsKind = SETTINGS_KINDS.includes(form.kind);
  const maxTimeoutS = browser || form.kind === "api" ? 120 : form.kind === "ping" ? 10 : 60;
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [recorder, setRecorder] = useState(false);
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
    const sec: Record<string, string> = {};
    for (const { k, v } of secrets) {
      if (k.trim()) sec[k.trim()] = v;
    }
    const body = browser
      ? { ...form, headers: {}, steps, secrets: sec }
      : {
          ...form,
          headers: settingsKind ? {} : h,
          steps: undefined,
          secrets: form.kind === "api" ? sec : undefined,
          final_screenshot: undefined,
          trace_origins: undefined,
          no_trace: undefined,
          max_lcp_ms: undefined,
          max_cls: undefined,
          max_ttfb_ms: undefined,
          max_duration_ms: undefined,
          warn_lcp_ms: undefined,
          warn_cls: undefined,
          warn_ttfb_ms: undefined,
          device: undefined,
          viewport_w: undefined,
          viewport_h: undefined,
          step_screenshots: undefined,
          files: undefined,
        };
    setBusy(true);
    try {
      const { touched, ...alert } = alertDraft;
      // The rule for every check is shared, so choosing it sends none of this
      // form's channels or failures; the server turns it on if it was off. It
      // is sent untouched too when nobody would hear, so saving fixes that.
      const shared = alert.mode === "default";
      const send = touched || !check || (shared && !!alertGap(current.data?.alert));
      const { check: saved, alert_error } = await saveCheck({ ...body, alert: send ? (shared ? { mode: "default" } : alert) : undefined }, check?.id);
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
    <Drawer title={check ? `Edit ${check.name}` : "New uptime check"} onClose={onClose} className={browser || form.kind === "api" ? "up-drawer-wide" : ""}>
      <form
        className="up-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {!check && <KindPicker value={form.kind} onChange={setKind} />}
        {browser && <BrowserNotice always />}
        {form.kind === "ping" && <PingNotice always />}
        <label className="field">
          <span>Name</span>
          <input className="input" value={form.name} onChange={(e) => set("name", e.target.value)} placeholder={NAME_HINT[form.kind]} maxLength={120} required autoFocus />
        </label>
        <TagsField value={form.tags ?? []} onChange={(t) => set("tags", t)} />
        {browser && (
          <div>
            <button type="button" className="btn ghost sm" onClick={() => setRecorder(true)} title="Turn a recording exported from Chrome DevTools' Recorder into steps">
              <Clapperboard size={14} />
              Import from Chrome Recorder
            </button>
          </div>
        )}
        {browser && <StepsEditor steps={steps} onChange={setSteps} files={form.files ?? []} />}
        {browser && <DeviceSettings form={form} set={set} />}
        {browser && <SecretsEditor secrets={secrets} onChange={setSecrets} stored={check?.secrets ?? {}} />}
        {browser && (form.files?.length || steps.some((s) => s.action === "upload")) ? <FilesEditor files={form.files ?? []} onChange={(f) => set("files", f)} /> : null}
        {browser && <TraceSettings form={form} set={set} steps={steps} />}
        {form.kind === "api" && form.api && <ApiRequestsEditor requests={form.api.requests} onChange={(requests) => set("api", { requests })} />}
        {form.kind === "api" && <SecretsEditor secrets={secrets} onChange={setSecrets} stored={check?.secrets ?? {}} />}
        {form.kind === "tcp" && form.tcp && <TcpFields value={form.tcp} onChange={(v) => set("tcp", v)} />}
        {form.kind === "dns" && form.dns && <DnsFields value={form.dns} onChange={(v) => set("dns", v)} />}
        {form.kind === "ping" && form.ping && <PingFields value={form.ping} onChange={(v) => set("ping", v)} />}
        {form.kind === "ssl" && form.ssl && <SslFields value={form.ssl} onChange={(v) => set("ssl", v)} />}
        {form.kind === "heartbeat" && form.heartbeat && <HeartbeatFields value={form.heartbeat} onChange={(v) => set("heartbeat", v)} />}
        {playwright && <PlaywrightFields form={form} onChange={(p) => setForm((f) => ({ ...f, ...p }))} />}
        {http && (
          <>
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
          </>
        )}

        {form.kind !== "heartbeat" && !playwright && <LocationsField kind={form.kind} value={form.locations} rule={form.location_rule} onChange={(v) => set("locations", v)} onRule={(r) => set("location_rule", r)} />}

        <div className="up-row" style={form.kind === "heartbeat" || playwright ? { display: "none" } : undefined}>
          <div className="field" style={{ flex: 1 }}>
            <span>Check every</span>
            <Select label="Interval" value={String(form.interval_s)} align="left" onChange={(v) => set("interval_s", Number(v))} options={intervalOptions} />
          </div>
          <label className="field" style={{ flex: 1 }}>
            <span>{form.kind === "ping" ? "Wait per reply (seconds)" : "Timeout (seconds)"}</span>
            <input className="input" type="number" min={form.kind === "ping" ? 0.1 : 1} step={form.kind === "ping" ? 0.1 : 1} max={maxTimeoutS} value={form.timeout_ms / 1000} onChange={(e) => set("timeout_ms", Math.round(Number(e.target.value) * 1000))} />
          </label>
          {!browser && (
          <label className="field" style={{ flex: 1 }}>
            <span>Degraded above (ms)</span>
            <input
              className="input"
              type="number"
              min={0}
              max={browser || settingsKind ? 120000 : 60000}
              value={form.degraded_ms || ""}
              placeholder="Off"
              onChange={(e) => set("degraded_ms", Number(e.target.value) || 0)}
            />
          </label>
          )}
        </div>
        {browser && <LimitSettings form={form} set={set} />}

        {http && (
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
        )}

        <fieldset className="up-checks">
          {browser ? (
            <>
              <label>
                <input type="checkbox" checked={!!form.final_screenshot} onChange={(e) => set("final_screenshot", e.target.checked)} />
                Keep a screenshot of the last page when it passes too
              </label>
              <label title="Small screenshots, at most 1 MB a run; the latest 24 runs keep theirs">
                <input type="checkbox" checked={!!form.step_screenshots} onChange={(e) => set("step_screenshots", e.target.checked)} />
                Screenshot every step
              </label>
            </>
          ) : (
            (http || form.kind === "api") && (
              <label>
                <input type="checkbox" checked={form.follow_redirects} onChange={(e) => set("follow_redirects", e.target.checked)} />
                Follow redirects (up to 5)
              </label>
            )
          )}
          <label>
            <input type="checkbox" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} />
            Enabled
          </label>
          <label>
            <input
              type="checkbox"
              checked={form.on_status_page}
              onChange={(e) => setForm((f) => ({ ...f, on_status_page: e.target.checked, status_show_url: e.target.checked && f.status_show_url, status_show_locations: e.target.checked && f.status_show_locations }))}
            />
            Show on the public status page
          </label>
          {form.kind !== "heartbeat" && (
            <label className={form.on_status_page ? "" : "off"}>
              <input type="checkbox" disabled={!form.on_status_page} checked={form.status_show_url} onChange={(e) => set("status_show_url", e.target.checked)} />
              {http || browser || form.kind === "api" ? "Show the URL there too" : "Show the address there too"}
            </label>
          )}
          {form.locations.length > 1 && form.kind !== "heartbeat" && (
            <label className={form.on_status_page ? "" : "off"}>
              <input type="checkbox" disabled={!form.on_status_page} checked={form.status_show_locations} onChange={(e) => set("status_show_locations", e.target.checked)} />
              Show its state per location there (location names become public)
            </label>
          )}
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
      {recorder && (
        <RecorderDialog
          onClose={() => setRecorder(false)}
          onImport={(r) => {
            setSteps(r.steps);
            setForm((f) => ({
              ...f,
              name: f.name.trim() ? f.name : r.title.slice(0, 120),
              ...(r.device ? { device: r.device, viewport_w: r.viewport_w, viewport_h: r.viewport_h } : {}),
            }));
            setRecorder(false);
            toast("ok", `Imported ${r.steps.length} steps`);
          }}
        />
      )}
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
