import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, Eye, EyeOff, KeyRound, Plus, RotateCcw, RotateCw, ShieldOff, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { formatRelative } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { ErrorNotice } from "../components/ErrorNotice";
import { Modal } from "../components/Modal";
import { Select } from "../components/Select";
import { DurationSelect, spanSeconds } from "../components/DurationSelect";
import { useToast } from "../components/Toast";
import { Card, Switch } from "./Settings.profiling";

// ---------- Server ----------

type Kind = "text" | "long" | "secret" | "int" | "float" | "bool" | "duration" | "choice";

interface ConfSetting {
  key: string;
  group: string;
  label: string;
  help: string;
  kind: Kind;
  default: string;
  choices?: string[];
  /** Bounds for numbers, and for durations in seconds. */
  min?: number;
  max?: number;
  restart?: boolean;
  /** Secrets come back empty; set says whether one is stored. */
  value: string;
  set: boolean;
  stored: boolean;
}

interface ConfView {
  groups: { Key: string; Label: string; Help: string }[];
  settings: ConfSetting[];
  /** Restart-only settings saved since the server started. */
  restart_pending?: string[];
}

const getConfig = () => api<ConfView>("/api/settings/config");
/** null resets a setting to its default; a secret left out stays as it is. */
const saveConfig = (values: Record<string, string | null>) => api<ConfView>("/api/settings/config", { method: "PUT", json: values });
/** Asks the server to restart itself. */
const restartServer = () => api<{ restarting: boolean }>("/api/settings/restart", { method: "POST", json: {} });

/** Fetches one stored secret; the server audits each reveal. */
const revealSecret = (key: string) => api<{ value: string }>(`/api/settings/config/${encodeURIComponent(key)}/reveal`, { method: "POST", json: {} });

/** Settings → Server: everything the server used to read from its environment. */
export function ServerTab() {
  const [run, setRun] = useState(0);
  const q = useQuery(`config#${run}`, getConfig);
  const [view, setView] = useState<ConfView>();
  useEffect(() => {
    if (q.data) setView(q.data);
  }, [q.data]);

  if (q.error && !view) return <ErrorNotice title="Could not load server settings" error={q.error} />;
  if (!view) return <div className="settings-empty">Loading…</div>;
  return (
    <div className="settings-stack">
      <div className="settings-intro-row">
        <p className="dim settings-intro">
          The server reads these from the database, so they're the same on every instance and need no redeploy. Only the database connection, <code>VISIONARY_ADDR</code> and{" "}
          <code>VISIONARY_ENABLE_AUTH</code> stay in the environment.
        </p>
        <RestartButton pending={view.restart_pending ?? []} labels={view.settings} />
      </div>
      {(view.restart_pending?.length ?? 0) > 0 && <RestartNotice pending={view.restart_pending!} labels={view.settings} />}
      {view.groups.map((g) => (
        <ConfGroup
          key={g.Key}
          title={g.Label}
          help={g.Help}
          settings={view.settings.filter((s) => s.group === g.Key)}
          onSaved={(v) => {
            setView(v);
            setRun((n) => n + 1);
          }}
        />
      ))}
    </div>
  );
}

const labelOf = (key: string, settings: ConfSetting[]) => settings.find((s) => s.key === key)?.label ?? key;

/** Saved settings that wait for a restart, with the button that does it. */
function RestartNotice({ pending, labels }: { pending: string[]; labels: ConfSetting[] }) {
  return (
    <div className="notice restart-notice" role="status">
      <AlertTriangle size={16} aria-hidden />
      <div className="restart-notice-text">
        <strong>
          {pending.length === 1 ? "1 saved setting applies" : `${pending.length} saved settings apply`} after a restart
        </strong>
        <span className="dim">
          {pending
            .slice(0, 4)
            .map((k) => labelOf(k, labels))
            .join(", ")}
          {pending.length > 4 ? ` and ${pending.length - 4} more` : ""}.
        </span>
      </div>
      <RestartButton pending={pending} labels={labels} primary />
    </div>
  );
}

/**
 * Restarts the server: it finishes requests in flight, starts again with the
 * same command, and the page reloads once it answers. Takes a few seconds.
 */
