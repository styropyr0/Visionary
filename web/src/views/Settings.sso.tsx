import { useEffect, useState } from "react";
import { AlertTriangle, ExternalLink, Plus, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { formatRelative } from "../lib/time";
import { useQuery } from "../lib/useQuery";
import { CopyButton } from "../components/Modal";
import { ErrorNotice } from "../components/ErrorNotice";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { Card, Switch } from "./Settings.profiling";
import { Row, type Account } from "./Settings.config";

// ---------- Single sign-on and SCIM (Settings → Sign-in) ----------

type Kind = "oidc" | "saml";

interface Provider {
  id: string;
  kind: Kind;
  name: string;
  enabled: boolean;
  issuer?: string;
  client_id?: string;
  scopes?: string[];
  metadata_url?: string;
  metadata_xml?: string;
  email_attr?: string;
  name_attr?: string;
  groups_claim?: string;
  allowed_domains?: string[];
  role_map?: string;
  default_role?: string;
  create_users: boolean;
  client_secret_set?: boolean;
  redirect_url?: string;
  entity_id?: string;
  acs_url?: string;
}

interface SsoView {
  config: { public_url: string; providers: Provider[] | null; scim: { role_map: string; default_role: string } };
  error?: string;
  sp_cert?: string;
  scim_url?: string;
  sign_in_type: string;
}

/** Where each identity provider's issuer lives and what it calls groups. */
const PRESETS: { value: string; label: string; issuer: string; groups: string; hint: string }[] = [
  { value: "google", label: "Google", issuer: "https://accounts.google.com", groups: "", hint: "Google sends no groups; use a default role and allowed domains." },
  {
    value: "entra",
    label: "Microsoft Entra ID",
    issuer: "https://login.microsoftonline.com/<tenant-id>/v2.0",
    groups: "roles",
    hint: "Put your tenant ID in the URL. App roles arrive in roles; groups arrive as object IDs in groups.",
  },
  { value: "okta", label: "Okta", issuer: "https://<your-org>.okta.com", groups: "groups", hint: "Add a groups claim to the app's ID token in Okta." },
  { value: "auth0", label: "Auth0", issuer: "https://<tenant>.auth0.com", groups: "", hint: "Auth0 sends roles only through an Action, under a namespaced claim such as https://example.com/roles." },
  { value: "keycloak", label: "Keycloak", issuer: "https://<host>/realms/<realm>", groups: "groups", hint: "Add a Group Membership mapper (claim groups, full path off) to the client." },
  { value: "other", label: "Another OpenID provider", issuer: "", groups: "groups", hint: "Any provider with /.well-known/openid-configuration under its issuer URL." },
];

function presetOf(issuer: string) {
  if (issuer.includes("accounts.google.com")) return "google";
  if (issuer.includes("login.microsoftonline.com")) return "entra";
  if (issuer.includes(".okta.com") || issuer.includes(".oktapreview.com")) return "okta";
  if (issuer.includes(".auth0.com")) return "auth0";
  if (issuer.includes("/realms/")) return "keycloak";
  return "other";
}

const DEFAULT_ROLES = [
  { value: "", label: "No access" },
  { value: "viewer", label: "Viewer" },
  { value: "editor", label: "Editor" },
  { value: "admin", label: "Admin" },
];

const KINDS = [
  { value: "oidc", label: "OpenID Connect" },
  { value: "saml", label: "SAML 2.0" },
];

const list = (s: string) =>
  s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);

const newID = (name: string, taken: string[]) => {
  const base = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 24) || "sso";
  let id = base;
  for (let n = 2; taken.includes(id); n++) id = `${base}-${n}`;
  return id;
};

