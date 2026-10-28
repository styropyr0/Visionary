import { useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, CheckCircle2, MonitorSmartphone, XCircle } from "lucide-react";
import type { Result, StatsResponse, Viz } from "../lib/api";
import { formatCount, formatMs, formatPercent, relabelColumn, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { formatFull, formatLogTime, formatRelative, previousLabel } from "../lib/time";
import {
  RATING_LABEL,
  VITALS,
  filterSeries,
  firstRow,
  formatVital,
  pagePath,
  rate,
  useRum,
  useRumError,
  vitalsByName,
  type Rating,
  type RumApp,
  type VitalInfo,
  type VitalSummary,
} from "../lib/rum";
import { DataTable, type Column } from "../components/DataTable";
import { Drawer } from "../components/Modal";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { Select } from "../components/Select";
import { StatTile } from "../components/StatTile";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { ErrorNotice, linkify } from "../components/ErrorNotice";
import { InfoTip } from "../components/InfoTip";
import { SessionsPanel } from "./Frontend.sessions";
import "./Frontend.css";
import { SetupCard, SetupCommand, SetupHelpButton, SetupOption } from "../components/SetupCard";

type Q<T = Result> = { data?: T; loading: boolean; error?: Error };

const VIZ = {
  loadingVitals: { type: "timeseries", x: "t", y: ["value"], series: "series", unit: "ms" },
  cls: { type: "timeseries", x: "t", y: ["value"], series: "series", unit: "none" },
  sessions: { type: "timeseries", x: "t", y: ["sessions", "users"], unit: "count" },
  errors: { type: "timeseries", x: "t", y: ["value"], series: "series", stack: true, unit: "count" },
  screens: { type: "bar", x: "label", y: ["p75"], unit: "ms" },
  api: { type: "bar", x: "endpoint", y: ["client_p75", "server_p75"], unit: "ms" },
  breakdown: { type: "bar", x: "key", y: ["sessions"], unit: "count" },
} satisfies Record<string, Viz>;

const APPS = [
  { value: "", label: "All apps" },
  { value: "web", label: "Web" },
  { value: "android", label: "Android" },
  { value: "ios", label: "iOS" },
];

/** An app id ("web", "ios") as its label. */
const appLabel = (v: unknown) => APPS.find((a) => a.value === v)?.label ?? String(v);

const BREAKDOWNS = [
  { value: "app", label: "By app", column: "App" },
  { value: "app_version", label: "By app version", column: "App version" },
  { value: "device", label: "By device", column: "Device" },
  { value: "os", label: "By OS", column: "OS" },
  { value: "country", label: "By country", column: "Country" },
];

export function Frontend() {
  const { range } = useApp();
  const [app, setApp] = useState<RumApp>("");
  const [by, setBy] = useState("app");
  const [openError, setOpenError] = useState<string | null>(null);

  const web = app === "" || app === "web";
  const mobile = app !== "web";

  const summary = useRum<StatsResponse>("summary", app);
  const vitals = useRum<StatsResponse>("vitals", app);
  const vitalSeries = useRum("vitals_series", app);
  const pages = useRum("pages", app);
  const screens = useRum("screens", app);
  const apiLatency = useRum("api", app);
  const errors = useRum("errors", app);
  const errorSeries = useRum("errors_series", app);
  const sessions = useRum("sessions", app);
  const breakdown = useRum("breakdown", app, { by });

  const busy = [summary, vitals, vitalSeries, pages, screens, apiLatency, errors, errorSeries, sessions, breakdown].some((q) => q.loading);
  const cur = firstRow(summary.data?.current);
  const prev = firstRow(summary.data?.previous);
  const prevLbl = previousLabel(range);
  const delta = (a: number | null, b: number | null) => (a === null || b === null || b === 0 ? null : (a - b) / b);
  const n = (o: Record<string, number> | null, k: string) => o?.[k] ?? null;
  const errorFree = (o: Record<string, number> | null) => (o && o.sessions > 0 ? 100 - (100 * (o.error_sessions ?? 0)) / o.sessions : null);
  const apiFail = (o: Record<string, number> | null) => (o && o.api_calls > 0 ? (100 * (o.api_failures ?? 0)) / o.api_calls : null);

  const vitalNow = useMemo(() => vitalsByName(vitals.data?.current), [vitals.data]);
  const vitalBefore = useMemo(() => vitalsByName(vitals.data?.previous), [vitals.data]);
  const loadingVitals = useMemo(() => filterSeries(vitalSeries.data, (s) => s !== "CLS"), [vitalSeries.data]);
  const clsSeries = useMemo(() => filterSeries(vitalSeries.data, (s) => s === "CLS"), [vitalSeries.data]);
  const screenRows = useMemo(() => withScreenLabel(screens.data, app === ""), [screens.data, app]);
  const apiChart = useMemo(() => apiChartRows(apiLatency.data), [apiLatency.data]);
  const appParam: Record<string, string> = app ? { app } : {};
  const breakdownRows = useMemo(() => withKey(breakdown.data, by), [breakdown.data, by]);
  const errorsByApp = useMemo(() => errorSeries.data && relabelColumn(errorSeries.data, "series", appLabel), [errorSeries.data]);

  const empty =
    !summary.loading && cur !== null && !cur.sessions && !cur.views && !cur.errors && !cur.api_calls;

  return (
    <>
      <TopBar
        title="Frontend"
        section="Monitor"
        actions={
          <>
            <Select label="App" icon={<MonitorSmartphone size={14} />} value={app} onChange={(v) => setApp(v as RumApp)} options={APPS} />
            <TimeRangePicker />
            <RefreshControl busy={busy} />
          </>
        }
      />
      <div className="content">
        {summary.error && !summary.data && (
          <ErrorNotice title="Could not load frontend data" error={summary.error} style={{ marginBottom: 12 }} />
        )}
        {empty ? (
          <FrontendSetup app={app} />
        ) : (
          <div className="grid">
            <StatTile
              label="Sessions"
              description="Browser tabs and app launches that reported in the range."
              value={cur ? formatCount(cur.sessions ?? 0) : "—"}
              delta={delta(n(cur, "sessions"), n(prev, "sessions"))}
              deltaLabel={prevLbl}
              upIs="neutral"
              loading={summary.loading}
            />
            <StatTile
              label="Users"
              description="Distinct signed-in users (set with setUser or user_id)."
              value={cur ? formatCount(cur.users ?? 0) : "—"}
              delta={delta(n(cur, "users"), n(prev, "users"))}
              deltaLabel={prevLbl}
              upIs="neutral"
              loading={summary.loading}
            />
            <StatTile
              label="Page and screen views"
              description="Web page loads (including single-page app route changes) and mobile screens."
              value={cur ? formatCount(cur.views ?? 0) : "—"}
              delta={delta(n(cur, "views"), n(prev, "views"))}
              deltaLabel={prevLbl}
              upIs="neutral"
              loading={summary.loading}
            />
            <StatTile
              label="Error-free sessions"
              description="Sessions in which no client error was reported."
              value={errorFree(cur) === null ? "—" : formatPercent(errorFree(cur)!)}
              delta={delta(errorFree(cur), errorFree(prev))}
              deltaLabel={prevLbl}
              upIs="good"
              loading={summary.loading}
            />
            <StatTile
              label="Client errors"
              description="JavaScript errors, unhandled rejections and mobile crashes or captured exceptions."
              value={cur ? formatCount(cur.errors ?? 0) : "—"}
              delta={delta(n(cur, "errors"), n(prev, "errors"))}
              deltaLabel={prevLbl}
              upIs="bad"
              loading={summary.loading}
              footer={cur ? `${formatCount(cur.error_sessions ?? 0)} sessions affected` : undefined}
            />
            <StatTile
              label="Failed API calls"
              description="Client calls that got a 5xx or never reached the server (network failure), as a share of all client API calls."
              value={apiFail(cur) === null ? "—" : formatPercent(apiFail(cur)!)}
              delta={delta(apiFail(cur), apiFail(prev))}
              deltaLabel={prevLbl}
              upIs="bad"
              loading={summary.loading}
              footer={cur ? `${formatCount(cur.api_failures ?? 0)} of ${formatCount(cur.api_calls ?? 0)} calls` : undefined}
            />

            {web && (
              <>
                <Section
                  title="Web Vitals"
                  text="The 75th percentile of real page loads, rated with Google's thresholds: a page passes when at least three in four visits are good. The bars show how all visits split."
                />
                <div className="span-12 rum-vitals">
                  {VITALS.map((v) => (
                    <VitalTile
                      key={v.name}
                      info={v}
                      now={vitalNow.get(v.name)}
                      before={vitalBefore.get(v.name)}
                      loading={vitals.loading}
                      error={vitals.error}
                      prevLabel={prevLbl}
                    />
                  ))}
                </div>
                <ChartPanel
                  className="span-8"
                  title="Loading and responsiveness, p75"
                  description="LCP, FCP and TTFB measure loading; INP measures how fast the page reacts. One line per vital."
                  q={{ ...vitalSeries, data: loadingVitals }}
                  viz={VIZ.loadingVitals}
                  height={240}
                  empty="No Web Vitals in this range. They come from the browser SDK."
                />
                <ChartPanel
                  className="span-4"
                  title="Layout shift (CLS), p75"
                  description="Unitless. Good is 0.1 or less; above 0.25 is poor."
                  q={{ ...vitalSeries, data: clsSeries }}
                  viz={VIZ.cls}
                  height={240}
                  empty="No layout-shift data in this range. Safari and Firefox do not report CLS."
                />
                <Panel
                  className="span-12"
                  title="Slowest pages by LCP"
                  description="Pages by the 75th percentile of Largest Contentful Paint, with the other vitals and the full load time. Ids in paths are grouped as :id."
                  result={pages.data}
                  loading={pages.loading}
                  error={pages.error}
                  bodyHeight={160}
                  table={
                    pages.data &&
                    (pages.data.rows.length === 0 ? (
                      <div className="panel-state">No page loads with LCP in this range.</div>
                    ) : (
                      <DataTable result={pages.data} columns={pageColumns} maxHeight={420} />
                    ))
                  }
                />
              </>
            )}

            {mobile && (
              <>
                <Section
                  title="Mobile screens"
                  text="Time to first draw for each screen or activity, from the Android and iOS SDKs. Slow screens on low-end devices show up in the device breakdown below."
                />
                <ChartPanel
                  className="span-12"
                  title="Slowest screens, p75"
                  description="The 75th percentile of time to first draw per screen."
                  q={{ ...screens, data: screenRows }}
                  src={{ ref: "rum/screens", params: appParam, viz: { ...VIZ.screens, x: "screen" } }}
                  viz={VIZ.screens}
                  height={260}
                  empty="No screen views in this range. They come from the Android and iOS SDKs."
                  table={screenRows && <DataTable result={screenRows} columns={screenColumns} maxHeight={260} />}
                />
              </>
            )}

            <Section
              title="API latency as users feel it"
              text="Client p75 is measured in the app or browser, so it includes the network, TLS, queuing and the device. Server p75 is what the API itself logged for the same route. The gap is time users wait that the server never sees."
            />
            <ChartPanel
              className="span-12"
              title="Client and server p75 per endpoint"
              description="The 12 endpoints clients call most. Endpoints are matched to server routes by method and path, with ids as :id."
              q={{ ...apiLatency, data: apiChart }}
              viz={VIZ.api}
              height={320}
              empty="No client API calls in this range."
              table={apiLatency.data && <DataTable result={apiLatency.data} columns={apiColumns(apiLatency.data)} maxHeight={420} />}
            />

            <Section
              title="Errors"
              text="Client errors grouped by fingerprint: the same error type thrown from the same place in your code is one group, across releases and build hashes."
            />
            <Panel
              className="span-8"
              title="Error groups"
              description="Open a group to see its latest occurrences with stack traces."
              result={errors.data}
              loading={errors.loading}
              error={errors.error}
              bodyHeight={200}
              table={
                errors.data &&
                (errors.data.rows.length === 0 ? (
                  <div className="panel-state">No client errors in this range.</div>
                ) : (
                  <DataTable
                    result={errors.data}
                    columns={errorColumns}
                    maxHeight={360}
                    onRowClick={(row) => setOpenError(String(row[0]))}
                    rowKey={(row) => row[0]}
                    selectedKey={openError ?? undefined}
                  />
                ))
              }
            />
            <ChartPanel className="span-4" title="Errors by app" q={{ ...errorSeries, data: errorsByApp }} src={{ ref: "rum/errors_series", params: appParam }} viz={VIZ.errors} height={200} empty="No client errors in this range." />

            <Section title="Sessions and users" />
            <ChartPanel
              className="span-6"
              title="Sessions and users"
              description="Distinct sessions and signed-in users per interval."
              q={sessions}
              src={{ ref: "rum/sessions", params: appParam }}
              viz={VIZ.sessions}
              height={260}
            />
            <ChartPanel
              className="span-6"
              title="Breakdown"
              description="Sessions per group. The table adds users, views, errors, the share of sessions with an error and the p75 page or screen load."
              q={{ ...breakdown, data: breakdownRows }}
              src={{ ref: "rum/breakdown", params: { ...appParam, by }, viz: { ...VIZ.breakdown, x: by } }}
              viz={VIZ.breakdown}
              height={260}
              table={breakdown.data && <DataTable result={breakdown.data} columns={breakdownColumns(by)} maxHeight={260} />}
              actions={<Select label="Breakdown" value={by} onChange={setBy} options={BREAKDOWNS} maxWidth={170} />}
            />
            <SessionsPanel app={app} />
          </div>
        )}
      </div>
      {openError && <ErrorDrawer fingerprint={openError} onClose={() => setOpenError(null)} />}
    </>
  );
}

function FrontendSetup({ app }: { app: RumApp }) {
  const name = APPS.find((a) => a.value === app)?.label;
  return (
    <SetupCard
      icon={<MonitorSmartphone size={20} />}
      title={app ? `No ${name} data in this range` : "No frontend data yet"}
      text="Web Vitals, page and screen loads, client errors and API latency as your users see them, from your web and mobile apps."
      topic="client"
    >
      <SetupOption title="Web apps" text="Add the browser SDK with a client key (Settings → API keys).">
        <SetupCommand
          code={`<script src="https://unpkg.com/visionary-browser/dist/visionary.min.js"></script>
<script>
  Visionary.init({ endpoint: "${location.origin}", key: "pk_…" });
</script>`}
        />
      </SetupOption>
      <SetupOption title="Android and iOS apps" text="Add the mobile SDK with the same kind of client key; screens and errors show up here per device.">
        <SetupHelpButton topic="client">Mobile setup</SetupHelpButton>
      </SetupOption>
    </SetupCard>
  );
}

const RATING_ICON = { good: CheckCircle2, "needs-improvement": AlertTriangle, poor: XCircle } as const;
const RATING_SHORT: Record<Rating, string> = { good: "Good", "needs-improvement": "NI", poor: "Poor" };

function VitalTile({
  info,
  now,
  before,
  loading,
  error,
  prevLabel,
}: {
  info: VitalInfo;
  now?: VitalSummary;
  before?: VitalSummary;
  loading: boolean;
  error?: Error;
  prevLabel: string;
}) {
  const p75 = now?.p75 ?? null;
  const rating = rate(info.name, p75);
  const total = now ? now.good + now.needs + now.poor : 0;
  const share = (x: number) => (total ? (100 * x) / total : 0);
  const parts: { key: Rating; value: number }[] = now
    ? [
        { key: "good", value: share(now.good) },
        { key: "needs-improvement", value: share(now.needs) },
        { key: "poor", value: share(now.poor) },
      ]
    : [];
  const text = formatVital(info.name, p75);
  const [num, unit] = info.name === "CLS" ? [text, ""] : splitValue(text);
  const limits = info.name === "CLS" ? [String(info.good), String(info.poor)] : [formatMs(info.good), formatMs(info.poor)];
  const Icon = rating ? RATING_ICON[rating] : null;

  return (
    <section className="panel rum-vital" aria-busy={loading || undefined}>
      {loading && <div className="progress" aria-hidden />}
      <div className={`stat ${loading ? "refetching" : ""}`}>
        <div className="stat-label">
          <span className="rum-vital-name">{info.name}</span>
          <span className="rum-vital-full">{info.label}</span>
          <InfoTip text={`${info.help} Good up to ${limits[0]}, poor above ${limits[1]}.`} size={12} />
        </div>
        <div className="stat-value">
          {num}
          {unit && <span className="stat-unit">{unit}</span>}
        </div>
        {rating && Icon ? (
          <span className={`rum-rating ${rating}`}>
            <Icon size={13} aria-hidden />
            {RATING_LABEL[rating]}
          </span>
        ) : (
          <span className="rum-rating none">{error ? "Could not load" : loading ? "Loading…" : "No samples in this range"}</span>
        )}
        {total > 0 && (
          <>
            <div className="rum-dist" role="img" aria-label={parts.map((p) => `${RATING_LABEL[p.key]} ${formatPercent(p.value)}`).join(", ")}>
              {parts.map(
                (p) => p.value > 0 && <span key={p.key} className={p.key} style={{ width: `${p.value}%` }} title={`${RATING_LABEL[p.key]}: ${formatPercent(p.value)}`} />,
              )}
            </div>
            <div className="rum-dist-legend">
              {parts.map((p) => (
                <span key={p.key} title={RATING_LABEL[p.key]}>
                  <i className={p.key} aria-hidden />
                  {RATING_SHORT[p.key]} {Math.round(p.value)}%
                </span>
              ))}
            </div>
          </>
        )}
        <div className="rum-vital-foot">
          {now ? `${formatCount(now.samples)} samples` : " "}
          {before?.p75 != null && p75 !== null && ` · ${formatVital(info.name, before.p75)} ${prevLabel}`}
        </div>
      </div>
    </section>
  );
}

function splitValue(text: string): [string, string] {
  const m = text.match(/^(-?[\d.,]+)\s*(.*)$/);
  return m ? [m[1], m[2]] : [text, ""];
}

function ChartPanel({
  q,
  viz,
  height,
  title,
  description,
  className,
  empty,
  table,
  actions,
  src,
}: {
  q: Q;
  viz: Viz;
  height: number;
  title: string;
  description?: string;
  className: string;
  empty?: string;
  table?: ReactNode;
  actions?: ReactNode;
  /** Endpoint under /api and its parameters, so a dashboard can show the chart too; viz when the endpoint's columns differ from the chart's. */
  src?: { ref: string; params: Record<string, string>; viz?: Viz };
}) {
  const tokens = useChartTokens();
  return (
    <Panel
      className={className}
      title={title}
      description={description}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={height}
      note={vizNote(q.data, viz, tokens)}
      extraActions={actions}
      chart={q.data && <VizView result={q.data} viz={viz} height={height} title={title} empty={empty} />}
      table={table ?? (q.data && <DataTable result={q.data} maxHeight={height} defaultUnit={viz.unit} />)}
      dashboard={src && { kind: "builtin", ref: src.ref, params: src.params, viz: src.viz ?? viz }}
    />
  );
}

function ErrorDrawer({ fingerprint, onClose }: { fingerprint: string; onClose: () => void }) {
  const q = useRumError(fingerprint);
  const rows = useMemo(() => {
    const res = q.data;
    if (!res) return [];
    return res.rows.map((r) => Object.fromEntries(res.columns.map((c, i) => [c, r[i]])) as Record<string, unknown>);
  }, [q.data]);
  const latest = rows[0];
  const s = (v: unknown) => (v === null || v === undefined || v === "" ? "—" : String(v));
  return (
    <Drawer
      onClose={onClose}
      title={
        <span style={{ display: "inline-flex", gap: 10, alignItems: "center" }}>
          <span className="mono">{latest ? s(latest.name) : "Error"}</span>
          <span className="dim mono" style={{ fontSize: 12 }}>
            {fingerprint}
          </span>
        </span>
      }
    >
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {!q.data && !q.error && <div className="panel-state">Loading…</div>}
      {q.data && rows.length === 0 && <div className="panel-state">No occurrences in this time range.</div>}
      {q.data && latest && (
        <>
          <p className="rum-error-message">{s(latest.message)}</p>
          <dl className="kv">
            <dt>Latest</dt>
            <dd>{formatFull(new Date(String(latest.created_at)))}</dd>
            <dt>App</dt>
            <dd>
              {latest.app ? appLabel(latest.app) : "—"} {latest.app_version ? s(latest.app_version) : ""}
              {latest.version ? <span className="dim mono"> · build {s(latest.version)}</span> : null}
            </dd>
            <dt>Device</dt>
            <dd>
              {s(latest.device)} · {s(latest.os)}
              {latest.country ? ` · ${s(latest.country)}` : ""}
            </dd>
            <dt>Page</dt>
            <dd className="mono">{s(latest.url)}</dd>
            <dt>Session</dt>
            <dd className="mono">
              {s(latest.session_id)}
              {latest.user_id != null ? <span className="dim"> · user #{s(latest.user_id)}</span> : null}
            </dd>
          </dl>
          <h3 className="rum-drawer-h">Stack trace</h3>
          {latest.stack ? (
            <pre className="code mono rum-stack">{String(latest.stack)}</pre>
          ) : (
            <p className="dim" style={{ margin: "0 0 20px" }}>No stack was sent with this error.</p>
          )}
          <h3 className="rum-drawer-h">Latest {rows.length} occurrences</h3>
          <DataTable
            result={{ ...q.data, columns: ["created_at", "app", "app_version", "device", "url"], rows: rows.map((r) => [r.created_at, r.app ? appLabel(r.app) : r.app, r.app_version, r.device, r.url]) }}
            maxHeight={260}
          />
        </>
      )}
    </Drawer>
  );
}

/* ---------- derived results ---------- */

/** Screens are labelled with their app when several apps are shown. */
function withScreenLabel(res: Result | undefined, withApp: boolean): Result | undefined {
  if (!res) return undefined;
  const ai = res.columns.indexOf("app");
  const si = res.columns.indexOf("screen");
  return { ...res, columns: ["label", ...res.columns], rows: res.rows.map((r) => [withApp ? `${r[si]} (${r[ai]})` : r[si], ...r]) };
}

/** The busiest endpoints, client p75 next to server p75. */
function apiChartRows(res: Result | undefined): Result | undefined {
  if (!res) return undefined;
  const i = (c: string) => res.columns.indexOf(c);
  return {
    ...res,
    columns: ["endpoint", "client_p75", "server_p75"],
    rows: res.rows.slice(0, 12).map((r) => [r[i("endpoint")], r[i("p75")], r[i("server_p75")]]),
  };
}

function withKey(res: Result | undefined, by: string): Result | undefined {
  if (!res) return undefined;
  const ki = res.columns.indexOf(by);
  return { ...res, columns: ["key", ...res.columns], rows: res.rows.map((r) => [by === "app" ? appLabel(r[ki]) : r[ki], ...r]) };
}

/* ---------- table columns ---------- */

function msCell(v: unknown) {
  const n = toNumber(v);
  return n === null ? <span className="dim">—</span> : formatMs(n);
}

function VitalCell({ name, value }: { name: string; value: unknown }) {
  const n = toNumber(value);
  const r = rate(name, n);
  if (n === null || !r) return <span className="dim">—</span>;
  return (
    <span className={`rum-cell ${r}`} title={RATING_LABEL[r]}>
      <span className="dot" aria-label={RATING_LABEL[r]} />
      {formatVital(name, n)}
    </span>
  );
}

const pageColumns: Column[] = [
  {
    key: "url",
    label: "Page",
    render: (v) => (
      <span className="mono" title={String(v)}>
        {pagePath(String(v))}
      </span>
    ),
  },
  { key: "loads", label: "Page loads", align: "right", unit: "count" },
  { key: "lcp_p75", label: "LCP p75", align: "right", render: (v) => <VitalCell name="LCP" value={v} /> },
  { key: "inp_p75", label: "INP p75", align: "right", render: (v) => <VitalCell name="INP" value={v} /> },
  { key: "cls_p75", label: "CLS p75", align: "right", render: (v) => <VitalCell name="CLS" value={v} /> },
  { key: "load_p75", label: "Load p75", align: "right", render: msCell },
];

const screenColumns: Column[] = [
  { key: "app", render: (v) => <span className="pill">{appLabel(v)}</span>, width: 90 },
  { key: "screen", render: (v) => <span className="mono">{String(v)}</span> },
  { key: "views", align: "right", unit: "count" },
  { key: "p50", align: "right", render: msCell },
  { key: "p75", align: "right", render: msCell },
  { key: "p95", align: "right", render: msCell },
];

function apiColumns(res: Result): Column[] {
  const gi = res.columns.indexOf("gap_ms");
  const max = Math.max(1, ...res.rows.map((r) => toNumber(r[gi]) ?? 0));
  return [
    {
      key: "endpoint",
      render: (v) => {
        const [method, ...path] = String(v).split(" ");
        return (
          <span>
            <span className="method">{method}</span>
            <span className="mono">{path.join(" ")}</span>
          </span>
        );
      },
    },
    { key: "calls", align: "right", unit: "count" },
    { key: "p50", label: "Client p50", align: "right", render: msCell },
    { key: "p75", label: "Client p75", align: "right", render: msCell },
    { key: "p95", label: "Client p95", align: "right", render: msCell },
    {
      key: "server_p75",
      label: "Server p75",
      align: "right",
      render: (v) =>
        toNumber(v) === null ? (
          <span className="dim" title="No api_logs rows for this route in the range">
            not matched
          </span>
        ) : (
          formatMs(toNumber(v)!)
        ),
    },
    {
      key: "gap_ms",
      label: "Network and client",
      align: "right",
      render: (v) => {
        const n = toNumber(v);
        if (n === null) return <span className="dim">—</span>;
        return (
          <span className="cell-bar">
            {n > 0 ? "+" : ""}
            {formatMs(n)}
            <span className="track" aria-hidden>
              <span className="fill" style={{ display: "block", width: `${Math.max(0, (100 * n) / max)}%` }} />
            </span>
          </span>
        );
      },
    },
    { key: "server_requests", label: "Server requests", align: "right", unit: "count" },
    {
      key: "failure_rate",
      label: "Failed",
      align: "right",
      render: (v) => {
        const n = toNumber(v);
        if (n === null) return <span className="dim">—</span>;
        const cls = n >= 5 ? "bad" : n >= 1 ? "warn" : "";
        return <span className={`flag ${cls}`}>{n === 0 ? <span className="dim">0%</span> : formatPercent(n)}</span>;
      },
    },
    { key: "network_failures", label: "Network failures", align: "right", unit: "count" },
  ];
}

const errorColumns: Column[] = [
  {
    key: "type",
    label: "Error",
    render: (v, row) => (
      <span className="rum-error-cell">
        <span className="mono">{String(v)}</span>
        <span className="dim">{String(row[2] ?? "")}</span>
      </span>
    ),
  },
  {
    key: "app",
    render: (v, row) => {
      const apps = toNumber(row[7]) ?? 1;
      return <span className="pill">{apps > 1 ? `${appLabel(v)} +${apps - 1}` : appLabel(v)}</span>;
    },
    width: 90,
  },
  { key: "app_version", label: "Version", render: (v) => <span className="mono dim">{v == null ? "—" : String(v)}</span>, width: 80 },
  { key: "events", align: "right", unit: "count" },
  { key: "sessions", align: "right", unit: "count" },
  { key: "users", align: "right", unit: "count" },
  {
    key: "last_seen",
    label: "Last seen",
    align: "right",
    render: (v) => (
      <span className="dim" title={formatLogTime(String(v))}>
        {formatRelative(String(v))}
      </span>
    ),
    width: 100,
  },
];

function breakdownColumns(by: string): Column[] {
  return [
    { key: by, label: BREAKDOWNS.find((b) => b.value === by)?.column, render: (v) => <span>{by === "app" ? appLabel(v) : String(v)}</span> },
    { key: "sessions", align: "right", unit: "count" },
    { key: "users", align: "right", unit: "count" },
    { key: "views", align: "right", unit: "count" },
    { key: "errors", align: "right", unit: "count" },
    {
      key: "error_session_percent",
      label: "Sessions with errors",
      align: "right",
      render: (v) => (toNumber(v) === null ? <span className="dim">—</span> : formatPercent(toNumber(v)!)),
    },
    { key: "load_p75", label: "Load p75", align: "right", render: msCell },
  ];
}
