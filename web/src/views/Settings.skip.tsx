import { useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { saveRequestSkip, useSettings } from "../lib/settings";
import { useToast } from "../components/Toast";
import { ErrorNotice } from "../components/ErrorNotice";
import { Card } from "./Settings.profiling";

const COMMON = ["/health", "/healthz", "/ready", "/livez", "/metrics", "/favicon.ico"];

/** Settings → Data: paths every SDK leaves out of the request log. */
export function SkipPathsCard() {
  const toast = useToast();
  const { settings, error } = useSettings();
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);

  if (error) return <ErrorNotice title="Could not load settings" error={error} />;
  if (!settings) return <div className="settings-empty">Loading…</div>;
  const admin = settings.can_admin;
  const list = settings.request_skip ?? [];
  const missing = COMMON.filter((p) => !list.includes(p));

  const save = async (next: string[], done: string) => {
    setBusy(true);
    try {
      await saveRequestSkip(next);
      toast("ok", done);
      return true;
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setBusy(false);
    }
  };
  const add = async () => {
    const p = path.trim();
    if (p && (await save([...list, p], "Added. Services stop logging it within 30 seconds."))) setPath("");
  };

  return (
    <Card
      title="Paths not logged"
      description={
        <>
          Requests to these paths get no request row and no calls, from every SDK and from OpenTelemetry. They still count in throughput and error rates, and their
          errors and log lines are kept. End a path with <code>*</code> to skip everything under it, e.g. <code>/static/*</code>. Services follow changes within 30 seconds.
        </>
      }
    >
      <div className="settings-body pset">
        {list.length === 0 ? (
          <div className="dim">None. Every request is logged.</div>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Path</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((p) => (
                  <tr key={p}>
                    <td className="mono">{p}</td>
                    <td className="lg-row-actions">
                      {admin && (
                        <button
                          type="button"
                          className="icon-btn"
                          aria-label={`Log ${p} again`}
                          disabled={busy}
                          onClick={() => save(list.filter((x) => x !== p), "Removed. Services log it again within 30 seconds.")}
                        >
                          <Trash2 size={14} />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {admin ? (
          <div className="lgs-add">
            <input
              className="input mono"
              placeholder="/healthz or /static/*"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && add()}
            />
            <button type="button" className="btn" disabled={busy || !path.trim()} onClick={add}>
              <Plus size={14} />
              Add
            </button>
            {missing.length > 0 && (
              <button type="button" className="btn ghost" disabled={busy} onClick={() => save([...list, ...missing], "Added the common health checks.")}>
                Add common health checks
              </button>
            )}
          </div>
        ) : (
          <p className="dim" style={{ margin: 0 }}>
            Only admins change these.
          </p>
        )}
      </div>
    </Card>
  );
}
