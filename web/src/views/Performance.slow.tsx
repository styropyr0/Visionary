import { Fragment, useEffect, useRef, useState } from "react";
import { ChevronDown, ChevronRight, EyeOff, Plus, X } from "lucide-react";
import { api, rangeQuery, type Result } from "../lib/api";
import { formatBytes, formatCount, formatMs, toNumber } from "../lib/format";
import { load, save, useApp } from "../lib/state";
import { formatLogTime } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { Combobox } from "../components/Combobox";
import { DataTable, type Column } from "../components/DataTable";
import { Panel } from "../components/Panel";
import { FloatingMenu } from "../components/Floating";

const EXCLUDE_KEY = "va.perf.slowExclude";
const GROUP_KEY = "va.perf.slowGroup";

/** The slow-requests endpoint with the panel's filters. */
function slowURL(qs: string, excludes: string[], extra: Record<string, string> = {}): string {
  const p = new URLSearchParams(qs);
  for (const e of excludes) p.append("exclude", e);
  for (const [k, v] of Object.entries(extra)) p.set(k, v);
  return `/api/performance/slow-requests?${p}`;
}

function useSlow(excludes: string[], extra: Record<string, string> = {}, enabled = true) {
  const { resolved, tick } = useApp();
  const url = slowURL(rangeQuery(resolved.from, resolved.to), excludes, extra);
  return useQuery<Result>(enabled ? `${url}#${tick}` : null, (signal) => api<Result>(url, { signal }));
}

/**
 * The slowest requests in the range. Routes that are slow by design (an
 * export, a report) can be left out so they don't crowd out the rest, and
 * grouping by route shows whether a route is always slow or had one outlier.
 * Both choices are remembered per browser.
 */
export function SlowRequestsPanel({ routes, openLog, onOpenLog }: { routes: string[]; openLog: number | null; onOpenLog: (id: number | null) => void }) {
  const [excludes, setExcludesState] = useState<string[]>(() => load<string[]>(EXCLUDE_KEY, []));
  const [grouped, setGroupedState] = useState<boolean>(() => load<boolean>(GROUP_KEY, false));
  const setExcludes = (v: string[]) => {
    setExcludesState(v);
    save(EXCLUDE_KEY, v);
  };
  const setGrouped = (v: boolean) => {
    setGroupedState(v);
    save(GROUP_KEY, v);
  };
  const exclude = (pattern: string) => !excludes.includes(pattern) && setExcludes([...excludes, pattern]);

  // One query per view: useQuery keeps the last result while the next loads,
  // and a shared one would hand route rows to the request table (and back).
  const list = useSlow(excludes, {}, !grouped);
  const groups = useSlow(excludes, { group: "route" }, grouped);
  const q = grouped ? groups : list;
  const columns = requestColumns(true, exclude);

  return (
    <Panel
      className="span-12"
      title="Slowest requests"
      description={
        grouped
          ? "Routes ranked by their slowest request in the range, with the typical (p50) and p95 time next to it. Expand a route for its 25 slowest requests."
          : "The 25 slowest individual requests in the range. Open one to see its calls, headers and bodies."
      }
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={200}
      extraActions={
        <>
          <ExcludeMenu excludes={excludes} routes={routes} onChange={setExcludes} />
          <div className="segmented" role="radiogroup" aria-label="Show">
            <button type="button" role="radio" aria-checked={!grouped} className={!grouped ? "on" : ""} onClick={() => setGrouped(false)}>
              Requests
            </button>
            <button type="button" role="radio" aria-checked={grouped} className={grouped ? "on" : ""} onClick={() => setGrouped(true)}>
              By route
            </button>
          </div>
        </>
      }
      table={
        q.data &&
        (grouped ? (
          <RouteGroups result={q.data} excludes={excludes} openLog={openLog} onOpenLog={onOpenLog} onExclude={exclude} />
        ) : (
          <DataTable
            result={q.data}
            columns={columns}
            maxHeight={420}
            onRowClick={(row) => onOpenLog(toNumber(row[0]))}
            rowKey={(row) => row[0]}
            selectedKey={openLog ?? undefined}
          />
        ))
      }
    />
  );
}

