import { useState } from "react";
import { Ban, CheckCircle2, Globe, LogOut, Server, ShieldAlert, ShieldCheck, TriangleAlert, UserRound } from "lucide-react";
import { formatCount } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { useSession } from "../lib/team";
import { formatFull, formatLogTime } from "../lib/time";
import { kindLabel, securityApi, type Finding, type Incident, type IncidentList, type Outcome, type Posture, type PostureItem } from "../lib/security";
import { Drawer } from "../components/Modal";
import { InfoTip } from "../components/InfoTip";
import { useToast } from "../components/Toast";
import { linkify } from "../components/ErrorNotice";
import { BlockForm, BlockState, SevBadge } from "./Security.blocks";

const OUTCOME: Record<Outcome, { label: string; tone: string }> = {
  succeeded: { label: "Got through", tone: "critical" },
  attempted: { label: "Tried", tone: "warning" },
  blocked: { label: "Blocked", tone: "ok" },
};

export function OutcomePill({ outcome }: { outcome: Outcome }) {
  return <span className={`sec-outcome ${OUTCOME[outcome].tone}`}>{OUTCOME[outcome].label}</span>;
}

function whoIcon(by: Incident["by"]) {
  if (by === "ip") return <Globe size={14} />;
  if (by === "host") return <Server size={14} />;
  return <UserRound size={14} />;
}

function incidentTitle(inc: Incident): string {
  const kinds = inc.kinds.map(kindLabel);
  return kinds.length <= 3 ? kinds.join(", ") : `${kinds.slice(0, 3).join(", ")} and ${kinds.length - 3} more`;
}

export function useIncidents() {
  const { resolved, tick } = useApp();
  return useQuery(`sec-incidents-${resolved.from.toISOString()}-${resolved.to.toISOString()}#${tick}`, (signal) => securityApi.incidents(resolved.from, resolved.to, signal));
}

export function usePosture() {
  const { tick } = useApp();
  return useQuery(`sec-posture#${tick}`, (signal) => securityApi.posture(signal));
}

/**
 * The first thing on the page: what got through and what is badly exposed,
 * or a plain all clear with what was stopped.
 */
