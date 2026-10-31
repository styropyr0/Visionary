import { useRef, useState } from "react";
import { AlertTriangle, ArrowDown, ArrowUp, FileUp, Globe, Plus, X } from "lucide-react";
import { Select } from "../components/Select";
import { formatBytes } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { getBrowserStatus, MASK, type BrowserFile, type BrowserStep, type StepAction } from "../lib/uptime";

/** What each step asks for, in the order the menu lists them. */
const ACTIONS: { value: StepAction; label: string; fields: ("selector" | "text" | "value" | "count" | "ms")[]; target?: boolean; hint?: string }[] = [
  { value: "goto", label: "Go to URL", fields: ["value"] },
  { value: "click", label: "Click", fields: ["selector", "text"], target: true },
  { value: "type", label: "Type into a field", fields: ["selector", "value"] },
  { value: "press", label: "Press a key", fields: ["value"], hint: "Enter, Tab, Escape, Backspace, ArrowDown, …" },
  { value: "select", label: "Choose an option", fields: ["selector", "value"], hint: "The option's value or its visible text." },
  { value: "wait_selector", label: "Wait for an element", fields: ["selector", "text"], target: true },
  { value: "wait_text", label: "Wait for text", fields: ["text"] },
  { value: "wait_navigation", label: "Wait for the next page", fields: ["value"], hint: "Optional: part of the URL it must reach." },
  { value: "assert_text", label: "Check text is shown", fields: ["text", "selector"] },
  { value: "assert_no_text", label: "Check text is not shown", fields: ["text", "selector"] },
  { value: "assert_url", label: "Check the URL", fields: ["value"], hint: "Passes when the URL contains this." },
  { value: "assert_count", label: "Count elements", fields: ["selector", "count"] },
  { value: "wait", label: "Pause", fields: ["ms"] },
  { value: "script", label: "JavaScript check (advanced)", fields: ["value"], hint: "An expression, or a body with return; passes when the result is truthy. Runs in the page; await works." },
  { value: "extract", label: "Read a value into a variable", fields: [], hint: "Later steps use it as {{var.NAME}}. A regex keeps its first group, or the whole match." },
  { value: "hover", label: "Hover", fields: ["selector", "text"], target: true },
  { value: "drag", label: "Drag and drop", fields: [] },
  { value: "upload", label: "Upload a file", fields: [], hint: "Add the file under Files below; at most 256 KB." },
  { value: "frame", label: "Switch frame", fields: [], hint: "Later steps work inside this iframe, until a Switch frame with no selector goes back to the page." },
  { value: "set_cookie", label: "Set a cookie", fields: [] },
  { value: "set_storage", label: "Set local or session storage", fields: [] },
  { value: "grant_permission", label: "Grant a permission", fields: [], hint: "For the current page's origin unless one is given." },
];

const actionOf = (a: StepAction) => ACTIONS.find((x) => x.value === a) ?? ACTIONS[0];

const PLACEHOLDER: Record<string, string> = {
  goto: "https://app.example.com/login",
  type: "you@example.com or {{secret.PASSWORD}}",
  press: "Enter",
  select: "Europe",
  wait_navigation: "/dashboard",
  assert_url: "/dashboard",
  script: "document.querySelectorAll('.row').length > 0",
};

const EXTRACT_ATTRS = [
  { value: "text", label: "Its text" },
  { value: "value", label: "Its value" },
  { value: "attr", label: "An attribute" },
];

