import { useMemo, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp } from "lucide-react";
import type { Result, Unit } from "../lib/api";
import { formatValue, humanize, isTimeColumn, toNumber, unitForColumn, yesNo } from "../lib/format";
import { cellTitle, formatLogTime } from "../lib/time";

export interface Column {
  key: string;
  label?: string;
  unit?: Unit;
  align?: "left" | "right";
  render?: (value: unknown, row: unknown[]) => ReactNode;
  width?: number | string;
}

interface Props {
  result: Result;
  columns?: Column[];
  maxHeight?: number;
  onRowClick?: (row: unknown[]) => void;
  selectedKey?: unknown;
  rowKey?: (row: unknown[]) => unknown;
  sortable?: boolean;
  defaultUnit?: Unit;
}

/** Generic result table. Numbers right-align with tabular figures. */
export function DataTable({ result, columns, maxHeight, onRowClick, selectedKey, rowKey, sortable = true, defaultUnit }: Props) {
  const cols = useMemo<Column[]>(() => {
    if (columns) return columns;
    return result.columns.map((key) => {
      const sample = result.rows.find((r) => r[result.columns.indexOf(key)] != null)?.[result.columns.indexOf(key)];
      const numeric = toNumber(sample) !== null && typeof sample !== "string";
      return {
        key,
        unit: numeric ? unitForColumn(key, key === "value" ? (defaultUnit ?? "none") : "none") : undefined,
        align: numeric ? "right" : "left",
      };
    });
  }, [columns, result, defaultUnit]);

  const [sort, setSort] = useState<{ key: string; dir: 1 | -1 } | null>(null);

  const rows = useMemo(() => {
    if (!sort) return result.rows;
    const i = result.columns.indexOf(sort.key);
    if (i < 0) return result.rows;
    return [...result.rows].sort((a, b) => {
      const na = toNumber(a[i]);
      const nb = toNumber(b[i]);
      if (na !== null && nb !== null) return (na - nb) * sort.dir;
      return String(a[i] ?? "").localeCompare(String(b[i] ?? "")) * sort.dir;
    });
  }, [result, sort]);

  const toggle = (key: string) => {
    if (!sortable) return;
    setSort((s) => (s?.key === key ? (s.dir === -1 ? { key, dir: 1 } : null) : { key, dir: -1 }));
  };

  if (result.rows.length === 0) {
    return <div className="panel-state">No rows in this time range.</div>;
  }

  return (
    <div className="table-wrap" style={{ maxHeight }}>
      <table className="data">
        <thead>
          <tr>
            {cols.map((c) => (
              <th
                key={c.key}
                className={`${c.align === "right" ? "num" : ""} ${sortable ? "sortable" : ""}`}
                style={{ width: c.width }}
                onClick={() => toggle(c.key)}
                aria-sort={sort?.key === c.key ? (sort.dir === 1 ? "ascending" : "descending") : undefined}
              >
                {c.label ?? humanize(c.key)}
                {sort?.key === c.key && (
                  <span className="sort">{sort.dir === 1 ? <ArrowUp size={12} /> : <ArrowDown size={12} />}</span>
                )}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, ri) => {
            const key = rowKey ? rowKey(row) : ri;
            return (
              <tr
                key={String(key)}
                className={`${onRowClick ? "clickable" : ""} ${selectedKey !== undefined && key === selectedKey ? "selected" : ""}`}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
              >
                {cols.map((c) => {
                  const v = row[result.columns.indexOf(c.key)];
                  return (
                    <td key={c.key} className={c.align === "right" ? "num" : ""} title={typeof v === "string" ? cellTitle(v) : undefined}>
                      {c.render ? c.render(v, row) : defaultCell(c, v)}
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function defaultCell(c: Column, v: unknown): ReactNode {
  if (v === null || v === undefined || v === "") return <span className="dim">—</span>;
  if (c.key === "t") {
    const n = toNumber(v);
    if (n !== null) return formatLogTime(new Date(n < 1e11 ? n * 1000 : n).toISOString());
  }
  if (typeof v === "boolean") return yesNo(v);
  if (typeof v === "string" && isTimeColumn(c.key)) return formatLogTime(v);
  if (c.unit) return formatValue(v, c.unit);
  return String(v);
}
