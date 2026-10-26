import { useEffect, useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, ChevronRight, Copy, History, Plus, Trash2, X } from "lucide-react";
import { Drawer } from "../Modal";
import { Select } from "../Select";
import { DurationSelect } from "../DurationSelect";
import { Combobox } from "../Combobox";
import { useToast } from "../Toast";
import {
  alertsApi,
  formatAlertValue,
  GROUP_LABEL,
  labelPairs,
  OPS,
  RULE_PRESETS,
  type AlertsMeta,
  type Channel,
  type NoDataPolicy,
  type PreviewResponse,
  type RuleInput,
  type RuleKind,
  type RuleView,
  type Severity,
} from "../../lib/alerts";
import { listChecks } from "../../lib/uptime";
import { useQuery } from "../../lib/useQuery";
import { formatLogTime } from "../../lib/time";
import { PreviewChart } from "./PreviewChart";
import { titleCase } from "../../lib/format";
import { KIND_LABEL } from "./ChannelsTab";
import { KINDS, kindLabel } from "../../lib/security";

/** Kinds of container_events. */
const CONTAINER_KINDS: Record<string, string> = { restarted: "Restarted", oom_killed: "Out-of-memory kill", exited: "Exited", unhealthy: "Health check failing", healthy: "Healthy again" };
const containerKindLabel = (k: string) => CONTAINER_KINDS[k] ?? titleCase(k);
const METHODS = ["", "GET", "POST", "PUT", "PATCH", "DELETE"];
const UNIT_HINT: Record<string, string> = { rpm: "per min", gb: "GB", mb: "MB", days: "days", minutes: "min" };
/** Metrics read once per evaluation rather than over a window. */
const NO_WINDOW = ["instance_silent", "uptime_cert_days"];

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

export function ruleInputOf(r: RuleView): RuleInput {
  return {
    name: r.name,
    kind: r.kind,
    severity: r.severity,
    channels: r.channels,
    runbook: r.runbook,
    enabled: r.enabled,
    spec: { ...r.spec, scope: { ...(r.spec.scope ?? {}) }, escalation: r.spec.escalation?.map((s) => ({ ...s, channels: [...s.channels] })), labels: r.spec.labels ? { ...r.spec.labels } : undefined },
  };
}

function parseLabels(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const part of text.split(/[,\n]+/)) {
    const [k, ...v] = part.split("=");
    if (k?.trim()) out[k.trim()] = v.join("=").trim();
  }
  return Object.keys(out).length ? out : undefined;
}

interface Props {
  meta: AlertsMeta;
  channels: Channel[];
  /** Every rule, for "depends on". */
  rules: RuleView[];
  rule: RuleView | null;
  /** Starts a new rule from these settings (Duplicate). */
  initial?: RuleInput;
  onClose: () => void;
  onSaved: () => void;
  onDuplicate?: (input: RuleInput) => void;
}

