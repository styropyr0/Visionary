import { useState } from "react";
import { api, rangeQuery } from "../../lib/api";
import { AGG_LABEL, aggsFor, labelText, parseLabelText, type MetricLabels, type MetricNames, type MetricQuery } from "../../lib/metrics";
import { useQuery } from "../../lib/useQuery";
import { Combobox } from "../Combobox";
import { Select } from "../Select";

const DAY = 24 * 3600 * 1000;

/** The custom_metric rule's metric, label filters, aggregation and grouping. */
export function MetricQueryFields({ value, onChange }: { value: MetricQuery | undefined; onChange: (q: MetricQuery) => void }) {
  const q = value ?? { name: "" };
  const [labels, setLabels] = useState(() => labelText(q.labels));
  const qs = rangeQuery(new Date(Date.now() - DAY), new Date());
  const names = useQuery<MetricNames>("rule-metric-names", (signal) => api<MetricNames>(`/api/metrics/names?${qs}`, { signal }));
  const known = names.data?.metrics.find((m) => m.name === q.name);
  const keys = useQuery<MetricLabels>(known ? `rule-metric-labels-${q.name}` : null, (signal) =>
    api<MetricLabels>(`/api/metrics/labels?${rangeQuery(new Date(Date.now() - DAY), new Date(), { name: q.name })}`, { signal }),
  );
  const aggs = aggsFor(known?.kind ?? "");
  const agg = q.agg && aggs.includes(q.agg) ? q.agg : aggs[0];

  return (
    <>
      <label className="field">
        <span>Metric</span>
        <Combobox label="Custom metric" value={q.name} placeholder="orders_placed" options={(names.data?.metrics ?? []).map((m) => m.name)} onChange={(name) => onChange({ ...q, name, agg: undefined })} />
      </label>
      <div className="al-row">
        <div className="field">
          <span>Aggregation</span>
          <Select label="Aggregation" align="left" value={agg} options={aggs.map((a) => ({ value: a, label: AGG_LABEL[a] ?? a }))} onChange={(a) => onChange({ ...q, agg: a })} />
        </div>
        <div className="field">
          <span>Fire per</span>
          <Select
            label="Fire per"
            align="left"
            value={q.group ?? ""}
            options={[
              { value: "", label: "The whole metric" },
              ...(keys.data?.labels ?? []).map((l) => ({ value: l.key, label: `Each ${l.key}` })),
              { value: ":service", label: "Each service" },
              { value: ":instance", label: "Each instance" },
            ]}
            onChange={(group) => onChange({ ...q, group: group || undefined })}
          />
        </div>
      </div>
      <label className="field">
        <span>Labels</span>
        <input
          className="input mono"
          value={labels}
          placeholder="region=eu, status=500"
          spellCheck={false}
          onChange={(e) => {
            setLabels(e.target.value);
            const parsed = parseLabelText(e.target.value);
            onChange({ ...q, labels: Object.keys(parsed).length ? parsed : undefined });
          }}
        />
        <p className="al-hint">Only series with these label values count. {known ? `${known.series} series in the last day.` : "Pick a metric from the list, or type one that has not reported yet."}</p>
      </label>
    </>
  );
}
