import { useEffect, useMemo, useRef, useState } from "react";
import * as echarts from "echarts/core";
import { BarChart, HeatmapChart, LineChart, PieChart } from "echarts/charts";
import { GridComponent, LegendComponent, MarkLineComponent, TooltipComponent, VisualMapComponent } from "echarts/components";
import { SVGRenderer } from "echarts/renderers";
import type { EChartsOption } from "echarts";
import { withDeployMarkers } from "../charts/option";
import { readTokens } from "../charts/palette";
import { showsUTC } from "../lib/time";
import { useApp } from "../lib/state";
import { useDeploys } from "./telemetry/deploys";

echarts.use([LineChart, BarChart, PieChart, HeatmapChart, GridComponent, TooltipComponent, LegendComponent, MarkLineComponent, VisualMapComponent, SVGRenderer]);

interface Props {
  option: EChartsOption;
  height: number;
  label: string;
}

// Charts render as SVG so their text takes the app font and its features
// (canvas text cannot use font-feature-settings); the family is also passed
// explicitly because ECharts writes it inline.
function withAppFont(option: EChartsOption): EChartsOption {
  const font = getComputedStyle(document.documentElement).getPropertyValue("--font-sans").trim();
  if (!font) return { ...option, useUTC: showsUTC() };
  const tooltip = option.tooltip && !Array.isArray(option.tooltip) ? { ...option.tooltip, textStyle: { ...option.tooltip.textStyle, fontFamily: font } } : option.tooltip;
  // Time axes follow the time zone preference like every other date.
  return { ...option, useUTC: showsUTC(), textStyle: { ...(option.textStyle as object), fontFamily: font }, tooltip };
}

// ECharts wraps a long legend onto more rows but leaves the plot where it was,
// under the legend. Count the rows it will take at this width and move the
// plot down to match. Row pitch and padding follow ECharts' box layout.
const LEGEND_ROW = 28;
const LEGEND_PAD = 5;
const TEXT_GAP = 5;
let measure: CanvasRenderingContext2D | null = null;

function legendRows(names: string[], width: number, legend: { itemWidth?: number; itemGap?: number; textStyle?: { fontSize?: number } }, font: string): number {
  measure ??= document.createElement("canvas").getContext("2d");
  if (!measure || width <= 0) return 1;
  measure.font = `${legend.textStyle?.fontSize ?? 12}px ${font || "sans-serif"}`;
  const gap = legend.itemGap ?? 10;
  const room = width - 2 * LEGEND_PAD;
  let rows = 1;
  let x = 0;
  for (const n of names) {
    const w = (legend.itemWidth ?? 25) + TEXT_GAP + measure.measureText(n).width;
    if (x > 0 && x + w > room) {
      rows++;
      x = 0;
    }
    x += w + gap;
  }
  return rows;
}

function fitLegend(option: EChartsOption, width: number): EChartsOption {
  const legend = option.legend as (Record<string, unknown> & { show?: boolean; orient?: string }) | undefined;
  const grid = option.grid as (Record<string, unknown> & { top?: number }) | undefined;
  if (!legend || Array.isArray(legend) || legend.show === false || legend.orient === "vertical" || !grid || Array.isArray(grid) || typeof grid.top !== "number") return option;
  const series = Array.isArray(option.series) ? option.series : option.series ? [option.series] : [];
  const names = [...new Set(series.map((x) => (x as { name?: string }).name).filter((n): n is string => !!n && !n.startsWith("__")))];
  const font = getComputedStyle(document.documentElement).getPropertyValue("--font-sans").trim();
  const rows = legendRows(names, width, legend as Parameters<typeof legendRows>[2], font);
  return rows <= 1 ? option : { ...option, grid: { ...grid, top: grid.top + (rows - 1) * LEGEND_ROW } };
}

export function Chart({ option, height, label }: Props) {
  const el = useRef<HTMLDivElement>(null);
  const inst = useRef<echarts.ECharts | null>(null);
  const [width, setWidth] = useState(0);

  useEffect(() => {
    if (!el.current) return;
    const chart = echarts.init(el.current, undefined, { renderer: "svg" });
    inst.current = chart;
    const ro = new ResizeObserver((entries) => {
      chart.resize();
      setWidth(Math.round(entries[0]?.contentRect.width ?? 0));
    });
    ro.observe(el.current);
    return () => {
      ro.disconnect();
      chart.dispose();
      inst.current = null;
    };
  }, []);

  // The first draw can happen before the web font loads; draw again once it has.
  const [fontsReady, setFontsReady] = useState(false);
  useEffect(() => {
    let live = true;
    document.fonts?.ready.then(() => live && setFontsReady(true));
    return () => {
      live = false;
    };
  }, []);

  // Time charts mark the deploys in the range (see telemetry/deploys).
  const deploys = useDeploys();
  const { theme } = useApp();
  const tokens = useMemo(() => readTokens(), [theme]);
  const drawn = useMemo(() => fitLegend(withDeployMarkers(option, deploys, tokens), width), [option, deploys, tokens, width]);

  useEffect(() => {
    inst.current?.setOption(withAppFont(drawn), { notMerge: true, lazyUpdate: true });
  }, [drawn, fontsReady]);

  return <div ref={el} className="chart" role="img" aria-label={label} style={{ width: "100%", height }} />;
}
