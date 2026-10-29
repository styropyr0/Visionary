import { useCallback, useEffect, useMemo, useState } from "react";
import { LayoutGrid, Link2, Monitor, Pencil, Plus, Settings2, Trash2, X } from "lucide-react";
import { api, rangeQuery, type Panel as SavedPanel, type Result } from "../lib/api";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { PRESETS, formatRelative, rangeLabel, resolveRange, type Resolved, type TimeRange } from "../lib/time";
import {
  BUILTINS,
  BUILTIN_BY_REF,
  VAR_NAMES,
  dashboardLink,
  newPanelId,
  parseDashboardHash,
  placeAtBottom,
  saveDashboard,
  useSession,
  type Dashboard,
  type DashboardLayout,
  type DashPanel,
  type VarName,
} from "../lib/team";
import { DataTable } from "../components/DataTable";
import { Modal } from "../components/Modal";
import { Panel } from "../components/Panel";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView } from "../components/VizView";
import { DashGrid, resolveLayout } from "../components/team/DashGrid";
import { TvMode } from "../components/team/TvMode";
import { ErrorNotice } from "../components/ErrorNotice";
import { copyText } from "../lib/clipboard";
import "./Dashboards.css";

type Vars = Partial<Record<VarName, string>>;

function useHash() {
  const [hash, setHash] = useState(window.location.hash);
  useEffect(() => {
    const on = () => setHash(window.location.hash);
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return hash;
}

/** Appends tv=1 to a dashboard link. */
function tvLink(link: string): string {
  return `${link}${link.includes("?") ? "&" : "?"}tv=1`;
}

export function Dashboards() {
  const hash = useHash();
  const route = parseDashboardHash(hash);
  const [version, setVersion] = useState(0);
  const list = useQuery(`dashboards#${version}`, (signal) => api<{ dashboards: Dashboard[] }>("/api/dashboards", { signal }));
  const dashboards = list.data?.dashboards ?? [];
  const reload = useCallback(() => setVersion((v) => v + 1), []);

  if (route.id !== null) {
    return <DashboardView key={route.id} id={route.id} route={route} all={dashboards} onChanged={reload} />;
  }
  return <DashboardList list={dashboards} loading={list.loading} error={list.error} onChanged={reload} />;
}

// ---------- List ----------

function DashboardList({ list, loading, error, onChanged }: { list: Dashboard[]; loading: boolean; error?: Error; onChanged: () => void }) {
  const { can } = useSession();
  const [creating, setCreating] = useState(false);
  return (
    <>
      <TopBar
        title="Dashboards"
        section="Investigate"
        actions={
          can("edit") && (
            <button type="button" className="btn primary" onClick={() => setCreating(true)}>
              <Plus size={14} />
              New dashboard
            </button>
          )
        }
      />
      <div className="content">
        {error && <ErrorNotice title="Could not load dashboards" error={error} />}
        {!loading && !error && list.length === 0 && (
          <section className="panel">
            <div className="empty">
              <div className="empty-icon">
                <LayoutGrid size={20} />
              </div>
              <h3>No dashboards yet</h3>
              <p>
                Dashboards put saved panels and any built-in Overview, Performance or System panel side by side, with instance, route and
                service selectors and their own time range.
              </p>
              {can("edit") ? (
                <button type="button" className="btn primary" onClick={() => setCreating(true)}>
                  <Plus size={14} />
                  New dashboard
                </button>
              ) : (
                <p className="dim">Ask an editor to create one.</p>
              )}
            </div>
          </section>
        )}
        <div className="dash-cards">
          {list.map((d) => (
            <a key={d.id} className="dash-card" href={dashboardLink(d.id)}>
              <span className="dash-card-name">{d.name}</span>
              {d.description && <span className="dash-card-desc">{d.description}</span>}
              <span className="dash-card-meta">
                {d.layout.panels.length} {d.layout.panels.length === 1 ? "panel" : "panels"}
                {d.layout.variables.length > 0 && ` · ${d.layout.variables.map((v) => v.name).join(", ")}`}
                {` · updated ${formatRelative(d.updated_at)}`}
              </span>
            </a>
          ))}
        </div>
      </div>
      {creating && (
        <DashboardSettings
          title="New dashboard"
          initial={{ name: "", description: "", layout: { panels: [], variables: [] } }}
          onClose={() => setCreating(false)}
          onSave={async (d) => {
            const created = await saveDashboard(d);
            onChanged();
            window.location.hash = `${dashboardLink(created.id)}?edit=1`;
          }}
        />
      )}
    </>
  );
}

// ---------- One dashboard ----------

interface RouteInfo {
  from?: string;
  to?: string;
  vars: Vars;
  tv: boolean;
}

function DashboardView({ id, route, all, onChanged }: { id: number; route: RouteInfo; all: Dashboard[]; onChanged: () => void }) {
  const { can } = useSession();
  const toast = useToast();
  const app = useApp();
  const [version, setVersion] = useState(0);
  const loaded = useQuery(`dashboard-${id}#${version}`, (signal) => api<Dashboard>(`/api/dashboards/${id}`, { signal }));
  const saved = useQuery("saved-panels", (signal) => api<{ panels: SavedPanel[] }>("/api/panels", { signal }));
  const [draft, setDraft] = useState<Dashboard | null>(null);
  const [adding, setAdding] = useState(false);
  const [settings, setSettings] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [busy, setBusy] = useState(false);
  const [vars, setVars] = useState<Vars>(route.vars);
  const [tvTick, setTvTick] = useState(0);
  const onTvTick = useCallback(() => setTvTick((t) => t + 1), []);

  const dash = draft ?? loaded.data ?? null;
  const editing = draft !== null;
  const mayEdit = can("edit");

  // Open in edit mode straight after creating.
  useEffect(() => {
    if (loaded.data && mayEdit && !draft && window.location.hash.includes("edit=1")) {
      setDraft(loaded.data);
      window.history.replaceState(null, "", dashboardLink(id));
    }
  }, [loaded.data, mayEdit, draft, id]);

  // Variable defaults apply when the link doesn't set a value.
  useEffect(() => {
    const d = loaded.data;
    if (!d) return;
    setVars((cur) => {
      const next: Vars = { ...cur };
      for (const v of d.layout.variables) if (next[v.name] === undefined && v.default) next[v.name] = v.default;
      return next;
    });
  }, [loaded.data]);

  const override = dash?.layout.time as TimeRange | undefined;
  const overrideKey = override ? JSON.stringify(override) : "";
  const range: Resolved = useMemo(() => {
    if (route.from && route.to) {
      const from = new Date(route.from);
      const to = new Date(route.to);
      if (!Number.isNaN(from.getTime()) && !Number.isNaN(to.getTime()) && to > from) return { from, to };
    }
    if (override) return resolveRange(override);
    return app.resolved;
    // overrideKey stands for override; tvTick re-resolves relative ranges.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [route.from, route.to, overrideKey, app.resolved, tvTick]);
  const shared = route.from && route.to ? range : null;

  const panelsById = useMemo(() => new Map((saved.data?.panels ?? []).map((p) => [String(p.id), p])), [saved.data]);

  const setVar = (name: VarName, value: string) => {
    const next = { ...vars, [name]: value };
    setVars(next);
    // Keep the address shareable without a navigation.
    const link = dashboardLink(id, shared?.from, shared?.to, next);
    window.history.replaceState(null, "", route.tv ? tvLink(link) : link);
  };

  const shareLink = () => {
    const link = `${window.location.origin}${window.location.pathname}${dashboardLink(id, range.from, range.to, vars)}`;
    copyText(link).then((ok) =>
      ok ? toast("ok", "Link copied, with this time range and these selections.") : toast("err", "Could not copy the link."),
    );
  };

  const save = async () => {
    if (!draft) return;
    setBusy(true);
    try {
      await saveDashboard(draft);
      toast("ok", `Saved “${draft.name}”`);
      setDraft(null);
      setVersion((v) => v + 1);
      onChanged();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
    setBusy(false);
  };

  const remove = async () => {
    try {
      await api(`/api/dashboards/${id}`, { method: "DELETE" });
      toast("ok", "Dashboard deleted");
      onChanged();
      window.location.hash = "#/dashboards";
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
    setConfirmDelete(false);
  };

  const updateLayout = (layout: Partial<DashboardLayout>) => draft && setDraft({ ...draft, layout: { ...draft.layout, ...layout } });

  if (loaded.error && !loaded.data) {
    return (
      <>
        <TopBar title="Dashboards" section="Investigate" />
        <div className="content">
          <ErrorNotice title="Could not open this dashboard" error={loaded.error} />
          <p>
            <a href="#/dashboards">Back to dashboards</a>
          </p>
        </div>
      </>
    );
  }

  const grid = dash && (
    <DashGrid
      items={dash.layout.panels}
      editing={editing}
      onChange={(panels) => updateLayout({ panels })}
      render={(item, size) => (
        <DashPanelView
          item={item}
          size={size}
          range={range}
          vars={vars}
          tick={app.tick + tvTick}
          saved={panelsById.get(item.ref)}
          savedLoaded={Boolean(saved.data)}
          editing={editing}
          onRemove={() => updateLayout({ panels: resolveLayout(dash.layout.panels.filter((p) => p.id !== item.id)) })}
        />
      )}
    />
  );

  const varBar = dash && dash.layout.variables.length > 0 && <VariableBar dash={dash} range={range} vars={vars} onChange={setVar} />;
  const plainLink = dashboardLink(id, shared?.from, shared?.to, vars);

  if (route.tv && dash && !editing) {
    return (
      <TvMode
        dash={dash}
        all={all}
        rangeText={shared ? "Shared range" : rangeLabel(override ?? app.range)}
        onTick={onTvTick}
        onExit={() => (window.location.hash = plainLink)}
        variables={varBar}
      >
        {grid}
      </TvMode>
    );
  }

  const rangeControl = shared ? (
    <span className="pill dash-range-pill" title="This link pins the time range">
      Shared range · {rangeLabel({ kind: "absolute", from: range.from.toISOString(), to: range.to.toISOString() })}
      <a className="dash-pill-x" href={dashboardLink(id, undefined, undefined, vars)} aria-label="Use the normal range" title="Use the normal range">
        <X size={12} />
      </a>
    </span>
  ) : override ? (
    <span className="pill dash-range-pill" title="Set in this dashboard's settings">
      Dashboard range · {rangeLabel(override)}
    </span>
  ) : (
    <TimeRangePicker />
  );

  return (
    <>
      <TopBar
        title={dash?.name ?? "Dashboard"}
        section="Dashboards"
        actions={
          editing ? (
            <>
              <button type="button" className="btn" onClick={() => setAdding(true)}>
                <Plus size={14} />
                Add panel
              </button>
              <button type="button" className="btn" onClick={() => setSettings(true)}>
                <Settings2 size={14} />
                Settings
              </button>
              <button type="button" className="btn" onClick={() => setConfirmDelete(true)} title="Delete dashboard" aria-label="Delete dashboard">
                <Trash2 size={14} />
              </button>
              <button type="button" className="btn ghost" onClick={() => setDraft(null)} disabled={busy}>
                Cancel
              </button>
              <button type="button" className="btn primary" onClick={save} disabled={busy}>
                Save
              </button>
            </>
          ) : (
            <>
              {rangeControl}
              <RefreshControl busy={loaded.loading} />
              <button type="button" className="btn" onClick={shareLink} title="Copy a link with this range and selections" aria-label="Copy link">
                <Link2 size={14} />
              </button>
              <a className="btn" href={tvLink(plainLink)} title="TV mode: full screen, auto-refresh, cycles through dashboards">
                <Monitor size={14} />
                TV
              </a>
              {mayEdit && dash && (
                <button type="button" className="btn" onClick={() => setDraft(dash)}>
                  <Pencil size={14} />
                  Edit
                </button>
              )}
            </>
          )
        }
      />
      <div className="content">
        <div className="dash-crumbs">
          <a href="#/dashboards">All dashboards</a>
          {dash?.description && <span className="dim">{dash.description}</span>}
        </div>
        {varBar}
        {editing && (
          <div className="dash-edit-hint">
            Drag a panel by its header to move it, or by its bottom-right corner to resize it. Changes apply when you save.
          </div>
        )}
        {dash && dash.layout.panels.length === 0 && (
          <section className="panel">
            <div className="empty">
              <div className="empty-icon">
                <LayoutGrid size={20} />
              </div>
              <h3>This dashboard is empty</h3>
              <p>
                {editing
                  ? "Add saved panels or built-in panels, then save."
                  : mayEdit
                    ? "Edit it to add saved panels or built-in panels."
                    : "An editor can add panels to it."}
              </p>
              {editing && (
                <button type="button" className="btn primary" onClick={() => setAdding(true)}>
                  <Plus size={14} />
                  Add panel
                </button>
              )}
            </div>
          </section>
        )}
        {grid}
      </div>
      {adding && draft && (
        <AddPanel
          saved={saved.data?.panels ?? []}
          onClose={() => setAdding(false)}
          onAdd={(kind, ref, w, h) => {
            const panels = [...draft.layout.panels, { id: newPanelId(), kind, ref, ...placeAtBottom(draft.layout.panels, w, h) }];
            updateLayout({ panels });
            setAdding(false);
          }}
        />
      )}
      {settings && draft && (
        <DashboardSettings title="Dashboard settings" initial={draft} onClose={() => setSettings(false)} onSave={async (d) => setDraft({ ...draft, ...d })} />
      )}
      {confirmDelete && (
        <Modal
          title="Delete dashboard"
          narrow
          onClose={() => setConfirmDelete(false)}
          footer={
            <>
              <button type="button" className="btn ghost" onClick={() => setConfirmDelete(false)}>
                Cancel
              </button>
              <button type="button" className="btn danger" onClick={remove}>
                Delete
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>Delete “{dash?.name}” for everyone? Its saved panels stay in Saved panels.</p>
        </Modal>
      )}
    </>
  );
}

// Panel chrome around the chart: header, body padding and borders.
const CHROME_H = 60;

function DashPanelView({
  item,
  size,
  range,
  vars,
  tick,
  saved,
  savedLoaded,
  editing,
  onRemove,
}: {
  item: DashPanel;
  size: { width: number; height: number };
  range: Resolved;
  vars: Vars;
  tick: number;
  saved?: SavedPanel;
  savedLoaded: boolean;
  editing: boolean;
  onRemove: () => void;
}) {
  const builtin = item.kind === "builtin" ? BUILTIN_BY_REF.get(item.ref) : undefined;
  const extra: Record<string, string> = { ...item.params };
  if (item.kind === "saved") {
    for (const n of VAR_NAMES) if (vars[n]) extra[`var.${n}`] = vars[n]!;
  } else if (item.ref.startsWith("system/") && vars.instance) {
    extra.instance = vars.instance;
  }
  const qs = rangeQuery(range.from, range.to, extra);
  const path = item.kind === "saved" ? `/api/dashboards/panels/${item.ref}/data` : `/api/${item.ref}`;
  // A chart from a composite response (errors, dependencies) is one of its fields.
  const q = useQuery(`${path}?${qs}#${tick}`, async (signal) => {
    const res = await api<Result & Record<string, Result>>(`${path}?${qs}`, { signal });
    return item.field ? res[item.field] : res;
  });

  const missing = item.kind === "saved" && savedLoaded && !saved;
  const title = item.title || builtin?.title || saved?.title || (missing ? "Deleted panel" : "Saved panel");
  const viz = item.viz ?? builtin?.viz ?? saved?.viz ?? { type: "table" as const };
  const height = Math.max(60, size.height - CHROME_H);
  const isTable = viz.type === "table";

  return (
    <Panel
      className="dash-fill"
      title={title}
      description={item.description || builtin?.description || saved?.description || saved?.prompt}
      result={missing ? undefined : q.data}
      loading={q.loading}
      error={missing ? new Error("This saved panel was deleted; remove it from the dashboard.") : q.error}
      bodyHeight={height}
      chart={!isTable && q.data ? <VizView result={q.data} viz={viz} height={height} title={title} /> : undefined}
      table={q.data && <DataTable result={q.data} maxHeight={height} defaultUnit={viz.unit} />}
      extraActions={
        editing && (
          <button type="button" className="icon-btn" title="Remove from dashboard" aria-label="Remove from dashboard" onClick={onRemove}>
            <X size={15} />
          </button>
        )
      }
    />
  );
}

const VAR_LABEL: Record<VarName, string> = { instance: "Instance", route: "Route", service: "Service" };

function VariableBar({ dash, range, vars, onChange }: { dash: Dashboard; range: Resolved; vars: Vars; onChange: (n: VarName, v: string) => void }) {
  return (
    <div className="filter-row dash-vars">
      {dash.layout.variables.map((v) => (
        <VariableSelect key={v.name} name={v.name} range={range} value={vars[v.name] ?? ""} onChange={(val) => onChange(v.name, val)} />
      ))}
    </div>
  );
}

function VariableSelect({ name, range, value, onChange }: { name: VarName; range: Resolved; value: string; onChange: (v: string) => void }) {
  // Options follow the range by the hour, not on every refresh.
  const hourKey = `${Math.floor(range.from.getTime() / 3_600_000)}-${Math.floor(range.to.getTime() / 3_600_000)}`;
  const opts = useQuery(`var-${name}-${hourKey}`, (signal) =>
    api<{ values: string[] }>(`/api/dashboards/variables/${name}?${rangeQuery(range.from, range.to)}`, { signal }),
  );
  const values = opts.data?.values ?? [];
  const options = [{ value: "", label: `All ${VAR_LABEL[name].toLowerCase()}s` }, ...values.map((v) => ({ value: v, label: v }))];
  if (value && !values.includes(value)) options.push({ value, label: value });
  return (
    <div className="dash-var">
      <span>{VAR_LABEL[name]}</span>
      <Select label={VAR_LABEL[name]} value={value} options={options} onChange={onChange} maxWidth={300} align="left" />
    </div>
  );
}

// ---------- Editing ----------

function AddPanel({ saved, onClose, onAdd }: { saved: SavedPanel[]; onClose: () => void; onAdd: (kind: DashPanel["kind"], ref: string, w: number, h: number) => void }) {
  const [tab, setTab] = useState<"saved" | "builtin">(saved.length ? "saved" : "builtin");
  const groups = ["API overview", "Performance", "System", "Logs"] as const;
  return (
    <Modal title="Add a panel" onClose={onClose}>
      <div className="tabs">
        <button type="button" className={tab === "saved" ? "on" : ""} onClick={() => setTab("saved")}>
          Saved panels ({saved.length})
        </button>
        <button type="button" className={tab === "builtin" ? "on" : ""} onClick={() => setTab("builtin")}>
          Built-in panels
        </button>
      </div>
      {tab === "saved" && saved.length === 0 && <p className="dim">No saved panels yet: ask a question in Explore and save the chart.</p>}
      <div className="dash-pick">
        {tab === "saved" &&
          saved.map((p) => (
            <button type="button" key={p.id} className="dash-pick-row" onClick={() => onAdd("saved", String(p.id), p.viz.type === "stat" ? 3 : 6, p.viz.type === "stat" ? 3 : 6)}>
              <span className="dash-pick-title">{p.title}</span>
              <span className="dim">{p.description || p.prompt || p.viz.type}</span>
            </button>
          ))}
        {tab === "builtin" &&
          groups.map((g) => (
            <div key={g}>
              <div className="section-label dash-pick-group">{g}</div>
              {BUILTINS.filter((b) => b.group === g).map((b) => (
                <button type="button" key={b.ref} className="dash-pick-row" onClick={() => onAdd("builtin", b.ref, b.viz.type === "table" ? 12 : 6, 6)}>
                  <span className="dash-pick-title">{b.title}</span>
                  <span className="dim">{b.description}</span>
                </button>
              ))}
            </div>
          ))}
      </div>
    </Modal>
  );
}

type SettingsValue = Pick<Dashboard, "name" | "description" | "layout">;

function DashboardSettings({ title, initial, onClose, onSave }: { title: string; initial: SettingsValue; onClose: () => void; onSave: (d: SettingsValue) => Promise<void> }) {
  const [name, setName] = useState(initial.name);
  const [description, setDescription] = useState(initial.description);
  const [variables, setVariables] = useState(initial.layout.variables);
  const [time, setTime] = useState(initial.layout.time?.kind === "relative" ? initial.layout.time.key : "");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const toggleVar = (n: VarName) =>
    setVariables((vs) =>
      vs.some((v) => v.name === n) ? vs.filter((v) => v.name !== n) : [...vs, { name: n }].sort((a, b) => VAR_NAMES.indexOf(a.name) - VAR_NAMES.indexOf(b.name)),
    );

  const submit = async () => {
    if (!name.trim()) {
      setErr("Give the dashboard a name.");
      return;
    }
    setBusy(true);
    try {
      const preset = PRESETS.find((p) => p.key === time);
      await onSave({
        name: name.trim(),
        description: description.trim(),
        layout: { ...initial.layout, variables, time: preset ? { kind: "relative", key: preset.key } : undefined },
      });
      onClose();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={submit} disabled={busy}>
            {initial.name ? "Apply" : "Create"}
          </button>
        </>
      }
    >
      <div className="dash-form">
        <label className="field">
          <span>Name</span>
          <input className="input" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} autoFocus />
        </label>
        <label className="field">
          <span>Description</span>
          <input className="input" value={description} maxLength={500} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
        </label>
        <div className="field">
          <span>Time range</span>
          <Select
            label="Time range"
            align="left"
            value={time}
            onChange={setTime}
            options={[{ value: "", label: "Follow the global range" }, ...PRESETS.map((p) => ({ value: p.key, label: `Always ${p.label.toLowerCase()}` }))]}
          />
        </div>
        <div className="field">
          <span>Variables</span>
          <div className="dash-var-picks">
            {VAR_NAMES.map((n) => {
              const v = variables.find((x) => x.name === n);
              return (
                <div key={n} className="dash-var-pick">
                  <label>
                    <input type="checkbox" checked={Boolean(v)} onChange={() => toggleVar(n)} /> {VAR_LABEL[n]}
                  </label>
                  {v && (
                    <input
                      className="input"
                      placeholder="Default (optional)"
                      value={v.default ?? ""}
                      onChange={(e) => setVariables((vs) => vs.map((x) => (x.name === n ? { ...x, default: e.target.value || undefined } : x)))}
                    />
                  )}
                </div>
              );
            })}
          </div>
          <p className="dash-help">
            In a saved panel's SQL, <code>{"{{var.route}}"}</code> is the selected value as a quoted string (<code>''</code> for all), and{" "}
            <code>{"{{filter.route}}"}</code> adds <code>AND route = '…'</code> only when a value is selected. Built-in System panels follow the
            instance selector. Values are checked before they reach the database.
          </p>
        </div>
        {err && <span className="form-error">{err}</span>}
      </div>
    </Modal>
  );
}
