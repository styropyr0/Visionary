import { useMemo, useState } from "react";
import { ArrowDown, ArrowUp, Gauge, Plus, Power, Settings2, ShieldAlert, Trash2, X } from "lucide-react";
import type { Result, Viz } from "../lib/api";
import { formatCount } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatLogTime } from "../lib/time";
import {
  KEY_OPTIONS,
  OUTCOME_LABELS,
  propLabel,
  rateLimitsApi,
  ruleNeedsUser,
  ruleSummary,
  scopeSummary,
  windowLabel,
  type RLAction,
  type RLActivity,
  type RLConfig,
  type RLRule,
  type RLRuleStats,
  type RLView,
} from "../lib/ratelimits";
import { DataTable, type Column } from "../components/DataTable";
import { Drawer } from "../components/Modal";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { Select } from "../components/Select";
import { StatTile } from "../components/StatTile";
import { useToast } from "../components/Toast";
import { useConfirm } from "../components/Confirm";
import { linkify } from "../components/ErrorNotice";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { localResult } from "./Security.triage";
import { Switch } from "./Settings.profiling";

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const BLANK: RLRule = { name: "", action: "limit", mode: "monitor", key: "ip", limit: 60, window_s: 60, algorithm: "" };

const ACTIONS: { value: RLAction; label: string; text: string }[] = [
  { value: "limit", label: "Limit", text: "Count matching requests per key and refuse them with 429 and Retry-After past the limit." },
  { value: "block", label: "Block", text: "Refuse every matching request, with 403 unless you pick another status. Use it for blocklists and countries." },
  { value: "allow", label: "Allow", text: "Exempt matching requests from every rule below this one: office IPs, health checks, internal callers." },
];

const ALGORITHMS = [
  { value: "", label: "Sliding window" },
  { value: "fixed", label: "Fixed window" },
  { value: "token_bucket", label: "Token bucket" },
];

const UNITS = [
  { value: "1", label: "seconds" },
  { value: "60", label: "minutes" },
  { value: "3600", label: "hours" },
  { value: "86400", label: "days" },
];

/** Shows a stored rule list as text, one entry per line, and back. */
const lines = (v?: string[]) => (v ?? []).join("\n");
const parseLines = (s: string) =>
  s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean);

function refused(s: RLRuleStats | undefined): number {
  return s ? s.limited + s.blocked : 0;
}

function monitored(s: RLRuleStats | undefined): number {
  return s ? s.would_limit + s.would_block : 0;
}

