import { useEffect, useMemo, useState } from "react";
import { Search } from "lucide-react";
import { api, rangeQuery, type Meta, type Result } from "../lib/api";
import { formatBytes, formatMs, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatLogTime } from "../lib/time";
import { DataTable, type Column } from "../components/DataTable";
import { Select } from "../components/Select";
import { CopyButton, Drawer } from "../components/Modal";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { RequestTelemetry } from "../components/telemetry/RequestTelemetry";
import { shortVersion } from "../lib/telemetry";
import { linkify } from "../components/ErrorNotice";

interface Filters {
  status: string;
  method: string;
  route: string;
  user: string;
  search: string;
}

const EMPTY: Filters = { status: "", method: "", route: "", user: "", search: "" };

export function Logs({ meta }: { meta: Meta | undefined }) {
  const { resolved, tick } = useApp();
  const [filters, setFilters] = useState<Filters>(EMPTY);
  const [debounced, setDebounced] = useState<Filters>(EMPTY);
  const [pages, setPages] = useState<Result[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  const [selected, setSelected] = useState<number | null>(null);

  // Text inputs settle before querying D1; selects apply at once.
  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(filters), 350);
    return () => window.clearTimeout(id);
  }, [filters]);

  const qs = rangeQuery(resolved.from, resolved.to, { ...debounced });
  const first = useQuery(`logs?${qs}#${tick}`, (signal) => api<Result>(`/api/logs?${qs}`, { signal }));

  useEffect(() => {
    setPages(first.data ? [first.data] : []);
  }, [first.data]);

  const merged = useMemo<Result | undefined>(() => {
    if (pages.length === 0) return undefined;
    return { ...pages[0], rows: pages.flatMap((p) => p.rows), truncated: pages[pages.length - 1].truncated };
  }, [pages]);

  const loadMore = async () => {
    if (!merged || loadingMore) return;
    const last = merged.rows[merged.rows.length - 1];
    setLoadingMore(true);
    try {
      const next = await api<Result>(`/api/logs?${rangeQuery(resolved.from, resolved.to, { ...debounced, before: String(last[0]) })}`);
      setPages((p) => [...p, next]);
    } finally {
      setLoadingMore(false);
    }
  };

  const set = (k: keyof Filters) => (e: { target: { value: string } }) => setFilters((f) => ({ ...f, [k]: e.target.value }));
  const setNow = (k: keyof Filters) => (e: { target: { value: string } }) => {
    const next = { ...filters, [k]: e.target.value };
    setFilters(next);
    setDebounced(next);
  };
  const pick = (k: keyof Filters) => (value: string) => setNow(k)({ target: { value } });
  const active = Object.values(filters).some(Boolean);

  return (
    <>
      <TopBar
        title="Request logs"
        section="API"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl busy={first.loading} />
          </>
        }
      />
      <div className="content logs-content">
        <div className="filter-row" role="search">
          <Select
            label="Status"
            align="left"
            value={filters.status}
            onChange={pick("status")}
            options={[
              { value: "", label: "All statuses" },
              { value: "2xx", label: "2xx success" },
              { value: "3xx", label: "3xx redirect" },
              { value: "4xx", label: "4xx client error" },
              { value: "5xx", label: "5xx server error" },
            ]}
          />
          <Select
            label="Method"
            align="left"
            value={filters.method}
            onChange={pick("method")}
            options={[{ value: "", label: "All methods" }, ...["GET", "POST", "PUT", "PATCH", "DELETE"].map((m) => ({ value: m, label: m }))]}
          />
          <Select
            label="Route"
            align="left"
            value={filters.route}
            onChange={pick("route")}
            maxWidth={280}
            options={[{ value: "", label: "All routes" }, ...(meta?.routes ?? []).map((r) => ({ value: r, label: r }))]}
          />
          <input className="input" placeholder="User ID or @handle" value={filters.user} onChange={set("user")} style={{ width: 170 }} aria-label="User" />
          <div className="search">
            <Search size={14} />
            <input className="input" placeholder="Path contains, or exact request ID" value={filters.search} onChange={set("search")} aria-label="Search" />
          </div>
          {active && (
            <button
              className="btn ghost"
              onClick={() => {
                setFilters(EMPTY);
                setDebounced(EMPTY);
              }}
            >
              Clear filters
            </button>
          )}
        </div>

        <section className="panel logs-panel" aria-busy={first.loading || undefined}>
          {first.loading && <div className="progress" aria-hidden />}
          {first.error && !merged && <div className="panel-state error">{linkify(first.error.message)}</div>}
          {merged && (
            <div className={`logs-body ${first.loading ? "refetching" : ""}`} style={{ transition: "opacity 160ms" }}>
              <DataTable
                result={merged}
                columns={logColumns}
                sortable={false}
                rowKey={(r) => r[0]}
                selectedKey={selected ?? undefined}
                onRowClick={(r) => setSelected(Number(r[0]))}
              />
              {merged.truncated && (
                <div className="load-more">
                  <button className="btn" onClick={loadMore} disabled={loadingMore}>
                    {loadingMore ? "Loading…" : "Load older requests"}
                  </button>
                </div>
              )}
            </div>
          )}
          {!merged && !first.error && <div className="panel-state" style={{ height: 320 }} />}
        </section>
      </div>
      {selected !== null && <LogDetail id={selected} onClose={() => setSelected(null)} />}
    </>
  );
}

function StatusBadge({ code }: { code: unknown }) {
  const n = toNumber(code) ?? 0;
  return (
    <span className={`status s${Math.floor(n / 100)}`}>
      <span className="dot" aria-hidden />
      {String(code)}
    </span>
  );
}