/** Excluded route patterns: chips to remove, and a field to add one. */
function ExcludeMenu({ excludes, routes, onChange }: { excludes: string[]; routes: string[]; onChange: (v: string[]) => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [draft, setDraft] = useState("");
  useEffect(() => {
    if (!open) return;
    // The route suggestions open in a portal of their own; clicks there stay inside.
    const onDown = (e: MouseEvent) => {
      const t = e.target as Element;
      if (!ref.current?.contains(t) && !menuRef.current?.contains(t) && !t.closest?.(".floating")) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && !(e.target as Element).closest?.(".combobox") && setOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  const add = () => {
    const v = draft.trim();
    if (v && !excludes.includes(v)) onChange([...excludes, v]);
    setDraft("");
  };
  return (
    <div className="popover-anchor" ref={ref}>
      <button type="button" className={`btn sm slow-exclude-btn ${excludes.length ? "on" : ""}`} onClick={() => setOpen(!open)} aria-expanded={open}>
        <EyeOff size={13} />
        {excludes.length ? `${excludes.length} excluded` : "Exclude"}
      </button>
      {open && (
        <FloatingMenu anchor={ref} menuRef={menuRef} className="slow-exclude" role="dialog" maxHeight={420}>
          <div className="menu-title">Leave out of the slowest requests</div>
          {excludes.length > 0 && (
            <ul className="slow-exclude-list">
              {excludes.map((e) => (
                <li key={e}>
                  <span className="mono">{e}</span>
                  <button type="button" className="icon-btn" title={`Include ${e} again`} aria-label={`Include ${e} again`} onClick={() => onChange(excludes.filter((x) => x !== e))}>
                    <X size={13} />
                  </button>
                </li>
              ))}
            </ul>
          )}
          <form
            className="slow-exclude-add"
            onSubmit={(ev) => {
              ev.preventDefault();
              add();
            }}
          >
            <Combobox value={draft} onChange={setDraft} options={routes.filter((r) => !excludes.includes(r))} placeholder="/api/reports/*" label="Route to exclude" />
            <button type="submit" className="btn" disabled={!draft.trim()}>
              <Plus size={13} /> Add
            </button>
          </form>
          <p className="dim slow-exclude-help">
            * matches anything, so /api/admin/* covers the whole tree. Start with a method (POST /api/export) to leave out only that method.
          </p>
          {excludes.length > 0 && (
            <button type="button" className="btn danger sm slow-exclude-clear" onClick={() => onChange([])}>
              Clear all
            </button>
          )}
        </FloatingMenu>
      )}
    </div>
  );
}

/** Routes with their slowest requests behind a disclosure each. */
function RouteGroups({
  result,
  excludes,
  openLog,
  onOpenLog,
  onExclude,
}: {
  result: Result;
  excludes: string[];
  openLog: number | null;
  onOpenLog: (id: number | null) => void;
  onExclude: (pattern: string) => void;
}) {
  const [open, setOpen] = useState<Set<string>>(new Set());
  if (result.rows.length === 0) return <div className="panel-state">No requests in this time range.</div>;
  const col = (name: string) => result.columns.indexOf(name);
  const toggle = (k: string) =>
    setOpen((o) => {
      const n = new Set(o);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });

  return (
    <div className="table-wrap" style={{ maxHeight: 560 }}>
      <table className="data slow-groups">
        <thead>
          <tr>
            <th style={{ width: 28 }} aria-label="Expand" />
            <th style={{ width: 70 }}>Method</th>
            <th>Route</th>
            <th className="num" style={{ width: 90 }}>Requests</th>
            <th className="num" style={{ width: 80 }}>p50</th>
            <th className="num" style={{ width: 80 }}>p95</th>
            <th className="num" style={{ width: 90 }}>Slowest</th>
            <th className="num" style={{ width: 60 }}>5xx</th>
            <th style={{ width: 36 }} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {result.rows.map((row) => {
            const method = String(row[col("method")]);
            const route = String(row[col("route")]);
            const k = `${method} ${route}`;
            const isOpen = open.has(k);
            const errors = toNumber(row[col("errors")]) ?? 0;
            return (
              <Fragment key={k}>
                <tr className={`clickable ${isOpen ? "selected" : ""}`} onClick={() => toggle(k)} aria-expanded={isOpen}>
                  <td className="dim">{isOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}</td>
                  <td>
                    <span className="method">{method}</span>
                  </td>
                  <td className="mono slow-group-route" title={route}>{route}</td>
                  <td className="num">{formatCount(toNumber(row[col("requests")]) ?? 0)}</td>
                  <td className="num">{msCell(row[col("p50")])}</td>
                  <td className="num">{msCell(row[col("p95")])}</td>
                  <td className="num">{msCell(row[col("max_ms")])}</td>
                  <td className="num">{errors > 0 ? <span className="flag bad">{formatCount(errors)}</span> : <span className="dim">0</span>}</td>
                  <td>
                    <ExcludeButton pattern={k} onExclude={onExclude} />
                  </td>
                </tr>
                {isOpen && (
                  <tr className="slow-group-body">
                    <td colSpan={9}>
                      <GroupRequests method={method} route={route} excludes={excludes} openLog={openLog} onOpenLog={onOpenLog} />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function GroupRequests({ method, route, excludes, openLog, onOpenLog }: { method: string; route: string; excludes: string[]; openLog: number | null; onOpenLog: (id: number | null) => void }) {
  const q = useSlow(excludes, { method, route });
  if (q.error) return <div className="panel-state error">{q.error.message}</div>;
  if (!q.data) return <div className="panel-state" style={{ height: 80 }} aria-busy />;
  return (
    <DataTable
      result={q.data}
      columns={requestColumns(false)}
      maxHeight={320}
      onRowClick={(row) => onOpenLog(toNumber(row[0]))}
      rowKey={(row) => row[0]}
      selectedKey={openLog ?? undefined}
    />
  );
}

function ExcludeButton({ pattern, onExclude }: { pattern: string; onExclude: (pattern: string) => void }) {
  return (
    <button
      type="button"
      className="icon-btn slow-row-exclude"
      title={`Exclude ${pattern}`}
      aria-label={`Exclude ${pattern}`}
      onClick={(e) => {
        e.stopPropagation();
        onExclude(pattern);
      }}
    >
      <EyeOff size={13} />
    </button>
  );
}

function msCell(v: unknown) {
  const n = toNumber(v);
  return n === null ? <span className="dim">—</span> : formatMs(n);
}

/** Columns of a request list; the route ones are left out inside a group. */
function requestColumns(withRoute: boolean, onExclude?: (pattern: string) => void): Column[] {
  const cols: Column[] = [{ key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 }];
  if (withRoute) {
    cols.push(
      { key: "method", render: (v) => <span className="method">{String(v)}</span>, width: 70 },
      { key: "route", render: (v, row) => <span className="mono" title={String(row[4])}>{String(v)}</span> },
    );
  } else {
    cols.push({ key: "path", render: (v) => <span className="mono">{String(v)}</span> });
  }
  cols.push(
    {
      key: "status",
      align: "right",
      render: (v) => {
        const n = toNumber(v) ?? 0;
        return (
          <span className={`status s${Math.floor(n / 100)}`}>
            <span className="dot" aria-hidden />
            {String(v)}
          </span>
        );
      },
    },
    { key: "duration_ms", label: "Duration", align: "right", render: msCell },
    { key: "user_handle", label: "User", render: (v, row) => (v ? `@${String(v)}` : row[8] != null ? <span className="dim">#{String(row[8])}</span> : <span className="dim">—</span>) },
    { key: "response_size", label: "Size", align: "right", render: (v) => formatBytes(toNumber(v) ?? 0) },
  );
  if (onExclude) {
    cols.push({ key: "id", label: "", width: 36, render: (_v, row) => <ExcludeButton pattern={`${String(row[2])} ${String(row[3])}`} onExclude={onExclude} /> });
  }
  return cols;
}
