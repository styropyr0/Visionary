import { useState } from "react";
import { Bookmark, LayoutGrid } from "lucide-react";
import { api, rangeQuery, type Panel as SavedPanel, type Result } from "../lib/api";
import { dashboardLink, newPanelId, placeAtBottom, saveDashboard, useSession, type Dashboard } from "../lib/team";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { DataTable } from "../components/DataTable";
import { Modal } from "../components/Modal";
import { Panel } from "../components/Panel";
import { useToast } from "../components/Toast";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import "./Dashboards.css";

export function Saved() {
  const { tick } = useApp();
  const toast = useToast();
  const [version, setVersion] = useState(0);
  const panels = useQuery(`panels#${version}#${tick}`, (signal) => api<{ panels: SavedPanel[] }>("/api/panels", { signal }));
  const [confirm, setConfirm] = useState<SavedPanel | null>(null);
  const [adding, setAdding] = useState<SavedPanel | null>(null);
  const { can } = useSession();

  const remove = async (p: SavedPanel) => {
    try {
      await api(`/api/panels/${p.id}`, { method: "DELETE" });
      toast("ok", `Deleted “${p.title}”`);
      setVersion((v) => v + 1);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
    setConfirm(null);
  };

  const list = panels.data?.panels ?? [];

  return (
    <>
      <TopBar
        title="Saved panels"
        section="API"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl busy={panels.loading} />
          </>
        }
      />
      <div className="content">
        {panels.error && !panels.data && <div className="notice">Could not load saved panels: {panels.error.message}</div>}
        {panels.data && list.length === 0 && (
          <section className="panel">
            <div className="empty">
              <div className="empty-icon">
                <Bookmark size={20} />
              </div>
              <h3>No saved panels yet</h3>
              <p>Ask a question in Explore and save the chart. Saved panels show up here for everyone and follow the selected time range.</p>
              <a className="btn primary" href="#/explore">
                Open Explore
              </a>
            </div>
          </section>
        )}
        <div className="grid">
          {list.map((p) => (
            <SavedPanelView
              key={p.id}
              panel={p}
              onDelete={can("edit") ? () => setConfirm(p) : undefined}
              onAddToDashboard={can("edit") ? () => setAdding(p) : undefined}
            />
          ))}
        </div>
      </div>
      {adding && <AddToDashboard panel={adding} onClose={() => setAdding(null)} />}
      {confirm && (
        <Modal
          title="Delete panel"
          narrow
          onClose={() => setConfirm(null)}
          footer={
            <>
              <button className="btn ghost" onClick={() => setConfirm(null)}>
                Cancel
              </button>
              <button className="btn danger" onClick={() => remove(confirm)}>
                Delete
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>
            Delete “{confirm.title}” for everyone? This can’t be undone.
          </p>
        </Modal>
      )}
    </>
  );
}

function SavedPanelView({ panel, onDelete, onAddToDashboard }: { panel: SavedPanel; onDelete?: () => void; onAddToDashboard?: () => void }) {
  const { resolved, tick } = useApp();
  const tokens = useChartTokens();
  const qs = rangeQuery(resolved.from, resolved.to);
  // The dashboards endpoint renders {{var.*}} placeholders (as "all" here).
  const q = useQuery(`panel-${panel.id}?${qs}#${tick}`, (signal) => api<Result>(`/api/dashboards/panels/${panel.id}/data?${qs}`, { signal }));
  const height = 280;
  const isTable = panel.viz.type === "table";
  return (
    <Panel
      className="span-6"
      title={panel.title}
      description={panel.description || panel.prompt}
      result={q.data}
      loading={q.loading}
      error={q.error}
      bodyHeight={height}
      note={isTable ? undefined : vizNote(q.data, panel.viz, tokens)}
      chart={!isTable && q.data ? <VizView result={q.data} viz={panel.viz} height={height} title={panel.title} /> : undefined}
      table={q.data && <DataTable result={q.data} maxHeight={height} defaultUnit={panel.viz.unit} />}
      onDelete={onDelete}
      extraActions={
        onAddToDashboard && (
          <button className="icon-btn" title="Add to dashboard" aria-label="Add to dashboard" onClick={onAddToDashboard}>
            <LayoutGrid size={15} />
          </button>
        )
      }
    />
  );
}

function AddToDashboard({ panel, onClose }: { panel: SavedPanel; onClose: () => void }) {
  const toast = useToast();
  const list = useQuery("dashboards-pick", (signal) => api<{ dashboards: Dashboard[] }>("/api/dashboards", { signal }));
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const dashboards = list.data?.dashboards ?? [];

  const add = async (target: Dashboard | null) => {
    setBusy(true);
    try {
      const base = target ?? { name: name.trim() || panel.title, description: "", layout: { panels: [], variables: [] } };
      const size = panel.viz.type === "stat" ? { w: 3, h: 3 } : { w: 6, h: 6 };
      const layout = { ...base.layout, panels: [...base.layout.panels, { id: newPanelId(), kind: "saved" as const, ref: String(panel.id), ...placeAtBottom(base.layout.panels, size.w, size.h) }] };
      const saved = await saveDashboard({ ...base, layout });
      toast("ok", `Added to “${saved.name}”`);
      onClose();
      window.location.hash = dashboardLink(saved.id);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <Modal title={`Add “${panel.title}” to a dashboard`} narrow onClose={onClose}>
      {list.error && <div className="notice">Could not load dashboards: {list.error.message}</div>}
      <div className="saved-dash-list">
        {dashboards.map((d) => (
          <button key={d.id} className="saved-dash-row" disabled={busy} onClick={() => add(d)}>
            <span>{d.name}</span>
            <span className="saved-dash-meta">{d.layout.panels.length} panels</span>
          </button>
        ))}
        {list.data && dashboards.length === 0 && <p className="saved-dash-meta">No dashboards yet. Name a new one below.</p>}
      </div>
      <div className="saved-dash-new">
        <input className="input" placeholder="New dashboard name" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} />
        <button className="btn primary" disabled={busy} onClick={() => add(null)}>
          Create and add
        </button>
      </div>
    </Modal>
  );
}