const logColumns: Column[] = [
  { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 170 },
  { key: "method", render: (v) => <span className="method">{String(v)}</span>, width: 70 },
  { key: "status", render: (v) => <StatusBadge code={v} />, width: 72 },
  {
    key: "path",
    render: (v, row) => (
      <span className="mono" title={String(row[4] ?? "")}>
        {String(v)}
      </span>
    ),
  },
  { key: "duration_ms", label: "Duration", align: "right", render: (v) => formatMs(toNumber(v) ?? 0), width: 90 },
  { key: "response_size", label: "Size", align: "right", render: (v) => <span className="dim">{formatBytes(toNumber(v) ?? 0)}</span>, width: 80 },
  {
    key: "user_handle",
    label: "User",
    render: (v, row) => (v ? `@${String(v)}` : row[7] != null ? <span className="dim">#{String(row[7])}</span> : <span className="dim">—</span>),
    width: 140,
  },
  { key: "error_key", label: "Error", render: (v) => (v ? <span className="mono">{String(v)}</span> : <span className="dim">—</span>) },
];

export function LogDetail({ id, onClose }: { id: number; onClose: () => void }) {
  const q = useQuery(`log-${id}`, (signal) => api<Result>(`/api/logs/${id}`, { signal }));
  const [tab, setTab] = useState<"request" | "response" | "headers">("response");
  const rec = useMemo(() => {
    if (!q.data || q.data.rows.length === 0) return null;
    const o: Record<string, unknown> = {};
    q.data.columns.forEach((c, i) => (o[c] = q.data!.rows[0][i]));
    return o;
  }, [q.data]);

  const body = (v: unknown) => pretty(v);

  return (
    <Drawer
      onClose={onClose}
      title={
        rec ? (
          <span style={{ display: "inline-flex", gap: 10, alignItems: "center" }}>
            <span className="method">{String(rec.method)}</span>
            <span className="mono">{String(rec.path)}</span>
          </span>
        ) : (
          "Request"
        )
      }
    >
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {!rec && !q.error && <div className="panel-state">Loading…</div>}
      {rec && (
        <>
          <dl className="kv">
            <dt>Status</dt>
            <dd>
              <StatusBadge code={rec.status} />
              {rec.error_key ? <span className="mono" style={{ marginLeft: 10 }}>{String(rec.error_key)}</span> : null}
              {rec.error_code ? <span className="dim mono" style={{ marginLeft: 8 }}>{String(rec.error_code)}</span> : null}
            </dd>
            <dt>Time</dt>
            <dd>{formatFull(new Date(String(rec.created_at)))}</dd>
            <dt>Duration</dt>
            <dd>{formatMs(toNumber(rec.duration_ms) ?? 0)}</dd>
            <dt>Route</dt>
            <dd className="mono">{String(rec.route ?? "—")}</dd>
            {rec.query ? (
              <>
                <dt>Query</dt>
                <dd className="mono">{String(rec.query)}</dd>
              </>
            ) : null}
            <dt>User</dt>
            <dd>
              {rec.user_id != null ? `#${String(rec.user_id)}` : "Anonymous"}
              {rec.user_handle ? ` · @${String(rec.user_handle)}` : ""}
              {rec.user_role ? <span className="pill" style={{ marginLeft: 8 }}>{String(rec.user_role)}</span> : null}
            </dd>
            <dt>Client</dt>
            <dd>
              <span className="mono">{String(rec.ip ?? "—")}</span>
              <div className="dim" style={{ marginTop: 2 }}>{String(rec.user_agent ?? "")}</div>
            </dd>
            {rec.service || rec.version ? (
              <>
                <dt>Service</dt>
                <dd>
                  {rec.service ? String(rec.service) : <span className="dim">—</span>}
                  {rec.version ? (
                    <span className="pill mono" style={{ marginLeft: 8 }} title={String(rec.version)}>
                      {shortVersion(String(rec.version))}
                    </span>
                  ) : null}
                </dd>
              </>
            ) : null}
            <dt>Request ID</dt>
            <dd className="mono">{String(rec.request_id)}</dd>
          </dl>
          <RequestTelemetry rec={rec} />
          <div className="tabs" role="tablist">
            {(["response", "request", "headers"] as const).map((t) => (
              <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? "on" : ""} onClick={() => setTab(t)}>
                {t === "response" ? "Response body" : t === "request" ? "Request body" : "Request headers"}
              </button>
            ))}
          </div>
          <BodyBlock
            text={body(tab === "response" ? rec.response_body : tab === "request" ? rec.request_body : rec.request_headers)}
            truncated={tab === "response" ? !!toNumber(rec.response_body_truncated) : tab === "request" ? !!toNumber(rec.request_body_truncated) : false}
            meta={
              tab === "response"
                ? `${String(rec.response_content_type ?? "")} · ${formatBytes(toNumber(rec.response_size) ?? 0)}`
                : tab === "request"
                  ? `${String(rec.request_content_type ?? "")} · ${formatBytes(toNumber(rec.request_size) ?? 0)}`
                  : ""
            }
          />
        </>
      )}
    </Drawer>
  );
}

function BodyBlock({ text, truncated, meta }: { text: string; truncated: boolean; meta: string }) {
  if (!text) return <div className="panel-state">Empty</div>;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <span className="dim" style={{ fontSize: 12 }}>
          {meta.replace(/^ · /, "")}
          {truncated ? " · truncated" : ""}
        </span>
        <CopyButton text={text} />
      </div>
      <pre className="code mono">{text}</pre>
    </div>
  );
}

function pretty(v: unknown): string {
  if (v === null || v === undefined || v === "") return "";
  const s = String(v);
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}
