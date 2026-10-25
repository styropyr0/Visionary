import { useState } from "react";
import { BellRing, Hash, KeyRound, Mail, MessageCircle, MessageSquare, Pencil, Plus, Send, Siren, Smartphone, Trash2, Users, Webhook } from "lucide-react";
import { Modal } from "../Modal";
import { useToast } from "../Toast";
import { alertsApi, type AlertsMeta, type Channel, type ChannelInput, type ChannelKind } from "../../lib/alerts";

export const KIND_LABEL: Record<ChannelKind, string> = {
  slack: "Slack",
  discord: "Discord",
  teams: "Microsoft Teams",
  google_chat: "Google Chat",
  webhook: "Webhook",
  email: "Email",
  pagerduty: "PagerDuty",
  opsgenie: "Opsgenie",
  telegram: "Telegram",
  sms: "SMS (Twilio)",
};
export const KIND_ICON: Record<ChannelKind, typeof Hash> = {
  slack: Hash,
  discord: MessageSquare,
  teams: Users,
  google_chat: MessageCircle,
  webhook: Webhook,
  email: Mail,
  pagerduty: Siren,
  opsgenie: BellRing,
  telegram: Send,
  sms: Smartphone,
};
const KIND_HINT: Record<ChannelKind, string> = {
  slack: "Incoming webhook",
  discord: "Channel webhook",
  teams: "Workflows or incoming webhook",
  google_chat: "Space webhook",
  webhook: "JSON to any URL",
  email: "Over your SMTP server",
  pagerduty: "Opens and resolves incidents",
  opsgenie: "Opens and closes alerts",
  telegram: "Bot message to a chat",
  sms: "Text messages",
};
const KINDS = Object.keys(KIND_LABEL) as ChannelKind[];
const URL_KINDS: ChannelKind[] = ["slack", "discord", "teams", "google_chat", "webhook"];
/** Paging and SMS are for alerts only; the weekly digest goes elsewhere. */
export const NO_DIGEST_KINDS: ChannelKind[] = ["pagerduty", "opsgenie", "sms"];

