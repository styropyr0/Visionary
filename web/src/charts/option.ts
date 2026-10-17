import type { EChartsOption } from "echarts";
import type { Result, Unit, Viz } from "../lib/api";
import { escapeHtml, formatAxis, formatValue, humanize, toNumber } from "../lib/format";
import { statusColor, type ChartTokens } from "./palette";

const MAX_SERIES = 8;
const MAX_PIE_SLICES = 6;
const MAX_FILL_POINTS = 3000;

interface Shaped {
  name: string;
  color: string;
  points: [number, number | null][];
}

export interface BuiltChart {
  option: EChartsOption;
  /** Shown under the chart when the data had to be reduced. */
  note?: string;
}

const col = (res: Result, name?: string) => (name ? res.columns.indexOf(name) : -1);

function toEpochMs(v: unknown): number | null {
  const n = toNumber(v);
  if (n !== null) return n < 1e11 ? n * 1000 : n;
  if (typeof v === "string") {
    const t = Date.parse(v);
    return Number.isNaN(t) ? null : t;
  }
  return null;
}

function isAdditive(viz: Viz): boolean {
  return viz.unit === "count" || viz.unit === "bytes" || !!viz.stack;
}

/**
 * Assign colors by identity: status names get status colors, everything else
 * takes categorical slots in fixed order of the (sorted) names, so the same
 * set of series always gets the same colors.
 */
function colorize(names: string[], t: ChartTokens): Map<string, string> {
  const out = new Map<string, string>();
  const allStatus = names.every((n) => n === "Other" || statusColor(n, t) !== null);
  let slot = 0;
  for (const n of names) {
    if (n === "Other") out.set(n, t.text3);
    else if (allStatus) out.set(n, statusColor(n, t)!);
    else out.set(n, t.series[slot++ % MAX_SERIES]);
  }
  return out;
}

/** Reduce to at most 8 series: additive data folds the tail into "Other". */
function capSeries(
  groups: Map<string, Map<number, number | null>>,
  additive: boolean,
): { groups: Map<string, Map<number, number | null>>; note?: string } {
  if (groups.size <= MAX_SERIES) return { groups };
  const ranked = [...groups.entries()]
    .map(([name, pts]) => ({ name, pts, sum: [...pts.values()].reduce<number>((a, b) => a + (b ?? 0), 0) }))
    .sort((a, b) => b.sum - a.sum);
  const keep = additive ? MAX_SERIES - 1 : MAX_SERIES;
  const out = new Map<string, Map<number, number | null>>();
  for (const r of ranked.slice(0, keep)) out.set(r.name, r.pts);
  if (additive) {
    const other = new Map<number, number | null>();
    for (const r of ranked.slice(keep)) {
      for (const [ts, v] of r.pts) other.set(ts, (other.get(ts) ?? 0) + (v ?? 0));
    }
    out.set("Other", other);
    return { groups: out, note: `${ranked.length - keep} smaller series are combined into Other.` };
  }
  return { groups: out, note: `Showing the top ${keep} of ${ranked.length} series.` };
}

function shapeTimeseries(res: Result, viz: Viz, t: ChartTokens): { series: Shaped[]; note?: string } {
  const xi = col(res, viz.x);
  if (xi < 0) return { series: [] };
  const raw = new Map<string, Map<number, number | null>>();

  const si = col(res, viz.series);
  if (si >= 0) {
    const yi = col(res, viz.y?.[0]);
    if (yi < 0) return { series: [] };
    for (const row of res.rows) {
      const ts = toEpochMs(row[xi]);
      if (ts === null) continue;
      const name = row[si] === null || row[si] === undefined ? "(none)" : String(row[si]);
      if (!raw.has(name)) raw.set(name, new Map());
      raw.get(name)!.set(ts, toNumber(row[yi]));
    }
  } else {
    for (const y of viz.y ?? []) {
      const yi = col(res, y);
      if (yi < 0) continue;
      const pts = new Map<number, number | null>();
      for (const row of res.rows) {
        const ts = toEpochMs(row[xi]);
        if (ts !== null) pts.set(ts, toNumber(row[yi]));
      }
      raw.set(y, pts);
    }
  }

  const additive = isAdditive(viz);
  const { groups, note } = capSeries(raw, additive);
  const names = [...groups.keys()].filter((n) => n !== "Other");
  if (si >= 0) names.sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
  if (groups.has("Other")) names.push("Other");
  const colors = colorize(names, t);

  // Fill missing buckets so gaps read as zero traffic (or a break, for
  // non-additive measures like latency) instead of a line jumping across.
  const bucketMs = res.bucket * 1000;
  const allTs = new Set<number>();
  for (const pts of groups.values()) for (const ts of pts.keys()) allTs.add(ts);
  const aligned = bucketMs > 0 && [...allTs].every((ts) => ts % bucketMs === 0);
  let timeline: number[];
  if (aligned) {
    const start = Math.floor(Date.parse(res.from) / bucketMs) * bucketMs;
    const end = Date.parse(res.to);
    const count = Math.floor((end - start) / bucketMs) + 1;
    timeline =
      count > 0 && count <= MAX_FILL_POINTS
        ? Array.from({ length: count }, (_, i) => start + i * bucketMs).filter((ts) => ts <= end)
        : [...allTs].sort((a, b) => a - b);
  } else {
    timeline = [...allTs].sort((a, b) => a - b);
  }

  const series = names.map((name) => {
    const pts = groups.get(name)!;
    return {
      // Column names read as labels ("server_errors" -> "5xx"); data values stay as-is.
      name: si >= 0 ? name : humanize(name),
      color: colors.get(name)!,
      points: timeline.map((ts): [number, number | null] => [
        ts,
        // Stacked columns leave empty buckets blank; lines drop to zero.
        pts.has(ts) ? (pts.get(ts) ?? null) : aligned && additive && !viz.stack ? 0 : null,
      ]),
    };
  });
  return { series, note };
}

