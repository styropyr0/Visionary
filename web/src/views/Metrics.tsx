import { useEffect, useMemo, useState } from "react";
import { ChartLine, Plus, Search, X } from "lucide-react";
import type { Result, Viz } from "../lib/api";
import { formatCount, formatValue } from "../lib/format";
import { formatRelative } from "../lib/time";
import {
  AGG_LABEL,
  aggsFor,
  chartUnit,
  queryParams,
  useMetricLabels,
  useMetricNames,
  useMetricSeries,
  type MetricName,
  type MetricQuery,
} from "../lib/metrics";
import { DataTable, type Column } from "../components/DataTable";
import { ErrorNotice } from "../components/ErrorNotice";
import { Panel } from "../components/Panel";
import { Select } from "../components/Select";
import { Combobox } from "../components/Combobox";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { SetupCard, SetupCommand, SetupHelpButton, SetupOption } from "../components/SetupCard";
import "./Metrics.css";

/** The chart's state lives in the hash (#/metrics?name=…&agg=…) so it can be linked. */
interface MetricsState extends MetricQuery {
  service: string;
}

function readState(): MetricsState {
  const p = new URLSearchParams(window.location.hash.split("?")[1] ?? "");
  let labels: Record<string, string> | undefined;
  try {
    labels = JSON.parse(p.get("labels") ?? "null") ?? undefined;
  } catch {
    labels = undefined;
  }
  return { name: p.get("name") ?? "", agg: p.get("agg") ?? "", group: p.get("group") ?? "", labels, service: p.get("service") ?? "" };
}

function writeState(s: MetricsState) {
  const p = new URLSearchParams();
  if (s.name) p.set("name", s.name);
  if (s.agg) p.set("agg", s.agg);
  if (s.group) p.set("group", s.group);
  if (s.labels && Object.keys(s.labels).length) p.set("labels", JSON.stringify(s.labels));
  if (s.service) p.set("service", s.service);
  const qs = p.toString();
  window.location.hash = `#/metrics${qs ? `?${qs}` : ""}`;
}

