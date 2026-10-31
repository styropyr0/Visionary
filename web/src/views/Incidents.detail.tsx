import { useEffect, useState, type ReactNode } from "react";
import {
  ArrowLeft,
  Bell,
  Check,
  CircleDot,
  Download,
  ExternalLink,
  FileText,
  Flag,
  Globe,
  Link2,
  Megaphone,
  MessageSquare,
  Pencil,
  Plus,
  Rocket,
  ShieldAlert,
  Sparkles,
  Trash2,
  UserRound,
  X,
} from "lucide-react";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatDateTime, formatFull, formatRelative, toLocalInput } from "../lib/time";
import { alertsApi } from "../lib/alerts";
import {
  LINK_KINDS,
  ROLES,
  SEVERITIES,
  STATUSES,
  formatSpan,
  incidentsApi,
  linkHref,
  roleLabel,
  type ActionItem,
  type Incident,
  type IncidentDetail as Detail,
  type IncidentStatus,
  type LinkKind,
  type Postmortem,
  type RoleName,
  type Severity,
  type TimelineEntry,
} from "../lib/incidents";
import { RefreshControl, TopBar } from "../components/Toolbar";
import { StatTile } from "../components/StatTile";
import { Modal } from "../components/Modal";
import { Combobox } from "../components/Combobox";
import { Select } from "../components/Select";
import { ErrorNotice, linkify } from "../components/ErrorNotice";
import { useToast } from "../components/Toast";
import { SevTag, StatusBadge, openIncident } from "./Incidents.parts";
import { splitList } from "./Incidents.declare";
import { useConfirm } from "../components/Confirm";

const errMessage = (e: unknown) => (e instanceof Error ? e.message : String(e));

type Tab = "timeline" | "postmortem";

