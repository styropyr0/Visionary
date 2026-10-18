import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, Trash2 } from "lucide-react";
import { Drawer } from "../Modal";
import { Select } from "../Select";
import { Combobox } from "../Combobox";
import { useToast } from "../Toast";
import {
  alertsApi,
  formatAlertValue,
  OPS,
  type AlertsMeta,
  type Channel,
  type PreviewResponse,
  type RuleInput,
  type RuleKind,
  type RuleView,
  type Severity,
} from "../../lib/alerts";
import { PreviewChart } from "./PreviewChart";

const GROUP_LABEL: Record<string, string> = { request: "Requests", system: "System", liveness: "Liveness", uptime: "Uptime" };
const METHODS = ["", "GET", "POST", "PUT", "PATCH", "DELETE"];

function blank(meta: AlertsMeta): RuleInput {
  const m = meta.metrics.find((x) => x.id === "error_rate_5xx") ?? meta.metrics[0];
  return {
    name: "",
    kind: "threshold",
    severity: "warning",
    channels: [],
    runbook: "",
    enabled: true,
    spec: { metric: m.id, scope: {}, op: m.op, threshold: m.threshold, window: "5m", for: "2m", repeat: "" },
  };
}

function fromRule(r: RuleView): RuleInput {
  return { name: r.name, kind: r.kind, severity: r.severity, channels: r.channels, runbook: r.runbook, enabled: r.enabled, spec: { ...r.spec, scope: { ...(r.spec.scope ?? {}) } } };
}

interface Props {
  meta: AlertsMeta;
  channels: Channel[];
  rule: RuleView | null;
  onClose: () => void;
  onSaved: () => void;
}

