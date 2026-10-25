import { useState, type ReactNode } from "react";
import { ArrowDownRight, ArrowUpRight, CheckCircle2, Mail, Send, Sparkles } from "lucide-react";
import { Select } from "../Select";
import { useToast } from "../Toast";
import { useQuery } from "../../lib/useQuery";
import { alertsApi, WEEKDAYS, type AlertsMeta, type Channel, type DigestChange, type DigestPreview } from "../../lib/alerts";
import type { Result } from "../../lib/api";
import { formatCount, formatMs, formatPercent, titleCase } from "../../lib/format";
import { formatDateTime, formatLogTime, dateFormat } from "../../lib/time";
import { KIND_ICON, KIND_LABEL, NO_DIGEST_KINDS } from "./ChannelsTab";
import { kindLabel } from "../../lib/security";
import { DataTable } from "../DataTable";
import { ErrorNotice } from "../ErrorNotice";
import { Modal } from "../Modal";
import { StatTile } from "../StatTile";

type Data = DigestPreview["data"];

/**
 * The weekly digest as a report: what it will say this week, section by
 * section, and which channels get it when.
 */
export function DigestTab({ meta, channels, onChange }: { meta: AlertsMeta; channels: Channel[]; onChange: () => void }) {
  const digest = useQuery("digest", (signal) => alertsApi.digest(signal));
  const d = digest.data?.data;
  return (
    <div className="grid al-digest-report">
      {digest.error && (
        <div className="span-12">
          <ErrorNotice title="Could not load the digest" error={digest.error} />
        </div>
      )}
      <ReportHead data={d} loading={digest.loading} llm={meta.llm} />
      {d && <Tiles d={d} />}
      {d && <Sections d={d} />}
      <Delivery meta={meta} channels={channels} onChange={onChange} />
    </div>
  );
}