export function IncidentDetail({ id }: { id: number }) {
  const { tick } = useApp();
  const toast = useToast();
  const ask = useConfirm();
  const [version, setVersion] = useState(0);
  const [tab, setTab] = useState<Tab>(() => (new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("tab") === "postmortem" ? "postmortem" : "timeline"));
  const [editing, setEditing] = useState(false);
  const reload = () => setVersion((v) => v + 1);
  const q = useQuery(`incident-${id}#${tick}.${version}`, (signal) => incidentsApi.get(id, signal));
  const d = q.data;
  const inc = d?.incident;

  // An open incident changes while it is watched, as alerts fire and others post
  useEffect(() => {
    if (!inc || inc.resolved_at) return;
    const t = window.setInterval(() => document.visibilityState === "visible" && setVersion((v) => v + 1), 20_000);
    return () => window.clearInterval(t);
  }, [inc]);

  const run = async (fn: () => Promise<unknown>, done?: string) => {
    try {
      await fn();
      if (done) toast("ok", done);
      reload();
    } catch (e) {
      toast("err", errMessage(e));
    }
  };

  const remove = async () => {
    if (!inc || !(await ask({ title: "Delete this incident?", message: `"${inc.title}" is deleted with its timeline and postmortem. This can't be undone.`, confirm: "Delete incident", danger: true }))) return;
    try {
      await incidentsApi.remove(inc.id);
      toast("ok", "Incident deleted.");
      openIncident(null);
    } catch (e) {
      toast("err", errMessage(e));
    }
  };

  const notFound = q.error && "status" in q.error && (q.error as { status: number }).status === 404;

  return (
    <>
      <TopBar
        title={inc ? `#${inc.id} ${inc.title}` : "Incident"}
        section="Incidents"
        actions={
          <>
            <RefreshControl busy={q.loading} />
            {inc && (
              <a className="btn" href={incidentsApi.markdownURL(inc.id)} download>
                <Download size={14} />
                Postmortem .md
              </a>
            )}
            {d?.can_edit && (
              <button type="button" className="btn" onClick={() => setEditing(true)}>
                <Pencil size={14} />
                Edit
              </button>
            )}
            {d?.can_admin && (
              <button type="button" className="icon-btn" title="Delete incident" aria-label="Delete incident" onClick={() => void remove()}>
                <Trash2 size={14} />
              </button>
            )}
          </>
        }
      />
      <div className="content">
        <a className="inc-back" href="#/incidents">
          <ArrowLeft size={14} />
          All incidents
        </a>
        {notFound && <div className="notice">This incident no longer exists.</div>}
        {q.error && !notFound && !d && <ErrorNotice title="Could not load the incident" error={q.error} />}
        {!d && !q.error && <div className="panel-state" style={{ height: 240 }} />}
        {d && inc && (
          <>
            <section className="panel inc-head">
              <div className="inc-head-top">
                <SevTag severity={inc.severity} />
                <h2>{inc.title}</h2>
                <StatusBadge status={inc.status} />
              </div>
              <div className="inc-head-meta dim">
                <span>
                  <UserRound size={13} aria-hidden /> {inc.commander ? `Commander: ${inc.commander}` : "No commander yet"}
                </span>
                {inc.services.length > 0 && <span>Services: {inc.services.join(", ")}</span>}
                <span>
                  Declared by {inc.created_by} {formatRelative(inc.declared_at)}
                </span>
                {inc.on_status_page && (
                  <span>
                    <Globe size={13} aria-hidden /> On the status page
                  </span>
                )}
              </div>
              {inc.impact && <p className="inc-impact">{inc.impact}</p>}
              <StatusSteps incident={inc} canEdit={d.can_edit} onSet={(s) => run(() => incidentsApi.setStatus(inc.id, s), `Now ${s}.`)} />
            </section>

            <div className="grid inc-tiles">
              <StatTile label="Duration" value={formatSpan(inc.ttr_s ?? (Date.now() - Date.parse(inc.started_at)) / 1000)} delta={null} deltaLabel="" upIs="neutral" footer={inc.resolved_at ? "start to resolution" : "so far"} />
              <StatTile label="Time to acknowledge" value={formatSpan(inc.tta_s)} delta={null} deltaLabel="" upIs="neutral" footer={inc.acknowledged_at ? formatDateTime(new Date(inc.acknowledged_at)) : "not yet"} />
              <StatTile label="Time to mitigate" value={formatSpan(inc.ttm_s)} delta={null} deltaLabel="" upIs="neutral" footer={inc.mitigated_at ? formatDateTime(new Date(inc.mitigated_at)) : "not yet"} />
              <StatTile label="Time to resolve" value={formatSpan(inc.ttr_s)} delta={null} deltaLabel="" upIs="neutral" footer={inc.resolved_at ? formatDateTime(new Date(inc.resolved_at)) : "not yet"} />
            </div>

            <nav className="tabs" role="tablist" aria-label="Incident">
              {(["timeline", "postmortem"] as Tab[]).map((t) => (
                <button type="button" key={t} role="tab" aria-selected={tab === t} className={tab === t ? "on" : ""} onClick={() => setTab(t)}>
                  {t === "timeline" ? "Timeline" : "Postmortem"}
                </button>
              ))}
            </nav>

            {tab === "timeline" ? (
              <div className="inc-layout">
                <div className="inc-main">
                  {d.can_edit && <Composer detail={d} onDone={reload} />}
                  <Timeline entries={d.timeline} />
                </div>
                <aside className="inc-side">
                  <RolesCard detail={d} onChange={reload} />
                  <LinksCard detail={d} onChange={reload} />
                </aside>
              </div>
            ) : (
              <PostmortemTab detail={d} onChange={reload} />
            )}
          </>
        )}
      </div>
      {editing && inc && <EditModal incident={inc} onClose={() => setEditing(false)} onSaved={reload} />}
    </>
  );
}