function useMetricsState(): [MetricsState, (patch: Partial<MetricsState>) => void] {
  const [state, setState] = useState(readState);
  useEffect(() => {
    const on = () => setState(readState());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return [state, (patch) => writeState({ ...state, ...patch })];
}

const KIND_LABEL: Record<string, string> = { counter: "Counter", gauge: "Gauge", histogram: "Histogram" };

export function Metrics() {
  const [state, set] = useMetricsState();
  const names = useMetricNames(state.service);
  const services = names.data?.services ?? [];
  const list = names.data?.metrics ?? [];
  const empty = names.data && list.length === 0 && !state.service;

  return (
    <>
      <TopBar
        title="Metrics"
        section="Monitor"
        actions={
          <>
            {services.length > 0 && (
              <Select
                label="Service"
                value={state.service}
                options={[{ value: "", label: "All services" }, ...services.map((s) => ({ value: s, label: s }))]}
                onChange={(service) => set({ service })}
                maxWidth={220}
              />
            )}
            <TimeRangePicker />
            <RefreshControl busy={names.loading} />
          </>
        }
      />
      <div className="content">
        {names.error && !names.data && <ErrorNotice title="Could not load metrics" error={names.error} />}
        {empty ? (
          <MetricsSetup />
        ) : (
          <div className="grid">
            <MetricList list={list} loading={names.loading} selected={state.name} onPick={(name) => set({ name, agg: "", group: "", labels: undefined })} />
            {state.name ? (
              <MetricChart state={state} set={set} info={list.find((m) => m.name === state.name)} />
            ) : (
              <section className="panel span-8 mt-pick">
                <div className="panel-state">
                  <div className="state-inner">
                    <ChartLine size={18} />
                    <span>Pick a metric to chart it over the time range, by label.</span>
                  </div>
                </div>
              </section>
            )}
          </div>
        )}
      </div>
    </>
  );
}

function MetricList({ list, loading, selected, onPick }: { list: MetricName[]; loading: boolean; selected: string; onPick: (name: string) => void }) {
  const [search, setSearch] = useState("");
  const result = useMemo<Result>(() => {
    const q = search.trim().toLowerCase();
    const rows = list.filter((m) => !q || m.name.toLowerCase().includes(q)).map((m) => [m.name, m.kind, m.series, m.value, m.last_seen, m]);
    return { columns: ["name", "kind", "series", "value", "last_seen", "metric"], rows, truncated: false, rows_read: 0, duration_ms: 0, sql: "", bucket: 0, from: "", to: "" };
  }, [list, search]);
  const columns: Column[] = [
    {
      key: "name",
      label: "Metric",
      render: (v, row) => {
        const m = row[5] as MetricName;
        return (
          <span className="mt-name" title={`Last reading ${formatRelative(m.last_seen)}`}>
            <span className="mono">{String(v)}</span>
            <span className="mt-kind">
              {KIND_LABEL[m.kind] ?? m.kind}
              {m.unit && m.unit !== "1" ? ` · ${m.unit}` : ""}
            </span>
            {!!m.dropped && <span className="mt-dropped" title="New label sets past 1000 per metric were dropped">{formatCount(m.dropped)} series dropped</span>}
          </span>
        );
      },
    },
    { key: "series", label: "Series", align: "right", unit: "count" },
    {
      key: "value",
      label: "Value",
      align: "right",
      render: (v, row) => {
        const m = row[5] as MetricName;
        if (v == null) return <span className="dim">—</span>;
        return <span title={m.kind === "counter" ? "Total in the range" : m.kind === "gauge" ? "Latest reading" : "Mean in the range"}>{formatValue(v, chartUnit(m.unit, ""))}</span>;
      },
    },
  ];
  return (
    <section className="panel span-4 mt-list" aria-busy={loading || undefined}>
      {loading && <div className="progress" aria-hidden />}
      <header className="panel-head">
        <h3 className="panel-title">Custom metrics</h3>
      </header>
      <div className="mt-list-body">
        <div className="filter-row">
          <label className="search">
            <Search size={14} />
            <input className="input" placeholder="Find a metric" value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Find a metric" />
          </label>
        </div>
        {list.length === 0 ? (
          <p className="dim mt-none">No metrics in this time range.</p>
        ) : (
          <DataTable result={result} columns={columns} maxHeight={640} rowKey={(r) => r[0]} selectedKey={selected || undefined} onRowClick={(r) => onPick(String(r[0]))} />
        )}
      </div>
    </section>
  );
}

function MetricChart({ state, set, info }: { state: MetricsState; set: (p: Partial<MetricsState>) => void; info?: MetricName }) {
  const tokens = useChartTokens();
  const labels = useMetricLabels(state.name, state.service);
  const kind = info?.kind ?? labels.data?.kind ?? "";
  const aggs = aggsFor(kind);
  const agg = state.agg && aggs.includes(state.agg) ? state.agg : aggs[0];
  const query: MetricQuery = { name: state.name, agg, group: state.group || undefined, labels: state.labels };
  const series = useMetricSeries(query, state.service);
  const unit = chartUnit(info?.unit ?? labels.data?.unit ?? "", agg);
  const viz: Viz = { type: "timeseries", x: "t", y: ["value"], series: "series", unit };

  const keys = labels.data?.labels ?? [];
  const groupOptions = [
    { value: "", label: "No grouping" },
    ...keys.map((k) => ({ value: k.key, label: `By ${k.key}` })),
    { value: ":service", label: "By service" },
    { value: ":instance", label: "By instance" },
  ];
  const filters = Object.entries(state.labels ?? {});
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const pickKey = key || keys.find((k) => !(state.labels ?? {})[k.key])?.key || "";
  const values = keys.find((k) => k.key === pickKey)?.values ?? [];
  const addFilter = () => {
    if (!pickKey || !value) return;
    set({ labels: { ...(state.labels ?? {}), [pickKey]: value } });
    setKey("");
    setValue("");
  };
  const removeFilter = (k: string) => {
    const next = { ...(state.labels ?? {}) };
    delete next[k];
    set({ labels: next });
  };

  const title = `${AGG_LABEL[agg] ?? agg} of ${state.name}`;
  const what = [
    filters.length ? filters.map(([k, v]) => `${k}=${v}`).join(", ") : "",
    state.group ? `by ${state.group.replace(/^:/, "")}` : "",
    state.service ? `in ${state.service}` : "",
  ].filter(Boolean);
  const description = `${KIND_LABEL[kind] ?? "Metric"} ${state.name}${what.length ? `, ${what.join(", ")}` : ""}. ${aggNote(kind, agg)}`;
  const note = series.data && (series.data.truncated && state.group ? "Showing the 20 largest groups." : vizNote(series.data, viz, tokens));

  return (
    <div className="span-8 mt-detail">
      <div className="filter-row mt-controls">
        <Select label="Aggregation" align="left" value={agg} options={aggs.map((a) => ({ value: a, label: AGG_LABEL[a] ?? a }))} onChange={(a) => set({ agg: a })} />
        <Select label="Group by" align="left" value={state.group ?? ""} options={groupOptions} onChange={(group) => set({ group })} maxWidth={220} />
        {keys.length > 0 && (
          <div className="mt-add">
            <Select label="Label" align="left" value={pickKey} options={keys.map((k) => ({ value: k.key, label: k.key }))} onChange={setKey} maxWidth={180} />
            <span className="dim">=</span>
            <Combobox label="Label value" value={value} onChange={setValue} options={values} placeholder="value" />
            <button type="button" className="btn" onClick={addFilter} disabled={!value}>
              <Plus size={14} />
              Filter
            </button>
          </div>
        )}
      </div>
      {filters.length > 0 && (
        <div className="mt-filters" aria-label="Label filters">
          {filters.map(([k, v]) => (
            <span key={k} className="pill mt-filter">
              <span className="mono">
                {k}={v}
              </span>
              <button type="button" className="mt-filter-x" onClick={() => removeFilter(k)} aria-label={`Remove ${k}=${v}`} title="Remove filter">
                <X size={12} />
              </button>
            </span>
          ))}
        </div>
      )}
      {series.error && <ErrorNotice title="Could not chart the metric" error={series.error} />}
      <Panel
        title={title}
        description={description}
        result={series.data}
        loading={series.loading}
        bodyHeight={320}
        chart={series.data && <VizView result={series.data} viz={viz} height={320} title={title} empty="No readings match in this time range." />}
        table={series.data && <DataTable result={series.data} maxHeight={320} defaultUnit={unit} />}
        note={note}
        dashboard={{ kind: "builtin", ref: "metrics/series", params: queryParams(query, state.service), viz, title, description }}
      />
      <p className="mt-meta dim">
        {labels.data ? `${formatCount(labels.data.series)} series` : ""}
        {info?.unit ? ` · unit ${info.unit}` : ""}
        {labels.data?.truncated ? " · the first 5000 series are read" : ""}
      </p>
    </div>
  );
}

function aggNote(kind: string, agg: string): string {
  if (agg.startsWith("p")) return `The ${agg.slice(1)}th percentile per interval, estimated from the histogram's buckets.`;
  if (agg === "rate") return kind === "histogram" ? "Observations per second." : "Increase per second.";
  if (agg === "count") return "Observations per interval.";
  if (kind === "counter") return agg === "sum" ? "The increase per interval, summed over series." : `The ${AGG_LABEL[agg].toLowerCase()} of each series' increase per interval.`;
  if (kind === "gauge") return agg === "sum" ? "Each series' average per interval, summed." : `The ${AGG_LABEL[agg].toLowerCase()} reading per interval.`;
  return `The ${AGG_LABEL[agg]?.toLowerCase() ?? agg} per interval.`;
}

function MetricsSetup() {
  const go = `visionary.Counter("orders_placed", "region", "eu").Add(1)
visionary.Gauge("queue_depth", "queue", "emails").Set(float64(n))
visionary.Histogram("checkout_ms").Observe(float64(took.Milliseconds()))`;
  const prom = `remote_write:
  - url: ${window.location.origin}/api/v1/write
    authorization:
      credentials: <server key>`;
  return (
    <SetupCard
      icon={<ChartLine size={20} />}
      title="Record your own metrics"
      text="Counters, gauges and histograms your services define: orders placed, queue depth, checkout time. Chart them by label, put them on dashboards and alert on them."
      topic="metrics"
    >
      <SetupOption title="From your code" text="The Go SDK adds them up in memory and writes every 15 seconds. Labels are key/value pairs.">
        <SetupCommand code={go} />
      </SetupOption>
      <SetupOption title="From OpenTelemetry or Prometheus" text="OTLP metrics that are not host metrics land here. Prometheus sends with remote_write and a server key.">
        <SetupCommand code={prom} />
        <div className="connect-more">
          <SetupHelpButton topic="otlp">Send OpenTelemetry</SetupHelpButton>
        </div>
      </SetupOption>
    </SetupCard>
  );
}
