import { useMemo, useState } from "react";
import type { Viz } from "../../lib/api";
import { formatMs, formatPercent, toNumber } from "../../lib/format";
import { useInsights, type BreakdownResponse } from "../../lib/insights";
import { DataTable } from "../DataTable";
import { Panel } from "../Panel";
import { Select } from "../Select";
import { VizView } from "../VizView";

const STACKED: Viz = { type: "timeseries", x: "t", y: ["value"], series: "series", stack: true, unit: "ms" };

const PARTS: { key: "db_ms" | "http_ms" | "other_ms" | "app_ms"; label: string }[] = [
  { key: "db_ms", label: "Database" },
  { key: "http_ms", label: "Outbound HTTP" },
  { key: "other_ms", label: "Cache, queue and custom" },
  { key: "app_ms", label: "App code" },
];

/**
 * Where one route's time goes, per request, over time: database, outbound
 * HTTP, other recorded calls and the route's own code (what is left).
 */
export function BreakdownPanel({ className }: { className: string }) {
  const [pick, setPick] = useState("");
  const [method, route] = pick ? (JSON.parse(pick) as [string, string]) : ["", ""];
  const q = useInsights<BreakdownResponse>("breakdown", pick ? { method, route } : {});
  const d = q.data;
  const options = useMemo(
    () =>
      (d?.routes.rows ?? []).map((r) => ({
        value: JSON.stringify([String(r[0]), String(r[1])]),
        label: `${String(r[0])} ${String(r[1])}`,
      })),
    [d],
  );
  const current = d?.method ? JSON.stringify([d.method, d.route]) : "";
  const t = d?.totals;
  const total = toNumber(t?.total_ms) ?? 0;

  return (
    <Panel
      className={className}
      title="Endpoint breakdown"
      description="The average request of one route, split into database time, outbound HTTP, other recorded calls (cache, queue, custom and GraphQL spans) and the rest: the route's own code, or calls nothing records. Parallel calls can add up to more than the request, so app code never goes below zero."
      result={d?.series}
      loading={q.loading}
      error={q.error}
      bodyHeight={260}
      extraActions={options.length > 0 && <Select label="Route" value={current} options={options} onChange={setPick} maxWidth={340} />}
      chart={
        d?.series && (
          <div>
            {t && total > 0 && (
              <div className="bd-totals">
                {PARTS.map((p) => {
                  const v = toNumber(t[p.key]) ?? 0;
                  return (
                    <span key={p.key} className="bd-part">
                      <span className="dim">{p.label}</span> {formatMs(v)} <span className="dim">({formatPercent((100 * v) / total)})</span>
                    </span>
                  );
                })}
                <span className="bd-part">
                  <span className="dim">Request</span> {formatMs(total)}
                </span>
              </div>
            )}
            <VizView result={d.series} viz={STACKED} height={240} title="Endpoint breakdown" empty="No requests to this route in the range." />
          </div>
        )
      }
      table={d?.series && <DataTable result={d.series} maxHeight={260} />}
    />
  );
}
