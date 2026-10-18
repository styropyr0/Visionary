import { useState } from "react";
import { Hash, KeyRound, Mail, MessageSquare, Pencil, Plus, Send, Trash2, Webhook } from "lucide-react";
import { Modal } from "../Modal";
import { useToast } from "../Toast";
import { alertsApi, type AlertsMeta, type Channel, type ChannelInput, type ChannelKind } from "../../lib/alerts";

export const KIND_LABEL: Record<ChannelKind, string> = { slack: "Slack", discord: "Discord", webhook: "Webhook", email: "Email" };
export const KIND_ICON: Record<ChannelKind, typeof Hash> = { slack: Hash, discord: MessageSquare, webhook: Webhook, email: Mail };

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
    if (!window.confirm(`Delete the channel "${c.name}"? Rules that use it stop sending there.`)) return;
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
        <span className="al-hint">Webhook URLs and secrets are stored on the server and only shown masked.</span>
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
          <p>Send alerts to a Slack or Discord incoming webhook, any HTTP endpoint (JSON, optionally signed with HMAC-SHA256), or email over SMTP.</p>
        </div>
      ) : (
        <div className="al-cards">
          {channels.map((c) => {
            const Icon = KIND_ICON[c.kind];
            return (
              <section key={c.id} className="panel al-card">
                <div className="al-card-head">
                  <span className="al-kind">
                    <Icon size={15} aria-hidden />
                  </span>
                  <div className="al-card-title">
                    <h3>{c.name}</h3>
                    <span className="al-hint">{KIND_LABEL[c.kind]}</span>
                  </div>
                </div>
                <dl className="kv al-kv">
                  <dt>{c.kind === "email" ? "To" : "URL"}</dt>
                  <dd className="mono">{c.kind === "email" ? (c.to ?? []).join(", ") : c.url_masked}</dd>
                  {c.kind === "webhook" && (
                    <>
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
                  )}
                  <dt>Weekly digest</dt>
                  <dd>{c.digest_enabled ? `On, ${["Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays"][c.digest_weekday]} at ${String(c.digest_hour).padStart(2, "0")}:00` : "Off"}</dd>
                </dl>
                {c.kind === "email" && !meta.smtp_configured && <p className="form-error">Set VISIONARY_SMTP_HOST and VISIONARY_SMTP_FROM on the server to send email.</p>}
                <div className="al-card-actions">
                  <button type="button" className="btn sm" onClick={() => test(c)} disabled={busy === c.id || !meta.can_edit}>
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

export function ChannelModal({ channel, onClose, onSaved }: { channel: Channel | null; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [form, setForm] = useState<ChannelInput>(() => ({
    name: channel?.name ?? "",
    kind: channel?.kind ?? "slack",
    url: "",
    secret: "",
    to: channel?.to ?? [],
    digest_enabled: channel?.digest_enabled ?? false,
    digest_weekday: channel?.digest_weekday ?? 1,
    digest_hour: channel?.digest_hour ?? 9,
  }));
  const [toText, setToText] = useState((channel?.to ?? []).join(", "));
  const [clearSecret, setClearSecret] = useState(false);
  const [err, setErr] = useState("");
  const [saving, setSaving] = useState(false);
  const set = (p: Partial<ChannelInput>) => setForm((f) => ({ ...f, ...p }));

  const save = async () => {
    setSaving(true);
    setErr("");
    const body: ChannelInput = { ...form, to: toText.split(/[,\s]+/).filter(Boolean), clear_secret: clearSecret };
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

  const placeholder: Record<ChannelKind, string> = {
    slack: "https://hooks.slack.com/services/…",
    discord: "https://discord.com/api/webhooks/…",
    webhook: "https://example.com/hooks/visionary",
    email: "",
  };

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
            <div className="segmented" role="radiogroup" aria-label="Kind">
              {(Object.keys(KIND_LABEL) as ChannelKind[]).map((k) => (
                <button type="button" key={k} role="radio" aria-checked={form.kind === k} className={form.kind === k ? "on" : ""} onClick={() => set({ kind: k })}>
                  {KIND_LABEL[k]}
                </button>
              ))}
            </div>
          </div>
        )}
        <label className="field">
          <span>Name</span>
          <input className="input" value={form.name} maxLength={120} placeholder="e.g. #ops-alerts" onChange={(e) => set({ name: e.target.value })} />
        </label>
        {form.kind === "email" ? (
          <label className="field">
            <span>Recipients</span>
            <input className="input" value={toText} placeholder="ops@example.com, oncall@example.com" onChange={(e) => setToText(e.target.value)} />
          </label>
        ) : (
          <label className="field">
            <span>Webhook URL</span>
            <input className="input mono" value={form.url} placeholder={channel?.url_masked ? `${channel.url_masked} (leave empty to keep)` : placeholder[form.kind]} onChange={(e) => set({ url: e.target.value })} autoComplete="off" />
          </label>
        )}
        {form.kind === "webhook" && (
          <label className="field">
            <span>Signing secret (optional)</span>
            <input className="input mono" type="password" value={form.secret} placeholder={channel?.has_secret ? "•••••• (leave empty to keep)" : "Signs each body with HMAC-SHA256"} onChange={(e) => set({ secret: e.target.value })} autoComplete="new-password" />
            {channel?.has_secret && (
              <label className="al-check">
                <input type="checkbox" checked={clearSecret} onChange={(e) => setClearSecret(e.target.checked)} />
                <span>Remove the secret</span>
              </label>
            )}
            <p className="al-hint">
              The header <code>X-Visionary-Signature: sha256=&lt;hex&gt;</code> is the HMAC of the exact request body.
            </p>
          </label>
        )}
      </div>
    </Modal>
  );
}