export function RuleDrawer({ meta, channels, rules, rule, initial, onClose, onSaved, onDuplicate }: Props) {
  const toast = useToast();
  const [form, setForm] = useState<RuleInput>(() => (rule ? ruleInputOf(rule) : (initial ?? blank(meta))));
  const [labelsText, setLabelsText] = useState(() => labelPairs(form.spec.labels).join(", "));
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
  const checks = useQuery(metric?.group === "uptime" ? "rule-checks" : null, (signal) => listChecks(signal));

  const setKind = (kind: RuleKind) => {
    if (kind === form.kind) return;
    const keep = { no_data: sp.no_data, notify_resolved: sp.notify_resolved, mention: sp.mention, title: sp.title, message: sp.message, escalation: sp.escalation, depends_on: sp.depends_on, labels: sp.labels, repeat: sp.repeat };
    if (kind === "anomaly") {
      const id = metric?.anomaly ? sp.metric : "requests_per_min";
      setForm((f) => ({ ...f, kind, spec: { ...keep, metric: id, scope: { route: f.spec.scope.route, method: f.spec.scope.method, service: f.spec.scope.service }, threshold: 0, window: "15m", for: "5m", sensitivity: "medium" } }));
    } else if (kind === "slo") {
      setForm((f) => ({ ...f, kind, spec: { ...keep, metric: "slo", scope: { route: f.spec.scope.route, method: f.spec.scope.method, service: f.spec.scope.service }, threshold: 0, window: "", for: "0s", target: meta.slo_target } }));
    } else {
      const m = meta.metrics.find((x) => x.id === "error_rate_5xx")!;
      setForm((f) => ({ ...f, kind, spec: { ...keep, metric: m.id, scope: f.spec.scope, op: m.op, threshold: m.threshold, window: "5m", for: "2m" } }));
    }
  };

  const pickMetric = (id: string) => {
    const m = meta.metrics.find((x) => x.id === id);
    if (form.kind === "anomaly") return setSpec({ metric: id });
    if (id === "custom") {
      return setSpec({ metric: id, sql: sp.sql || "SELECT COUNT(*) AS value FROM api_logs WHERE created_at >= {{from}} AND created_at < {{to}} AND status >= 500", op: ">", threshold: 10, scope: {}, clear_threshold: undefined });
    }
    const keep: Record<string, string | undefined> = {};
    for (const s of m?.scopes ?? []) keep[s] = (sp.scope as Record<string, string | undefined>)[s];
    const window = id === "uptime_failing" ? "30m" : id === "uptime_availability" ? "1h" : sp.window || "5m";
    setSpec({ metric: id, op: m?.op, threshold: m?.threshold ?? 0, scope: keep, window, clear_threshold: undefined });
  };

  // Debounced live preview: the metric over 24h and what the rule would say now.
  const condition = { k: form.kind, s: { ...form.spec, mention: undefined, title: undefined, message: undefined, escalation: undefined, depends_on: undefined, labels: undefined } };
  const key = JSON.stringify(condition);
  useEffect(() => {
    const ctrl = new AbortController();
    const id = window.setTimeout(() => {
      setPreviewing(true);
      alertsApi
        .preview({ ...form, name: form.name || "Preview", channels: [], spec: { ...form.spec, escalation: undefined, depends_on: undefined } }, ctrl.signal)
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

  const body = (): RuleInput => {
    const spec = { ...form.spec, labels: parseLabels(labelsText) };
    if (spec.clear_threshold === null || Number.isNaN(spec.clear_threshold)) spec.clear_threshold = undefined;
    if (!spec.escalation?.length) spec.escalation = undefined;
    if (!spec.depends_on?.length) spec.depends_on = undefined;
    return { ...form, spec };
  };

  const save = async () => {
    setSaving(true);
    setErr("");
    try {
      if (rule) await alertsApi.updateRule(rule.id, body());
      else await alertsApi.createRule(body());
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

  const applyPreset = (id: string) => {
    const p = RULE_PRESETS.find((x) => x.id === id);
    if (!p) return;
    setForm((f) => ({ ...f, kind: p.kind ?? "threshold", name: f.name || p.name, severity: p.severity, runbook: f.runbook || p.runbook || "", spec: { ...p.spec, scope: { ...p.spec.scope } } }));
  };
  const presetOptions = useMemo(() => {
    const have = new Set(meta.metrics.map((m) => m.id));
    return RULE_PRESETS.filter((p) => p.kind === "slo" || have.has(p.spec.metric)).map((p) => ({ value: p.id, label: `${p.group} · ${p.label}` }));
  }, [meta.metrics]);

  const metricOptions = useMemo(() => {
    const list = form.kind === "anomaly" ? meta.metrics.filter((m) => m.anomaly) : meta.metrics;
    const opts = list.map((m) => ({ value: m.id, label: `${GROUP_LABEL[m.group] ?? m.group} · ${m.label}` }));
    if (form.kind === "threshold") opts.push({ value: "custom", label: "Custom SQL" });
    return opts;
  }, [meta.metrics, form.kind]);

  const scopes = form.kind === "slo" ? ["route", "method", "service"] : sp.metric === "custom" ? [] : (metric?.scopes ?? []);
  const unit = form.kind === "slo" ? "percent" : (metric?.unit ?? "none");
  const label = form.kind === "slo" ? "Good requests" : sp.metric === "custom" ? "Custom query" : (metric?.label ?? "Value");
  const unitHint = UNIT_HINT[unit] ?? (unit !== "none" && unit !== "score" && unit !== "count" ? unit : "");
  const others = rules.filter((r) => r.id !== rule?.id);
  const steps = sp.escalation ?? [];
  const setStep = (i: number, patch: Partial<(typeof steps)[number]>) => setSpec({ escalation: steps.map((s, j) => (j === i ? { ...s, ...patch } : s)) });
  const advancedSet = !!(sp.no_data || sp.clear_for || sp.clear_threshold != null || sp.notify_resolved === false);
  const messageSet = !!(sp.mention || sp.title || sp.message);

  return (
    <Drawer
      title={rule ? "Edit rule" : "New alert rule"}
      onClose={onClose}
      actions={
        rule && meta.can_edit ? (
          <>
            {onDuplicate && (
              <button type="button" className="icon-btn" onClick={() => onDuplicate({ ...body(), name: `Copy of ${form.name}`.slice(0, 120) })} title="Duplicate rule" aria-label="Duplicate rule">
                <Copy size={15} />
              </button>
            )}
            <button type="button" className="icon-btn" onClick={remove} title="Delete rule" aria-label="Delete rule">
              <Trash2 size={15} />
            </button>
          </>
        ) : undefined
      }
    >
      <div className="al-form">
        {!rule && (
          <div className="field">
            <span>Start from a preset</span>
            <Select label="Preset" value="" options={[{ value: "", label: "Blank rule" }, ...presetOptions]} onChange={applyPreset} align="left" />
            <p className="al-hint">{presetOptions.length} presets for requests, errors, hosts, uptime, browsers, dependencies, security and containers. Each fills the form; change anything before saving.</p>
          </div>
        )}
        <label className="field">
          <span>Name</span>
          <input className="input" value={form.name} maxLength={120} placeholder="e.g. Checkout p95 above 800 ms" onChange={(e) => set({ name: e.target.value })} />
        </label>

        <div className="field">
          <span>Type</span>
          <div className="segmented" role="radiogroup" aria-label="Rule type">
            {(["threshold", "anomaly", "slo"] as RuleKind[]).map((k) => (
              <button type="button" key={k} role="radio" aria-checked={form.kind === k} className={form.kind === k ? "on" : ""} onClick={() => setKind(k)}>
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
            {scopes.includes("app") && (
              <label className="field">
                <span>App</span>
                <input className="input" value={sp.scope.app ?? ""} placeholder="Every app" onChange={(e) => setScope("app", e.target.value)} />
              </label>
            )}
            {scopes.includes("event_kind") && (
              <div className="field">
                <span>{sp.metric === "container_events" ? "Event" : "Finding kind"}</span>
                <Select
                  label={sp.metric === "container_events" ? "Event" : "Finding kind"}
                  value={sp.scope.event_kind ?? ""}
                  options={
                    sp.metric === "container_events"
                      ? [{ value: "", label: "Any event" }, ...Object.entries(CONTAINER_KINDS).map(([k, label]) => ({ value: k, label }))]
                      : [{ value: "", label: "Any kind" }, ...Object.entries(KINDS).map(([k, v]) => ({ value: k, label: v.label }))]
                  }
                  onChange={(v) => setScope("event_kind", v)}
                  align="left"
                />
              </div>
            )}
            {scopes.includes("event_severity") && (
              <div className="field">
                <span>Severity</span>
                <Select
                  label="Minimum severity"
                  value={sp.scope.event_severity ?? ""}
                  options={[
                    { value: "", label: "Any" },
                    { value: "warning", label: "Warning and up" },
                    { value: "critical", label: "Critical" },
                  ]}
                  onChange={(v) => setScope("event_severity", v)}
                  align="left"
                />
              </div>
            )}
            {scopes.includes("source") && (
              <div className="field">
                <span>Source</span>
                <Select
                  label="Source"
                  value={sp.scope.source ?? ""}
                  options={[
                    { value: "", label: "Any" },
                    { value: "api", label: "API detectors" },
                    { value: "host", label: "Host agent" },
                  ]}
                  onChange={(v) => setScope("source", v)}
                  align="left"
                />
              </div>
            )}
            {scopes.includes("container") && (
              <label className="field">
                <span>Container</span>
                <input className="input" value={sp.scope.container ?? ""} placeholder="Each container" onChange={(e) => setScope("container", e.target.value)} />
              </label>
            )}
            {scopes.includes("host") && (
              <label className="field">
                <span>{metric?.group === "container" ? "Machine" : "Host"}</span>
                <input className="input" value={sp.scope.host ?? ""} placeholder={metric?.group === "container" ? "Every machine" : "Every host"} onChange={(e) => setScope("host", e.target.value)} />
              </label>
            )}
            {scopes.includes("check") && (
              <div className="field grow">
                <span>Check</span>
                <Select
                  label="Check"
                  value={sp.scope.check ?? ""}
                  options={[
                    { value: "", label: "Every check" },
                    ...(checks.data?.checks ?? []).map((c) => ({ value: String(c.check.id), label: c.check.name })),
                    ...(sp.scope.check && !checks.data?.checks.some((c) => String(c.check.id) === sp.scope.check) ? [{ value: sp.scope.check, label: `Check ${sp.scope.check}` }] : []),
                  ]}
                  onChange={(v) => setScope("check", v)}
                  align="left"
                />
              </div>
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
              <span>Threshold{unitHint ? ` (${unitHint})` : unit === "percent" ? " (%)" : unit === "ms" ? " (ms)" : ""}</span>
              <input className="input num" type="number" step="any" value={sp.threshold} onChange={(e) => setSpec({ threshold: Number(e.target.value) })} />
            </label>
            {!NO_WINDOW.includes(sp.metric) && (
              <div className="field">
                <span>Window</span>
                <DurationSelect label="Window" value={sp.window || "5m"} min={60} max={86400} onChange={(v) => setSpec({ window: v })} align="left" />
              </div>
            )}
          </div>
        )}

        {form.kind === "anomaly" && (
          <div className="al-row">
            <div className="field">
              <span>Sensitivity</span>
              <div className="segmented" role="radiogroup" aria-label="Sensitivity">
                {["low", "medium", "high"].map((s) => (
                  <button type="button" key={s} role="radio" aria-checked={(sp.sensitivity ?? "medium") === s} className={(sp.sensitivity ?? "medium") === s ? "on" : ""} onClick={() => setSpec({ sensitivity: s })}>
                    {titleCase(s)}
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
            <div className="field">
              <span>Window</span>
              <DurationSelect label="Window" value={sp.window || "15m"} min={300} max={7200} onChange={(v) => setSpec({ window: v })} align="left" />
            </div>
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
            <div className="field">
              <span>For</span>
              <DurationSelect label="For" value={sp.for} empty="At once" max={86400} onChange={(v) => setSpec({ for: v || "0s" })} align="left" />
            </div>
          )}
          <div className="field">
            <span>Re-notify every</span>
            <DurationSelect label="Re-notify every" value={sp.repeat} empty="Never" min={300} max={604800} onChange={(v) => setSpec({ repeat: v })} align="left" />
          </div>
          <div className="field">
            <span>Severity</span>
            <div className="segmented" role="radiogroup" aria-label="Severity">
              {(["info", "warning", "critical"] as Severity[]).map((s) => (
                <button type="button" key={s} role="radio" aria-checked={form.severity === s} className={form.severity === s ? "on" : ""} onClick={() => set({ severity: s })}>
                  {titleCase(s)}
                </button>
              ))}
            </div>
          </div>
        </div>
        <p className="al-hint" style={{ marginTop: -6 }}>
          {form.kind !== "slo" && '"For" is how long the condition must hold before the rule fires. '}Re-notifying stops once someone acknowledges.
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
              <PreviewChart preview={preview} label={label} seriesLabel={sp.metric === "container_events" ? containerKindLabel : sp.metric === "security_events" ? kindLabel : undefined} />
              {preview.note && <p className="al-hint">{preview.note}</p>}
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
            <ChannelChecks channels={channels} value={form.channels} onChange={(ids) => set({ channels: ids })} />
          )}
        </div>

        <Section title="When data stops, flapping and resolving" open={advancedSet}>
          <div className="field">
            <span>When there is no data</span>
            <div className="segmented" role="radiogroup" aria-label="No data">
              {(
                [
                  ["", "Treat as OK"],
                  ["alerting", "Fire"],
                  ["keep", "Keep last state"],
                ] as [NoDataPolicy, string][]
              ).map(([v, l]) => (
                <button type="button" key={v || "ok"} role="radio" aria-checked={(sp.no_data ?? "") === v} className={(sp.no_data ?? "") === v ? "on" : ""} onClick={() => setSpec({ no_data: v || undefined })}>
                  {l}
                </button>
              ))}
            </div>
            <p className="al-hint">A window with no requests or samples (a service that died, a host agent that stopped) is OK by default. Fire for rules that must catch a total outage.</p>
          </div>
          <div className="al-row">
            <div className="field">
              <span>Resolve after clear for</span>
              <DurationSelect label="Resolve after clear for" value={sp.clear_for} empty="At once" max={86400} onChange={(v) => setSpec({ clear_for: v || undefined })} align="left" />
            </div>
            {form.kind === "threshold" && sp.metric !== "custom" && (
              <label className="field">
                <span>Clear threshold</span>
                <input
                  className="input num"
                  type="number"
                  step="any"
                  value={sp.clear_threshold ?? ""}
                  placeholder="Same"
                  onChange={(e) => setSpec({ clear_threshold: e.target.value === "" ? undefined : Number(e.target.value) })}
                />
              </label>
            )}
          </div>
          <p className="al-hint" style={{ marginTop: -6 }}>
            Both stop a reading that hovers at the threshold from firing and resolving over and over: resolve only after the condition has been clear this long
            {form.kind === "threshold" ? ", and only once the reading passes the clear threshold (below it for > rules, above it for <)" : ""}.
          </p>
          <label className="al-check">
            <input type="checkbox" checked={sp.notify_resolved !== false} onChange={(e) => setSpec({ notify_resolved: e.target.checked ? undefined : false })} />
            <span>Notify when it resolves</span>
          </label>
        </Section>

        <Section title={`Escalation${steps.length ? ` · ${steps.length} step${steps.length === 1 ? "" : "s"}` : ""}`} open={steps.length > 0}>
          <p className="al-hint">If nobody acknowledges a firing alert, notify more channels after a while. Each step waits longer than the one before, counted from when it fired.</p>
          {steps.map((s, i) => (
            <div key={i} className="al-step">
              <div className="al-step-head">
                <span className="al-step-n">Step {i + 1}</span>
                <div className="al-inline">
                  after
                  <DurationSelect label={`Step ${i + 1} wait`} value={s.after} min={60} max={604800} onChange={(v) => setStep(i, { after: v })} align="left" />
                </div>
                <button type="button" className="icon-btn" aria-label={`Remove step ${i + 1}`} onClick={() => setSpec({ escalation: steps.filter((_, j) => j !== i) })}>
                  <X size={14} />
                </button>
              </div>
              <ChannelChecks channels={channels} value={s.channels} onChange={(ids) => setStep(i, { channels: ids })} />
            </div>
          ))}
          <div>
            <button
              type="button"
              className="btn ghost sm"
              disabled={steps.length >= 5 || channels.length === 0}
              onClick={() => setSpec({ escalation: [...steps, { after: steps.length ? "1h" : "15m", channels: [] }] })}
            >
              <Plus size={14} />
              Add step
            </button>
          </div>
        </Section>

        <Section title="Message" open={messageSet}>
          <label className="field">
            <span>Mention</span>
            <input className="input mono" value={sp.mention ?? ""} maxLength={200} placeholder="e.g. <!here>, <@U123ABC>, @oncall" onChange={(e) => setSpec({ mention: e.target.value || undefined })} />
            <p className="al-hint">Put at the start of every message and escalation. Slack wants &lt;!here&gt;, &lt;!channel&gt; or &lt;@user-id&gt;; Discord &lt;@&amp;role-id&gt;.</p>
          </label>
          <label className="field">
            <span>Title</span>
            <input className="input" value={sp.title ?? ""} maxLength={250} placeholder="[{{state}}] {{rule}}" onChange={(e) => setSpec({ title: e.target.value || undefined })} />
          </label>
          <label className="field">
            <span>Message</span>
            <textarea className="textarea" rows={2} maxLength={2000} value={sp.message ?? ""} placeholder="The reading, in a sentence (the default)" onChange={(e) => setSpec({ message: e.target.value || undefined })} />
            <p className="al-hint">
              Use <code>{"{{rule}}"}</code> <code>{"{{state}}"}</code> <code>{"{{severity}}"}</code> <code>{"{{series}}"}</code> <code>{"{{value}}"}</code> <code>{"{{threshold}}"}</code> <code>{"{{message}}"}</code>{" "}
              <code>{"{{labels.team}}"}</code>. Empty keeps the default.
            </p>
          </label>
        </Section>

        <Section title={`Depends on${sp.depends_on?.length ? ` · ${sp.depends_on.length}` : ""}`} open={!!sp.depends_on?.length}>
          <p className="al-hint">
            Hold this rule's notifications while one of these fires for the same instance, check or host (or at all, when it has one series), so an outage sends one alert instead of a pile. Activity
            still records them.
          </p>
          {others.length === 0 ? (
            <p className="al-hint">No other rules yet.</p>
          ) : (
            <div className="al-checks al-checks-col">
              {others.map((r) => (
                <label key={r.id} className="al-check">
                  <input
                    type="checkbox"
                    checked={!!sp.depends_on?.includes(r.id)}
                    onChange={(e) => setSpec({ depends_on: e.target.checked ? [...(sp.depends_on ?? []), r.id] : (sp.depends_on ?? []).filter((x) => x !== r.id) })}
                  />
                  <span>{r.name}</span>
                </label>
              ))}
            </div>
          )}
        </Section>

        <label className="field">
          <span>Labels</span>
          <input className="input mono" value={labelsText} placeholder="team=payments, env=prod" onChange={(e) => setLabelsText(e.target.value)} />
          <p className="al-hint">Filter rules by them, aim silences at them, and they ride along in every message.</p>
        </label>

        <label className="field">
          <span>Runbook note</span>
          <textarea className="textarea" rows={3} maxLength={2000} value={form.runbook} placeholder="What to check first, who owns it, links" onChange={(e) => set({ runbook: e.target.value })} />
        </label>

        <label className="al-check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          <span>Enabled</span>
        </label>

        {rule && <RuleHistory id={rule.id} />}

        {err && <span className="form-error">{err}</span>}
        <div className="al-actions">
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={save} disabled={saving || !meta.can_edit} title={meta.can_edit ? undefined : "Your role cannot change alerts"}>
            {saving ? "Saving…" : rule ? "Save rule" : "Create rule"}
          </button>
        </div>
      </div>
    </Drawer>
  );
}

function ChannelChecks({ channels, value, onChange }: { channels: Channel[]; value: number[]; onChange: (ids: number[]) => void }) {
  return (
    <div className="al-checks">
      {channels.map((c) => (
        <label key={c.id} className="al-check">
          <input type="checkbox" checked={value.includes(c.id)} onChange={(e) => onChange(e.target.checked ? [...value, c.id] : value.filter((x) => x !== c.id))} />
          <span>{c.name}</span>
          <span className="al-hint">{KIND_LABEL[c.kind]}</span>
        </label>
      ))}
    </div>
  );
}

/** A part of the form folded away until it is wanted. */
function Section({ title, open, children }: { title: string; open: boolean; children: ReactNode }) {
  const [shown, setShown] = useState(open);
  return (
    <section className={`al-section ${shown ? "open" : ""}`}>
      <button type="button" className="al-section-head" aria-expanded={shown} onClick={() => setShown(!shown)}>
        <ChevronRight size={14} aria-hidden />
        {title}
      </button>
      {shown && <div className="al-section-body">{children}</div>}
    </section>
  );
}

const ACTION_LABEL: Record<string, string> = { create: "Created", update: "Changed", delete: "Deleted", mute: "Muted", unmute: "Unmuted", enable: "Enabled", disable: "Disabled" };

function RuleHistory({ id }: { id: number }) {
  const [open, setOpen] = useState(false);
  const history = useQuery(open ? `rule-history-${id}` : null, (signal) => alertsApi.history(id, signal));
  return (
    <section className={`al-section ${open ? "open" : ""}`}>
      <button type="button" className="al-section-head" aria-expanded={open} onClick={() => setOpen(!open)}>
        <ChevronRight size={14} aria-hidden />
        <History size={13} aria-hidden /> History
      </button>
      {open && (
        <div className="al-section-body">
          {history.error ? (
            <p className="form-error">{history.error.message}</p>
          ) : !history.data ? (
            <p className="al-hint">Loading…</p>
          ) : history.data.history.length === 0 ? (
            <p className="al-hint">No changes recorded yet. Changes made from now on are listed here.</p>
          ) : (
            <ol className="al-history">
              {history.data.history.map((h) => (
                <li key={h.id}>
                  <span className="mono al-hint">{formatLogTime(h.at)}</span>
                  <span>
                    <strong>{ACTION_LABEL[h.action] ?? titleCase(h.action)}</strong>
                    {h.actor && <> by {h.actor}</>}
                    {h.action === "update" && h.changes.length > 0 && <span className="al-hint"> · {h.changes.join(", ")}</span>}
                  </span>
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
    </section>
  );
}