function StatusSteps({ incident: inc, canEdit, onSet }: { incident: Incident; canEdit: boolean; onSet: (s: IncidentStatus) => void }) {
  const at: Record<IncidentStatus, string | undefined> = { investigating: inc.declared_at, identified: inc.identified_at, monitoring: inc.mitigated_at, resolved: inc.resolved_at };
  const current = STATUSES.findIndex((s) => s.key === inc.status);
  return (
    <ol className="inc-steps" aria-label="Lifecycle">
      {STATUSES.map((s, i) => (
        <li key={s.key} className={`${i < current ? "done" : ""} ${i === current ? "on" : ""}`}>
          <button type="button" disabled={!canEdit || i === current} onClick={() => onSet(s.key)} title={canEdit && i !== current ? `Move to ${s.label}` : undefined}>
            <span className="inc-step-dot" aria-hidden />
            <span className="inc-step-label">{s.label}</span>
            <span className="inc-step-at dim">{at[s.key] && (i <= current || s.key !== "resolved") ? formatDateTime(new Date(at[s.key]!)) : "—"}</span>
          </button>
        </li>
      ))}
    </ol>
  );
}

const KIND_ICON: Record<string, typeof Flag> = {
  declared: Flag,
  status: CircleDot,
  severity: ShieldAlert,
  role: UserRound,
  note: MessageSquare,
  update: Megaphone,
  link: Link2,
  edit: Pencil,
  postmortem: FileText,
  alert: Bell,
  check: Globe,
  deploy: Rocket,
};

function Timeline({ entries }: { entries: TimelineEntry[] }) {
  const [auto, setAuto] = useState(true);
  const shown = [...entries].reverse().filter((e) => auto || !e.auto);
  const autoCount = entries.filter((e) => e.auto).length;
  return (
    <section className="panel inc-timeline">
      <header className="panel-head">
        <h3 className="panel-title">Timeline</h3>
        <div className="panel-actions pinned">
          {autoCount > 0 && (
            <label className="inc-toggle dim">
              <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
              Alerts, checks and deploys ({autoCount})
            </label>
          )}
        </div>
      </header>
      <ol className="inc-entries">
        {shown.map((e, i) => {
          const Icon = KIND_ICON[e.kind] ?? CircleDot;
          return (
            <li key={e.id ?? `a${i}`} className={`inc-entry ${e.kind} ${e.auto ? "auto" : ""}`}>
              <span className="inc-entry-icon" aria-hidden>
                <Icon size={14} />
              </span>
              <div className="inc-entry-body">
                <div className="inc-entry-head">
                  <time title={formatFull(new Date(e.at))}>{formatDateTime(new Date(e.at))}</time>
                  {e.actor && <span className="dim">{e.actor}</span>}
                  {e.auto && <span className="pill">auto</span>}
                  {e.kind === "update" && e.detail?.public && <span className="pill">status page</span>}
                </div>
                <div className="inc-entry-text">{linkify(e.body)}</div>
                {e.detail?.note && <div className="inc-entry-note">{e.detail.note}</div>}
                {e.kind === "update" && <Deliveries entry={e} />}
                {e.detail?.link && (
                  <a className="inc-entry-link" href={e.detail.link} target={e.detail.link.startsWith("#") ? undefined : "_blank"} rel="noreferrer">
                    {e.detail.link.startsWith("#") ? "Open" : e.detail.link}
                    <ExternalLink size={12} aria-hidden />
                  </a>
                )}
              </div>
            </li>
          );
        })}
        {shown.length === 0 && <li className="dim inc-entry-empty">Nothing yet.</li>}
      </ol>
    </section>
  );
}

function Deliveries({ entry }: { entry: TimelineEntry }) {
  const list = entry.detail?.delivery ?? [];
  if (list.length === 0 && !(entry.detail?.channels?.length ?? 0)) return null;
  return (
    <div className="inc-deliveries">
      {list.map((d, i) => (
        <span key={i} className={`inc-delivery ${d.status}`} title={d.error || undefined}>
          {d.channel_name}: {d.status === "sent" ? "sent" : d.status === "failed" ? "failed" : "sending"}
        </span>
      ))}
    </div>
  );
}

