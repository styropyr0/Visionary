import { useEffect, useMemo, useState } from "react";
import { Search, Siren } from "lucide-react";
import type { Result } from "../lib/api";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { useSession } from "../lib/team";
import { formatDateTime, formatRelative } from "../lib/time";
import { SEVERITIES, formatSpan, incidentHref, incidentsApi, type Incident, type Severity } from "../lib/incidents";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { StatTile } from "../components/StatTile";
import { Panel } from "../components/Panel";
import { VizView } from "../components/VizView";
import { DataTable } from "../components/DataTable";
import { Select } from "../components/Select";
import { ErrorNotice } from "../components/ErrorNotice";
import { DeclareModal } from "./Incidents.declare";
import { IncidentDetail } from "./Incidents.detail";
import { SevTag, StatusBadge, openIncident } from "./Incidents.parts";
import "./Incidents.css";

/** The page's state in the address: #/incidents?id=4, or ?declare=alert&ref=12 to open the form. */
function readHash(): { id: number | null; declare: { kind: string; ref: string } | null } {
  const p = new URLSearchParams(window.location.hash.split("?")[1] ?? "");
  const n = Number(p.get("id"));
  const kind = p.get("declare");
  return { id: Number.isInteger(n) && n > 0 ? n : null, declare: kind !== null ? { kind, ref: p.get("ref") ?? "" } : null };
}