export function RateLimitsTab() {
  const { resolved, tick } = useApp();
  const toast = useToast();
  const ask = useConfirm();
  const [run, setRun] = useState(0);
  const view = useQuery(`rl#${tick}-${run}`, (signal) => rateLimitsApi.get(signal));
  const act = useQuery(`rl-act#${tick}-${run}-${resolved.from.toISOString()}-${resolved.to.toISOString()}`, (signal) => rateLimitsApi.activity(resolved.from, resolved.to, signal));
  const [editing, setEditing] = useState<{ rule: RLRule; index: number } | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const tokens = useChartTokens();

  const data = view.data;
  const config = data?.config;
  const rules = config?.rules ?? [];
  const canEdit = !!data?.can_edit;
  const stats = useMemo(() => new Map((act.data?.rules ?? []).map((r) => [r.rule_id, r])), [act.data]);

  const save = async (next: RLConfig, done: string) => {
    setBusy(true);
    try {
      await rateLimitsApi.save(next);
      toast("ok", done);
      setRun((n) => n + 1);
      return true;
    } catch (e) {
      toast("err", errMessage(e));
      return false;
    } finally {
      setBusy(false);
    }
  };
  const withRules = (list: RLRule[]): RLConfig => ({ ...config!, rules: list });
  const move = (i: number, by: number) => {
    const list = [...rules];
    const [r] = list.splice(i, 1);
    list.splice(i + by, 0, r);
    void save(withRules(list), "Order saved. Services follow it within 15 seconds.");
  };
  const toggle = (i: number) => {
    const r = rules[i];
    const on = r.enabled === false;
    void save(withRules(rules.map((x, j) => (j === i ? { ...x, enabled: on } : x))), on ? `“${r.name || "Rule"}” is on.` : `“${r.name || "Rule"}” is off.`);
  };
  const remove = async (i: number) => {
    const r = rules[i];
    const ok = await ask({ title: "Delete rule", message: `Delete “${r.name || "this rule"}”? Services stop applying it within 15 seconds. What it refused stays in the activity below.`, confirm: "Delete rule", danger: true });
    if (ok && (await save(withRules(rules.filter((_, j) => j !== i)), "Rule deleted."))) setEditing(null);
  };

  const totals = useMemo(() => {
    const list = act.data?.rules ?? [];
    return {
      limited: list.reduce((a, r) => a + r.limited, 0),
      blocked: list.reduce((a, r) => a + r.blocked, 0),
      would: list.reduce((a, r) => a + monitored(r), 0),
      clients: act.data?.clients.filter((c) => c.enforced > 0).length ?? 0,
    };
  }, [act.data]);

  const series: Result = useMemo(
    () => ({
      ...localResult(
        ["t", "Refused", "Would refuse"],
        (act.data?.series ?? []).map((p) => [p.t, p.enforced, p.monitored]),
      ),
      bucket: act.data?.bucket ?? 0,
      from: resolved.from.toISOString(),
      to: resolved.to.toISOString(),
    }),
    [act.data, resolved],
  );
  const viz: Viz = { type: "timeseries", x: "t", y: ["Refused", "Would refuse"], stack: true, unit: "count" };

  const ruleTable = useMemo(
    () => localResult(["i", "on", "order", "rule", "what", "mode", "refused"], rules.map((_, i) => [i, i, i, i, i, i, i])),
    [rules],
  );
  const columns: Column[] = [
    {
      key: "on",
      label: "On",
      width: 56,
      render: (_, row) => {
        const i = Number(row[0]);
        const r = rules[i];
        const on = r.enabled !== false;
        return (
          <button
            type="button"
            className={`icon-btn sec-rule-power ${on ? "on" : ""}`}
            aria-label={on ? `Turn off ${r.name}` : `Turn on ${r.name}`}
            title={on ? "On: click to turn off" : "Off: click to turn on"}
            disabled={!canEdit || busy}
            onClick={(e) => {
              e.stopPropagation();
              toggle(i);
            }}
          >
            <Power size={14} />
          </button>
        );
      },
    },
    {
      key: "order",
      label: "Order",
      width: 84,
      render: (_, row) => {
        const i = Number(row[0]);
        return (
          <span className="rl-order">
            <button type="button" className="icon-btn" aria-label="Move up" disabled={!canEdit || busy || i === 0} onClick={(e) => (e.stopPropagation(), move(i, -1))}>
              <ArrowUp size={14} />
            </button>
            <button type="button" className="icon-btn" aria-label="Move down" disabled={!canEdit || busy || i === rules.length - 1} onClick={(e) => (e.stopPropagation(), move(i, 1))}>
              <ArrowDown size={14} />
            </button>
          </span>
        );
      },
    },
    {
      key: "rule",
      label: "Rule",
      render: (_, row) => {
        const r = rules[Number(row[0])];
        return (
          <div className="sec-rule-name">
            <span>{r.name || "Untitled rule"}</span>
            <span className="dim">{scopeSummary(r)}</span>
          </div>
        );
      },
    },
    {
      key: "what",
      label: "Does",
      width: 260,
      render: (_, row) => {
        const r = rules[Number(row[0])];
        return (
          <span className="dim">
            {ruleSummary(r)}
            {ruleNeedsUser(r) && " · needs the user check"}
          </span>
        );
      },
    },
    {
      key: "mode",
      label: "Mode",
      width: 100,
      render: (_, row) => {
        const r = rules[Number(row[0])];
        if (r.action === "allow") return <span className="dim">—</span>;
        return r.mode === "monitor" ? <span className="pill">Monitor</span> : <span className="pill rl-enforce">Enforce</span>;
      },
    },
    {
      key: "refused",
      label: "Refused",
      align: "right",
      width: 110,
      render: (_, row) => {
        const r = rules[Number(row[0])];
        const s = r.id ? stats.get(r.id) : undefined;
        if (r.action === "allow") return <span className="dim">—</span>;
        if (r.mode === "monitor") return <span className="dim">{formatCount(monitored(s))} would</span>;
        return formatCount(refused(s));
      },
    },
  ];

  const suggestions = (data?.route_limits ?? []).filter((rl) => !rules.some((r) => r.paths?.includes(rl.path) && (rl.method === "" || r.methods?.includes(rl.method))));

  return (
    <div className="grid">
      <Section
        title="Rate limits"
        text="Rules every server SDK enforces in its middleware, before your handler runs: limit requests per IP, user, header or attribute, block IPs, ranges or countries, and exempt others. Services pick up changes within 15 seconds; counters are kept in each instance."
      />
      {config && config.enabled === false && (
        <div className="span-12 notice">
          <ShieldAlert size={16} />
          <span>Enforcement is off: no rule applies. Turn it on under Client IP and country.</span>
        </div>
      )}
      {view.error && !data && <div className="span-12 panel-state error">{linkify(view.error.message)}</div>}

      <StatTile label="Refused" description="Requests the SDKs answered with a rate-limit or block response in this range." value={formatCount(totals.limited + totals.blocked)} delta={null} deltaLabel="" upIs="bad" loading={act.loading} />
      <StatTile label="Limited" description="Over a limit rule: answered 429 with Retry-After." value={formatCount(totals.limited)} delta={null} deltaLabel="" upIs="bad" loading={act.loading} />
      <StatTile label="Blocked" description="Matched a block rule: an IP, range or country you refuse." value={formatCount(totals.blocked)} delta={null} deltaLabel="" upIs="bad" loading={act.loading} />
      <StatTile label="Would refuse" description="What rules in monitor mode would have refused. Nothing was refused." value={formatCount(totals.would)} delta={null} deltaLabel="" upIs="neutral" loading={act.loading} />
      <StatTile label="Clients refused" description="Distinct IPs refused at least once (of the 20 most active)." value={formatCount(totals.clients)} delta={null} deltaLabel="" upIs="bad" loading={act.loading} />
      <StatTile
        label="Rules on"
        description="Rules that apply now, out of all rules."
        value={data ? `${rules.filter((r) => r.enabled !== false).length}` : "—"}
        delta={null}
        deltaLabel=""
        upIs="neutral"
        loading={view.loading}
        footer={data ? `of ${rules.length}` : undefined}
      />

      <Panel
        className="span-12"
        title="Refused over time"
        description="Requests refused by enforced rules, and what monitor-mode rules would have refused."
        result={act.data ? series : undefined}
        loading={act.loading}
        error={act.error}
        bodyHeight={200}
        note={vizNote(act.data ? series : undefined, viz, tokens)}
        chart={act.data && <VizView result={series} viz={viz} height={200} title="Refused over time" empty="Nothing refused in this time range." />}
        table={act.data && <DataTable result={series} maxHeight={200} defaultUnit="count" />}
      />

      <section className="panel span-12" aria-busy={view.loading || undefined}>
        {view.loading && <div className="progress" aria-hidden />}
        <header className="panel-head sec-rules-head">
          <h3 className="panel-title">{rules.length === 0 ? "No rules yet" : `${rules.length} rule${rules.length === 1 ? "" : "s"}, first match first`}</h3>
          <span className="rl-head-actions">
            <button type="button" className="btn sm" onClick={() => setSettingsOpen(true)}>
              <Settings2 size={14} />
              Client IP and country
            </button>
            {canEdit && (
              <button type="button" className="btn primary sm" onClick={() => setEditing({ rule: { ...BLANK }, index: -1 })}>
                <Plus size={14} />
                New rule
              </button>
            )}
          </span>
        </header>
        {data && rules.length === 0 && (
          <div className="panel-state">Add a rule, or enforce one of your Route limits below. New rules start in monitor mode, so you can see what they would refuse first.</div>
        )}
        {rules.length > 0 && (
          <DataTable result={ruleTable} columns={columns} sortable={false} maxHeight={520} rowKey={(r) => String(r[0])} onRowClick={(row) => setEditing({ rule: rules[Number(row[0])], index: Number(row[0]) })} />
        )}
        {data && !canEdit && <p className="dim rl-readonly">Only admins who see every service change rate limits.</p>}
      </section>

      {canEdit && suggestions.length > 0 && (
        <>
          <Section title="From Route limits" text="Your Route limits (Settings → Server → Security detectors) only flag. Enforce one to refuse what goes over it, per IP." />
          <div className="span-12 rl-suggestions">
            {suggestions.map((rl) => (
              <article key={`${rl.method} ${rl.path}`} className="sec-template">
                <div className="sec-template-head">
                  <Gauge size={16} />
                  <h4 className="mono">{`${rl.method} ${rl.path}`.trim()}</h4>
                </div>
                <p>{rl.per_min} requests a minute per IP.</p>
                <div className="sec-template-foot">
                  <button
                    type="button"
                    className="btn sm"
                    onClick={() =>
                      setEditing({
                        index: -1,
                        rule: { ...BLANK, name: `${rl.method} ${rl.path}`.trim(), methods: rl.method ? [rl.method] : [], paths: [rl.path], limit: rl.per_min, window_s: 60 },
                      })
                    }
                  >
                    <Plus size={14} />
                    Enforce
                  </button>
                </div>
              </article>
            ))}
          </div>
        </>
      )}

      <Section title="Who was refused" text="The most refused clients and the latest rows the SDKs wrote. Rows are summed per rule, IP and key each time a service writes, about every 15 seconds." />
      <ClientsPanel activity={act.data} loading={act.loading} />
      <RecentPanel activity={act.data} loading={act.loading} />

      {editing && config && (
        <RuleEditor
          rule={editing.rule}
          canEdit={canEdit}
          busy={busy}
          onClose={() => setEditing(null)}
          onDelete={editing.index >= 0 ? () => void remove(editing.index) : undefined}
          onSave={async (r) => {
            const list = editing.index >= 0 ? rules.map((x, j) => (j === editing.index ? r : x)) : [...rules, r];
            if (await save(withRules(list), editing.index >= 0 ? `Saved “${r.name}”. Services follow within 15 seconds.` : `Added “${r.name}”. Services follow within 15 seconds.`)) setEditing(null);
          }}
        />
      )}
      {settingsOpen && data && <SettingsDrawer view={data} busy={busy} onClose={() => setSettingsOpen(false)} onSave={async (c) => (await save(c, "Saved.")) && setSettingsOpen(false)} />}
    </div>
  );
}