function RestartButton({ pending, labels, primary }: { pending: string[]; labels: ConfSetting[]; primary?: boolean }) {
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  const [state, setState] = useState<"idle" | "asking" | "waiting">("idle");

  const restart = async () => {
    setState("asking");
    try {
      await restartServer();
    } catch (e) {
      setState("idle");
      toast("err", e instanceof Error ? e.message : String(e));
      return;
    }
    setConfirm(false);
    setState("waiting");
    // The old process answers until it stops, so give it a moment, then wait
    // for the new one.
    const until = Date.now() + 90_000;
    await new Promise((r) => setTimeout(r, 1500));
    while (Date.now() < until) {
      try {
        await getConfig();
        window.location.reload();
        return;
      } catch {
        await new Promise((r) => setTimeout(r, 1000));
      }
    }
    setState("idle");
    toast("err", "The server hasn't come back after 90 seconds. Check its logs; it may need starting by hand.");
  };

  return (
    <>
      <button type="button" className={`btn sm ${primary ? "primary" : ""}`} onClick={() => setConfirm(true)} disabled={state !== "idle"}>
        <RotateCw size={13} className={state === "waiting" ? "spin" : undefined} aria-hidden /> {state === "waiting" ? "Restarting…" : primary ? "Restart now" : "Restart server"}
      </button>
      {confirm && (
        <Modal
          title="Restart Visionary?"
          narrow
          onClose={() => state === "idle" && setConfirm(false)}
          footer={
            <>
              <button type="button" className="btn ghost" onClick={() => setConfirm(false)} disabled={state !== "idle"}>
                Cancel
              </button>
              <button type="button" className="btn primary" onClick={() => void restart()} disabled={state !== "idle"}>
                {state === "asking" ? "Restarting…" : "Restart"}
              </button>
            </>
          }
        >
          <p style={{ margin: 0, lineHeight: 1.55 }}>
            It finishes the requests it's handling, then starts again with the same command. The dashboard and ingestion are unavailable for a few seconds; SDKs retry what they
            couldn't send.
          </p>
          {pending.length > 0 && (
            <p className="dim" style={{ margin: "10px 0 0", lineHeight: 1.55 }}>
              Applies: {pending.map((k) => labelOf(k, labels)).join(", ")}.
            </p>
          )}
        </Modal>
      )}
    </>
  );
}

