import { useMemo, useState, type ReactNode } from "react";
import { Bell, Crosshair, ListTree, ScrollText } from "lucide-react";
import type { Result } from "../lib/api";
import { formatCount, formatMs, formatPercent, toNumber } from "../lib/format";
import { incidentHref } from "../lib/incidents";
import { load, save } from "../lib/state";
import { formatRelative } from "../lib/time";
import {
  alertOnEdgeHref,
  change,
  edgeKey,
  errorRoles,
  filterMap,
  NO_MAP_FILTERS,
  serviceLabel,
  useEdgeDetail,
  useMapOverlay,
  useMapSeries,
  useServiceDetail,
  useServiceMap,
  type ErrorRole,
  type MapEdge,
  type MapFilters,
  type MapHealth,
  type MapNode,
  type ServiceMap,
  type TraceFilters,
} from "../lib/traces";
import { DataTable } from "../components/DataTable";
import { HelpLink } from "../components/Help";
import { Drawer } from "../components/Modal";
import { Panel } from "../components/Panel";
import { Select } from "../components/Select";
import { changeText, edgeSummary, rate, ServiceMapGraph } from "../components/telemetry/ServiceMapGraph";

const COMPARE_KEY = "va.smap.compare";

const KINDS = [
  { value: "", label: "All calls" },
  { value: "service", label: "Between services" },
  { value: "db", label: "Databases" },
  { value: "http", label: "HTTP hosts" },
  { value: "cache", label: "Caches" },
  { value: "queue", label: "Queues" },
];

const nodeLabel = (n: MapNode | undefined) => (n ? (n.kind === "service" ? serviceLabel(n.name) : n.name) : "");