function ClientsPanel({ activity, loading }: { activity: RLActivity | undefined; loading: boolean }) {
  const list = activity?.clients ?? [];
  const result = useMemo(
    () =>
      localResult(
        ["ip", "where", "user", "refused", "rules", "last"],
        list.map((c) => [c.ip, [c.country, c.place].filter(Boolean).join(" · ") || "—", c.user_id || "—", c.enforced, c.rules, c.last]),
      ),
    [list],
  );
  return (
    <section className="panel span-6" aria-busy={loading || undefined}>
      <header className="panel-head">
        <h3 className="panel-title">Most refused clients</h3>
      </header>
      {activity && list.length === 0 ? (
        <div className="panel-state">No client was refused in this range.</div>
      ) : (
        <DataTable
          result={result}
          maxHeight={360}
          columns={[
            { key: "ip", label: "IP", render: (v) => <span className="mono">{String(v)}</span> },
            { key: "where", label: "Where" },
            { key: "user", label: "User" },
            { key: "refused", label: "Refused", align: "right", render: (v) => formatCount(Number(v)) },
            { key: "rules", label: "Rules", align: "right" },
            { key: "last", label: "Last", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span> },
          ]}
        />
      )}
    </section>
  );
}

function RecentPanel({ activity, loading }: { activity: RLActivity | undefined; loading: boolean }) {
  const list = activity?.recent ?? [];
  const result = useMemo(
    () =>
      localResult(
        ["when", "rule", "outcome", "client", "request", "hits"],
        list.map((e) => [e.created_at, e.rule_name || e.rule_id, e.outcome, [e.ip, e.key_value && e.key_value !== e.ip ? e.key_value : ""].filter(Boolean).join(" · "), `${e.method} ${e.path}`, e.hits]),
      ),
    [list],
  );
  return (
    <section className="panel span-6" aria-busy={loading || undefined}>
      <header className="panel-head">
        <h3 className="panel-title">Latest</h3>
      </header>
      {activity && list.length === 0 ? (
        <div className="panel-state">Nothing recorded in this range.</div>
      ) : (
        <DataTable
          result={result}
          maxHeight={360}
          columns={[
            { key: "when", label: "When", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span> },
            { key: "rule", label: "Rule" },
            { key: "outcome", label: "Outcome", render: (v) => <span className={`pill rl-outcome ${String(v)}`}>{OUTCOME_LABELS[v as keyof typeof OUTCOME_LABELS] ?? String(v)}</span> },
            { key: "client", label: "Client", render: (v) => <span className="mono">{String(v) || "—"}</span> },
            { key: "request", label: "First request", render: (v) => <span className="mono dim">{String(v)}</span> },
            { key: "hits", label: "Hits", align: "right", render: (v) => formatCount(Number(v)) },
          ]}
        />
      )}
    </section>
  );
}

