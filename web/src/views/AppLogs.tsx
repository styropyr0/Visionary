import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  Bell,
  Bookmark,
  ChevronDown,
  ChevronRight,
  Columns3,
  Copy,
  Download,
  ExternalLink,
  HelpCircle,
  LayoutGrid,
  Minus,
  Pause,
  Play,
  Plus,
  Radio,
  Search,
  Sparkles,
  Trash2,
  X,
  ZoomOut,
} from "lucide-react";
import { ConnectCard } from "../components/ConnectCard";
import { useConnected } from "../lib/meta";
import { api, type Result } from "../lib/api";
import { formatBytes, formatCount } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatLogTime, type TimeRange } from "../lib/time";
import { Select } from "../components/Select";
import { CopyButton, Drawer, Modal } from "../components/Modal";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { useToast } from "../components/Toast";
import { ErrorNotice, linkify } from "../components/ErrorNotice";
import { useSession } from "../lib/team";
import { useMeta } from "../lib/meta";
import {
  LEVELS,
  LEVEL_LABEL,
  addTerm,
  alertFromSearch,
  fieldTerm,
  logSettingsApi,
  logsApi,
  logsHref,
  toLines,
  type FieldCount,
  type Level,
  type LogLine,
  type LogResult,
  type LogView,
} from "../lib/applogs";
import { LogDetail } from "./Logs";
import { AddToDashboard } from "../components/team/AddToDashboard";
import { CompareTab, HiddenTab, PatternsTab, VolumeTab } from "./AppLogs.insights";
import { copyText } from "../lib/clipboard";
import "./AppLogs.css";

type Tab = "search" | "patterns" | "volume" | "hidden" | "releases";

const TABS: { key: Tab; label: string }[] = [
  { key: "search", label: "Search" },
  { key: "patterns", label: "Patterns" },
  { key: "volume", label: "Volume" },
  { key: "hidden", label: "Hidden failures" },
  { key: "releases", label: "Releases" },
];

const LIVE_MAX = 2000;

/** The page's state in the address: #/applogs?q=…&tab=…&from=…&to=… */
function readHash(): { q: string; tab: Tab; from?: string; to?: string } {
  const p = new URLSearchParams(window.location.hash.split("?")[1] ?? "");
  const tab = (TABS.find((t) => t.key === p.get("tab"))?.key ?? "search") as Tab;
  return { q: p.get("q") ?? "", tab, from: p.get("from") ?? undefined, to: p.get("to") ?? undefined };
}

function writeHash(q: string, tab: Tab) {
  const p = new URLSearchParams();
  if (q) p.set("q", q);
  if (tab !== "search") p.set("tab", tab);
  const qs = p.toString();
  window.history.replaceState(null, "", `#/applogs${qs ? `?${qs}` : ""}`);
}

function loadColumns(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem("va.logs.columns") ?? "[]");
    return Array.isArray(v) ? v.filter((x) => typeof x === "string").slice(0, 12) : [];
  } catch {
    return [];
  }
}

function saveColumns(cols: string[]) {
  try {
    localStorage.setItem("va.logs.columns", JSON.stringify(cols));
  } catch {
    // localStorage may be unavailable.
  }
}

