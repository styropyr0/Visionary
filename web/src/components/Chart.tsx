import { useEffect, useRef, useState } from "react";
import * as echarts from "echarts/core";
import { BarChart, HeatmapChart, LineChart, PieChart } from "echarts/charts";
import { GridComponent, LegendComponent, TooltipComponent, VisualMapComponent } from "echarts/components";
import { SVGRenderer } from "echarts/renderers";
import type { EChartsOption } from "echarts";

echarts.use([LineChart, BarChart, PieChart, HeatmapChart, GridComponent, TooltipComponent, LegendComponent, VisualMapComponent, SVGRenderer]);

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
  if (!font) return option;
  const tooltip = option.tooltip && !Array.isArray(option.tooltip) ? { ...option.tooltip, textStyle: { ...option.tooltip.textStyle, fontFamily: font } } : option.tooltip;
  return { ...option, textStyle: { ...(option.textStyle as object), fontFamily: font }, tooltip };
}

export function Chart({ option, height, label }: Props) {
  const el = useRef<HTMLDivElement>(null);
  const inst = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    if (!el.current) return;
    const chart = echarts.init(el.current, undefined, { renderer: "svg" });
    inst.current = chart;
    const ro = new ResizeObserver(() => chart.resize());
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

  useEffect(() => {
    inst.current?.setOption(withAppFont(option), { notMerge: true, lazyUpdate: true });
  }, [option, fontsReady]);

  return <div ref={el} className="chart" role="img" aria-label={label} style={{ width: "100%", height }} />;
}
