import { useEffect, useState } from "react";
import { BellOff, CheckCircle2, Clock3, Download, History, Search, Sparkles, XCircle } from "lucide-react";
import { Select } from "../Select";
import { useApp } from "../../lib/state";
import { useQuery } from "../../lib/useQuery";
import { alertsApi, suppressedText, type AlertEvent, type Delivery, type RuleView } from "../../lib/alerts";
import { formatLogTime } from "../../lib/time";
import { titleCase } from "../../lib/format";
import { SeverityTag, StateBadge } from "./bits";
import { ErrorNotice } from "../ErrorNotice";
import { KIND_LABEL } from "./ChannelsTab";

export function ActivityTab({ rules, refreshKey }: { rules: RuleView[]; refreshKey: number }) {
  const { resolved, tick } = useApp();
  const [rule, setRule] = useState("");
  const [severity, setSeverity] = useState("");
  const [state, setState] = useState("");
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  // Searching waits for a pause in typing.
  useEffect(() => {
    const id = window.setTimeout(() => setQ(search.trim()), 350);
    return () => window.clearTimeout(id);
  }, [search]);
  const filters = { rule_id: rule, severity, state, q };
  const key = `events?${resolved.from.toISOString()}|${resolved.to.toISOString()}|${rule}|${severity}|${state}|${q}#${tick}.${refreshKey}`;
  const events = useQuery(key, (signal) => alertsApi.events(resolved.from, resolved.to, filters, signal));
  // Pages loaded with "Load more", reset whenever the first page changes.
  const [more, setMore] = useState<{ events: AlertEvent[]; next: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreErr, setMoreErr] = useState("");
  useEffect(() => {
    setMore(null);
    setMoreErr("");
  }, [key]);
  const list = [...(events.data?.events ?? []), ...(more?.events ?? [])];
  const next = more ? more.next : (events.data?.next ?? "");

  const loadMore = async () => {
    setLoadingMore(true);
    setMoreErr("");
    try {
      const page = await alertsApi.events(resolved.from, resolved.to, { ...filters, before: next });
      setMore((m) => ({ events: [...(m?.events ?? []), ...page.events], next: page.next }));
    } catch (e) {
      setMoreErr((e as Error).message);
    } finally {
      setLoadingMore(false);
    }
  };

  return (
    <>
      <div className="al-toolbar">
        <div className="al-filters">
          <label className="al-search">
            <Search size={14} aria-hidden />
            <input className="input" value={search} placeholder="Search messages" aria-label="Search activity" onChange={(e) => setSearch(e.target.value)} />
          </label>
          <Select label="Rule" align="left" maxWidth={260} value={rule} onChange={setRule} options={[{ value: "", label: "All rules" }, ...rules.map((r) => ({ value: String(r.id), label: r.name }))]} />
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
          <Select
            label="State"
            align="left"
            value={state}
            onChange={setState}
            options={[
              { value: "", label: "Fired and resolved" },
              { value: "firing", label: "Fired" },
              { value: "resolved", label: "Resolved" },
            ]}
          />
        </div>
        <div className="al-toolbar-actions">
          <span className="al-hint">{events.data ? `${list.length}${next ? "+" : ""} event${list.length === 1 ? "" : "s"} in this range` : ""}</span>
          <a className="btn ghost" href={alertsApi.eventsCSVURL(resolved.from, resolved.to, filters)} download title="Up to 10,000 events with these filters">
            <Download size={14} />
            CSV
          </a>
        </div>
      </div>
      {events.error && <ErrorNotice title="Could not load activity" error={events.error} />}
      {events.data && list.length === 0 ? (
        <div className="empty panel">
          <div className="empty-icon">
            <History size={20} />
          </div>
          <h3>No alert activity in this range</h3>
          <p>Each time a rule starts firing or resolves it is listed here, with the reading, where it was sent and, when a model is configured, the likely cause.</p>
        </div>
      ) : (
        <>
          <ol className={`al-timeline ${events.loading ? "refetching" : ""}`}>
            {list.map((e) => (
              <EventItem key={e.id} e={e} />
            ))}
          </ol>
          {next && (
            <div className="al-more">
              {moreErr && <span className="form-error">{moreErr}</span>}
              <button type="button" className="btn" onClick={loadMore} disabled={loadingMore}>
                {loadingMore ? "Loading…" : "Load more"}
              </button>
            </div>
          )}
        </>
      )}
    </>
  );
}

function EventItem({ e }: { e: AlertEvent }) {
  return (
    <li className={`al-event ${e.state}`}>
      <span className="al-event-mark" aria-hidden />
      <div className="al-event-body panel">
        <div className="al-event-head">
          <StateBadge state={e.state} />
          <span className="al-event-rule">{e.rule_name}</span>
          {e.series_key && <span className="pill">{e.series_key}</span>}
          <SeverityTag severity={e.severity} />
          <span className="al-event-time mono">{formatLogTime(e.created_at)}</span>
        </div>
        <p className="al-event-msg">{e.message}</p>
        {e.explanation && (
          <div className="al-explain">
            <div className="al-explain-head">
              <Sparkles size={13} aria-hidden /> Likely cause
            </div>
            {e.explanation.split("\n").map((l, i) => (
              <p key={i}>{l}</p>
            ))}
          </div>
        )}
        {e.deliveries.length > 0 && (
          // The firing's own sends first, then its reminders, escalations and
          // acknowledgement, in the order they went out.
          <ul className="al-deliveries">
            {e.deliveries.map((d) => (
              <DeliveryRow key={d.id} d={d} />
            ))}
          </ul>
        )}
        <div className="al-event-foot">
          <span>{e.kind === "slo" ? "SLO" : titleCase(e.kind)}</span>
          {e.suppressed ? (
            <span className="al-inline">
              <BellOff size={12} aria-hidden /> {suppressedText(e.suppressed)}
            </span>
          ) : e.deliveries.length === 0 ? (
            <span>{e.notified ? "Notified" : "Not sent: the rule has no channels."}</span>
          ) : null}
        </div>
      </div>
    </li>
  );
}

const EVENT_WORD: Record<string, string> = { firing: "", resolved: "", reminder: "Reminder", escalation: "Escalation", ack: "Acknowledgement" };

function DeliveryRow({ d }: { d: Delivery }) {
  const Icon = d.status === "sent" ? CheckCircle2 : d.status === "failed" ? XCircle : Clock3;
  const status =
    d.status === "sent"
      ? `Delivered${d.attempts > 1 ? ` on attempt ${d.attempts}` : ""}`
      : d.status === "failed"
        ? `Failed after ${d.attempts} attempt${d.attempts === 1 ? "" : "s"}`
        : d.attempts === 0
          ? "Sending…"
          : `Retrying${d.next_at ? ` at ${formatLogTime(d.next_at)}` : ""} (attempt ${d.attempts} failed)`;
  return (
    <li className={`al-delivery ${d.status}`}>
      <Icon size={13} aria-hidden />
      <span className="al-delivery-ch">{d.channel_name}</span>
      <span className="al-hint">{KIND_LABEL[d.channel_kind] ?? d.channel_kind}</span>
      {EVENT_WORD[d.event] && <span className="pill">{EVENT_WORD[d.event]}</span>}
      <span className="al-delivery-status">{status}</span>
      {d.error && d.status !== "sent" && (
        <span className="al-delivery-err mono" title={d.error}>
          {d.error}
        </span>
      )}
    </li>
  );
}
