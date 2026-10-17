import { useMemo } from "react";
import type { EChartsOption } from "echarts";
import type { Result } from "../../lib/api";
import { axisUnit, formatAlertValue, type PreviewResponse } from "../../lib/alerts";
import { buildChart } from "../../charts/option";
import { Chart } from "../Chart";
import { useChartTokens } from "../VizView";

const BOUND_NAMES = new Set(["Threshold", "Target", "Upper bound", "Lower bound"]);

/**
 * The metric over the last 24h with the line the rule compares it against.
 * Bounds are drawn as dashed neutral lines so they never read as a series.
 */
export function PreviewChart({ preview, label, height = 220 }: { preview: PreviewResponse; label: string; height?: number }) {
  const tokens = useChartTokens();
  const option = useMemo<EChartsOption | null>(() => {
    const res = preview.result;
    if (!res || res.rows.length === 0) return null;
    const ti = res.columns.indexOf("t");
    const si = res.columns.indexOf("series");
    const vi = res.columns.indexOf("value");
    const rows: unknown[][] = res.rows.map((r) => [r[ti], (si >= 0 && String(r[si] ?? "")) || label, r[vi]]);
    // Bounds get a point on every bucket of the range, so they draw as one
    // continuous line however sparse the metric is.
    const ts = [...new Set(res.rows.map((r) => Number(r[ti])))].sort((a, b) => a - b);
    const step = res.bucket || 300;
    const start = Math.floor(Date.parse(res.from) / 1000 / step) * step;
    const end = Date.parse(res.to) / 1000;
    if (Number.isFinite(start) && Number.isFinite(end) && (end - start) / step <= 2000) {
      ts.length = 0;
      for (let t = start; t <= end; t += step) ts.push(t);
    }
    const bound = (name: string, v: number | null | undefined) => {
      if (v === null || v === undefined) return;
      for (const t of ts) rows.push([t, name, v]);
    };
    bound(preview.slo ? "Target" : preview.baseline ? "Upper bound" : "Threshold", preview.threshold);
    bound("Lower bound", preview.lower);
    const shaped: Result = { ...res, columns: ["t", "series", "value"], rows };
    const built = buildChart(shaped, { type: "timeseries", x: "t", y: ["value"], series: "series", unit: axisUnit(preview.unit) }, tokens);
    if (!built) return null;
    // Data series keep the categorical slots in order; bounds take no slot.
    let slot = 0;
    const series = (Array.isArray(built.option.series) ? built.option.series : []).map((s) => {
      const name = String((s as { name?: string }).name);
      if (!BOUND_NAMES.has(name)) {
        const color = tokens.series[slot++ % tokens.series.length];
        const line = (s as { lineStyle?: object }).lineStyle;
        const item = (s as { itemStyle?: object }).itemStyle;
        return { ...s, areaStyle: undefined, lineStyle: { ...line, color }, itemStyle: { ...item, color } };
      }
      return { ...s, lineStyle: { width: 1.5, type: "dashed" as const, color: tokens.text3 }, itemStyle: { color: tokens.text3 }, areaStyle: undefined, z: 1 };
    });
    const y = built.option.yAxis as Record<string, unknown>;
    return {
      ...built.option,
      series,
      yAxis: { ...y, ...(preview.slo ? { max: 100 } : {}), axisLabel: { ...(y.axisLabel as object), formatter: (v: number) => formatAlertValue(v, preview.unit) } },
    } as EChartsOption;
  }, [preview, label, tokens]);

  if (!option) {
    return <div className="panel-state" style={{ height }}>{preview.note ?? "No data in the last 24 hours."}</div>;
  }
  return <Chart option={option} height={height} label={`${label} over the last 24 hours`} />;
}