export function MapTab({ onOpen }: { onOpen: (f: Partial<TraceFilters>) => void }) {
  const [compare, setCompareState] = useState(() => load<boolean>(COMPARE_KEY, false));
  const setCompare = (on: boolean) => {
    setCompareState(on);
    save(COMPARE_KEY, on);
  };
  const [filters, setFilters] = useState<MapFilters>(NO_MAP_FILTERS);
  const [minText, setMinText] = useState("");
  const q = useServiceMap(compare);
  const shown = useMemo(() => (q.data ? filterMap(q.data, filters) : undefined), [q.data, filters]);
  const serviceNames = useMemo(() => (q.data?.nodes ?? []).filter((n) => n.kind === "service").map((n) => n.name), [q.data]);
  const overlay = useMapOverlay(serviceNames);
  const series = useMapSeries();
  const roles = useMemo(() => (shown ? errorRoles(shown) : new Map<string, ErrorRole>()), [shown]);
  const byId = useMemo(() => new Map((q.data?.nodes ?? []).map((n) => [n.id, n])), [q.data]);
  const [picked, setPicked] = useState<{ node?: MapNode; edge?: MapEdge } | null>(null);

  const openEdgeTraces = (e: MapEdge) => {
    const a = byId.get(e.source);
    const b = byId.get(e.target);
    if (!a || !b) return;
    if (b.kind === "service") onOpen({ service: [a.name, b.name].filter(Boolean).join(",") });
    else onOpen({ service: a.name, span: b.name });
  };
  const openNodeTraces = (n: MapNode) => (n.kind === "service" ? onOpen({ service: n.name }) : onOpen({ span: n.name }));

  const prevEdges = useMemo(() => new Map((shown?.previous?.edges ?? []).map((e) => [edgeKey(e), e])), [shown]);
  const table = useMemo<Result | undefined>(() => {
    if (!shown) return undefined;
    const now = new Set(shown.edges.map(edgeKey));
    const row = (e: MapEdge, delta: string) => [
      nodeLabel(byId.get(e.source)),
      nodeLabel(byId.get(e.target)) || e.target,
      e.kind,
      e.per_min,
      e.calls,
      e.error_rate,
      e.p95,
      delta,
      edgeKey(e),
    ];
    const rows = shown.edges.map((e) => {
      const p = prevEdges.get(edgeKey(e));
      return row(e, !compare ? "" : p ? changeText(change(e.per_min, p.per_min)) || "same" : "new");
    });
    if (compare) for (const e of shown.previous?.edges ?? []) if (!now.has(edgeKey(e))) rows.push(row({ ...e, per_min: 0, calls: 0 }, "stopped"));
    return { columns: ["from", "to", "kind", "per_min", "calls", "error_rate", "p95", "change", "key"], rows } as Result;
  }, [shown, byId, prevEdges, compare]);
  const edgeAt = (key: unknown) => shown?.edges.find((e) => edgeKey(e) === key);
  const empty = q.data && q.data.nodes.length === 0;
  const services = q.data?.nodes.filter((n) => n.kind === "service").length ?? 0;
  const crossing = q.data?.edges.filter((e) => e.kind === "service").length ?? 0;
  const active = filters.focus !== "" || filters.minRate > 0 || filters.failing || filters.kind !== "";
  const set = (patch: Partial<MapFilters>) => setFilters((f) => ({ ...f, ...patch }));
  const focusOptions = [
    { value: "", label: "Whole map" },
    ...[...(q.data?.nodes ?? [])].sort((a, b) => (a.kind === "service" ? 0 : 1) - (b.kind === "service" ? 0 : 1) || a.name.localeCompare(b.name)).map((n) => ({ value: n.id, label: `Around ${nodeLabel(n)}` })),
  ];

  return (
    <>
      <div className="filter-row" role="search">
        <Select label="Focus" align="left" maxWidth={240} value={filters.focus} onChange={(v) => set({ focus: v })} options={focusOptions} />
        {filters.focus && (
          <Select
            label="Hops"
            align="left"
            value={String(filters.hops)}
            onChange={(v) => set({ hops: Number(v) })}
            options={[
              { value: "1", label: "1 hop" },
              { value: "2", label: "2 hops" },
            ]}
          />
        )}
        <Select label="Kind of call" align="left" value={filters.kind} onChange={(v) => set({ kind: v })} options={KINDS} />
        <Select
          label="Failures"
          align="left"
          value={filters.failing ? "1" : ""}
          onChange={(v) => set({ failing: v === "1" })}
          options={[
            { value: "", label: "Failing or not" },
            { value: "1", label: "Only failing arrows" },
          ]}
        />
        <input
          className="input"
          inputMode="decimal"
          placeholder="Min calls/min"
          value={minText}
          onChange={(e) => {
            setMinText(e.target.value);
            const n = Number(e.target.value);
            set({ minRate: Number.isFinite(n) && n > 0 ? n : 0 });
          }}
          style={{ width: 120 }}
          aria-label="Hide arrows under this many calls per minute"
        />
        <Select
          label="Compare"
          align="left"
          value={compare ? "1" : ""}
          onChange={(v) => setCompare(v === "1")}
          options={[
            { value: "", label: "No comparison" },
            { value: "1", label: "Compare with the window before" },
          ]}
        />
        {active && (
          <button
            type="button"
            className="btn ghost"
            onClick={() => {
              setFilters(NO_MAP_FILTERS);
              setMinText("");
            }}
          >
            Clear
          </button>
        )}
      </div>
      <Panel
        className="tr-fill-panel"
        title="Service map"
        description="Each box is a service, or a database, host, cache or queue they call. An arrow is calls from one to the other in the range: thicker is more often, amber or red when some failed. A red outline marks where failures likely start, a dashed amber one a service passing on failures from its calls. Icons on a box mark a deploy, an open incident, a firing alert or a busy machine. Click a box or an arrow for its detail."
        result={table}
        loading={q.loading}
        error={q.error}
        bodyHeight={360}
        chart={
          shown &&
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
              {shown.nodes.length === 0 ? (
                <div className="panel-state empty-why">
                  <div>Nothing matches these filters.</div>
                </div>
              ) : (
                <ServiceMapGraph
                  map={shown}
                  roles={roles}
                  overlay={overlay.data?.services}
                  series={series.data?.edges}
                  focus={filters.focus}
                  onEdge={(e) => setPicked({ edge: e })}
                  onNode={(n) => setPicked({ node: n })}
                />
              )}
            </>
          ))
        }
        table={
          table && (
            <DataTable
              result={table}
              maxHeight={420}
              rowKey={(row) => `${row[8]}${row[7] === "stopped" ? ":gone" : ""}`}
              onRowClick={(row) => {
                const e = edgeAt(row[8]);
                if (e) setPicked({ edge: e });
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
                ...(compare ? [{ key: "change", label: "Rate vs before", align: "right" as const, width: 110, render: (v: unknown) => <span className={v === "stopped" ? "flag warn" : v === "new" ? "flag" : "dim"}>{String(v)}</span> }] : []),
              ]}
            />
          )
        }
      />
      {picked?.node && q.data && (
        <NodeDrawer
          node={picked.node}
          map={q.data}
          role={roles.get(picked.node.id)}
          health={overlay.data?.services[picked.node.name]}
          onClose={() => setPicked(null)}
          onTraces={() => openNodeTraces(picked.node!)}
          onFocus={() => {
            set({ focus: picked.node!.id });
            setPicked(null);
          }}
          onEdge={(e) => setPicked({ edge: e })}
        />
      )}
      {picked?.edge && q.data && <EdgeDrawer edge={picked.edge} map={q.data} onClose={() => setPicked(null)} onTraces={() => openEdgeTraces(picked.edge!)} onNode={(n) => setPicked({ node: n })} />}
    </>
  );
}

