import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Eye, EyeOff, Flame, Play } from "lucide-react";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatRelative } from "../lib/time";
import { HELP } from "../lib/help";
import { profileNow, profiledServices, saveHiddenPages, saveProfileSettings, useSettings, type ProfileSettings } from "../lib/settings";
import { NAV } from "../components/Sidebar";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { ErrorNotice } from "../components/ErrorNotice";

const SDK_LABEL: Record<string, string> = { go: "Go", node: "Node", python: "Python", ruby: "Ruby", java: "Java", dotnet: ".NET" };

const INTERVALS = [
  { value: "60", label: "Every minute" },
  { value: "300", label: "Every 5 minutes" },
  { value: "900", label: "Every 15 minutes" },
  { value: "1800", label: "Every 30 minutes" },
  { value: "3600", label: "Every hour" },
];

const DURATIONS = [
  { value: "5000", label: "5 seconds" },
  { value: "10000", label: "10 seconds" },
  { value: "30000", label: "30 seconds" },
];

/** An on/off switch. */
export function Switch({ on, disabled, onChange, label }: { on: boolean; disabled?: boolean; onChange: (on: boolean) => void; label: string }) {
  return (
    <button type="button" role="switch" aria-checked={on} aria-label={label} className={`switch ${on ? "on" : ""}`} disabled={disabled} onClick={() => onChange(!on)}>
      <span className="switch-knob" />
    </button>
  );
}

function When({ iso }: { iso?: string }) {
  if (!iso) return <span className="dim">Never</span>;
  return <span title={formatFull(new Date(iso))}>{formatRelative(iso)}</span>;
}

export function Card({ title, description, actions, children }: { title: string; description?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="panel settings-card">
      <header className="settings-card-head">
        <div>
          <h3 className="panel-title">{title}</h3>
          {description && <p className="settings-desc">{description}</p>}
        </div>
        {actions && <div className="settings-card-actions">{actions}</div>}
      </header>
      {children}
    </section>
  );
}

type Draft = Pick<ProfileSettings, "enabled" | "services" | "interval_s" | "duration_ms">;

