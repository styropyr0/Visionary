import { useState } from "react";
import { AlertTriangle, BellOff, BellRing } from "lucide-react";
import { Modal } from "../components/Modal";
import { useToast } from "../components/Toast";
import { KIND_LABEL } from "../components/alerts/ChannelsTab";
import { ruleInputOf } from "../components/alerts/RuleDrawer";
import { alertsApi, type Channel } from "../lib/alerts";
import { titleCase } from "../lib/format";
import { useQuery } from "../lib/useQuery";
import { alertGap, setCheckAlert, type CheckAlert, type CheckAlertInput, type CheckList } from "../lib/uptime";
// The alert form pieces come from the Alerts page.
import "./Alerts.css";

// How uptime checks are alerted on: a line per check in the list, a banner
// when some checks would go down unheard, and the form's alert section.

/** Under a check's name: who hears when it goes down, or that nobody does. */
export function CheckAlertLine({ alert, onFix, canEdit }: { alert: CheckAlert | undefined; onFix: () => void; canEdit: boolean }) {
  const gap = alertGap(alert);
  if (gap) {
    return canEdit ? (
      <button
        type="button"
        className="up-alert-gap"
        onClick={(e) => {
          e.stopPropagation();
          onFix();
        }}
      >
        <AlertTriangle size={12} aria-hidden /> {gap}. Set up
      </button>
    ) : (
      <span className="up-alert-gap static">
        <AlertTriangle size={12} aria-hidden /> {gap}
      </span>
    );
  }
  return (
    <span className="up-alert-ok" title={`${alert!.rule_name}: after ${alert!.failures} failed check${alert!.failures === 1 ? "" : "s"} in a row`}>
      <BellRing size={11} aria-hidden /> {alert!.channels.join(", ")}
    </span>
  );
}

/**
 * Says how many checks nobody would hear about, and fixes the common case:
 * the rule for every check, sent to the channels picked here.
 */
