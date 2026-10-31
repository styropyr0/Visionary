import { useEffect, useState } from "react";
import { Link2, Siren } from "lucide-react";
import { useQuery } from "../lib/useQuery";
import { toLocalInput } from "../lib/time";
import { alertsApi } from "../lib/alerts";
import { SEVERITIES, incidentsApi, type Incident, type Severity } from "../lib/incidents";
import { Modal } from "../components/Modal";
import { Combobox } from "../components/Combobox";
import { useToast } from "../components/Toast";

const SOURCE_NAMES: Record<string, string> = { alert: "Alert rule", uptime: "Uptime check", error: "Error issue", log: "Log line" };

export const splitList = (v: string) =>
  v
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);

export function DeclareModal({ source, onClose, onDeclared }: { source?: { kind: string; ref: string }; onClose: () => void; onDeclared: (i: Incident) => void }) {
  const toast = useToast();
  const [title, setTitle] = useState("");
  const [severity, setSeverity] = useState<Severity>("sev3");
  const [services, setServices] = useState("");
  const [impact, setImpact] = useState("");
  const [started, setStarted] = useState(() => toLocalInput(new Date()));
  const [commander, setCommander] = useState("");
  const [channels, setChannels] = useState<number[]>([]);
  const [statusPage, setStatusPage] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const src = useQuery(source ? `incident-source?${source.kind}|${source.ref}` : null, (signal) => incidentsApi.source(source!.kind, source!.ref, signal));
  const people = useQuery("incident-people", (signal) => incidentsApi.people(signal));
  const chans = useQuery("alerts-channels", (signal) => alertsApi.channels(signal));

  // The source fills the form once it loads, and anything typed first is kept
  useEffect(() => {
    const s = src.data;
    if (!s) return;
    setTitle((t) => t || s.title);
    setSeverity(s.severity);
    setServices((v) => v || s.services.join(", "));
    setImpact((v) => v || s.impact);
    if (s.started_at) setStarted(toLocalInput(new Date(s.started_at)));
  }, [src.data]);
  useEffect(() => {
    if (people.data?.me) setCommander((c) => c || people.data!.me);
  }, [people.data]);

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      const at = new Date(started);
      const inc = await incidentsApi.declare({
        title: title.trim(),
        severity,
        services: splitList(services),
        impact: impact.trim(),
        started_at: Number.isNaN(at.getTime()) ? undefined : at.toISOString(),
        source_kind: src.data?.kind,
        source_ref: src.data?.ref,
        commander: commander.trim(),
        on_status_page: statusPage,
        channels,
      });
      toast("ok", `Declared ${inc.severity.toUpperCase()}: ${inc.title}`);
      onClose();
      onDeclared(inc);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="Declare an incident"
      onClose={onClose}
      footer={
        <>
          {error && <span className="form-error">{error}</span>}
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={busy || !title.trim() || (source !== undefined && !src.data)} onClick={() => void submit()}>
            <Siren size={14} />
            {busy ? "Declaring…" : "Declare"}
          </button>
        </>
      }
    >
      <div className="inc-form">
        {source && (
          <div className="inc-source">
            <Link2 size={14} aria-hidden />
            <span>
              {src.error ? (
                <span className="form-error">{src.error.message}</span>
              ) : src.data ? (
                <>
                  From {SOURCE_NAMES[src.data.kind]?.toLowerCase() ?? src.data.kind} <b>{src.data.label}</b>. It is linked to the incident, and its history joins the timeline.
                </>
              ) : (
                "Loading what to declare from…"
              )}
            </span>
          </div>
        )}
        <label className="field">
          <span>Title</span>
          <input className="input" value={title} maxLength={200} autoFocus placeholder="Checkout payments failing" onChange={(e) => setTitle(e.target.value)} />
        </label>
        <div className="field">
          <span>Severity</span>
          <div className="segmented" role="radiogroup" aria-label="Severity">
            {SEVERITIES.map((s) => (
              <button type="button" key={s.key} role="radio" aria-checked={severity === s.key} className={severity === s.key ? "on" : ""} title={s.hint} onClick={() => setSeverity(s.key)}>
                {s.label}
              </button>
            ))}
          </div>
          <small className="dim">{SEVERITIES.find((s) => s.key === severity)?.hint}.</small>
        </div>
        <div className="inc-form-row">
          <label className="field">
            <span>Affected services</span>
            <input className="input" value={services} placeholder="shop, payments" onChange={(e) => setServices(e.target.value)} />
          </label>
          <label className="field">
            <span>Impact started</span>
            <input className="input" type="datetime-local" value={started} onChange={(e) => setStarted(e.target.value)} />
          </label>
        </div>
        <label className="field">
          <span>Impact</span>
          <textarea className="textarea" rows={3} maxLength={4000} value={impact} placeholder="Who is affected and how: card payments fail for about 30% of customers." onChange={(e) => setImpact(e.target.value)} />
        </label>
        <div className="field">
          <span>Incident commander</span>
          <Combobox label="Incident commander" value={commander} onChange={setCommander} options={people.data?.people ?? []} placeholder="Nobody yet" />
          <small className="dim">Taking command acknowledges the incident. Leave it empty to assign it later.</small>
        </div>
        {(chans.data?.channels.length ?? 0) > 0 && (
          <fieldset className="inc-checks">
            <legend>Tell these channels</legend>
            {chans.data!.channels.map((c) => (
              <label key={c.id}>
                <input type="checkbox" checked={channels.includes(c.id)} onChange={(e) => setChannels((list) => (e.target.checked ? [...list, c.id] : list.filter((x) => x !== c.id)))} />
                {c.name} <span className="dim">{c.kind}</span>
              </label>
            ))}
          </fieldset>
        )}
        <fieldset className="inc-checks">
          <label>
            <input type="checkbox" checked={statusPage} onChange={(e) => setStatusPage(e.target.checked)} />
            Show on the public status page
          </label>
          <small className="dim">Only its title and the updates you publish appear there, while the status page is on (Settings → Status page).</small>
        </fieldset>
      </div>
    </Modal>
  );
}