/** The fields of the steps beyond clicking and checking. */
function MoreFields({ s, set, files }: { s: BrowserStep; set: (patch: Partial<BrowserStep>) => void; files: BrowserFile[] }) {
  const input = (k: "selector" | "name" | "value" | "target" | "scope" | "regex" | "attr", placeholder: string, label: string, mono = true) => (
    <input className={`input ${mono ? "mono" : ""}`} value={(s[k] as string | undefined) ?? ""} placeholder={placeholder} aria-label={label} onChange={(e) => set({ [k]: e.target.value })} />
  );
  switch (s.action) {
    case "extract": {
      const kind = !s.attr || s.attr === "text" ? "text" : s.attr === "value" ? "value" : "attr";
      return (
        <>
          {input("selector", "CSS selector, e.g. #order-number", "CSS selector")}
          <div className="up-row">
            <Select label="Read" value={kind} align="left" options={EXTRACT_ATTRS} onChange={(v) => set({ attr: v === "attr" ? "href" : v })} />
            {kind === "attr" && <input className="input mono" style={{ flex: 1 }} value={s.attr ?? ""} placeholder="href, data-id, …" aria-label="Attribute" onChange={(e) => set({ attr: e.target.value })} />}
          </div>
          {input("regex", "Regex (optional), e.g. #(\\d+)", "Regex")}
          {input("name", "Variable name, e.g. ORDER_ID", "Variable name")}
        </>
      );
    }
    case "drag":
      return (
        <>
          {input("selector", "Drag from: CSS selector, e.g. .card:first-child", "Drag from")}
          {input("target", "Drop on: CSS selector, e.g. #done", "Drop on")}
        </>
      );
    case "upload":
      return (
        <>
          {input("selector", "File input, e.g. input[type=file]", "File input")}
          {files.length === 0 ? (
            <small className="up-warn">Add a file under Files first.</small>
          ) : (
            <Select label="File" value={s.name ?? ""} align="left" options={[{ value: "", label: "Choose a file" }, ...files.map((f) => ({ value: f.name, label: f.name }))]} onChange={(v) => set({ name: v })} />
          )}
        </>
      );
    case "frame":
      return input("selector", "iframe CSS selector, e.g. iframe#payment; empty goes back to the page", "Frame selector");
    case "set_cookie":
      return (
        <>
          <div className="up-row">
            <input className="input mono" style={{ flex: 1 }} value={s.name ?? ""} placeholder="Name, e.g. consent" aria-label="Cookie name" onChange={(e) => set({ name: e.target.value })} />
            <input className="input mono" style={{ flex: 1.4 }} value={s.value ?? ""} placeholder="Value, e.g. accepted or {{secret.TOKEN}}" aria-label="Cookie value" onChange={(e) => set({ value: e.target.value })} />
          </div>
          {input("scope", "URL or domain (optional), e.g. https://app.example.com or .example.com", "Cookie URL or domain")}
        </>
      );
    case "set_storage":
      return (
        <div className="up-row">
          <Select
            label="Storage"
            value={s.scope || "local"}
            align="left"
            options={[
              { value: "local", label: "localStorage" },
              { value: "session", label: "sessionStorage" },
            ]}
            onChange={(v) => set({ scope: v })}
          />
          <input className="input mono" style={{ flex: 1 }} value={s.name ?? ""} placeholder="Key, e.g. onboarding_done" aria-label="Key" onChange={(e) => set({ name: e.target.value })} />
          <input className="input mono" style={{ flex: 1 }} value={s.value ?? ""} placeholder="Value, e.g. true" aria-label="Value" onChange={(e) => set({ value: e.target.value })} />
        </div>
      );
    case "grant_permission":
      return (
        <>
          <div className="up-row">
            <Select
              label="Permission"
              value={s.permission || ""}
              align="left"
              options={[
                { value: "", label: "Choose a permission" },
                { value: "geolocation", label: "Location" },
                { value: "notifications", label: "Notifications" },
                { value: "clipboard", label: "Clipboard" },
              ]}
              onChange={(v) => set({ permission: v as BrowserStep["permission"] })}
            />
            {s.permission === "geolocation" && (
              <>
                <input className="input" type="number" step="any" min={-90} max={90} style={{ width: 120 }} value={s.lat ?? ""} placeholder="Latitude" aria-label="Latitude" onChange={(e) => set({ lat: e.target.value === "" ? undefined : Number(e.target.value) })} />
                <input className="input" type="number" step="any" min={-180} max={180} style={{ width: 120 }} value={s.lon ?? ""} placeholder="Longitude" aria-label="Longitude" onChange={(e) => set({ lon: e.target.value === "" ? undefined : Number(e.target.value) })} />
              </>
            )}
          </div>
          {input("scope", "Origin (optional), e.g. https://app.example.com", "Origin")}
        </>
      );
  }
  return null;
}