export function CoverageBanner({ data, onDone }: { data: CheckList; onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const gaps = data.checks.filter((c) => c.check.enabled && alertGap(c.alert));
  if (gaps.length === 0) return null;
  const named = gaps.slice(0, 3).map((c) => c.check.name);
  return (
    <div className="notice up-coverage span-12">
      <AlertTriangle size={16} aria-hidden />
      <span>
        {gaps.length === data.checks.length ? (gaps.length === 1 ? "This check alerts nobody" : "None of these checks alert anyone") : `${gaps.length} check${gaps.length === 1 ? "" : "s"} alert nobody`} when
        {gaps.length === 1 ? " it goes" : " they go"} down
        {gaps.length < data.checks.length && `: ${named.join(", ")}${gaps.length > 3 ? "…" : ""}`}.
      </span>
      {data.can_edit && (
        <button type="button" className="btn sm primary" onClick={() => setOpen(true)}>
          Set up alerts
        </button>
      )}
      {open && (
        <DefaultRuleModal
          data={data}
          onClose={() => setOpen(false)}
          onDone={() => {
            setOpen(false);
            onDone();
          }}
        />
      )}
    </div>
  );
}

function DefaultRuleModal({ data, onClose, onDone }: { data: CheckList; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const channels = useQuery("uptime-alert-channels", (signal) => alertsApi.channels(signal));
  const [picked, setPicked] = useState<number[] | null>(null);
  const [failures, setFailures] = useState(2);
  const [busy, setBusy] = useState(false);
  const list = channels.data?.channels ?? [];
  const chosen = picked ?? list.slice(0, 1).map((c) => c.id);
  // Checks switched off on purpose keep their setting.
  const fixable = data.checks.filter((c) => c.check.enabled && alertGap(c.alert) && c.alert.mode !== "off");

  const save = async () => {
    setBusy(true);
    try {
      // Moving a check to the default rule creates that rule when missing.
      let ruleID = data.checks.find((c) => c.alert.mode === "default")?.alert.rule_id;
      for (const c of fixable) {
        const { alert } = await setCheckAlert(c.check.id, { mode: "default" });
        ruleID = alert.rule_id ?? ruleID;
      }
      const { rules } = await alertsApi.rules();
      // A default rule that was switched off is turned back on here.
      const rule = rules.find((r) => r.id === ruleID) ?? rules.find((r) => r.kind === "threshold" && r.spec.metric === "uptime_failing" && !r.spec.scope?.check);
      if (rule) {
        const input = ruleInputOf(rule);
        await alertsApi.updateRule(rule.id, { ...input, enabled: true, channels: chosen, spec: { ...input.spec, threshold: failures } });
      }
      toast("ok", "Checks without their own alert settings now alert when they go down");
      onDone();
    } catch (e) {
      toast("err", (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="Alert when a check goes down"
      narrow
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={save} disabled={busy || chosen.length === 0}>
            {busy ? "Saving…" : "Save"}
          </button>
        </>
      }
    >
      <div className="al-form">
        <p className="al-hint" style={{ margin: 0 }}>
          One rule covers every check. A check with its own alert settings (in its form) is left out of it, so nothing is alerted twice.
        </p>
        <ChannelPicker channels={list} loading={!channels.data} value={chosen} onChange={setPicked} />
        <FailuresField value={failures} onChange={setFailures} />
      </div>
    </Modal>
  );
}

function ChannelPicker({ channels, loading, value, onChange }: { channels: Channel[]; loading: boolean; value: number[]; onChange: (ids: number[]) => void }) {
  return (
    <div className="field">
      <span>Notify</span>
      {loading ? (
        <p className="al-hint">Loading channels…</p>
      ) : channels.length === 0 ? (
        <p className="form-error" style={{ margin: 0 }}>
          No notification channels yet. <a href="#/alerts">Add one in Alerts → Channels</a> first, or nobody hears about it.
        </p>
      ) : (
        <div className="al-checks">
          {channels.map((c) => (
            <label key={c.id} className="al-check">
              <input type="checkbox" checked={value.includes(c.id)} onChange={(e) => onChange(e.target.checked ? [...value, c.id] : value.filter((x) => x !== c.id))} />
              <span>{c.name}</span>
              <span className="al-hint">{KIND_LABEL[c.kind]}</span>
            </label>
          ))}
        </div>
      )}
    </div>
  );
}

function FailuresField({ value, onChange }: { value: number; onChange: (n: number) => void }) {
  return (
    <label className="field">
      <span>After failed checks in a row</span>
      <input className="input num" type="number" min={1} max={20} value={value} onChange={(e) => onChange(Math.max(1, Math.min(20, Number(e.target.value) || 1)))} />
      <small>1 alerts on the first failure; 2 rides out a single blip (each failure is already retried once).</small>
    </label>
  );
}

/** The alert part of the check form. */
export interface CheckAlertDraft extends CheckAlertInput {
  /** Whether the form changed it; an untouched draft is not sent. */
  touched: boolean;
}

export function draftOf(a: CheckAlert | undefined): CheckAlertDraft {
  if (!a || a.mode === "none" || a.mode === "default") return { mode: "default", touched: false, failures: a?.failures || 2, channels: a?.channel_ids ?? [] };
  return { mode: a.mode, touched: false, channels: a.channel_ids, failures: a.failures, severity: a.severity };
}

export function CheckAlertSection({ current, draft, onChange }: { current: CheckAlert | undefined; draft: CheckAlertDraft; onChange: (d: CheckAlertDraft) => void }) {
  const channels = useQuery("uptime-alert-channels", (signal) => alertsApi.channels(signal));
  const list = channels.data?.channels ?? [];
  const set = (p: Partial<CheckAlertDraft>) => onChange({ ...draft, ...p, touched: true });
  const def = current?.mode === "default" ? current : undefined;
  return (
    <fieldset className="up-alerts">
      <legend>Alerts when it goes down</legend>
      <div className="up-alert-modes" role="radiogroup" aria-label="Alerts">
        <label className="al-check">
          <input type="radio" name="up-alert" checked={draft.mode === "default"} onChange={() => set({ mode: "default" })} />
          <span>
            The rule for every check
            <small>
              {def
                ? def.channels.length
                  ? `“${def.rule_name}”, to ${def.channels.join(", ")}, after ${def.failures} failure${def.failures === 1 ? "" : "s"}`
                  : `“${def.rule_name}” has no channels yet`
                : list.length
                  ? `Created now if missing, sending to ${list[0].name}`
                  : "Created now if missing; add a channel so someone hears"}
            </small>
          </span>
        </label>
        <label className="al-check">
          <input type="radio" name="up-alert" checked={draft.mode === "custom"} onChange={() => set({ mode: "custom", channels: draft.channels?.length ? draft.channels : list.slice(0, 1).map((c) => c.id) })} />
          <span>
            Its own settings
            <small>Other channels, failures or severity for this check only</small>
          </span>
        </label>
        <label className="al-check">
          <input type="radio" name="up-alert" checked={draft.mode === "off"} onChange={() => set({ mode: "off" })} />
          <span>
            <span className="al-inline">
              <BellOff size={12} aria-hidden /> Don't alert
            </span>
            <small>Results and incidents are still recorded</small>
          </span>
        </label>
      </div>
      {draft.mode === "custom" && (
        <div className="up-alert-custom">
          <ChannelPicker channels={list} loading={!channels.data} value={draft.channels ?? []} onChange={(ids) => set({ channels: ids })} />
          <div className="up-row">
            <FailuresField value={draft.failures || 2} onChange={(n) => set({ failures: n })} />
            <div className="field">
              <span>Severity</span>
              <div className="segmented" role="radiogroup" aria-label="Severity">
                {["info", "warning", "critical"].map((s) => (
                  <button type="button" key={s} role="radio" aria-checked={(draft.severity || "critical") === s} className={(draft.severity || "critical") === s ? "on" : ""} onClick={() => set({ severity: s })}>
                    {titleCase(s)}
                  </button>
                ))}
              </div>
            </div>
          </div>
          <small className="dim">
            Saved as the rule “Uptime: … is down” in Alerts, where you can add a runbook, reminders or escalation.
          </small>
        </div>
      )}
    </fieldset>
  );
}