function ConfGroup({ title, help, settings, onSaved }: { title: string; help: string; settings: ConfSetting[]; onSaved: (v: ConfView) => void }) {
  const toast = useToast();
  const [draft, setDraft] = useState<Record<string, string | null>>({});
  const [busy, setBusy] = useState(false);
  useEffect(() => setDraft({}), [settings]);
  const changed = Object.keys(draft);
  const restart = settings.some((s) => s.restart && s.key in draft);

  const save = async () => {
    setBusy(true);
    try {
      const v = await saveConfig(draft);
      toast("ok", restart ? `${title} saved. Some of it applies after a restart; use Restart now at the top.` : `${title} saved.`);
      onSaved(v);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card
      title={title}
      description={help || undefined}
      actions={
        <button type="button" className="btn primary sm" disabled={changed.length === 0 || busy} onClick={save}>
          {busy ? "Saving…" : changed.length ? `Save ${changed.length} change${changed.length > 1 ? "s" : ""}` : "Save"}
        </button>
      }
    >
      <div className="settings-body pset">
        {settings.map((s) => (
          <ConfRow
            key={s.key}
            s={s}
            draft={draft[s.key]}
            dirty={s.key in draft}
            onChange={(v) => {
              const next = { ...draft };
              if (v === undefined) delete next[s.key];
              else next[s.key] = v;
              setDraft(next);
            }}
          />
        ))}
      </div>
    </Card>
  );
}

/** One setting. draft is undefined while unchanged, null when reset. */
function ConfRow({ s, draft, dirty, onChange }: { s: ConfSetting; draft: string | null | undefined; dirty: boolean; onChange: (v: string | null | undefined) => void }) {
  const value = draft === undefined ? s.value : draft === null ? (s.kind === "secret" ? "" : s.default) : draft;
  const set = (v: string) => onChange(v === s.value && s.kind !== "secret" ? undefined : v);
  const canReset = s.stored && draft !== null && (s.kind === "secret" ? s.set : s.value !== s.default);
  const toast = useToast();
  // A secret is shown only after an eye click: revealed holds what the server sent.
  const [visible, setVisible] = useState(false);
  const [revealed, setRevealed] = useState<string>();
  useEffect(() => {
    setVisible(false);
    setRevealed(undefined);
  }, [s]);
  const toggleSecret = async () => {
    if (visible) return setVisible(false);
    if (s.set && draft === undefined && revealed === undefined) {
      try {
        setRevealed((await revealSecret(s.key)).value);
      } catch (e) {
        toast("err", e instanceof Error ? e.message : String(e));
        return;
      }
    }
    setVisible(true);
  };

  let control;
  switch (s.kind) {
    case "bool":
      control = <Switch on={value === "true"} label={s.label} onChange={(on) => set(String(on))} />;
      break;
    case "choice":
      control = (
        <Select
          label={s.label}
          value={value}
          options={(s.choices ?? []).map((c) => ({ value: c, label: c ? c[0].toUpperCase() + c.slice(1) : "Off" }))}
          onChange={set}
          maxWidth={200}
        />
      );
      break;
    case "duration":
      control = (
        <DurationSelect
          label={s.label}
          value={value}
          min={s.min}
          max={s.max}
          onChange={(v) => onChange(spanSeconds(v) === spanSeconds(s.value) ? undefined : v)}
          maxWidth={200}
        />
      );
      break;
    case "long":
      control = <textarea className="textarea conf-long" rows={4} value={value} placeholder={s.default} onChange={(e) => set(e.target.value)} />;
      break;
    case "secret":
      control = (
        <>
          <input
            className="input conf-input"
            type={visible ? "text" : "password"}
            autoComplete="new-password"
            spellCheck={false}
            value={draft ?? (visible ? (revealed ?? "") : "")}
            placeholder={draft === null ? "Cleared on save" : s.set ? "Set; type to replace" : "Not set"}
            onChange={(e) => onChange(e.target.value === "" || e.target.value === revealed ? undefined : e.target.value)}
          />
          {draft !== null && (s.set || draft) && (
            <button type="button" className="icon-btn" title={visible ? "Hide" : "Show"} aria-label={visible ? `Hide ${s.label}` : `Show ${s.label}`} aria-pressed={visible} onClick={toggleSecret}>
              {visible ? <EyeOff size={14} /> : <Eye size={14} />}
            </button>
          )}
        </>
      );
      break;
    default:
      control = (
        <input
          className={`input conf-input ${s.kind === "text" ? "" : "conf-short"}`}
          inputMode={s.kind === "int" || s.kind === "float" ? "decimal" : undefined}
          value={value}
          placeholder={s.default}
          onChange={(e) => set(e.target.value)}
        />
      );
  }

  return (
    <div className={`pset-row conf-row ${dirty ? "changed" : ""} ${s.kind === "long" ? "conf-row-wide" : ""}`}>
      <div className="conf-what">
        <div className="pset-label">
          {s.label}
          {s.restart && <span className="conf-tag">After restart</span>}
        </div>
        {s.help && <div className="dim pset-hint">{s.help}</div>}
      </div>
      <div className="conf-control">
        {control}
        {canReset && (
          <button type="button" className="icon-btn" title={s.kind === "secret" ? "Clear" : `Reset to ${s.default || "empty"}`} aria-label="Reset" onClick={() => onChange(null)}>
            <RotateCcw size={14} />
          </button>
        )}
      </div>
    </div>
  );
}

// ---------- Sign-in ----------

type Mode = "shared" | "accounts" | "password" | "otp";

interface Endpoint {
  method: string;
  path: string;
  body?: Record<string, unknown>;
  headers?: Record<string, string>;
}

interface AuthConfig {
  provider: string;
  type: Mode;
  base_url: string;
  timeout?: string;
  identifier: { label: string; placeholder?: string; input?: string; normalize?: string; pattern?: string; pattern_hint?: string };
  code_length?: number;
  resend_seconds?: number;
  location?: string;
  request_code?: Endpoint;
  verify: Endpoint;
  profile?: Endpoint;
  logout?: Endpoint;
  token: { header: string; prefix: string };
  headers?: Record<string, string>;
  fields: { sent_to?: string; token: string; expires_at?: string; user_id?: string; name?: string; handle?: string; role: string; error_message?: string };
  allowed_roles: string[];
}

interface SigninView {
  env_enabled: boolean;
  open?: "disabled" | "unconfigured";
  error?: string;
  config: AuthConfig | null;
  role_map: string;
  shared_password_set: boolean;
  viewer_password_set: boolean;
}

const getSignin = () => api<SigninView>("/api/settings/signin");

const BLANK: AuthConfig = {
  provider: "",
  type: "shared",
  base_url: "",
  identifier: { label: "Email", input: "email", normalize: "lowercase" },
  verify: { method: "POST", path: "" },
  token: { header: "Authorization", prefix: "Bearer " },
  fields: { token: "data.token", role: "data.role" },
  allowed_roles: [],
};

const MODES = [
  { value: "accounts", label: "Accounts (a login per person)" },
  { value: "shared", label: "Team password" },
  { value: "password", label: "Your API: username and password" },
  { value: "otp", label: "Your API: one-time code" },
];
const METHODS = ["POST", "GET", "PUT"].map((m) => ({ value: m, label: m }));
const INPUTS = [
  { value: "email", label: "Email" },
  { value: "tel", label: "Phone" },
  { value: "text", label: "Text" },
];
const NORMALIZE = [
  { value: "", label: "As typed" },
  { value: "lowercase", label: "Lowercase" },
  { value: "digits", label: "Digits only" },
  { value: "trim", label: "Trim spaces" },
];
const LOCATION = [
  { value: "none", label: "Don't ask" },
  { value: "optional", label: "Ask, optional" },
  { value: "required", label: "Required" },
];

/** Settings → Sign-in: how people get into Visionary. */
export function SigninTab() {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`signin#${run}`, getSignin);
  const [cfg, setCfg] = useState<AuthConfig>(BLANK);
  const [roleMap, setRoleMap] = useState("");
  const [shared, setShared] = useState("");
  const [viewer, setViewer] = useState<string | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!q.data) return;
    const c = q.data.config;
    // Allowed roles are admins: shown, and saved, as part of the mapping.
    const allowed = (c?.allowed_roles ?? []).filter((r) => !q.data!.role_map.split(",").some((e) => e.split("=")[0].trim().toUpperCase() === r.toUpperCase()));
    setCfg(c ? { ...BLANK, ...c, identifier: { ...BLANK.identifier, ...c.identifier }, allowed_roles: [] } : BLANK);
    setRoleMap([q.data.role_map, ...allowed.map((r) => `${r}=admin`)].filter(Boolean).join(","));
    setShared("");
    setViewer(undefined);
  }, [q.data]);

  if (q.error && !q.data) return <ErrorNotice title="Could not load sign-in" error={q.error} />;
  const v = q.data;
  if (!v) return <div className="settings-empty">Loading…</div>;

  const delegated = cfg.type === "password" || cfg.type === "otp";
  const up = (patch: Partial<AuthConfig>) => setCfg({ ...cfg, ...patch });

  const save = async () => {
    setBusy(true);
    try {
      const body: Record<string, unknown> = { config: delegated ? cfg : { type: cfg.type, provider: "Visionary" }, role_map: delegated ? roleMap : "" };
      if (cfg.type === "shared") {
        if (shared) body.shared_password = shared;
        if (viewer !== undefined) body.viewer_password = viewer ?? "";
      }
      await api<SigninView>("/api/settings/signin", { method: "PUT", json: body });
      toast("ok", "Sign-in saved. It applies now; people sign in again if their way in changed.");
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="settings-stack">
      <SigninState v={v} />
      <Card
        title="How people sign in"
        description="Give each person their own username and password here, share one team password, or let people sign in with your own API's accounts, which then decides their role."
        actions={
          <button type="button" className="btn primary sm" disabled={busy} onClick={save}>
            {busy ? "Saving…" : "Save sign-in"}
          </button>
        }
      >
        <div className="settings-body pset">
          <Row label="Way in">
            <Select label="Way in" value={cfg.type} options={MODES} onChange={(t) => up({ type: t as Mode })} maxWidth={280} />
          </Row>
          {cfg.type === "shared" ? (
            <>
              <Row label="Team password" hint="At least 12 characters. Signs in as admin.">
                <input
                  className="input conf-input"
                  type="password"
                  autoComplete="new-password"
                  value={shared}
                  placeholder={v.shared_password_set && v.config?.type === "shared" ? "Set; type to replace" : "Not set"}
                  onChange={(e) => setShared(e.target.value)}
                />
              </Row>
              <Row label="Read-only password" hint="Optional: a second password that signs in as viewer.">
                <div className="conf-control">
                  <input
                    className="input conf-input"
                    type="password"
                    autoComplete="new-password"
                    value={viewer ?? ""}
                    placeholder={viewer === null ? "Removed on save" : v.viewer_password_set ? "Set; type to replace" : "Not set"}
                    onChange={(e) => setViewer(e.target.value === "" ? undefined : e.target.value)}
                  />
                  {v.viewer_password_set && viewer !== null && (
                    <button type="button" className="icon-btn" title="Remove" aria-label="Remove the read-only password" onClick={() => setViewer(null)}>
                      <RotateCcw size={14} />
                    </button>
                  )}
                </div>
              </Row>
            </>
          ) : cfg.type === "accounts" ? (
            <Row label="Accounts" hint="People sign in with the username and password you give them below, and get the role you choose. Save with at least one admin account.">
              <span className="dim">{v.config?.type === "accounts" ? "In use" : "Not in use until you save"}</span>
            </Row>
          ) : (
            <DelegatedForm cfg={cfg} up={up} />
          )}
        </div>
      </Card>
      {cfg.type === "accounts" && <AccountsCard />}
      {delegated && (
        <Card
          title="Who gets in, and as what"
          description={
            <>
              Map your API's roles to Visionary roles, comma separated: <code>ADMIN=admin,ENGINEER=editor,SUPPORT=viewer</code>. <code>*</code> matches every user of your API
              (<code>ADMIN=admin,*=viewer</code>). Anyone whose role isn't listed is turned away.
            </>
          }
        >
          <div className="settings-body pset">
            <Row label="Role mapping">
              <input className="input conf-input" value={roleMap} placeholder="ADMIN=admin,*=viewer" onChange={(e) => setRoleMap(e.target.value)} />
            </Row>
            <Row label="Where the role is" hint="A dotted path into the profile (or sign-in) response. Not needed when the mapping is only *.">
              <input className="input conf-input" value={cfg.fields.role} placeholder="data.role" onChange={(e) => up({ fields: { ...cfg.fields, role: e.target.value } })} />
            </Row>
          </div>
        </Card>
      )}
    </div>
  );
}