export function RuleDrawer({ meta, channels, rule, onClose, onSaved }: Props) {
  const toast = useToast();
  const [form, setForm] = useState<RuleInput>(() => (rule ? fromRule(rule) : blank(meta)));
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState("");
  const [preview, setPreview] = useState<PreviewResponse | null>(null);
  const [previewErr, setPreviewErr] = useState("");
  const [previewing, setPreviewing] = useState(false);

  const sp = form.spec;
  const metric = meta.metrics.find((m) => m.id === sp.metric);
  const set = (patch: Partial<RuleInput>) => setForm((f) => ({ ...f, ...patch }));
  const setSpec = (patch: Partial<RuleInput["spec"]>) => setForm((f) => ({ ...f, spec: { ...f.spec, ...patch } }));
  const setScope = (k: string, v: string) => setForm((f) => ({ ...f, spec: { ...f.spec, scope: { ...f.spec.scope, [k]: v || undefined } } }));

  const setKind = (kind: RuleKind) => {
    if (kind === form.kind) return;
    if (kind === "anomaly") {
      const id = metric?.anomaly ? sp.metric : "requests_per_min";
      setForm((f) => ({ ...f, kind, spec: { metric: id, scope: { route: f.spec.scope.route, method: f.spec.scope.method, service: f.spec.scope.service }, threshold: 0, window: "15m", for: "5m", sensitivity: "medium" } }));
    } else if (kind === "slo") {
      setForm((f) => ({ ...f, kind, spec: { metric: "slo", scope: { route: f.spec.scope.route, method: f.spec.scope.method, service: f.spec.scope.service }, threshold: 0, window: "", for: "0s", target: meta.slo_target } }));
    } else {
      const m = meta.metrics.find((x) => x.id === "error_rate_5xx")!;
      setForm((f) => ({ ...f, kind, spec: { metric: m.id, scope: f.spec.scope, op: m.op, threshold: m.threshold, window: "5m", for: "2m" } }));
    }
  };

  const pickMetric = (id: string) => {
    const m = meta.metrics.find((x) => x.id === id);
    if (form.kind === "anomaly") return setSpec({ metric: id });
    if (id === "custom") {
      return setSpec({ metric: id, sql: sp.sql || "SELECT COUNT(*) AS value FROM api_logs WHERE created_at >= {{from}} AND created_at < {{to}} AND status >= 500", op: ">", threshold: 10, scope: {} });
    }
    const keep: Record<string, string | undefined> = {};
    for (const s of m?.scopes ?? []) keep[s] = (sp.scope as Record<string, string | undefined>)[s];
    setSpec({ metric: id, op: m?.op, threshold: m?.threshold ?? 0, scope: keep, window: id === "uptime_failing" ? "30m" : sp.window || "5m" });
  };

  // Debounced live preview: the metric over 24h and what the rule would say now.
  const key = JSON.stringify({ k: form.kind, s: form.spec });
  useEffect(() => {
    const ctrl = new AbortController();
    const id = window.setTimeout(() => {
      setPreviewing(true);
      alertsApi
        .preview({ ...form, name: form.name || "Preview", channels: [] }, ctrl.signal)
        .then((p) => {
          setPreview(p);
          setPreviewErr("");
        })
        .catch((e: Error) => {
          if (!ctrl.signal.aborted) setPreviewErr(e.message);
        })
        .finally(() => !ctrl.signal.aborted && setPreviewing(false));
    }, 600);
    return () => {
      ctrl.abort();
      window.clearTimeout(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const save = async () => {
    setSaving(true);
    setErr("");
    try {
      if (rule) await alertsApi.updateRule(rule.id, form);
      else await alertsApi.createRule(form);
      toast("ok", rule ? "Rule saved" : "Rule created");
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!rule || !window.confirm(`Delete the rule "${rule.name}"? Its history stays in Activity.`)) return;
    try {
      await alertsApi.deleteRule(rule.id);
      toast("ok", "Rule deleted");
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const metricOptions = useMemo(() => {
    const list = form.kind === "anomaly" ? meta.metrics.filter((m) => m.anomaly) : meta.metrics;
    const opts = list.map((m) => ({ value: m.id, label: `${GROUP_LABEL[m.group]} · ${m.label}` }));
    if (form.kind === "threshold") opts.push({ value: "custom", label: "Custom SQL" });
    return opts;
  }, [meta.metrics, form.kind]);

  const scopes = form.kind === "slo" ? ["route", "method", "service"] : sp.metric === "custom" ? [] : (metric?.scopes ?? []);
  const unit = form.kind === "slo" ? "percent" : (metric?.unit ?? "none");
  const label = form.kind === "slo" ? "Good requests" : sp.metric === "custom" ? "Custom query" : (metric?.label ?? "Value");

  return (
    <Drawer
      title={rule ? "Edit rule" : "New alert rule"}
      onClose={onClose}
      actions={
        rule && meta.can_edit ? (
          <button className="icon-btn" onClick={remove} title="Delete rule" aria-label="Delete rule">
            <Trash2 size={15} />
          </button>
        ) : undefined
      }
    >
      <div className="al-form">
        <label className="field">
          <span>Name</span>
          <input className="input" value={form.name} maxLength={120} placeholder="e.g. Checkout p95 above 800 ms" onChange={(e) => set({ name: e.target.value })} />
        </label>

        <div className="field">
          <span>Type</span>
          <div className="segmented" role="radiogroup" aria-label="Rule type">
            {(["threshold", "anomaly", "slo"] as RuleKind[]).map((k) => (
              <button key={k} role="radio" aria-checked={form.kind === k} className={form.kind === k ? "on" : ""} onClick={() => setKind(k)}>
                {k === "threshold" ? "Threshold" : k === "anomaly" ? "Anomaly" : "SLO burn rate"}
              </button>
            ))}
          </div>
          <p className="al-hint">
            {form.kind === "threshold" && "Fires when a metric crosses a fixed value."}
            {form.kind === "anomaly" && "Fires when a metric leaves its normal range for this hour and weekday (median and MAD over the past weeks, or the past day with less history). No threshold to set."}
            {form.kind === "slo" && `Target share of requests that succeed within ${meta.apdex_ms} ms over 30 days. Fires when the 1h and 5m windows burn the error budget 14.4× too fast, or the 6h and 30m windows 6×.`}
          </p>
        </div>

        {form.kind !== "slo" && (
          <div className="field">
            <span>Metric</span>
            <Select label="Metric" value={sp.metric} options={metricOptions} onChange={pickMetric} align="left" />
            {metric && sp.metric !== "custom" && <p className="al-hint">{metric.description}</p>}
          </div>
        )}

        {sp.metric === "custom" && form.kind === "threshold" && (
          <label className="field">
            <span>SQL</span>
            <textarea className="textarea mono" rows={5} value={sp.sql ?? ""} onChange={(e) => setSpec({ sql: e.target.value })} spellCheck={false} />
            <p className="al-hint">
              One read-only SELECT returning a single number, or <code>t, value</code> rows (the latest counts). <code>{"{{from}}"}</code> and <code>{"{{to}}"}</code> are the window.
            </p>
          </label>
        )}

        {scopes.length > 0 && (
          <div className="al-row">
            {scopes.includes("route") && (
              <label className="field grow">
                <span>Route</span>
                <Combobox label="Route" value={sp.scope.route ?? ""} placeholder="All routes" options={meta.routes} onChange={(v) => setScope("route", v)} />
              </label>
            )}
            {scopes.includes("method") && (
              <div className="field">
                <span>Method</span>
                <Select label="Method" value={sp.scope.method ?? ""} options={METHODS.map((m) => ({ value: m, label: m || "Any" }))} onChange={(v) => setScope("method", v)} align="left" />
              </div>
            )}
            {scopes.includes("instance") && (
              <div className="field">
                <span>Instance</span>
                <Select label="Instance" value={sp.scope.instance ?? ""} options={[{ value: "", label: "Each instance" }, ...meta.instances.map((i) => ({ value: i, label: i }))]} onChange={(v) => setScope("instance", v)} align="left" />
              </div>
            )}
            {scopes.includes("service") && (
              <label className="field">
                <span>Service</span>
                <Combobox label="Service" value={sp.scope.service ?? ""} placeholder="Any" options={meta.services} onChange={(v) => setScope("service", v)} />
              </label>
            )}
            {scopes.includes("check") && (
              <label className="field grow">
                <span>Check ID</span>
                <input className="input" value={sp.scope.check ?? ""} placeholder="Every check" onChange={(e) => setScope("check", e.target.value)} />
              </label>
            )}
          </div>
        )}

        {form.kind === "threshold" && (
          <div className="al-row">
            <div className="field">
              <span>Condition</span>
              <Select label="Condition" value={sp.op ?? ">"} options={OPS.map((o) => ({ value: o, label: o }))} onChange={(v) => setSpec({ op: v })} align="left" />
            </div>
            <label className="field">
              <span>Threshold{unit !== "none" ? ` (${unit === "rpm" ? "per min" : unit})` : ""}</span>
              <input className="input num" type="number" step="any" value={sp.threshold} onChange={(e) => setSpec({ threshold: Number(e.target.value) })} />
            </label>
            {sp.metric !== "instance_silent" && (
              <label className="field">
                <span>Window</span>
                <input className="input" value={sp.window} placeholder="5m" onChange={(e) => setSpec({ window: e.target.value })} />
              </label>
            )}
          </div>
        )}

        {form.kind === "anomaly" && (
          <div className="al-row">
            <div className="field">
              <span>Sensitivity</span>
              <div className="segmented" role="radiogroup" aria-label="Sensitivity">
                {["low", "medium", "high"].map((s) => (
                  <button key={s} role="radio" aria-checked={(sp.sensitivity ?? "medium") === s} className={(sp.sensitivity ?? "medium") === s ? "on" : ""} onClick={() => setSpec({ sensitivity: s })}>
                    {s[0].toUpperCase() + s.slice(1)}
                  </button>
                ))}
              </div>
            </div>
            <div className="field">
              <span>Direction</span>
              <Select
                label="Direction"
                value={sp.direction ?? ""}
                options={[
                  { value: "", label: metric?.up_is_bad ? "Default (rises)" : "Default (either way)" },
                  { value: "up", label: "Rises" },
                  { value: "down", label: "Drops" },
                  { value: "both", label: "Either way" },
                ]}
                onChange={(v) => setSpec({ direction: v || undefined })}
                align="left"
              />
            </div>
            <label className="field">
              <span>Window</span>
              <input className="input" value={sp.window} placeholder="15m" onChange={(e) => setSpec({ window: e.target.value })} />
            </label>
          </div>
        )}

        {form.kind === "slo" && (
          <label className="field">
            <span>Target (%)</span>
            <input className="input num" type="number" step="0.01" min={50} max={99.999} value={sp.target ?? meta.slo_target} onChange={(e) => setSpec({ target: Number(e.target.value) })} />
          </label>
        )}

        <div className="al-row">
          {form.kind !== "slo" && (
            <label className="field">
              <span>For</span>
              <input className="input" value={sp.for} placeholder="0s" onChange={(e) => setSpec({ for: e.target.value })} />
            </label>
          )}
          <label className="field">
            <span>Re-notify every</span>
            <input className="input" value={sp.repeat ?? ""} placeholder="Never" onChange={(e) => setSpec({ repeat: e.target.value })} />
          </label>
          <div className="field">
            <span>Severity</span>
            <div className="segmented" role="radiogroup" aria-label="Severity">
              {(["info", "warning", "critical"] as Severity[]).map((s) => (
                <button key={s} role="radio" aria-checked={form.severity === s} className={form.severity === s ? "on" : ""} onClick={() => set({ severity: s })}>
                  {s[0].toUpperCase() + s.slice(1)}
                </button>
              ))}
            </div>
          </div>
        </div>
        <p className="al-hint" style={{ marginTop: -6 }}>
          Durations like 30s, 5m, 2h.{form.kind !== "slo" && ' "For" is how long the condition must hold before the rule fires.'}
        </p>

        <section className="al-preview">
          <div className="al-preview-head">
            <span className="section-label">Last 24 hours</span>
            {previewing && <span className="al-hint">Updating…</span>}
          </div>
          {previewErr ? (
            <div className="notice">
              <AlertTriangle size={16} />
              <span>{previewErr}</span>
            </div>
          ) : preview ? (
            <>
              <PreviewChart preview={preview} label={label} />
              {preview.note && preview.result && preview.result.rows.length > 0 && <p className="al-hint">{preview.note}</p>}
              {preview.current.length > 0 && (
                <ul className="al-now">
                  {preview.current.slice(0, 8).map((o) => (
                    <li key={o.key}>
                      <span className={`al-now-dot ${o.breach ? "breach" : ""}`} aria-hidden />
                      <span className="al-now-state">{o.breach ? "Would fire" : "OK now"}</span>
                      <span className="al-now-val mono">{formatAlertValue(o.value, form.kind === "slo" ? "burn" : unit)}</span>
                      <span className="al-now-msg">{o.message}</span>
                    </li>
                  ))}
                </ul>
              )}
            </>
          ) : (
            <div className="panel-state" style={{ height: 220 }} />
          )}
        </section>

        <div className="field">
          <span>Notify</span>
          {channels.length === 0 ? (
            <p className="al-hint">No channels yet. Add one in the Channels tab; the rule still records its activity meanwhile.</p>
          ) : (
            <div className="al-checks">
              {channels.map((c) => (
                <label key={c.id} className="al-check">
                  <input
                    type="checkbox"
                    checked={form.channels.includes(c.id)}
                    onChange={(e) => set({ channels: e.target.checked ? [...form.channels, c.id] : form.channels.filter((x) => x !== c.id) })}
                  />
                  <span>{c.name}</span>
                  <span className="al-hint">{c.kind}</span>
                </label>
              ))}
            </div>
          )}
        </div>

        <label className="field">
          <span>Runbook note</span>
          <textarea className="textarea" rows={3} maxLength={2000} value={form.runbook} placeholder="What to check first, who owns it, links" onChange={(e) => set({ runbook: e.target.value })} />
        </label>

        <label className="al-check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          <span>Enabled</span>
        </label>

        {err && <span className="form-error">{err}</span>}
        <div className="al-actions">
          <button className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={save} disabled={saving || !meta.can_edit} title={meta.can_edit ? undefined : "Your role cannot change alerts"}>
            {saving ? "Saving…" : rule ? "Save rule" : "Create rule"}
          </button>
        </div>
      </div>
    </Drawer>
  );
}
