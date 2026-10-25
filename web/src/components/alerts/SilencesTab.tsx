import { useMemo, useState } from "react";
import { BellOff, CalendarClock, Moon, Pencil, Plus, Trash2 } from "lucide-react";
import { Modal } from "../Modal";
import { useToast } from "../Toast";
import { useQuery } from "../../lib/useQuery";
import { alertsApi, labelPairs, WEEKDAYS, type AlertsMeta, type RuleView, type Severity, type Silence, type SilenceInput } from "../../lib/alerts";
import { formatDateTime } from "../../lib/time";
import { titleCase } from "../../lib/format";
import { ErrorNotice } from "../ErrorNotice";

const STATE_LABEL: Record<Silence["state"], string> = { active: "Active now", scheduled: "Scheduled", ended: "Ended", disabled: "Off" };

const browserTZ = () => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
};

/** A Date as the value of a datetime-local input, in this browser's zone. */
function localInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

export function describeSchedule(s: Silence["schedule"]): string {
  if (s.kind === "once") return `${formatDateTime(new Date(s.starts_at ?? ""))} to ${formatDateTime(new Date(s.ends_at ?? ""))}`;
  const days = s.days ?? [];
  const named =
    days.length === 7
      ? "Every day"
      : [1, 2, 3, 4, 5].every((d) => days.includes(d)) && days.length === 5
        ? "Weekdays"
        : days.length === 2 && days.includes(0) && days.includes(6)
          ? "Weekends"
          : [...days].sort().map((d) => WEEKDAYS[d].slice(0, 3)).join(", ");
  return `${named}, ${s.from} to ${s.to}${(s.from ?? "") > (s.to ?? "") ? " (next day)" : ""}, ${s.tz || "UTC"}`;
}

function describeMatchers(s: Silence, rules: RuleView[]): string {
  const m = s.matchers;
  const parts: string[] = [];
  if (m.rule_ids?.length) {
    const names = m.rule_ids.map((id) => rules.find((r) => r.id === id)?.name ?? `rule ${id}`);
    parts.push(names.length > 3 ? `${names.slice(0, 3).join(", ")} and ${names.length - 3} more` : names.join(", "));
  }
  if (m.severities?.length) parts.push(`${m.severities.join(" or ")} severity`);
  if (m.labels && Object.keys(m.labels).length) parts.push(labelPairs(m.labels).join(", "));
  if (m.series) parts.push(`series ${m.series}`);
  return parts.length ? parts.join(" · ") : "Every alert";
}

export function SilencesTab({ meta, rules, refreshKey }: { meta: AlertsMeta; rules: RuleView[]; refreshKey: number }) {
  const toast = useToast();
  const [version, setVersion] = useState(0);
  const [editing, setEditing] = useState<Silence | "new" | null>(null);
  const silences = useQuery(`silences#${refreshKey}.${version}`, (signal) => alertsApi.silences(signal));
  const changed = () => setVersion((v) => v + 1);
  const list = useMemo(() => {
    const rank = { active: 0, scheduled: 1, disabled: 2, ended: 3 };
    return [...(silences.data?.silences ?? [])].sort((a, b) => rank[a.state] - rank[b.state] || a.name.localeCompare(b.name));
  }, [silences.data]);

  const toggle = async (s: Silence) => {
    try {
      await alertsApi.updateSilence(s.id, { name: s.name, matchers: s.matchers, schedule: s.schedule, enabled: !s.enabled });
      changed();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };
  const remove = async (s: Silence) => {
    if (!window.confirm(`Delete the silence "${s.name}"?`)) return;
    try {
      await alertsApi.deleteSilence(s.id);
      changed();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };

  return (
    <>
      <div className="al-toolbar">
        <span className="al-hint">Silences hold back notifications for matching alerts. Rules keep evaluating, and Activity records what was held back and why.</span>
        <button type="button" className="btn primary" onClick={() => setEditing("new")} disabled={!meta.can_edit}>
          <Plus size={15} />
          New silence
        </button>
      </div>
      {silences.error && <ErrorNotice title="Could not load silences" error={silences.error} />}
      {silences.data && list.length === 0 ? (
        <div className="empty panel">
          <div className="empty-icon">
            <BellOff size={20} />
          </div>
          <h3>No silences</h3>
          <p>Plan a maintenance window so a deploy or migration does not page anyone, or set quiet hours (say, weeknights for warnings) that come back every week.</p>
        </div>
      ) : (
        <div className="al-cards">
          {list.map((s) => {
            const Icon = s.schedule.kind === "once" ? CalendarClock : Moon;
            return (
              <section key={s.id} className={`panel al-card ${s.state === "ended" ? "al-card-dim" : ""}`}>
                <div className="al-card-head">
                  <span className="al-kind">
                    <Icon size={15} aria-hidden />
                  </span>
                  <div className="al-card-title">
                    <h3>{s.name}</h3>
                    <span className={`al-silence-state ${s.state}`}>{STATE_LABEL[s.state]}</span>
                  </div>
                </div>
                <dl className="kv al-kv">
                  <dt>{s.schedule.kind === "once" ? "Window" : "Every week"}</dt>
                  <dd>{describeSchedule(s.schedule)}</dd>
                  <dt>Applies to</dt>
                  <dd>{describeMatchers(s, rules)}</dd>
                  {s.created_by && (
                    <>
                      <dt>Created by</dt>
                      <dd>{s.created_by}</dd>
                    </>
                  )}
                </dl>
                <div className="al-card-actions">
                  <label className="al-check">
                    <input type="checkbox" checked={s.enabled} disabled={!meta.can_edit} onChange={() => toggle(s)} />
                    <span>On</span>
                  </label>
                  <button type="button" className="btn ghost sm" onClick={() => setEditing(s)} disabled={!meta.can_edit}>
                    <Pencil size={13} />
                    Edit
                  </button>
                  <button type="button" className="btn ghost sm" onClick={() => remove(s)} disabled={!meta.can_edit}>
                    <Trash2 size={13} />
                    Delete
                  </button>
                </div>
              </section>
            );
          })}
        </div>
      )}
      {editing && (
        <SilenceModal
          silence={editing === "new" ? null : editing}
          rules={rules}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            changed();
          }}
        />
      )}
    </>
  );
}

