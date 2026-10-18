import { useEffect, useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, CheckCircle2, CircleSlash, KeyRound, Plus, ShieldCheck } from "lucide-react";
import { api, rangeQuery, type SessionUser } from "../lib/api";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatRelative } from "../lib/time";
import {
  ROLE_INFO,
  ROLE_LABEL,
  SCOPE_INFO,
  useSession,
  type About,
  type ApiKey,
  type AuditEntry,
  type AuditPage,
  type KeyScope,
  type Members,
  type Role,
} from "../lib/team";
import { CopyButton, Modal } from "../components/Modal";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { TimeRangePicker, TopBar } from "../components/Toolbar";
import "./Settings.css";

type Tab = "members" | "keys" | "audit" | "about";

const TABS: { key: Tab; label: string; admin: boolean }[] = [
  { key: "members", label: "Members & roles", admin: false },
  { key: "keys", label: "API keys", admin: true },
  { key: "audit", label: "Audit log", admin: true },
  { key: "about", label: "About", admin: false },
];

function readTab(): Tab {
  const t = new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("tab");
  return TABS.some((x) => x.key === t) ? (t as Tab) : "members";
}

export function Settings({ user }: { user: SessionUser | null }) {
  void user;
  const { can, session } = useSession();
  const [tab, setTabState] = useState<Tab>(readTab);
  const admin = can("admin");

  useEffect(() => {
    const on = () => setTabState(readTab());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  const setTab = (t: Tab) => {
    setTabState(t);
    window.history.replaceState(null, "", `#/settings?tab=${t}`);
  };

  const visible = TABS.filter((t) => !t.admin || admin);
  const current = visible.some((t) => t.key === tab) ? tab : "members";

  return (
    <>
      <TopBar title="Settings" section="Manage" actions={current === "audit" ? <TimeRangePicker /> : undefined} />
      <div className="content">
        <div className="tabs" role="tablist">
          {visible.map((t) => (
            <button key={t.key} role="tab" aria-selected={current === t.key} className={current === t.key ? "on" : ""} onClick={() => setTab(t.key)}>
              {t.label}
            </button>
          ))}
        </div>
        {session && !admin && (
          <div className="notice settings-note">
            <ShieldCheck size={16} />
            <span>API keys and the audit log are for admins. You're signed in as {ROLE_LABEL[session.role] ?? session.role}.</span>
          </div>
        )}
        {current === "members" && <MembersTab />}
        {current === "keys" && <KeysTab />}
        {current === "audit" && <AuditTab />}
        {current === "about" && <AboutTab />}
      </div>
    </>
  );
}

function Card({ title, description, actions, children }: { title: string; description?: ReactNode; actions?: ReactNode; children: ReactNode }) {
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

function RoleBadge({ role }: { role: string }) {
  const r = role as Role;
  return <span className={`role-badge role-${r}`}>{ROLE_LABEL[r] ?? (role || "None")}</span>;
}

function When({ iso }: { iso: string | null }) {
  if (!iso) return <span className="dim">Never</span>;
  return <span title={formatFull(new Date(iso))}>{formatRelative(iso)}</span>;
}

function LoadState({ error, loading, empty }: { error?: Error; loading: boolean; empty?: string }) {
  if (error) return <div className="settings-empty error">{error.message}</div>;
  if (loading) return <div className="settings-empty">Loading…</div>;
  return empty ? <div className="settings-empty">{empty}</div> : null;
}

// ---------- Members & roles ----------

const ACTION_LABEL: Record<string, string> = {
  "auth.sign_in": "Signed in",
  "auth.sign_out": "Signed out",
  "auth.sign_in_refused": "Refused: role not allowed",
};

function MembersTab() {
  const m = useQuery("team-members", (signal) => api<Members>("/api/team/members", { signal }));
  const d = m.data;
  if (!d) return <LoadState error={m.error} loading={m.loading} />;
  const me = d.me;
  return (
    <div className="settings-stack">
      <Card title="You" description="What your role lets you do in Visionary.">
        <div className="settings-me">
          <div className="settings-kv">
            <span>Name</span>
            <strong>{me.user.name || me.user.handle || me.user.id}</strong>
            {me.user.handle && (
              <>
                <span>Handle</span>
                <span className="mono">@{me.user.handle}</span>
              </>
            )}
            <span>{d.provider} roles</span>
            <span className="mono">{me.user.roles.join(", ") || "none"}</span>
            <span>Visionary role</span>
            <span>
              <RoleBadge role={me.role} /> <span className="dim">{ROLE_INFO[me.role]}</span>
            </span>
          </div>
        </div>
      </Card>

      <Card title="Role mapping" description={<MappingNote mode={d.mode} viewer={d.viewer_password} />}>
        {d.mode === "otp" || d.mode === "password" ? (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>{d.provider} role</th>
                  <th>Visionary role</th>
                  <th>Set by</th>
                </tr>
              </thead>
              <tbody>
                {d.mapping.map((e) => (
                  <tr key={e.provider_role}>
                    <td className="mono">{e.provider_role}</td>
                    <td>
                      <RoleBadge role={e.role} />
                    </td>
                    <td className="dim">{e.source === "map" ? "VISIONARY_ROLE_MAP" : "VISIONARY_ALLOWED_ROLES (admin unless mapped)"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="settings-body">
            <table className="data">
              <thead>
                <tr>
                  <th>Sign-in</th>
                  <th>Visionary role</th>
                </tr>
              </thead>
              <tbody>
                {d.mode === "shared" ? (
                  <>
                    <tr>
                      <td>Team password (VISIONARY_SHARED_PASSWORD)</td>
                      <td>
                        <RoleBadge role="admin" />
                      </td>
                    </tr>
                    <tr>
                      <td>Viewer password (VISIONARY_VIEWER_PASSWORD)</td>
                      <td>{d.viewer_password ? <RoleBadge role="viewer" /> : <span className="dim">Not set</span>}</td>
                    </tr>
                  </>
                ) : (
                  <tr>
                    <td>Everyone (sign-in is off)</td>
                    <td>
                      <RoleBadge role="admin" />
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title="Roles">
        <div className="settings-roles">
          {(["viewer", "editor", "admin"] as Role[]).map((r) => (
            <div key={r} className="settings-role">
              <RoleBadge role={r} />
              <span>{ROLE_INFO[r]}</span>
            </div>
          ))}
        </div>
      </Card>

      {d.sign_ins && (
        <Card title="Recent sign-ins" description="The last 50 sign-ins and sign-outs, from the audit log.">
          {d.sign_ins.length === 0 ? (
            <div className="settings-empty">No sign-ins recorded yet.</div>
          ) : (
            <div className="table-wrap settings-scroll">
              <table className="data">
                <thead>
                  <tr>
                    <th>When</th>
                    <th>User</th>
                    <th>Role</th>
                    <th>Event</th>
                    <th>IP</th>
                  </tr>
                </thead>
                <tbody>
                  {d.sign_ins.map((s, i) => (
                    <tr key={i}>
                      <td>
                        <When iso={s.created_at} />
                      </td>
                      <td>
                        {s.user_name || s.user_id} <span className="dim mono">{s.user_name && s.user_id !== s.user_name ? s.user_id : ""}</span>
                      </td>
                      <td>{s.role ? <RoleBadge role={s.role} /> : <span className="dim">—</span>}</td>
                      <td>
                        {s.action === "auth.sign_in_refused" && <AlertTriangle size={13} className="warn-icon" />} {ACTION_LABEL[s.action] ?? s.action}
                      </td>
                      <td className="mono dim">{s.ip}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}
    </div>
  );
}

function MappingNote({ mode, viewer }: { mode: Members["mode"]; viewer: boolean }) {
  if (mode === "none") return <>Sign-in is off (VISIONARY_AUTH_MODE=none), so everyone who can reach Visionary is an admin.</>;
  if (mode === "shared")
    return (
      <>
        The team password signs in as admin.{" "}
        {viewer ? "The viewer password signs in read-only." : "Set VISIONARY_VIEWER_PASSWORD for a read-only shared login."}
      </>
    );
  return (
    <>
      Set with VISIONARY_ROLE_MAP, e.g. <code>ADMIN=admin,ANALYST=viewer</code>. A user gets the highest role any of their roles maps to; anyone
      else can't sign in.
    </>
  );
}

// ---------- API keys ----------

const EXPIRY = [
  { value: "0", label: "Never expires" },
  { value: "30", label: "Expires in 30 days" },
  { value: "90", label: "Expires in 90 days" },
  { value: "365", label: "Expires in 1 year" },
];

function keyStatus(k: ApiKey): { label: string; tone: "ok" | "off" } {
  if (k.revoked_at) return { label: "Revoked", tone: "off" };
  if (k.expires_at && new Date(k.expires_at) < new Date()) return { label: "Expired", tone: "off" };
  return { label: "Active", tone: "ok" };
}

function KeysTab() {
  const toast = useToast();
  const [version, setVersion] = useState(0);
  const keys = useQuery(`team-keys#${version}`, (signal) => api<{ keys: ApiKey[] }>("/api/team/keys", { signal }));
  const [creating, setCreating] = useState(false);
  const [revoke, setRevoke] = useState<ApiKey | null>(null);
  const [showRevoked, setShowRevoked] = useState(false);

  const all = keys.data?.keys ?? [];
  const list = showRevoked ? all : all.filter((k) => keyStatus(k).tone === "ok");
  const hidden = all.length - list.length;

  const doRevoke = async (k: ApiKey) => {
    try {
      await api(`/api/team/keys/${k.id}`, { method: "DELETE" });
      toast("ok", `Revoked “${k.name}”`);
      setVersion((v) => v + 1);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
    setRevoke(null);
  };

  return (
    <div className="settings-stack">
      <Card
        title="API keys"
        description="Keys are shown once, when created; Visionary keeps only a hash. Revoking takes effect within 30 seconds on every instance."
        actions={
          <>
            {hidden > 0 || showRevoked ? (
              <button className="btn ghost sm" onClick={() => setShowRevoked(!showRevoked)}>
                {showRevoked ? "Hide revoked and expired" : `Show revoked and expired (${hidden})`}
              </button>
            ) : null}
            <button className="btn primary" onClick={() => setCreating(true)}>
              <Plus size={14} />
              Create key
            </button>
          </>
        }
      >
        {!keys.data ? (
          <LoadState error={keys.error} loading={keys.loading} />
        ) : list.length === 0 ? (
          <div className="settings-empty">
            <KeyRound size={18} />
            <span>No active keys. Create one for scripts (read), SDKs and OTLP (server ingest) or browser and mobile apps (client ingest).</span>
          </div>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Scope</th>
                  <th>Key</th>
                  <th>Created</th>
                  <th>Expires</th>
                  <th>Last used</th>
                  <th>Status</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {list.map((k) => {
                  const st = keyStatus(k);
                  return (
                    <tr key={k.id}>
                      <td>{k.name}</td>
                      <td>
                        <span className="pill" title={SCOPE_INFO[k.scope]?.detail}>
                          {SCOPE_INFO[k.scope]?.label ?? k.scope}
                        </span>
                      </td>
                      <td className="mono">{k.prefix}…</td>
                      <td>
                        <When iso={k.created_at} /> <span className="dim">by {k.created_by}</span>
                      </td>
                      <td>{k.expires_at ? <span title={formatFull(new Date(k.expires_at))}>{new Date(k.expires_at).toLocaleDateString()}</span> : <span className="dim">Never</span>}</td>
                      <td>
                        <When iso={k.last_used_at} />
                      </td>
                      <td>
                        <span className={`key-status ${st.tone}`}>
                          {st.tone === "ok" ? <CheckCircle2 size={13} /> : <CircleSlash size={13} />}
                          {st.label}
                        </span>
                      </td>
                      <td className="num">
                        {!k.revoked_at && (
                          <button className="btn ghost sm" onClick={() => setRevoke(k)}>
                            Revoke
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <Card title="Using keys">
        <div className="settings-roles">
          {(Object.keys(SCOPE_INFO) as KeyScope[]).map((s) => (
            <div key={s} className="settings-role">
              <span className="pill">{SCOPE_INFO[s].label}</span>
              <span>{SCOPE_INFO[s].detail}</span>
            </div>
          ))}
        </div>
      </Card>
      {creating && <CreateKey onClose={() => setCreating(false)} onCreated={() => setVersion((v) => v + 1)} />}
      {revoke && (
        <Modal
          title="Revoke key"
          narrow
          onClose={() => setRevoke(null)}
          footer={
            <>
              <button className="btn ghost" onClick={() => setRevoke(null)}>
                Cancel
              </button>
              <button className="btn danger" onClick={() => doRevoke(revoke)}>
                Revoke
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>
            Revoke “{revoke.name}” ({revoke.prefix}…)? Anything still using it stops working. This can't be undone.
          </p>
        </Modal>
      )}
    </div>
  );
}

function CreateKey({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [scope, setScope] = useState<KeyScope>("read");
  const [expires, setExpires] = useState("90");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);

  const submit = async () => {
    if (!name.trim()) {
      setErr("Name the key after what will use it.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const res = await api<{ key: ApiKey; secret: string }>("/api/team/keys", {
        method: "POST",
        json: { name: name.trim(), scope, expires_days: Number(expires) },
      });
      setSecret(res.secret);
      onCreated();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
    setBusy(false);
  };

  if (secret) {
    return (
      <Modal
        title="Copy your new key"
        onClose={onClose}
        footer={
          <button className="btn primary" onClick={onClose}>
            Done
          </button>
        }
      >
        <div className="notice">
          <AlertTriangle size={16} />
          <span>This is the only time the key is shown. Store it somewhere safe now.</span>
        </div>
        <div className="key-secret">
          <code className="mono">{secret}</code>
          <CopyButton text={secret} />
        </div>
        {scope === "read" && (
          <p className="settings-desc">
            Use it as <code>Authorization: Bearer {secret.slice(0, 12)}…</code> on GET requests such as <code>/api/overview/latency</code>, or POST{" "}
            <code>/api/query</code>.
          </p>
        )}
      </Modal>
    );
  }

  return (
    <Modal
      title="Create API key"
      onClose={onClose}
      footer={
        <>
          <button className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={submit} disabled={busy}>
            Create key
          </button>
        </>
      }
    >
      <div className="settings-form">
        <label className="field">
          <span>Name</span>
          <input className="input" value={name} maxLength={80} onChange={(e) => setName(e.target.value)} placeholder="e.g. Grafana export, checkout-service" autoFocus />
        </label>
        <div className="field">
          <span>Scope</span>
          <div className="scope-picks" role="radiogroup" aria-label="Scope">
            {(Object.keys(SCOPE_INFO) as KeyScope[]).map((s) => (
              <button key={s} role="radio" aria-checked={scope === s} className={`scope-pick ${scope === s ? "on" : ""}`} onClick={() => setScope(s)}>
                <strong>{SCOPE_INFO[s].label}</strong>
                <span>{SCOPE_INFO[s].detail}</span>
              </button>
            ))}
          </div>
        </div>
        <div className="field">
          <span>Expiry</span>
          <Select label="Expiry" align="left" value={expires} options={EXPIRY} onChange={setExpires} />
        </div>
        {err && <span className="form-error">{err}</span>}
      </div>
    </Modal>
  );
}

// ---------- Audit log ----------

const CATEGORIES = [
  { value: "", label: "All actions" },
  { value: "auth", label: "Sign-in" },
  { value: "panel", label: "Saved panels" },
  { value: "dashboard", label: "Dashboards" },
  { value: "api_key", label: "API keys" },
  { value: "alert", label: "Alerts" },
  { value: "channel", label: "Notification channels" },
  { value: "check", label: "Uptime checks" },
];

function AuditTab() {
  const { resolved, tick } = useApp();
  const [category, setCategory] = useState("");
  const [user, setUser] = useState("");
  const [userQuery, setUserQuery] = useState("");
  const [pages, setPages] = useState<AuditEntry[][]>([]);
  const [more, setMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [open, setOpen] = useState<AuditEntry | null>(null);

  const params = useMemo(
    () => ({ from: resolved.from, to: resolved.to, extra: { action: category, user: userQuery, limit: "100" } }),
    [resolved, category, userQuery],
  );
  const first = useQuery(`audit?${rangeQuery(params.from, params.to, params.extra)}#${tick}`, (signal) =>
    api<AuditPage>(`/api/team/audit?${rangeQuery(params.from, params.to, params.extra)}`, { signal }),
  );

  useEffect(() => {
    if (first.data) {
      setPages([first.data.entries]);
      setMore(first.data.more);
    }
  }, [first.data]);

  const entries = pages.flat();
  const loadMore = async () => {
    const last = entries[entries.length - 1];
    if (!last) return;
    setLoadingMore(true);
    try {
      const page = await api<AuditPage>(`/api/team/audit?${rangeQuery(params.from, params.to, { ...params.extra, before: String(last.id) })}`);
      setPages((p) => [...p, page.entries]);
      setMore(page.more);
    } catch {
      setMore(false);
    }
    setLoadingMore(false);
  };

  return (
    <div className="settings-stack">
      <Card
        title="Audit log"
        description={`Who changed what, newest first. Entries are kept for ${first.data?.retention_days ?? 365} days (VISIONARY_AUDIT_RETENTION_DAYS).`}
      >
        <div className="filter-row settings-filters">
          <Select label="Action" align="left" value={category} options={CATEGORIES} onChange={setCategory} />
          <form
            className="settings-user-filter"
            onSubmit={(e) => {
              e.preventDefault();
              setUserQuery(user.trim());
            }}
          >
            <input className="input" placeholder="User ID or name, then Enter" value={user} onChange={(e) => setUser(e.target.value)} />
          </form>
          {(category || userQuery) && (
            <button
              className="btn ghost sm"
              onClick={() => {
                setCategory("");
                setUser("");
                setUserQuery("");
              }}
            >
              Clear filters
            </button>
          )}
          <span className="dim settings-count">{entries.length ? `${entries.length}${more ? "+" : ""} entries` : ""}</span>
        </div>
        {!first.data ? (
          <LoadState error={first.error} loading={first.loading} />
        ) : entries.length === 0 ? (
          <div className="settings-empty">Nothing recorded for these filters in this time range.</div>
        ) : (
          <div className="table-wrap">
            <table className="data audit-table">
              <thead>
                <tr>
                  <th>When</th>
                  <th>User</th>
                  <th>Role</th>
                  <th>Action</th>
                  <th>Target</th>
                  <th>Detail</th>
                  <th>IP</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((e) => (
                  <tr key={e.id} className="clickable" onClick={() => setOpen(e)}>
                    <td>
                      <When iso={e.created_at} />
                    </td>
                    <td>{e.user_name || e.user_id || <span className="dim">—</span>}</td>
                    <td>{e.role ? <RoleBadge role={e.role} /> : <span className="dim">—</span>}</td>
                    <td className="mono">{e.action}</td>
                    <td className="mono">{e.target}</td>
                    <td className="mono dim audit-detail">{detailText(e.detail)}</td>
                    <td className="mono dim">{e.ip}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {more && (
          <div className="load-more">
            <button className="btn" onClick={loadMore} disabled={loadingMore}>
              {loadingMore ? "Loading…" : "Load older entries"}
            </button>
          </div>
        )}
      </Card>
      {open && (
        <Modal title={`${open.action} · ${open.target}`} onClose={() => setOpen(null)}>
          <div className="settings-kv">
            <span>When</span>
            <span>{formatFull(new Date(open.created_at))}</span>
            <span>User</span>
            <span>
              {open.user_name} <span className="mono dim">{open.user_id}</span>
            </span>
            <span>Role</span>
            <span>{open.role || "—"}</span>
            <span>IP</span>
            <span className="mono">{open.ip}</span>
          </div>
          <pre className="code mono">{JSON.stringify(open.detail, null, 2)}</pre>
        </Modal>
      )}
    </div>
  );
}

function detailText(d: unknown): string {
  if (d == null || (typeof d === "object" && Object.keys(d as object).length === 0)) return "";
  return JSON.stringify(d);
}

// ---------- About ----------

function AboutTab() {
  const a = useQuery("team-about", (signal) => api<About>("/api/team/about", { signal }));
  const d = a.data;
  if (!d) return <LoadState error={a.error} loading={a.loading} />;
  return (
    <div className="settings-stack">
      <Card title="Visionary">
        <div className="settings-body">
          <div className="settings-kv">
            <span>Version</span>
            <span className="mono">{d.version}</span>
            <span>Database</span>
            <span>{d.database}</span>
            <span>Sign-in mode</span>
            <span className="mono">{d.auth_mode}</span>
            <span>Go</span>
            <span className="mono">{d.go_version}</span>
          </div>
        </div>
      </Card>
      <Card
        title="Features"
        description={
          <>
            Notification channels for alerts are set up on the <a href="#/alerts">Alerts</a> page.
          </>
        }
      >
        <div className="table-wrap">
          <table className="data">
            <tbody>
              {d.features.map((f) => (
                <tr key={f.name}>
                  <td className="feature-name">{f.name}</td>
                  <td>
                    <span className={`key-status ${f.on ? "ok" : "off"}`}>
                      {f.on ? <CheckCircle2 size={13} /> : <CircleSlash size={13} />}
                      {f.on ? "On" : "Off"}
                    </span>
                  </td>
                  <td className="dim feature-detail">{f.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  );
}