export function AppLogs() {
  const connected = useConnected("logs");
  const { setRange } = useApp();
  const initial = useMemo(readHash, []);
  const [tab, setTabState] = useState<Tab>(initial.tab);
  const [q, setQ] = useState(initial.q);

  // A linked range (from an error, a deploy, a chart) pins it.
  useEffect(() => {
    if (initial.from && initial.to) setRange({ kind: "absolute", from: initial.from, to: initial.to });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Links inside the app change the hash while the page is open.
  useEffect(() => {
    const on = () => {
      const h = readHash();
      setQ(h.q);
      setTabState(h.tab);
      if (h.from && h.to) setRange({ kind: "absolute", from: h.from, to: h.to });
    };
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, [setRange]);

  const setTab = (t: Tab) => {
    setTabState(t);
    writeHash(q, t);
  };
  const search = (next: string) => {
    setQ(next);
    writeHash(next, tab);
  };
  const openSearch = (next: string) => {
    setQ(next);
    setTabState("search");
    writeHash(next, "search");
  };

  return (
    <>
      <TopBar
        title="Logs"
        section="Investigate"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl />
          </>
        }
      />
      <div className="content lg-content">
        {connected ? (
          <>
            <div className="tabs" role="tablist">
              {TABS.map((t) => (
                <button type="button" key={t.key} role="tab" aria-selected={tab === t.key} className={tab === t.key ? "on" : ""} onClick={() => setTab(t.key)}>
                  {t.label}
                </button>
              ))}
            </div>
            {tab === "search" && <SearchTab q={q} onSearch={search} />}
            {tab === "patterns" && <PatternsTab q={q} onSearch={openSearch} />}
            {tab === "volume" && <VolumeTab q={q} onSearch={openSearch} />}
            {tab === "hidden" && <HiddenTab q={q} onSearch={openSearch} />}
            {tab === "releases" && <CompareTab onSearch={openSearch} />}
          </>
        ) : (
          <ConnectCard
            title="Send your application's logs here"
            text="Every line your services log, searchable, linked to the request that wrote it, grouped into patterns and with alerts. Turn on log capture in an SDK, or send logs from OpenTelemetry, Fluent Bit, Vector, Filebeat, Promtail, syslog or the host agent."
          />
        )}
      </div>
    </>
  );
}

function SearchTab({ q, onSearch }: { q: string; onSearch: (q: string) => void }) {
  const { range, resolved, tick, setRange } = useApp();
  const meta = useMeta();
  const toast = useToast();
  const { can } = useSession();
  const [draft, setDraft] = useState(q);
  const [level, setLevel] = useState("");
  const [service, setService] = useState("");
  const [columns, setColumns] = useState<string[]>(loadColumns);
  const [pages, setPages] = useState<LogResult[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  // #/applogs?line=123 opens that line.
  const [selected, setSelected] = useState<LogLine | null>(() => {
    const id = Number(new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("line"));
    return id > 0 ? ({ id } as LogLine) : null;
  });
  const [live, setLive] = useState(false);
  const [paused, setPaused] = useState(false);
  const [liveLines, setLiveLines] = useState<LogLine[]>([]);
  const [showHelp, setShowHelp] = useState(false);
  const [asking, setAsking] = useState(false);
  const [explain, setExplain] = useState<{ loading: boolean; text?: string; error?: string } | null>(null);
  const [adding, setAdding] = useState(false);

  useEffect(() => setDraft(q), [q]);

  useEffect(() => {
    const on = () => {
      const id = Number(new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("line"));
      if (id > 0) setSelected({ id } as LogLine);
    };
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  const full = useMemo(() => [q, level, service ? fieldTerm("service", service) : ""].filter(Boolean).join(" "), [q, level, service]);
  const key = `${resolved.from.getTime()}-${resolved.to.getTime()}-${tick}-${full}`;
  const first = useQuery(live ? null : `applogs:${key}`, (signal) => logsApi.list(resolved.from, resolved.to, full, {}, signal));
  const hist = useQuery(`applogs-hist:${key}`, (signal) => logsApi.histogram(resolved.from, resolved.to, full, signal));
  const fields = useQuery(`applogs-fields:${key}`, (signal) => logsApi.fields(resolved.from, resolved.to, full, signal));
  const services = useQuery(`applogs-services:${resolved.from.getTime()}-${tick}`, () => logsApi.values(resolved.from, resolved.to, "", "service"));

  useEffect(() => setPages(first.data ? [first.data] : []), [first.data]);

  const lines = useMemo(() => (live ? liveLines : pages.flatMap((p) => toLines(p))), [live, liveLines, pages]);
  const truncated = !live && pages.length > 0 && pages[pages.length - 1].truncated;
  const terms = first.data?.terms ?? [];

  const loadMore = async () => {
    const last = lines[lines.length - 1];
    if (!last || loadingMore) return;
    setLoadingMore(true);
    try {
      const next = await logsApi.list(resolved.from, resolved.to, full, { before: String(last.id) });
      setPages((p) => [...p, next]);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setLoadingMore(false);
    }
  };

  const pausedRef = useRef(paused);
  pausedRef.current = paused;
  useEffect(() => {
    if (!live) return;
    setLiveLines([]);
    const es = new EventSource(logsApi.tailUrl(full));
    es.addEventListener("lines", (ev) => {
      if (pausedRef.current) return;
      const res = JSON.parse((ev as MessageEvent).data) as Result;
      const incoming = toLines(res).reverse();
      setLiveLines((cur) => [...incoming, ...cur].slice(0, LIVE_MAX));
    });
    es.addEventListener("end", () => {
      setLive(false);
      toast("ok", "Live tail stopped after 30 minutes.");
    });
    es.onerror = () => {
      // EventSource reconnects on its own.
    };
    return () => es.close();
  }, [live, full, toast]);

  const toggleColumn = (c: string) => {
    const next = columns.includes(c) ? columns.filter((x) => x !== c) : [...columns, c].slice(0, 12);
    setColumns(next);
    saveColumns(next);
  };

  const apply = () => onSearch(draft.trim());
  const ask = async () => {
    if (!draft.trim()) return;
    setAsking(true);
    try {
      const out = await logsApi.ask(draft.trim());
      onSearch(out.query);
      if (out.since) setRange({ kind: "relative", key: out.since as never });
      if (out.note) toast("ok", out.note);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setAsking(false);
    }
  };
  const runExplain = async () => {
    setExplain({ loading: true });
    try {
      const out = await logsApi.explain(resolved.from, resolved.to, full);
      setExplain({ loading: false, text: out.summary });
    } catch (e) {
      setExplain({ loading: false, error: e instanceof Error ? e.message : String(e) });
    }
  };

  const filterBy = (field: string, value: unknown, exclude = false) => onSearch(addTerm(q, fieldTerm(field, value, exclude)));
  // Zoom out steps back through earlier zooms, then doubles the span.
  const zooms = useRef<{ from: TimeRange; to: TimeRange }[]>([]);
  const zoom = (from: Date, to: Date) => {
    const next: TimeRange = { kind: "absolute", from: from.toISOString(), to: to.toISOString() };
    zooms.current.push({ from: range, to: next });
    setRange(next);
  };
  const zoomOut = () => {
    const last = zooms.current.pop();
    if (last && last.to === range) {
      setRange(last.from);
      return;
    }
    zooms.current = [];
    const span = resolved.to.getTime() - resolved.from.getTime();
    const end = Math.min(Date.now(), resolved.to.getTime() + span / 2);
    setRange({ kind: "absolute", from: new Date(end - 2 * span).toISOString(), to: new Date(end).toISOString() });
  };
  const total = useMemo(() => (hist.data?.rows ?? []).reduce((a, r) => a + Number(r[2] ?? 0), 0), [hist.data]);

  return (
    <div className="lg-search">
      <div className="lg-querybar" role="search">
        <div className="search lg-q">
          <Search size={14} />
          <input
            className="input mono"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") apply();
            }}
            placeholder={`timeout level:error+ service:api -healthz order_id:42`}
            aria-label="Search logs"
            spellCheck={false}
          />
          {draft && (
            <button type="button" className="icon-btn" aria-label="Clear the search" onClick={() => onSearch("")}>
              <X size={14} />
            </button>
          )}
        </div>
        <button type="button" className="btn primary" onClick={apply}>
          Search
        </button>
        {meta?.ask_enabled && (
          <button type="button" className="btn" onClick={ask} disabled={asking || !draft.trim()} title="Turn a question in words into a search">
            <Sparkles size={14} />
            {asking ? "Asking…" : "Ask"}
          </button>
        )}
        <button type="button" className="icon-btn" aria-label="Search syntax" title="Search syntax" onClick={() => setShowHelp(true)}>
          <HelpCircle size={15} />
        </button>
      </div>

      <div className="filter-row lg-filters">
        <Select
          label="Level"
          align="left"
          value={level}
          onChange={setLevel}
          options={[
            { value: "", label: "All levels" },
            { value: "level:warn+", label: "Warn and worse" },
            { value: "level:error+", label: "Error and worse" },
            ...LEVELS.map((l) => ({ value: `level:${l}`, label: `${LEVEL_LABEL[l]} only` })),
          ]}
        />
        <Select
          label="Service"
          align="left"
          value={service}
          onChange={setService}
          maxWidth={240}
          options={[{ value: "", label: "All services" }, ...(services.data?.values ?? []).map((v) => ({ value: String(v.value), label: String(v.value) }))]}
        />
        <SavedSearches q={q} columns={columns} onPick={(v) => {
          onSearch(v.query);
          if (v.columns.length) {
            setColumns(v.columns);
            saveColumns(v.columns);
          }
        }} />
        <div className="lg-filters-end">
          <button type="button" className={`btn ${live ? "primary" : ""}`} onClick={() => {
            setLive(!live);
            setPaused(false);
          }} title="Show new lines as they arrive">
            <Radio size={14} />
            {live ? "Live" : "Live tail"}
          </button>
          {live && (
            <button type="button" className="btn" onClick={() => setPaused(!paused)}>
              {paused ? <Play size={14} /> : <Pause size={14} />}
              {paused ? "Resume" : "Pause"}
            </button>
          )}
          {meta?.ask_enabled && (
            <button type="button" className="btn" onClick={runExplain} disabled={explain?.loading} title="Have the analyst read the matching lines">
              <Sparkles size={14} />
              Explain
            </button>
          )}
          <ExportMenu from={resolved.from} to={resolved.to} q={full} />
        </div>
      </div>

      {explain && (
        <div className="panel lg-explain" role="status">
          <div className="lg-explain-head">
            <Sparkles size={15} />
            <strong>What these lines say</strong>
            <button type="button" className="icon-btn" aria-label="Close" onClick={() => setExplain(null)}>
              <X size={14} />
            </button>
          </div>
          {explain.loading && <div className="dim">Reading the lines…</div>}
          {explain.error && <div className="panel-state error">{explain.error}</div>}
          {explain.text && <ExplainText text={explain.text} onOpen={(id) => setSelected({ id } as LogLine)} />}
        </div>
      )}

      <section className="panel lg-hist-panel">
        <div className="lg-hist-head">
          <span>
            {live ? (
              <span className="lg-live-dot">Live · {formatCount(liveLines.length)} new {liveLines.length === 1 ? "line" : "lines"}</span>
            ) : (
              <>
                <strong>{formatCount(total)}</strong> <span className="dim">{total === 1 ? "line" : "lines"}</span>
              </>
            )}
          </span>
          <span className="lg-hist-actions">
            <span className="dim lg-hist-hint">Drag across the chart to zoom in</span>
            <button type="button" className="btn sm" onClick={zoomOut}>
              <ZoomOut size={14} /> Zoom out
            </button>
            {can("edit") && (
              <button type="button" className="icon-btn" title="Add to dashboard" aria-label="Add to dashboard" onClick={() => setAdding(true)}>
                <LayoutGrid size={15} />
              </button>
            )}
          </span>
        </div>
        {hist.error ? <div className="panel-state error">{linkify(hist.error.message)}</div> : <Histogram result={hist.data} from={resolved.from} to={resolved.to} onZoom={zoom} />}
      </section>

      <div className="lg-body">
        <FieldSidebar
          fields={fields.data?.fields}
          loading={fields.loading}
          from={resolved.from}
          to={resolved.to}
          q={full}
          columns={columns}
          onToggleColumn={toggleColumn}
          onFilter={filterBy}
        />
        <section className="panel lg-table-panel" aria-busy={first.loading || undefined}>
          {first.loading && !live && <div className="progress" aria-hidden />}
          {first.error && !live ? (
            <div className="panel-state error">{linkify(first.error.message)}</div>
          ) : (
            <LogTable lines={lines} terms={terms} columns={columns} selected={selected?.id} onOpen={setSelected} empty={live ? "Waiting for new lines…" : first.loading ? "" : "No lines match in this range."} />
          )}
          {truncated && (
            <div className="load-more">
              <button type="button" className="btn" onClick={loadMore} disabled={loadingMore}>
                {loadingMore ? "Loading…" : "Load older lines"}
              </button>
            </div>
          )}
        </section>
      </div>

      {selected && <LogLineDrawer id={selected.id} canEdit={can("edit")} onClose={() => setSelected(null)} onSearch={(next) => {
        setSelected(null);
        onSearch(next);
      }} onFilter={filterBy} />}
      {showHelp && <SyntaxHelp onClose={() => setShowHelp(false)} />}
      {adding && (
        <AddToDashboard
          title={full ? `Logs: ${full}` : "Log lines by level"}
          source={{
            kind: "builtin",
            ref: "applogs/histogram",
            title: full ? `Logs: ${full}` : "Log lines by level",
            description: full ? `Lines matching ${full}, by level.` : "Application log lines per interval, stacked by level.",
            ...(full ? { params: { q: full } } : {}),
          }}
          onClose={() => setAdding(false)}
        />
      )}
    </div>
  );
}

function ExplainText({ text, onOpen }: { text: string; onOpen: (id: number) => void }) {
  return (
    <div className="lg-explain-text">
      {text.split("\n").map((line, i) => (
        <p key={i}>
          {line.split(/(#\d+)/).map((part, j) =>
            /^#\d+$/.test(part) ? (
              <button type="button" key={j} className="link-btn mono" onClick={() => onOpen(Number(part.slice(1)))}>
                {part}
              </button>
            ) : (
              <Fragment key={j}>{part}</Fragment>
            ),
          )}
        </p>
      ))}
    </div>
  );
}

function ExportMenu({ from, to, q }: { from: Date; to: Date; q: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="lg-menu">
      <button type="button" className="btn" onClick={() => setOpen(!open)} aria-expanded={open}>
        <Download size={14} />
        Export
      </button>
      {open && (
        <div className="lg-menu-pop" role="menu" onMouseLeave={() => setOpen(false)}>
          <a role="menuitem" href={logsApi.exportUrl(from, to, q, "csv")} onClick={() => setOpen(false)}>
            CSV
          </a>
          <a role="menuitem" href={logsApi.exportUrl(from, to, q, "ndjson")} onClick={() => setOpen(false)}>
            NDJSON
          </a>
          <span className="dim lg-menu-note">Up to 50 000 lines, newest first</span>
        </div>
      )}
    </div>
  );
}

function SavedSearches({ q, columns, onPick }: { q: string; columns: string[]; onPick: (v: LogView) => void }) {
  const toast = useToast();
  const { can } = useSession();
  const [run, setRun] = useState(0);
  const views = useQuery(`log-views#${run}`, () => logsApi.views());
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState("");
  const list = views.data?.views ?? [];
  const save = async () => {
    try {
      await logsApi.saveView({ name: name.trim(), query: q, columns });
      setNaming(false);
      setName("");
      setRun((n) => n + 1);
      toast("ok", "Search saved.");
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  const remove = async (v: LogView) => {
    try {
      await logsApi.deleteView(v.id);
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <div className="lg-saved">
      <Select
        label="Saved searches"
        align="left"
        value=""
        onChange={(id) => {
          const v = list.find((x) => String(x.id) === id);
          if (v) onPick(v);
        }}
        options={[{ value: "", label: list.length ? "Saved searches" : "No saved searches" }, ...list.map((v) => ({ value: String(v.id), label: v.name }))]}
      />
      {can("edit") && (
        <button type="button" className="btn" disabled={!q} onClick={() => setNaming(true)} title="Save this search for the team">
          <Bookmark size={14} />
          Save
        </button>
      )}
      {naming && (
        <Modal title="Save this search" onClose={() => setNaming(false)}>
          <div className="lg-save-form">
            <label className="field">
              <span>Name</span>
              <input className="input" autoFocus value={name} maxLength={120} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && name.trim() && save()} />
            </label>
            <div className="mono dim lg-save-q">{q}</div>
            {list.length > 0 && (
              <ul className="lg-saved-list">
                {list.map((v) => (
                  <li key={v.id}>
                    <span>{v.name}</span>
                    <span className="mono dim">{v.query}</span>
                    <button type="button" className="icon-btn" aria-label={`Delete ${v.name}`} onClick={() => remove(v)}>
                      <Trash2 size={13} />
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <div className="modal-actions">
              <button type="button" className="btn" onClick={() => setNaming(false)}>
                Cancel
              </button>
              <button type="button" className="btn primary" disabled={!name.trim()} onClick={save}>
                Save
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}

/** Drag across it to zoom the range. */
function Histogram({ result, from, to, onZoom }: { result?: Result; from: Date; to: Date; onZoom: (from: Date, to: Date) => void }) {
  const bucket = result?.bucket ?? 60;
  const buckets = useMemo(() => {
    const start = Math.floor(from.getTime() / 1000 / bucket) * bucket;
    const n = Math.max(1, Math.ceil((to.getTime() / 1000 - start) / bucket));
    const out = Array.from({ length: Math.min(n, 400) }, (_, i) => ({ t: start + i * bucket, by: {} as Partial<Record<Level, number>>, total: 0 }));
    for (const row of result?.rows ?? []) {
      const i = Math.floor((Number(row[0]) - start) / bucket);
      const b = out[i];
      if (!b) continue;
      const lv = String(row[1]) as Level;
      const n2 = Number(row[2] ?? 0);
      b.by[lv] = (b.by[lv] ?? 0) + n2;
      b.total += n2;
    }
    return out;
  }, [result, from, to, bucket]);
  const max = Math.max(1, ...buckets.map((b) => b.total));
  const [drag, setDrag] = useState<{ a: number; b: number } | null>(null);
  const [hover, setHover] = useState<number | null>(null);
  const done = () => {
    if (drag) {
      const lo = Math.min(drag.a, drag.b);
      const hi = Math.max(drag.a, drag.b);
      const f = new Date(buckets[lo].t * 1000);
      const t = new Date((buckets[hi].t + bucket) * 1000);
      if (hi > lo || buckets.length > 1) onZoom(f, t);
    }
    setDrag(null);
  };
  const h = hover !== null ? buckets[hover] : null;
  return (
    <div className="lg-hist" onMouseLeave={() => {
      setHover(null);
      setDrag(null);
    }} onMouseUp={done} role="img" aria-label="Log lines over time by level">
      {buckets.map((b, i) => {
        const inDrag = drag && i >= Math.min(drag.a, drag.b) && i <= Math.max(drag.a, drag.b);
        return (
          <div
            key={b.t}
            className={`lg-hist-col ${inDrag ? "sel" : ""}`}
            onMouseDown={(e) => {
              e.preventDefault();
              setDrag({ a: i, b: i });
            }}
            onMouseEnter={() => {
              setHover(i);
              if (drag) setDrag({ ...drag, b: i });
            }}
          >
            <div className="lg-hist-stack" style={{ height: `${(100 * b.total) / max}%` }}>
              {[...LEVELS].reverse().map((l) =>
                b.by[l] ? <span key={l} className={`lv-bg-${l}`} style={{ flexGrow: b.by[l] }} /> : null,
              )}
            </div>
          </div>
        );
      })}
      {h && (
        <div className="lg-hist-tip">
          <div className="dim">{formatFull(new Date(h.t * 1000))}</div>
          <div>
            <strong>{formatCount(h.total)}</strong> {h.total === 1 ? "line" : "lines"}
          </div>
          {LEVELS.filter((l) => h.by[l]).map((l) => (
            <div key={l} className="lg-hist-tip-row">
              <span className={`lg-dot lv-bg-${l}`} />
              {LEVEL_LABEL[l]} {formatCount(h.by[l] ?? 0)}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function FieldSidebar({
  fields,
  loading,
  from,
  to,
  q,
  columns,
  onToggleColumn,
  onFilter,
}: {
  fields?: FieldCount[];
  loading: boolean;
  from: Date;
  to: Date;
  q: string;
  columns: string[];
  onToggleColumn: (c: string) => void;
  onFilter: (field: string, value: unknown, exclude?: boolean) => void;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  const shown = (fields ?? []).filter((f) => !filter || f.name.toLowerCase().includes(filter.toLowerCase()));
  return (
    <aside className="panel lg-fields" aria-label="Fields">
      <div className="lg-fields-head">
        <span className="panel-title">Fields</span>
        <input className="input lg-fields-filter" placeholder="Filter" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter fields" />
      </div>
      {loading && !fields && <div className="dim lg-fields-empty">Loading…</div>}
      {fields && shown.length === 0 && <div className="dim lg-fields-empty">No fields</div>}
      <ul className="lg-fields-list">
        {shown.map((f) => (
          <li key={f.name}>
            <div className="lg-field">
              <button type="button" className="lg-field-name" onClick={() => setOpen(open === f.name ? null : f.name)} aria-expanded={open === f.name}>
                {open === f.name ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                <span className={`mono ${f.column ? "" : "lg-attr"}`}>{f.name}</span>
              </button>
              <span className="dim lg-field-count">{formatCount(f.count)}</span>
              {f.name !== "level" && (
                <button
                  type="button"
                  className={`icon-btn lg-col-toggle ${columns.includes(f.name) ? "on" : ""}`}
                  aria-label={columns.includes(f.name) ? `Hide the ${f.name} column` : `Show ${f.name} as a column`}
                  title={columns.includes(f.name) ? "Hide column" : "Show as a column"}
                  onClick={() => onToggleColumn(f.name)}
                >
                  <Columns3 size={12} />
                </button>
              )}
            </div>
            {open === f.name && <FieldValues field={f.name} from={from} to={to} q={q} onFilter={onFilter} />}
          </li>
        ))}
      </ul>
    </aside>
  );
}

function FieldValues({ field, from, to, q, onFilter }: { field: string; from: Date; to: Date; q: string; onFilter: (field: string, value: unknown, exclude?: boolean) => void }) {
  const values = useQuery(`field-values:${field}:${from.getTime()}:${to.getTime()}:${q}`, () => logsApi.values(from, to, q, field));
  if (values.error) return <div className="panel-state error">{values.error.message}</div>;
  if (!values.data) return <div className="dim lg-values-empty">Loading…</div>;
  if (values.data.values.length === 0) return <div className="dim lg-values-empty">No values</div>;
  const total = Math.max(1, values.data.total);
  return (
    <ul className="lg-values">
      {values.data.values.map((v) => (
        <li key={String(v.value)}>
          <span className="lg-value-bar" style={{ width: `${(100 * v.count) / total}%` }} aria-hidden />
          <span className="mono lg-value" title={String(v.value)}>
            {String(v.value)}
          </span>
          <span className="dim">{formatCount(v.count)}</span>
          <button type="button" className="icon-btn" aria-label={`Only ${field} ${String(v.value)}`} title="Only this" onClick={() => onFilter(field, v.value)}>
            <Plus size={12} />
          </button>
          <button type="button" className="icon-btn" aria-label={`Leave out ${field} ${String(v.value)}`} title="Leave out" onClick={() => onFilter(field, v.value, true)}>
            <Minus size={12} />
          </button>
        </li>
      ))}
    </ul>
  );
}

export function LevelBadge({ level }: { level: Level }) {
  return <span className={`lg-level lv-${level}`}>{level}</span>;
}

function Highlight({ text, terms }: { text: string; terms: string[] }) {
  const words = terms.filter((t) => t.length > 0);
  if (words.length === 0) return <>{text}</>;
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})`, "gi");
  return (
    <>
      {text.split(re).map((part, i) => (i % 2 === 1 ? <mark key={i}>{part}</mark> : <Fragment key={i}>{part}</Fragment>))}
    </>
  );
}

function cellValue(line: LogLine, col: string): string {
  const v = (line as unknown as Record<string, unknown>)[col];
  if (v !== undefined && col !== "attrs") return v === null ? "" : String(v);
  const a = line.attrs[col];
  return a === undefined || a === null ? "" : typeof a === "object" ? JSON.stringify(a) : String(a);
}

export function LogTable({
  lines,
  terms = [],
  columns = [],
  selected,
  onOpen,
  empty,
  compact,
  focus,
}: {
  lines: LogLine[];
  terms?: string[];
  columns?: string[];
  selected?: number;
  onOpen?: (l: LogLine) => void;
  empty?: string;
  compact?: boolean;
  focus?: number;
}) {
  if (lines.length === 0) return <div className="panel-state lg-empty">{empty}</div>;
  return (
    <div className={`lg-table ${compact ? "compact" : ""}`} role="table" aria-label="Log lines">
      <div className="lg-row lg-head" role="row">
        <span role="columnheader">Time</span>
        <span role="columnheader">Level</span>
        {!compact && (
          <span role="columnheader" className="lg-svc">
            Service
          </span>
        )}
        {columns.map((c) => (
          <span role="columnheader" key={c} className="mono lg-col">
            {c}
          </span>
        ))}
        <span role="columnheader" className="lg-msg">
          Message
        </span>
      </div>
      {lines.map((l) => {
        const first = l.message.split("\n")[0];
        const more = l.message.length > first.length;
        return (
          <div
            key={l.id}
            role="row"
            tabIndex={onOpen ? 0 : undefined}
            className={`lg-row ${selected === l.id ? "selected" : ""} ${focus === l.id ? "focus" : ""} lv-row-${l.level}`}
            onClick={() => onOpen?.(l)}
            onKeyDown={(e) => {
              if (e.key === "Enter") onOpen?.(l);
            }}
          >
            <span role="cell" className="mono dim lg-time">
              {formatLogTime(l.created_at)}
            </span>
            <span role="cell">
              <LevelBadge level={l.level} />
            </span>
            {!compact && (
              <span role="cell" className="lg-svc" title={l.service ?? ""}>
                {l.service ?? <span className="dim">—</span>}
              </span>
            )}
            {columns.map((c) => (
              <span role="cell" key={c} className="mono lg-col" title={cellValue(l, c)}>
                {cellValue(l, c) || <span className="dim">—</span>}
              </span>
            ))}
            <span role="cell" className="mono lg-msg" data-line={l.id}>
              <Highlight text={first} terms={terms} />
              {more && <span className="dim lg-more"> …</span>}
              {l.request_id && <span className="lg-tag">request</span>}
            </span>
          </div>
        );
      })}
    </div>
  );
}

const COLUMN_FIELDS = ["service", "source", "host", "container", "logger", "instance", "version", "request_id", "trace_id", "span_id", "user_id", "session_id", "code_file", "code_line", "code_func", "pattern"];

/** Columns whose search field has a different name. */
const FILTER_FIELD: Record<string, string> = { request_id: "request", trace_id: "trace", span_id: "span", user_id: "user", session_id: "session", code_file: "file", code_line: "line", code_func: "func" };

export function LogLineDrawer({
  id,
  canEdit,
  onClose,
  onSearch,
  onFilter,
}: {
  id: number;
  canEdit: boolean;
  onClose: () => void;
  onSearch?: (q: string) => void;
  onFilter?: (field: string, value: unknown, exclude?: boolean) => void;
}) {
  const toast = useToast();
  const q = useQuery(`applog-${id}`, (signal) => logsApi.line(id, signal));
  const [context, setContext] = useState(false);
  const [request, setRequest] = useState<number | null>(null);
  const line = q.data?.line;
  const attrs = useMemo<Record<string, unknown>>(() => {
    if (typeof line?.attrs !== "string" || !line.attrs) return {};
    try {
      return JSON.parse(line.attrs);
    } catch {
      return {};
    }
  }, [line]);
  const level = (String(line?.level ?? "info") as Level) || "info";
  const search = (s: string) => (onSearch ? onSearch(s) : (window.location.hash = logsHref(s)));

  const openRequest = async () => {
    try {
      const t = await api<{ log_id: number | null }>(`/api/telemetry/requests/${encodeURIComponent(String(line?.request_id))}`);
      if (t.log_id) setRequest(Number(t.log_id));
      else toast("err", "The request this line belongs to isn't in Requests (it may be outside VISIONARY_LOG_PATH_PREFIX).");
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  const alertAgain = async () => {
    if (!line?.pattern) {
      toast("err", "This line has no pattern yet; it gets one within a few seconds.");
      return;
    }
    try {
      const tpl = q.data?.pattern?.template ?? String(line.message).slice(0, 60);
      const r = await alertFromSearch(`Again: ${tpl.slice(0, 80)}`, `pattern:${line.pattern}`, line.service ? String(line.service) : undefined);
      toast("ok", "Alert created. Pick where it notifies in Alerts.");
      window.location.hash = `#/alerts?rule=${r.id}`;
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  const moreDetail = async (scope: { service?: string; user?: string }) => {
    try {
      await logSettingsApi.addOverride({ ...scope, level: "debug", minutes: 15 });
      toast("ok", "Debug lines are kept for the next 15 minutes. Services pick this up within 30 seconds.");
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };

  if (request !== null) return <LogDetail id={request} onClose={() => setRequest(null)} />;

  return (
    <Drawer
      onClose={onClose}
      title={
        line ? (
          <span className="lg-drawer-title">
            <LevelBadge level={level} />
            <span className="mono">{formatFull(new Date(String(line.created_at)))}</span>
          </span>
        ) : (
          "Log line"
        )
      }
    >
      {q.error && <ErrorNotice title="Could not load the line" error={q.error} />}
      {!line && !q.error && <div className="panel-state">Loading…</div>}
      {line && (
        <div className="lg-detail">
          <div className="lg-detail-msg">
            <div className="lg-detail-msg-head">
              <span className="dim">Message</span>
              <CopyButton text={String(line.message)} />
            </div>
            <pre className="code mono">{String(line.message)}</pre>
          </div>

          <div className="lg-actions">
            <button type="button" className="btn" onClick={() => setContext(!context)}>
              {context ? "Hide the lines around it" : "Lines around it"}
            </button>
            {line.request_id ? (
              <button type="button" className="btn" onClick={openRequest}>
                <ExternalLink size={14} />
                Open its request
              </button>
            ) : null}
            {line.pattern ? (
              <button type="button" className="btn" onClick={() => search(`pattern:${line.pattern}`)}>
                Lines like this
              </button>
            ) : null}
            {canEdit && (
              <button type="button" className="btn" onClick={alertAgain}>
                <Bell size={14} />
                Alert when it happens again
              </button>
            )}
            {canEdit && line.service ? (
              <button type="button" className="btn" onClick={() => moreDetail({ service: String(line.service) })} title="Keep this service's debug lines for 15 minutes">
                Debug {String(line.service)} for 15 min
              </button>
            ) : null}
            {canEdit && line.user_id ? (
              <button type="button" className="btn" onClick={() => moreDetail({ user: String(line.user_id) })} title="Keep this user's debug lines for 15 minutes">
                Debug this user for 15 min
              </button>
            ) : null}
          </div>

          {context && <ContextLines id={id} />}

          {q.data?.pattern && (
            <div className="lg-pattern-box">
              <div className="dim">Pattern</div>
              <div className="mono">{q.data.pattern.template}</div>
              <div className="dim lg-pattern-meta">
                {formatCount(q.data.pattern.lines)} lines since {formatFull(new Date(q.data.pattern.first_seen))}
                {q.data.pattern.first_version ? ` · first in ${q.data.pattern.first_version}` : ""}
              </div>
            </div>
          )}

          <table className="lg-kv">
            <tbody>
              {COLUMN_FIELDS.filter((c) => line[c] !== null && line[c] !== undefined && line[c] !== "").map((c) => (
                <FieldRow key={c} name={c} value={line[c]} filterField={FILTER_FIELD[c] ?? c} onFilter={onFilter} />
              ))}
              {Object.keys(attrs)
                .sort()
                .map((k) => (
                  <FieldRow key={`a:${k}`} name={k} value={attrs[k]} filterField={k} onFilter={onFilter} attr />
                ))}
            </tbody>
          </table>
        </div>
      )}
    </Drawer>
  );
}

function FieldRow({ name, value, filterField, onFilter, attr }: { name: string; value: unknown; filterField: string; onFilter?: (field: string, value: unknown, exclude?: boolean) => void; attr?: boolean }) {
  const text = typeof value === "object" && value !== null ? JSON.stringify(value) : String(value);
  return (
    <tr>
      <th className={`mono ${attr ? "lg-attr" : ""}`}>{name}</th>
      <td className="mono">{text}</td>
      <td className="lg-kv-actions">
        {onFilter && (
          <>
            <button type="button" className="icon-btn" aria-label={`Only ${name} ${text}`} title="Only this" onClick={() => onFilter(filterField, value)}>
              <Plus size={12} />
            </button>
            <button type="button" className="icon-btn" aria-label={`Leave out ${name} ${text}`} title="Leave out" onClick={() => onFilter(filterField, value, true)}>
              <Minus size={12} />
            </button>
          </>
        )}
        <button
          type="button"
          className="icon-btn"
          aria-label={`Copy ${name}`}
          title="Copy"
          onClick={() => void copyText(text)}
        >
          <Copy size={12} />
        </button>
      </td>
    </tr>
  );
}

function ContextLines({ id }: { id: number }) {
  const ctx = useQuery(`applog-ctx-${id}`, () => logsApi.context(id, 25));
  const lines = useMemo(() => toLines(ctx.data?.result), [ctx.data]);
  if (ctx.error) return <div className="panel-state error">{ctx.error.message}</div>;
  if (!ctx.data) return <div className="dim">Loading…</div>;
  return (
    <div className="lg-context">
      <div className="dim lg-context-head">
        The lines logged just before and after, by the same {ctx.data.by}
        {ctx.data.value ? <span className="mono"> {String(ctx.data.value)}</span> : null}
      </div>
      <LogTable lines={lines} compact focus={id} empty="Nothing else was logged around it." />
    </div>
  );
}

const SYNTAX: [string, string][] = [
  ["timeout", "The message contains timeout (any case)"],
  ['"connection refused"', "The message contains the phrase"],
  ["level:error", "A field equals a value"],
  ["level:warn+", "That level or worse"],
  ["service:api*", "Starts with api"],
  ["order_id:42  user.plan:pro", "A field of the line, by its name"],
  ["http.status:>=500", "Compare numbers"],
  ["host:*", "The field is set"],
  ["-healthz  NOT level:debug", "Leave out"],
  ["a OR b  (a OR b) c", "Either; terms side by side must all match"],
  ["/time(d|o)ut/", "A regular expression"],
  ["request:abc123  trace:4bf9…", "Lines of one request or trace"],
  ["pattern:3f2a9c1e0b7d4a55", "Lines with the same message template"],
];

function SyntaxHelp({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="Search syntax" onClose={onClose}>
      <table className="lg-syntax">
        <tbody>
          {SYNTAX.map(([ex, what]) => (
            <tr key={ex}>
              <td className="mono">{ex}</td>
              <td>{what}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="dim lg-syntax-note">
        Fields are the line's columns (level, service, source, host, container, logger, version, request, trace, user, session, file, line) and any field the line
        carries. Click a value in Fields or in a line to add it to the search.
      </p>
    </Modal>
  );
}

export function LogsAround({
  at,
  scope,
  title = "Logs around this moment",
  beforeS = 120,
  afterS = 30,
  empty = "Nothing was logged around this moment.",
  extra,
}: {
  at: string;
  scope: Record<string, string>;
  title?: string;
  beforeS?: number;
  afterS?: number;
  empty?: string;
  extra?: ReactNode;
}) {
  const connected = useConnected("logs");
  const params = { at, before_s: String(beforeS), after_s: String(afterS), ...Object.fromEntries(Object.entries(scope).filter(([, v]) => v)) };
  const q = useQuery(connected ? `logs-around:${JSON.stringify(params)}` : null, (signal) => logsApi.around(params, signal));
  const [open, setOpen] = useState<number | null>(null);
  const lines = useMemo(() => toLines(q.data), [q.data]);
  const { can } = useSession();
  if (!connected) return null;
  const from = new Date(new Date(at).getTime() - beforeS * 1000);
  const to = new Date(new Date(at).getTime() + afterS * 1000);
  const search = Object.entries(scope)
    .filter(([, v]) => v)
    .map(([k, v]) => fieldTerm(k === "request_id" ? "request" : k, v))
    .join(" ");
  return (
    <section className="lg-around">
      <div className="lg-around-head">
        <h3 className="rt-title">{title}</h3>
        {extra}
        <a className="btn sm" href={logsHref(search, { from, to })}>
          Open in Logs
        </a>
      </div>
      {q.error ? <div className="panel-state error">{q.error.message}</div> : !q.data ? <div className="dim">Loading…</div> : <LogTable lines={lines} compact onOpen={(l) => setOpen(l.id)} empty={empty} />}
      {open !== null && <LogLineDrawer id={open} canEdit={can("edit")} onClose={() => setOpen(null)} />}
    </section>
  );
}

export function formatLogBytes(n: number): string {
  return formatBytes(n);
}