function SilenceModal({ silence, rules, onClose, onSaved }: { silence: Silence | null; rules: RuleView[]; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const now = new Date();
  const [name, setName] = useState(silence?.name ?? "");
  const [kind, setKind] = useState<"once" | "weekly">(silence?.schedule.kind ?? "once");
  const [start, setStart] = useState(() => localInput(silence?.schedule.starts_at ? new Date(silence.schedule.starts_at) : now));
  const [end, setEnd] = useState(() => localInput(silence?.schedule.ends_at ? new Date(silence.schedule.ends_at) : new Date(now.getTime() + 2 * 3600_000)));
  const [days, setDays] = useState<number[]>(silence?.schedule.days ?? [1, 2, 3, 4, 5]);
  const [from, setFrom] = useState(silence?.schedule.from ?? "22:00");
  const [to, setTo] = useState(silence?.schedule.to ?? "07:00");
  const [tz, setTz] = useState(silence?.schedule.tz || browserTZ());
  const [ruleIds, setRuleIds] = useState<number[]>(silence?.matchers.rule_ids ?? []);
  const [sevs, setSevs] = useState<Severity[]>(silence?.matchers.severities ?? []);
  const [labels, setLabels] = useState(labelPairs(silence?.matchers.labels).join(", "));
  const [series, setSeries] = useState(silence?.matchers.series ?? "");
  const [ruleFilter, setRuleFilter] = useState("");
  const [err, setErr] = useState("");
  const [saving, setSaving] = useState(false);

  const quick = (hours: number) => {
    const s = new Date();
    setStart(localInput(s));
    setEnd(localInput(new Date(s.getTime() + hours * 3600_000)));
  };

  const save = async () => {
    setErr("");
    const lbl: Record<string, string> = {};
    for (const part of labels.split(/[,\n]+/)) {
      const [k, ...v] = part.split("=");
      if (k?.trim()) lbl[k.trim()] = v.join("=").trim();
    }
    const body: SilenceInput = {
      name,
      enabled: silence?.enabled ?? true,
      matchers: { rule_ids: ruleIds.length ? ruleIds : undefined, severities: sevs.length ? sevs : undefined, labels: Object.keys(lbl).length ? lbl : undefined, series: series.trim() || undefined },
      schedule:
        kind === "once"
          ? { kind, starts_at: new Date(start).toISOString(), ends_at: new Date(end).toISOString() }
          : { kind, days: [...days].sort(), from, to, tz },
    };
    setSaving(true);
    try {
      if (silence) await alertsApi.updateSilence(silence.id, body);
      else await alertsApi.createSilence(body);
      toast("ok", "Silence saved");
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const shownRules = rules.filter((r) => !ruleFilter || r.name.toLowerCase().includes(ruleFilter.toLowerCase()));
  const matchesAll = !ruleIds.length && !sevs.length && !labels.trim() && !series.trim();

  return (
    <Modal
      title={silence ? "Edit silence" : "New silence"}
      onClose={onClose}
      narrow
      footer={
        <>
          {err && <span className="form-error" style={{ marginRight: "auto" }}>{err}</span>}
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={save} disabled={saving}>
            {saving ? "Saving…" : "Save silence"}
          </button>
        </>
      }
    >
      <div className="al-form">
        <label className="field">
          <span>Name</span>
          <input className="input" value={name} maxLength={120} placeholder={kind === "once" ? "e.g. Database migration" : "e.g. Quiet nights for warnings"} onChange={(e) => setName(e.target.value)} />
        </label>
        <div className="field">
          <span>When</span>
          <div className="segmented" role="radiogroup" aria-label="Schedule">
            <button type="button" role="radio" aria-checked={kind === "once"} className={kind === "once" ? "on" : ""} onClick={() => setKind("once")}>
              Maintenance window
            </button>
            <button type="button" role="radio" aria-checked={kind === "weekly"} className={kind === "weekly" ? "on" : ""} onClick={() => setKind("weekly")}>
              Quiet hours, every week
            </button>
          </div>
        </div>
        {kind === "once" ? (
          <>
            <div className="al-row">
              <label className="field grow">
                <span>Starts</span>
                <input className="input" type="datetime-local" value={start} onChange={(e) => setStart(e.target.value)} />
              </label>
              <label className="field grow">
                <span>Ends</span>
                <input className="input" type="datetime-local" value={end} onChange={(e) => setEnd(e.target.value)} />
              </label>
            </div>
            <div className="al-quick">
              <span className="al-hint">From now for</span>
              {[1, 2, 4, 8, 24].map((h) => (
                <button type="button" key={h} className="btn ghost sm" onClick={() => quick(h)}>
                  {h < 24 ? `${h}h` : "1 day"}
                </button>
              ))}
            </div>
          </>
        ) : (
          <>
            <div className="field">
              <span>Days</span>
              <div className="al-days" role="group" aria-label="Days">
                {WEEKDAYS.map((d, i) => (
                  <button type="button" key={d} aria-pressed={days.includes(i)} className={`al-day ${days.includes(i) ? "on" : ""}`} onClick={() => setDays(days.includes(i) ? days.filter((x) => x !== i) : [...days, i])}>
                    {d.slice(0, 3)}
                  </button>
                ))}
              </div>
            </div>
            <div className="al-row">
              <label className="field">
                <span>From</span>
                <input className="input" type="time" value={from} onChange={(e) => setFrom(e.target.value)} />
              </label>
              <label className="field">
                <span>To</span>
                <input className="input" type="time" value={to} onChange={(e) => setTo(e.target.value)} />
              </label>
              <label className="field grow">
                <span>Time zone</span>
                <input className="input" value={tz} onChange={(e) => setTz(e.target.value)} placeholder="Europe/Berlin" />
              </label>
            </div>
            <p className="al-hint" style={{ marginTop: -6 }}>
              A time range that ends before it starts runs overnight: 22:00 to 07:00 on Friday covers Saturday morning.
            </p>
          </>
        )}

        <div className="field">
          <span>Applies to</span>
          <p className="al-hint">{matchesAll ? "Every alert, until you narrow it below." : "Alerts that match all of the following."}</p>
        </div>
        <div className="field">
          <span>Severity</span>
          <div className="al-checks">
            {(["info", "warning", "critical"] as Severity[]).map((s) => (
              <label key={s} className="al-check">
                <input type="checkbox" checked={sevs.includes(s)} onChange={(e) => setSevs(e.target.checked ? [...sevs, s] : sevs.filter((x) => x !== s))} />
                <span>{titleCase(s)}</span>
              </label>
            ))}
          </div>
        </div>
        <div className="field">
          <span>Rules{ruleIds.length ? ` · ${ruleIds.length} picked` : ""}</span>
          {rules.length > 8 && <input className="input" value={ruleFilter} placeholder="Find a rule" onChange={(e) => setRuleFilter(e.target.value)} />}
          <div className="al-checks al-checks-col al-rule-pick">
            {shownRules.map((r) => (
              <label key={r.id} className="al-check">
                <input type="checkbox" checked={ruleIds.includes(r.id)} onChange={(e) => setRuleIds(e.target.checked ? [...ruleIds, r.id] : ruleIds.filter((x) => x !== r.id))} />
                <span>{r.name}</span>
              </label>
            ))}
            {rules.length === 0 && <span className="al-hint">No rules yet.</span>}
          </div>
        </div>
        <div className="al-row">
          <label className="field grow">
            <span>Labels</span>
            <input className="input mono" value={labels} placeholder="team=payments" onChange={(e) => setLabels(e.target.value)} />
          </label>
          <label className="field grow">
            <span>Series</span>
            <input className="input mono" value={series} placeholder="web-* (instance, check, host)" onChange={(e) => setSeries(e.target.value)} />
          </label>
        </div>
      </div>
    </Modal>
  );
}
