import { useState } from "react";
import type { BrowserStep, CheckInput } from "../lib/uptime";

type Set = <K extends keyof CheckInput>(k: K, v: CheckInput[K]) => void;

const originOf = (url: string | undefined) => {
  try {
    return url ? new URL(url).origin : "";
  } catch {
    return "";
  }
};

type NumKey = "max_lcp_ms" | "max_cls" | "max_ttfb_ms" | "max_duration_ms" | "warn_lcp_ms" | "warn_cls" | "warn_ttfb_ms" | "degraded_ms";

const LIMITS: { label: string; unit: string; max: NumKey; warn: NumKey; step?: number }[] = [
  { label: "Total time", unit: "ms", max: "max_duration_ms", warn: "degraded_ms" },
  { label: "LCP", unit: "ms", max: "max_lcp_ms", warn: "warn_lcp_ms" },
  { label: "CLS", unit: "", max: "max_cls", warn: "warn_cls", step: 0.01 },
  { label: "TTFB", unit: "ms", max: "max_ttfb_ms", warn: "warn_ttfb_ms" },
];

/** Web Vitals and time limits: over a limit fails the run, over a warning degrades it. */
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

/** Whether runs carry a traceparent, and to which origins. */
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