function tooltipBox(t: ChartTokens) {
  return {
    backgroundColor: t.surface,
    borderColor: t.border,
    borderWidth: 1,
    padding: [8, 10] as [number, number],
    textStyle: { color: t.text1, fontSize: 12 },
    extraCssText: "border-radius:6px;box-shadow:0 8px 24px rgba(0,0,0,.28);",
    confine: true,
  };
}

function legend(names: string[], t: ChartTokens, block: boolean) {
  if (names.length < 2) return { show: false };
  return {
    show: true,
    top: 0,
    left: 0,
    icon: "roundRect",
    itemWidth: 12,
    itemHeight: block ? 8 : 2,
    itemGap: 16,
    // Series carry a surface-colored ring for overlap; the legend key must not.
    itemStyle: { borderWidth: 0 },
    lineStyle: { width: 0 },
    textStyle: { color: t.text2, fontSize: 12 },
    inactiveColor: t.text3,
  };
}

const timeLabels = {
  year: "{yyyy}",
  month: "{MMM}",
  day: "{MMM} {d}",
  hour: "{HH}:{mm}",
  minute: "{HH}:{mm}",
  second: "{HH}:{mm}:{ss}",
};

const tooltipTime = new Intl.DateTimeFormat(undefined, {
  weekday: "short",
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

interface AxisParam {
  seriesName: string;
  value: [number, number | null] | number | null;
  color: string;
  axisValue: number | string;
  name: string;
}

function row(color: string, value: string, name: string, block: boolean) {
  return `<div class="tt-row"><span class="tt-key${block ? " block" : ""}" style="background:${color}"></span><span class="tt-val">${escapeHtml(value)}</span><span class="tt-name">${escapeHtml(name)}</span></div>`;
}

function valueAxis(t: ChartTokens, unit: Unit) {
  return {
    type: "value" as const,
    min: 0,
    splitNumber: 4,
    axisLine: { show: false },
    axisTick: { show: false },
    axisLabel: { color: t.text3, fontSize: 11, formatter: (v: number) => formatAxis(v, unit) },
    splitLine: { lineStyle: { color: t.grid, width: 1 } },
  };
}

function buildTimeseries(res: Result, viz: Viz, t: ChartTokens): BuiltChart | null {
  const { series, note } = shapeTimeseries(res, viz, t);
  if (series.length === 0) return null;
  const unit = viz.unit ?? "none";
  const stacked = !!viz.stack;
  const names = series.map((s) => s.name);
  const hasLegend = names.length >= 2;

  return {
    note,
    option: {
      animationDuration: 250,
      animationDurationUpdate: 200,
      grid: { left: 2, right: 10, top: hasLegend ? 30 : 10, bottom: 2, containLabel: true },
      legend: legend(names, t, stacked),
      tooltip: {
        ...tooltipBox(t),
        trigger: "axis",
        axisPointer: stacked
          ? { type: "shadow", shadowStyle: { color: t.hover } }
          : { type: "line", lineStyle: { color: t.axis, width: 1 } },
        formatter: (raw: unknown) => {
          const params = (Array.isArray(raw) ? raw : [raw]) as AxisParam[];
          if (params.length === 0) return "";
          const ts = Number(Array.isArray(params[0].value) ? params[0].value[0] : params[0].axisValue);
          const items = params
            .map((p) => ({ p, v: Array.isArray(p.value) ? p.value[1] : (p.value as number | null) }))
            .filter((x) => x.v !== null && x.v !== undefined)
            .sort((a, b) => (b.v as number) - (a.v as number));
          let html = `<div class="tt"><div class="tt-head">${escapeHtml(tooltipTime.format(new Date(ts)))}</div>`;
          for (const { p, v } of items) html += row(p.color, formatValue(v, unit), p.seriesName, stacked);
          if (stacked && items.length > 1) {
            const total = items.reduce((a, x) => a + (x.v as number), 0);
            html += `<div class="tt-total">${row("transparent", formatValue(total, unit), "Total", true)}</div>`;
          }
          return html + "</div>";
        },
      },
      xAxis: {
        type: "time",
        axisLine: { lineStyle: { color: t.axis } },
        axisTick: { show: false },
        axisLabel: { color: t.text3, fontSize: 11, hideOverlap: true, formatter: timeLabels },
        splitLine: { show: false },
      },
      yAxis: valueAxis(t, unit),
      series: series.map((s, i) =>
        stacked
          ? {
              type: "bar" as const,
              name: s.name,
              stack: "total",
              data: s.points,
              barMaxWidth: 24,
              itemStyle: {
                color: s.color,
                // The surface-colored edge is the 2px gap between segments.
                borderColor: t.surface,
                borderWidth: 1,
                borderRadius: i === series.length - 1 ? [3, 3, 0, 0] : 0,
              },
              emphasis: { disabled: true },
            }
          : {
              type: "line" as const,
              name: s.name,
              data: s.points,
              showSymbol: false,
              symbol: "circle",
              symbolSize: 8,
              connectNulls: false,
              lineStyle: { width: 2, color: s.color, cap: "round", join: "round" },
              itemStyle: { color: s.color, borderColor: t.surface, borderWidth: 2 },
              areaStyle: series.length === 1 ? { color: s.color, opacity: 0.1 } : undefined,
              emphasis: { disabled: true },
            },
      ),
    },
  };
}

function buildBar(res: Result, viz: Viz, t: ChartTokens): BuiltChart | null {
  const xi = col(res, viz.x);
  const ys = (viz.y ?? []).filter((y) => col(res, y) >= 0).slice(0, 4);
  if (xi < 0 || ys.length === 0) return null;
  const unit = viz.unit ?? "none";
  const cats = res.rows.map((r) => (r[xi] === null || r[xi] === undefined ? "(none)" : String(r[xi])));
  const longest = cats.reduce((m, c) => Math.max(m, c.length), 0);
  const horizontal = cats.length > 6 || longest > 12;
  const byStatus = viz.colorBy === "status" && ys.length === 1;
  const colors = colorize(ys, t);
  const single = ys.length === 1;

  const series = ys.map((y) => {
    const yi = col(res, y);
    return {
      type: "bar" as const,
      name: humanize(y),
      data: res.rows.map((r, i) => {
        const v = toNumber(r[yi]);
        if (!byStatus) return v;
        return { value: v, itemStyle: { color: statusColor(cats[i], t) ?? t.series[0] } };
      }),
      barMaxWidth: 20,
      itemStyle: {
        color: colors.get(y),
        borderRadius: horizontal ? [0, 4, 4, 0] : [4, 4, 0, 0],
      },
      label: single
        ? {
            show: true,
            position: horizontal ? ("right" as const) : ("top" as const),
            color: t.text2,
            fontSize: 11,
            formatter: (p: { value: unknown }) => formatValue(p.value, unit),
          }
        : { show: false },
      emphasis: { itemStyle: { opacity: 0.85 } },
    };
  });

  const catAxis = {
    type: "category" as const,
    data: cats,
    inverse: horizontal,
    axisLine: { lineStyle: { color: t.axis } },
    axisTick: { show: false },
    axisLabel: {
      color: t.text2,
      fontSize: 11,
      width: horizontal ? 180 : 90,
      overflow: "truncate" as const,
      hideOverlap: !horizontal,
    },
  };
  const valAxis = { ...valueAxis(t, unit), splitLine: { lineStyle: { color: t.grid, width: 1 } } };

  return {
    option: {
      animationDuration: 250,
      grid: { left: 2, right: horizontal ? 56 : 10, top: single ? 16 : 30, bottom: 2, containLabel: true },
      legend: legend(ys.map(humanize), t, true),
      tooltip: {
        ...tooltipBox(t),
        trigger: "axis",
        axisPointer: { type: "shadow", shadowStyle: { color: t.hover } },
        formatter: (raw: unknown) => {
          const params = (Array.isArray(raw) ? raw : [raw]) as AxisParam[];
          if (params.length === 0) return "";
          let html = `<div class="tt"><div class="tt-head">${escapeHtml(String(params[0].name))}</div>`;
          for (const p of params) {
            const v = typeof p.value === "object" && p.value !== null && !Array.isArray(p.value) ? (p.value as { value: number }).value : p.value;
            html += row(p.color, formatValue(v, unit), p.seriesName, true);
          }
          return html + "</div>";
        },
      },
      xAxis: horizontal ? valAxis : catAxis,
      yAxis: horizontal ? catAxis : valAxis,
      series,
    },
  };
}

function buildPie(res: Result, viz: Viz, t: ChartTokens): BuiltChart | null {
  const xi = col(res, viz.x);
  const yi = col(res, viz.y?.[0]);
  if (xi < 0 || yi < 0) return null;
  const unit = viz.unit ?? "none";
  let slices = res.rows
    .map((r) => ({ name: r[xi] === null ? "(none)" : String(r[xi]), value: toNumber(r[yi]) ?? 0 }))
    .filter((s) => s.value > 0)
    .sort((a, b) => b.value - a.value);
  let note: string | undefined;
  if (slices.length > MAX_PIE_SLICES) {
    const rest = slices.slice(MAX_PIE_SLICES - 1);
    slices = [...slices.slice(0, MAX_PIE_SLICES - 1), { name: "Other", value: rest.reduce((a, s) => a + s.value, 0) }];
    note = `${rest.length} smaller parts are combined into Other.`;
  }
  const total = slices.reduce((a, s) => a + s.value, 0);
  const colors = colorize(
    slices.map((s) => s.name),
    t,
  );

  return {
    note,
    option: {
      animationDuration: 250,
      tooltip: {
        ...tooltipBox(t),
        trigger: "item",
        formatter: (raw: unknown) => {
          const p = raw as { name: string; value: number; color: string; percent: number };
          return `<div class="tt">${row(p.color, `${formatValue(p.value, unit)} · ${p.percent.toFixed(1)}%`, p.name, true)}</div>`;
        },
      },
      legend: {
        orient: "vertical",
        right: 8,
        top: "middle",
        icon: "roundRect",
        itemWidth: 10,
        itemHeight: 10,
        itemGap: 10,
        textStyle: { color: t.text2, fontSize: 12 },
        formatter: (name: string) => {
          const s = slices.find((x) => x.name === name);
          return s && total > 0 ? `${name}  ${((100 * s.value) / total).toFixed(1)}%` : name;
        },
      },
      series: [
        {
          type: "pie",
          radius: ["52%", "78%"],
          center: ["34%", "50%"],
          avoidLabelOverlap: true,
          label: { show: false },
          labelLine: { show: false },
          itemStyle: { borderColor: t.surface, borderWidth: 2, borderRadius: 4 },
          emphasis: { scale: false, itemStyle: { opacity: 0.85 } },
          data: slices.map((s) => ({ ...s, itemStyle: { color: colors.get(s.name) } })),
        },
      ],
    },
  };
}

export function buildChart(res: Result, viz: Viz, t: ChartTokens): BuiltChart | null {
  if (res.rows.length === 0) return null;
  switch (viz.type) {
    case "timeseries":
      return buildTimeseries(res, viz, t);
    case "bar":
      return buildBar(res, viz, t);
    case "pie":
      return buildPie(res, viz, t);
    default:
      return null;
  }
}

/** Sparkline points for stat tiles: one series summed per bucket. */
export function sparkPoints(res: Result | undefined, valueCols: string[], seriesFilter?: (s: string) => boolean): number[] {
  if (!res) return [];
  const ti = res.columns.indexOf("t");
  const si = res.columns.indexOf("series");
  const idx = valueCols.map((c) => res.columns.indexOf(c)).filter((i) => i >= 0);
  if (ti < 0 || idx.length === 0) return [];
  const byT = new Map<number, number>();
  for (const r of res.rows) {
    if (si >= 0 && seriesFilter && !seriesFilter(String(r[si]))) continue;
    const ts = toNumber(r[ti]);
    if (ts === null) continue;
    const v = idx.reduce((a, i) => a + (toNumber(r[i]) ?? 0), 0);
    byT.set(ts, (byT.get(ts) ?? 0) + v);
  }
  return [...byT.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => v);
}
