import { useEffect, useMemo, useState } from "react";
import { EyeOff, FlaskConical, Pencil, Plus, Power, Trash2, UserRound, X } from "lucide-react";
import { alertsApi } from "../lib/alerts";
import { logsHref } from "../lib/applogs";
import { traceHref } from "../lib/traces";
import { formatCount, humanize, titleCase } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { useSession } from "../lib/team";
import { formatLogTime } from "../lib/time";
import {
  LOGIC_LABELS,
  SOURCE_LABELS,
  mitreHref,
  mitreLabel,
  rulesApi,
  type DetectionRule,
  type Finding,
  type FindingDetail,
  type RuleLogic,
  type RulePreview,
  type RuleSource,
  type RulesList,
  type RuleTemplate,
  type RuleView,
} from "../lib/security";
import { DataTable, type Column } from "../components/DataTable";
import { Drawer, Modal } from "../components/Modal";
import { Section } from "../components/Section";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { linkify } from "../components/ErrorNotice";
import { SUPPRESS_DAYS, localResult } from "./Security.triage";

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const FILTERS: Record<string, { label: string; placeholder?: string; options?: { value: string; label: string }[]; hint?: string }> = {
  status: { label: "Status", placeholder: "401,403 or 4xx or >=500" },
  method: { label: "Method", placeholder: "POST" },
  route: { label: "Route", placeholder: "/api/login, *admin*", hint: "Comma separated; * matches anything, a word without / matches anywhere." },
  path: { label: "Path or query contains", placeholder: "../, union select", hint: "Matched against the path and the query string, any case." },
  ip: { label: "IP", placeholder: "203.0.113.9" },
  user: { label: "User", placeholder: "alice@example.com" },
  role: { label: "Role", placeholder: "admin" },
  service: { label: "Service", placeholder: "checkout" },
  user_agent: { label: "User agent contains", placeholder: "sqlmap, nikto" },
  login: {
    label: "Routes",
    options: [
      { value: "", label: "Any route" },
      { value: "yes", label: "Login routes only" },
    ],
  },
  auth_failed: {
    label: "Outcome",
    options: [
      { value: "", label: "Any outcome" },
      { value: "yes", label: "Failed sign-in or auth error" },
      { value: "no", label: "Succeeded (status under 400)" },
    ],
  },
  query: { label: "Search", placeholder: 'level:error "invalid token" service:auth', hint: "The Logs search syntax: words, field:value, level:warn+, OR, -term." },
  kind: { label: "Kind", placeholder: "ssh_failed, sudo" },
  source: { label: "Source", placeholder: "host, api" },
  host: { label: "Host", placeholder: "web-1" },
  target: { label: "Target contains", placeholder: "/etc/sudoers" },
  min_severity: {
    label: "Severity",
    options: [
      { value: "", label: "Any severity" },
      { value: "warning", label: "Warning and up" },
      { value: "critical", label: "Critical only" },
    ],
  },
  action: { label: "Action", placeholder: "auth.sign_in, api_key.*", hint: "Audit actions; a trailing * matches a prefix." },
};

const LOGIC_TEXT: Record<RuleLogic, string> = {
  threshold: "Count matches in the window per group; signal when the count (or the number of different values) reaches the threshold.",
  new_value: "Signal the first time a group shows a value it has not had in the learning period: a new country for a user, a new IP for an admin.",
  sequence: "Signal when a group does the first step at least N times and then the second step within the window: failed logins, then a success.",
  anomaly: "Signal when a group's count is far above its own usual count in recent runs, and at least the minimum.",
};

const BLANK: Partial<DetectionRule> = {
  name: "",
  enabled: true,
  severity: "warning",
  source: "requests",
  logic: "threshold",
  window: "10m",
  threshold: 10,
  group_by: ["ip"],
  filter: {},
};

