import { useEffect, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatRelative } from "../lib/time";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { ErrorNotice } from "../components/ErrorNotice";
import { LEVELS, LEVEL_LABEL, archiveApi, logSettingsApi, type LogSettings, type LogSettingsResponse, type ParseRule } from "../lib/applogs";
import { formatBytes, formatCount } from "../lib/format";
import { Card, Switch } from "./Settings.profiling";

const LEVEL_OPTIONS = LEVELS.map((l) => ({ value: l, label: LEVEL_LABEL[l] }));

const MINUTES = [
  { value: "15", label: "15 minutes" },
  { value: "60", label: "1 hour" },
  { value: "240", label: "4 hours" },
  { value: "1440", label: "1 day" },
];

function list(s: string): string[] {
  return s
    .split(/[,\n]/)
    .map((x) => x.trim())
    .filter(Boolean);
}

/** Settings → Logs: what services keep, for how long debug is on, what is dropped and parsed, who sees what. */
export function LogsTab() {
  const toast = useToast();
  const res = useQuery("log-settings", () => logSettingsApi.get());
  const [data, setData] = useState<LogSettingsResponse>();
  const [draft, setDraft] = useState<LogSettings>();
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (res.data) {
      setData(res.data);
      setDraft(structuredClone(res.data.settings));
    }
  }, [res.data]);

  if (res.error) return <ErrorNotice title="Could not load log settings" error={res.error} />;
  if (!data || !draft) return <div className="settings-empty">Loading…</div>;

  const admin = data.can_admin;
  const edit = data.can_edit;
  const saved = data.settings;
  const dirty = JSON.stringify(draft) !== JSON.stringify(saved);
  const apply = (next: LogSettingsResponse) => {
    setData(next);
    setDraft(structuredClone(next.settings));
  };
  const act = async (fn: () => Promise<LogSettingsResponse>, ok: string) => {
    try {
      apply(await fn());
      toast("ok", ok);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  const save = async () => {
    setSaving(true);
    await act(() => logSettingsApi.put(draft), "Saved. Services pick it up within 30 seconds.");
    setSaving(false);
  };
  const set = (patch: Partial<LogSettings>) => setDraft({ ...draft, ...patch });
  const setServer = (patch: Partial<LogSettings["server"]>) => setDraft({ ...draft, server: { ...draft.server, ...patch } });

  const saveBar = admin ? (
    <div className="pset-actions">
      <button type="button" className="btn primary" disabled={!dirty || saving} onClick={save}>
        {saving ? "Saving…" : "Save"}
      </button>
      {dirty && (
        <button type="button" className="btn ghost" onClick={() => setDraft(structuredClone(saved))}>
          Discard
        </button>
      )}
    </div>
  ) : (
    <p className="dim" style={{ margin: 0 }}>
      Only admins change these.
    </p>
  );

  return (
    <div className="settings-stack">
      <Card
        title="What services keep"
        description="Every SDK that captures logs reads these within 30 seconds; no redeploy. OpenTelemetry, shippers and syslog follow them too."
        actions={<Switch on={draft.enabled} disabled={!admin} label="Store application logs" onChange={(enabled) => set({ enabled })} />}
      >
        <div className="settings-body pset">
          <div className="pset-row">
            <div>
              <div className="pset-label">Keep lines at</div>
              <div className="dim pset-hint">This level and above are always stored.</div>
            </div>
            <Select label="Level" value={draft.level} disabled={!admin} options={LEVEL_OPTIONS.map((o) => ({ ...o, label: `${o.label} and above` }))} onChange={(level) => set({ level })} />
          </div>
          <div className="pset-row">
            <div>
              <div className="pset-label">Lines of failed and slow requests</div>
              <div className="dim pset-hint">
                Lines below the level are held while a request runs. When it fails (5xx, an exception) or is slow, they are stored down to this level; otherwise dropped.
              </div>
            </div>
            <div className="lgs-inline">
              <Switch on={draft.buffer} disabled={!admin} label="Keep lines of failed and slow requests" onChange={(buffer) => set({ buffer })} />
              <Select label="Down to" value={draft.buffer_level} disabled={!admin || !draft.buffer} options={LEVEL_OPTIONS} onChange={(buffer_level) => set({ buffer_level })} />
            </div>
          </div>
          <div className="pset-row">
            <div>
              <div className="pset-label">Slow means</div>
              <div className="dim pset-hint">A request at least this slow keeps its lower lines.</div>
            </div>
            <label className="lgs-num">
              <input className="input" type="number" min={1} disabled={!admin} value={draft.slow_ms} onChange={(e) => set({ slow_ms: Number(e.target.value) })} /> ms
            </label>
          </div>
          <div className="pset-row">
            <div>
              <div className="pset-label">Cap per process</div>
              <div className="dim pset-hint">Lines per minute each running copy of a service may write; errors pass up to ten times over. 0 is no cap.</div>
            </div>
            <label className="lgs-num">
              <input className="input" type="number" min={0} disabled={!admin} value={draft.rate} onChange={(e) => set({ rate: Number(e.target.value) })} /> a minute
            </label>
          </div>
          <div className="pset-row">
            <div>
              <div className="pset-label">Cap per service, received</div>
              <div className="dim pset-hint">For lines sent to this server (OpenTelemetry, shippers, syslog, the host agent). 0 is no cap.</div>
            </div>
            <label className="lgs-num">
              <input className="input" type="number" min={0} disabled={!admin} value={draft.server.rate} onChange={(e) => setServer({ rate: Number(e.target.value) })} /> a minute
            </label>
          </div>
          {saveBar}
        </div>
      </Card>

      <OverridesCard data={data} edit={edit} onApply={act} />
      <DropsCard data={data} edit={edit} onApply={act} />

      <Card title="Parse rules" description="Turn plain-text lines received by this server into fields: a pattern with %{TYPE:name} placeholders, or a regular expression with named groups. Fields named message, level and time set those.">
        <div className="settings-body pset">
          {draft.server.rules.length === 0 && <div className="dim">No rules. JSON, logfmt and nginx/Apache access lines are read without one.</div>}
          {draft.server.rules.map((r, i) => (
            <RuleRow
              key={i}
              rule={r}
              disabled={!admin}
              onChange={(next) => setServer({ rules: draft.server.rules.map((x, j) => (j === i ? next : x)) })}
              onRemove={() => setServer({ rules: draft.server.rules.filter((_, j) => j !== i) })}
            />
          ))}
          {admin && (
            <div>
              <button type="button" className="btn" onClick={() => setServer({ rules: [...draft.server.rules, { name: "", service: "", source: "", pattern: "" }] })}>
                <Plus size={14} />
                Add a rule
              </button>
            </div>
          )}
          {saveBar}
        </div>
      </Card>

      <Card title="Secrets and access" description="Values of these fields are never stored, on top of the built-in list (password, token, api_key, secret…). Viewers don't see lines of hidden services, and see masked fields as [hidden].">
        <div className="settings-body pset">
          <ListField label="Also redact these fields" hint="Comma-separated field names, any case." value={draft.server.redact} disabled={!admin} onChange={(redact) => setServer({ redact })} />
          <ListField
            label="Hide these services from viewers"
            hint="Editors and admins see everything."
            value={draft.server.access.hidden_services}
            disabled={!admin}
            onChange={(hidden_services) => setServer({ access: { ...draft.server.access, hidden_services } })}
          />
          <ListField
            label="Mask these fields for viewers"
            hint="e.g. email, phone, ip"
            value={draft.server.access.masked_fields}
            disabled={!admin}
            onChange={(masked_fields) => setServer({ access: { ...draft.server.access, masked_fields } })}
          />
          {saveBar}
        </div>
      </Card>

      <Card
        title="Syslog receiver"
        description="Receives RFC 5424 and RFC 3164 syslog over UDP and TCP. Syslog has no keys: use a private address or a firewall."
      >
        <div className="settings-body pset">
          <div className="pset-row">
            <div>
              <div className="pset-label">Listen on</div>
              <div className="dim pset-hint">
                host:port or :port, e.g. :5514. Empty is off.
                {data.syslog.listening && <> Listening on <span className="mono">{data.syslog.listening}</span>.</>}
                {data.syslog.error && <span className="lgs-err"> {data.syslog.error}</span>}
              </div>
            </div>
            <input className="input mono lgs-addr" disabled={!admin} value={draft.server.syslog} placeholder=":5514" onChange={(e) => setServer({ syslog: e.target.value })} />
          </div>
          <div className="pset-row">
            <div>
              <div className="pset-label">Service for senders without an app name</div>
            </div>
            <input className="input mono lgs-addr" disabled={!admin} value={draft.server.syslog_service} placeholder="network" onChange={(e) => setServer({ syslog_service: e.target.value })} />
          </div>
          {saveBar}
        </div>
      </Card>

      <ArchiveCard admin={admin} />
    </div>
  );
}

/** The log archive: its state, the latest hours, and loading a range back. */
function ArchiveCard({ admin }: { admin: boolean }) {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const info = useQuery(`log-archive#${run}`, () => archiveApi.get());
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [loading, setLoading] = useState(false);
  const d = info.data;
  const hours = d && !Array.isArray(d.hours) ? d.hours : null;
  const load = async () => {
    setLoading(true);
    try {
      const out = await archiveApi.rehydrate(new Date(from), new Date(to));
      const skipped = out.hours.filter((h) => h.skipped).length;
      toast("ok", `Loaded ${formatCount(out.lines)} lines back${skipped ? `; ${skipped} hours were skipped` : ""}. Search them in Logs.`);
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };
  return (
    <Card
      title="Archive"
      description={
        <>
          Every finished hour of logs is written to a bucket as gzipped NDJSON, so logs outlive retention. Set the bucket in{" "}
          <a href="#/settings?tab=server">Settings → Server → Log archive</a>.
        </>
      }
    >
      <div className="settings-body pset">
        {info.error ? (
          <ErrorNotice title="Could not load the archive" error={info.error} />
        ) : !d ? (
          <div className="dim">Loading…</div>
        ) : !d.configured ? (
          <div className="dim">Off: no bucket is set.</div>
        ) : (
          <>
            <div className="dim">
              Writing to <span className="mono">{d.bucket}/{d.prefix}</span> at <span className="mono">{d.endpoint}</span>
              {d.last_run && <> · last run {formatRelative(d.last_run)}</>}
            </div>
            {d.last_error && <div className="lgs-err">The last run failed: {d.last_error}</div>}
            {hours && hours.rows.length > 0 ? (
              <div className="table-wrap" style={{ maxHeight: 220 }}>
                <table className="data">
                  <thead>
                    <tr>
                      <th>Hour</th>
                      <th className="num">Lines</th>
                      <th className="num">Size</th>
                      <th>Object</th>
                    </tr>
                  </thead>
                  <tbody>
                    {hours.rows.slice(0, 48).map((r) => (
                      <tr key={String(r[0])}>
                        <td>{formatFull(new Date(String(r[0])))}</td>
                        <td className="num">{formatCount(Number(r[2]))}</td>
                        <td className="num">{formatBytes(Number(r[3]))}</td>
                        <td className="mono dim">{String(r[1])}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="dim">No hour archived yet. Hours are written within ten minutes of ending.</div>
            )}
            {admin && (
              <div className="lgs-add">
                <span>Load back from</span>
                <input className="input" type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} aria-label="From" />
                <span>to</span>
                <input className="input" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} aria-label="To" />
                <button type="button" className="btn" disabled={!from || !to || loading} onClick={load}>
                  {loading ? "Loading…" : "Load back"}
                </button>
                <span className="dim">At most 72 hours at a time; hours still stored are skipped.</span>
              </div>
            )}
          </>
        )}
      </div>
    </Card>
  );
}

function ListField({ label, hint, value, disabled, onChange }: { label: string; hint: string; value: string[]; disabled: boolean; onChange: (v: string[]) => void }) {
  const [text, setText] = useState(value.join(", "));
  useEffect(() => setText(value.join(", ")), [value]);
  return (
    <div className="pset-row">
      <div>
        <div className="pset-label">{label}</div>
        <div className="dim pset-hint">{hint}</div>
      </div>
      <input className="input mono lgs-list" disabled={disabled} value={text} onChange={(e) => setText(e.target.value)} onBlur={() => onChange(list(text))} />
    </div>
  );
}

function RuleRow({ rule, disabled, onChange, onRemove }: { rule: ParseRule; disabled: boolean; onChange: (r: ParseRule) => void; onRemove: () => void }) {
  const [sample, setSample] = useState("");
  const test = useQuery(rule.pattern && sample ? `rule-test:${rule.pattern}:${sample}` : null, () => logSettingsApi.testRule(rule.pattern, sample));
  return (
    <div className="lgs-rule">
      <div className="lgs-rule-row">
        <input className="input" placeholder="Name" disabled={disabled} value={rule.name} onChange={(e) => onChange({ ...rule, name: e.target.value })} />
        <input className="input mono" placeholder="Only for service" disabled={disabled} value={rule.service} onChange={(e) => onChange({ ...rule, service: e.target.value })} />
        <input className="input mono" placeholder="Only for source" disabled={disabled} value={rule.source} onChange={(e) => onChange({ ...rule, source: e.target.value })} />
        {!disabled && (
          <button type="button" className="icon-btn" aria-label="Remove the rule" onClick={onRemove}>
            <Trash2 size={14} />
          </button>
        )}
      </div>
      <input
        className="input mono"
        placeholder="%{TIMESTAMP_ISO8601:time} [%{LOGLEVEL:level}] %{GREEDYDATA:message}"
        disabled={disabled}
        value={rule.pattern}
        onChange={(e) => onChange({ ...rule, pattern: e.target.value })}
      />
      <input className="input mono" placeholder="Paste a sample line to try the rule" value={sample} onChange={(e) => setSample(e.target.value)} />
      {test.error && <div className="lgs-err">{test.error.message}</div>}
      {test.data && (
        <div className="lgs-test">
          {test.data.matched ? (
            <>
              <span className="pill">Matches</span>
              {test.data.level && <span className="mono">level={test.data.level}</span>}
              {test.data.time && <span className="mono">time={test.data.time}</span>}
              {Object.entries(test.data.fields).map(([k, v]) => (
                <span key={k} className="mono">
                  {k}={String(v)}
                </span>
              ))}
              <span className="mono">message={test.data.message}</span>
            </>
          ) : (
            <span className="dim">Doesn't match the sample.</span>
          )}
        </div>
      )}
      <div className="dim lgs-hint">
        Placeholders: WORD NOTSPACE DATA GREEDYDATA INT NUMBER IP UUID LOGLEVEL TIMESTAMP_ISO8601 PATH URIPATH QUOTEDSTRING HOSTNAME
      </div>
    </div>
  );
}

type Apply = (fn: () => Promise<LogSettingsResponse>, ok: string) => Promise<void>;

function OverridesCard({ data, edit, onApply }: { data: LogSettingsResponse; edit: boolean; onApply: Apply }) {
  const [service, setService] = useState("");
  const [route, setRoute] = useState("");
  const [user, setUser] = useState("");
  const [level, setLevel] = useState("debug");
  const [minutes, setMinutes] = useState("15");
  const list = data.settings.overrides;
  const add = () =>
    onApply(
      () => logSettingsApi.addOverride({ service: service.trim(), route: route.trim(), user: user.trim(), level, minutes: Number(minutes) }),
      "Override added. Services pick it up within 30 seconds.",
    );
  return (
    <Card
      title="Level overrides"
      description="Change the level for a service, a route or a user for a while: debug for /checkout for 15 minutes, or quieter for a noisy route. The most specific match wins; they end on their own."
    >
      <div className="settings-body pset">
        {list.length === 0 ? (
          <div className="dim">None.</div>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Route</th>
                  <th>User</th>
                  <th>Level</th>
                  <th>Until</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((o, i) => (
                  <tr key={i} className={data.expired[i] ? "dim" : ""}>
                    <td className="mono">{o.service || "any"}</td>
                    <td className="mono">{o.route || "any"}</td>
                    <td className="mono">{o.user || "any"}</td>
                    <td>{LEVEL_LABEL[o.level as keyof typeof LEVEL_LABEL] ?? o.level}</td>
                    <td title={o.until ? formatFull(new Date(o.until)) : ""}>{o.until ? (data.expired[i] ? "Ended" : formatRelative(o.until)) : "No end"}</td>
                    <td className="lg-row-actions">
                      {edit && (
                        <button type="button" className="icon-btn" aria-label="Remove the override" onClick={() => onApply(() => logSettingsApi.deleteOverride(i), "Override removed.")}>
                          <Trash2 size={14} />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {edit && (
          <div className="lgs-add">
            <input className="input mono" placeholder="Service (any)" value={service} onChange={(e) => setService(e.target.value)} />
            <input className="input mono" placeholder="Route, e.g. /checkout or /admin/*" value={route} onChange={(e) => setRoute(e.target.value)} />
            <input className="input mono" placeholder="User id (any)" value={user} onChange={(e) => setUser(e.target.value)} />
            <Select label="Level" value={level} options={LEVEL_OPTIONS} onChange={setLevel} />
            <Select label="For" value={minutes} options={MINUTES} onChange={setMinutes} />
            <button type="button" className="btn" onClick={add}>
              <Plus size={14} />
              Add
            </button>
          </div>
        )}
      </div>
    </Card>
  );
}

function DropsCard({ data, edit, onApply }: { data: LogSettingsResponse; edit: boolean; onApply: Apply }) {
  const [service, setService] = useState("");
  const [logger, setLogger] = useState("");
  const [contains, setContains] = useState("");
  const [level, setLevel] = useState("");
  const list = data.settings.drops;
  const add = () =>
    onApply(() => logSettingsApi.addDrop({ service: service.trim(), logger: logger.trim(), contains, level }), "Drop rule added. Services follow it within 30 seconds.");
  return (
    <Card title="Drop rules" description="Lines that match are dropped before they are stored, by every SDK and by this server. Fill in what to match; empty fields match anything.">
      <div className="settings-body pset">
        {list.length === 0 ? (
          <div className="dim">None.</div>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Logger</th>
                  <th>Contains</th>
                  <th>Levels</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((d, i) => (
                  <tr key={i}>
                    <td className="mono">{d.service || "any"}</td>
                    <td className="mono">{d.logger || "any"}</td>
                    <td className="mono">{d.contains || "anything"}</td>
                    <td>{d.level ? `${LEVEL_LABEL[d.level as keyof typeof LEVEL_LABEL] ?? d.level} and below` : "All"}</td>
                    <td className="lg-row-actions">
                      {edit && (
                        <button type="button" className="icon-btn" aria-label="Remove the drop rule" onClick={() => onApply(() => logSettingsApi.deleteDrop(i), "Drop rule removed.")}>
                          <Trash2 size={14} />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {edit && (
          <div className="lgs-add">
            <input className="input mono" placeholder="Service (any)" value={service} onChange={(e) => setService(e.target.value)} />
            <input className="input mono" placeholder="Logger (any)" value={logger} onChange={(e) => setLogger(e.target.value)} />
            <input className="input mono" placeholder="Contains, e.g. GET /healthz" value={contains} onChange={(e) => setContains(e.target.value)} />
            <Select label="Levels" value={level} options={[{ value: "", label: "Every level" }, ...LEVEL_OPTIONS.map((o) => ({ ...o, label: `${o.label} and below` }))]} onChange={setLevel} />
            <button type="button" className="btn" disabled={!service.trim() && !logger.trim() && !contains && !level} onClick={add}>
              <Plus size={14} />
              Add
            </button>
          </div>
        )}
      </div>
    </Card>
  );
}
