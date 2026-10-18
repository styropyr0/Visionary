import { useState } from "react";
import { History, Sparkles } from "lucide-react";
import { Select } from "../Select";
import { useApp } from "../../lib/state";
import { useQuery } from "../../lib/useQuery";
import { alertsApi, type AlertEvent, type RuleView } from "../../lib/alerts";
import { formatLogTime } from "../../lib/time";
import { SeverityTag, StateBadge } from "./bits";
import { ErrorNotice } from "../ErrorNotice";

export function ActivityTab({ rules, refreshKey }: { rules: RuleView[]; refreshKey: number }) {
  const { resolved, tick } = useApp();
  const [rule, setRule] = useState("");
  const [severity, setSeverity] = useState("");
  const [state, setState] = useState("");
  const filters = { rule_id: rule, severity, state };
  const key = `events?${resolved.from.toISOString()}|${resolved.to.toISOString()}|${rule}|${severity}|${state}#${tick}.${refreshKey}`;
  const events = useQuery(key, (signal) => alertsApi.events(resolved.from, resolved.to, filters, signal));
  const list = events.data?.events ?? [];

  return (
    <>
      <div className="al-toolbar">
        <div className="al-filters">
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
        <span className="al-hint">{events.data ? `${list.length} event${list.length === 1 ? "" : "s"} in this range` : ""}</span>
      </div>
      {events.error && <ErrorNotice title="Could not load activity" error={events.error} />}
      {events.data && list.length === 0 ? (
        <div className="empty panel">
          <div className="empty-icon">
            <History size={20} />
          </div>
          <h3>No alert activity in this range</h3>
          <p>Each time a rule starts firing or resolves it is listed here, with the reading and, when a model is configured, the likely cause.</p>
        </div>
      ) : (
        <ol className={`al-timeline ${events.loading ? "refetching" : ""}`}>
          {list.map((e) => (
            <EventItem key={e.id} e={e} />
          ))}
        </ol>
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
        <div className="al-event-foot">
          <span>{e.kind}</span>
          <span>{e.notified ? "Notified" : "Not notified (muted, no channels or delivery failed)"}</span>
        </div>
      </div>
    </li>
  );
}