function Composer({ detail, onDone }: { detail: Detail; onDone: () => void }) {
  const toast = useToast();
  const inc = detail.incident;
  const [mode, setMode] = useState<"note" | "update">("note");
  const [body, setBody] = useState("");
  const [link, setLink] = useState("");
  const [status, setStatus] = useState<IncidentStatus | "">("");
  const [channels, setChannels] = useState<number[]>([]);
  const [statusPage, setStatusPage] = useState(inc.on_status_page);
  const [busy, setBusy] = useState(false);
  const chans = useQuery(mode === "update" ? "alerts-channels" : null, (signal) => alertsApi.channels(signal));

  const send = async () => {
    setBusy(true);
    try {
      if (mode === "note") {
        await incidentsApi.note(inc.id, body.trim(), link.trim());
        toast("ok", "Note added.");
      } else {
        const r = await incidentsApi.update(inc.id, { body: body.trim(), status, channels, status_page: statusPage });
        toast("ok", r.channels.length ? `Update sent to ${r.channels.join(", ")}${statusPage ? " and the status page" : ""}.` : "Update published on the status page.");
      }
      setBody("");
      setLink("");
      setStatus("");
      onDone();
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const canSend = body.trim() !== "" && (mode === "note" || channels.length > 0 || statusPage);
  return (
    <section className="panel inc-composer">
      <div className="segmented" role="radiogroup" aria-label="Kind">
        <button type="button" role="radio" aria-checked={mode === "note"} className={mode === "note" ? "on" : ""} onClick={() => setMode("note")}>
          <MessageSquare size={13} />
          Note
        </button>
        <button type="button" role="radio" aria-checked={mode === "update"} className={mode === "update" ? "on" : ""} onClick={() => setMode("update")}>
          <Megaphone size={13} />
          Status update
        </button>
      </div>
      <textarea
        className="textarea"
        rows={3}
        maxLength={mode === "note" ? 4000 : 2000}
        value={body}
        placeholder={mode === "note" ? "What you found or did. Notes stay in the team." : "What people outside the team should know: what is affected and what happens next."}
        onChange={(e) => setBody(e.target.value)}
        aria-label={mode === "note" ? "Note" : "Update"}
      />
      {mode === "note" ? (
        <input className="input" value={link} placeholder="Link (optional): a chart, request or trace, #/… or https://…" onChange={(e) => setLink(e.target.value)} aria-label="Link" />
      ) : (
        <div className="inc-update-opts">
          <Select
            label="Status"
            align="left"
            value={status}
            onChange={(v) => setStatus(v as IncidentStatus | "")}
            options={[{ value: "", label: `Keep ${inc.status}` }, ...STATUSES.filter((s) => s.key !== inc.status).map((s) => ({ value: s.key, label: `Move to ${s.label}` }))]}
          />
          <div className="inc-checks inline">
            {(chans.data?.channels ?? []).map((c) => (
              <label key={c.id}>
                <input type="checkbox" checked={channels.includes(c.id)} onChange={(e) => setChannels((l) => (e.target.checked ? [...l, c.id] : l.filter((x) => x !== c.id)))} />
                {c.name}
              </label>
            ))}
            <label title={detail.status_page ? undefined : "The status page is off; turn it on in Settings → Status page."}>
              <input type="checkbox" checked={statusPage} onChange={(e) => setStatusPage(e.target.checked)} />
              Status page{detail.status_page ? "" : " (off)"}
            </label>
          </div>
          {chans.data && chans.data.channels.length === 0 && <small className="dim">No alert channels yet; add one in Alerts → Channels.</small>}
        </div>
      )}
      <div>
        <button type="button" className="btn primary sm" disabled={busy || !canSend} onClick={() => void send()}>
          {mode === "note" ? <Plus size={13} /> : <Megaphone size={13} />}
          {busy ? "Sending…" : mode === "note" ? "Add note" : "Post update"}
        </button>
      </div>
    </section>
  );
}

function SideCard({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="panel inc-card">
      <header className="panel-head">
        <h3 className="panel-title">{title}</h3>
        <div className="panel-actions pinned">{action}</div>
      </header>
      <div className="inc-card-body">{children}</div>
    </section>
  );
}

function RolesCard({ detail, onChange }: { detail: Detail; onChange: () => void }) {
  const toast = useToast();
  const inc = detail.incident;
  const [adding, setAdding] = useState(false);
  const [role, setRole] = useState<RoleName>(inc.commander ? "responder" : "commander");
  const [user, setUser] = useState("");
  const people = useQuery(detail.can_edit ? "incident-people" : null, (signal) => incidentsApi.people(signal));
  const me = people.data?.me ?? "";

  const assign = async (r: RoleName, u: string) => {
    try {
      await incidentsApi.assign(inc.id, r, u);
      toast("ok", `${u || "You"}: ${roleLabel(r).toLowerCase()}.`);
      setAdding(false);
      setUser("");
      onChange();
    } catch (e) {
      toast("err", errMessage(e));
    }
  };
  const unassign = async (rid: number) => {
    try {
      await incidentsApi.unassign(inc.id, rid);
      onChange();
    } catch (e) {
      toast("err", errMessage(e));
    }
  };

  const sorted = [...inc.roles].sort((a, b) => ROLES.findIndex((r) => r.key === a.role) - ROLES.findIndex((r) => r.key === b.role));
  return (
    <SideCard
      title="Responders"
      action={
        detail.can_edit && (
          <button type="button" className="btn ghost sm" onClick={() => setAdding(!adding)} aria-expanded={adding}>
            <Plus size={13} />
            Assign
          </button>
        )
      }
    >
      {detail.can_edit && me && inc.commander !== me && !inc.resolved_at && (
        <button type="button" className="btn sm inc-take" onClick={() => void assign("commander", me)}>
          <Flag size={13} />
          Take command
        </button>
      )}
      {sorted.length === 0 && <p className="dim">Nobody is assigned yet.</p>}
      <ul className="inc-roles">
        {sorted.map((r) => (
          <li key={r.id}>
            <UserRound size={14} aria-hidden />
            <div>
              <div>{r.user}</div>
              <div className="dim">
                {roleLabel(r.role)} · {formatRelative(r.assigned_at)}
              </div>
            </div>
            {detail.can_edit && (
              <button type="button" className="icon-btn sm" aria-label={`Remove ${r.user} as ${roleLabel(r.role)}`} title="Remove" onClick={() => void unassign(r.id)}>
                <X size={13} />
              </button>
            )}
          </li>
        ))}
      </ul>
      {adding && (
        <div className="inc-addform">
          <Select label="Role" align="left" value={role} onChange={(v) => setRole(v as RoleName)} options={ROLES.map((r) => ({ value: r.key, label: r.label }))} />
          <Combobox label="Person" value={user} onChange={setUser} options={people.data?.people ?? []} placeholder="Name" />
          <button type="button" className="btn primary sm" disabled={!user.trim()} onClick={() => void assign(role, user.trim())}>
            Assign
          </button>
        </div>
      )}
    </SideCard>
  );
}

function LinksCard({ detail, onChange }: { detail: Detail; onChange: () => void }) {
  const toast = useToast();
  const inc = detail.incident;
  const [adding, setAdding] = useState(false);
  const [kind, setKind] = useState<LinkKind>("url");
  const [ref, setRef] = useState("");
  const [label, setLabel] = useState("");
  const add = async () => {
    try {
      await incidentsApi.link(inc.id, kind, ref.trim(), label.trim());
      setAdding(false);
      setRef("");
      setLabel("");
      onChange();
    } catch (e) {
      toast("err", errMessage(e));
    }
  };
  const remove = async (lid: number) => {
    try {
      await incidentsApi.unlink(inc.id, lid);
      onChange();
    } catch (e) {
      toast("err", errMessage(e));
    }
  };
  return (
    <SideCard
      title="Linked"
      action={
        detail.can_edit && (
          <button type="button" className="btn ghost sm" onClick={() => setAdding(!adding)} aria-expanded={adding}>
            <Plus size={13} />
            Link
          </button>
        )
      }
    >
      {detail.links.length === 0 && <p className="dim">Link the alerts, checks, errors, requests, traces and charts that matter. Linked alerts and checks add their history to the timeline.</p>}
      <ul className="inc-links">
        {detail.links.map((l) => {
          const href = linkHref(l);
          return (
            <li key={l.id}>
              <Link2 size={14} aria-hidden />
              <div>
                <a href={href} target={href.startsWith("#") ? undefined : "_blank"} rel="noreferrer">
                  {l.label}
                </a>
                <div className="dim">{LINK_KINDS.find((k) => k.key === l.kind)?.label ?? l.kind}</div>
              </div>
              {detail.can_edit && (
                <button type="button" className="icon-btn sm" aria-label={`Unlink ${l.label}`} title="Unlink" onClick={() => void remove(l.id)}>
                  <X size={13} />
                </button>
              )}
            </li>
          );
        })}
      </ul>
      {adding && (
        <div className="inc-addform">
          <Select label="Kind" align="left" value={kind} onChange={(v) => setKind(v as LinkKind)} options={LINK_KINDS.map((k) => ({ value: k.key, label: k.label }))} />
          <input className="input" value={ref} placeholder={LINK_KINDS.find((k) => k.key === kind)?.placeholder} onChange={(e) => setRef(e.target.value)} aria-label="What to link" />
          <input className="input" value={label} placeholder="Label (optional)" onChange={(e) => setLabel(e.target.value)} aria-label="Label" />
          <button type="button" className="btn primary sm" disabled={!ref.trim()} onClick={() => void add()}>
            Link
          </button>
        </div>
      )}
    </SideCard>
  );
}

const PM_FIELDS: { key: keyof Pick<Postmortem, "summary" | "impact" | "root_cause" | "timeline" | "lessons">; label: string; rows: number; hint: string }[] = [
  { key: "summary", label: "Summary", rows: 3, hint: "What happened, for how long, and how it was fixed." },
  { key: "impact", label: "Impact", rows: 4, hint: "Who and what was affected, with numbers." },
  { key: "root_cause", label: "Root cause", rows: 4, hint: "Why it happened. Blame systems, not people." },
  { key: "timeline", label: "Timeline", rows: 8, hint: "The key moments, one per line." },
  { key: "lessons", label: "Lessons learned", rows: 4, hint: "What went well, what didn't, where we got lucky." },
];

const EMPTY_PM: Postmortem = { summary: "", impact: "", root_cause: "", timeline: "", lessons: "" };

function PostmortemTab({ detail, onChange }: { detail: Detail; onChange: () => void }) {
  const toast = useToast();
  const ask = useConfirm();
  const inc = detail.incident;
  const [pm, setPm] = useState<Postmortem>(detail.postmortem ?? EMPTY_PM);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState("");
  const [note, setNote] = useState("");
  const [actions, setActions] = useState<ActionItem[]>(detail.actions);
  const [newAction, setNewAction] = useState("");
  const [newOwner, setNewOwner] = useState("");
  const people = useQuery(detail.can_edit ? "incident-people" : null, (signal) => incidentsApi.people(signal));
  const edit = detail.can_edit;

  useEffect(() => {
    if (!dirty) setPm(detail.postmortem ?? EMPTY_PM);
    setActions(detail.actions);
  }, [detail, dirty]);

  const draft = async () => {
    if (detail.postmortem && !(await ask({ title: "Replace the postmortem?", message: "A new draft replaces what's written now. Action items are kept.", confirm: "Draft again" }))) return;
    setBusy("draft");
    try {
      const r = await incidentsApi.draft(inc.id);
      setPm(r.postmortem);
      setActions(r.actions);
      setDirty(false);
      setNote(r.note);
      toast("ok", r.postmortem.drafted === "ai" ? "Draft saved. The AI analyst wrote it, so check it before sharing" : "Draft saved from the timeline");
      onChange();
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy("");
    }
  };
  const save = async () => {
    setBusy("save");
    try {
      setPm(await incidentsApi.savePostmortem(inc.id, pm));
      setDirty(false);
      toast("ok", "Postmortem saved.");
      onChange();
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy("");
    }
  };
  const act = async (fn: () => Promise<{ actions: ActionItem[] }>) => {
    try {
      setActions((await fn()).actions);
    } catch (e) {
      toast("err", errMessage(e));
    }
  };

  return (
    <div className="inc-layout">
      <div className="inc-main">
        <section className="panel inc-pm">
          <header className="panel-head">
            <h3 className="panel-title">Postmortem</h3>
            <div className="panel-actions pinned">
              {edit && (
                <button type="button" className="btn sm" disabled={busy !== ""} onClick={() => void draft()}>
                  <Sparkles size={13} />
                  {busy === "draft" ? "Drafting…" : detail.postmortem ? "Draft again" : "Draft it"}
                </button>
              )}
              {edit && (
                <button type="button" className="btn primary sm" disabled={busy !== "" || !dirty} onClick={() => void save()} title={dirty ? undefined : "No unsaved changes"}>
                  {busy === "save" ? (
                    "Saving…"
                  ) : dirty || !detail.postmortem ? (
                    "Save"
                  ) : (
                    <>
                      <Check size={13} />
                      Saved
                    </>
                  )}
                </button>
              )}
            </div>
          </header>
          <div className="inc-pm-body">
            {note && (
              <p className="notice inc-pm-note">
                <Sparkles size={14} aria-hidden />
                <span>{note}</span>
              </p>
            )}
            {!detail.postmortem && !dirty && (
              <p className="dim">
                {edit
                  ? `Draft it from the timeline and the affected services' traffic${detail.llm ? "; the AI analyst writes a first version" : ""}, then edit it here.`
                  : "No postmortem has been written yet."}
              </p>
            )}
            {pm.drafted && (
              <p className="dim">
                {pm.drafted === "ai" ? "Drafted by the AI analyst" : "Drafted from the data"}
                {pm.updated_by ? ` for ${pm.updated_by}` : ""}
                {pm.updated_at ? ` ${formatRelative(pm.updated_at)}` : ""}; nobody has edited it since.
              </p>
            )}
            {!pm.drafted && pm.updated_by && (
              <p className="dim">
                Last edited by {pm.updated_by} {pm.updated_at ? formatRelative(pm.updated_at) : ""}.
              </p>
            )}
            {PM_FIELDS.map((f) => (
              <label key={f.key} className="field">
                <span>{f.label}</span>
                <textarea
                  className="textarea"
                  rows={f.rows}
                  value={pm[f.key]}
                  disabled={!edit}
                  placeholder={edit ? f.hint : "Not written yet."}
                  onChange={(e) => {
                    setPm({ ...pm, [f.key]: e.target.value });
                    setDirty(true);
                  }}
                />
              </label>
            ))}
          </div>
        </section>
      </div>
      <aside className="inc-side">
        <SideCard title="Action items">
          {actions.length === 0 && <p className="dim">Follow-ups that would prevent a repeat or shorten the next one, each with an owner.</p>}
          <ul className="inc-actions">
            {actions.map((a) => (
              <li key={a.id} className={a.done ? "done" : ""}>
                <input type="checkbox" checked={a.done} disabled={!edit} aria-label={`Done: ${a.body}`} onChange={(e) => void act(() => incidentsApi.editAction(inc.id, a.id, { done: e.target.checked }))} />
                <div>
                  <div>{a.body}</div>
                  <div className="dim">{a.owner ? `Owner: ${a.owner}` : "No owner"}</div>
                </div>
                {edit && (
                  <button type="button" className="icon-btn sm" aria-label={`Remove ${a.body}`} title="Remove" onClick={() => void act(() => incidentsApi.removeAction(inc.id, a.id))}>
                    <X size={13} />
                  </button>
                )}
              </li>
            ))}
          </ul>
          {edit && (
            <div className="inc-addform">
              <input className="input" value={newAction} maxLength={500} placeholder="New action item" onChange={(e) => setNewAction(e.target.value)} aria-label="New action item" />
              <Combobox label="Owner" value={newOwner} onChange={setNewOwner} options={people.data?.people ?? []} placeholder="Owner (optional)" />
              <button
                type="button"
                className="btn sm"
                disabled={!newAction.trim()}
                onClick={() =>
                  void act(async () => {
                    const r = await incidentsApi.addAction(inc.id, newAction.trim(), newOwner.trim());
                    setNewAction("");
                    setNewOwner("");
                    return r;
                  })
                }
              >
                <Plus size={13} />
                Add
              </button>
            </div>
          )}
        </SideCard>
        <SideCard title="Export">
          <p className="dim">The postmortem, its facts and the action items as one Markdown file, ready for a wiki or a pull request.</p>
          <a className="btn sm" href={incidentsApi.markdownURL(inc.id)} download>
            <Download size={13} />
            Download Markdown
          </a>
        </SideCard>
      </aside>
    </div>
  );
}

function EditModal({ incident: inc, onClose, onSaved }: { incident: Incident; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [title, setTitle] = useState(inc.title);
  const [severity, setSeverity] = useState<Severity>(inc.severity);
  const [services, setServices] = useState(inc.services.join(", "));
  const [impact, setImpact] = useState(inc.impact);
  const [started, setStarted] = useState(toLocalInput(new Date(inc.started_at)));
  const [statusPage, setStatusPage] = useState(inc.on_status_page);
  const [busy, setBusy] = useState(false);
  const save = async () => {
    setBusy(true);
    try {
      const at = new Date(started);
      await incidentsApi.edit(inc.id, {
        title: title.trim(),
        severity,
        services: splitList(services),
        impact: impact.trim(),
        started_at: Number.isNaN(at.getTime()) ? undefined : at.toISOString(),
        on_status_page: statusPage,
      });
      onSaved();
      onClose();
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Edit incident"
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={busy || !title.trim()} onClick={() => void save()}>
            {busy ? "Saving…" : "Save"}
          </button>
        </>
      }
    >
      <div className="inc-form">
        <label className="field">
          <span>Title</span>
          <input className="input" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
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
        </div>
        <div className="inc-form-row">
          <label className="field">
            <span>Affected services</span>
            <input className="input" value={services} onChange={(e) => setServices(e.target.value)} />
          </label>
          <label className="field">
            <span>Impact started</span>
            <input className="input" type="datetime-local" value={started} onChange={(e) => setStarted(e.target.value)} />
          </label>
        </div>
        <label className="field">
          <span>Impact</span>
          <textarea className="textarea" rows={3} maxLength={4000} value={impact} onChange={(e) => setImpact(e.target.value)} />
        </label>
        <fieldset className="inc-checks">
          <label>
            <input type="checkbox" checked={statusPage} onChange={(e) => setStatusPage(e.target.checked)} />
            Show on the public status page
          </label>
        </fieldset>
      </div>
    </Modal>
  );
}