function SigninState({ v }: { v: SigninView }) {
  if (!v.env_enabled)
    return (
      <div className="notice warn">
        <AlertTriangle size={16} />
        <span>
          Sign-in is off because the server runs with <code>VISIONARY_ENABLE_AUTH=false</code>: anyone who can reach it is an admin. Save sign-in here, then remove the variable and
          restart.
        </span>
      </div>
    );
  if (v.error)
    return (
      <div className="notice error">
        <AlertTriangle size={16} />
        <span>The saved sign-in can't be used, so nobody can sign in: {v.error}. Fix it below and save.</span>
      </div>
    );
  if (v.open === "unconfigured")
    return (
      <div className="notice warn">
        <AlertTriangle size={16} />
        <span>Sign-in isn't set up yet: anyone who can reach this server is an admin. Choose a way in below and save.</span>
      </div>
    );
  return null;
}

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="pset-row conf-row">
      <div className="conf-what">
        <div className="pset-label">{label}</div>
        {hint && <div className="dim pset-hint">{hint}</div>}
      </div>
      <div className="conf-control">{children}</div>
    </div>
  );
}

function DelegatedForm({ cfg, up }: { cfg: AuthConfig; up: (p: Partial<AuthConfig>) => void }) {
  const otp = cfg.type === "otp";
  const field = (k: keyof AuthConfig["fields"], label: string, hint: string, placeholder: string) => (
    <Row label={label} hint={hint}>
      <input className="input conf-input" value={cfg.fields[k] ?? ""} placeholder={placeholder} onChange={(e) => up({ fields: { ...cfg.fields, [k]: e.target.value } })} />
    </Row>
  );
  return (
    <>
      <Row label="API base URL" hint="Your API's address; endpoint paths below are relative to it.">
        <input className="input conf-input" value={cfg.base_url} placeholder="https://api.example.com" onChange={(e) => up({ base_url: e.target.value })} />
      </Row>
      <Row label="Provider name" hint='Shown on the sign-in screen: "Use your … account". Defaults to the API host.'>
        <input className="input conf-input" value={cfg.provider} placeholder="Acme" onChange={(e) => up({ provider: e.target.value })} />
      </Row>
      <Row label="Identifier" hint="What people type to sign in.">
        <div className="conf-control conf-inline">
          <input className="input conf-short" value={cfg.identifier.label} placeholder="Email" onChange={(e) => up({ identifier: { ...cfg.identifier, label: e.target.value } })} />
          <Select label="Input" value={cfg.identifier.input ?? "text"} options={INPUTS} onChange={(x) => up({ identifier: { ...cfg.identifier, input: x } })} />
          <Select label="Normalize" value={cfg.identifier.normalize ?? ""} options={NORMALIZE} onChange={(x) => up({ identifier: { ...cfg.identifier, normalize: x || undefined } })} />
        </div>
      </Row>
      {otp && (
        <EndpointRow
          label="Send the code"
          hint="Body values may use {{identifier}}."
          e={cfg.request_code ?? { method: "POST", path: "" }}
          bodyHint='{"phone": "{{identifier}}"}'
          onChange={(e) => up({ request_code: e })}
        />
      )}
      <EndpointRow
        label={otp ? "Verify the code" : "Log in"}
        hint={otp ? "Body values may use {{identifier}} and {{code}}." : "Body values may use {{identifier}} and {{password}}."}
        e={cfg.verify}
        bodyHint={otp ? '{"phone": "{{identifier}}", "code": "{{code}}"}' : '{"email": "{{identifier}}", "password": "{{password}}"}'}
        onChange={(e) => up({ verify: e })}
      />
      <EndpointRow label="Profile (optional)" hint="Returns the signed-in user; lets roles be re-checked while signed in." e={cfg.profile} optional onChange={(e) => up({ profile: e })} />
      <EndpointRow label="Log out (optional)" hint="Ends the API's session when someone signs out." e={cfg.logout} optional onChange={(e) => up({ logout: e })} />
      {field("token", "Where the token is", "In the sign-in response.", "data.token")}
      {field("expires_at", "Token expiry", "Optional.", "data.expires_at")}
      {field("user_id", "User ID", "Optional.", "data.user.id")}
      {field("name", "Name", "Optional.", "data.user.name")}
      {field("handle", "Handle", "Optional.", "data.user.handle")}
      {otp && field("sent_to", "Where the code went", "Optional; shown as “We sent a code to …”.", "data.sent_to")}
      {field("error_message", "Error message", "Where the API puts its error, shown on failed sign-ins.", "message")}
      <Row label="Token header" hint="How the token is sent back to the API.">
        <div className="conf-control conf-inline">
          <input className="input conf-short" value={cfg.token.header} placeholder="Authorization" onChange={(e) => up({ token: { ...cfg.token, header: e.target.value } })} />
          <input className="input conf-short" value={cfg.token.prefix} placeholder="Bearer " onChange={(e) => up({ token: { ...cfg.token, prefix: e.target.value } })} />
        </div>
      </Row>
      {otp && (
        <Row label="Code" hint="Digits in the code, and seconds before it can be sent again.">
          <div className="conf-control conf-inline">
            <input className="input conf-num" inputMode="numeric" value={cfg.code_length ?? 6} onChange={(e) => up({ code_length: Number(e.target.value) || undefined })} />
            <input className="input conf-num" inputMode="numeric" value={cfg.resend_seconds ?? 30} onChange={(e) => up({ resend_seconds: Number(e.target.value) || undefined })} />
          </div>
        </Row>
      )}
      <Row label="Location" hint="For APIs that want the user's position at sign-in ({{latitude}}, {{longitude}}).">
        <Select label="Location" value={cfg.location ?? "none"} options={LOCATION} onChange={(x) => up({ location: x })} />
      </Row>
      <JsonRow label="Headers on every call" hint='JSON object, e.g. {"X-API-Key": "…"}.' value={cfg.headers} onChange={(h) => up({ headers: h as Record<string, string> | undefined })} />
    </>
  );
}