/** Settings → Profiling: whether the SDKs take CPU profiles, of which services, how often. */
export function ProfilingTab() {
  const toast = useToast();
  const { settings, error } = useSettings();
  const [run, setRun] = useState(0);
  const services = useQuery(`profiled-services#${run}`, profiledServices);
  const [draft, setDraft] = useState<Draft>();
  const [saving, setSaving] = useState(false);
  const [asking, setAsking] = useState(false);

  const saved = settings?.profile;
  useEffect(() => {
    if (saved) setDraft({ enabled: saved.enabled, services: saved.services, interval_s: saved.interval_s, duration_ms: saved.duration_ms });
  }, [saved]);

  // Every service seen, plus any chosen earlier that has not reported lately.
  const names = useMemo(() => {
    const seen = (services.data?.services ?? []).map((s) => s.service).filter(Boolean);
    return [...new Set([...seen, ...(saved?.services ?? [])])].sort();
  }, [services.data, saved]);

  if (error) return <ErrorNotice title="Could not load settings" error={error} />;
  if (!settings || !draft || !saved) return <div className="settings-empty">Loading…</div>;

  const admin = settings.can_admin;
  const dirty = JSON.stringify(draft) !== JSON.stringify({ enabled: saved.enabled, services: saved.services, interval_s: saved.interval_s, duration_ms: saved.duration_ms });
  const chosen = draft.services.length > 0;
  const toggleService = (name: string) =>
    setDraft({ ...draft, services: draft.services.includes(name) ? draft.services.filter((s) => s !== name) : [...draft.services, name] });

  const save = async () => {
    setSaving(true);
    try {
      await saveProfileSettings(draft);
      toast("ok", draft.enabled ? "Profiling is on. Services pick it up within 30 seconds." : "Profiling is off. Services stop within 30 seconds.");
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };
  const now = async () => {
    setAsking(true);
    try {
      await profileNow();
      toast("ok", "Asked for a profile. It shows on CPU profiles within about a minute.");
      window.setTimeout(() => setRun((n) => n + 1), 45_000);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setAsking(false);
    }
  };

  return (
    <div className="settings-stack">
      <Card
        title="CPU profiling"
        description={
          <>
            When on, the SDKs sample where your services spend CPU while they serve requests, and the CPU profiles page shows it as flame graphs. Changes reach running
            services within 30 seconds; no redeploy. A service with <code>VISIONARY_PROFILE=false</code> never profiles.
          </>
        }
        actions={<Switch on={draft.enabled} disabled={!admin} label="Take CPU profiles" onChange={(enabled) => setDraft({ ...draft, enabled })} />}
      >
        <div className="settings-body pset">
          <div className="pset-row">
            <div>
              <div className="pset-label">Services</div>
              <div className="dim pset-hint">Profile every service, or only some, e.g. staging but not production.</div>
            </div>
            <div className="pset-choice" role="radiogroup" aria-label="Services">
              <label>
                <input type="radio" checked={!chosen} disabled={!admin} onChange={() => setDraft({ ...draft, services: [] })} /> Every service
              </label>
              <label>
                <input type="radio" checked={chosen} disabled={!admin || names.length === 0} onChange={() => setDraft({ ...draft, services: names.slice(0, 1) })} /> Only these
              </label>
            </div>
          </div>
          {chosen && (
            <div className="pset-services">
              {names.map((n) => (
                <label key={n} className="pset-service">
                  <input type="checkbox" checked={draft.services.includes(n)} disabled={!admin} onChange={() => toggleService(n)} />
                  <span className="mono">{n}</span>
                </label>
              ))}
            </div>
          )}
          <div className="pset-row">
            <div>
              <div className="pset-label">How often</div>
              <div className="dim pset-hint">A profile is skipped while a service gets no requests.</div>
            </div>
            <Select label="Interval" value={String(draft.interval_s)} disabled={!admin} options={withCurrent(INTERVALS, draft.interval_s, (v) => `Every ${v} s`)} onChange={(v) => setDraft({ ...draft, interval_s: Number(v) })} />
          </div>
          <div className="pset-row">
            <div>
              <div className="pset-label">Each profile samples for</div>
              <div className="dim pset-hint">Sampling costs a few percent of one CPU while it runs, and nothing in between.</div>
            </div>
            <Select label="Duration" value={String(draft.duration_ms)} disabled={!admin} options={withCurrent(DURATIONS, draft.duration_ms, (v) => `${v / 1000} seconds`)} onChange={(v) => setDraft({ ...draft, duration_ms: Number(v) })} />
          </div>
          {admin ? (
            <div className="pset-actions">
              <button type="button" className="btn primary" disabled={!dirty || saving || (chosen && draft.services.length === 0)} onClick={save}>
                {saving ? "Saving…" : "Save"}
              </button>
              {dirty && (
                <button type="button" className="btn ghost" onClick={() => setDraft({ enabled: saved.enabled, services: saved.services, interval_s: saved.interval_s, duration_ms: saved.duration_ms })}>
                  Discard
                </button>
              )}
              {saved.updated_at && (
                <span className="dim">
                  Last changed <When iso={saved.updated_at} />
                </span>
              )}
            </div>
          ) : (
            <p className="dim" style={{ margin: 0 }}>
              Only admins can change profiling.
            </p>
          )}
        </div>
      </Card>

      <Card
        title="Profile now"
        description="Takes one profile from each chosen service at once, whether or not profiling is on: for a CPU spike happening right now."
        actions={
          <button type="button" className="btn" disabled={!settings.can_edit || asking} onClick={now}>
            <Play size={14} />
            {asking ? "Asking…" : "Profile now"}
          </button>
        }
      >
        <div className="settings-body dim">
          Last asked <When iso={saved.now} />.
        </div>
      </Card>

      <Card title="Services" description="Services that reported in the last day, and their latest profile. Profiling needs an SDK version that reads these settings.">
        {services.error ? (
          <ErrorNotice title="Could not load services" error={services.error} />
        ) : !services.data ? (
          <div className="settings-empty">Loading…</div>
        ) : services.data.services.length === 0 ? (
          <div className="settings-empty">No service has reported in the last day.</div>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Service</th>
                  <th>SDK</th>
                  <th style={{ textAlign: "right" }}>Instances</th>
                  <th>Profiling</th>
                  <th>Latest profile</th>
                </tr>
              </thead>
              <tbody>
                {services.data.services.map((s) => {
                  const included = saved.services.length === 0 || saved.services.includes(s.service);
                  return (
                    <tr key={s.service}>
                      <td className="mono">{s.service || <span className="dim">(no name)</span>}</td>
                      <td>{s.sdks.map((k) => SDK_LABEL[k] ?? k).join(", ") || <span className="dim">—</span>}</td>
                      <td style={{ textAlign: "right" }}>{s.instances}</td>
                      <td>
                        {saved.enabled && included ? (
                          <span className="pset-state on">
                            <Flame size={12} /> On
                          </span>
                        ) : (
                          <span className="pset-state">{saved.enabled ? "Not chosen" : "Off"}</span>
                        )}
                      </td>
                      <td>
                        <When iso={s.last_profile} />
                        {s.profiles > 0 && <span className="dim"> · {s.profiles} this week</span>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}

/** The options, plus the current value when it is not one of them. */
function withCurrent(opts: { value: string; label: string }[], v: number, label: (v: number) => string) {
  return opts.some((o) => o.value === String(v)) ? opts : [...opts, { value: String(v), label: label(v) }];
}

/** Settings → Pages: which pages the sidebar shows, for the whole team. */
export function PagesTab() {
  const toast = useToast();
  const { settings, error } = useSettings();
  const [busy, setBusy] = useState(false);
  if (error) return <ErrorNotice title="Could not load settings" error={error} />;
  if (!settings) return <div className="settings-empty">Loading…</div>;
  const admin = settings.can_admin;
  const hidden = new Set(settings.hidden_pages);

  const toggle = async (route: string) => {
    const next = hidden.has(route) ? settings.hidden_pages.filter((p) => p !== route) : [...settings.hidden_pages, route];
    setBusy(true);
    try {
      await saveHiddenPages(next);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="settings-stack">
      <Card
        title="Pages in the sidebar"
        description="Hide the pages your team doesn't use, such as Frontend without a web app or Containers without Docker. Hidden pages stay reachable by their link, and the change applies to everyone."
      >
        <div className="settings-body pages-groups">
          {NAV.map(({ group, items }) => (
            <div key={group} className="pages-group">
              <div className="nav-label">{group}</div>
              {items.map(({ route, label, icon: Icon }) => {
                const locked = route === "settings";
                const shown = !hidden.has(route);
                return (
                  <div key={route} className={`pages-row ${shown ? "" : "off"}`}>
                    <Icon size={15} />
                    <div className="pages-text">
                      <div>{label}</div>
                      <div className="dim">{HELP[route].tip}</div>
                    </div>
                    {locked ? (
                      <span className="dim pages-locked">Always shown</span>
                    ) : (
                      <span className="pages-switch">
                        {shown ? <Eye size={13} className="dim" /> : <EyeOff size={13} className="dim" />}
                        <Switch on={shown} disabled={!admin || busy} label={`Show ${label}`} onChange={() => toggle(route)} />
                      </span>
                    )}
                  </div>
                );
              })}
            </div>
          ))}
          {!admin && <p className="dim">Only admins can change which pages show.</p>}
        </div>
      </Card>
    </div>
  );
}
