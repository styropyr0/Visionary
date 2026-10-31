import { useEffect, useState } from "react";
import { api } from "../lib/api";
import { formatBytes, formatCount } from "../lib/format";
import { useQuery } from "../lib/useQuery";
import { ErrorNotice } from "../components/ErrorNotice";
import { Modal } from "../components/Modal";
import { useToast } from "../components/Toast";
import { Card, Switch } from "./Settings.profiling";

interface Conversion {
  table: string;
  step: string;
  copied: number;
  total: number;
  error?: string;
  done: boolean;
}

interface PartitionTable {
  table: string;
  exists: boolean;
  partitioned: boolean;
  partitions: number;
  rows: number;
  bytes: number;
  conversion?: Conversion;
}

interface PartitionsView {
  supported: boolean;
  enabled: boolean;
  can_admin: boolean;
  tables: PartitionTable[];
}

const getPartitions = (signal?: AbortSignal) => api<PartitionsView>("/api/settings/partitions", { signal });

export function PartitionsCard() {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`partitions#${run}`, getPartitions);
  const [view, setView] = useState<PartitionsView>();
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<PartitionTable>();
  useEffect(() => {
    if (q.data) setView(q.data);
  }, [q.data]);

  const converting = view?.tables.some((t) => t.conversion && !t.conversion.done);
  useEffect(() => {
    if (!converting) return;
    const id = setInterval(() => setRun((n) => n + 1), 3000);
    return () => clearInterval(id);
  }, [converting]);

  if (q.error && !view) return <ErrorNotice title="Could not load partitioning" error={q.error} />;
  if (!view) return <div className="settings-empty">Loading…</div>;
  if (!view.supported) return null;

  const call = async (fn: () => Promise<PartitionsView>, ok: string) => {
    setBusy(true);
    try {
      setView(await fn());
      toast("ok", ok);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const toggle = (on: boolean) =>
    call(() => api<PartitionsView>("/api/settings/partitions", { method: "PUT", json: { enabled: on } }), on ? "Time partitioning is on." : "Time partitioning is off for new tables.");
  const convert = (t: PartitionTable) =>
    call(() => api<PartitionsView>(`/api/settings/partitions/${t.table}/convert`, { method: "POST", json: {} }), `Converting ${t.table}. Keep this page open to follow it.`);

  return (
    <Card
      title="Time partitioning"
      description={
        <>
          Splits the busiest tables into one PostgreSQL partition per day. Retention then drops whole days instead of deleting rows, and charts read only the days they show. Worth it
          from a few million rows a day; the SDKs and queries need no change. Empty tables convert at once; a table with rows converts when you ask, copying while writes continue.
        </>
      }
      actions={<Switch on={view.enabled} disabled={!view.can_admin || busy} label="Time partitioning" onChange={toggle} />}
    >
      <div className="ret-list">
        <section className="ret-group">
          {view.tables
            .filter((t) => t.exists)
            .map((t) => {
              const c = t.conversion;
              const running = c && !c.done;
              return (
                <div key={t.table} className="ret-row">
                  <div className="ret-what">
                    <div className="ret-label">
                      <code>{t.table}</code>
                    </div>
                    <div className="dim ret-desc">
                      {running
                        ? `${c.step}${c.total > 0 ? `: ${formatCount(c.copied)} of ${formatCount(c.total)} rows` : ""}`
                        : c?.error
                          ? `Conversion failed: ${c.error}`
                          : t.partitioned
                            ? `${t.partitions} partitions`
                            : "One table"}
                    </div>
                  </div>
                  <div className="ret-usage">
                    <div>About {formatCount(t.rows)} rows</div>
                    <div className="dim">{formatBytes(t.bytes)}</div>
                  </div>
                  <div className="ret-keep">
                    {!t.partitioned && view.enabled && view.can_admin && (
                      <button type="button" className="btn sm" disabled={busy || !!running} onClick={() => setConfirm(t)}>
                        {running ? "Converting…" : "Convert"}
                      </button>
                    )}
                  </div>
                </div>
              );
            })}
        </section>
      </div>
      {confirm && (
        <Modal
          title={`Partition ${confirm.table}?`}
          onClose={() => setConfirm(undefined)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setConfirm(undefined)}>
                Cancel
              </button>
              <button
                type="button"
                className="btn primary"
                onClick={() => {
                  const t = confirm;
                  setConfirm(undefined);
                  void convert(t);
                }}
              >
                Convert
              </button>
            </>
          }
        >
          <p>
            Copies about {formatCount(confirm.rows)} rows into a new partitioned table while writes continue, then swaps the two. Writes to <code>{confirm.table}</code> pause for
            the swap, a few seconds plus the last minutes of rows; the SDKs queue meanwhile. The database needs room for a second copy ({formatBytes(confirm.bytes)}) until the
            old table is dropped at the end. Pick a quiet hour for large tables.
          </p>
        </Modal>
      )}
    </Card>
  );
}