function EndpointRow({ label, hint, e, optional, bodyHint = "Optional JSON body", onChange }: { label: string; hint: string; e?: Endpoint; optional?: boolean; bodyHint?: string; onChange: (e: Endpoint | undefined) => void }) {
  const cur = e ?? { method: optional && label.startsWith("Profile") ? "GET" : "POST", path: "" };
  const set = (p: Partial<Endpoint>) => {
    const next = { ...cur, ...p };
    onChange(optional && !next.path ? undefined : next);
  };
  return (
    <div className="pset-row conf-row conf-row-wide">
      <div className="conf-what">
        <div className="pset-label">{label}</div>
        <div className="dim pset-hint">{hint}</div>
      </div>
      <div className="conf-endpoint">
        <div className="conf-inline">
          <div className="conf-method">
            <Select label="Method" value={cur.method} options={METHODS} onChange={(m) => set({ method: m })} />
          </div>
          <input className="input conf-input" value={cur.path} placeholder={optional ? "Not used" : "/auth/login"} onChange={(x) => set({ path: x.target.value })} />
        </div>
        {(cur.method !== "GET" || cur.body) && cur.path && (
          <JsonEditor value={cur.body} placeholder={bodyHint} onChange={(b) => set({ body: b as Record<string, unknown> | undefined })} />
        )}
      </div>
    </div>
  );
}

