import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, Bug, Search } from "lucide-react";
import { ConnectCard } from "../components/ConnectCard";
import { DataTable, type Column } from "../components/DataTable";
import { ErrorNotice } from "../components/ErrorNotice";
import { HelpLink } from "../components/Help";
import { CopyButton } from "../components/Modal";
import { Panel } from "../components/Panel";
import { Select } from "../components/Select";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { ServiceMapGraph } from "../components/telemetry/ServiceMapGraph";
import { TraceTimeline, traceTree } from "../components/telemetry/TraceTimeline";
import type { Result } from "../lib/api";
import { logsHref } from "../lib/applogs";
import { formatCount, formatMs, formatPercent, toNumber } from "../lib/format";
import { useMeta, useConnected } from "../lib/meta";
import { load, save } from "../lib/state";
import { records } from "../lib/telemetry";
import { formatLogTime } from "../lib/time";
import {
  NO_FILTERS,
  serviceColors,
  serviceLabel,
  traceHref,
  traceSearchHref,
  tracesApi,
  useServiceMap,
  useTraceSearch,
  type MapEdge,
  type MapNode,
  type TraceFilters,
} from "../lib/traces";
import { useQuery } from "../lib/useQuery";
import { LogDetail } from "./Logs";
import "../components/telemetry/telemetry.css";
import "./Traces.css";

type Tab = "search" | "map";

const TABS: { key: Tab; label: string }[] = [
  { key: "search", label: "Search" },
  { key: "map", label: "Service map" },
];

const TAB_KEY = "va.traces.tab";

