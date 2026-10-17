import { useMemo } from "react";
import type { Result, Viz } from "../lib/api";
import { formatValue, humanize, splitUnit } from "../lib/format";
import { useApp } from "../lib/state";
import { buildChart } from "../charts/option";
import { readTokens, type ChartTokens } from "../charts/palette";
import { Chart } from "./Chart";
import { DataTable } from "./DataTable";

/** Chart tokens re-read from CSS whenever the theme flips. */
export function useChartTokens(): ChartTokens {
  const { theme } = useApp();
  return useMemo(() => readTokens(), [theme]);
}

interface Props {
  result: Result;
  viz: Viz;
  height: number;
  title: string;
  /** Shown instead of the default when there are no rows. */
  empty?: string;
}

/** Draws a result the way its viz says, falling back to a table. */
export function VizView({ result, viz, height, title, empty }: Props) {
  const tokens = useChartTokens();
  const built = useMemo(() => buildChart(result, viz, tokens), [result, viz, tokens]);

  if (result.rows.length === 0) {
    return (
      <div className="panel-state" style={{ height }}>
        {empty ?? "No data in this time range."}
      </div>
    );
  }
  if (viz.type === "stat") {
    return <StatHero result={result} viz={viz} height={height} />;
  }
  if (!built) {
    return <DataTable result={result} maxHeight={height} defaultUnit={viz.unit} />;
  }
  return <Chart option={built.option} height={height} label={title} />;
}

export function vizNote(result: Result | undefined, viz: Viz, tokens: ChartTokens): string | undefined {
  if (!result) return undefined;
  const built = buildChart(result, viz, tokens);
  const notes = [built?.note, result.truncated ? `Showing the first ${result.rows.length.toLocaleString()} rows.` : undefined];
  return notes.filter(Boolean).join(" ") || undefined;
}

function StatHero({ result, viz, height }: { result: Result; viz: Viz; height: number }) {
  const key = viz.y?.[0] ?? result.columns[0];
  const i = result.columns.indexOf(key);
  const value = i >= 0 ? result.rows[0]?.[i] : undefined;
  const [num, unit] = splitUnit(formatValue(value, viz.unit));
  return (
    <div className="stat-hero" style={{ minHeight: height }}>
      <div className="stat-label">{humanize(key)}</div>
      <div className="stat-value">
        {num}
        {unit && <span className="stat-unit">{unit}</span>}
      </div>
    </div>
  );
}
