import { useEffect, useMemo, useState } from "react";
import { ExternalLink, Plus, X } from "lucide-react";
import { api, type Meta } from "../lib/api";
import { formatCount, formatMs } from "../lib/format";
import { LANGS } from "../lib/help";
import { prefs, setPrefs, usePrefs, type Preferences } from "../lib/prefs";
import { getServerSettings, retentionUsage, saveRetention, saveStatusPage, saveThresholds, type RetentionKind, type RouteApdex, type ServerSettingsView } from "../lib/settings";
import { useApp } from "../lib/state";
import { formatRelative, PRESETS, setTimeDisplay } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { Combobox } from "../components/Combobox";
import { ErrorNotice } from "../components/ErrorNotice";
import { Select } from "../components/Select";
import { NAV } from "../components/Sidebar";
import { useToast } from "../components/Toast";
import { Card, Switch } from "./Settings.profiling";

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"].map((m) => ({ value: m, label: m }));

function useServerSettings(): { view?: ServerSettingsView; error?: Error; setView: (v: ServerSettingsView) => void } {
  const [run, setRun] = useState(0);
  const q = useQuery(`server-settings#${run}`, getServerSettings);
  const [view, setView] = useState<ServerSettingsView>();
  useEffect(() => {
    if (q.data) setView(q.data);
  }, [q.data]);
  return { view, error: q.error, setView: (v) => (setView(v), setRun((n) => n + 1)) };
}

function useSave() {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const run = async <T,>(fn: () => Promise<T>, ok: string): Promise<T | undefined> => {
    setBusy(true);
    try {
      const v = await fn();
      toast("ok", ok);
      return v;
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
      return undefined;
    } finally {
      setBusy(false);
    }
  };
  return { busy, run };
}

// ---------- Thresholds ----------

