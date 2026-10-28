import { useEffect, useMemo, useRef, useState } from "react";
import { BellOff, BellRing, CheckCheck, Download, Play, Plus, Search, Siren, Upload, X } from "lucide-react";
import { useToast } from "../Toast";
import { Modal } from "../Modal";
import { Select } from "../Select";
import {
  alertsApi,
  describeCondition,
  formatAlertValue,
  isMuted,
  labelPairs,
  ruleUnit,
  type AlertsMeta,
  type BulkAction,
  type Channel,
  type ImportResult,
  type RuleInput,
  type RuleView,
} from "../../lib/alerts";
import { formatDateTime } from "../../lib/time";
import { SeverityTag, Sparkline, StateBadge, timeAgo } from "./bits";
import { RuleDrawer } from "./RuleDrawer";
import { usePopover } from "../Toolbar";
import { FloatingMenu } from "../Floating";

interface Props {
  meta: AlertsMeta;
  rules: RuleView[];
  channels: Channel[];
  loading: boolean;
  onChange: () => void;
}

const ORDER: Record<string, number> = { firing: 0, pending: 1, resolved: 2, ok: 3, "": 4 };

type Editing = { rule: RuleView } | { initial?: RuleInput } | null;

export function RulesTab({ meta, rules, channels, loading, onChange }: Props) {
  const toast = useToast();
  const [editing, setEditing] = useState<Editing>(null);
  const [search, setSearch] = useState("");
  const [state, setState] = useState("");
  const [severity, setSeverity] = useState("");
  const [label, setLabel] = useState("");
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [imported, setImported] = useState<ImportResult | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  // #/alerts?rule=12 opens that rule.
  const linked = useRef(new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("rule"));
  useEffect(() => {
    const id = Number(linked.current);
    if (!id) return;
    const r = rules.find((x) => x.id === id);
    if (r) {
      linked.current = null;
      setEditing({ rule: r });
      window.history.replaceState(null, "", "#/alerts");
    }
  }, [rules]);

  const allLabels = useMemo(() => [...new Set(rules.flatMap((r) => labelPairs(r.spec.labels)))].sort(), [rules]);
  const shown = useMemo(() => {
    const q = search.trim().toLowerCase();
    return [...rules]
      .filter((r) => {
        if (q && !`${r.name} ${describeCondition(r, meta.metrics)} ${labelPairs(r.spec.labels).join(" ")}`.toLowerCase().includes(q)) return false;
        if (severity && r.severity !== severity) return false;
        if (label && !labelPairs(r.spec.labels).includes(label)) return false;
        switch (state) {
          case "firing":
            return r.enabled && r.status.state === "firing";
          case "unacked":
            return r.enabled && r.status.firing > r.status.acked;
          case "pending":
            return r.enabled && r.status.state === "pending";
          case "muted":
            return isMuted(r);
          case "disabled":
            return !r.enabled;
          case "failing":
            return !!r.status.error;
        }
        return true;
      })
      .sort((a, b) => (ORDER[a.status.state] ?? 5) - (ORDER[b.status.state] ?? 5) || a.name.localeCompare(b.name));
  }, [rules, search, state, severity, label, meta.metrics]);
  const firing = rules.filter((r) => r.enabled && r.status.state === "firing").length;
  const unacked = rules.reduce((n, r) => n + (r.enabled ? r.status.firing - r.status.acked : 0), 0);
  const pending = rules.filter((r) => r.enabled && r.status.state === "pending").length;
  const filtered = shown.length !== rules.length;
  const picked = shown.filter((r) => selected.has(r.id));

  const toggle = (id: number, on: boolean) =>
    setSelected((s) => {
      const n = new Set(s);
      if (on) n.add(id);
      else n.delete(id);
      return n;
    });

  const bulk = async (action: BulkAction, minutes = 0) => {
    const ids = picked.map((r) => r.id);
    if (action === "delete" && !window.confirm(`Delete ${ids.length} rule${ids.length === 1 ? "" : "s"}? Their history stays in Activity.`)) return;
    try {
      const r = await alertsApi.bulk(ids, action, minutes);
      toast("ok", `${r.changed} rule${r.changed === 1 ? "" : "s"} ${action === "delete" ? "deleted" : action === "mute" ? "muted" : `${action}d`}`);
      setSelected(new Set());
      onChange();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };

  const importFile = async (file: File) => {
    try {
      const data = JSON.parse(await file.text());
      const r = await alertsApi.import(data);
      setImported(r);
      onChange();
    } catch (e) {
      toast("err", e instanceof SyntaxError ? "That file is not JSON." : (e as Error).message);
    } finally {
      if (fileRef.current) fileRef.current.value = "";
    }
  };

  return (
    <>
      <div className="al-toolbar">
        <div className="al-counts">
          <span>
            <strong>{rules.length}</strong> rules
          </span>
          <button type="button" className={`al-count-btn ${firing ? "al-count-firing" : ""}`} onClick={() => setState(state === "firing" ? "" : "firing")} aria-pressed={state === "firing"}>
            <strong>{firing}</strong> firing
          </button>
          {unacked > 0 && (
            <button type="button" className="al-count-btn al-count-firing" onClick={() => setState(state === "unacked" ? "" : "unacked")} aria-pressed={state === "unacked"}>
              <strong>{unacked}</strong> unacknowledged
            </button>
          )}
          <span>
            <strong>{pending}</strong> pending
          </span>
          <span className="al-hint">Evaluated every {meta.interval_s}s</span>
        </div>
        <div className="al-toolbar-actions">
          <a className="btn ghost" href={alertsApi.exportURL} download title="Download every rule and silence as JSON (channel secrets stay here)">
            <Download size={14} />
            Export
          </a>
          <button type="button" className="btn ghost" onClick={() => fileRef.current?.click()} disabled={!meta.can_edit} title="Add rules from an export">
            <Upload size={14} />
            Import
          </button>
          <input ref={fileRef} type="file" accept="application/json,.json" hidden onChange={(e) => e.target.files?.[0] && void importFile(e.target.files[0])} />
          <button type="button" className="btn primary" onClick={() => setEditing({})} disabled={!meta.can_edit}>
            <Plus size={15} />
            New rule
          </button>
        </div>
      </div>

      {rules.length > 0 && (
        <div className="al-filters al-rule-filters">
          <label className="al-search">
            <Search size={14} aria-hidden />
            <input className="input" value={search} placeholder="Search rules" aria-label="Search rules" onChange={(e) => setSearch(e.target.value)} />
          </label>
          <Select
            label="State"
            align="left"
            value={state}
            onChange={setState}
            options={[
              { value: "", label: "Any state" },
              { value: "firing", label: "Firing" },
              { value: "unacked", label: "Firing, not acknowledged" },
              { value: "pending", label: "Pending" },
              { value: "muted", label: "Muted" },
              { value: "disabled", label: "Disabled" },
              { value: "failing", label: "Evaluation failing" },
            ]}
          />
          <Select
            label="Severity"
            align="left"
            value={severity}
            onChange={setSeverity}
            options={[
              { value: "", label: "Any severity" },
              { value: "critical", label: "Critical" },
              { value: "warning", label: "Warning" },
              { value: "info", label: "Info" },
            ]}
          />
          {allLabels.length > 0 && <Select label="Label" align="left" maxWidth={220} value={label} onChange={setLabel} options={[{ value: "", label: "Any label" }, ...allLabels.map((l) => ({ value: l, label: l }))]} />}
          {filtered && (
            <button
              type="button"
              className="btn ghost sm"
              onClick={() => {
                setSearch("");
                setState("");
                setSeverity("");
                setLabel("");
              }}
            >
              <X size={13} /> Clear
            </button>
          )}
        </div>
      )}

      {picked.length > 0 && meta.can_edit && (
        <div className="al-bulk" role="toolbar" aria-label="Selected rules">
          <strong>{picked.length} selected</strong>
          <button type="button" className="btn sm" onClick={() => bulk("enable")}>
            Enable
          </button>
          <button type="button" className="btn sm" onClick={() => bulk("disable")}>
            Disable
          </button>
          <BulkMute onMute={(m) => bulk("mute", m)} />
          <button type="button" className="btn sm" onClick={() => bulk("unmute")}>
            Unmute
          </button>
          <button type="button" className="btn sm danger" onClick={() => bulk("delete")}>
            Delete
          </button>
          <button type="button" className="btn ghost sm" onClick={() => setSelected(new Set())}>
            Clear selection
          </button>
        </div>
      )}

      {rules.length === 0 && !loading ? (
        <div className="empty panel">
          <div className="empty-icon">
            <Siren size={20} />
          </div>
          <h3>No alert rules yet</h3>
          <p>
            Rules watch a metric (latency, errors, traffic, hosts, exceptions, Web Vitals, uptime checks, certificates and more), a seasonal baseline, or an SLO's error budget, and notify your channels
            when they fire. Start from one of the presets.
          </p>
          <button type="button" className="btn primary" onClick={() => setEditing({})} disabled={!meta.can_edit}>
            <Plus size={15} />
            Create the first rule
          </button>
        </div>
      ) : shown.length === 0 ? (
        <div className="empty panel">
          <h3>No rules match</h3>
          <p>Change the search or filters above.</p>
        </div>
      ) : (
        <div className="panel">
          <div className="table-wrap">
            <table className="data al-rules">
              <thead>
                <tr>
                  {meta.can_edit && (
                    <th style={{ width: 32 }}>
                      <input
                        type="checkbox"
                        aria-label="Select all shown rules"
                        checked={picked.length === shown.length}
                        ref={(el) => {
                          if (el) el.indeterminate = picked.length > 0 && picked.length < shown.length;
                        }}
                        onChange={(e) => setSelected(e.target.checked ? new Set(shown.map((r) => r.id)) : new Set())}
                      />
                    </th>
                  )}
                  <th style={{ width: 120 }}>State</th>
                  <th>Rule</th>
                  <th style={{ width: 90 }}>Severity</th>
                  <th className="num" style={{ width: 110 }}>
                    Last value
                  </th>
                  <th style={{ width: 140 }}>Recent</th>
                  <th style={{ width: 130 }}>Since</th>
                  <th style={{ width: 120 }} />
                </tr>
              </thead>
              <tbody>
                {shown.map((r) => (
                  <RuleRow key={r.id} rule={r} meta={meta} selected={selected.has(r.id)} onSelect={(on) => toggle(r.id, on)} onEdit={() => setEditing({ rule: r })} onChange={onChange} />
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {editing && (
        <RuleDrawer
          key={"rule" in editing ? `r${editing.rule.id}` : `n${JSON.stringify(editing.initial ?? "")}`}
          meta={meta}
          channels={channels}
          rules={rules}
          rule={"rule" in editing ? editing.rule : null}
          initial={"initial" in editing ? editing.initial : undefined}
          onClose={() => setEditing(null)}
          onDuplicate={(input) => setEditing({ initial: input })}
          onSaved={() => {
            setEditing(null);
            onChange();
          }}
        />
      )}

      {imported && (
        <Modal
          title="Import finished"
          narrow
          onClose={() => setImported(null)}
          footer={
            <button type="button" className="btn primary" onClick={() => setImported(null)}>
              Done
            </button>
          }
        >
          <p style={{ marginTop: 0 }}>
            Added {imported.created} rule{imported.created === 1 ? "" : "s"} and {imported.silences} silence{imported.silences === 1 ? "" : "s"}.
            {imported.skipped > 0 && ` Skipped ${imported.skipped} with a name that already exists.`}
          </p>
          {imported.warnings.length > 0 && (
            <>
              <p className="al-hint">Worth a look:</p>
              <ul className="al-warnings">
                {imported.warnings.slice(0, 30).map((w, i) => (
                  <li key={i}>{w}</li>
                ))}
              </ul>
            </>
          )}
        </Modal>
      )}
    </>
  );
}

function RuleRow({ rule, meta, selected, onSelect, onEdit, onChange }: { rule: RuleView; meta: AlertsMeta; selected: boolean; onSelect: (on: boolean) => void; onEdit: () => void; onChange: () => void }) {
  const toast = useToast();
  const unit = ruleUnit(rule, meta.metrics);
  const muted = isMuted(rule);
  const st = !rule.enabled ? "disabled" : rule.status.state;
  const fanout = rule.status.series.length > 1;
  const labels = labelPairs(rule.spec.labels);
  const acked = rule.status.series.filter((s) => s.state === "firing" && s.ack);

  const evaluate = async () => {
    try {
      await alertsApi.evaluate(rule.id);
      onChange();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };

  return (
    <tr className={`al-rule-row ${selected ? "selected" : ""}`} onClick={onEdit}>
      {meta.can_edit && (
        <td onClick={(e) => e.stopPropagation()}>
          <input type="checkbox" aria-label={`Select ${rule.name}`} checked={selected} onChange={(e) => onSelect(e.target.checked)} />
        </td>
      )}
      <td>
        <StateBadge state={st} />
      </td>
      <td>
        <div className="al-rule-name">
          {rule.name}
          {muted && (
            <span className="pill" title={`Muted until ${formatDateTime(new Date(rule.muted_until))}`}>
              <BellOff size={11} /> Muted
            </span>
          )}
          {acked.length > 0 && (
            <span className="pill" title={acked.map((s) => `${s.label || s.key || "Acknowledged"} by ${s.ack!.by}${s.ack!.note ? `: ${s.ack!.note}` : ""}`).join("\n")}>
              <CheckCheck size={11} /> {fanout ? `${acked.length} acknowledged` : `Acknowledged by ${acked[0].ack!.by}`}
            </span>
          )}
          {fanout && <span className="pill">{rule.status.firing ? `${rule.status.firing} of ${rule.status.series.length} firing` : `${rule.status.series.length} series`}</span>}
          {labels.slice(0, 3).map((l) => (
            <span key={l} className="pill al-label">
              {l}
            </span>
          ))}
        </div>
        <div className="al-rule-cond">{describeCondition(rule, meta.metrics)}</div>
        {rule.status.error ? <div className="al-rule-err">Evaluation failed: {rule.status.error}</div> : rule.status.state === "firing" && <div className="al-rule-msg">{rule.status.message}</div>}
      </td>
      <td>
        <SeverityTag severity={rule.severity} />
      </td>
      <td className="num mono">{formatAlertValue(rule.status.value, unit)}</td>
      <td>
        <Sparkline points={rule.status.recent} threshold={rule.kind === "threshold" || rule.kind === "slo" ? rule.status.threshold : null} />
      </td>
      <td className="al-muted-cell" title={rule.status.since}>
        {rule.status.since ? timeAgo(rule.status.since) : "—"}
      </td>
      <td onClick={(e) => e.stopPropagation()}>
        <div className="al-row-actions">
          {rule.enabled && rule.status.firing > 0 && <AckMenu rule={rule} disabled={!meta.can_edit} onChange={onChange} />}
          <button type="button" className="icon-btn" title="Evaluate now" aria-label="Evaluate now" onClick={evaluate} disabled={!meta.can_edit}>
            <Play size={14} />
          </button>
          <MuteMenu rule={rule} disabled={!meta.can_edit} onChange={onChange} />
        </div>
      </td>
    </tr>
  );
}

/** Acknowledge every firing series, or one at a time for rules with several. */
function AckMenu({ rule, disabled, onChange }: { rule: RuleView; disabled: boolean; onChange: () => void }) {
  const { open, setOpen, ref, menuRef } = usePopover();
  const toast = useToast();
  const [note, setNote] = useState("");
  const firing = rule.status.series.filter((s) => s.state === "firing");
  const open_ = firing.filter((s) => !s.ack);
  const run = async (fn: () => Promise<unknown>, msg: string) => {
    try {
      await fn();
      toast("ok", msg);
      setNote("");
      setOpen(false);
      onChange();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };
  return (
    <div className="popover-anchor" ref={ref}>
      <button
        type="button"
        className={`icon-btn ${open_.length === 0 ? "on" : "al-ack-needed"}`}
        title={open_.length ? "Acknowledge: stop reminders and escalation" : "Acknowledged"}
        aria-label="Acknowledge"
        onClick={() => setOpen(!open)}
        disabled={disabled}
      >
        <CheckCheck size={14} />
      </button>
      {open && (
        <FloatingMenu anchor={ref} menuRef={menuRef} className="menu-scroll al-ack-pop" role="dialog" ariaLabel="Acknowledge" maxHeight={420}>
          <div className="menu-title">Acknowledge</div>
          <p className="al-hint">Reminders and escalation stop until it resolves; the channels that were told hear who took it.</p>
          <input className="input" value={note} maxLength={500} placeholder="Note (optional), e.g. looking into it" onChange={(e) => setNote(e.target.value)} />
          {open_.length > 0 && (
            <button type="button" className="btn primary sm" onClick={() => run(() => alertsApi.ack(rule.id, note), "Acknowledged")}>
              <CheckCheck size={13} /> Acknowledge {firing.length > 1 ? `all ${open_.length}` : ""}
            </button>
          )}
          {firing.length > 1 && (
            <ul className="al-ack-list">
              {firing.map((s) => (
                <li key={s.key}>
                  <span className="mono">{s.label || s.key}</span>
                  {s.ack ? (
                    <button type="button" className="btn ghost sm" onClick={() => run(() => alertsApi.unack(rule.id, s.key), "Acknowledgement undone")}>
                      Undo ({s.ack.by})
                    </button>
                  ) : (
                    <button type="button" className="btn ghost sm" onClick={() => run(() => alertsApi.ack(rule.id, note, s.key), "Acknowledged")}>
                      Acknowledge
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {firing.length === 1 && firing[0].ack && (
            <button type="button" className="btn ghost sm" onClick={() => run(() => alertsApi.unack(rule.id), "Acknowledgement undone")}>
              Undo acknowledgement by {firing[0].ack.by}
            </button>
          )}
        </FloatingMenu>
      )}
    </div>
  );
}

const SNOOZE = [
  { m: 30, label: "30 minutes" },
  { m: 60, label: "1 hour" },
  { m: 240, label: "4 hours" },
  { m: 1440, label: "1 day" },
  { m: 10080, label: "1 week" },
];

function MuteMenu({ rule, disabled, onChange }: { rule: RuleView; disabled: boolean; onChange: () => void }) {
  const { open, setOpen, ref, menuRef } = usePopover();
  const toast = useToast();
  const muted = isMuted(rule);
  const run = async (minutes: number) => {
    setOpen(false);
    try {
      await alertsApi.mute(rule.id, minutes);
      toast("ok", minutes ? "Rule muted" : "Rule unmuted");
      onChange();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };
  return (
    <div className="popover-anchor" ref={ref}>
      <button type="button" className={`icon-btn ${muted ? "on" : ""}`} title={muted ? "Muted: change or unmute" : "Mute (snooze)"} aria-label="Mute" onClick={() => setOpen(!open)} disabled={disabled}>
        {muted ? <BellOff size={14} /> : <BellRing size={14} />}
      </button>
      {open && (
        <FloatingMenu anchor={ref} menuRef={menuRef} className="menu-scroll" role="menu" style={{ minWidth: 180 }}>
          <div className="menu-title">Mute notifications for</div>
          {SNOOZE.map((s) => (
            <button type="button" key={s.m} className="menu-row" role="menuitem" onClick={() => run(s.m)}>
              <span className="check" />
              {s.label}
            </button>
          ))}
          {muted && (
            <>
              <div className="menu-sep" />
              <button type="button" className="menu-row" role="menuitem" onClick={() => run(0)}>
                <span className="check" />
                Unmute now
              </button>
            </>
          )}
        </FloatingMenu>
      )}
    </div>
  );
}

function BulkMute({ onMute }: { onMute: (minutes: number) => void }) {
  const { open, setOpen, ref } = usePopover();
  return (
    <div className="popover-anchor" ref={ref}>
      <button type="button" className="btn sm" onClick={() => setOpen(!open)} aria-haspopup="menu" aria-expanded={open}>
        Mute…
      </button>
      {open && (
        <div className="popover" role="menu" style={{ minWidth: 160 }}>
          {SNOOZE.map((s) => (
            <button
              type="button"
              key={s.m}
              className="menu-row"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                onMute(s.m);
              }}
            >
              <span className="check" />
              {s.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
