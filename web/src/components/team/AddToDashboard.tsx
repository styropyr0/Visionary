import { useState } from "react";
import { api } from "../../lib/api";
import { dashboardLink, newPanelId, placeAtBottom, saveDashboard, type DashSource, type Dashboard } from "../../lib/team";
import { useQuery } from "../../lib/useQuery";
import { Modal } from "../Modal";
import { useToast } from "../Toast";
import { ErrorNotice } from "../ErrorNotice";
import "../../views/Dashboards.css";

/**
 * Puts a chart on an existing dashboard or a new one. `source` may be a
 * function when the chart has to be saved first (an Explore answer).
 */
export function AddToDashboard({ title, source, onClose }: { title: string; source: DashSource | (() => Promise<DashSource>); onClose: () => void }) {
  const toast = useToast();
  const list = useQuery("dashboards-pick", (signal) => api<{ dashboards: Dashboard[] }>("/api/dashboards", { signal }));
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const dashboards = list.data?.dashboards ?? [];

  const add = async (target: Dashboard | null) => {
    setBusy(true);
    try {
      const panel = typeof source === "function" ? await source() : source;
      const base = target ?? { name: name.trim() || title, description: "", layout: { panels: [], variables: [] } };
      const size = panel.viz?.type === "stat" ? { w: 3, h: 3 } : { w: 6, h: 6 };
      // A saved panel keeps its own title and chart, so later edits show up.
      const item = panel.kind === "saved" ? { kind: panel.kind, ref: panel.ref } : panel;
      const layout = { ...base.layout, panels: [...base.layout.panels, { ...item, id: newPanelId(), ...placeAtBottom(base.layout.panels, size.w, size.h) }] };
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
    <Modal title={`Add “${title}” to a dashboard`} narrow onClose={onClose}>
      {list.error && <ErrorNotice title="Could not load dashboards" error={list.error} />}
      <div className="saved-dash-list">
        {dashboards.map((d) => (
          <button type="button" key={d.id} className="saved-dash-row" disabled={busy} onClick={() => add(d)}>
            <span>{d.name}</span>
            <span className="saved-dash-meta">{d.layout.panels.length} panels</span>
          </button>
        ))}
        {list.data && dashboards.length === 0 && <p className="saved-dash-meta">No dashboards yet. Name a new one below.</p>}
      </div>
      <div className="saved-dash-new">
        <input className="input" placeholder="New dashboard name" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} />
        <button type="button" className="btn primary" disabled={busy} onClick={() => add(null)}>
          Create and add
        </button>
      </div>
    </Modal>
  );
}
