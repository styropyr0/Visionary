import { useState } from "react";
import { BellOff, BellRing, Play, Plus, Siren } from "lucide-react";
import { useToast } from "../Toast";
import { alertsApi, describeCondition, formatAlertValue, isMuted, ruleUnit, type AlertsMeta, type Channel, type RuleView } from "../../lib/alerts";
import { formatDateTime } from "../../lib/time";
import { SeverityTag, Sparkline, StateBadge, timeAgo } from "./bits";
import { RuleDrawer } from "./RuleDrawer";
import { usePopover } from "../Toolbar";

interface Props {
  meta: AlertsMeta;
  rules: RuleView[];
  channels: Channel[];
  loading: boolean;
  onChange: () => void;
}

const ORDER: Record<string, number> = { firing: 0, pending: 1, resolved: 2, ok: 3, "": 4 };

export function RulesTab({ meta, rules, channels, loading, onChange }: Props) {
  const [editing, setEditing] = useState<RuleView | "new" | null>(null);
  const sorted = [...rules].sort((a, b) => (ORDER[a.status.state] ?? 5) - (ORDER[b.status.state] ?? 5) || a.name.localeCompare(b.name));
  const firing = rules.filter((r) => r.status.state === "firing").length;
  const pending = rules.filter((r) => r.status.state === "pending").length;

  return (
    <>
      <div className="al-toolbar">
        <div className="al-counts">
          <span>
            <strong>{rules.length}</strong> rules
          </span>
          <span className={firing ? "al-count-firing" : ""}>
            <strong>{firing}</strong> firing
          </span>
          <span>
            <strong>{pending}</strong> pending
          </span>
          <span className="al-hint">Evaluated every {meta.interval_s}s</span>
        </div>
        <button className="btn primary" onClick={() => setEditing("new")} disabled={!meta.can_edit}>
          <Plus size={15} />
          New rule
        </button>
      </div>

      {rules.length === 0 && !loading ? (
        <div className="empty panel">
          <div className="empty-icon">
            <Siren size={20} />
          </div>
          <h3>No alert rules yet</h3>
          <p>Rules watch a metric (latency, errors, traffic, CPU, an instance going quiet, an uptime check), a seasonal baseline, or an SLO's error budget, and notify your channels when they fire.</p>
          <button className="btn primary" onClick={() => setEditing("new")} disabled={!meta.can_edit}>
            <Plus size={15} />
            Create the first rule
          </button>
        </div>
      ) : (
        <div className="panel">
          <div className="table-wrap">
            <table className="data al-rules">
              <thead>
                <tr>
                  <th style={{ width: 120 }}>State</th>
                  <th>Rule</th>
                  <th style={{ width: 90 }}>Severity</th>
                  <th className="num" style={{ width: 110 }}>
                    Last value
                  </th>
                  <th style={{ width: 140 }}>Recent</th>
                  <th style={{ width: 130 }}>Since</th>
                  <th style={{ width: 110 }} />
                </tr>
              </thead>
              <tbody>
                {sorted.map((r) => (
                  <RuleRow key={r.id} rule={r} meta={meta} onEdit={() => setEditing(r)} onChange={onChange} />
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {editing && (
        <RuleDrawer
          meta={meta}
          channels={channels}
          rule={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            onChange();
          }}
        />
      )}
    </>
  );
}

function RuleRow({ rule, meta, onEdit, onChange }: { rule: RuleView; meta: AlertsMeta; onEdit: () => void; onChange: () => void }) {
  const toast = useToast();
  const unit = ruleUnit(rule, meta.metrics);
  const muted = isMuted(rule);
  const st = !rule.enabled ? "disabled" : rule.status.state;
  const fanout = rule.status.series.length > 1;

  const evaluate = async () => {
    try {
      await alertsApi.evaluate(rule.id);
      onChange();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };

  return (
    <tr className="al-rule-row" onClick={onEdit}>
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
          {fanout && <span className="pill">{rule.status.firing ? `${rule.status.firing} of ${rule.status.series.length} firing` : `${rule.status.series.length} series`}</span>}
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
          <button className="icon-btn" title="Evaluate now" aria-label="Evaluate now" onClick={evaluate} disabled={!meta.can_edit}>
            <Play size={14} />
          </button>
          <MuteMenu rule={rule} disabled={!meta.can_edit} onChange={onChange} />
        </div>
      </td>
    </tr>
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
  const { open, setOpen, ref } = usePopover();
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
      <button className={`icon-btn ${muted ? "on" : ""}`} title={muted ? "Muted: change or unmute" : "Mute (snooze)"} aria-label="Mute" onClick={() => setOpen(!open)} disabled={disabled}>
        {muted ? <BellOff size={14} /> : <BellRing size={14} />}
      </button>
      {open && (
        <div className="popover" role="menu" style={{ minWidth: 180 }}>
          <div className="menu-title">Mute notifications for</div>
          {SNOOZE.map((s) => (
            <button key={s.m} className="menu-row" role="menuitem" onClick={() => run(s.m)}>
              <span className="check" />
              {s.label}
            </button>
          ))}
          {muted && (
            <>
              <div className="menu-sep" />
              <button className="menu-row" role="menuitem" onClick={() => run(0)}>
                <span className="check" />
                Unmute now
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