export function RulesTab({ openRule }: { openRule?: number }) {
  const { tick } = useApp();
  const { can } = useSession();
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`sec-rules#${tick}-${run}`, (signal) => rulesApi.list(signal));
  const [editing, setEditing] = useState<Partial<DetectionRule> | null>(null);
  const [busy, setBusy] = useState("");
  const reload = () => setRun((n) => n + 1);
  const data = q.data;

  useEffect(() => {
    if (!openRule || !data) return;
    const r = data.rules.find((x) => x.id === openRule);
    if (r) setEditing(r);
  }, [openRule, data]);

  const toggle = async (r: RuleView) => {
    setBusy(`t${r.id}`);
    try {
      await rulesApi.setEnabled(r.id, !r.enabled);
      reload();
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy("");
    }
  };
  const turnOn = async (t: RuleTemplate) => {
    setBusy(t.id);
    try {
      const r = await rulesApi.fromTemplate(t.id);
      toast("ok", `“${r.name}” is on.`);
      reload();
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy("");
    }
  };

  const rules = data?.rules ?? [];
  const result = useMemo(
    () => localResult(["id", "enabled", "name", "how", "mitre", "severity", "signals", "last"], rules.map((r) => [r.id, r.enabled, r.name, r.id, r.id, r.severity, r.signals, r.id])),
    [rules],
  );
  const byId = useMemo(() => new Map(rules.map((r) => [r.id, r])), [rules]);
  const columns: Column[] = [
    {
      key: "enabled",
      label: "On",
      width: 56,
      render: (_, row) => {
        const r = byId.get(Number(row[0]))!;
        return (
          <button
            type="button"
            className={`icon-btn sec-rule-power ${r.enabled ? "on" : ""}`}
            aria-label={r.enabled ? `Turn off ${r.name}` : `Turn on ${r.name}`}
            title={r.enabled ? "On: click to turn off" : "Off: click to turn on"}
            disabled={!can("edit") || busy !== ""}
            onClick={(e) => {
              e.stopPropagation();
              void toggle(r);
            }}
          >
            <Power size={14} />
          </button>
        );
      },
    },
    {
      key: "name",
      label: "Rule",
      render: (_, row) => {
        const r = byId.get(Number(row[0]))!;
        return (
          <div className="sec-rule-name">
            <span>{r.name}</span>
            {r.description && <span className="dim">{r.description}</span>}
          </div>
        );
      },
    },
    {
      key: "how",
      label: "Reads",
      width: 200,
      render: (_, row) => {
        const r = byId.get(Number(row[0]))!;
        return (
          <span className="dim">
            {SOURCE_LABELS[r.source]} · {LOGIC_LABELS[r.logic]}
            {r.group_by?.length ? ` by ${r.group_by.join(", ")}` : ""}
          </span>
        );
      },
    },
    { key: "mitre", label: "MITRE ATT&CK", width: 150, render: (_, row) => <MitreTags rule={byId.get(Number(row[0]))!} list={data} /> },
    { key: "severity", width: 90, render: (v) => <span className={`sec-sev ${String(v)}`}>{titleCase(String(v))}</span> },
    { key: "signals", label: "Signals (7d)", align: "right", width: 100, render: (v) => formatCount(Number(v) || 0) },
    {
      key: "last",
      label: "Last run",
      width: 170,
      render: (_, row) => {
        const r = byId.get(Number(row[0]))!;
        if (r.error) return <span className="flag bad" title={r.error}>Failed: {r.error}</span>;
        if (!r.enabled) return <span className="dim">Off</span>;
        return <span className="mono dim">{r.last_run ? formatLogTime(r.last_run) : `Every ${r.every}`}</span>;
      },
    },
  ];

  return (
    <div className="grid">
      <Section
        title="Detection rules"
        text="Your own detectors over requests, app logs, host and detector events, and Visionary's audit log. What they find shows under Signals and in Findings, with the same triage, suppressions and alerts."
      />
      <section className="panel span-12" aria-busy={q.loading || undefined}>
        {q.loading && <div className="progress" aria-hidden />}
        <header className="panel-head sec-rules-head">
          <h3 className="panel-title">{rules.length === 0 ? "No rules yet" : `${rules.length} rule${rules.length === 1 ? "" : "s"}`}</h3>
          {can("edit") && (
            <button type="button" className="btn primary sm" onClick={() => setEditing({ ...BLANK })}>
              <Plus size={14} />
              New rule
            </button>
          )}
        </header>
        {q.error && !data && <div className="panel-state error">{linkify(q.error.message)}</div>}
        {data && rules.length === 0 && <div className="panel-state">Turn on a rule from the library below, or write your own.</div>}
        {rules.length > 0 && <DataTable result={result} columns={columns} sortable={false} maxHeight={520} rowKey={(r) => r[0]} onRowClick={(row) => setEditing(byId.get(Number(row[0]))!)} />}
      </section>

      <Section title="Rule library" text="Ready-made rules, each tagged with its MITRE ATT&CK tactic and technique. Turn one on as it is, or open it to change it first." />
      <div className="span-12 sec-library">
        {(data?.templates ?? []).map((t) => (
          <article key={t.id} className="sec-template">
            <div className="sec-template-head">
              <span className={`sec-sev ${t.severity}`}>{titleCase(t.severity)}</span>
              <h4>{t.name}</h4>
            </div>
            <p>{t.description}</p>
            <div className="sec-template-tags">
              <span className="pill">{SOURCE_LABELS[t.source]}</span>
              <span className="pill">{LOGIC_LABELS[t.logic]}</span>
              <a className="pill" href={mitreHref(t.technique)} target="_blank" rel="noreferrer" title={mitreLabel(t.technique, data?.techniques)}>
                {t.technique}
              </a>
            </div>
            {t.needs && <p className="dim sec-template-needs">Needs {t.needs}.</p>}
            <div className="sec-template-foot">
              {t.rule_id ? (
                <button type="button" className="btn sm" onClick={() => setEditing(byId.get(t.rule_id!) ?? null)}>
                  <Pencil size={14} />
                  Added: edit
                </button>
              ) : (
                can("edit") && (
                  <>
                    <button type="button" className="btn primary sm" disabled={busy !== ""} onClick={() => void turnOn(t)}>
                      <Power size={14} />
                      {busy === t.id ? "Turning on…" : "Turn on"}
                    </button>
                    <button type="button" className="btn ghost sm" onClick={() => setEditing(fromTemplate(t))}>
                      Customize
                    </button>
                  </>
                )
              )}
            </div>
          </article>
        ))}
      </div>
      {editing && data && (
        <RuleEditor
          rule={editing}
          list={data}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

function fromTemplate(t: RuleTemplate): Partial<DetectionRule> {
  const { id, needs: _needs, rule_id: _rule, ...rest } = t;
  return { ...rest, template: id, threshold: t.threshold ?? 1, enabled: true, filter: { ...t.filter }, then: t.then ? { ...t.then } : undefined };
}

export function MitreTags({ rule, list }: { rule: Pick<DetectionRule, "tactic" | "technique">; list?: Pick<RulesList, "tactics" | "techniques"> }) {
  if (!rule.tactic && !rule.technique) return <span className="dim">—</span>;
  return (
    <span className="sec-mitre">
      {rule.tactic && (
        <a className="pill" href={mitreHref(rule.tactic)} target="_blank" rel="noreferrer" title={mitreLabel(rule.tactic, list?.tactics)} onClick={(e) => e.stopPropagation()}>
          {list ? mitreLabel(rule.tactic, list.tactics).replace(/^TA\d+ /, "") : rule.tactic}
        </a>
      )}
      {rule.technique && (
        <a className="pill mono" href={mitreHref(rule.technique)} target="_blank" rel="noreferrer" title={mitreLabel(rule.technique, list?.techniques)} onClick={(e) => e.stopPropagation()}>
          {rule.technique}
        </a>
      )}
    </span>
  );
}

function FilterFields({ source, keys, value, onChange }: { source: RuleSource; keys: string[]; value: Record<string, string>; onChange: (v: Record<string, string>) => void }) {
  const set = (k: string, v: string) => onChange({ ...value, [k]: v });
  return (
    <div className="sec-rule-filters">
      {keys.map((k) => {
        const meta = FILTERS[k] ?? { label: humanize(k) };
        if (meta.options) {
          return (
            <div className="field" key={k}>
              <span>{meta.label}</span>
              <Select label={meta.label} align="left" value={value[k] ?? ""} options={meta.options} onChange={(v) => set(k, v)} />
            </div>
          );
        }
        const wide = k === "query" || k === "path";
        return (
          <label className={`field ${wide ? "wide" : ""}`} key={`${source}-${k}`}>
            <span>{meta.label}</span>
            <input className="input mono" value={value[k] ?? ""} placeholder={meta.placeholder} onChange={(e) => set(k, e.target.value)} />
            {meta.hint && <small>{meta.hint}</small>}
          </label>
        );
      })}
    </div>
  );
}

function RuleEditor({ rule, list, onClose, onSaved }: { rule: Partial<DetectionRule>; list: RulesList; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const { can } = useSession();
  const [form, setForm] = useState<Partial<DetectionRule>>(() => ({ ...BLANK, ...rule, filter: { ...rule.filter }, suppress: [...(rule.suppress ?? [])] }));
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<RulePreview | null>(null);
  const [testing, setTesting] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const channels = useQuery("sec-rule-channels", (signal) => alertsApi.channels(signal));
  const set = <K extends keyof DetectionRule>(k: K, v: DetectionRule[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
    setPreview(null);
  };
  const source = (form.source ?? "requests") as RuleSource;
  const logic = (form.logic ?? "threshold") as RuleLogic;
  const fields = (list.fields[source] ?? []).filter((f) => f !== "country").sort();
  const groupBy = form.group_by ?? [];
  const editable = can("edit");
  const saved = !!form.id;

  const body = (): Partial<DetectionRule> => {
    const b: Partial<DetectionRule> = { ...form, threshold: Number(form.threshold) || 0, factor: Number(form.factor) || 0 };
    if (logic !== "sequence") delete b.then;
    return b;
  };
  const test = async () => {
    setErr("");
    setTesting(true);
    try {
      setPreview(await rulesApi.test(body()));
    } catch (e) {
      setErr(errMessage(e));
    } finally {
      setTesting(false);
    }
  };
  const submit = async () => {
    setErr("");
    setBusy(true);
    try {
      if (saved) await rulesApi.update(form.id!, body());
      else await rulesApi.create(body());
      toast("ok", saved ? `Saved “${form.name}”` : `Created “${form.name}”`);
      onSaved();
    } catch (e) {
      setErr(errMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    try {
      await rulesApi.remove(form.id!);
      toast("ok", `Deleted “${form.name}”`);
      onSaved();
    } catch (e) {
      toast("err", errMessage(e));
    }
    setConfirm(false);
  };

  const sourceChange = (v: string) => {
    const next = v as RuleSource;
    setForm((f) => ({ ...f, source: next, filter: {}, then: f.then ? {} : undefined, group_by: (f.group_by ?? []).filter((g) => list.fields[next]?.includes(g)), distinct: "", field: "" }));
    setPreview(null);
  };
  const toggleGroup = (g: string) => {
    const has = groupBy.includes(g);
    if (!has && groupBy.length >= 3) return;
    set("group_by", has ? groupBy.filter((x) => x !== g) : [...groupBy, g]);
  };
  const watchable = logic === "new_value" ? [...fields.filter((f) => !groupBy.includes(f)), ...(list.fields[source]?.includes("country") ? ["country"] : [])] : [];
  const supFields = [...groupBy, ...(logic === "new_value" && form.field ? [form.field] : [])];
  const suppress = form.suppress ?? [];
  const setSup = (i: number, k: "field" | "value" | "until" | "note", v: string) =>
    set(
      "suppress",
      suppress.map((s, j) => (j === i ? { ...s, [k]: k === "until" && v ? new Date(`${v}T23:59:59Z`).toISOString() : v } : s)),
    );

  return (
    <Drawer title={saved ? `Edit ${rule.name}` : form.template ? `New rule: ${form.name}` : "New detection rule"} onClose={onClose} className="sec-rule-drawer">
      <form
        className="up-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <label className="field">
          <span>Name</span>
          <input className="input" value={form.name ?? ""} onChange={(e) => set("name", e.target.value)} placeholder="Brute force on login" maxLength={120} required autoFocus />
        </label>
        <label className="field">
          <span>Description</span>
          <input className="input" value={form.description ?? ""} onChange={(e) => set("description", e.target.value)} placeholder="What it catches, for whoever triages it" maxLength={1000} />
        </label>

        <div className="up-row">
          <div className="field" style={{ flex: 1 }}>
            <span>Reads</span>
            <Select label="Source" align="left" value={source} onChange={sourceChange} options={(Object.keys(SOURCE_LABELS) as RuleSource[]).map((s) => ({ value: s, label: SOURCE_LABELS[s] }))} />
          </div>
          <div className="field" style={{ flex: 1 }}>
            <span>Logic</span>
            <Select
              label="Logic"
              align="left"
              value={logic}
              onChange={(v) => {
                set("logic", v as RuleLogic);
                if (v === "sequence" && !form.then) set("then", {});
              }}
              options={(Object.keys(LOGIC_LABELS) as RuleLogic[]).map((l) => ({ value: l, label: LOGIC_LABELS[l] }))}
            />
          </div>
        </div>
        <p className="dim sec-rule-help">{LOGIC_TEXT[logic]}</p>

        <fieldset className="sec-rule-step">
          <legend>{logic === "sequence" ? "First step: match" : "Match"}</legend>
          <FilterFields source={source} keys={list.filters[source] ?? []} value={form.filter ?? {}} onChange={(v) => set("filter", v)} />
        </fieldset>
        {logic === "sequence" && (
          <fieldset className="sec-rule-step">
            <legend>Then, by the same group</legend>
            <FilterFields source={source} keys={list.filters[source] ?? []} value={form.then ?? {}} onChange={(v) => set("then", v)} />
          </fieldset>
        )}

        <div className="field">
          <span>Group by {logic === "sequence" ? "(required)" : "(up to 3)"}</span>
          <div className="sec-chips" role="group" aria-label="Group by">
            {fields.map((f) => (
              <button type="button" key={f} className={`sec-chip ${groupBy.includes(f) ? "on" : ""}`} aria-pressed={groupBy.includes(f)} onClick={() => toggleGroup(f)}>
                {f}
              </button>
            ))}
          </div>
          <small>{groupBy.length === 0 ? "No groups: the rule counts everything together." : `One signal per ${groupBy.join(" and ")}.`}</small>
        </div>

        <div className="up-row">
          <label className="field" style={{ flex: 1 }}>
            <span>Window</span>
            <input className="input mono" value={form.window ?? ""} onChange={(e) => set("window", e.target.value)} placeholder="10m" />
            <small>1m to 24h. Runs every {list.rules.find((r) => r.id === form.id)?.every ?? "fifth of it, 1–10 minutes"}.</small>
          </label>
          {logic !== "new_value" && (
            <label className="field" style={{ flex: 1 }}>
              <span>{logic === "sequence" ? "First step at least" : logic === "anomaly" ? "Minimum count" : "Threshold"}</span>
              <input className="input" type="number" min={1} value={form.threshold ?? ""} onChange={(e) => set("threshold", Number(e.target.value))} />
            </label>
          )}
          {logic === "anomaly" && (
            <label className="field" style={{ flex: 1 }}>
              <span>Times the usual</span>
              <input className="input" type="number" min={1.5} max={100} step={0.5} value={form.factor || 3} onChange={(e) => set("factor", Number(e.target.value))} />
            </label>
          )}
          {(logic === "threshold" || logic === "anomaly") && (
            <div className="field" style={{ flex: 1 }}>
              <span>Count</span>
              <Select
                label="Count"
                align="left"
                value={form.distinct ?? ""}
                onChange={(v) => set("distinct", v)}
                options={[{ value: "", label: "Matches" }, ...fields.filter((f) => !groupBy.includes(f)).map((f) => ({ value: f, label: `Different ${f}` }))]}
              />
            </div>
          )}
        </div>

        {logic === "new_value" && (
          <div className="up-row">
            <div className="field" style={{ flex: 1 }}>
              <span>Watch for new</span>
              <Select
                label="Field"
                align="left"
                value={form.field ?? ""}
                onChange={(v) => set("field", v)}
                options={[{ value: "", label: "Choose a field" }, ...watchable.map((f) => ({ value: f, label: f }))]}
              />
              {form.field === "country" && !list.geo && <small className="flag warn">Needs a location database in Settings → Server → Threat intelligence.</small>}
            </div>
            <label className="field" style={{ flex: 1 }}>
              <span>Learning period</span>
              <input className="input mono" value={form.learn ?? ""} onChange={(e) => set("learn", e.target.value)} placeholder="7d" />
              <small>Values seen in this period before are not new. 1h to 30d.</small>
            </label>
          </div>
        )}

        <div className="up-row">
          <div className="field" style={{ flex: 1 }}>
            <span>Severity</span>
            <Select
              label="Severity"
              align="left"
              value={form.severity ?? "warning"}
              onChange={(v) => set("severity", v as DetectionRule["severity"])}
              options={["info", "warning", "critical"].map((s) => ({ value: s, label: titleCase(s) }))}
            />
          </div>
          <div className="field" style={{ flex: 1.4 }}>
            <span>MITRE ATT&CK tactic</span>
            <Select
              label="Tactic"
              align="left"
              maxWidth={260}
              value={form.tactic ?? ""}
              onChange={(v) => set("tactic", v)}
              options={[{ value: "", label: "None" }, ...list.tactics.map((t) => ({ value: t.id, label: `${t.id} ${t.name}` }))]}
            />
          </div>
          <label className="field" style={{ flex: 1 }}>
            <span>Technique</span>
            <input className="input mono" list="sec-techniques" value={form.technique ?? ""} onChange={(e) => set("technique", e.target.value.toUpperCase())} placeholder="T1110.004" />
            <datalist id="sec-techniques">
              {list.techniques.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </datalist>
            {form.technique && <small>{mitreLabel(form.technique, list.techniques)}</small>}
          </label>
        </div>

        <div className="field">
          <span>Notify</span>
          {channels.data && channels.data.channels.length === 0 && <small>No channels yet; add one in Alerts → Channels.</small>}
          <div className="sec-chips">
            {(channels.data?.channels ?? []).map((c) => {
              const on = (form.channels ?? []).includes(c.id);
              return (
                <button
                  type="button"
                  key={c.id}
                  className={`sec-chip ${on ? "on" : ""}`}
                  aria-pressed={on}
                  onClick={() => set("channels", on ? (form.channels ?? []).filter((x) => x !== c.id) : [...(form.channels ?? []), c.id])}
                >
                  {c.name}
                </button>
              );
            })}
          </div>
          <small>New signals go to these channels as they are found. Alerts on Security findings (source: rule) work too.</small>
        </div>

        <div className="field">
          <span>Suppressions</span>
          {suppress.map((s, i) => (
            <div className="up-row" key={i}>
              <div style={{ width: 130 }}>
                <Select
                  label="Field"
                  align="left"
                  value={s.field}
                  onChange={(v) => setSup(i, "field", v)}
                  options={supFields.length ? supFields.map((f) => ({ value: f, label: f })) : [{ value: s.field, label: s.field || "Group first" }]}
                />
              </div>
              <input className="input mono" style={{ flex: 1 }} value={s.value} placeholder="Value" aria-label="Value" onChange={(e) => setSup(i, "value", e.target.value)} />
              <input
                className="input"
                type="date"
                style={{ width: 150 }}
                aria-label="Until"
                title="Until (empty: forever)"
                value={s.until ? s.until.slice(0, 10) : ""}
                onChange={(e) => setSup(i, "until", e.target.value)}
              />
              <button type="button" className="icon-btn" aria-label="Remove suppression" onClick={() => set("suppress", suppress.filter((_, j) => j !== i))}>
                <X size={15} />
              </button>
            </div>
          ))}
          <div>
            <button
              type="button"
              className="btn ghost sm"
              disabled={supFields.length === 0}
              onClick={() => set("suppress", [...suppress, { field: supFields[0] ?? "", value: "" }])}
            >
              <Plus size={14} />
              Add suppression
            </button>
          </div>
          <small>Skips groups whose field has this value, until the date or forever: your own monitors, a known partner.</small>
        </div>

        <label className="field">
          <span>Runbook</span>
          <input className="input" value={form.runbook ?? ""} onChange={(e) => set("runbook", e.target.value)} placeholder="What to do, or a link to it" maxLength={500} />
        </label>

        <fieldset className="up-checks">
          <label>
            <input type="checkbox" checked={form.enabled ?? true} onChange={(e) => set("enabled", e.target.checked)} />
            On: run this rule in the background
          </label>
        </fieldset>

        <div className="sec-rule-test">
          <button type="button" className="btn" onClick={() => void test()} disabled={testing}>
            <FlaskConical size={14} />
            {testing ? "Testing…" : "Test against the last 24 hours"}
          </button>
          {preview && <PreviewResult preview={preview} />}
        </div>

        {err && <div className="form-error">{linkify(err)}</div>}

        <div className="up-form-foot">
          {saved && editable && (
            <button type="button" className="btn ghost" onClick={() => setConfirm(true)}>
              <Trash2 size={14} />
              Delete
            </button>
          )}
          <span style={{ flex: 1 }} />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          {editable && (
            <button type="submit" className="btn primary" disabled={busy}>
              {saved ? "Save changes" : "Create rule"}
            </button>
          )}
        </div>
      </form>
      {confirm && (
        <Modal
          title="Delete rule"
          narrow
          onClose={() => setConfirm(false)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setConfirm(false)}>
                Cancel
              </button>
              <button type="button" className="btn danger" onClick={() => void remove()}>
                Delete rule
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>Delete “{form.name}”? Its signals stay in Findings; what it learned for new values is forgotten. To stop it for now, turn it off instead.</p>
        </Modal>
      )}
    </Drawer>
  );
}

export const RULE_DETAIL = new Set(["samples", "group", "rule", "rule_id", "tactic", "technique", "logic", "first", "last", "new_value", "field"]);

export function SignalDetail({ finding, detail, onTimeline }: { finding: Finding; detail: FindingDetail | undefined; onTimeline: (account: string) => void }) {
  const { can } = useSession();
  const toast = useToast();
  const [suppressing, setSuppressing] = useState<string | null>(null);
  const [days, setDays] = useState("30");
  const d = finding.detail ?? {};
  const rule = detail?.rule;
  const ruleId = Number(d.rule_id) || rule?.id;
  const group = (d.group ?? {}) as Record<string, string>;
  const values: [string, string][] = [...Object.entries(group), ...(typeof d.new_value === "string" && typeof d.field === "string" ? [[d.field, d.new_value] as [string, string]] : [])];
  const range = detail ? { from: detail.from, to: detail.to } : undefined;

  const suppress = async (field: string, value: string) => {
    if (!ruleId) return;
    try {
      await rulesApi.suppress(ruleId, { field, value, days: Number(days) });
      toast("ok", `${field}=${value} won't raise this rule again${days === "0" ? "" : ` for ${days} days`}.`);
      setSuppressing(null);
    } catch (e) {
      toast("err", errMessage(e));
    }
  };
  const pivot = (field: string, value: string) => {
    if (!value) return null;
    if (field === "ip") return <a className="btn sm" href={`#/logs?ip=${encodeURIComponent(value)}`}>Requests</a>;
    if (field === "user" || field === "account") {
      return (
        <>
          <a className="btn sm" href={`#/logs?user=${encodeURIComponent(value)}`}>
            Requests
          </a>
          {rule?.source === "requests" && (
            <button type="button" className="btn sm" onClick={() => onTimeline(value)}>
              <UserRound size={14} />
              Timeline
            </button>
          )}
        </>
      );
    }
    if (field === "service") return <a className="btn sm" href={logsHref(`service:${value}`, range)}>Logs</a>;
    if (field === "trace") return <a className="btn sm" href={traceHref(value)}>Trace</a>;
    return null;
  };

  const events = detail?.events;
  const columns: Column[] = useMemo(() => {
    if (!events) return [];
    const cols: Column[] = [];
    for (const c of events.columns) {
      if (c === "id" || (c === "step" && rule?.logic !== "sequence")) continue;
      if (c === "step") cols.push({ key: c, label: "Step", width: 60, render: (v) => (Number(v) === 1 ? <span className="dim">first</span> : <span className="flag warn">then</span>) });
      else if (c === "created_at") cols.push({ key: c, label: "Time", width: 150, render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span> });
      else if (c === "request_id")
        cols.push({ key: c, label: "Request", width: 90, render: (v) => (v ? <a href={logsHref(`request:${String(v)}`, range)}>Its logs</a> : <span className="dim">—</span>) });
      else if (c === "trace_id") cols.push({ key: c, label: "Trace", width: 70, render: (v) => (v ? <a href={traceHref(String(v))}>Open</a> : <span className="dim">—</span>) });
      else if (c === "message") cols.push({ key: c, render: (v) => <span className="mono sec-rule-msg">{String(v ?? "")}</span> });
      else cols.push({ key: c, render: (v) => (v === null || v === "" ? <span className="dim">—</span> : <span className="mono">{String(v)}</span>) });
    }
    return cols;
  }, [events, rule?.logic, range?.from, range?.to]);

  return (
    <>
      <div className="section-label">Detection rule</div>
      <div className="sec-signal">
        <div className="sec-signal-row">
          <span>{rule ? `${SOURCE_LABELS[rule.source]} · ${LOGIC_LABELS[rule.logic]}${d.window ? ` over ${String(d.window)}` : ""}` : String(d.rule ?? finding.kind)}</span>
          {ruleId ? (
            <a className="btn sm" href={`#/security?tab=rules&rule=${ruleId}`}>
              <Pencil size={14} />
              Edit rule
            </a>
          ) : (
            <span className="dim">The rule was deleted.</span>
          )}
        </div>
        <MitreTags rule={{ tactic: d.tactic as string | undefined, technique: d.technique as string | undefined }} />
        {values.length > 0 && (
          <ul className="sec-signal-group">
            {values.map(([k, v]) => (
              <li key={k}>
                <span className="dim">{k}</span>
                <span className="mono">{v || "(empty)"}</span>
                <span className="sec-signal-pivots">
                  {pivot(k, v)}
                  {can("edit") && ruleId && v && (
                    <button type="button" className="btn ghost sm" onClick={() => setSuppressing(suppressing === k ? null : k)} aria-expanded={suppressing === k}>
                      <EyeOff size={14} />
                      Suppress
                    </button>
                  )}
                </span>
                {suppressing === k && (
                  <div className="sec-suppress-row">
                    <span className="dim">Skip {k}={v} in this rule</span>
                    <Select label="How long" align="left" value={days} options={SUPPRESS_DAYS} onChange={setDays} />
                    <button type="button" className="btn primary sm" onClick={() => void suppress(k, v)}>
                      Suppress
                    </button>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
        {Boolean(d.first || d.last) && (
          <p className="dim">
            Matched from {d.first ? formatLogTime(String(d.first)) : "—"} to {d.last ? formatLogTime(String(d.last)) : "—"}.
          </p>
        )}
      </div>
      <div className="section-label">Matched events</div>
      {events && (
        <>
          <p className="dim" style={{ marginTop: 4 }}>
            This group's matches between {formatLogTime(detail!.from)} and {formatLogTime(detail!.to)}, newest first{events.truncated ? " (newest 200)" : ""}.
          </p>
          {events.rows.length === 0 ? <p className="dim">None left in that range; the data may have aged out.</p> : <DataTable result={events} columns={columns} sortable={false} maxHeight={420} />}
        </>
      )}
      {detail && !events && <p className="dim">The rule behind this signal no longer exists, so its events can't be matched again.</p>}
    </>
  );
}

function PreviewResult({ preview }: { preview: RulePreview }) {
  const suppressed = preview.total - preview.signals;
  const result = useMemo(
    () =>
      localResult(
        ["at", "group", "count", "summary"],
        (preview.hits ?? []).map((h) => [h.at, Object.entries(h.group).map(([k, v]) => `${k}=${v}`).join(" ") + (h.value ? ` → ${h.value}` : ""), h.count, h.suppressed ? `${h.summary} (suppressed)` : h.summary]),
      ),
    [preview],
  );
  const columns: Column[] = [
    { key: "at", label: "When", width: 150, render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span> },
    { key: "group", render: (v) => <span className="mono">{String(v) || "everything"}</span> },
    { key: "count", align: "right", width: 70, unit: "count" },
    { key: "summary", label: "What it would say" },
  ];
  return (
    <div className="sec-rule-preview">
      <p>
        {preview.signals === 0
          ? "Nothing in the last 24 hours would have matched."
          : `Would have raised ${formatCount(preview.signals)} signal${preview.signals === 1 ? "" : "s"} for ${formatCount(preview.groups)} group${preview.groups === 1 ? "" : "s"}`}
        {suppressed > 0 ? `; ${formatCount(suppressed)} more suppressed.` : preview.signals > 0 ? "." : ""}
        {preview.note && <span className="dim"> {preview.note}</span>}
      </p>
      {(preview.hits ?? []).length > 0 && <DataTable result={result} columns={columns} sortable={false} maxHeight={280} />}
    </div>
  );
}
