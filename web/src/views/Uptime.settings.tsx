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