export function NeedsYou({ incidents, posture, onIncident }: { incidents: IncidentList | undefined; posture: Posture | undefined; onIncident: (i: Incident) => void }) {
  if (!incidents || !posture) return <section className="panel span-12 sec-needs"><div className="panel-state">Loading…</div></section>;
  const through = incidents.incidents.filter((i) => i.outcome === "succeeded" && i.open);
  const exposed = posture.items.filter((p) => p.severity === "critical");
  const tried = incidents.incidents.filter((i) => i.outcome === "attempted").length;
  const blocked = incidents.incidents.filter((i) => i.outcome === "blocked").length;
  const stopped = `${formatCount(tried, false)} attack(s) tried and failed, ${formatCount(blocked, false)} blocked, and ${formatCount(incidents.noise.addresses, false)} address(es) scanning in the background.`;
  if (through.length === 0 && exposed.length === 0) {
    return (
      <section className="panel span-12 sec-needs ok">
        <CheckCircle2 size={20} />
        <div>
          <div className="sec-needs-title">Nothing needs you right now</div>
          <div className="dim">
            Nothing got through in this range and nothing is badly exposed. {stopped}
          </div>
        </div>
      </section>
    );
  }
  return (
    <section className="panel span-12 sec-needs bad">
      <div className="sec-needs-head">
        <TriangleAlert size={20} />
        <div>
          <div className="sec-needs-title">
            {through.length + exposed.length} thing{through.length + exposed.length === 1 ? "" : "s"} need{through.length + exposed.length === 1 ? "s" : ""} you
          </div>
          <div className="dim">{stopped}</div>
        </div>
      </div>
      <ul className="sec-needs-list">
        {through.map((inc) => (
          <li key={inc.key}>
            <button type="button" className="sec-needs-item" onClick={() => onIncident(inc)}>
              <OutcomePill outcome={inc.outcome} />
              <span className="sec-needs-who">
                {whoIcon(inc.by)}
                <span className="mono">{inc.value}</span>
              </span>
              <span className="sec-needs-what">{inc.why.join("; ")}</span>
              <span className="mono dim">{formatLogTime(inc.last)}</span>
            </button>
          </li>
        ))}
        {exposed.map((p) => (
          <li key={p.key}>
            <a className="sec-needs-item" href="#/security?tab=posture">
              <SevBadge severity={p.severity} />
              <span className="sec-needs-what">{p.title}</span>
              <span className="dim">{p.fix}</span>
            </a>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Attacks grouped by who did them, the ones that worked first. */
export function IncidentsPanel({ q, onIncident, limit }: { q: ReturnType<typeof useIncidents>; onIncident: (i: Incident) => void; limit?: number }) {
  const [all, setAll] = useState(false);
  const list = q.data?.incidents ?? [];
  const shown = limit && !all ? list.slice(0, limit) : list;
  return (
    <section className="panel span-12" aria-busy={q.loading || undefined}>
      {q.loading && <div className="progress" aria-hidden />}
      <header className="panel-head">
        <h3 className="panel-title">Incidents</h3>
        <InfoTip text="Findings grouped by the address, account or host behind them, split after an hour of quiet. Got through means Visionary saw it work: a probe answered 2xx, a sign-in after the failed ones, an SSH login after a brute force, or a change after a risky sign-in." />
      </header>
      <div className="panel-body">
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {q.data && list.length === 0 && <div className="panel-state">No incidents in this range.</div>}
      {shown.length > 0 && (
        <ul className="sec-incidents">
          {shown.map((inc) => (
            <li key={inc.key}>
              <button type="button" className="sec-incident" onClick={() => onIncident(inc)}>
                <OutcomePill outcome={inc.outcome} />
                <span className="sec-incident-main">
                  <span className="sec-incident-who">
                    {whoIcon(inc.by)}
                    <span className="mono">{inc.value}</span>
                    <SevBadge severity={inc.severity} />
                    {!inc.open && <span className="pill">Triaged</span>}
                  </span>
                  <span className="sec-incident-what">{inc.why.length ? inc.why.join("; ") : incidentTitle(inc)}</span>
                  {inc.why.length > 0 && <span className="dim">{incidentTitle(inc)}</span>}
                </span>
                <span className="sec-incident-when">
                  <span className="mono dim">{formatLogTime(inc.last)}</span>
                  <span className="dim">
                    {formatCount(inc.findings.length, false)} finding{inc.findings.length === 1 ? "" : "s"}
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {q.data && (
        <div className="sec-noise">
          {q.data.noise.incidents > 0 && (
            <span className="dim">
              Background noise left out: {formatCount(q.data.noise.addresses, false)} address(es) scanning or trying injections that never got a 2xx ({formatCount(q.data.noise.findings, false)} findings, under Findings).
            </span>
          )}
          {limit && list.length > limit && (
            <button type="button" className="btn sm" onClick={() => setAll(!all)}>
              {all ? "Show fewer" : `Show all ${list.length}`}
            </button>
          )}
        </div>
      )}
      </div>
    </section>
  );
}

/** One incident: why it counts, what to do, and the findings and activity behind it. */
export function IncidentDrawer({ incident, onClose, onFinding, onIP }: { incident: Incident; onClose: () => void; onFinding: (f: Finding) => void; onIP: (ip: string) => void }) {
  const { can } = useSession();
  const toast = useToast();
  const [blocking, setBlocking] = useState(false);
  const [blocked, setBlocked] = useState(incident.block);
  const from = new Date(new Date(incident.first).getTime() - 60_000);
  const to = new Date(new Date(incident.last).getTime() + 3_600_000);
  const filter = incident.by === "ip" ? { ip: incident.value } : incident.by === "host" ? { host: incident.value } : incident.by === "account" ? { actor: incident.value } : {};
  const findings = useQuery(`sec-inc-findings-${incident.key}`, (signal) => securityApi.findings(from, to, filter, signal));
  const ids = new Set(incident.findings);
  const own = (findings.data?.findings ?? []).filter((f) => ids.has(f.id));
  const lastFinding = incident.findings[incident.findings.length - 1];
  const endSessions = async (account: string) => {
    try {
      await securityApi.action(lastFinding, "end_sessions", { account });
      toast("ok", `Asked your webhook to end ${account}'s sessions.`);
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Drawer
      onClose={onClose}
      className="wide sec-drawer"
      title={
        <span style={{ display: "inline-flex", gap: 10, alignItems: "center" }}>
          <OutcomePill outcome={incident.outcome} />
          <span className="mono">{incident.value}</span>
        </span>
      }
    >
      <div className={`sec-verdict ${OUTCOME[incident.outcome].tone}`}>
        {incident.outcome === "succeeded" ? <TriangleAlert size={16} /> : <ShieldCheck size={16} />}
        <div>
          <strong>
            {incident.outcome === "succeeded" ? "It got through" : incident.outcome === "blocked" ? "Blocked" : "Tried, and nothing worked that Visionary can see"}
          </strong>
          {incident.why.length > 0 && (
            <ul className="sec-bullets">
              {incident.why.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          )}
        </div>
      </div>
      {blocked && <BlockState block={blocked} onChange={() => setBlocked(null)} />}
      {incident.steps.length > 0 && (
        <>
          <div className="section-label">What to do</div>
          <ol className="sec-todo">
            {incident.steps.map((st) => (
              <li key={st.text}>
                <span>{st.text}</span>
                {st.action === "block_ip" && can("admin") && !blocked && (
                  <button type="button" className="btn sm" onClick={() => setBlocking(!blocking)} aria-expanded={blocking}>
                    <Ban size={14} />
                    Block
                  </button>
                )}
                {st.action === "end_sessions" && can("admin") && st.target && (
                  <button type="button" className="btn sm" onClick={() => endSessions(st.target!)}>
                    <LogOut size={14} />
                    End sessions
                  </button>
                )}
              </li>
            ))}
          </ol>
          {blocking && !blocked && incident.by === "ip" && (
            <BlockForm
              address={incident.value}
              findingId={lastFinding}
              reason={incident.why[0] ?? incidentTitle(incident)}
              onDone={(b) => {
                setBlocking(false);
                setBlocked(b);
              }}
            />
          )}
        </>
      )}
      <div className="section-label">Details</div>
      <dl className="kv">
        <dt>When</dt>
        <dd>
          {formatFull(new Date(incident.first))} to {formatFull(new Date(incident.last))}
        </dd>
        <dt>What</dt>
        <dd>{incident.kinds.map(kindLabel).join(", ")}</dd>
        {incident.hosts.length > 0 && (
          <>
            <dt>Hosts</dt>
            <dd className="mono">{incident.hosts.join(", ")}</dd>
          </>
        )}
        {incident.accounts.length > 0 && (
          <>
            <dt>Accounts</dt>
            <dd className="mono">{incident.accounts.join(", ")}</dd>
          </>
        )}
      </dl>
      {incident.by === "ip" && (
        <div className="sec-actions">
          <button type="button" className="btn" onClick={() => onIP(incident.value)}>
            <Globe size={14} />
            Everything from this IP
          </button>
        </div>
      )}
      <div className="section-label">Findings</div>
      {findings.error && <div className="panel-state error">{linkify(findings.error.message)}</div>}
      {!findings.data && !findings.error && <div className="panel-state">Loading…</div>}
      <ul className="sec-incident-findings">
        {own.map((f) => (
          <li key={f.id}>
            <button type="button" className="sec-incident" onClick={() => onFinding(f)}>
              <SevBadge severity={f.severity} />
              <span className="sec-incident-main">
                <span>{kindLabel(f.kind)}</span>
                <span className="dim">{f.summary || f.target}</span>
              </span>
              <span className="mono dim">{formatLogTime(f.created_at)}</span>
            </button>
          </li>
        ))}
      </ul>
    </Drawer>
  );
}

const POSTURE_GROUPS: { label: string; kinds: string[] }[] = [
  { label: "Exposed to the internet", kinds: ["exposed_path", "debug_endpoint", "anonymous_change"] },
  { label: "Vulnerable packages", kinds: ["vulnerable_package"] },
  { label: "Websites", kinds: ["missing_headers", "tls_expiry", "no_tls"] },
  { label: "Hosts and containers", kinds: ["updates_pending", "container_privileged", "container_root", "port_opened"] },
];

function PostureRow({ item }: { item: PostureItem }) {
  const [open, setOpen] = useState(false);
  const vulns = (item.detail?.vulns as string[] | undefined) ?? [];
  const services = (item.detail?.services as string[] | undefined)?.filter(Boolean) ?? [];
  return (
    <li className="sec-posture-row">
      <button type="button" className="sec-posture-head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <SevBadge severity={item.severity} />
        <span className="sec-posture-name">{item.title}</span>
        {item.last && <span className="mono dim">{formatLogTime(item.last)}</span>}
      </button>
      {open && (
        <div className="sec-posture-body">
          <div>
            <strong>Fix:</strong> {item.fix}
          </div>
          {services.length > 0 && <div className="dim">In {services.join(", ")}</div>}
          {vulns.length > 0 && (
            <ul className="sec-bullets">
              {vulns.map((v) => (
                <li key={v} className="mono">
                  {v}
                </li>
              ))}
            </ul>
          )}
          {item.since && <div className="dim">First seen {formatLogTime(item.since)}</div>}
        </div>
      )}
    </li>
  );
}

/** Weak spots that stay until fixed, grouped by where they are. */
export function PosturePanel({ q }: { q: ReturnType<typeof usePosture> }) {
  const items = q.data?.items ?? [];
  return (
    <section className="panel span-12" aria-busy={q.loading || undefined}>
      {q.loading && <div className="progress" aria-hidden />}
      <header className="panel-head">
        <h3 className="panel-title">Posture</h3>
        <InfoTip text="What is weak now, not what happened: each item stays until it is fixed. Read from your traffic over the last 7 days, the host agent, the security headers and certificates of the sites your uptime checks watch, and the packages your services report." />
      </header>
      <div className="panel-body">
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {q.data && items.length === 0 && (
        <div className="panel-state">
          <ShieldCheck size={16} /> Nothing weak found.
        </div>
      )}
      {POSTURE_GROUPS.map((g) => {
        const group = items.filter((i) => g.kinds.includes(i.kind));
        if (group.length === 0) return null;
        return (
          <div key={g.label} className="sec-posture-group">
            <div className="section-label">
              {g.label} <span className="dim">{group.length}</span>
            </div>
            <ul>
              {group.map((it) => (
                <PostureRow key={it.key} item={it} />
              ))}
            </ul>
          </div>
        );
      })}
      {q.data && (
        <div className="sec-noise dim">
          <ShieldAlert size={13} />
          {q.data.osv_enabled
            ? q.data.osv_checked
              ? `Packages checked against osv.dev ${formatLogTime(q.data.osv_checked)}.`
              : "Packages are checked against osv.dev every 6 hours once your services report them (SDK update needed)."
            : "Package checks are off (Settings → Server → Threat intelligence)."}{" "}
          {q.data.headers_checked ? `Site headers checked ${formatLogTime(q.data.headers_checked)}.` : "Site headers are checked hourly for the URLs your uptime checks watch."}
        </div>
      )}
      </div>
    </section>
  );
}