/** A property: a fixed one, or a header, query parameter or attribute with its name. */
function PropPicker({ label, value, onChange, allowGlobal = true, none }: { label: string; value: string; onChange: (v: string) => void; allowGlobal?: boolean; none?: string }) {
  const prefix = ["header:", "query:", "attr:"].find((p) => value.startsWith(p));
  const kind = prefix ?? value;
  const options = [...(none !== undefined ? [{ value: "", label: none }] : []), ...KEY_OPTIONS.filter((o) => allowGlobal || o.value !== "global")];
  return (
    <div className="rl-prop">
      <Select label={label} align="left" value={kind} options={options} onChange={(v) => onChange(v)} />
      {prefix && (
        <input
          className="input mono"
          aria-label={`${label} name`}
          placeholder={prefix === "header:" ? "X-Api-Key" : prefix === "query:" ? "api_key" : "plan"}
          value={value.slice(prefix.length)}
          onChange={(e) => onChange(prefix + e.target.value.trim())}
        />
      )}
    </div>
  );
}

function RuleEditor({
  rule,
  canEdit,
  busy,
  onClose,
  onSave,
  onDelete,
}: {
  rule: RLRule;
  canEdit: boolean;
  busy: boolean;
  onClose: () => void;
  onSave: (r: RLRule) => void;
  onDelete?: () => void;
}) {
  const [form, setForm] = useState<RLRule>(() => ({ ...BLANK, ...rule, mode: rule.mode || (rule.id ? "enforce" : "monitor") }));
  const [text, setText] = useState(() => ({
    methods: (rule.methods ?? []).join(", "),
    paths: lines(rule.paths),
    ips: lines(rule.ips),
    countries: (rule.countries ?? []).join(", "),
    services: (rule.services ?? []).join(", "),
  }));
  const startWindow = rule.window_s ?? 60;
  const [unit, setUnit] = useState(() => (UNITS.slice().reverse().find((u) => startWindow % Number(u.value) === 0)?.value ?? "1"));
  const [amount, setAmount] = useState(() => String(startWindow / Number(unit)));
  const [match, setMatch] = useState<{ prop: string; values: string }[]>(() => Object.entries(rule.match ?? {}).map(([prop, v]) => ({ prop, values: v.join(", ") })));
  const [tiers, setTiers] = useState<{ value: string; limit: string }[]>(() => Object.entries(rule.limits ?? {}).map(([value, n]) => ({ value, limit: String(n) })));
  const [showResponse, setShowResponse] = useState(!!(rule.status || rule.body || rule.content_type));
  const set = <K extends keyof RLRule>(k: K, v: RLRule[K]) => setForm((f) => ({ ...f, [k]: v }));
  const saved = !!rule.id;

  const built = (): RLRule => {
    const r: RLRule = {
      ...form,
      methods: parseLines(text.methods),
      paths: parseLines(text.paths),
      ips: parseLines(text.ips),
      countries: parseLines(text.countries),
      services: parseLines(text.services),
      match: Object.fromEntries(match.filter((m) => m.prop && !m.prop.endsWith(":")).map((m) => [m.prop, parseLines(m.values)])),
      status: Number(form.status) || 0,
    };
    if (r.action === "limit") {
      r.window_s = Math.round(Number(amount) * Number(unit)) || 0;
      r.limit = Number(form.limit) || 0;
      r.burst = Number(form.burst) || 0;
      r.limits = r.limit_by ? Object.fromEntries(tiers.filter((t) => t.value.trim() !== "").map((t) => [t.value.trim(), Number(t.limit)])) : {};
    }
    return r;
  };
  const preview = built();
  const action = ACTIONS.find((a) => a.value === form.action)!;

  return (
    <Drawer title={saved ? `Edit ${rule.name || "rule"}` : "New rate-limit rule"} onClose={onClose} className="sec-rule-drawer">
      <form
        className="up-form"
        onSubmit={(e) => {
          e.preventDefault();
          onSave(built());
        }}
      >
        <label className="field">
          <span>Name</span>
          <input className="input" value={form.name ?? ""} onChange={(e) => set("name", e.target.value)} placeholder="Export API per IP" maxLength={120} required autoFocus />
        </label>

        <div className="up-row">
          <div className="field" style={{ flex: 1 }}>
            <span>Action</span>
            <div className="segmented" role="radiogroup" aria-label="Action">
              {ACTIONS.map((a) => (
                <button type="button" key={a.value} role="radio" aria-checked={form.action === a.value} className={form.action === a.value ? "on" : ""} onClick={() => set("action", a.value)}>
                  {a.label}
                </button>
              ))}
            </div>
          </div>
          {form.action !== "allow" && (
            <div className="field" style={{ flex: 1 }}>
              <span>Mode</span>
              <div className="segmented" role="radiogroup" aria-label="Mode">
                {[
                  { value: "enforce", label: "Enforce" },
                  { value: "monitor", label: "Monitor only" },
                ].map((m) => (
                  <button type="button" key={m.value} role="radio" aria-checked={form.mode === m.value} className={form.mode === m.value ? "on" : ""} onClick={() => set("mode", m.value as RLRule["mode"])}>
                    {m.label}
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
        <p className="dim sec-rule-help">
          {action.text}
          {form.action !== "allow" && form.mode === "monitor" && " Monitor only records what it would refuse; every request goes through."}
        </p>

        <fieldset className="sec-rule-step">
          <legend>Which requests</legend>
          <div className="sec-rule-filters">
            <label className="field">
              <span>Methods</span>
              <input className="input mono" value={text.methods} placeholder="POST, PUT (any when empty)" onChange={(e) => setText({ ...text, methods: e.target.value })} />
            </label>
            <label className="field">
              <span>Services</span>
              <input className="input mono" value={text.services} placeholder="checkout (every service when empty)" onChange={(e) => setText({ ...text, services: e.target.value })} />
            </label>
            <label className="field wide">
              <span>Paths</span>
              <textarea className="input mono" rows={2} value={text.paths} placeholder={"/api/export*\n/api/*/search"} onChange={(e) => setText({ ...text, paths: e.target.value })} />
              <small>One per line. * matches anything, / included; the rest is exact and case-sensitive. Every path when empty.</small>
            </label>
            <label className="field wide">
              <span>IPs and ranges</span>
              <textarea className="input mono" rows={2} value={text.ips} placeholder={"203.0.113.9\n10.0.0.0/8\n2001:db8::/32"} onChange={(e) => setText({ ...text, ips: e.target.value })} />
              <small>IPv4 or IPv6 addresses and CIDR ranges, one per line. Any IP when empty.</small>
            </label>
            <label className="field wide">
              <span>Countries</span>
              <input className="input mono" value={text.countries} placeholder="CN, RU (two-letter codes; any when empty)" onChange={(e) => setText({ ...text, countries: e.target.value })} />
              <small>Read from your CDN's country header (Client IP and country). Without one, country rules match nothing.</small>
            </label>
          </div>
          <div className="rl-match">
            {match.map((m, i) => (
              <div className="rl-match-row" key={i}>
                <PropPicker label="Property" value={m.prop} allowGlobal={false} onChange={(v) => setMatch(match.map((x, j) => (j === i ? { ...x, prop: v } : x)))} />
                <span className="dim">is</span>
                <input
                  className="input mono"
                  aria-label="Values"
                  placeholder="free, trial (any value when empty)"
                  value={m.values}
                  onChange={(e) => setMatch(match.map((x, j) => (j === i ? { ...x, values: e.target.value } : x)))}
                />
                <button type="button" className="icon-btn" aria-label="Remove condition" onClick={() => setMatch(match.filter((_, j) => j !== i))}>
                  <X size={14} />
                </button>
              </div>
            ))}
            <button type="button" className="btn sm" onClick={() => setMatch([...match, { prop: "header:", values: "" }])}>
              <Plus size={14} />
              Add a condition
            </button>
          </div>
        </fieldset>

        {form.action === "limit" && (
          <fieldset className="sec-rule-step">
            <legend>Limit</legend>
            <div className="up-row rl-limit-row">
              <label className="field">
                <span>Requests</span>
                <input className="input" type="number" min={0} step={1} value={form.limit ?? 0} onChange={(e) => set("limit", Number(e.target.value))} required />
              </label>
              <label className="field">
                <span>Every</span>
                <input className="input" type="number" min={1} step={1} value={amount} onChange={(e) => setAmount(e.target.value)} required />
              </label>
              <div className="field">
                <span>Unit</span>
                <Select label="Unit" align="left" value={unit} options={UNITS} onChange={setUnit} />
              </div>
              <div className="field" style={{ flex: 2 }}>
                <span>Counted per</span>
                <PropPicker label="Counted per" value={form.key || "ip"} onChange={(v) => set("key", v)} />
              </div>
            </div>
            <div className="up-row">
              <div className="field" style={{ flex: 1 }}>
                <span>Algorithm</span>
                <Select label="Algorithm" align="left" value={form.algorithm ?? ""} options={ALGORITHMS} onChange={(v) => set("algorithm", v as RLRule["algorithm"])} />
              </div>
              {form.algorithm === "token_bucket" && (
                <label className="field" style={{ flex: 1 }}>
                  <span>Burst</span>
                  <input className="input" type="number" min={0} step={1} value={form.burst || ""} placeholder={String(form.limit ?? 0)} onChange={(e) => set("burst", Number(e.target.value))} />
                </label>
              )}
            </div>
            <p className="dim sec-rule-help">
              {form.algorithm === "fixed"
                ? "Counts per calendar window; a client can send up to twice the limit across a window boundary."
                : form.algorithm === "token_bucket"
                  ? "Refills steadily at the limit's rate and allows bursts up to the bucket size."
                  : "Weighs the previous window by how much of it still overlaps, so there is no burst at the boundary."}
              {" "}A limit of 0 refuses every matching request.
            </p>

            <div className="field">
              <span>Different limits by</span>
              <PropPicker label="Different limits by" value={form.limit_by ?? ""} allowGlobal={false} none="One limit for everyone" onChange={(v) => set("limit_by", v)} />
            </div>
            {form.limit_by && (
              <div className="rl-match">
                {tiers.map((t, i) => (
                  <div className="rl-match-row" key={i}>
                    <span className="dim">When {propLabel(form.limit_by)} is</span>
                    <input className="input mono" aria-label="Value" placeholder="pro" value={t.value} onChange={(e) => setTiers(tiers.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} />
                    <input
                      className="input"
                      type="number"
                      step={1}
                      aria-label="Limit"
                      placeholder="600"
                      value={t.limit}
                      onChange={(e) => setTiers(tiers.map((x, j) => (j === i ? { ...x, limit: e.target.value } : x)))}
                    />
                    <button type="button" className="icon-btn" aria-label="Remove limit" onClick={() => setTiers(tiers.filter((_, j) => j !== i))}>
                      <X size={14} />
                    </button>
                  </div>
                ))}
                <button type="button" className="btn sm" onClick={() => setTiers([...tiers, { value: "", limit: "" }])}>
                  <Plus size={14} />
                  Add a limit
                </button>
                <small className="dim">Other values use {form.limit ?? 0}. A negative limit means no limit for that value.</small>
              </div>
            )}
          </fieldset>
        )}

        {form.action !== "allow" && (
          <fieldset className="sec-rule-step">
            <legend>Response</legend>
            {!showResponse ? (
              <button type="button" className="btn sm" onClick={() => setShowResponse(true)}>
                Customize the response
              </button>
            ) : (
              <div className="sec-rule-filters">
                <label className="field">
                  <span>Status</span>
                  <input className="input" type="number" min={400} max={599} value={form.status || ""} placeholder={form.action === "block" ? "403" : "429"} onChange={(e) => set("status", Number(e.target.value))} />
                </label>
                <label className="field">
                  <span>Content type</span>
                  <input className="input mono" value={form.content_type ?? ""} placeholder="from the body" onChange={(e) => set("content_type", e.target.value)} />
                </label>
                <label className="field wide">
                  <span>Body</span>
                  <textarea
                    className="input mono"
                    rows={3}
                    maxLength={4096}
                    value={form.body ?? ""}
                    placeholder={`{"error":{"code":"${form.action === "block" ? "blocked" : "rate_limited"}","key":"<rule id>","message":"${form.action === "block" ? "Forbidden" : "Too many requests"}"}}`}
                    onChange={(e) => set("body", e.target.value)}
                  />
                  <small>{"{retry_after} becomes the seconds to wait. Empty keeps the default JSON."}</small>
                </label>
              </div>
            )}
          </fieldset>
        )}

        {ruleNeedsUser(preview) && (
          <div className="notice">
            <ShieldAlert size={16} />
            <span>
              This rule uses the user, role or an app attribute, which are known only after your auth code runs. It applies where your service calls the SDK's user check after setting the
              user (How to use → Rate limits); without that call it never applies.
            </span>
          </div>
        )}

        <fieldset className="up-checks">
          <label>
            <input type="checkbox" checked={form.enabled !== false} onChange={(e) => set("enabled", e.target.checked)} />
            On: services apply this rule
          </label>
        </fieldset>

        {preview.action === "limit" && (
          <p className="dim sec-rule-help">
            {ruleSummary(preview)}
            {preview.window_s ? "" : " (set a window)"} · {scopeSummary(preview)}
            {preview.window_s && preview.window_s % 60 !== 0 ? ` · window of ${windowLabel(preview.window_s)}` : ""}
          </p>
        )}

        <div className="up-form-foot">
          {onDelete && canEdit && (
            <button type="button" className="btn ghost" disabled={busy} onClick={onDelete}>
              <Trash2 size={14} />
              Delete
            </button>
          )}
          <span style={{ flex: 1 }} />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          {canEdit && (
            <button type="submit" className="btn primary" disabled={busy}>
              {saved ? "Save changes" : "Add rule"}
            </button>
          )}
        </div>
      </form>
    </Drawer>
  );
}

function SettingsDrawer({ view, busy, onClose, onSave }: { view: RLView; busy: boolean; onClose: () => void; onSave: (c: RLConfig) => void }) {
  const [on, setOn] = useState(view.config.enabled !== false);
  const [ipHeader, setIpHeader] = useState(view.config.ip_header ?? "");
  const [countries, setCountries] = useState((view.config.country_headers ?? []).join(", "));
  return (
    <Drawer title="Client IP and country" onClose={onClose} className="sec-rule-drawer">
      <form
        className="up-form"
        onSubmit={(e) => {
          e.preventDefault();
          onSave({ ...view.config, enabled: on, ip_header: ipHeader.trim(), country_headers: parseLines(countries) });
        }}
      >
        <div className="rl-switch-row">
          <Switch on={on} label="Enforce rate limits" disabled={!view.can_edit} onChange={setOn} />
          <div>
            <strong>Enforce rate limits</strong>
            <p className="dim">Off, no rule applies anywhere, without deleting them. A single service can opt out with VISIONARY_RATE_LIMIT=false.</p>
          </div>
        </div>
        <label className="field">
          <span>Client IP header</span>
          <input className="input mono" value={ipHeader} disabled={!view.can_edit} placeholder="CF-Connecting-IP" onChange={(e) => setIpHeader(e.target.value)} />
          <small>
            The header your CDN or load balancer sets to the real client IP: CF-Connecting-IP, True-Client-IP, X-Real-IP. Empty uses the first X-Forwarded-For hop, which clients can forge unless
            your proxy overwrites it.
          </small>
        </label>
        <label className="field">
          <span>Country headers</span>
          <input className="input mono" value={countries} disabled={!view.can_edit} placeholder={view.default_country_headers.join(", ")} onChange={(e) => setCountries(e.target.value)} />
          <small>
            Where the SDKs read the visitor's two-letter country, set by your CDN or edge from its own IP data, so no service ships a GeoIP database. Empty reads the common ones:{" "}
            {view.default_country_headers.join(", ")}.
          </small>
        </label>
        <div className="up-form-foot">
          <span style={{ flex: 1 }} />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          {view.can_edit && (
            <button type="submit" className="btn primary" disabled={busy}>
              Save
            </button>
          )}
        </div>
      </form>
    </Drawer>
  );
}