/** Settings → Thresholds: Apdex T, per-route T and the SLO target. */
export function ThresholdsTab() {
  const { view, error, setView } = useServerSettings();
  const meta = useQuery("meta", (signal) => api<Meta>("/api/meta", { signal }));
  const { busy, run } = useSave();
  const [t, setT] = useState("");
  const [slo, setSlo] = useState("");
  const [routes, setRoutes] = useState<RouteApdex[]>([]);

  const saved = view?.settings;
  const reset = () => {
    if (!saved) return;
    setT(String(saved.apdex.default_ms));
    setSlo(String(saved.slo_target));
    setRoutes(saved.apdex.routes.map((r) => ({ ...r })));
  };
  useEffect(reset, [saved]);

  if (error) return <ErrorNotice title="Could not load settings" error={error} />;
  if (!view || !saved) return <div className="settings-empty">Loading…</div>;
  const admin = view.can_admin;
  const dirty =
    t !== String(saved.apdex.default_ms) || slo !== String(saved.slo_target) || JSON.stringify(routes) !== JSON.stringify(saved.apdex.routes);
  const tMs = Number(t);

  const save = async () => {
    const v = await run(
      () => saveThresholds({ default_ms: tMs, routes: routes.map((r) => ({ ...r, ms: Number(r.ms) })) }, Number(slo)),
      "Thresholds saved. Charts use them from now on.",
    );
    if (v) setView(v);
  };

  return (
    <div className="settings-stack">
      <Card
        title="Apdex and SLO"
        description="How fast a request must be to count as good. Performance, Overview, SLO alerts and the weekly digest all use these."
      >
        <div className="settings-body pset">
          <div className="pset-row">
            <div>
              <div className="pset-label">Satisfied within (T)</div>
              <div className="dim pset-hint">
                Faster is satisfied; up to 4T ({Number.isFinite(tMs) && tMs > 0 ? formatMs(4 * tMs) : "—"}) is tolerating; slower, or a server error, is frustrated.
              </div>
            </div>
            <label className="thr-input">
              <input className="input" type="number" min={1} value={t} disabled={!admin} onChange={(e) => setT(e.target.value)} aria-label="Apdex threshold in ms" />
              <span className="dim">ms</span>
            </label>
          </div>
          <div className="pset-row">
            <div>
              <div className="pset-label">SLO target</div>
              <div className="dim pset-hint">The share of requests that should be satisfied: SLO burn alerts and the error budget measure against it.</div>
            </div>
            <label className="thr-input">
              <input className="input" type="number" min={50} max={99.999} step={0.1} value={slo} disabled={!admin} onChange={(e) => setSlo(e.target.value)} aria-label="SLO target percent" />
              <span className="dim">%</span>
            </label>
          </div>
        </div>
      </Card>

      <Card
        title="Routes with their own threshold"
        description="For routes that are slow by design, such as exports and uploads, so they don't drag Apdex and the SLO down. Every other route uses T above."
        actions={
          admin && (
            <button type="button" className="btn sm" onClick={() => setRoutes([...routes, { method: "GET", route: "", ms: Math.max(tMs * 4, 2000) }])} disabled={routes.length >= 50}>
              <Plus size={14} /> Add route
            </button>
          )
        }
      >
        <div className="settings-body">
          {routes.length === 0 ? (
            <p className="dim" style={{ margin: 0 }}>
              None: every route is held to {formatMs(tMs || 0)}.
            </p>
          ) : (
            <div className="thr-routes">
              {routes.map((r, i) => (
                <div key={i} className="thr-route">
                  <Select label="Method" value={r.method} options={METHODS} disabled={!admin} align="left" onChange={(v) => setRoutes(routes.map((x, j) => (j === i ? { ...x, method: v } : x)))} />
                  <div className="thr-route-path">
                    <Combobox label="Route" value={r.route} placeholder="/api/export" options={meta.data?.routes ?? []} onChange={(v) => setRoutes(routes.map((x, j) => (j === i ? { ...x, route: v } : x)))} />
                  </div>
                  <label className="thr-input">
                    <input
                      className="input"
                      type="number"
                      min={1}
                      value={r.ms}
                      disabled={!admin}
                      onChange={(e) => setRoutes(routes.map((x, j) => (j === i ? { ...x, ms: Number(e.target.value) } : x)))}
                      aria-label="Threshold in ms"
                    />
                    <span className="dim">ms</span>
                  </label>
                  {admin && (
                    <button type="button" className="icon-btn" aria-label="Remove route" onClick={() => setRoutes(routes.filter((_, j) => j !== i))}>
                      <X size={14} />
                    </button>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      </Card>

      {admin ? (
        <div className="pset-actions">
          <button type="button" className="btn primary" disabled={!dirty || busy} onClick={save}>
            {busy ? "Saving…" : "Save thresholds"}
          </button>
          {dirty && (
            <button type="button" className="btn ghost" onClick={reset}>
              Discard
            </button>
          )}
          <span className="dim">Summaries already built for long ranges keep the threshold of their time.</span>
        </div>
      ) : (
        <p className="dim">Only admins can change thresholds.</p>
      )}
    </div>
  );
}

// ---------- Retention ----------

const KEEP = [7, 14, 30, 60, 90, 180, 365, 730].map((d) => ({ value: String(d), label: d >= 365 ? `${d / 365} year${d > 365 ? "s" : ""}` : `${d} days` }));

const RETENTION_GROUPS: { title: string; keys: string[] }[] = [
  { title: "Requests and calls", keys: ["requests", "spans", "errors", "profiles", "plans"] },
  { title: "Servers and containers", keys: ["system", "containers", "hosts"] },
  { title: "Users, checks and deploys", keys: ["frontend", "uptime", "alerts", "releases"] },
  { title: "Security and audit", keys: ["security", "audit"] },
  { title: "Chart summaries", keys: ["rollup_minute", "rollup_hour"] },
];

/** The kinds under their headings; any the dashboard doesn't know go last. */
function retentionGroups(kinds: RetentionKind[]): { title: string; kinds: RetentionKind[] }[] {
  const known = new Set(RETENTION_GROUPS.flatMap((g) => g.keys));
  const groups = RETENTION_GROUPS.map((g) => ({ title: g.title, kinds: g.keys.map((k) => kinds.find((x) => x.key === k)).filter((k): k is RetentionKind => !!k) }));
  const other = kinds.filter((k) => !known.has(k.key));
  if (other.length) groups.push({ title: "Other", kinds: other });
  return groups.filter((g) => g.kinds.length > 0);
}

/** Settings → Data: how long each kind of data is kept. */
export function RetentionCard() {
  const { view, error, setView } = useServerSettings();
  const usage = useQuery("retention-usage", retentionUsage);
  const { busy, run } = useSave();
  const [draft, setDraft] = useState<Record<string, number>>({});
  const saved = view?.settings.retention;
  useEffect(() => {
    if (saved) setDraft({ ...saved });
  }, [saved]);

  if (error) return <ErrorNotice title="Could not load retention" error={error} />;
  if (!view || !saved) return <div className="settings-empty">Loading…</div>;
  const admin = view.can_admin;
  const changed = Object.keys(draft).filter((k) => draft[k] !== saved[k]);

  const options = (days: number) => {
    const list = [...KEEP, { value: "0", label: "Forever" }];
    return list.some((o) => o.value === String(days)) ? list : [...list, { value: String(days), label: `${days} days` }];
  };

  return (
    <section className="panel settings-card">
      <header className="settings-card-head">
        <div>
          <h3 className="panel-title">Retention</h3>
          <p className="settings-desc">
            How long each kind of data is kept. Older rows are deleted every 6 hours, for every service. Raw rows go first; the hourly summaries keep long-range charts after them.
          </p>
        </div>
        {admin && (
          <div className="settings-card-actions">
            <button
              type="button"
              className="btn primary sm"
              disabled={changed.length === 0 || busy}
              onClick={async () => {
                const v = await run(() => saveRetention(Object.fromEntries(changed.map((k) => [k, draft[k]]))), "Retention saved. The next cleanup applies it.");
                if (v) setView(v);
              }}
            >
              {busy ? "Saving…" : changed.length ? `Save ${changed.length} change${changed.length > 1 ? "s" : ""}` : "Save"}
            </button>
          </div>
        )}
      </header>
      <div className="ret-list">
        {retentionGroups(view.retention_kinds).map((g) => (
          <section key={g.title} className="ret-group">
            <h4 className="ret-group-title">{g.title}</h4>
            {g.kinds.map((k) => {
              const days = draft[k.key] ?? k.default_days;
              const u = usage.data?.usage[k.key];
              return (
                <div key={k.key} className={`ret-row ${draft[k.key] !== saved[k.key] ? "changed" : ""}`}>
                  <div className="ret-what">
                    <div className="ret-label">{k.label}</div>
                    <div className="dim ret-desc">{k.description}</div>
                  </div>
                  <div className="ret-usage">
                    {u && u.rows === 0 ? (
                      <div className="dim">Empty</div>
                    ) : u ? (
                      <>
                        <div>
                          {formatCount(u.rows)} {u.rows === 1 ? "row" : "rows"}
                        </div>
                        {u.oldest && <div className="dim">oldest {formatRelative(u.oldest)}</div>}
                      </>
                    ) : usage.loading && !usage.data ? (
                      <div className="dim">Counting…</div>
                    ) : null}
                  </div>
                  <div className="ret-keep">
                    <Select label={`Keep ${k.label} for`} value={String(days)} options={options(days)} disabled={!admin} onChange={(v) => setDraft({ ...draft, [k.key]: Number(v) })} />
                  </div>
                </div>
              );
            })}
          </section>
        ))}
      </div>
    </section>
  );
}

// ---------- Status page ----------

/** Settings → Status page: the public page at /status. */
export function StatusPageTab() {
  const { view, error, setView } = useServerSettings();
  const { busy, run } = useSave();
  const [title, setTitle] = useState("");
  useEffect(() => {
    if (view) setTitle(view.settings.status_title);
  }, [view]);
  if (error) return <ErrorNotice title="Could not load settings" error={error} />;
  if (!view) return <div className="settings-empty">Loading…</div>;
  const s = view.settings;
  const admin = view.can_admin;
  const save = async (enabled: boolean, t: string) => {
    const v = await run(() => saveStatusPage(enabled, t), enabled ? "The status page is public." : "The status page is off.");
    if (v) setView(v);
  };
  return (
    <div className="settings-stack">
      <Card
        title="Public status page"
        description={
          <>
            A page anyone can open, at <code>/status</code> (and <code>/status.json</code> for widgets), with the uptime and incidents of the checks you choose. It needs no sign-in, so it never shows anything else.
          </>
        }
        actions={<Switch on={s.status_page} disabled={!admin || busy} label="Public status page" onChange={(on) => save(on, title)} />}
      >
        <div className="settings-body pset">
          <div className="pset-row">
            <div>
              <div className="pset-label">Title</div>
              <div className="dim pset-hint">Shown at the top of the page and in the browser tab. Empty shows your app's name followed by “status”.</div>
            </div>
            <div className="sp-title">
              <input className="input" value={title} maxLength={120} disabled={!admin} placeholder="Acme status" onChange={(e) => setTitle(e.target.value)} />
              {admin && title !== s.status_title && (
                <button type="button" className="btn sm" disabled={busy} onClick={() => save(s.status_page, title)}>
                  Save
                </button>
              )}
            </div>
          </div>
          <div className="pset-row">
            <div>
              <div className="pset-label">Which checks</div>
              <div className="dim pset-hint">Each uptime check has "Show on the public status page", and whether its URL shows.</div>
            </div>
            <a className="btn sm" href="#/uptime">
              Uptime checks
            </a>
          </div>
          {s.status_page && (
            <a className="about-guide" href="/status" target="_blank" rel="noreferrer">
              Open the status page <ExternalLink size={12} />
            </a>
          )}
          {!admin && <p className="dim" style={{ margin: 0 }}>Only admins can change the status page.</p>}
        </div>
      </Card>
    </div>
  );
}

// ---------- Preferences ----------

const AUTO = [
  { value: "last", label: "The last one used" },
  { value: "0", label: "Off" },
  { value: "30000", label: "Every 30 seconds" },
  { value: "60000", label: "Every minute" },
  { value: "300000", label: "Every 5 minutes" },
  { value: "900000", label: "Every 15 minutes" },
];

/** Settings → Preferences: each person's own, kept on the server. */
export function PreferencesTab() {
  const toast = useToast();
  const p = usePrefs();
  const { setTheme } = useApp();
  const zone = useMemo(() => Intl.DateTimeFormat().resolvedOptions().timeZone, []);
  const set = (patch: Partial<Preferences>, note = "Saved.") => {
    setPrefs(patch);
    toast("ok", note);
  };
  // Times are drawn once per page load; a new zone or clock reloads.
  const setTime = (patch: Partial<Preferences>) => {
    setPrefs(patch);
    setTimeDisplay({ ...prefs(), ...patch });
    window.setTimeout(() => window.location.reload(), 500);
  };
  const pages = NAV.flatMap((g) => g.items.map((i) => ({ value: i.route, label: `${g.group} · ${i.label}` })));

  return (
    <div className="settings-stack">
      <Card title="Your preferences" description="Yours alone, on every browser you sign in from. They don't change what anyone else sees.">
        <div className="settings-body pset">
          <Row label="Start page" hint="Where Visionary opens.">
            <Select label="Start page" value={p.startPage || "overview"} options={pages} onChange={(v) => set({ startPage: v === "overview" ? "" : v })} />
          </Row>
          <Row label="Time range" hint="What a visit starts with.">
            <Select
              label="Time range"
              value={p.range ?? "last"}
              options={[{ value: "last", label: "The last one used" }, ...PRESETS.map((x) => ({ value: x.key, label: x.label }))]}
              onChange={(v) => set({ range: v as Preferences["range"] })}
            />
          </Row>
          <Row label="Auto-refresh" hint="What a visit starts with, for ranges that end now.">
            <Select label="Auto-refresh" value={String(p.autoRefresh ?? "last")} options={AUTO} onChange={(v) => set({ autoRefresh: v === "last" ? "last" : Number(v) })} />
          </Row>
          <Row label="Theme" hint="Follow the device switches with your system's light and dark mode.">
            <Select
              label="Theme"
              value={p.theme ?? "system"}
              options={[
                { value: "system", label: "Follow the device" },
                { value: "dark", label: "Dark" },
                { value: "light", label: "Light" },
              ]}
              onChange={(v) => {
                // setTheme ends "follow the device", so the choice is saved after it.
                setTheme(v === "system" ? (window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark") : (v as "dark" | "light"));
                set({ theme: v as Preferences["theme"] });
              }}
            />
          </Row>
          <Row label="Time zone" hint="For every time and chart. Changing it reloads the page.">
            <Select
              label="Time zone"
              value={p.utc ? "utc" : "local"}
              options={[
                { value: "local", label: `This device (${zone})` },
                { value: "utc", label: "UTC" },
              ]}
              onChange={(v) => setTime({ utc: v === "utc" })}
            />
          </Row>
          <Row label="Clock" hint="Changing it reloads the page.">
            <Select
              label="Clock"
              value={p.clock ?? "auto"}
              options={[
                { value: "auto", label: "Your language's" },
                { value: "24", label: "24-hour" },
                { value: "12", label: "12-hour" },
              ]}
              onChange={(v) => setTime({ clock: v as Preferences["clock"] })}
            />
          </Row>
          <Row label="Code in Help" hint="The language Help shows first.">
            <Select label="Code in Help" value={p.helpLang ?? "go"} options={LANGS.map((l) => ({ value: l.key, label: l.name }))} onChange={(v) => set({ helpLang: v })} />
          </Row>
          <Row label="Slowest requests" hint="Routes you left out of Performance → Slowest requests.">
            <span className="dim">
              {(p.slowExclude ?? []).length === 0 ? "None left out" : `${(p.slowExclude ?? []).length} left out`}
              {(p.slowExclude ?? []).length > 0 && (
                <>
                  {" · "}
                  <button type="button" className="help-link" style={{ marginTop: 0 }} onClick={() => set({ slowExclude: [] }, "All routes are back in Slowest requests.")}>
                    Clear
                  </button>
                </>
              )}
            </span>
          </Row>
        </div>
      </Card>
    </div>
  );
}

function Row({ label, hint, children }: { label: string; hint: string; children: React.ReactNode }) {
  return (
    <div className="pset-row">
      <div>
        <div className="pset-label">{label}</div>
        <div className="dim pset-hint">{hint}</div>
      </div>
      {children}
    </div>
  );
}
