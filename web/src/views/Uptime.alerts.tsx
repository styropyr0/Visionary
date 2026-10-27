import { useState } from "react";
import { AlertTriangle, BellOff, BellRing } from "lucide-react";
import { Modal } from "../components/Modal";
import { useToast } from "../components/Toast";
import { KIND_LABEL } from "../components/alerts/ChannelsTab";
import { alertsApi, type Channel } from "../lib/alerts";
import { titleCase } from "../lib/format";
import { save } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { alertGap, setCheckAlert, type CheckAlert, type CheckAlertInput, type CheckList, type UptimeCheck } from "../lib/uptime";
// The alert form pieces come from the Alerts page.
import "./Alerts.css";

// How uptime checks are alerted on: a line per check in the list, a banner
// when some checks would go down unheard, and the form's alert section.

/** Under a check's name: who hears when it goes down, or that nobody does. */
export function CheckAlertLine({ alert, onFix, canEdit }: { alert: CheckAlert | undefined; onFix: () => void; canEdit: boolean }) {
  // Switched off on purpose: worth seeing, not a warning.
  if (alert?.mode === "off") {
    return (
      <span className="up-alert-line muted" title="Alerts are off for this check. Results and incidents are still recorded.">
        <BellOff size={12} aria-hidden />
        <span className="up-alert-text">Alerts off</span>
      </span>
    );
  }
  const gap = alertGap(alert);
  if (gap) {
    return (
      <span className="up-alert-line gap" title={gap}>
        <AlertTriangle size={12} aria-hidden />
        <span className="up-alert-text">{alert && alert.mode !== "none" ? "Nobody is notified" : "No alert"}</span>
        {canEdit && (
          <button
            type="button"
            className="up-alert-fix"
            onClick={(e) => {
              e.stopPropagation();
              onFix();
            }}
          >
            Set up
          </button>
        )}
      </span>
    );
  }
  return (
    <span className="up-alert-line" title={`${alert!.rule_name}: after ${alert!.failures} failed check${alert!.failures === 1 ? "" : "s"} in a row, to ${alert!.channels.join(", ")}`}>
      <BellRing size={12} aria-hidden />
      <span className="up-alert-text">{alert!.channels.join(", ")}</span>
    </span>
  );
}

/** Checks that would go down unheard; ones switched off on purpose are left out. */
export function unalerted<T extends { check: UptimeCheck; alert: CheckAlert }>(checks: T[]): T[] {
  return checks.filter((c) => c.check.enabled && c.alert.mode !== "off" && alertGap(c.alert));
}

/**
 * Says how many checks nobody would hear about, and fixes the common case:
 * the rule for every check, sent to the channels picked here.
 */
export function CoverageBanner({ data, onDone }: { data: CheckList; onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const gaps = unalerted(data.checks);
  if (gaps.length === 0) return null;
  const all = gaps.length === data.checks.filter((c) => c.check.enabled).length;
  const named = gaps.slice(0, 3).map((c) => c.check.name);
  return (
    <div className="notice up-coverage span-12" role="status">
      <AlertTriangle size={16} aria-hidden />
      <div className="up-coverage-text">
        <strong>
          {all ? (gaps.length === 1 ? "This check alerts nobody" : "None of these checks alert anyone") : `${gaps.length} check${gaps.length === 1 ? "" : "s"} alert nobody`} when{" "}
          {gaps.length === 1 ? "it goes" : "they go"} down
        </strong>
        <span className="dim">
          {!all && `${named.join(", ")}${gaps.length > 3 ? ` and ${gaps.length - 3} more` : ""}. `}
          {data.can_edit ? "Pick who to notify and they join the rule for every check." : "Ask an editor to set up an alert."}
        </span>
      </div>
      {data.can_edit && (
        <button type="button" className="btn sm primary" onClick={() => setOpen(true)}>
          <BellRing size={13} aria-hidden /> Set up alerts
        </button>
      )}
      {open && (
        <DefaultRuleModal
          checks={gaps}
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

/**
 * Puts checks nobody hears about under the rule for every check, with the
 * channels and failures picked here. The server creates the rule when
 * missing and turns it back on when it was switched off.
 */
export function DefaultRuleModal({ checks, onClose, onDone }: { checks: { check: UptimeCheck; alert: CheckAlert }[]; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const channels = useQuery("uptime-alert-channels", (signal) => alertsApi.channels(signal));
  // The rule's own settings, when one of the checks already uses it.
  const current = checks.find((c) => c.alert.mode === "default")?.alert;
  const [picked, setPicked] = useState<number[] | null>(null);
  const [failures, setFailures] = useState(current?.failures || 2);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const list = channels.data?.channels ?? [];
  const chosen = picked ?? (current?.channel_ids.length ? current.channel_ids : list.slice(0, 1).map((c) => c.id));
  const noChannels = !!channels.data && list.length === 0;

  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      for (const c of checks) await setCheckAlert(c.check.id, { mode: "default", channels: chosen, failures });
      const names = list.filter((c) => chosen.includes(c.id)).map((c) => c.name);
      toast("ok", `${checks.length === 1 ? `“${checks[0].check.name}” now alerts` : `${checks.length} checks now alert`} ${names.join(", ")} when down`);
      onDone();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={checks.length === 1 ? `Alert when “${checks[0].check.name}” goes down` : "Alert when a check goes down"}
      narrow
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          {!noChannels && (
            <button type="button" className="btn primary" onClick={submit} disabled={busy || chosen.length === 0}>
              {busy ? "Saving…" : "Turn on alerts"}
            </button>
          )}
        </>
      }
    >
      <div className="al-form">
        {noChannels ? (
          <div className="up-no-channels">
            <BellOff size={18} aria-hidden />
            <div>
              <strong>No notification channels yet</strong>
              <p className="dim">An alert needs somewhere to go, such as email, Slack or a webhook. Add a channel, then come back here.</p>
              <a className="btn sm primary" href="#/alerts" onClick={() => save("va.alerts.tab", "channels")}>
                Add a channel
              </a>
            </div>
          </div>
        ) : (
          <>
            <p className="al-hint" style={{ margin: 0 }}>
              {checks.length === 1 ? "It joins the rule for every check" : `${checks.length} checks join the rule for every check`}
              {current?.rule_name ? ` (“${current.rule_name}”)` : ""}, which is turned on if it was off. A check with its own alert settings (in its form) stays out of it, so
              nothing is alerted twice.
            </p>
            <ChannelPicker channels={list} loading={!channels.data} value={chosen} onChange={setPicked} />
            <FailuresField value={failures} onChange={setFailures} />
          </>
        )}
        {err && <div className="form-error">{err}</div>}
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
          No notification channels yet.{" "}
          <a href="#/alerts" onClick={() => save("va.alerts.tab", "channels")}>
            Add one in Alerts → Channels
          </a>{" "}
          first, or nobody hears about it.
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
                  : list.length
                    ? `“${def.rule_name}” has no channels; saving sends it to ${list[0].name}`
                    : `“${def.rule_name}” has no channels yet; add one in Alerts → Channels`
                : list.length
                  ? `Turned on, or created, when you save, sending to ${list[0].name}`
                  : "Turned on, or created, when you save; add a channel so someone hears"}
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