function JsonRow({ label, hint, value, onChange }: { label: string; hint: string; value: unknown; onChange: (v: unknown) => void }) {
  return (
    <div className="pset-row conf-row conf-row-wide">
      <div className="conf-what">
        <div className="pset-label">{label}</div>
        <div className="dim pset-hint">{hint}</div>
      </div>
      <JsonEditor value={value} placeholder="{}" onChange={onChange} />
    </div>
  );
}

/** A JSON object in a textarea; it reports a value only while the text parses. */
function JsonEditor({ value, placeholder, onChange }: { value: unknown; placeholder: string; onChange: (v: unknown) => void }) {
  const initial = useMemo(() => (value && Object.keys(value as object).length ? JSON.stringify(value, null, 2) : ""), []); // eslint-disable-line react-hooks/exhaustive-deps
  const [text, setText] = useState(initial);
  const [bad, setBad] = useState(false);
  return (
    <div className="conf-json">
      <textarea
        className={`textarea mono conf-long ${bad ? "invalid" : ""}`}
        rows={3}
        value={text}
        placeholder={placeholder}
        onChange={(e) => {
          setText(e.target.value);
          if (!e.target.value.trim()) {
            setBad(false);
            onChange(undefined);
            return;
          }
          try {
            const v = JSON.parse(e.target.value);
            const ok = v && typeof v === "object" && !Array.isArray(v);
            setBad(!ok);
            if (ok) onChange(v);
          } catch {
            setBad(true);
          }
        }}
      />
      {bad && <div className="conf-json-err">Not a JSON object yet.</div>}
    </div>
  );
}