export function Incidents() {
  const [hash, setHash] = useState(readHash);
  useEffect(() => {
    const onHash = () => setHash(readHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return hash.id ? <IncidentDetail key={hash.id} id={hash.id} /> : <IncidentList declare={hash.declare} />;
}

const VIEWS = [
  { key: "active", label: "Active" },
  { key: "resolved", label: "Resolved" },
  { key: "", label: "All" },
];

function IncidentList({ declare }: { declare: { kind: string; ref: string } | null }) {
  const { resolved, tick } = useApp();
  const { can } = useSession();
  const [view, setView] = useState("");
  const [severity, setSeverity] = useState("");
  const [service, setService] = useState("");
  const [q, setQ] = useState("");
  const [declaring, setDeclaring] = useState(false);
  const from = resolved.from.toISOString();
  const filters = { status: view, severity, service, q, from, to: resolved.to.toISOString() };
  const list = useQuery(`incidents?${JSON.stringify(filters)}#${tick}`, (signal) => incidentsApi.list(filters, signal));
  const stats = useQuery(`incident-stats?${from}|${resolved.to.toISOString()}#${tick}`, (signal) => incidentsApi.stats(resolved.from, resolved.to, signal));
  const services = useMemo(() => [...new Set((list.data?.incidents ?? []).flatMap((i) => i.services))].sort(), [list.data]);
  const open = (declaring || declare !== null) && can("edit");
  const closeDeclare = () => {
    setDeclaring(false);
    if (declare) window.history.replaceState(null, "", "#/incidents");
    if (declare) window.dispatchEvent(new HashChangeEvent("hashchange"));
  };

  return (
    <>
      <TopBar
        title="Incidents"
        section="Manage"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl busy={list.loading || stats.loading} />
            {can("edit") && (
              <button type="button" className="btn primary" onClick={() => setDeclaring(true)}>
                <Siren size={14} />
                Declare incident
              </button>
            )}
          </>
        }
      />
      <div className="content">
        <div className="grid">
          <StatTile label="Active now" value={stats.data ? String(stats.data.open) : "—"} delta={null} deltaLabel="" upIs="bad" loading={stats.loading} footer="not yet resolved" />
          <StatTile label="Declared" value={stats.data ? String(stats.data.total) : "—"} delta={null} deltaLabel="" upIs="neutral" loading={stats.loading} footer={stats.data ? severityLine(stats.data.by_severity) : "in this range"} />
          <StatTile label="Mean time to acknowledge" value={formatSpan(stats.data?.mtta_s)} description="From the start of the impact until someone took command or moved the incident on." delta={null} deltaLabel="" upIs="bad" loading={stats.loading} footer="MTTA" />
          <StatTile label="Mean time to mitigate" value={formatSpan(stats.data?.mttm_s)} description="From the start of the impact until a fix was in place (Monitoring)." delta={null} deltaLabel="" upIs="bad" loading={stats.loading} footer="MTTM" />
          <StatTile label="Mean time to resolve" value={formatSpan(stats.data?.mttr_s)} description="From the start of the impact until the incident was resolved." delta={null} deltaLabel="" upIs="bad" loading={stats.loading} footer="MTTR" />
          <StatTile label="Unassigned" value={stats.data ? String(stats.data.unassigned) : "—"} delta={null} deltaLabel="" upIs="bad" loading={stats.loading} footer="active without a commander" />
          <Panel
            className="span-12"
            title="Incidents by severity"
            description="Incidents declared in each hour, day or week of the range, by severity."
            result={stats.data ? severityResult(stats.data.buckets, stats.data.step_s, from, resolved.to.toISOString()) : undefined}
            error={stats.error}
            loading={stats.loading}
            bodyHeight={200}
            chart={stats.data && <VizView result={severityResult(stats.data.buckets, stats.data.step_s, from, resolved.to.toISOString())} viz={{ type: "timeseries", x: "t", y: ["incidents"], series: "severity", stack: true, unit: "count" }} height={200} title="Incidents by severity" empty="No incidents declared in this time range." />}
            table={stats.data && <DataTable result={severityResult(stats.data.buckets, stats.data.step_s, from, resolved.to.toISOString())} maxHeight={200} />}
          />
        </div>

        <div className="filter-row inc-filters">
          <div className="segmented" role="radiogroup" aria-label="Show">
            {VIEWS.map((v) => (
              <button type="button" key={v.key} role="radio" aria-checked={view === v.key} className={view === v.key ? "on" : ""} onClick={() => setView(v.key)}>
                {v.label}
              </button>
            ))}
          </div>
          <Select label="Severity" align="left" value={severity} onChange={setSeverity} options={[{ value: "", label: "Any severity" }, ...SEVERITIES.map((s) => ({ value: s.key, label: s.label }))]} />
          <Select label="Service" align="left" value={service} onChange={setService} options={[{ value: "", label: "Any service" }, ...services.map((s) => ({ value: s, label: s }))]} />
          <div className="search">
            <Search size={14} />
            <input className="input" value={q} placeholder="Search titles and impact" onChange={(e) => setQ(e.target.value)} aria-label="Search incidents" />
          </div>
        </div>

        {list.error && !list.data ? (
          <ErrorNotice title="Could not load incidents" error={list.error} />
        ) : !list.data ? (
          <div className="panel-state" style={{ height: 200 }} />
        ) : list.data.incidents.length === 0 ? (
          <div className="panel empty">
            <span className="empty-icon">
              <Siren size={20} />
            </span>
            <h3>{view || severity || service || q ? "No incidents match" : "No incidents in this range"}</h3>
            <p>
              Declare one when something is broken for users: from scratch here, or from a firing alert, a failing uptime check, an error issue or a log line with their{" "}
              <b>Declare incident</b> button.
            </p>
          </div>
        ) : (
          <div className="panel">
            <div className="table-wrap">
              <table className="data inc-table">
                <thead>
                  <tr>
                    <th>Severity</th>
                    <th>Incident</th>
                    <th>Status</th>
                    <th>Commander</th>
                    <th>Started</th>
                    <th className="num">Duration</th>
                  </tr>
                </thead>
                <tbody>
                  {list.data.incidents.map((i) => (
                    <IncidentRow key={i.id} incident={i} />
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>
      {open && <DeclareModal source={declare ?? undefined} onClose={closeDeclare} onDeclared={(inc) => openIncident(inc.id)} />}
    </>
  );
}

function IncidentRow({ incident: i }: { incident: Incident }) {
  const go = () => openIncident(i.id);
  const running = i.ttr_s ?? Math.max(0, (Date.now() - Date.parse(i.started_at)) / 1000);
  return (
    <tr className="clickable" onClick={go}>
      <td>
        <SevTag severity={i.severity} />
      </td>
      <td className="inc-title-cell">
        <a href={incidentHref(i.id)} onClick={(e) => e.stopPropagation()}>
          {i.title}
        </a>
        {i.services.length > 0 && <span className="dim"> · {i.services.join(", ")}</span>}
      </td>
      <td>
        <StatusBadge status={i.status} />
      </td>
      <td>{i.commander || <span className="inc-unassigned">Unassigned</span>}</td>
      <td title={formatDateTime(new Date(i.started_at))}>{formatRelative(i.started_at)}</td>
      <td className="num">{formatSpan(running)}{i.resolved_at ? "" : " so far"}</td>
    </tr>
  );
}

function severityLine(by: Partial<Record<Severity, number>>): string {
  const parts = SEVERITIES.filter((s) => by[s.key]).map((s) => `${by[s.key]} ${s.label}`);
  return parts.length ? parts.join(", ") : "in this range";
}

/** The stats' buckets as a long-format result for the chart and its table. */
function severityResult(buckets: { at: string; counts: Partial<Record<Severity, number>> }[], step: number, from: string, to: string): Result {
  const rows: unknown[][] = [];
  for (const b of buckets) for (const s of SEVERITIES) rows.push([b.at, s.label, b.counts[s.key] ?? 0]);
  const any = rows.some((r) => (r[2] as number) > 0);
  return { columns: ["t", "severity", "incidents"], rows: any ? rows : [], truncated: false, rows_read: 0, duration_ms: 0, sql: "", bucket: step, from, to };
}
