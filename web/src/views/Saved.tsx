import { useState } from "react";
import { Bookmark } from "lucide-react";
import { api, rangeQuery, type Panel as SavedPanel, type Result } from "../lib/api";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { DataTable } from "../components/DataTable";
import { Modal } from "../components/Modal";
import { Panel } from "../components/Panel";
import { useToast } from "../components/Toast";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";

export function Saved() {
  const { tick } = useApp();
  const toast = useToast();
  const [version, setVersion] = useState(0);
  const panels = useQuery(`panels#${version}#${tick}`, (signal) => api<{ panels: SavedPanel[] }>("/api/panels", { signal }));
  const [confirm, setConfirm] = useState<SavedPanel | null>(null);

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
            <SavedPanelView key={p.id} panel={p} onDelete={() => setConfirm(p)} />
          ))}
        </div>
      </div>
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

function SavedPanelView({ panel, onDelete }: { panel: SavedPanel; onDelete: () => void }) {
  const { resolved, tick } = useApp();
  const tokens = useChartTokens();
  const qs = rangeQuery(resolved.from, resolved.to);
  const q = useQuery(`panel-${panel.id}?${qs}#${tick}`, (signal) =>
    api<Result>("/api/query", {
      method: "POST",
      signal,
      json: { query: panel.query, from: resolved.from.toISOString(), to: resolved.to.toISOString() },
    }),
  );
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
    />
  );
}