// ---------- Accounts ----------

interface Account {
  id: string;
  username: string;
  name: string;
  role: "viewer" | "editor" | "admin";
  disabled: boolean;
  created_by: string;
  created_at: string;
  last_sign_in_at?: string;
  password_changed_at: string;
  two_step: boolean;
}

const ROLES = [
  { value: "viewer", label: "Viewer" },
  { value: "editor", label: "Editor" },
  { value: "admin", label: "Admin" },
];

/** Settings → Sign-in → Accounts: one username and password per person. */
function AccountsCard() {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`accounts#${run}`, () => api<{ accounts: Account[] }>("/api/team/accounts"));
  const [adding, setAdding] = useState(false);
  const [resetting, setResetting] = useState<Account | null>(null);
  const reload = () => setRun((n) => n + 1);

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn();
      toast("ok", ok);
      reload();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  const update = (a: Account, patch: Partial<Account> & { password?: string }, ok: string) =>
    act(() => api(`/api/team/accounts/${a.id}`, { method: "PUT", json: patch }), ok);

  const list = q.data?.accounts ?? [];
  return (
    <Card
      title="Accounts"
      description="Each person signs in with their own username and password and gets the role set here. Disabling, deleting or changing someone's role applies within a minute; a new password signs them out everywhere."
      actions={
        <button type="button" className="btn sm" onClick={() => setAdding(true)}>
          <Plus size={14} /> Add account
        </button>
      }
    >
      {q.error && !q.data ? (
        <div className="settings-body">
          <ErrorNotice title="Could not load accounts" error={q.error} />
        </div>
      ) : !q.data ? (
        <div className="settings-empty">Loading…</div>
      ) : list.length === 0 && !adding ? (
        <div className="settings-empty">No accounts yet. Add one for yourself as an admin first.</div>
      ) : (
        <div className="table-wrap">
          <table className="data acct-table">
            <thead>
              <tr>
                <th>Username</th>
                <th>Name</th>
                <th>Role</th>
                <th>Last sign-in</th>
                <th>Two-step</th>
                <th>Status</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {list.map((a) => (
                <tr key={a.id} className={a.disabled ? "acct-disabled" : ""}>
                  <td className="mono">{a.username}</td>
                  <td>{a.name || <span className="dim">—</span>}</td>
                  <td>
                    <Select label={`Role of ${a.username}`} value={a.role} options={ROLES} onChange={(r) => update(a, { role: r as Account["role"] }, `${a.username} is now ${r === "admin" ? "an admin" : `a ${r}`}.`)} />
                  </td>
                  <td>{a.last_sign_in_at ? formatRelative(a.last_sign_in_at) : <span className="dim">Never</span>}</td>
                  <td>{a.two_step ? "On" : <span className="dim">Off</span>}</td>
                  <td>
                    <Switch
                      on={!a.disabled}
                      label={a.disabled ? `Enable ${a.username}` : `Disable ${a.username}`}
                      onChange={(on) => update(a, { disabled: !on }, on ? `${a.username} can sign in again.` : `${a.username} is disabled.`)}
                    />
                  </td>
                  <td className="acct-actions">
                    {a.two_step && (
                      <button
                        type="button"
                        className="icon-btn"
                        title="Turn off two-step sign-in"
                        aria-label={`Turn off two-step sign-in for ${a.username}`}
                        onClick={() => {
                          if (window.confirm(`Turn off two-step sign-in for ${a.username}? Do this only if they lost their phone and recovery codes; they can set it up again.`)) {
                            void act(() => api(`/api/team/accounts/${a.id}/two-step/reset`, { method: "POST", json: {} }), `Two-step sign-in is off for ${a.username}.`);
                          }
                        }}
                      >
                        <ShieldOff size={14} />
                      </button>
                    )}
                    <button type="button" className="icon-btn" title="Set a new password" aria-label={`Set a new password for ${a.username}`} onClick={() => setResetting(a)}>
                      <KeyRound size={14} />
                    </button>
                    <button
                      type="button"
                      className="icon-btn"
                      title="Delete"
                      aria-label={`Delete ${a.username}`}
                      onClick={() => {
                        if (window.confirm(`Delete the account ${a.username}? They can't sign in afterwards.`)) {
                          void act(() => api(`/api/team/accounts/${a.id}`, { method: "DELETE" }), `${a.username} was deleted.`);
                        }
                      }}
                    >
                      <Trash2 size={14} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {adding && (
        <AccountForm
          onCancel={() => setAdding(false)}
          onSave={(f) =>
            act(() => api("/api/team/accounts", { method: "POST", json: f }), `${f.username} can sign in now.`).then(() => setAdding(false))
          }
        />
      )}
      {resetting && (
        <PasswordForm
          title={`New password for ${resetting.username}`}
          onCancel={() => setResetting(null)}
          onSave={(password) => update(resetting, { password }, `${resetting.username} has a new password and was signed out.`).then(() => setResetting(null))}
        />
      )}
    </Card>
  );
}

function AccountForm({ onSave, onCancel }: { onSave: (f: { username: string; name: string; role: string; password: string }) => Promise<unknown>; onCancel: () => void }) {
  const [f, setF] = useState({ username: "", name: "", role: "viewer", password: "" });
  const [busy, setBusy] = useState(false);
  const short = f.password.length > 0 && f.password.length < 12;
  return (
    <form
      className="settings-body pset acct-form"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        await onSave(f);
        setBusy(false);
      }}
    >
      <Row label="Username" hint="3 to 64 characters: letters, digits and . _ @ + -. An email address works.">
        <input className="input conf-input" autoComplete="off" value={f.username} onChange={(e) => setF({ ...f, username: e.target.value })} />
      </Row>
      <Row label="Name" hint="Optional; shown in the audit log and the sidebar.">
        <input className="input conf-input" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />
      </Row>
      <Row label="Role">
        <Select label="Role" value={f.role} options={ROLES} onChange={(r) => setF({ ...f, role: r })} />
      </Row>
      <Row label="Password" hint="At least 12 characters. Share it with them privately; they can change it under Members & roles.">
        <input className="input conf-input" type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} />
      </Row>
      {short && <div className="conf-json-err">Passwords are at least 12 characters.</div>}
      <div className="acct-form-actions">
        <button type="button" className="btn sm" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" className="btn primary sm" disabled={busy || !f.username || f.password.length < 12}>
          {busy ? "Adding…" : "Add account"}
        </button>
      </div>
    </form>
  );
}

function PasswordForm({ title, onSave, onCancel }: { title: string; onSave: (password: string) => Promise<unknown>; onCancel: () => void }) {
  const [pw, setPw] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <form
      className="settings-body pset acct-form"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        await onSave(pw);
        setBusy(false);
      }}
    >
      <Row label={title} hint="At least 12 characters. Signs them out everywhere.">
        <input className="input conf-input" type="password" autoComplete="new-password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} />
      </Row>
      <div className="acct-form-actions">
        <button type="button" className="btn sm" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" className="btn primary sm" disabled={busy || pw.length < 12}>
          {busy ? "Saving…" : "Set password"}
        </button>
      </div>
    </form>
  );
}

/** Settings → Members & roles: someone signed in with an account changes its password. */
export function OwnPasswordCard() {
  const toast = useToast();
  const [f, setF] = useState({ current: "", password: "" });
  const [busy, setBusy] = useState(false);
  return (
    <Card title="Your password" description="Changing it signs you out everywhere else.">
      <form
        className="settings-body pset"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            await api("/api/me/password", { method: "PUT", json: f });
            toast("ok", "Password changed.");
            setF({ current: "", password: "" });
          } catch (err) {
            toast("err", err instanceof Error ? err.message : String(err));
          } finally {
            setBusy(false);
          }
        }}
      >
        <Row label="Current password">
          <input className="input conf-input" type="password" autoComplete="current-password" value={f.current} onChange={(e) => setF({ ...f, current: e.target.value })} />
        </Row>
        <Row label="New password" hint="At least 12 characters.">
          <input className="input conf-input" type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} />
        </Row>
        <div className="acct-form-actions">
          <button type="submit" className="btn primary sm" disabled={busy || !f.current || f.password.length < 12}>
            {busy ? "Saving…" : "Change password"}
          </button>
        </div>
      </form>
    </Card>
  );
}