function Trend({ label, points, format }: { label: string; points: [number, number][]; format: (v: number) => string }) {
  const w = 300;
  const h = 44;
  const shown = points.filter((p) => Number.isFinite(p[1]));
  const last = shown.length ? shown[shown.length - 1][1] : null;
  let line = "";
  if (shown.length > 1) {
    const t0 = shown[0][0];
    const t1 = shown[shown.length - 1][0];
    const hi = Math.max(...shown.map((p) => p[1])) || 1;
    line = shown.map((p, i) => `${i ? "L" : "M"}${(((p[0] - t0) / (t1 - t0 || 1)) * w).toFixed(1)},${(h - 2 - (p[1] / hi) * (h - 4)).toFixed(1)}`).join("");
  }
  return (
    <div className="smap-trend">
      <div className="smap-trend-head">
        <span>{label}</span>
        <span className="dim">{last === null ? "—" : `last ${format(last)}`}</span>
      </div>
      {line ? (
        <svg viewBox={`0 0 ${w} ${h}`} width="100%" height={h} preserveAspectRatio="none" aria-hidden>
          <path d={line} className="smap-trend-line" />
        </svg>
      ) : (
        <div className="dim smap-trend-empty">Too few points to draw</div>
      )}
    </div>
  );
}

function Stat({ label, value, delta, upIsBad = true }: { label: string; value: string; delta?: number | null; upIsBad?: boolean }) {
  const text = changeText(delta ?? null, 0.05);
  const bad = delta != null && (upIsBad ? delta > 0 : delta < 0);
  return (
    <div className="smap-stat">
      <div className="dim">{label}</div>
      <div className="smap-stat-value">{value}</div>
      {text && <div className={bad ? "smap-tip-bad" : "dim"}>{text} vs before</div>}
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="smap-dsec">
      <h3>{title}</h3>
      {children}
    </section>
  );
}

const col = (r: Result | undefined, name: string) => (r ? r.columns.indexOf(name) : -1);

function points(r: Result | undefined, name: string, scale: (v: number) => number = (v) => v): [number, number][] {
  const t = col(r, "t");
  const v = col(r, name);
  if (!r || t < 0 || v < 0) return [];
  return r.rows.filter((row) => row[v] !== null).map((row) => [Number(row[t]), scale(Number(row[v]))]);
}

function EdgeList({ edges, map, side, onEdge }: { edges: MapEdge[]; map: ServiceMap; side: "source" | "target"; onEdge: (e: MapEdge) => void }) {
  const byId = new Map(map.nodes.map((n) => [n.id, n]));
  if (edges.length === 0) return <p className="dim">None in this range.</p>;
  return (
    <ul className="smap-list">
      {[...edges]
        .sort((a, b) => b.calls - a.calls)
        .map((e) => (
          <li key={edgeKey(e)}>
            <button type="button" className="btn ghost smap-list-btn" onClick={() => onEdge(e)}>
              <span className="mono">{nodeLabel(byId.get(e[side])) || e[side]}</span>
              <span className="dim">{edgeSummary(e)}</span>
            </button>
          </li>
        ))}
    </ul>
  );
}

function HealthList({ health }: { health: MapHealth | undefined }) {
  if (!health) return null;
  const hot = [
    health.cpu !== null && `CPU ${formatPercent(health.cpu)}`,
    health.memory !== null && `memory ${formatPercent(health.memory)}`,
    health.container_memory !== null && `container memory ${formatPercent(health.container_memory)} of its limit`,
  ].filter(Boolean);
  if (!health.incidents.length && !health.alerts.length && !hot.length) return null;
  return (
    <Section title="Health now">
      <ul className="smap-list plain">
        {health.incidents.map((i) => (
          <li key={`i${i.id}`}>
            <a href={incidentHref(i.id)}>Incident: {i.title}</a> <span className="dim">{i.severity}</span>
          </li>
        ))}
        {health.alerts.map((a) => (
          <li key={`a${a.id}`}>
            <a href={`#/alerts?rule=${a.id}`}>
              {a.slo ? "SLO burning" : "Firing"}: {a.name}
            </a>
          </li>
        ))}
        {hot.length > 0 && <li>Machines over the last five minutes: {hot.join(", ")}</li>}
      </ul>
    </Section>
  );
}

