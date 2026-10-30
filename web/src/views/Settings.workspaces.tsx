import { useMemo, useState } from "react";
import { Info, Layers, Plus, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { useQuery } from "../lib/useQuery";
import {
  matchService,
  refreshSession,
  type ApiKey,
  type MemberKind,
  type Role,
  type Workspace,
  type WorkspaceItem,
  type WorkspaceItemKind,
  type WorkspaceList,
  type WorkspaceMember,
} from "../lib/team";
import { Modal } from "../components/Modal";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { ErrorNotice } from "../components/ErrorNotice";
import { Card } from "./Settings.profiling";
import type { Account } from "./Settings.config";

// ---------- Settings → Workspaces ----------

const ROLES: { value: Role; label: string }[] = [
  { value: "viewer", label: "Viewer" },
  { value: "editor", label: "Editor" },
  { value: "admin", label: "Admin" },
];

const KINDS: { value: MemberKind; label: string }[] = [
  { value: "user", label: "Person" },
  { value: "group", label: "Group" },
  { value: "key", label: "API key" },
];

const ITEM_KINDS: { kind: WorkspaceItemKind; label: string }[] = [
  { kind: "alert", label: "Alert rules" },
  { kind: "check", label: "Uptime checks" },
  { kind: "dashboard", label: "Dashboards" },
  { kind: "view", label: "Saved log views" },
];

function errText(e: unknown) {
  return e instanceof Error ? e.message : String(e);
}

export function WorkspacesTab({ admin }: { admin: boolean }) {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`workspaces#${run}`, (signal) => api<WorkspaceList>("/api/workspaces", { signal }));
  const [editing, setEditing] = useState<Workspace | "new" | null>(null);
  const [removing, setRemoving] = useState<Workspace | null>(null);
  const reload = () => {
    setRun((n) => n + 1);
    refreshSession();
  };

  const remove = async (ws: Workspace) => {
    try {
      await api(`/api/workspaces/${ws.id}`, { method: "DELETE" });
      toast("ok", `Deleted “${ws.name}”`);
      reload();
    } catch (e) {
      toast("err", errText(e));
    }
    setRemoving(null);
  };

  const setUnassigned = async (mode: string) => {
    try {
      await api("/api/workspaces/unassigned", { method: "PUT", body: JSON.stringify({ mode }) });
      toast("ok", "Saved");
      reload();
    } catch (e) {
      toast("err", errText(e));
    }
  };

  const d = q.data;
  if (!d) return q.error ? <ErrorNotice title="Could not load workspaces" error={q.error} /> : <div className="settings-empty">Loading…</div>;
  const list = d.workspaces;
  return (
    <div className="settings-stack">
      <Card
        title="Workspaces"
        description="A workspace is a set of services and the people who work on them. Its members see only those services, on every page, in their own queries and through the Prometheus API. Admins always see everything. With no workspaces, everyone sees everything."
        actions={
          admin ? (
            <button type="button" className="btn primary sm" onClick={() => setEditing("new")}>
              <Plus size={14} /> New workspace
            </button>
          ) : undefined
        }
      >
        {list.length === 0 ? (
          <div className="settings-empty">
            <Layers size={18} />
            <span>No workspaces yet, so everyone sees every service.</span>
          </div>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Services</th>
                  <th>Members</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {list.map((ws) => {
                  const canEdit = admin || d.roles[ws.id] === "admin";
                  return (
                    <tr key={ws.id}>
                      <td>{ws.name}</td>
                      <td>
                        <div className="ws-pills">
                          {ws.services.length === 0 ? <span className="dim">None yet</span> : ws.services.map((p) => <span key={p} className="pill mono">{p}</span>)}
                        </div>
                      </td>
                      <td>{ws.members ? ws.members.length : <span className="dim">—</span>}</td>
                      <td className="num">
                        <div className="ws-actions">
                          {canEdit && (
                            <button type="button" className="btn sm" onClick={() => setEditing(ws)}>
                              Edit
                            </button>
                          )}
                          {admin && (
                            <button type="button" className="btn sm" aria-label={`Delete ${ws.name}`} onClick={() => setRemoving(ws)}>
                              <Trash2 size={14} />
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {admin && list.length > 0 && (
        <Card title="People outside every workspace" description="Who belongs to no workspace: they can see everything, as before workspaces, or nothing until an admin adds them to one.">
          <div className="settings-body">
            <Select
              value={d.unassigned}
              options={[
                { value: "all", label: "See every service" },
                { value: "none", label: "See nothing" },
              ]}
              onChange={setUnassigned}
              label="People outside every workspace"
              align="left"
            />
          </div>
        </Card>
      )}

      {admin && list.length > 0 && <ItemsCard workspaces={list} />}

      <Card title="How it works">
        <div className="settings-roles">
          <div className="settings-role ws-note">
            <Info size={14} />
            <span>
              Services are exact names or patterns with <code>*</code> at the start or end, such as <code>payments-*</code>. Data without a service name (older SDKs, hosts
              without one) is seen only by people who see everything.
            </span>
          </div>
          <div className="settings-role ws-note">
            <Info size={14} />
            <span>
              Members are people, identity provider or SCIM groups, and API keys, each with a role there. Inside a workspace that role applies instead of their own, but never admin of the
              whole server. Someone in several workspaces picks one under the logo, or all of them.
            </span>
          </div>
          <div className="settings-role ws-note">
            <Info size={14} />
            <span>
              Alert rules and uptime checks that belong to a workspace are seen by its members only, and a workspace's alert rules watch only its services. Dashboards and saved views without
              a workspace are seen by everyone, with each person's own data in them. What someone creates inside a workspace belongs to it.
            </span>
          </div>
        </div>
      </Card>

      {editing && <WorkspaceEditor ws={editing === "new" ? null : editing} admin={admin} onClose={() => setEditing(null)} onSaved={reload} />}
      {removing && (
        <Modal
          title="Delete workspace"
          narrow
          onClose={() => setRemoving(null)}
          footer={
            <>
              <button type="button" className="btn ghost" onClick={() => setRemoving(null)}>
                Cancel
              </button>
              <button type="button" className="btn danger" onClick={() => remove(removing)}>
                Delete
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>
            Delete “{removing.name}”? Its members go back to what people outside every workspace see, and its alert rules, checks, dashboards and views stay, without a workspace.
          </p>
        </Modal>
      )}
    </div>
  );
}

function WorkspaceEditor({ ws, admin, onClose, onSaved }: { ws: Workspace | null; admin: boolean; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [name, setName] = useState(ws?.name ?? "");
  const [services, setServices] = useState((ws?.services ?? []).join("\n"));
  const [members, setMembers] = useState<WorkspaceMember[]>(ws?.members ?? []);
  const [busy, setBusy] = useState(false);
  const known = useQuery(admin ? "workspace-services" : null, (signal) => api<{ services: string[] }>("/api/workspaces/services", { signal }));
  const accounts = useQuery(admin ? "workspace-accounts" : null, (signal) => api<{ accounts: Account[] }>("/api/team/accounts", { signal }));
  const keys = useQuery(admin ? "workspace-keys" : null, (signal) => api<{ keys: ApiKey[] }>("/api/team/keys", { signal }));

  const patterns = services
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
  const seen = known.data?.services ?? [];
  const matched = useMemo(() => seen.filter((s) => patterns.some((p) => matchService(p, s))), [seen, patterns]);
  const idle = patterns.filter((p) => !seen.some((s) => matchService(p, s)));

  const save = async () => {
    setBusy(true);
    try {
      const body = JSON.stringify({ name, services: patterns, members });
      if (ws) await api(`/api/workspaces/${ws.id}`, { method: "PUT", body });
      else await api("/api/workspaces", { method: "POST", body });
      toast("ok", ws ? "Workspace saved" : "Workspace created");
      onSaved();
      onClose();
    } catch (e) {
      toast("err", errText(e));
    } finally {
      setBusy(false);
    }
  };

  const up = (i: number, p: Partial<WorkspaceMember>) => setMembers(members.map((m, j) => (j === i ? { ...m, ...p } : m)));
  const people = (accounts.data?.accounts ?? []).map((a) => ({ value: a.id, label: `${a.name || a.username} (@${a.username})` }));
  const readKeys = (keys.data?.keys ?? []).filter((k) => k.scope === "read" && !k.revoked_at).map((k) => ({ value: String(k.id), label: `${k.name} (${k.prefix}…)` }));

  return (
    <Modal
      title={ws ? `Edit ${ws.name}` : "New workspace"}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={busy || !name.trim()} onClick={save}>
            {busy ? "Saving…" : ws ? "Save" : "Create"}
          </button>
        </>
      }
    >
      <div className="ws-form">
        <label className="ws-field">
          <span className="pset-label">Name</span>
          <input className="input" value={name} disabled={!admin} maxLength={80} placeholder="Payments" onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="ws-field">
          <span className="pset-label">Services</span>
          <span className="dim pset-hint">One per line: an exact name, or a pattern with * at the start or end.</span>
          <textarea className="textarea mono" rows={4} value={services} disabled={!admin} placeholder={"payments-*\ncheckout"} onChange={(e) => setServices(e.target.value)} />
        </label>
        {admin && (
          <div className="ws-preview">
            <div className="pset-label">
              Matches {matched.length} of {seen.length} services seen in the last 30 days
            </div>
            <div className="ws-pills">
              {matched.length === 0 ? <span className="dim">Nothing yet.</span> : matched.map((s) => <span key={s} className="pill mono">{s}</span>)}
            </div>
            {idle.length > 0 && <div className="dim pset-hint">No data yet for {idle.join(", ")}; it applies as soon as a service by that name reports.</div>}
          </div>
        )}
        <div className="ws-field">
          <span className="pset-label">Members</span>
          <span className="dim pset-hint">Groups are the names your identity provider sends at sign-in, or SCIM group names, or the roles of a delegated sign-in.</span>
          {members.length === 0 && <div className="dim">No members yet.</div>}
          {members.map((m, i) => (
            <div className="ws-member" key={i}>
              <Select value={m.kind} options={KINDS} onChange={(v) => up(i, { kind: v as MemberKind, id: "" })} label="Member type" align="left" />
              {m.kind === "user" && people.length > 0 ? (
                <Select value={m.id} options={[{ value: "", label: "Pick a person" }, ...people]} onChange={(v) => up(i, { id: v })} label="Person" align="left" maxWidth={260} />
              ) : m.kind === "key" && readKeys.length > 0 ? (
                <Select value={m.id} options={[{ value: "", label: "Pick a read key" }, ...readKeys]} onChange={(v) => up(i, { id: v })} label="API key" align="left" maxWidth={260} />
              ) : (
                <input className="input ws-member-id" value={m.id} placeholder={m.kind === "group" ? "engineering-payments" : m.kind === "key" ? "Key id" : "User id"} onChange={(e) => up(i, { id: e.target.value })} />
              )}
              <Select value={m.role} options={ROLES} onChange={(v) => up(i, { role: v as Role })} label="Role" align="left" />
              <button type="button" className="btn sm" aria-label="Remove member" onClick={() => setMembers(members.filter((_, j) => j !== i))}>
                <Trash2 size={14} />
              </button>
            </div>
          ))}
          <div>
            <button type="button" className="btn sm" onClick={() => setMembers([...members, { kind: "user", id: "", role: "viewer" }])}>
              <Plus size={14} /> Add member
            </button>
          </div>
        </div>
      </div>
    </Modal>
  );
}

function ItemsCard({ workspaces }: { workspaces: Workspace[] }) {
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`workspace-items#${run}`, (signal) => api<Record<WorkspaceItemKind, WorkspaceItem[]>>("/api/workspaces/items", { signal }));
  const options = [{ value: "", label: "No workspace" }, ...workspaces.map((w) => ({ value: String(w.id), label: w.name }))];
  const assign = async (kind: WorkspaceItemKind, item: WorkspaceItem, v: string) => {
    try {
      await api("/api/workspaces/items", { method: "PUT", body: JSON.stringify({ kind, id: item.id, workspace_id: v ? Number(v) : null }) });
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", errText(e));
    }
  };
  return (
    <Card title="What belongs to a workspace" description="Alert rules and uptime checks without a workspace are seen only by people who see everything; dashboards and saved views without one are shared.">
      {!q.data ? (
        q.error ? (
          <ErrorNotice title="Could not load" error={q.error} />
        ) : (
          <div className="settings-empty">Loading…</div>
        )
      ) : (
        <div className="settings-body ws-items">
          {ITEM_KINDS.map(({ kind, label }) => (
            <div key={kind} className="ws-item-group">
              <div className="pset-label">{label}</div>
              {(q.data?.[kind] ?? []).length === 0 ? (
                <div className="dim">None yet.</div>
              ) : (
                (q.data?.[kind] ?? []).map((item) => (
                  <div key={item.id} className="ws-item">
                    <span className="ws-item-name">{item.name}</span>
                    <Select value={item.workspace_id ? String(item.workspace_id) : ""} options={options} onChange={(v) => assign(kind, item, v)} label={`Workspace of ${item.name}`} maxWidth={220} />
                  </div>
                ))
              )}
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

