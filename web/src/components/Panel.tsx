import { useState, type ReactNode } from "react";
import { AlertTriangle, BarChart3, Code2, LayoutGrid, Table2, Trash2 } from "lucide-react";
import type { Result } from "../lib/api";
import { useSession, type DashSource } from "../lib/team";
import { AddToDashboard } from "./team/AddToDashboard";
import { InspectModal } from "./Modal";
import { linkify } from "./ErrorNotice";
import { InfoTip } from "./InfoTip";

interface Props {
  title: string;
  description?: string;
  className?: string;
  result?: Result;
  loading?: boolean;
  error?: Error;
  /** Chart view; omit for panels that are always tables. */
  chart?: ReactNode;
  table?: ReactNode;
  note?: string;
  bodyHeight?: number;
  onDelete?: () => void;
  extraActions?: ReactNode;
  /** Offers "Add to dashboard" to editors; title and description default to the panel's. */
  dashboard?: DashSource | (() => Promise<DashSource>);
}

/**
 * Dashboard panel frame. Every chart has a table twin, so values never depend
 * on reading color or hovering; Inspect shows the exact SQL that ran.
 */
export function Panel({
  title,
  description,
  className = "",
  result,
  loading,
  error,
  chart,
  table,
  note,
  bodyHeight,
  onDelete,
  extraActions,
  dashboard,
}: Props) {
  const [view, setView] = useState<"chart" | "table">("chart");
  const [inspect, setInspect] = useState(false);
  const showTable = !chart || view === "table";

  let body: ReactNode;
  if (error && !result) {
    body = (
      <div className="panel-state error">
        <div className="state-inner">
          <AlertTriangle size={18} />
          <span>{linkify(error.message)}</span>
        </div>
      </div>
    );
  } else if (!result) {
    body = <div className="panel-state" style={{ height: bodyHeight }} />;
  } else {
    body = showTable ? table : chart;
  }

  return (
    <section className={`panel ${className}`} aria-busy={loading || undefined}>
      {loading && <div className="progress" aria-hidden />}
      <header className="panel-head">
        <h3 className="panel-title">{title}</h3>
        {description && (
          <InfoTip text={description} />
        )}
        {error && result && (
          <InfoTip text={<>Could not refresh: {linkify(error.message)}</>} icon={<AlertTriangle size={13} />} style={{ color: "var(--critical-text)" }} />
        )}
        <div className="panel-actions">
          {extraActions}
          {dashboard && <AddButton title={title} description={description} source={dashboard} />}
          {chart && table && (
            <button type="button"
              className={`icon-btn ${view === "table" ? "on" : ""}`}
              title={view === "table" ? "Show chart" : "Show table"}
              aria-label={view === "table" ? "Show chart" : "Show table"}
              onClick={() => setView(view === "table" ? "chart" : "table")}
            >
              {view === "table" ? <BarChart3 size={15} /> : <Table2 size={15} />}
            </button>
          )}
          {result && (
            <button type="button" className="icon-btn" title="Inspect query" aria-label="Inspect query" onClick={() => setInspect(true)}>
              <Code2 size={15} />
            </button>
          )}
          {onDelete && (
            <button type="button" className="icon-btn" title="Delete panel" aria-label="Delete panel" onClick={onDelete}>
              <Trash2 size={15} />
            </button>
          )}
        </div>
      </header>
      <div className={`panel-body ${loading && result ? "refetching" : ""}`} style={{ minHeight: bodyHeight }}>
        {body}
      </div>
      {note && result && !showTable && <div className="panel-note">{note}</div>}
      {inspect && result && <InspectModal title={title} result={result} onClose={() => setInspect(false)} />}
    </section>
  );
}

function AddButton({ title, description, source }: { title: string; description?: string; source: DashSource | (() => Promise<DashSource>) }) {
  const { can } = useSession();
  const [open, setOpen] = useState(false);
  if (!can("edit")) return null;
  const full = typeof source === "function" ? source : { title, description, ...source };
  return (
    <>
      <button type="button" className="icon-btn" title="Add to dashboard" aria-label="Add to dashboard" onClick={() => setOpen(true)}>
        <LayoutGrid size={15} />
      </button>
      {open && <AddToDashboard title={title} source={full} onClose={() => setOpen(false)} />}
    </>
  );
}