const ROLE_NOTE: Record<ErrorRole, string> = {
  origin: "Likely where failures start: it fails while none of its own calls do.",
  downstream: "Fails while calls it makes fail too, so it is likely passing those failures on. Look at the arrows it sends that fail.",
};

function NodeDrawer({
  node,
  map,
  role,
  health,
  onClose,
  onTraces,
  onFocus,
  onEdge,
}: {
  node: MapNode;
  map: ServiceMap;
  role?: ErrorRole;
  health?: MapHealth;
  onClose: () => void;
  onTraces: () => void;
  onFocus: () => void;
  onEdge: (e: MapEdge) => void;
}) {
  const svc = node.kind === "service";
  const d = useServiceDetail(svc ? node.name : null);
  const prev = map.previous?.nodes.find((n) => n.id === node.id);
  const failed = node.requests > 0 ? (100 * node.errors) / node.requests : 0;
  const prevFailed = prev && prev.requests > 0 ? (100 * prev.errors) / prev.requests : null;
  const callers = map.edges.filter((e) => e.target === node.id);
  const calls = map.edges.filter((e) => e.source === node.id);
  const r = d.data;
  const bucketMin = (r?.bucket ?? 60) / 60;

  return (
    <Drawer
      title={nodeLabel(node)}
      onClose={onClose}
      className="smap-drawer"
      actions={
        <>
          <button type="button" className="btn ghost" onClick={onFocus}>
            <Crosshair size={14} /> Focus
          </button>
          <button type="button" className="btn ghost" onClick={onTraces}>
            <ListTree size={14} /> Traces
          </button>
          {svc && (
            <a className="btn ghost" href={`#/applogs?q=${encodeURIComponent(`service:${node.name}`)}`}>
              <ScrollText size={14} /> Logs
            </a>
          )}
        </>
      }
    >
      {svc ? (
        <div className="smap-stats">
          <Stat label="Requests" value={formatCount(node.requests)} delta={change(node.requests, prev?.requests)} upIsBad={false} />
          <Stat label="Failed" value={formatPercent(failed)} delta={prevFailed === null ? null : change(failed, prevFailed)} />
          <Stat label="p95" value={node.p95 === null ? "—" : formatMs(node.p95)} delta={change(node.p95, prev?.p95)} />
        </div>
      ) : (
        <p className="dim">A {node.kind === "db" ? "database" : node.kind === "http" ? "HTTP host" : node.kind} your services call.</p>
      )}
      {role && <p className={role === "origin" ? "smap-tip-bad" : "smap-tip-warn"}>{ROLE_NOTE[role]}</p>}
      <HealthList health={health} />
      {svc && (
        <Section title="Over the range">
          {d.error ? (
            <p className="dim">{d.error.message}</p>
          ) : (
            <>
              <Trend label="Requests per minute" points={points(r?.trend, "requests", (v) => v / bucketMin)} format={(v) => formatCount(v)} />
              <Trend label="Failed requests" points={points(r?.trend, "errors")} format={(v) => formatCount(v)} />
              <Trend label="p95" points={points(r?.trend, "p95")} format={(v) => formatMs(v)} />
            </>
          )}
        </Section>
      )}
      <Section title={`Called by (${callers.length})`}>
        <EdgeList edges={callers} map={map} side="source" onEdge={onEdge} />
      </Section>
      {svc && (
        <Section title={`Calls (${calls.length})`}>
          <EdgeList edges={calls} map={map} side="target" onEdge={onEdge} />
        </Section>
      )}
      {svc && r && r.routes.rows.length > 0 && (
        <Section title="Slowest routes">
          <DataTable
            result={r.routes}
            rowKey={(row) => String(row[0])}
            columns={[
              { key: "route", label: "Route", render: (v) => <span className="mono">{String(v)}</span> },
              { key: "requests", align: "right", unit: "count", width: 80 },
              { key: "errors", label: "Failed", align: "right", unit: "count", width: 70 },
              { key: "p95", align: "right", width: 80, render: (v) => (toNumber(v) === null ? "—" : formatMs(toNumber(v)!)) },
            ]}
          />
        </Section>
      )}
      {svc && r && r.errors.rows.length > 0 && (
        <Section title="Commonest errors">
          <ul className="smap-list plain">
            {r.errors.rows.map((row) => (
              <li key={String(row[0])}>
                <a href={`#/errors?fp=${encodeURIComponent(String(row[0]))}`}>
                  <span className="mono">{String(row[1])}</span> {String(row[2] ?? "")}
                </a>{" "}
                <span className="dim">
                  {formatCount(Number(row[3]))}× · last {formatRelative(String(row[4]))}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}
      {svc && ((r && r.deploys.rows.length > 0) || (health?.deploys.length ?? 0) > 0) && (
        <Section title="Deploys in the range">
          <ul className="smap-list plain">
            {(r?.deploys.rows ?? health!.deploys.map((x) => [x.version, x.at])).map((row) => (
              <li key={String(row[0])}>
                <span className="mono">{String(row[0])}</span> <span className="dim">{formatRelative(String(row[1]))}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}
      {svc && r && r.instances.rows.length > 0 && (
        <Section title="Instances">
          <ul className="smap-list plain">
            {r.instances.rows.map((row) => (
              <li key={String(row[0])}>
                <a href={`#/system?instance=${encodeURIComponent(String(row[0]))}`} className="mono">
                  {String(row[0])}
                </a>{" "}
                <span className="dim">seen {formatRelative(String(row[1]))}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}
    </Drawer>
  );
}

function EdgeDrawer({ edge, map, onClose, onTraces, onNode }: { edge: MapEdge; map: ServiceMap; onClose: () => void; onTraces: () => void; onNode: (n: MapNode) => void }) {
  const byId = new Map(map.nodes.map((n) => [n.id, n]));
  const a = byId.get(edge.source);
  const b = byId.get(edge.target);
  const d = useEdgeDetail(edge);
  const prev = map.previous?.edges.find((e) => edgeKey(e) === edgeKey(edge));
  const r = d.data;
  const bucketMin = (r?.bucket ?? 60) / 60;
  const src = a?.name ?? "";
  const callee = b?.name ?? "";

  return (
    <Drawer
      title={`${nodeLabel(a)} → ${nodeLabel(b)}`}
      onClose={onClose}
      className="smap-drawer"
      actions={
        <button type="button" className="btn ghost" onClick={onTraces}>
          <ListTree size={14} /> Traces
        </button>
      }
    >
      <div className="smap-stats">
        <Stat label="Calls" value={rate(edge.per_min)} delta={change(edge.per_min, prev?.per_min)} upIsBad={false} />
        <Stat label="Failed" value={formatPercent(edge.error_rate)} delta={prev ? change(edge.error_rate, prev.error_rate) : null} />
        <Stat label="p95" value={edge.p95 === null ? "—" : formatMs(edge.p95)} delta={change(edge.p95, prev?.p95)} />
      </div>
      <Section title="Over the range">
        {d.error ? (
          <p className="dim">{d.error.message}</p>
        ) : (
          <>
            <Trend label="Calls per minute" points={points(r?.series, "calls", (v) => v / bucketMin)} format={(v) => formatCount(v)} />
            <Trend label="Failed calls" points={points(r?.series, "errors")} format={(v) => formatCount(v)} />
            <Trend label="p95" points={points(r?.series, "p95")} format={(v) => formatMs(v)} />
          </>
        )}
      </Section>
      {edge.kind === "service" && (
        <Section title={`Routes of ${nodeLabel(b)} it calls`}>
          {r && r.routes.rows.length > 0 ? (
            <DataTable
              result={r.routes}
              rowKey={(row) => String(row[0])}
              columns={[
                { key: "route", label: "Route", render: (v) => <span className="mono">{String(v) || "—"}</span> },
                { key: "calls", align: "right", unit: "count", width: 80 },
                { key: "errors", label: "Failed", align: "right", unit: "count", width: 70 },
                { key: "p95", align: "right", width: 80, render: (v) => (toNumber(v) === null ? "—" : formatMs(toNumber(v)!)) },
              ]}
            />
          ) : (
            <p className="dim">{d.loading ? "Loading…" : "No routes recorded for these calls."}</p>
          )}
        </Section>
      )}
      <Section title="Alert on this arrow">
        <div className="smap-actions">
          <a className="btn ghost" href={alertOnEdgeHref(src, callee, "edge_error_rate")}>
            <Bell size={14} /> When calls fail
          </a>
          <a className="btn ghost" href={alertOnEdgeHref(src, callee, "edge_p95")}>
            <Bell size={14} /> When calls slow down
          </a>
          <a className="btn ghost" href={alertOnEdgeHref(src, callee, "edge_rate")}>
            <Bell size={14} /> When calls stop
          </a>
        </div>
      </Section>
      <div className="smap-actions">
        {a && (
          <button type="button" className="btn ghost" onClick={() => onNode(a)}>
            {nodeLabel(a)}
          </button>
        )}
        {b && (
          <button type="button" className="btn ghost" onClick={() => onNode(b)}>
            {nodeLabel(b)}
          </button>
        )}
      </div>
    </Drawer>
  );
}