/** "#/traces/<id>" is one trace; "#/traces?tab=search&service=…" the list. */
function readHash(): { traceId: string | null; tab: Tab | null; filters: TraceFilters } {
  const [path, qs = ""] = window.location.hash.replace(/^#\/?/, "").split("?");
  const id = path.split("/")[1];
  const p = new URLSearchParams(qs);
  const filters = { ...NO_FILTERS };
  for (const k of Object.keys(NO_FILTERS) as (keyof TraceFilters)[]) filters[k] = p.get(k) ?? "";
  const tab = p.get("tab");
  return { traceId: id ? decodeURIComponent(id) : null, tab: tab === "search" || tab === "map" ? tab : null, filters };
}

/**
 * Distributed traces: one trace across every service it touched, the search
 * that finds them, and the map of which service calls which.
 */
export function Traces() {
  const [where, setWhere] = useState(readHash);
  useEffect(() => {
    const onHash = () => setWhere(readHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  if (where.traceId) return <TracePage id={where.traceId} />;
  return <TraceLists initialTab={where.tab} initialFilters={where.filters} key={`${where.tab}-${JSON.stringify(where.filters)}`} />;
}

function TraceLists({ initialTab, initialFilters }: { initialTab: Tab | null; initialFilters: TraceFilters }) {
  const connected = useConnected("requests");
  const [tab, setTabState] = useState<Tab>(() => initialTab ?? load<Tab>(TAB_KEY, "search"));
  const setTab = (t: Tab) => {
    setTabState(t);
    save(TAB_KEY, t);
  };
  const [filters, setFilters] = useState<TraceFilters>(initialFilters);
  const openSearch = (f: Partial<TraceFilters>) => {
    window.location.hash = traceSearchHref(f);
  };

  return (
    <>
      <TopBar
        title="Traces"
        section="API"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl />
          </>
        }
      />
      <div className="content">
        {connected ? (
          <>
            <nav className="tabs" role="tablist" aria-label="Traces">
              {TABS.map((t) => (
                <button type="button" key={t.key} role="tab" aria-selected={tab === t.key} className={tab === t.key ? "on" : ""} onClick={() => setTab(t.key)}>
                  {t.label}
                </button>
              ))}
            </nav>
            <div className="grid" style={{ marginTop: 12 }}>
              {tab === "search" && <TraceSearch filters={filters} onChange={setFilters} />}
              {tab === "map" && <MapTab onOpen={openSearch} />}
            </div>
          </>
        ) : (
          <ConnectCard title="Connect your services to follow requests across them" text="A trace follows one request through every service it calls, with each service's calls and logs on one timeline." />
        )}
      </div>
    </>
  );
}

function TraceSearch({ filters, onChange }: { filters: TraceFilters; onChange: (f: TraceFilters) => void }) {
  const meta = useMeta();
  // Text settles before querying; selects apply at once.
  const [typed, setTyped] = useState(filters);
  useEffect(() => {
    const id = window.setTimeout(() => onChange(typed), 350);
    return () => window.clearTimeout(id);
  }, [typed]);
  const q = useTraceSearch(filters);
  const set = (k: keyof TraceFilters) => (e: { target: { value: string } }) => setTyped((f) => ({ ...f, [k]: e.target.value }));
  const pick = (k: keyof TraceFilters) => (value: string) => {
    const next = { ...typed, [k]: value };
    setTyped(next);
    onChange(next);
  };
  const active = Object.values(typed).some(Boolean);
  const columns = useMemo(() => (q.data ? searchColumns(q.data) : []), [q.data]);

  return (
    <>
      <div className="filter-row span-12" role="search">
        <input className="input" placeholder="Services, comma separated" value={typed.service} onChange={set("service")} style={{ width: 200 }} aria-label="Services" />
        <Select
          label="Route"
          align="left"
          value={typed.route}
          onChange={pick("route")}
          maxWidth={260}
          options={[{ value: "", label: "Any route" }, ...(meta?.routes ?? []).map((r) => ({ value: r, label: r }))]}
        />
        <div className="search">
          <Search size={14} />
          <input className="input" placeholder="Call name or target, e.g. SELECT users" value={typed.span} onChange={set("span")} aria-label="Call name or target" />
        </div>
        <input className="input" inputMode="numeric" placeholder="Min ms" value={typed.min_ms} onChange={set("min_ms")} style={{ width: 90 }} aria-label="Minimum duration in milliseconds" />
        <Select
          label="Errors"
          align="left"
          value={typed.errors}
          onChange={pick("errors")}
          options={[
            { value: "", label: "With or without errors" },
            { value: "1", label: "Only with errors" },
          ]}
        />
        {active && (
          <button
            type="button"
            className="btn ghost"
            onClick={() => {
              setTyped(NO_FILTERS);
              onChange(NO_FILTERS);
            }}
          >
            Clear
          </button>
        )}
      </div>
      <Panel
        className="span-12"
        title="Traces"
        description="Traces that started in the range, newest first, at most 50. The root is the request that started the trace; duration runs from its start to the end of the last request in it."
        result={q.data}
        loading={q.loading}
        error={q.error}
        bodyHeight={240}
        table={
          q.data &&
          (q.data.rows.length === 0 ? (
            <div className="panel-state empty-why">
              <div>{active ? "No traces match these filters in this range." : "No traces in this range."}</div>
              <HelpLink topic="tracing">How requests join a trace</HelpLink>
            </div>
          ) : (
            <DataTable result={q.data} columns={columns} maxHeight={640} onRowClick={(row) => (window.location.hash = traceHref(String(row[0])))} rowKey={(row) => row[0]} />
          ))
        }
      />
    </>
  );
}

function searchColumns(res: Result): Column[] {
  const col = (name: string) => res.columns.indexOf(name);
  const all = res.rows.flatMap((r) => (r[col("services")] as string[] | null) ?? []);
  const colors = serviceColors(all);
  return [
    { key: "started", label: "Started", render: (v) => <span className="mono">{formatLogTime(String(v))}</span>, width: 150 },
    {
      key: "route",
      label: "Root request",
      render: (v, row) => (
        <span className="span-name">
          <span className="wf-swatch" style={{ background: colors.get(String(row[col("service")] ?? "")) }} aria-hidden />
          <span className="mono">
            {String(row[col("method")] ?? "")} {String(v ?? "")}
          </span>
          {(toNumber(row[col("status")]) ?? 0) >= 500 && <span className="flag bad">{String(row[col("status")])}</span>}
        </span>
      ),
    },
    {
      key: "services",
      label: "Services",
      render: (v) => (
        <span className="tr-services">
          {((v as string[] | null) ?? []).map((s) => (
            <span key={s} className="tr-service">
              <span className="wf-swatch" style={{ background: colors.get(s) }} aria-hidden />
              {serviceLabel(s)}
            </span>
          ))}
        </span>
      ),
    },
    { key: "requests", align: "right", unit: "count", width: 80 },
    { key: "spans", label: "Calls", align: "right", unit: "count", width: 70 },
    {
      key: "errors",
      align: "right",
      width: 70,
      render: (v) => {
        const n = toNumber(v) ?? 0;
        return n > 0 ? <span className="flag bad">{formatCount(n)}</span> : <span className="dim">0</span>;
      },
    },
    { key: "duration_ms", label: "Duration", align: "right", render: (v) => formatMs(toNumber(v) ?? 0), width: 90 },
  ];
}

function MapTab({ onOpen }: { onOpen: (f: Partial<TraceFilters>) => void }) {
  const q = useServiceMap();
  const byId = useMemo(() => new Map((q.data?.nodes ?? []).map((n) => [n.id, n])), [q.data]);
  const openEdge = (e: MapEdge) => {
    const a = byId.get(e.source);
    const b = byId.get(e.target);
    if (!a || !b) return;
    if (b.kind === "service") onOpen({ service: [a.name, b.name].filter(Boolean).join(",") });
    else onOpen({ service: a.name, span: b.name });
  };
  const openNode = (n: MapNode) => (n.kind === "service" ? onOpen({ service: n.name }) : onOpen({ span: n.name }));
  const table = useMemo<Result | undefined>(() => {
    if (!q.data) return undefined;
    return {
      columns: ["from", "to", "kind", "per_min", "calls", "error_rate", "p95", "key"],
      rows: q.data.edges.map((e) => [
        serviceLabel(byId.get(e.source)?.name),
        e.kind === "service" ? serviceLabel(byId.get(e.target)?.name) : (byId.get(e.target)?.name ?? ""),
        e.kind,
        e.per_min,
        e.calls,
        e.error_rate,
        e.p95,
        `${e.source}>${e.target}`,
      ]),
    } as Result;
  }, [q.data, byId]);
  const edgeAt = (key: unknown) => q.data?.edges.find((e) => `${e.source}>${e.target}` === key);
  const empty = q.data && q.data.nodes.length === 0;
  const services = q.data?.nodes.filter((n) => n.kind === "service").length ?? 0;
  const crossing = q.data?.edges.filter((e) => e.kind === "service").length ?? 0;

  return (
    <>
      <Panel
        className="span-12"
        title="Service map"
        description="Each box is a service, or a database, host, cache or queue they call. An arrow is calls from one to the other in the range: thicker is more often, amber or red when some failed. Calls between services come from traces; click an arrow, a box or a row of the table to list the traces behind it."
        result={table}
        loading={q.loading}
        error={q.error}
        bodyHeight={360}
        chart={
          q.data &&
          (empty ? (
            <div className="panel-state empty-why">
              <div>No requests or calls in this range.</div>
              <HelpLink topic="tracing">How requests join a trace</HelpLink>
            </div>
          ) : (
            <>
              {services > 1 && crossing === 0 && (
                <p className="dim smap-note">
                  No calls between services were traced in this range. Calls join up once the caller sends <code>traceparent</code> (the Visionary HTTP transport does) and the service it calls records it.
                </p>
              )}
              <ServiceMapGraph map={q.data} onEdge={openEdge} onNode={openNode} />
            </>
          ))
        }
        table={
          table && (
            <DataTable
              result={table}
              maxHeight={420}
              rowKey={(row) => row[7]}
              onRowClick={(row) => {
                const e = edgeAt(row[7]);
                if (e) openEdge(e);
              }}
              columns={[
                { key: "from", label: "From" },
                { key: "to", label: "To", render: (v) => <span className="mono">{String(v)}</span> },
                { key: "kind", label: "Kind", width: 80 },
                { key: "per_min", label: "Per min", align: "right", render: (v) => formatCount(toNumber(v) ?? 0), width: 80 },
                { key: "calls", align: "right", unit: "count", width: 80 },
                {
                  key: "error_rate",
                  label: "Failed",
                  align: "right",
                  width: 80,
                  render: (v) => {
                    const n = toNumber(v) ?? 0;
                    return <span className={n >= 5 ? "flag bad" : n > 0 ? "flag warn" : "dim"}>{formatPercent(n)}</span>;
                  },
                },
                { key: "p95", align: "right", render: (v) => (toNumber(v) === null ? <span className="dim">—</span> : formatMs(toNumber(v)!)), width: 80 },
              ]}
            />
          )
        }
      />
    </>
  );
}

/** One trace on its own page: summary, timeline, errors and log lines. */
function TracePage({ id }: { id: string }) {
  const q = useQuery(`trace-${id}`, (signal) => tracesApi.trace(id, signal));
  const [openLog, setOpenLog] = useState<number | null>(null);
  const tree = useMemo(() => traceTree(q.data), [q.data]);
  const errors = useMemo(() => records(q.data?.errors), [q.data]);
  const lines = useMemo(() => records(q.data?.logs), [q.data]);
  const services = useMemo(() => [...new Set(tree.nodes.map((n) => n.service))].sort(), [tree]);
  const colors = useMemo(() => serviceColors(services), [services]);
  const failed = tree.nodes.filter((n) => n.failed).length;
  const spanName = useMemo(() => new Map(tree.nodes.map((n) => [n.spanId, n.name])), [tree]);
  const root = tree.nodes[0];
  const logsLink = logsHref(`trace:${id}`, tree.start ? { from: new Date(tree.start - 60_000), to: new Date(tree.end + 15 * 60_000) } : undefined);
  const back = () => {
    if (window.history.length > 1) window.history.back();
    else window.location.hash = "#/traces";
  };

  return (
    <>
      <TopBar title="Trace" section="Traces" />
      <div className="content">
        <div className="tr-head">
          <button type="button" className="btn" onClick={back}>
            <ArrowLeft size={14} />
            Back
          </button>
          <span className="dim">Trace</span>
          <span className="mono tr-id">{id}</span>
          <CopyButton text={id} label="Copy ID" />
        </div>
        {q.error && <ErrorNotice title="Could not load the trace" error={q.error} style={{ marginBottom: 12 }} />}
        {q.data && tree.nodes.length === 0 && lines.length === 0 && (
          <div className="panel-state empty-why">
            <div>Nothing with this trace ID is stored. It may be older than Settings → Retention keeps, or its services don't record trace IDs yet.</div>
            <HelpLink topic="tracing">How requests join a trace</HelpLink>
          </div>
        )}
        {q.data && (tree.nodes.length > 0 || lines.length > 0) && (
          <div className="grid">
            <StatTile label="Duration" value={formatMs(tree.end - tree.start)} footer={root ? `${serviceLabel(root.service)} · ${root.name}` : undefined} delta={null} deltaLabel="" upIs="neutral" />
            <StatTile label="Services" value={formatCount(services.length)} footer={services.map(serviceLabel).join(", ")} delta={null} deltaLabel="" upIs="neutral" />
            <StatTile label="Spans" value={formatCount(tree.nodes.length)} footer={`${formatCount(tree.nodes.filter((n) => n.kind === "request").length)} requests`} delta={null} deltaLabel="" upIs="neutral" />
            <StatTile label="Failed" value={formatCount(failed)} footer={`${formatCount(errors.length)} errors recorded`} delta={null} deltaLabel="" upIs="neutral" />

            {errors.length > 0 && (
              <section className="span-12 panel tr-section">
                <h3 className="rt-title">Errors</h3>
                <ul className="rt-errors">
                  {errors.map((e) => (
                    <li key={String(e.id)}>
                      <a href={`#/errors?fp=${encodeURIComponent(String(e.fingerprint))}`} className="rt-error">
                        <Bug size={13} aria-hidden />
                        <span className="tr-service">
                          <span className="wf-swatch" style={{ background: colors.get(String(e.service ?? "")) }} aria-hidden />
                          {serviceLabel(e.service)}
                        </span>
                        <span className="mono rt-error-type">{String(e.type)}</span>
                        <span className="rt-error-msg">{String(e.message ?? "")}</span>
                        <span className="pill">{toNumber(e.handled) ? "Handled" : "Unhandled"}</span>
                      </a>
                    </li>
                  ))}
                </ul>
              </section>
            )}

            {tree.nodes.length > 0 && (
              <section className="span-12 panel tr-section">
                <h3 className="rt-title">Timeline</h3>
                <TraceTimeline trace={q.data} onOpenRequest={setOpenLog} />
              </section>
            )}

            {lines.length > 0 && (
              <section className="span-12 panel tr-section">
                <div className="tr-section-head">
                  <h3 className="rt-title">Log lines</h3>
                  <a className="btn sm" href={logsLink}>
                    Open in Logs
                  </a>
                </div>
                <ul className="rt-loglines">
                  {lines.map((l) => (
                    <li key={String(l.id)}>
                      <span className="mono dim">+{formatMs(Math.max(0, new Date(String(l.created_at)).getTime() - tree.start))}</span>
                      <span className="tr-service">
                        <span className="wf-swatch" style={{ background: colors.get(String(l.service ?? "")) }} aria-hidden />
                        {serviceLabel(l.service)}
                      </span>
                      <span className={`wf-loglevel lv-${String(l.level ?? "info")}`}>{String(l.level ?? "info")}</span>
                      <span className="mono">{String(l.message ?? "").split("\n")[0]}</span>
                      {l.span_id && spanName.get(String(l.span_id)) ? <span className="dim tr-in">in {spanName.get(String(l.span_id))}</span> : null}
                    </li>
                  ))}
                </ul>
              </section>
            )}
          </div>
        )}
        {q.loading && !q.data && <div className="panel-state">Loading…</div>}
      </div>
      {openLog !== null && <LogDetail id={openLog} onClose={() => setOpenLog(null)} />}
    </>
  );
}
