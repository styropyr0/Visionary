import { useEffect, useMemo, useState } from "react";
import { Ban, Download, EyeOff, LogOut, RefreshCw, ShieldCheck, Trash2, UserRound } from "lucide-react";
import type { Result } from "../lib/api";
import { formatCount } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { useSession } from "../lib/team";
import { formatFull, formatLogTime } from "../lib/time";
import { KINDS, STATUS_LABELS, kindLabel, securityApi, type Finding, type FindingStatus, type Suppression } from "../lib/security";
import { DataTable, type Column } from "../components/DataTable";
import { Drawer } from "../components/Modal";
import { Panel } from "../components/Panel";
import { Section } from "../components/Section";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { linkify } from "../components/ErrorNotice";

/** A table result built in the page rather than returned by a query. */
export function localResult(columns: string[], rows: unknown[][]): Result {
  return { columns, rows, truncated: false, rows_read: 0, duration_ms: 0, sql: "", bucket: 0, from: "", to: "" };
}

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** A finding's status as a small pill. */
export function StatusPill({ status }: { status: FindingStatus }) {
  if (status === "open") return <span className="dim">Open</span>;
  return <span className={`pill sec-status ${status}`}>{STATUS_LABELS[status]}</span>;
}

/** Status, note and who triaged it, editable for editors. */
export function TriageBox({ finding, onChange }: { finding: Finding; onChange: (f: Finding) => void }) {
  const { can } = useSession();
  const toast = useToast();
  const [note, setNote] = useState(finding.note);
  const [busy, setBusy] = useState(false);
  useEffect(() => setNote(finding.note), [finding.id, finding.note]);
  const save = async (change: { status?: FindingStatus; note?: string }) => {
    setBusy(true);
    try {
      onChange(await securityApi.updateFinding(finding.id, change));
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const edit = can("edit");
  return (
    <div className="sec-triage">
      <div className="segmented" role="radiogroup" aria-label="Status">
        {(Object.keys(STATUS_LABELS) as FindingStatus[]).map((s) => (
          <button type="button" key={s} role="radio" aria-checked={finding.status === s} className={finding.status === s ? "on" : ""} disabled={!edit || busy} onClick={() => save({ status: s })}>
            {STATUS_LABELS[s]}
          </button>
        ))}
      </div>
      <textarea
        className="textarea"
        rows={2}
        value={note}
        maxLength={2000}
        disabled={!edit}
        placeholder={edit ? "Add a note: what you checked, what you did" : "No note"}
        onChange={(e) => setNote(e.target.value)}
        aria-label="Note"
      />
      {edit && note !== finding.note && (
        <div>
          <button type="button" className="btn sm" disabled={busy} onClick={() => save({ note })}>
            Save note
          </button>
        </div>
      )}
      {finding.status_by && (
        <p className="dim sec-triage-by">
          Last changed by {finding.status_by}
          {finding.status_at ? ` on ${formatFull(new Date(finding.status_at))}` : ""}.
        </p>
      )}
    </div>
  );
}

const SUPPRESS_DAYS = [
  { value: "0", label: "Forever" },
  { value: "1", label: "For a day" },
  { value: "7", label: "For a week" },
  { value: "30", label: "For 30 days" },
  { value: "90", label: "For 90 days" },
];

/** Block IP, End sessions, Ignore IP and Don't flag again. */
export function FindingActions({ finding, onTimeline }: { finding: Finding; onTimeline: (account: string) => void }) {
  const { can } = useSession();
  const toast = useToast();
  const [busy, setBusy] = useState("");
  const [suppressing, setSuppressing] = useState(false);
  const [scope, setScope] = useState<"ip" | "actor">(finding.ip ? "ip" : "actor");
  const [days, setDays] = useState("0");
  const [note, setNote] = useState("");
  const account = finding.kind === "login_as_other" && finding.target ? finding.target : finding.actor;
  const run = async (action: "block_ip" | "end_sessions" | "ignore_ip" | "suppress", extra = {}, done = "") => {
    setBusy(action);
    try {
      await securityApi.action(finding.id, action, extra);
      toast("ok", done);
      setSuppressing(false);
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy("");
    }
  };
  const admin = can("admin");
  const accountish = account && !account.startsWith("role:") && finding.source !== "host";
  return (
    <>
      <div className="sec-actions">
        {accountish && (
          <button type="button" className="btn" onClick={() => onTimeline(account)}>
            <UserRound size={14} />
            Account timeline
          </button>
        )}
        {admin && finding.ip && (
          <button type="button" className="btn" disabled={busy !== ""} onClick={() => run("block_ip", {}, `Asked your webhook to block ${finding.ip}.`)}>
            <Ban size={14} />
            {busy === "block_ip" ? "Sending…" : "Block IP"}
          </button>
        )}
        {admin && accountish && finding.source === "api" && (
          <button type="button" className="btn" disabled={busy !== ""} onClick={() => run("end_sessions", { account }, `Asked your webhook to end ${account}'s sessions.`)}>
            <LogOut size={14} />
            {busy === "end_sessions" ? "Sending…" : "End sessions"}
          </button>
        )}
        {admin && finding.ip && (
          <button type="button" className="btn" disabled={busy !== ""} onClick={() => run("ignore_ip", {}, `${finding.ip} is ignored by every detector from now on.`)}>
            <ShieldCheck size={14} />
            Ignore this IP
          </button>
        )}
        {can("edit") && (finding.ip || finding.actor) && (
          <button type="button" className="btn" onClick={() => setSuppressing(!suppressing)} aria-expanded={suppressing}>
            <EyeOff size={14} />
            Don't flag this again
          </button>
        )}
      </div>
      {suppressing && (
        <div className="sec-suppress">
          <p className="dim">
            Marks this finding a false positive and stops {kindLabel(finding.kind).toLowerCase()} findings for{" "}
            {scope === "ip" ? <span className="mono">{finding.ip}</span> : <span className="mono">{finding.actor}</span>}.
          </p>
          <div className="sec-suppress-row">
            {finding.ip && finding.actor && (
              <div className="segmented" role="radiogroup" aria-label="Match">
                <button type="button" role="radio" aria-checked={scope === "ip"} className={scope === "ip" ? "on" : ""} onClick={() => setScope("ip")}>
                  This IP
                </button>
                <button type="button" role="radio" aria-checked={scope === "actor"} className={scope === "actor" ? "on" : ""} onClick={() => setScope("actor")}>
                  This account
                </button>
              </div>
            )}
            <Select label="How long" align="left" value={days} options={SUPPRESS_DAYS} onChange={setDays} />
          </div>
          <input className="input" value={note} maxLength={500} placeholder="Why (optional): our load test, a partner's crawler…" onChange={(e) => setNote(e.target.value)} aria-label="Why" />
          <div>
            <button type="button" className="btn primary sm" disabled={busy !== ""} onClick={() => run("suppress", { scope, days: Number(days), note }, "Won't be flagged again.")}>
              {busy === "suppress" ? "Saving…" : "Don't flag again"}
            </button>
          </div>
        </div>
      )}
    </>
  );
}

/** What Visionary knows about one address. */
export function IPCard({ ip }: { ip: string }) {
  const q = useQuery(`sec-ip-${ip}`, (signal) => securityApi.ip(ip, signal));
  if (q.error) return <div className="panel-state error">{linkify(q.error.message)}</div>;
  if (!q.data) return <div className="panel-state">Loading…</div>;
  const d = q.data;
  const t = d.traffic;
  return (
    <div className="sec-ipcard">
      <dl className="kv">
        <dt>Where</dt>
        <dd>{!d.public ? "A private or local address" : d.place || <span className="dim">Unknown; add a location database in Settings → Server → Threat intelligence</span>}</dd>
        {d.geo.org && (
          <>
            <dt>Network</dt>
            <dd>
              {d.geo.org}
              {d.geo.asn ? <span className="dim"> · AS{d.geo.asn}</span> : null}
            </dd>
          </>
        )}
        <dt>Blocklists</dt>
        <dd>{d.listed_in?.length ? <span className="flag bad">{d.listed_in.join(", ")}</span> : <span className="dim">Not listed{d.ignored ? " · ignored by the detectors" : ""}</span>}</dd>
        {t && (
          <>
            <dt>Last {t.days} days</dt>
            <dd>
              {formatCount(t.requests, false)} requests, {formatCount(t.failed_logins, false)} failed and {formatCount(t.logins, false)} successful sign-ins, {formatCount(t.accounts, false)} account(s),{" "}
              {formatCount(t.user_agents, false)} user agent(s)
              {t.requests > 0 && (
                <div className="dim">
                  {formatLogTime(t.first)} to {formatLogTime(t.last)}
                </div>
              )}
            </dd>
          </>
        )}
        {d.portal && (d.portal.failed_sign_ins > 0 || d.portal.sign_ins > 0) && (
          <>
            <dt>Visionary sign-ins</dt>
            <dd>
              {formatCount(d.portal.sign_ins, false)} signed in, {formatCount(d.portal.failed_sign_ins, false)} failed
            </dd>
          </>
        )}
        {d.findings && d.findings.length > 0 && (
          <>
            <dt>Findings (90 days)</dt>
            <dd>{d.findings.map((f) => `${kindLabel(f.kind)} × ${f.findings}`).join(", ")}</dd>
          </>
        )}
        {d.accounts && d.accounts.length > 0 && (
          <>
            <dt>Accounts</dt>
            <dd className="mono">{d.accounts.map((a) => `${a.account}${a.failed_logins ? ` (${a.failed_logins} failed)` : ""}`).join(", ")}</dd>
          </>
        )}
      </dl>
    </div>
  );
}

const TIMELINE_DAYS = [
  { value: "7", label: "Last 7 days" },
  { value: "30", label: "Last 30 days" },
  { value: "90", label: "Last 90 days" },
];

/** One account's sign-ins, findings and known places and devices. */
export function AccountDrawer({ account, onClose, onFinding }: { account: string; onClose: () => void; onFinding: (f: Finding) => void }) {
  const [days, setDays] = useState("30");
  const q = useQuery(`sec-account-${account}-${days}`, (signal) => securityApi.account(account, Number(days), signal));
  const d = q.data;
  const logins = useMemo(
    () => localResult(["at", "result", "ip", "place", "device", "route"], (d?.logins ?? []).map((l) => [l.at, l.failed ? "failed" : l.status < 400 ? "signed in" : String(l.status), l.ip, l.place, l.device, l.route])),
    [d],
  );
  const findings = useMemo(() => localResult(["id", "created_at", "kind", "summary"], (d?.findings ?? []).map((f) => [f.id, f.created_at, f.kind, f.summary])), [d]);
  const byId = useMemo(() => new Map((d?.findings ?? []).map((f) => [f.id, f])), [d]);
  const loginColumns: Column[] = [
    { key: "at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
    { key: "result", render: (v) => (v === "failed" ? <span className="flag bad">Failed</span> : v === "signed in" ? "Signed in" : String(v)), width: 90 },
    { key: "ip", label: "IP", render: (v) => <span className="mono">{String(v)}</span> },
    { key: "place", render: (v) => (v ? String(v) : <span className="dim">—</span>) },
    { key: "device", render: (v) => (v ? String(v) : <span className="dim">—</span>) },
  ];
  return (
    <Drawer onClose={onClose} title={<span className="mono">{account}</span>}>
      <div className="sec-actions">
        <Select label="Range" align="left" value={days} options={TIMELINE_DAYS} onChange={setDays} />
      </div>
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {!d && !q.error && <div className="panel-state">Loading…</div>}
      {d && (
        <>
          <p className="sec-summary">
            {formatCount(d.sign_ins, false)} sign-in(s) and {formatCount(d.failed_sign_ins, false)} failed attempt(s) from {formatCount(d.ips, false)} address(es) in the last {d.days} days
            {d.findings?.length ? `, and ${d.findings.length} finding(s)` : ""}.
          </p>
          {!d.geo && <p className="dim">Add a location database in Settings → Server → Threat intelligence to see where each sign-in came from.</p>}
          <div className="section-label">Sign-ins</div>
          {logins.rows.length === 0 ? (
            <p className="dim">No sign-ins for this account in the range. Sign-ins count when the app records the account tried (LoginAttempt) or the user on its login route.</p>
          ) : (
            <DataTable result={logins} columns={loginColumns} sortable={false} maxHeight={320} />
          )}
          <div className="section-label">Findings</div>
          {findings.rows.length === 0 ? (
            <p className="dim">No findings name this account.</p>
          ) : (
            <DataTable
              result={findings}
              sortable={false}
              maxHeight={260}
              rowKey={(r) => r[0]}
              onRowClick={(r) => onFinding(byId.get(Number(r[0]))!)}
              columns={[
                { key: "created_at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
                { key: "kind", render: (v) => kindLabel(String(v)), width: 170 },
                { key: "summary", label: "What happened" },
              ]}
            />
          )}
          {d.known && d.known.length > 0 && (
            <>
              <div className="section-label">Signed in from before</div>
              <dl className="kv">
                {d.known.map((k) => (
                  <KnownRow key={k.kind + k.value} kind={k.kind} value={k.value} first={k.first_seen} last={k.last_seen} hits={k.hits} />
                ))}
              </dl>
            </>
          )}
        </>
      )}
    </Drawer>
  );
}

function KnownRow({ kind, value, first, last, hits }: { kind: string; value: string; first: string; last: string; hits: number }) {
  return (
    <>
      <dt>{kind === "country" ? "Country" : "Device"}</dt>
      <dd>
        {value}
        <div className="dim">
          {formatCount(hits, false)} sign-in(s), {formatLogTime(first)} to {formatLogTime(last)}
        </div>
      </dd>
    </>
  );
}

/** The "don't flag again" rules, with a form for new ones. */
export function SuppressionsPanel() {
  const { tick } = useApp();
  const { can } = useSession();
  const toast = useToast();
  const [run, setRun] = useState(0);
  const q = useQuery(`sec-suppressions#${tick}-${run}`, (signal) => securityApi.suppressions(signal));
  const [form, setForm] = useState({ kind: "*", match: "", days: "0", note: "" });
  const [busy, setBusy] = useState(false);
  const list = q.data?.suppressions ?? [];
  const now = new Date().toISOString();
  const result = useMemo(
    () => localResult(["id", "kind", "match", "note", "by", "until"], list.map((s: Suppression) => [s.id, s.kind, s.ip || s.actor || "", s.note ?? "", s.created_by, s.expires_at ?? ""])),
    [list],
  );
  const add = async () => {
    const m = form.match.trim();
    if (!m) return;
    const isIP = /^[0-9a-f.:]+(\/\d+)?$/i.test(m);
    setBusy(true);
    try {
      await securityApi.addSuppression({ kind: form.kind, ...(isIP ? { ip: m } : { actor: m }), note: form.note, days: Number(form.days) });
      setForm({ ...form, match: "", note: "" });
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: number) => {
    try {
      await securityApi.deleteSuppression(id);
      setRun((n) => n + 1);
    } catch (e) {
      toast("err", errMessage(e));
    }
  };
  const edit = can("edit");
  return (
    <Panel
      className="span-6"
      title="Not flagged"
      description="Addresses and accounts the detectors skip, made with Don't flag this again or Ignore this IP on a finding, or here."
      result={result}
      loading={q.loading}
      error={q.error}
      bodyHeight={120}
      table={
        <>
          {edit && (
            <div className="filter-row sec-suppress-add">
              <Select
                label="Kind"
                align="left"
                maxWidth={200}
                value={form.kind}
                onChange={(v) => setForm({ ...form, kind: v })}
                options={[{ value: "*", label: "Every kind" }, ...Object.entries(KINDS).map(([k, v]) => ({ value: k, label: v.label }))]}
              />
              <input className="input mono" style={{ width: 170 }} placeholder="IP, CIDR or account" value={form.match} onChange={(e) => setForm({ ...form, match: e.target.value })} aria-label="IP, CIDR range or account" />
              <Select label="How long" align="left" value={form.days} options={SUPPRESS_DAYS} onChange={(v) => setForm({ ...form, days: v })} />
              <input className="input" style={{ width: 160 }} placeholder="Why (optional)" value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} aria-label="Why" />
              <button type="button" className="btn sm" disabled={busy || !form.match.trim()} onClick={add}>
                Add
              </button>
            </div>
          )}
          {result.rows.length === 0 ? (
            <div className="panel-state">Nothing is skipped.</div>
          ) : (
            <DataTable
              result={result}
              sortable={false}
              maxHeight={280}
              rowKey={(r) => r[0]}
              columns={[
                { key: "kind", render: (v) => (v === "*" ? "Every kind" : kindLabel(String(v))) },
                { key: "match", label: "IP or account", render: (v) => <span className="mono">{String(v)}</span> },
                { key: "note", render: (v) => (v ? String(v) : <span className="dim">—</span>) },
                { key: "by" },
                {
                  key: "until",
                  render: (v) => (!v ? <span className="dim">Forever</span> : String(v) < now ? <span className="dim">Expired</span> : <span className="mono dim">{formatLogTime(String(v))}</span>),
                },
                {
                  key: "id",
                  label: "",
                  align: "right",
                  render: (v) =>
                    edit ? (
                      <button type="button" className="icon-btn" aria-label="Remove" title="Remove" onClick={() => remove(Number(v))}>
                        <Trash2 size={14} />
                      </button>
                    ) : null,
                },
              ]}
            />
          )}
        </>
      }
    />
  );
}

/** Whether the location and network databases opened, and what each blocklist holds. */
export function IntelPanel() {
  const { can } = useSession();
  const [refresh, setRefresh] = useState(0);
  const q = useQuery(`sec-intel-${refresh}`, (signal) => securityApi.intel(refresh > 0, signal));
  const d = q.data;
  const db = (label: string, v: { path: string; open: boolean; error: string } | undefined) => (
    <>
      <dt>{label}</dt>
      <dd>
        {!v?.path ? <span className="dim">Not set</span> : v.open ? "In use" : <span className="flag bad">{v.error || "Couldn't open"}</span>}
        {v?.path && <div className="dim mono">{v.path}</div>}
      </dd>
    </>
  );
  return (
    <Panel
      className="span-6"
      title="Threat intelligence"
      result={d ? localResult([], []) : undefined}
      description="Set these in Settings → Server → Threat intelligence. Findings get the place and network of their address; a listed address raises a finding one level."
      loading={q.loading}
      error={q.error}
      bodyHeight={120}
      extraActions={
        can("admin") ? (
          <button type="button" className="btn ghost sm" onClick={() => setRefresh((n) => n + 1)}>
            <RefreshCw size={14} />
            Fetch blocklists now
          </button>
        ) : undefined
      }
      table={
        d && (
          <dl className="kv sec-intel">
            {db("Locations", d.city)}
            {db("Networks", d.asn)}
            <dt>Blocklists</dt>
            <dd>
              {!d.blocklists?.length ? (
                <span className="dim">None</span>
              ) : (
                d.blocklists.map((b) => (
                  <div key={b.url}>
                    {b.name}: {b.error ? <span className="flag bad">{b.error}</span> : `${formatCount(b.entries, false)} entries`}
                  </div>
                ))
              )}
              {d.fetched && <div className="dim">Fetched {formatLogTime(d.fetched)}</div>}
            </dd>
          </dl>
        )
      }
    />
  );
}

/** Export the audit log as signed lines, and check an export. */
export function AuditExport() {
  const { resolved } = useApp();
  const toast = useToast();
  const [result, setResult] = useState<Awaited<ReturnType<typeof securityApi.verifyAudit>> | null>(null);
  const [busy, setBusy] = useState(false);
  const verify = async (file: File) => {
    setBusy(true);
    setResult(null);
    try {
      setResult(await securityApi.verifyAudit(await file.text()));
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Section title="Audit export" text="Every line of an export hashes the one before it, and the last line signs the chain with this installation's key: a changed, removed or reordered line shows when you check it." />
      <div className="span-12 panel sec-export">
        <a className="btn" href={securityApi.auditExportUrl(resolved.from, resolved.to)} download>
          <Download size={14} />
          Export the range shown
        </a>
        <label className="btn">
          <ShieldCheck size={14} />
          {busy ? "Checking…" : "Check an export"}
          <input type="file" accept=".jsonl,.json,.txt" hidden onChange={(e) => e.target.files?.[0] && verify(e.target.files[0])} />
        </label>
        {result &&
          (result.valid ? (
            <span className="flag ok">
              Intact: {formatCount(result.entries ?? 0, false)} entries from {formatLogTime(result.from ?? "")} to {formatLogTime(result.to ?? "")}, exported by {result.exported_by || "—"}.
            </span>
          ) : (
            <span className="flag bad">
              Not intact{result.line ? ` at line ${result.line}` : ""}: {result.error}
            </span>
          ))}
      </div>
    </>
  );
}