export function ChannelsTab({ meta, channels, onChange }: { meta: AlertsMeta; channels: Channel[]; onChange: () => void }) {
  const toast = useToast();
  const [editing, setEditing] = useState<Channel | "new" | null>(null);
  const [busy, setBusy] = useState<number | null>(null);

  const test = async (c: Channel) => {
    setBusy(c.id);
    try {
      const r = await alertsApi.testChannel(c.id);
      toast(r.ok ? "ok" : "err", r.ok ? `Test sent to ${c.name}` : `${c.name}: ${r.error}`);
    } catch (e) {
      toast("err", (e as Error).message);
    } finally {
      setBusy(null);
    }
  };
  const remove = async (c: Channel) => {
    if (!window.confirm(`Delete the channel "${c.name}"? Rules that use it stop sending there, and it is removed from their settings.`)) return;
    try {
      await alertsApi.deleteChannel(c.id);
      onChange();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };

  return (
    <>
      <div className="al-toolbar">
        <span className="al-hint">Webhook URLs, keys and tokens are stored on the server and only shown masked.</span>
        <button type="button" className="btn primary" onClick={() => setEditing("new")} disabled={!meta.can_edit}>
          <Plus size={15} />
          Add channel
        </button>
      </div>
      {channels.length === 0 ? (
        <div className="empty panel">
          <div className="empty-icon">
            <Send size={20} />
          </div>
          <h3>No notification channels</h3>
          <p>
            Send alerts to Slack, Discord, Microsoft Teams or Google Chat, page someone through PagerDuty or Opsgenie, message a Telegram chat, text phones through Twilio, email over SMTP, or post JSON to any URL
            (optionally signed with HMAC-SHA256). Failed sends are retried for about 20 minutes.
          </p>
        </div>
      ) : (
        <div className="al-cards">
          {channels.map((c) => {
            const Icon = KIND_ICON[c.kind] ?? Webhook;
            return (
              <section key={c.id} className="panel al-card">
                <div className="al-card-head">
                  <span className="al-kind">
                    <Icon size={15} aria-hidden />
                  </span>
                  <div className="al-card-title">
                    <h3>{c.name}</h3>
                    <span className="al-hint">{KIND_LABEL[c.kind] ?? c.kind}</span>
                  </div>
                </div>
                <dl className="kv al-kv">
                  <ChannelDetails c={c} />
                  {!NO_DIGEST_KINDS.includes(c.kind) && (
                    <>
                      <dt>Weekly digest</dt>
                      <dd>{c.digest_enabled ? `On, ${["Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays"][c.digest_weekday]} at ${String(c.digest_hour).padStart(2, "0")}:00` : "Off"}</dd>
                    </>
                  )}
                </dl>
                {c.kind === "email" && !meta.smtp_configured && <p className="form-error">Add an SMTP server in Settings → Server → Email to send email.</p>}
                <div className="al-card-actions">
                  <button type="button" className="btn sm" onClick={() => test(c)} disabled={busy === c.id || !meta.can_edit} title={c.kind === "pagerduty" || c.kind === "opsgenie" ? "Opens a test incident and closes it again" : undefined}>
                    <Send size={13} />
                    {busy === c.id ? "Sending…" : "Send test"}
                  </button>
                  <button type="button" className="btn ghost sm" onClick={() => setEditing(c)} disabled={!meta.can_edit}>
                    <Pencil size={13} />
                    Edit
                  </button>
                  <button type="button" className="btn ghost sm" onClick={() => remove(c)} disabled={!meta.can_edit}>
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
        <ChannelModal
          channel={editing === "new" ? null : editing}
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

function ChannelDetails({ c }: { c: Channel }) {
  const key = (label: string) => (
    <>
      <dt>{label}</dt>
      <dd className="mono">{c.secret_hint ?? (c.has_secret ? "Set" : "Missing")}</dd>
    </>
  );
  switch (c.kind) {
    case "email":
      return (
        <>
          <dt>To</dt>
          <dd className="mono">{(c.to ?? []).join(", ")}</dd>
        </>
      );
    case "sms":
      return (
        <>
          <dt>From</dt>
          <dd className="mono">{c.from}</dd>
          <dt>To</dt>
          <dd className="mono">{(c.to ?? []).join(", ")}</dd>
          <dt>Account</dt>
          <dd className="mono">{c.account_sid}</dd>
        </>
      );
    case "pagerduty":
      return key("Routing key");
    case "opsgenie":
      return (
        <>
          {key("API key")}
          <dt>Region</dt>
          <dd>{(c.region || "us").toUpperCase()}</dd>
        </>
      );
    case "telegram":
      return (
        <>
          <dt>Chat</dt>
          <dd className="mono">{c.chat_id}</dd>
          {key("Bot token")}
        </>
      );
    case "webhook":
      return (
        <>
          <dt>URL</dt>
          <dd className="mono">{c.url_masked}</dd>
          <dt>Signature</dt>
          <dd>
            {c.has_secret ? (
              <span className="al-inline">
                <KeyRound size={12} /> HMAC-SHA256 in X-Visionary-Signature
              </span>
            ) : (
              "Unsigned"
            )}
          </dd>
        </>
      );
  }
  return (
    <>
      <dt>URL</dt>
      <dd className="mono">{c.url_masked}</dd>
    </>
  );
}

const URL_PLACEHOLDER: Partial<Record<ChannelKind, string>> = {
  slack: "https://hooks.slack.com/services/…",
  discord: "https://discord.com/api/webhooks/…",
  teams: "https://….webhook.office.com/… or a Workflows URL",
  google_chat: "https://chat.googleapis.com/v1/spaces/…/messages?key=…",
  webhook: "https://example.com/hooks/visionary",
};

export function ChannelModal({ channel, onClose, onSaved }: { channel: Channel | null; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [form, setForm] = useState<ChannelInput>(() => ({
    name: channel?.name ?? "",
    kind: channel?.kind ?? "slack",
    url: "",
    secret: "",
    to: channel?.to ?? [],
    chat_id: channel?.chat_id ?? "",
    account_sid: channel?.account_sid ?? "",
    from: channel?.from ?? "",
    region: channel?.region || "us",
    digest_enabled: channel?.digest_enabled ?? false,
    digest_weekday: channel?.digest_weekday ?? 1,
    digest_hour: channel?.digest_hour ?? 9,
  }));
  const [toText, setToText] = useState((channel?.to ?? []).join(", "));
  const [clearSecret, setClearSecret] = useState(false);
  const [err, setErr] = useState("");
  const [saving, setSaving] = useState(false);
  const set = (p: Partial<ChannelInput>) => setForm((f) => ({ ...f, ...p }));
  const kind = form.kind;

  const save = async () => {
    setSaving(true);
    setErr("");
    const body: ChannelInput = { ...form, to: toText.split(/[,\s]+/).filter(Boolean), clear_secret: clearSecret };
    if (NO_DIGEST_KINDS.includes(kind)) body.digest_enabled = false;
    try {
      if (channel) await alertsApi.updateChannel(channel.id, body);
      else await alertsApi.createChannel(body);
      toast("ok", "Channel saved");
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  /** A credential field: empty keeps the stored value. */
  const secretField = (label: string, placeholder: string, hint?: string) => (
    <label className="field">
      <span>{label}</span>
      <input
        className="input mono"
        type="password"
        value={form.secret}
        placeholder={channel?.has_secret ? `${channel.secret_hint ?? "••••••"} (leave empty to keep)` : placeholder}
        onChange={(e) => set({ secret: e.target.value })}
        autoComplete="new-password"
      />
      {hint && <p className="al-hint">{hint}</p>}
    </label>
  );

  return (
    <Modal
      title={channel ? "Edit channel" : "Add channel"}
      onClose={onClose}
      narrow
      footer={
        <>
          {err && <span className="form-error" style={{ marginRight: "auto" }}>{err}</span>}
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={save} disabled={saving}>
            {saving ? "Saving…" : "Save channel"}
          </button>
        </>
      }
    >
      <div className="al-form">
        {!channel && (
          <div className="field">
            <span>Kind</span>
            <div className="al-kind-grid" role="radiogroup" aria-label="Kind">
              {KINDS.map((k) => {
                const Icon = KIND_ICON[k];
                return (
                  <button type="button" key={k} role="radio" aria-checked={kind === k} className={`al-kind-opt ${kind === k ? "on" : ""}`} onClick={() => set({ kind: k, secret: "" })}>
                    <Icon size={15} aria-hidden />
                    <span>
                      <strong>{KIND_LABEL[k]}</strong>
                      <small>{KIND_HINT[k]}</small>
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
        )}
        <label className="field">
          <span>Name</span>
          <input className="input" value={form.name} maxLength={120} placeholder={kind === "pagerduty" ? "e.g. Payments on-call" : "e.g. #ops-alerts"} onChange={(e) => set({ name: e.target.value })} />
        </label>

        {URL_KINDS.includes(kind) && (
          <label className="field">
            <span>Webhook URL</span>
            <input className="input mono" value={form.url} placeholder={channel?.url_masked ? `${channel.url_masked} (leave empty to keep)` : URL_PLACEHOLDER[kind]} onChange={(e) => set({ url: e.target.value })} autoComplete="off" />
            {kind === "teams" && <p className="al-hint">In Teams, add the “Post to a channel when a webhook request is received” workflow to the channel and paste its URL. Older incoming-webhook URLs work too.</p>}
          </label>
        )}

        {kind === "webhook" && (
          <>
            {secretField("Signing secret (optional)", "Signs each body with HMAC-SHA256")}
            {channel?.has_secret && (
              <label className="al-check">
                <input type="checkbox" checked={clearSecret} onChange={(e) => setClearSecret(e.target.checked)} />
                <span>Remove the secret</span>
              </label>
            )}
            <p className="al-hint" style={{ marginTop: -8 }}>
              The header <code>X-Visionary-Signature: sha256=&lt;hex&gt;</code> is the HMAC of the exact request body. Bodies carry <code>event</code> (alert.firing, alert.resolved, alert.reminder,
              alert.escalation, alert.ack), the rule, the reading and a <code>key</code> that stays the same for one series.
            </p>
          </>
        )}

        {kind === "email" && (
          <label className="field">
            <span>Recipients</span>
            <input className="input" value={toText} placeholder="ops@example.com, oncall@example.com" onChange={(e) => setToText(e.target.value)} />
          </label>
        )}

        {kind === "pagerduty" &&
          secretField(
            "Integration key",
            "32 characters, from the service's Events API v2 integration",
            "In PagerDuty: Service → Integrations → Add an integration → Events API v2. Each rule series is one incident: firing triggers it, acknowledging in Visionary acknowledges it, resolving resolves it.",
          )}

        {kind === "opsgenie" && (
          <>
            {secretField("API key", "From an API integration in Opsgenie", "Settings → Integrations → API. Each rule series is one alert (its alias): firing opens it, resolving closes it.")}
            <div className="field">
              <span>Region</span>
              <div className="segmented" role="radiogroup" aria-label="Region">
                {["us", "eu"].map((r) => (
                  <button type="button" key={r} role="radio" aria-checked={form.region === r} className={form.region === r ? "on" : ""} onClick={() => set({ region: r })}>
                    {r.toUpperCase()}
                  </button>
                ))}
              </div>
            </div>
          </>
        )}

        {kind === "telegram" && (
          <>
            {secretField("Bot token", "123456789:AA…", "Create a bot with @BotFather and add it to the chat or channel.")}
            <label className="field">
              <span>Chat ID</span>
              <input className="input mono" value={form.chat_id} placeholder="-1001234567890 or @channelname" onChange={(e) => set({ chat_id: e.target.value })} />
              <p className="al-hint">A group's ID is negative. Send the bot a message and open api.telegram.org/bot&lt;token&gt;/getUpdates to find it.</p>
            </label>
          </>
        )}

        {kind === "sms" && (
          <>
            <label className="field">
              <span>Twilio account SID</span>
              <input className="input mono" value={form.account_sid} placeholder="AC…" onChange={(e) => set({ account_sid: e.target.value })} autoComplete="off" />
            </label>
            {secretField("Auth token", "From the Twilio console")}
            <div className="al-row">
              <label className="field grow">
                <span>From</span>
                <input className="input mono" value={form.from} placeholder="+14155550100" onChange={(e) => set({ from: e.target.value })} />
              </label>
              <label className="field grow">
                <span>To</span>
                <input className="input mono" value={toText} placeholder="+919876543210, +14155550199" onChange={(e) => setToText(e.target.value)} />
              </label>
            </div>
            <p className="al-hint" style={{ marginTop: -8 }}>
              Numbers in international form. Each text is the title and first line, up to 300 characters; Twilio charges per message.
            </p>
          </>
        )}
      </div>
    </Modal>
  );
}