/** The ordered steps of a browser check. */
export function StepsEditor({ steps, onChange, files = [] }: { steps: BrowserStep[]; onChange: (s: BrowserStep[]) => void; files?: BrowserFile[] }) {
  const set = (i: number, patch: Partial<BrowserStep>) => onChange(steps.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  const move = (i: number, d: number) => {
    const next = [...steps];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    onChange(next);
  };
  const options = ACTIONS.map((a) => ({ value: a.value, label: a.label }));
  return (
    <div className="field">
      <span>Steps</span>
      <ol className="up-steps">
        {steps.map((s, i) => {
          const a = actionOf(s.action);
          return (
            <li key={i} className="up-step">
              <div className="up-step-head">
                <span className="up-step-n" aria-hidden>
                  {i + 1}
                </span>
                <Select
                  label={`Step ${i + 1}`}
                  value={s.action}
                  align="left"
                  options={options}
                  onChange={(v) => onChange(steps.map((x, j) => (j === i ? { action: v as StepAction, label: x.label, max_ms: x.max_ms, ...(v === "assert_count" ? { op: "eq", count: 1 } : v === "wait" ? { ms: 1000 } : {}) } : x)))}
                />
                <span style={{ flex: 1 }} />
                <button type="button" className="icon-btn" aria-label="Move up" title="Move up" disabled={i === 0} onClick={() => move(i, -1)}>
                  <ArrowUp size={14} />
                </button>
                <button type="button" className="icon-btn" aria-label="Move down" title="Move down" disabled={i === steps.length - 1} onClick={() => move(i, 1)}>
                  <ArrowDown size={14} />
                </button>
                <button type="button" className="icon-btn" aria-label="Remove step" title="Remove step" disabled={steps.length === 1} onClick={() => onChange(steps.filter((_, j) => j !== i))}>
                  <X size={14} />
                </button>
              </div>
              <div className="up-step-fields">
                {a.fields.includes("selector") && (
                  <input
                    className="input mono"
                    value={s.selector ?? ""}
                    placeholder={a.target ? "CSS selector, e.g. button[type=submit]" : a.value === "assert_text" || a.value === "assert_no_text" ? "Within (optional CSS selector)" : "CSS selector, e.g. #email"}
                    aria-label="CSS selector"
                    onChange={(e) => set(i, { selector: e.target.value })}
                  />
                )}
                {a.fields.includes("text") && (
                  <input
                    className="input"
                    value={s.text ?? ""}
                    placeholder={a.target ? "or its visible text, e.g. Sign in" : "Text, e.g. Welcome back"}
                    aria-label="Text"
                    onChange={(e) => set(i, { text: e.target.value })}
                  />
                )}
                {a.fields.includes("value") &&
                  (a.value === "script" ? (
                    <textarea className="textarea mono" rows={3} value={s.value ?? ""} placeholder={PLACEHOLDER.script} aria-label="Script" onChange={(e) => set(i, { value: e.target.value })} />
                  ) : (
                    <input
                      className={`input ${a.value === "goto" || a.value === "type" ? "mono" : ""}`}
                      value={s.value ?? ""}
                      placeholder={PLACEHOLDER[a.value] ?? ""}
                      aria-label="Value"
                      onChange={(e) => set(i, { value: e.target.value })}
                    />
                  ))}
                {a.fields.includes("count") && (
                  <div className="up-row">
                    <Select
                      label="Compare"
                      value={s.op ?? "eq"}
                      align="left"
                      options={[
                        { value: "eq", label: "Exactly" },
                        { value: "gte", label: "At least" },
                        { value: "lte", label: "At most" },
                      ]}
                      onChange={(v) => set(i, { op: v as BrowserStep["op"] })}
                    />
                    <input className="input" type="number" min={0} style={{ width: 110 }} value={s.count ?? 0} aria-label="Count" onChange={(e) => set(i, { count: Number(e.target.value) || 0 })} />
                  </div>
                )}
                {a.fields.includes("ms") && (
                  <input className="input" type="number" min={1} max={30000} value={s.ms ?? 1000} aria-label="Milliseconds" onChange={(e) => set(i, { ms: Number(e.target.value) || 0 })} />
                )}
                <MoreFields s={s} set={(patch) => set(i, patch)} files={files} />
                <div className="up-row">
                  <input className="input up-step-label" style={{ flex: 1 }} value={s.label ?? ""} placeholder="Name in results (optional)" aria-label="Step name" maxLength={120} onChange={(e) => set(i, { label: e.target.value })} />
                  <input
                    className="input up-step-label"
                    style={{ width: 150 }}
                    type="number"
                    min={0}
                    max={120000}
                    value={s.max_ms || ""}
                    placeholder="Time limit (ms)"
                    title="Fail the step when it takes longer than this"
                    aria-label="Time limit in milliseconds"
                    onChange={(e) => set(i, { max_ms: Number(e.target.value) || undefined })}
                  />
                </div>
                {a.hint && <small>{a.hint}</small>}
              </div>
            </li>
          );
        })}
      </ol>
      <div>
        <button type="button" className="btn ghost sm" disabled={steps.length >= 50} onClick={() => onChange([...steps, { action: "click", selector: "" }])}>
          <Plus size={14} />
          Add step
        </button>
      </div>
      <small>
        Steps run in order in a fresh private browser window. A step that waits for something gives up when the check's timeout runs out; checks of text, URL and counts wait up to 5 seconds. Values can use <code>{"{{secret.NAME}}"}</code> and <code>{"{{var.NAME}}"}</code> from an earlier Read step.
      </small>
    </div>
  );
}

const MAX_FILE = 256 * 1024;

/** Small files upload steps send, kept with the check. */
export function FilesEditor({ files, onChange }: { files: BrowserFile[]; onChange: (f: BrowserFile[]) => void }) {
  const pick = useRef<HTMLInputElement>(null);
  const [err, setErr] = useState("");
  const add = (list: FileList | null) => {
    setErr("");
    for (const f of Array.from(list ?? [])) {
      if (f.size > MAX_FILE) {
        setErr(`${f.name} is over 256 KB`);
        continue;
      }
      const reader = new FileReader();
      reader.onload = () => {
        const data = String(reader.result).replace(/^data:[^,]*,/, "");
        onChange([...files.filter((x) => x.name !== f.name), { name: f.name, type: f.type, size: f.size, data }]);
      };
      reader.readAsDataURL(f);
    }
  };
  return (
    <div className="field">
      <span>Files</span>
      {files.map((f, i) => (
        <div className="up-row up-file" key={f.name}>
          <FileUp size={14} aria-hidden className="dim" />
          <span className="mono" style={{ flex: 1 }}>
            {f.name}
          </span>
          <span className="dim">{formatBytes(f.size)}</span>
          <button type="button" className="icon-btn" aria-label={`Remove ${f.name}`} title="Remove" onClick={() => onChange(files.filter((_, j) => j !== i))}>
            <X size={15} />
          </button>
        </div>
      ))}
      <div>
        <button type="button" className="btn ghost sm" disabled={files.length >= 5} onClick={() => pick.current?.click()}>
          <Plus size={14} />
          Add file
        </button>
        <input ref={pick} type="file" hidden multiple onChange={(e) => (add(e.target.files), (e.target.value = ""))} />
      </div>
      {err && <small className="up-warn">{err}</small>}
      <small>For Upload a file steps: up to 5 files, 256 KB each and 512 KB in all, stored with the check.</small>
    </div>
  );
}

/** Values steps can use as {{secret.NAME}}; stored on the server, never shown again or recorded in results. */
export function SecretsEditor({ secrets, onChange, stored }: { secrets: { k: string; v: string }[]; onChange: (s: { k: string; v: string }[]) => void; stored: Record<string, string> }) {
  return (
    <div className="field">
      <span>Secrets</span>
      {secrets.map((h, i) => (
        <div className="up-row" key={i}>
          <input className="input mono" style={{ flex: 1 }} value={h.k} placeholder="PASSWORD" aria-label="Secret name" onChange={(e) => onChange(secrets.map((x, j) => (j === i ? { ...x, k: e.target.value } : x)))} />
          <input
            className="input mono"
            style={{ flex: 1.4 }}
            value={h.v}
            placeholder="Value"
            aria-label="Secret value"
            type="password"
            autoComplete="new-password"
            onFocus={() => h.v === MASK && onChange(secrets.map((x, j) => (j === i ? { ...x, v: "" } : x)))}
            onBlur={() => h.v === "" && stored[h.k] === MASK && onChange(secrets.map((x, j) => (j === i ? { ...x, v: MASK } : x)))}
            onChange={(e) => onChange(secrets.map((x, j) => (j === i ? { ...x, v: e.target.value } : x)))}
          />
          <button type="button" className="icon-btn" aria-label="Remove secret" onClick={() => onChange(secrets.filter((_, j) => j !== i))}>
            <X size={15} />
          </button>
        </div>
      ))}
      <div>
        <button type="button" className="btn ghost sm" disabled={secrets.length >= 20} onClick={() => onChange([...secrets, { k: "", v: "" }])}>
          <Plus size={14} />
          Add secret
        </button>
      </div>
      <small>
        Use one in a step as <code>{"{{secret.NAME}}"}</code>. Values are kept on the server, never shown again, and masked in results.
      </small>
    </div>
  );
}

/** Says when the server has no browser to run browser checks with, and how to get one. */
export function BrowserNotice({ always }: { always?: boolean }) {
  const { tick } = useApp();
  const st = useQuery(`uptime-browser#${tick}`, (signal) => getBrowserStatus(signal));
  const b = st.data;
  if (!b) return null;
  if (b.available) {
    if (!always) return null;
    return (
      <div className="notice up-browser-ok" role="status">
        <Globe size={16} aria-hidden />
        <span>
          Runs in {b.version || "the browser"} at <span className="mono">{b.path}</span>, {b.workers === 1 ? "one check" : `up to ${b.workers} checks`} at a time.
        </span>
      </div>
    );
  }
  return (
    <div className="notice up-coverage span-12" role="status">
      <AlertTriangle size={16} aria-hidden />
      <div className="up-coverage-text">
        <strong>Browser checks are paused: {b.error}</strong>
        <span className="dim">{b.help}</span>
      </div>
    </div>
  );
}