function ReportHead({ data, loading, llm }: { data: Data | undefined; loading: boolean; llm: boolean }) {
  const toast = useToast();
  const [summary, setSummary] = useState("");
  const [writing, setWriting] = useState(false);
  const [email, setEmail] = useState(false);
  const write = async () => {
    setWriting(true);
    try {
      setSummary((await alertsApi.digestSummary()).summary);
    } catch (e) {
      toast("err", (e as Error).message);
    } finally {
      setWriting(false);
    }
  };
  const lines = summary.split("\n").map((l) => l.trim().replace(/^- /, "")).filter(Boolean);
  return (
    <section className="panel span-12 dg-head" aria-busy={loading || undefined}>
      {loading && <div className="progress" aria-hidden />}
      <div className="dg-head-top">
        <div>
          <div className="dg-eyebrow">This week's digest</div>
          <h2 className="dg-period">{data ? `${formatDateTime(new Date(data.from))} – ${formatDateTime(new Date(data.to))}` : "Loading…"}</h2>
          <div className="dg-sub">Compared with the 7 days before. This is what each channel below receives on its day.</div>
        </div>
        <div className="dg-actions">
          <button type="button" className="btn" onClick={() => setEmail(true)} disabled={!data}>
            <Mail size={14} />
            Preview email
          </button>
          {llm && (
            <button type="button" className="btn primary" onClick={write} disabled={writing || !data}>
              <Sparkles size={14} />
              {writing ? "Writing…" : summary ? "Write again" : "Write summary"}
            </button>
          )}
        </div>
      </div>
      {lines.length > 0 ? (
        <div className="dg-summary">
          <Sparkles size={15} aria-hidden />
          <div>
            <p className="dg-summary-lead">{lines[0]}</p>
            {lines.length > 1 && (
              <ul>
                {lines.slice(1).map((l, i) => (
                  <li key={i}>{l}</li>
                ))}
              </ul>
            )}
          </div>
        </div>
      ) : (
        <p className="dg-summary-hint">
          {llm
            ? "The analyst writes a short summary into every digest it sends. Write one now to see what it would say this week."
            : "With a model configured, the analyst adds a short written summary to every digest."}
        </p>
      )}
      {email && (
        <Modal title="Digest email" onClose={() => setEmail(false)}>
          <p className="al-hint" style={{ marginTop: 0 }}>
            Email channels get this version (the analyst's summary goes above the numbers); Slack, Discord and webhooks get the same numbers as text.
          </p>
          <iframe className="dg-email" title="Digest email preview" src={alertsApi.digestEmailURL} sandbox="" />
        </Modal>
      )}
    </section>
  );
}

const pctChange = (cur: number | null, prev: number | null) => (cur !== null && prev ? (cur - prev) / prev : null);

function Tiles({ d }: { d: Data }) {
  const days = d.days ?? [];
  const up = d.uptime ?? [];
  const minUp = up.length ? Math.min(...up.map((c) => c.availability)) : null;
  const avgUp = up.length ? up.reduce((a, c) => a + c.availability, 0) / up.length : null;
  const errors = d.errors;
  return (
    <>
      <StatTile
        label="Requests"
        value={formatCount(d.this_week.requests)}
        delta={pctChange(d.this_week.requests, d.previous_week.requests)}
        deltaLabel="vs week before"
        upIs="neutral"
        spark={days.map((x) => x.requests)}
      />
      <StatTile label="p95 latency" value={d.this_week.p95_ms === null ? "—" : formatMs(d.this_week.p95_ms)} delta={pctChange(d.this_week.p95_ms, d.previous_week.p95_ms)} deltaLabel="vs week before" upIs="bad" />
      <StatTile
        label="5xx rate"
        value={d.this_week.error_rate === null ? "—" : formatPercent(d.this_week.error_rate)}
        delta={pctChange(d.this_week.error_rate, d.previous_week.error_rate)}
        deltaLabel="vs week before"
        upIs="bad"
        spark={days.map((x) => x.errors_5xx)}
      />
      <StatTile label="Alerts fired" value={formatCount(d.this_week.incidents)} delta={pctChange(d.this_week.incidents, d.previous_week.incidents)} deltaLabel="vs week before" upIs="bad" />
      <StatTile
        label="Errors"
        description="Exceptions and panics the SDKs captured."
        value={errors ? formatCount(errors.total) : "—"}
        delta={errors ? pctChange(errors.total, errors.previous_total) : null}
        deltaLabel="vs week before"
        upIs="bad"
        footer={errors ? (errors.new_issues ? `${errors.new_issues} new issue${errors.new_issues === 1 ? "" : "s"}` : "No new issues") : "None captured"}
      />
      <StatTile
        label="Uptime"
        description="Average availability across uptime checks; the footer names the lowest."
        value={avgUp === null ? "—" : `${avgUp.toFixed(2)}%`}
        delta={null}
        deltaLabel=""
        upIs="good"
        footer={minUp === null ? "No uptime checks" : `lowest ${minUp.toFixed(2)}%`}
      />
    </>
  );
}

/** A small table from plain rows. */
function table(columns: string[], rows: unknown[][]): Result {
  return { columns, rows, truncated: false, rows_read: 0, duration_ms: 0, sql: "", bucket: 0, from: "", to: "" };
}

function Block({ title, description, span = "span-6", children }: { title: string; description?: string; span?: string; children: ReactNode }) {
  return (
    <section className={`panel ${span} dg-block`}>
      <header className="panel-head">
        <h3 className="panel-title">{title}</h3>
      </header>
      {description && <p className="dg-block-note">{description}</p>}
      <div className="dg-block-body">{children}</div>
    </section>
  );
}

const mono = (v: unknown) => <span className="mono">{String(v)}</span>;
const ms = (v: unknown) => (v == null ? <span className="dim">—</span> : formatMs(Number(v)));
const pct = (v: unknown) => (v == null ? <span className="dim">—</span> : formatPercent(Number(v)));

function Change({ value }: { value: number }) {
  const Arrow = value > 0 ? ArrowUpRight : ArrowDownRight;
  return (
    <span className={`dg-change ${value > 0 ? "bad" : "good"}`}>
      <Arrow size={13} aria-hidden />
      {value > 0 ? "+" : ""}
      {value}%
    </span>
  );
}

function routeChangeTable(rows: DigestChange[]) {
  return (
    <DataTable
      result={table(["route", "previous_p95_ms", "p95_ms", "change_pct"], rows.map((r) => [r.route, r.previous_p95_ms, r.p95_ms, r.change_pct]))}
      sortable={false}
      columns={[
        { key: "route", label: "Route", render: mono },
        { key: "previous_p95_ms", label: "Was", align: "right", render: ms },
        { key: "p95_ms", label: "p95", align: "right", render: ms },
        { key: "change_pct", label: "Change", align: "right", render: (v) => <Change value={Number(v)} /> },
      ]}
    />
  );
}

function Calm({ children }: { children: ReactNode }) {
  return (
    <div className="dg-calm">
      <CheckCircle2 size={16} aria-hidden />
      {children}
    </div>
  );
}

const RATING: Record<string, string> = { good: "Good", needs_improvement: "Needs work", poor: "Poor" };

function Sections({ d }: { d: Data }) {
  const regressed = d.regressed_routes ?? [];
  const improved = d.improved_routes ?? [];
  const slowest = d.slowest_routes ?? [];
  const busiest = d.busiest_routes ?? [];
  const incidents = d.incidents ?? [];
  const releases = d.releases ?? [];
  const uptime = d.uptime ?? [];
  const vitals = d.web_vitals ?? [];
  const machines = d.machines ?? [];
  const ctr = d.containers;
  const sec = d.security;
  const secTotal = sec ? Object.values(sec.findings_by_severity ?? {}).reduce((a, n) => a + n, 0) : 0;
  const vital = (name: string, v: unknown) => (v == null ? <span className="dim">—</span> : name === "CLS" ? Number(v).toFixed(3) : formatMs(Number(v)));

  return (
    <>
      <Block title="Slower than last week" description="Routes whose p95 grew by 10% or more, with enough traffic both weeks.">
        {regressed.length ? routeChangeTable(regressed) : <Calm>No route got noticeably slower.</Calm>}
      </Block>
      <Block title="Faster than last week" description="Routes whose p95 dropped by 10% or more.">
        {improved.length ? routeChangeTable(improved) : <p className="dg-empty">No route got noticeably faster.</p>}
      </Block>

      {slowest.length > 0 && (
        <Block title="Slowest routes" description="Highest p95 this week.">
          <DataTable
            result={table(["route", "requests", "p95_ms"], slowest.map((r) => [r.route, r.requests, r.p95_ms]))}
            sortable={false}
            columns={[
              { key: "route", label: "Route", render: mono },
              { key: "requests", label: "Requests", align: "right", unit: "count" },
              { key: "p95_ms", label: "p95", align: "right", render: ms },
            ]}
          />
        </Block>
      )}
      {busiest.length > 0 && (
        <Block title="Busiest routes" description="Most requests this week.">
          <DataTable
            result={table(["route", "previous_requests", "requests"], busiest.map((r) => [r.route, r.previous_requests, r.requests]))}
            sortable={false}
            columns={[
              { key: "route", label: "Route", render: mono },
              { key: "previous_requests", label: "Week before", align: "right", unit: "count" },
              { key: "requests", label: "This week", align: "right", unit: "count" },
            ]}
          />
        </Block>
      )}

      <Block title="Incidents" description="Alert rules that fired, most often first.">
        {incidents.length ? (
          <DataTable
            result={table(["rule", "severity", "count"], incidents.map((i) => [i.rule, i.severity, i.count]))}
            sortable={false}
            columns={[
              { key: "rule", label: "Rule" },
              { key: "severity", render: (v) => <span className={`dg-sev ${String(v)}`}>{titleCase(String(v))}</span> },
              { key: "count", label: "Fired", align: "right", render: (v) => `${formatCount(Number(v))}×` },
            ]}
          />
        ) : (
          <Calm>No alerts fired this week.</Calm>
        )}
      </Block>
      <Block title="New versions" description="Releases first seen this week; changes around them may be related.">
        {releases.length ? (
          <ul className="dg-releases">
            {releases.map((r) => (
              <li key={`${r.service}-${r.version}`}>
                <span className="mono">{[r.service, r.version].filter(Boolean).join(" ")}</span>
                <span className="dim">{formatLogTime(r.first_seen).slice(0, 13)}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="dg-empty">No new versions this week.</p>
        )}
      </Block>

      {d.errors && (
        <Block
          title="Errors"
          span="span-12"
          description={`${formatCount(d.errors.total)} errors, was ${formatCount(d.errors.previous_total)}${d.errors.new_issues ? `; ${d.errors.new_issues} new issue${d.errors.new_issues === 1 ? "" : "s"}` : ""}. The most frequent issues:`}
        >
          <DataTable
            result={table(["type", "message", "route", "count"], (d.errors.top ?? []).map((i) => [i.new ? `${i.type} · New` : i.type, i.message, i.route, i.count]))}
            sortable={false}
            columns={[
              { key: "type", label: "Issue", render: (v) => (String(v).endsWith(" · New") ? <span className="dg-new">{String(v)}</span> : mono(v)) },
              { key: "message", render: (v) => <span className="dg-msg">{String(v)}</span> },
              { key: "route", render: (v) => (v ? mono(v) : <span className="dim">—</span>) },
              { key: "count", align: "right", unit: "count" },
            ]}
          />
        </Block>
      )}

      {uptime.length > 0 && (
        <Block title="Uptime" description="Availability per check, lowest first.">
          <DataTable
            result={table(["check", "failures", "avg_ms", "availability"], uptime.map((c) => [c.check, c.failures, c.avg_ms, c.availability]))}
            sortable={false}
            columns={[
              { key: "check", label: "Check" },
              { key: "failures", label: "Failed", align: "right", unit: "count" },
              { key: "avg_ms", label: "Avg", align: "right", render: ms },
              { key: "availability", label: "Up", align: "right", render: (v) => <span className={Number(v) < 99.9 ? "dg-bad" : "dg-good"}>{Number(v).toFixed(2)}%</span> },
            ]}
          />
        </Block>
      )}
      {vitals.length > 0 && (
        <Block title="Web Vitals" description="75th percentile of real page loads, rated by Google's thresholds.">
          <DataTable
            result={table(["name", "previous_p75", "p75", "rating"], vitals.map((v) => [v.name, v.previous_p75, v.p75, v.rating]))}
            sortable={false}
            columns={[
              { key: "name", label: "Metric", render: mono },
              { key: "previous_p75", label: "Was", align: "right", render: (v, row) => vital(String(row[0]), v) },
              { key: "p75", label: "Now", align: "right", render: (v, row) => vital(String(row[0]), v) },
              { key: "rating", align: "right", render: (v) => <span className={`dg-rating ${String(v)}`}>{RATING[String(v)] ?? String(v)}</span> },
            ]}
          />
        </Block>
      )}

      {machines.length > 0 && (
        <Block title="Machines" description="CPU and memory over the week, busiest first.">
          <DataTable
            result={table(["instance", "avg_cpu", "peak_cpu", "peak_memory"], machines.map((m) => [m.instance, m.avg_cpu, m.peak_cpu, m.peak_memory]))}
            sortable={false}
            columns={[
              { key: "instance", label: "Instance", render: mono },
              { key: "avg_cpu", label: "Avg CPU", align: "right", render: pct },
              { key: "peak_cpu", label: "Peak CPU", align: "right", render: (v) => <span className={Number(v) >= 90 ? "dg-bad" : ""}>{pct(v)}</span> },
              { key: "peak_memory", label: "Peak memory", align: "right", render: (v) => <span className={Number(v) >= 90 ? "dg-bad" : ""}>{pct(v)}</span> },
            ]}
          />
        </Block>
      )}
      {ctr && (
        <Block title="Containers" description={`${ctr.restarts} restart(s), ${ctr.oom_kills} out-of-memory kill(s), ${ctr.exits} exit(s), ${ctr.unhealthy} failed health check(s).`}>
          {(ctr.worst ?? []).length ? (
            <DataTable
              result={table(["container", "restarts", "unhealthy", "oom_kills"], (ctr.worst ?? []).map((c) => [c.container, c.restarts, c.unhealthy, c.oom_kills]))}
              sortable={false}
              columns={[
                { key: "container", label: "Container", render: mono },
                { key: "restarts", align: "right", unit: "count" },
                { key: "unhealthy", align: "right", unit: "count" },
                { key: "oom_kills", label: "OOM kills", align: "right", render: (v) => <span className={Number(v) > 0 ? "dg-bad" : ""}>{formatCount(Number(v))}</span> },
              ]}
            />
          ) : (
            <Calm>Every container stayed up.</Calm>
          )}
        </Block>
      )}

      {sec && (secTotal > 0 || sec.failed_logins > 0 || (sec.hosts_with_pending_updates ?? []).length > 0) && (
        <Block
          title="Security"
          span="span-12"
          description={`${formatCount(secTotal)} finding(s): ${sec.findings_by_severity.critical ?? 0} critical, ${sec.findings_by_severity.warning ?? 0} warning. Failed logins: ${formatCount(sec.failed_logins)}, was ${formatCount(sec.previous_failed_logins)}.`}
        >
          <div className="dg-sec">
            <ul className="dg-kinds">
              {(sec.findings_by_kind ?? []).slice(0, 6).map((k) => (
                <li key={k.kind}>
                  <span>{kindLabel(k.kind)}</span>
                  <b>{formatCount(k.count)}</b>
                </li>
              ))}
            </ul>
            {(sec.hosts_with_pending_updates ?? []).length > 0 && (
              <p className="dg-empty">Updates waiting on {(sec.hosts_with_pending_updates ?? []).map((h) => `${h.host} (${h.pending})`).join(", ")}.</p>
            )}
          </div>
        </Block>
      )}
    </>
  );
}

function Delivery({ meta, channels, onChange }: { meta: AlertsMeta; channels: Channel[]; onChange: () => void }) {
  const digestable = channels.filter((c) => !NO_DIGEST_KINDS.includes(c.kind));
  return (
    <section className="panel span-12 al-digest-schedule">
      <header className="panel-head">
        <h3 className="panel-title">Delivery</h3>
      </header>
      <div className="al-digest-body">
        <p className="al-hint">
          Off by default. Each channel gets the digest weekly on its own day and hour, in {meta.digest_tz} (change it in Settings → Server → Alerts). Email channels get the
          formatted report; Slack, Discord and webhooks get it as text.
        </p>
        {digestable.length === 0 ? (
          <p className="al-hint">Add a chat, webhook or email channel in the Channels tab first. Paging and SMS channels do not get the digest.</p>
        ) : (
          <ul className="al-digest-list">
            {digestable.map((c) => (
              <DigestRow key={c.id} channel={c} canEdit={meta.can_edit} offsetMin={meta.digest_offset_min} onChange={onChange} />
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

function DigestRow({ channel, canEdit, offsetMin, onChange }: { channel: Channel; canEdit: boolean; offsetMin: number; onChange: () => void }) {
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
        chat_id: next.chat_id,
        account_sid: next.account_sid,
        from: next.from,
        region: next.region,
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
        <button type="button" className="btn sm" onClick={sendNow} disabled={sending || !canEdit}>
          <Send size={13} />
          {sending ? "Sending…" : "Send now"}
        </button>
      </div>
      <span className="al-hint al-digest-last">
        {channel.digest_enabled && <>Arrives {localSlot(channel.digest_weekday, channel.digest_hour, offsetMin)} your time · </>}
        {channel.digest_sent_at ? `Last sent ${formatLogTime(channel.digest_sent_at)}` : "Never sent"}
      </span>
    </li>
  );
}

const slotFmt = () => dateFormat({ weekday: "long", hour: "2-digit", minute: "2-digit" });

/**
 * The next send slot (weekday and hour in the digest zone, offsetMin from UTC)
 * written in the viewer's own time zone.
 */
function localSlot(weekday: number, hour: number, offsetMin: number): string {
  const now = new Date();
  // Any week works for weekday and time; start from this week's Sunday, UTC.
  const sunday = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - now.getUTCDay());
  const at = sunday + ((weekday * 24 + hour) * 60 - offsetMin) * 60_000;
  return slotFmt().format(new Date(at));
}

