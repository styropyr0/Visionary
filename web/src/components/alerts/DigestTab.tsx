import { useState } from "react";
import { Send } from "lucide-react";
import { Select } from "../Select";
import { useToast } from "../Toast";
import { useQuery } from "../../lib/useQuery";
import { alertsApi, WEEKDAYS, type AlertsMeta, type Channel, type DigestWeek } from "../../lib/alerts";
import { formatCount, formatMs, formatPercent } from "../../lib/format";
import { formatDateTime, formatLogTime } from "../../lib/time";
import { KIND_ICON, KIND_LABEL } from "./ChannelsTab";

export function DigestTab({ meta, channels, onChange }: { meta: AlertsMeta; channels: Channel[]; onChange: () => void }) {
  const digest = useQuery("digest", (signal) => alertsApi.digest(signal));
  const d = digest.data?.data;
  return (
    <div className="al-digest">
      <section className="panel al-digest-schedule">
        <header className="panel-head">
          <h3 className="panel-title">Schedule</h3>
        </header>
        <div className="al-digest-body">
          <p className="al-hint">
            Off by default. Each channel gets the digest on its own weekday and hour, in {meta.digest_tz} (set <code>VISIONARY_DIGEST_TZ</code> to change).{" "}
            {meta.llm ? "The analyst writes the summary." : "Without a model configured, it is a plain summary of the numbers."}
          </p>
          {channels.length === 0 ? (
            <p className="al-hint">Add a channel in the Channels tab first.</p>
          ) : (
            <ul className="al-digest-list">
              {channels.map((c) => (
                <DigestRow key={c.id} channel={c} canEdit={meta.can_edit} onChange={onChange} />
              ))}
            </ul>
          )}
        </div>
      </section>

      <section className="panel al-digest-preview" aria-busy={digest.loading || undefined}>
        {digest.loading && <div className="progress" aria-hidden />}
        <header className="panel-head">
          <h3 className="panel-title">This week's digest</h3>
          {d && <span className="al-hint al-head-note">{`${formatDateTime(new Date(d.from))} – ${formatDateTime(new Date(d.to))}`}</span>}
        </header>
        <div className="al-digest-body">
          {digest.error && <div className="notice">{digest.error.message}</div>}
          {!d && !digest.error && <div className="panel-state" style={{ height: 200 }} />}
          {d && (
            <>
              <div className="al-week-grid">
                <WeekStat label="Requests" cur={d.this_week.requests} prev={d.previous_week.requests} fmt={(v) => formatCount(v)} upIsBad={false} />
                <WeekStat label="p95 latency" cur={d.this_week.p95_ms} prev={d.previous_week.p95_ms} fmt={formatMs} upIsBad />
                <WeekStat label="5xx rate" cur={d.this_week.error_rate} prev={d.previous_week.error_rate} fmt={formatPercent} upIsBad />
                <WeekStat label="Alerts fired" cur={d.this_week.incidents} prev={d.previous_week.incidents} fmt={(v) => formatCount(v)} upIsBad />
              </div>
              <pre className="al-digest-text">{digest.data!.lines.join("\n")}</pre>
            </>
          )}
        </div>
      </section>
    </div>
  );
}

function WeekStat({ label, cur, prev, fmt, upIsBad }: { label: string; cur: DigestWeek[keyof DigestWeek]; prev: DigestWeek[keyof DigestWeek]; fmt: (v: number) => string; upIsBad: boolean }) {
  const delta = cur !== null && prev ? (cur - prev) / prev : null;
  const vs = prev === null ? "No previous week" : delta === null ? `was ${fmt(prev)}` : `${delta > 0 ? "+" : ""}${(delta * 100).toFixed(0)}% vs ${fmt(prev)}`;
  const tone = delta === null || Math.abs(delta) < 0.005 ? "" : (delta > 0) === upIsBad ? "bad" : "good";
  return (
    <div className="al-week">
      <span className="stat-label">{label}</span>
      <span className="al-week-val mono">{cur === null ? "—" : fmt(cur)}</span>
      <span className={`al-week-delta ${tone}`}>
        {vs}
      </span>
    </div>
  );
}

function DigestRow({ channel, canEdit, onChange }: { channel: Channel; canEdit: boolean; onChange: () => void }) {
  const toast = useToast();
  const [sending, setSending] = useState(false);
  const Icon = KIND_ICON[channel.kind];
  const update = async (p: Partial<Channel>) => {
    const next = { ...channel, ...p };
    try {
      await alertsApi.updateChannel(channel.id, {
        name: next.name,
        kind: next.kind,
        to: next.to,
        digest_enabled: next.digest_enabled,
        digest_weekday: next.digest_weekday,
        digest_hour: next.digest_hour,
      });
      onChange();
    } catch (e) {
      toast("err", (e as Error).message);
    }
  };
  const sendNow = async () => {
    setSending(true);
    try {
      const r = await alertsApi.sendDigest(channel.id);
      toast(r.ok ? "ok" : "err", r.ok ? `Digest sent to ${channel.name}` : `${channel.name}: ${r.error}`);
      onChange();
    } catch (e) {
      toast("err", (e as Error).message);
    } finally {
      setSending(false);
    }
  };
  return (
    <li className="al-digest-row">
      <label className="al-check al-digest-name">
        <input type="checkbox" checked={channel.digest_enabled} disabled={!canEdit} onChange={(e) => update({ digest_enabled: e.target.checked })} />
        <Icon size={14} aria-hidden />
        <span>{channel.name}</span>
        <span className="al-hint">{KIND_LABEL[channel.kind]}</span>
      </label>
      <div className="al-digest-when">
        <Select label="Weekday" align="left" value={String(channel.digest_weekday)} options={WEEKDAYS.map((w, i) => ({ value: String(i), label: w }))} onChange={(v) => update({ digest_weekday: Number(v) })} />
        <Select
          label="Hour"
          align="left"
          value={String(channel.digest_hour)}
          options={Array.from({ length: 24 }, (_, h) => ({ value: String(h), label: `${String(h).padStart(2, "0")}:00` }))}
          onChange={(v) => update({ digest_hour: Number(v) })}
        />
        <button className="btn sm" onClick={sendNow} disabled={sending || !canEdit}>
          <Send size={13} />
          {sending ? "Sending…" : "Send digest now"}
        </button>
      </div>
      <span className="al-hint al-digest-last">{channel.digest_sent_at ? `Last sent ${formatLogTime(channel.digest_sent_at)}` : "Never sent"}</span>
    </li>
  );
}