/** Providers people can sign in with, next to the way in above, and SCIM provisioning. */
export function SsoSettings() {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`sso#${run}`, () => api<SsoView>("/api/settings/sso"));
  const [publicURL, setPublicURL] = useState("");
  const [providers, setProviders] = useState<Provider[]>([]);
  const [secrets, setSecrets] = useState<Record<string, string | null>>({});
  const [scim, setScim] = useState({ role_map: "", default_role: "" });
  const [keyPair, setKeyPair] = useState("");
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!q.data) return;
    setPublicURL(q.data.config.public_url || window.location.origin);
    setProviders(q.data.config.providers ?? []);
    setScim(q.data.config.scim ?? { role_map: "", default_role: "" });
    setSecrets({});
    setKeyPair("");
    setDirty(false);
  }, [q.data]);

  if (q.error && !q.data) return <ErrorNotice title="Could not load single sign-on" error={q.error} />;
  const v = q.data;
  if (!v) return null;

  const change = (i: number, patch: Partial<Provider>) => {
    setProviders(providers.map((p, j) => (j === i ? { ...p, ...patch } : p)));
    setDirty(true);
  };
  const add = () => {
    const name = `Provider ${providers.length + 1}`;
    setProviders([
      ...providers,
      { id: newID(name, providers.map((p) => p.id)), kind: "oidc", name, enabled: false, issuer: "", client_id: "", groups_claim: "groups", default_role: "viewer", create_users: true },
    ]);
    setDirty(true);
  };

  const save = async () => {
    setBusy(true);
    try {
      const body = {
        public_url: publicURL.trim(),
        providers: providers.map(({ client_secret_set, redirect_url, entity_id, acs_url, ...p }) => p),
        scim,
        secrets,
        sp_key_pair: keyPair.trim() || undefined,
      };
      await api<SsoView>("/api/settings/sso", { method: "PUT", json: body });
      toast("ok", "Single sign-on saved. It applies now on this server and within 30 seconds on the others.");
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const saved = (id: string) => (v.config.providers ?? []).find((p) => p.id === id);
  return (
    <>
      <Card
        title="Single sign-on"
        description={
          <>
            Sign in through your identity provider (Google, Microsoft Entra ID, Okta, Auth0, Keycloak or any OpenID Connect or SAML 2.0 provider). Its buttons show on the sign-in
            screen next to the way in above, so you can keep a password account as a way back in.
          </>
        }
        actions={
          <>
            <button type="button" className="btn sm" onClick={add} disabled={providers.length >= 10}>
              <Plus size={14} /> Add provider
            </button>
            <button type="button" className="btn primary sm" disabled={busy} onClick={save}>
              {busy ? "Saving…" : "Save single sign-on"}
            </button>
          </>
        }
      >
        <div className="settings-body pset">
          {v.error && (
            <div className="notice error">
              <AlertTriangle size={16} />
              <span>{v.error}. Single sign-on is off until it is saved again.</span>
            </div>
          )}
          <Row label="Public URL" hint="The address people open Visionary at. Redirect and SAML addresses are built from it.">
            <input
              className="input conf-input"
              value={publicURL}
              placeholder="https://visionary.example.com"
              onChange={(e) => {
                setPublicURL(e.target.value);
                setDirty(true);
              }}
            />
          </Row>
          {providers.length === 0 && <div className="dim sso-none">No providers yet. Add one, save it switched off, test it, then switch it on.</div>}
        </div>
        {providers.map((p, i) => (
          <ProviderForm
            key={p.id}
            p={p}
            saved={saved(p.id)}
            dirty={dirty}
            secret={secrets[p.id]}
            onSecret={(s) => {
              setSecrets({ ...secrets, [p.id]: s });
              setDirty(true);
            }}
            onChange={(patch) => change(i, patch)}
            onRemove={() => {
              setProviders(providers.filter((_, j) => j !== i));
              setDirty(true);
            }}
          />
        ))}
        {providers.some((p) => p.kind === "saml") && (
          <div className="settings-body pset sso-provider">
            <Row label="Visionary's SAML certificate" hint="Made for you the first time a SAML provider is saved. Some identity providers ask for it; the metadata above carries it too.">
              {v.sp_cert ? <CopyButton text={v.sp_cert} label="Copy certificate" /> : <span className="dim">Created when you save</span>}
            </Row>
            <Row label="Use your own key" hint="Optional: a PEM RSA private key followed by its certificate. Replaces the one Visionary made.">
              <textarea
                className="textarea mono conf-long"
                rows={3}
                value={keyPair}
                placeholder="-----BEGIN PRIVATE KEY-----"
                onChange={(e) => {
                  setKeyPair(e.target.value);
                  setDirty(true);
                }}
              />
            </Row>
          </div>
        )}
      </Card>
      <Card
        title="Provisioning (SCIM)"
        description="Your identity provider can add people before they first sign in, keep their names up to date, and switch them off when they leave: they are signed out at once."
      >
        <div className="settings-body pset">
          <Row label="SCIM base URL" hint="Give the identity provider this URL and a key of scope SCIM from Settings → API keys.">
            {v.scim_url ? (
              <div className="conf-control">
                <code className="mono sso-url">{v.scim_url}</code>
                <CopyButton text={v.scim_url} />
              </div>
            ) : (
              <span className="dim">Save a public URL first</span>
            )}
          </Row>
          <Row label="Groups and roles" hint="SCIM groups to Visionary roles, comma separated. When set, they decide the role of the people SCIM manages, whatever the provider sends at sign-in. Leave this and the default empty to take roles from the sign-in instead.">
            <input
              className="input conf-input"
              value={scim.role_map}
              placeholder="Visionary Admins=admin,Engineering=editor"
              onChange={(e) => {
                setScim({ ...scim, role_map: e.target.value });
                setDirty(true);
              }}
            />
          </Row>
          <Row label="Everyone else" hint="The role of people in none of those groups.">
            <Select
              label="Role for everyone else"
              value={scim.default_role}
              options={DEFAULT_ROLES}
              onChange={(r) => {
                setScim({ ...scim, default_role: r });
                setDirty(true);
              }}
            />
          </Row>
        </div>
      </Card>
      <SsoPeopleCard />
    </>
  );
}

function ProviderForm({
  p,
  saved,
  dirty,
  secret,
  onSecret,
  onChange,
  onRemove,
}: {
  p: Provider;
  saved?: Provider;
  dirty: boolean;
  secret: string | null | undefined;
  onSecret: (s: string | null) => void;
  onChange: (patch: Partial<Provider>) => void;
  onRemove: () => void;
}) {
  const [preset, setPreset] = useState(() => presetOf(p.issuer ?? ""));
  const hint = PRESETS.find((x) => x.value === preset)?.hint;
  const testable = !!saved && !dirty;
  return (
    <div className="settings-body pset sso-provider">
      <div className="sso-provider-head">
        <div className="sso-provider-title">
          <strong>{p.name || "Unnamed provider"}</strong>
          <span className="pill">{p.kind === "saml" ? "SAML 2.0" : "OpenID Connect"}</span>
          {saved?.enabled && <span className="pill">On</span>}
        </div>
        <div className="conf-control">
          <a
            className={`btn ghost sm ${testable ? "" : "disabled"}`}
            href={testable ? `/api/sso/${p.id}/test` : undefined}
            target="_blank"
            rel="noreferrer"
            aria-disabled={!testable}
            title={testable ? "Sign in with this provider without signing anyone in, and see what it sends" : "Save first, then test"}
          >
            <ExternalLink size={14} /> Test sign-in
          </a>
          <Switch on={p.enabled} label={p.enabled ? `Turn ${p.name} off` : `Turn ${p.name} on`} onChange={(on) => onChange({ enabled: on })} />
          <button type="button" className="icon-btn" title="Remove" aria-label={`Remove ${p.name}`} onClick={onRemove}>
            <Trash2 size={14} />
          </button>
        </div>
      </div>
      <Row label="Name" hint="On the sign-in button: “Sign in with …”.">
        <input className="input conf-input" value={p.name} onChange={(e) => onChange({ name: e.target.value })} />
      </Row>
      <Row label="Protocol">
        <Select label="Protocol" value={p.kind} options={KINDS} onChange={(k) => onChange({ kind: k as Kind, groups_claim: k === "saml" ? "groups" : p.groups_claim })} />
      </Row>
      {p.kind === "oidc" ? (
        <>
          <Row label="Provider" hint={hint}>
            <Select
              label="Provider"
              value={preset}
              options={PRESETS.map(({ value, label }) => ({ value, label }))}
              onChange={(x) => {
                setPreset(x);
                const pr = PRESETS.find((y) => y.value === x)!;
                const named = p.name.startsWith("Provider ") && x !== "other";
                onChange({ issuer: pr.issuer, groups_claim: pr.groups, name: named ? pr.label : p.name });
              }}
            />
          </Row>
          <Row label="Issuer URL" hint="Discovery is read from /.well-known/openid-configuration under it.">
            <input className="input conf-input" value={p.issuer ?? ""} placeholder="https://accounts.google.com" onChange={(e) => onChange({ issuer: e.target.value })} />
          </Row>
          <Row label="Client ID">
            <input className="input conf-input" value={p.client_id ?? ""} onChange={(e) => onChange({ client_id: e.target.value })} />
          </Row>
          <Row label="Client secret" hint="Leave empty for a public client; PKCE is always used.">
            <input
              className="input conf-input"
              type="password"
              autoComplete="new-password"
              value={secret ?? ""}
              placeholder={secret === null ? "Removed on save" : p.client_secret_set ? "Set; type to replace" : "Not set"}
              onChange={(e) => onSecret(e.target.value)}
            />
          </Row>
          <Row label="Scopes" hint="openid is always asked for.">
            <input className="input conf-input" value={(p.scopes ?? ["openid", "email", "profile"]).join(", ")} onChange={(e) => onChange({ scopes: list(e.target.value) })} />
          </Row>
          <CopyRow label="Redirect URL" hint="Register this at the provider." value={saved?.redirect_url} />
        </>
      ) : (
        <>
          <Row label="Metadata URL" hint="The identity provider's SAML metadata; read again once a day.">
            <input
              className="input conf-input"
              value={p.metadata_url ?? ""}
              placeholder="https://idp.example.com/app/…/sso/saml/metadata"
              onChange={(e) => onChange({ metadata_url: e.target.value })}
            />
          </Row>
          {!p.metadata_url && (
            <Row label="Or the metadata XML" hint="Paste it when there is no URL.">
              <textarea className="textarea mono conf-long" rows={3} value={p.metadata_xml ?? ""} placeholder="<EntityDescriptor …>" onChange={(e) => onChange({ metadata_xml: e.target.value })} />
            </Row>
          )}
          <Row label="Email and name attributes" hint="Optional; by default the NameID or the usual email and name attributes.">
            <div className="conf-control conf-inline">
              <input className="input conf-short" value={p.email_attr ?? ""} placeholder="email" onChange={(e) => onChange({ email_attr: e.target.value })} />
              <input className="input conf-short" value={p.name_attr ?? ""} placeholder="displayName" onChange={(e) => onChange({ name_attr: e.target.value })} />
            </div>
          </Row>
          <CopyRow label="Entity ID and metadata" hint="Upload this metadata to the identity provider, or give it the entity ID and ACS URL." value={saved?.entity_id} />
          <CopyRow label="ACS URL" hint="Where the identity provider posts its signed response." value={saved?.acs_url} />
        </>
      )}
      <Row label={p.kind === "saml" ? "Groups attribute" : "Groups claim"} hint="Where the person's groups or roles are. Leave empty when every user gets the default role.">
        <input className="input conf-input" value={p.groups_claim ?? ""} placeholder={p.kind === "saml" ? "groups" : "groups"} onChange={(e) => onChange({ groups_claim: e.target.value })} />
      </Row>
      <Row label="Groups and roles" hint="Comma separated; the highest role wins. * matches everyone.">
        <input className="input conf-input" value={p.role_map ?? ""} placeholder="visionary-admins=admin,engineering=editor" onChange={(e) => onChange({ role_map: e.target.value })} />
      </Row>
      <Row label="Everyone else" hint="The role of people in none of those groups; No access turns them away.">
        <Select label="Role for everyone else" value={p.default_role ?? ""} options={DEFAULT_ROLES} onChange={(r) => onChange({ default_role: r })} />
      </Row>
      <Row label="Allowed email domains" hint="Comma separated. Empty allows any address the provider vouches for.">
        <input
          className="input conf-input"
          value={(p.allowed_domains ?? []).join(", ")}
          placeholder="example.com"
          onChange={(e) => onChange({ allowed_domains: list(e.target.value) })}
        />
      </Row>
      <Row label="Create accounts" hint="Make an account the first time someone signs in. Off: only people added by SCIM can sign in.">
        <Switch on={p.create_users} label="Create accounts at first sign-in" onChange={(on) => onChange({ create_users: on })} />
      </Row>
    </div>
  );
}

function CopyRow({ label, hint, value }: { label: string; hint: string; value?: string }) {
  return (
    <Row label={label} hint={hint}>
      {value ? (
        <div className="conf-control">
          <code className="mono sso-url">{value}</code>
          <CopyButton text={value} />
        </div>
      ) : (
        <span className="dim">Shown once saved</span>
      )}
    </Row>
  );
}

const SOURCES: Record<string, string> = { oidc: "OpenID Connect", saml: "SAML", scim: "SCIM" };
const ROLE_PICKS = DEFAULT_ROLES.slice(1);

/** People who sign in through single sign-on; their accounts have no password. */
function SsoPeopleCard() {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`sso-people#${run}`, () => api<{ accounts: Account[] }>("/api/team/accounts"));
  const people = (q.data?.accounts ?? []).filter((a) => a.source);
  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn();
      toast("ok", ok);
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  const update = (a: Account, patch: Partial<Account>, ok: string) => act(() => api(`/api/team/accounts/${a.id}`, { method: "PUT", json: patch }), ok);
  return (
    <Card
      title="People from single sign-on"
      description="Made at their first sign-in or by SCIM. A role set here lasts until their groups next say otherwise; switching someone off signs them out within a minute."
    >
      {q.error && !q.data ? (
        <div className="settings-body">
          <ErrorNotice title="Could not load people" error={q.error} />
        </div>
      ) : !q.data ? (
        <div className="settings-empty">Loading…</div>
      ) : people.length === 0 ? (
        <div className="settings-empty">Nobody has signed in through single sign-on yet.</div>
      ) : (
        <div className="table-wrap">
          <table className="data acct-table">
            <thead>
              <tr>
                <th>Username</th>
                <th>Name</th>
                <th>From</th>
                <th>Role</th>
                <th>Last sign-in</th>
                <th>Status</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {people.map((a) => (
                <tr key={a.id} className={a.disabled ? "acct-disabled" : ""}>
                  <td className="mono">{a.username}</td>
                  <td>{a.name || <span className="dim">—</span>}</td>
                  <td>
                    <span className="pill">{SOURCES[a.source ?? ""] ?? a.source}</span>
                  </td>
                  <td>
                    {a.role ? (
                      <Select label={`Role of ${a.username}`} value={a.role} options={ROLE_PICKS} onChange={(r) => update(a, { role: r as Account["role"] }, `${a.username} is now ${r === "admin" ? "an admin" : `a ${r}`}.`)} />
                    ) : (
                      <span className="dim">No access yet</span>
                    )}
                  </td>
                  <td>{a.last_sign_in_at ? formatRelative(a.last_sign_in_at) : <span className="dim">Never</span>}</td>
                  <td>
                    <Switch
                      on={!a.disabled}
                      label={a.disabled ? `Enable ${a.username}` : `Disable ${a.username}`}
                      onChange={(on) => update(a, { disabled: !on }, on ? `${a.username} can sign in again.` : `${a.username} is disabled.`)}
                    />
                  </td>
                  <td className="acct-actions">
                    <button
                      type="button"
                      className="icon-btn"
                      title="Delete"
                      aria-label={`Delete ${a.username}`}
                      onClick={() => {
                        if (window.confirm(`Delete ${a.username}? If accounts are made at first sign-in, they get a new one next time; switch them off at the identity provider to keep them out.`)) {
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
    </Card>
  );
}
