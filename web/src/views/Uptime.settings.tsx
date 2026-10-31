import { useState } from "react";
import { Laptop, Maximize2, Monitor, Smartphone, Tablet } from "lucide-react";
import { DEVICES, type BrowserStep, type CheckInput, type Device } from "../lib/uptime";

type Set = <K extends keyof CheckInput>(k: K, v: CheckInput[K]) => void;

const originOf = (url: string | undefined) => {
  try {
    return url ? new URL(url).origin : "";
  } catch {
    return "";
  }
};

const DEVICE_ICON = { desktop: Monitor, laptop: Laptop, tablet: Tablet, phone: Smartphone, custom: Maximize2 };

export function DeviceSettings({ form, set }: { form: CheckInput; set: Set }) {
  const device = form.device || "desktop";
  const pick = (d: Device) => {
    set("device", d);
    if (d === "custom" && !form.viewport_w) {
      set("viewport_w", 1280);
      set("viewport_h", 800);
    }
  };
  return (
    <div className="field">
      <span>Device</span>
      <div className="segmented" role="radiogroup" aria-label="Device">
        {DEVICES.map((d) => {
          const Icon = DEVICE_ICON[d.value];
          return (
            <button key={d.value} type="button" role="radio" aria-checked={device === d.value} className={device === d.value ? "on" : ""} title={d.size || "Your own width and height"} onClick={() => pick(d.value)}>
              <Icon size={13} />
              {d.label}
            </button>
          );
        })}
      </div>
      {device === "custom" ? (
        <div className="up-row">
          <input className="input" type="number" min={320} max={3840} style={{ width: 110 }} value={form.viewport_w || ""} aria-label="Width" placeholder="Width" onChange={(e) => set("viewport_w", Number(e.target.value) || 0)} />
          <span className="dim" style={{ alignSelf: "center" }}>
            ×
          </span>
          <input className="input" type="number" min={320} max={2160} style={{ width: 110 }} value={form.viewport_h || ""} aria-label="Height" placeholder="Height" onChange={(e) => set("viewport_h", Number(e.target.value) || 0)} />
          <small style={{ alignSelf: "center" }}>CSS pixels, 320–3840 wide and 320–2160 high.</small>
        </div>
      ) : (
        <small>{DEVICES.find((d) => d.value === device)?.size}. The phone is a mobile browser: its user agent, touch taps and a pixel ratio of 3.</small>
      )}
    </div>
  );
}

type NumKey = "max_lcp_ms" | "max_cls" | "max_ttfb_ms" | "max_duration_ms" | "warn_lcp_ms" | "warn_cls" | "warn_ttfb_ms" | "degraded_ms";

const LIMITS: { label: string; unit: string; max: NumKey; warn: NumKey; step?: number }[] = [
  { label: "Total time", unit: "ms", max: "max_duration_ms", warn: "degraded_ms" },
  { label: "LCP", unit: "ms", max: "max_lcp_ms", warn: "warn_lcp_ms" },
  { label: "CLS", unit: "", max: "max_cls", warn: "warn_cls", step: 0.01 },
  { label: "TTFB", unit: "ms", max: "max_ttfb_ms", warn: "warn_ttfb_ms" },
];

export function LimitSettings({ form, set }: { form: CheckInput; set: Set }) {
  const input = (k: NumKey, step?: number) => (
    <input
      className="input"
      type="number"
      min={0}
      step={step ?? 1}
      value={form[k] || ""}
      placeholder="Off"
      aria-label={k.replace(/_/g, " ")}
      onChange={(e) => set(k, Number(e.target.value) || 0)}
    />
  );
  return (
    <div className="field">
      <span>Performance limits</span>
      <div className="up-limits">
        <span />
        <span className="dim">Degraded above</span>
        <span className="dim">Fails above</span>
        {LIMITS.map((l) => (
          <div key={l.label} className="up-limits-row">
            <span>
              {l.label}
              {l.unit && <span className="dim"> ({l.unit})</span>}
            </span>
            {input(l.warn, l.step)}
            {input(l.max, l.step)}
          </div>
        ))}
      </div>
      <small>Judged when every step passed; a vital the page did not report is not judged. A step can have its own time limit too. LCP, CLS and TTFB are the last page's.</small>
    </div>
  );
}

export function TraceSettings({ form, set, steps }: { form: CheckInput; set: Set; steps: BrowserStep[] }) {
  const [origins, setOrigins] = useState((form.trace_origins ?? []).join(", "));
  const first = originOf(steps[0]?.value) || "the first page's origin";
  return (
    <div className="field">
      <span>Tracing</span>
      <fieldset className="up-checks">
        <label>
          <input type="checkbox" checked={!form.no_trace} onChange={(e) => set("no_trace", !e.target.checked)} />
          Send a traceparent so each run links to its backend trace
        </label>
        <input
          className="input mono"
          value={origins}
          disabled={!!form.no_trace}
          placeholder={first}
          aria-label="Origins that get the trace header"
          onChange={(e) => {
            setOrigins(e.target.value);
            set("trace_origins", e.target.value.split(/[\s,]+/).filter(Boolean));
          }}
        />
      </fieldset>
      <small>
        Origins that get the header, comma separated; <span className="mono">https://*.example.com</span> covers subdomains. Empty means {first} only: a header on a cross-origin request can fail its CORS preflight, so add an API's origin only when it allows <span className="mono">traceparent</span>.
      </small>
    </div>
  );
}
