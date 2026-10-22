import { useMemo, useState } from "react";
import { AlertTriangle, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { formatCount } from "../lib/format";
import { formatFull } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { profiledServices, useSettings } from "../lib/settings";
import { Modal } from "../components/Modal";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { ErrorNotice } from "../components/ErrorNotice";

interface DataKind {
  key: string;
  label: string;
  description: string;
  service: boolean;
}

interface DataCount {
  kind: string;
  label: string;
  rows: number;
  skipped?: string;
}

type RangeMode = "older" | "between" | "all";

const OLDER = [
  { value: "1", label: "1 day" },
  { value: "7", label: "7 days" },
  { value: "30", label: "30 days" },
  { value: "90", label: "90 days" },
  { value: "365", label: "1 year" },
];

/** A datetime-local value, in local time. */
function localInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** Settings → Data: delete collected data by kind, time range and service. */
export function DataTab() {
  const toast = useToast();
  const { settings } = useSettings();
  const kinds = useQuery("data-kinds", (signal) => api<{ kinds: DataKind[] }>("/api/data/kinds", { signal }));
  const services = useQuery("profiled-services", profiledServices);
  const [chosen, setChosen] = useState<string[]>([]);
  const [mode, setMode] = useState<RangeMode>("older");
  const [olderDays, setOlderDays] = useState("30");
  const [from, setFrom] = useState(() => localInput(new Date(Date.now() - 24 * 3600_000)));
  const [to, setTo] = useState(() => localInput(new Date()));
  const [service, setService] = useState("*");
  const [preview, setPreview] = useState<DataCount[]>();
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [typed, setTyped] = useState("");

  const body = useMemo(() => {
    let fromIso = "";
    let toIso = "";
    if (mode === "older") toIso = new Date(Date.now() - Number(olderDays) * 86400_000).toISOString();
    if (mode === "between") {
      fromIso = from ? new Date(from).toISOString() : "";
      toIso = to ? new Date(to).toISOString() : "";
    }
    return { kinds: chosen, from: fromIso, to: toIso, ...(service === "*" ? {} : { service }) };
  }, [chosen, mode, olderDays, from, to, service]);

  // Any change makes an earlier preview stale.
  const change = <T,>(set: (v: T) => void) => (v: T) => {
    set(v);
    setPreview(undefined);
  };

  if (settings && !settings.can_admin) {
    return (
      <div className="notice settings-note">
        <AlertTriangle size={16} />
        <span>Only admins can delete data.</span>
      </div>
    );
  }
  if (kinds.error) return <ErrorNotice title="Could not load data kinds" error={kinds.error} />;
  if (!kinds.data) return <div className="settings-empty">Loading…</div>;

  const serviceOptions = [
    { value: "*", label: "Every service" },
    ...(services.data?.services ?? []).map((s) => ({ value: s.service, label: s.service || "(no name)" })),
  ];
  const total = preview?.reduce((n, c) => n + c.rows, 0) ?? 0;
  const badRange = mode === "between" && from && to && new Date(from) >= new Date(to);

  const run = async (path: "preview" | "delete") => {
    setBusy(true);
    try {
      const res = await api<{ counts: DataCount[]; rows?: number }>(`/api/data/${path}`, { method: "POST", json: body });
      if (path === "preview") {
        setPreview(res.counts);
      } else {
        toast("ok", `Deleted ${formatCount(res.rows ?? 0)} rows.`);
        setPreview(undefined);
        setConfirm(false);
        setTyped("");
      }
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const rangeText =
    mode === "all" ? "recorded at any time" : mode === "older" ? `older than ${OLDER.find((o) => o.value === olderDays)?.label}` : `between ${formatFull(new Date(from))} and ${formatFull(new Date(to))}`;

  return (
    <div className="settings-stack">
      <section className="panel settings-card">
        <header className="settings-card-head">
          <div>
            <h3 className="panel-title">Delete data</h3>
            <p className="settings-desc">
              Removes collected rows for good, for example a load test, or data from before a fix. Settings, dashboards, alert rules, uptime checks and the audit log are never
              touched, and every delete is recorded in the audit log.
            </p>
          </div>
        </header>
        <div className="settings-body data-del">
          <div className="data-step">
            <div className="data-step-head">
              <span className="pset-label">What to delete</span>
              <button type="button" className="btn ghost sm" onClick={() => change(setChosen)(chosen.length === kinds.data!.kinds.length ? [] : kinds.data!.kinds.map((k) => k.key))}>
                {chosen.length === kinds.data.kinds.length ? "Clear" : "Select all"}
              </button>
            </div>
            <div className="data-kinds">
              {kinds.data.kinds.map((k) => {
                const off = service !== "*" && !k.service;
                return (
                  <label key={k.key} className={`data-kind ${off ? "off" : ""}`} title={off ? "Not kept per service: choose Every service to delete it." : undefined}>
                    <input
                      type="checkbox"
                      checked={chosen.includes(k.key)}
                      onChange={() => change(setChosen)(chosen.includes(k.key) ? chosen.filter((c) => c !== k.key) : [...chosen, k.key])}
                    />
                    <span>
                      <span className="data-kind-name">{k.label}</span>
                      <span className="dim">{k.description}</span>
                    </span>
                  </label>
                );
              })}
            </div>
          </div>

          <div className="data-step">
            <span className="pset-label">When</span>
            <div className="data-range">
              <label className="data-radio">
                <input type="radio" checked={mode === "older"} onChange={() => change(setMode)("older")} /> Older than
              </label>
              <Select label="Older than" value={olderDays} options={OLDER} disabled={mode !== "older"} onChange={(v) => change(setOlderDays)(v)} align="left" />
            </div>
            <div className="data-range">
              <label className="data-radio">
                <input type="radio" checked={mode === "between"} onChange={() => change(setMode)("between")} /> Between
              </label>
              <input className="input" type="datetime-local" value={from} disabled={mode !== "between"} onChange={(e) => change(setFrom)(e.target.value)} aria-label="From" />
              <span className="dim">and</span>
              <input className="input" type="datetime-local" value={to} disabled={mode !== "between"} onChange={(e) => change(setTo)(e.target.value)} aria-label="To" />
            </div>
            <div className="data-range">
              <label className="data-radio">
                <input type="radio" checked={mode === "all"} onChange={() => change(setMode)("all")} /> Everything, whenever it was recorded
              </label>
            </div>
            {badRange && <p className="form-error">The range ends before it starts.</p>}
          </div>

          <div className="data-step">
            <span className="pset-label">Which service</span>
            <div className="data-range">
              <Select label="Service" value={service} options={serviceOptions} onChange={(v) => change(setService)(v)} align="left" />
              <span className="dim">Frontend, uptime, alert, host and query-plan data are not kept per service.</span>
            </div>
          </div>

          <div className="pset-actions">
            <button type="button" className="btn" disabled={busy || chosen.length === 0 || !!badRange} onClick={() => run("preview")}>
              {busy && !confirm ? "Counting…" : "Preview"}
            </button>
            <button type="button" className="btn danger" disabled={busy || !preview || total === 0} onClick={() => setConfirm(true)}>
              <Trash2 size={14} />
              {!preview ? "Delete" : total === 0 ? "Nothing to delete" : `Delete ${formatCount(total)} rows`}
            </button>
            {!preview && chosen.length > 0 && <span className="dim">Preview first to see how much would go.</span>}
          </div>

          {preview && (
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Data</th>
                    <th style={{ textAlign: "right" }}>Rows to delete</th>
                  </tr>
                </thead>
                <tbody>
                  {preview.map((c) => (
                    <tr key={c.kind}>
                      <td>{c.label}</td>
                      <td style={{ textAlign: "right" }}>{c.skipped ? <span className="dim">{c.skipped}</span> : formatCount(c.rows)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </section>

      {confirm && preview && (
        <Modal
          title="Delete data"
          narrow
          onClose={() => (setConfirm(false), setTyped(""))}
          footer={
            <>
              <button type="button" className="btn ghost" onClick={() => (setConfirm(false), setTyped(""))}>
                Cancel
              </button>
              <button type="button" className="btn danger" disabled={busy || typed.trim().toLowerCase() !== "delete"} onClick={() => run("delete")}>
                <Trash2 size={14} />
                {busy ? "Deleting…" : `Delete ${formatCount(total)} rows`}
              </button>
            </>
          }
        >
          <p style={{ marginTop: 0 }}>
            This permanently deletes {formatCount(total)} rows of {preview.filter((c) => c.rows > 0).map((c) => c.label.toLowerCase()).join(", ")} {rangeText}
            {service === "*" ? "" : ` for ${service || "services with no name"}`}. It can't be undone.
          </p>
          <label className="data-confirm">
            <span>
              Type <strong>delete</strong> to confirm
            </span>
            <input className="input" value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus />
          </label>
        </Modal>
      )}
    </div>
  );
}
