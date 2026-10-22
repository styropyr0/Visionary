import type { EChartsOption } from "echarts";
import type { Result } from "../lib/api";
import { escapeHtml, formatCount, toNumber } from "../lib/format";
import type { ChartTokens } from "./palette";
import { tooltipBox } from "./option";
import { dateFormat, showsUTC } from "../lib/time";

/** Latency bands, matching the server's latencyBuckets (upper bounds in ms). */
export const LATENCY_BANDS = ["< 25 ms", "25–50 ms", "50–100 ms", "100–250 ms", "250–500 ms", "0.5–1 s", "1–2.5 s", "2.5–5 s", "5–10 s", "≥ 10 s"];

const timeFmt = () => dateFormat({ month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
const axisFmt = () => dateFormat({ hour: "2-digit", minute: "2-digit" });
const dayFmt = () => dateFormat({ month: "short", day: "numeric" });

/**
 * Requests per time bucket and latency band. Color is the count on a
 * square-root scale, so a few slow requests stay visible next to thousands of
 * fast ones; the tooltip carries the exact number.
 */
export function buildLatencyHeatmap(res: Result, t: ChartTokens): EChartsOption | null {
  const ti = res.columns.indexOf("t");
  const bi = res.columns.indexOf("band");
  const ci = res.columns.indexOf("requests");
  if (ti < 0 || bi < 0 || ci < 0 || res.rows.length === 0) return null;

  // A full timeline, so quiet buckets show as gaps rather than closing up.
  const step = res.bucket;
  const start = Math.floor(Date.parse(res.from) / 1000 / step) * step;
  const end = Math.floor(Date.parse(res.to) / 1000);
  const times: number[] = [];
  for (let ts = start; ts <= end && times.length < 2000; ts += step) times.push(ts);
  const index = new Map(times.map((ts, i) => [ts, i]));

  const perBucket = new Map<number, number>();
  const cells: [number, number, number, number][] = [];
  let max = 0;
  for (const row of res.rows) {
    const ts = toNumber(row[ti]);
    const band = toNumber(row[bi]);
    const n = toNumber(row[ci]) ?? 0;
    if (ts === null || band === null || !index.has(ts)) continue;
    cells.push([index.get(ts)!, band, Math.sqrt(n), n]);
    perBucket.set(ts, (perBucket.get(ts) ?? 0) + n);
    max = Math.max(max, n);
  }
  if (cells.length === 0) return null;
  const multiDay = end - start > 86400;

  return {
    animation: false,
    grid: { left: 2, right: 64, top: 8, bottom: 2, containLabel: true },
    tooltip: {
      ...tooltipBox(t),
      trigger: "item",
      formatter: (raw: unknown) => {
        const p = raw as { value: [number, number, number, number]; color?: string };
        const [xi, yi, , n] = p.value;
        const ts = times[xi];
        const total = perBucket.get(ts) ?? 0;
        const share = total > 0 ? ` · ${((100 * n) / total).toFixed(1)}% of the interval` : "";
        return `<div class="tt"><div class="tt-head">${escapeHtml(timeFmt().format(new Date(ts * 1000)))}</div><div class="tt-row"><span class="tt-key block" style="background:${p.color ?? "transparent"}"></span><span class="tt-val">${escapeHtml(formatCount(n, false))}</span><span class="tt-name">${escapeHtml(LATENCY_BANDS[yi])}${escapeHtml(share)}</span></div></div>`;
      },
    },
    xAxis: {
      type: "category",
      data: times,
      axisLine: { lineStyle: { color: t.axis } },
      axisTick: { show: false },
      splitArea: { show: false },
      axisLabel: {
        color: t.text3,
        fontSize: 11,
        hideOverlap: true,
        formatter: (v: string) => {
          const d = new Date(Number(v) * 1000);
          return multiDay && (showsUTC() ? d.getUTCHours() === 0 && d.getUTCMinutes() === 0 : d.getHours() === 0 && d.getMinutes() === 0) ? dayFmt().format(d) : axisFmt().format(d);
        },
      },
    },
    yAxis: {
      type: "category",
      data: LATENCY_BANDS,
      axisLine: { show: false },
      axisTick: { show: false },
      splitArea: { show: false },
      axisLabel: { color: t.text3, fontSize: 11 },
    },
    visualMap: {
      type: "continuous",
      dimension: 2,
      min: 0,
      max: Math.max(1, Math.sqrt(max)),
      calculable: false,
      orient: "vertical",
      right: 0,
      top: "middle",
      itemWidth: 10,
      itemHeight: 120,
      text: [formatCount(max), "0"],
      textGap: 6,
      textStyle: { color: t.text3, fontSize: 11 },
      inRange: { color: t.seq },
    },
    series: [
      {
        type: "heatmap",
        data: cells,
        // The surface-colored edge separates neighboring cells.
        itemStyle: { borderColor: t.surface, borderWidth: 1, borderRadius: 2 },
        emphasis: { itemStyle: { borderColor: t.text1, borderWidth: 1 } },
        progressive: 0,
      },
    ],
  };
}
