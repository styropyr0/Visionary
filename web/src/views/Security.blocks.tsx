import { useMemo, useState } from "react";
import { Ban, Globe, ScrollText, Search, ShieldCheck, ShieldOff, TriangleAlert } from "lucide-react";
import { formatCount, titleCase, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { useSession } from "../lib/team";
import { formatFull, formatLogTime } from "../lib/time";
import { auditLabel, kindLabel, securityApi, type ActivityItem, type Block, type BlockHost, type Verdict } from "../lib/security";
import { DataTable, type Column } from "../components/DataTable";
import { Drawer } from "../components/Modal";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { linkify } from "../components/ErrorNotice";
import { InfoTip } from "../components/InfoTip";
import { IPCard, localResult } from "./Security.triage";

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

export function SevBadge({ severity }: { severity: string }) {
  return <span className={`sec-sev ${severity}`}>{titleCase(severity)}</span>;
}

const DURATIONS = [
  { value: "24", label: "For 24 hours" },
  { value: "168", label: "For 7 days" },
  { value: "720", label: "For 30 days" },
  { value: "0", label: "Until I unblock it" },
];

/** Where a block is enforced, in a few words. */
export function blockWhere(b: Block): string {
  const parts: string[] = [];
  if (b.firewall) parts.push(b.hosts.length ? `firewall on ${b.hosts.join(", ")}` : "firewall on every host");
  if (b.app) parts.push("your apps");
  return parts.join(" and ");
}

function hostState(h: BlockHost): { tone: "ok" | "warn" | "bad"; text: string } {
  switch (h.backend) {
    case "never":
      return { tone: "warn", text: "agent doesn't block yet: update it to 1.5 or later" };
    case "off":
      return { tone: "warn", text: "blocking turned off on this host" };
    case "unsupported":
      return { tone: "bad", text: h.error || "can't block here" };
  }
  if (h.error) return { tone: "bad", text: h.error };
  return h.current ? { tone: "ok", text: `${h.backend}, ${formatCount(h.applied, false)} applied` } : { tone: "warn", text: "applying…" };
}

/**
 * Blocks an address in the host firewall through the agent, in every SDK, or
 * both. With a finding it goes through the finding's action so the webhook
 * fires too.
 */
export function BlockForm({ address, findingId, reason, onDone }: { address: string; findingId?: number; reason?: string; onDone: (b: Block) => void }) {
  const toast = useToast();
  const list = useQuery("sec-blocks", (signal) => securityApi.blocks(signal));
  const [firewall, setFirewall] = useState(true);
  const [app, setApp] = useState(true);
  const [allHosts, setAllHosts] = useState(true);
  const [picked, setPicked] = useState<string[]>([]);
  const [hours, setHours] = useState("168");
  const [why, setWhy] = useState(reason ?? "");
  const [busy, setBusy] = useState(false);
  const hosts = list.data?.hosts ?? [];
  const submit = async () => {
    setBusy(true);
    const req = { firewall, app, hosts: firewall && !allHosts ? picked : [], hours: Number(hours) };
    try {
      let b: Block;
      if (findingId) {
        const out = await securityApi.action(findingId, "block_ip", { ...req, note: why });
        b = out.block as Block;
        if (out.webhook_error) toast("err", `Blocked, but the action webhook failed: ${String(out.webhook_error)}`);
      } else {
        b = await securityApi.block({ address, reason: why, ...req });
      }
      toast("ok", `${b.address} is blocked in the ${blockWhere(b)}.`);
      onDone(b);
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="sec-block-form">
      <label className="sec-check">
        <input type="checkbox" checked={firewall} onChange={(e) => setFirewall(e.target.checked)} />
        <span>
          Host firewall
          <span className="dim"> · dropped before it reaches anything on the machine, through the host agent</span>
        </span>
      </label>
      {firewall && (
        <div className="sec-block-hosts">
          {hosts.length === 0 ? (
            <span className="dim">No host agent reports here yet. The block is kept and applies to agents as they connect.</span>
          ) : (
            <>
              <div className="segmented" role="radiogroup" aria-label="Hosts">
                <button type="button" role="radio" aria-checked={allHosts} className={allHosts ? "on" : ""} onClick={() => setAllHosts(true)}>
                  Every host
                </button>
                <button type="button" role="radio" aria-checked={!allHosts} className={!allHosts ? "on" : ""} onClick={() => setAllHosts(false)}>
                  Some hosts
                </button>
              </div>
              {!allHosts && (
                <div className="sec-chips">
                  {hosts.map((h) => (
                    <button
                      type="button"
                      key={h.host}
                      className={`sec-chip ${picked.includes(h.host) ? "on" : ""}`}
                      aria-pressed={picked.includes(h.host)}
                      onClick={() => setPicked(picked.includes(h.host) ? picked.filter((x) => x !== h.host) : [...picked, h.host])}
                    >
                      {h.host}
                    </button>
                  ))}
                </div>
              )}
            </>
          )}
        </div>
      )}
      <label className="sec-check">
        <input type="checkbox" checked={app} onChange={(e) => setApp(e.target.checked)} />
        <span>
          Your apps
          <span className="dim"> · every server SDK answers it 403, within about 15 seconds, no agent needed</span>
        </span>
      </label>
      <div className="sec-block-row">
        <Select label="How long" align="left" value={hours} options={DURATIONS} onChange={setHours} />
        <input className="input" value={why} maxLength={500} placeholder="Why (optional)" onChange={(e) => setWhy(e.target.value)} aria-label="Why" />
      </div>
      <div>
        <button type="button" className="btn primary" disabled={busy || (!firewall && !app) || (firewall && !allHosts && picked.length === 0)} onClick={submit}>
          <Ban size={14} />
          {busy ? "Blocking…" : `Block ${address}`}
        </button>
      </div>
    </div>
  );
}

/** Whether an address is blocked, where, and a way to lift it. */
export function BlockState({ block, onChange }: { block: Block; onChange: () => void }) {
  const { can } = useSession();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const unblock = async () => {
    setBusy(true);
    try {
      await securityApi.unblock(block.id);
      toast("ok", `${block.address} is no longer blocked.`);
      onChange();
    } catch (e) {
      toast("err", errMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="sec-blocked">
      <ShieldCheck size={16} />
      <div>
        <strong>Blocked{block.address.includes("/") ? ` as part of ${block.address}` : ""}</strong> in the {blockWhere(block)}
        <div className="dim">
          Since {formatLogTime(block.created_at)}
          {block.created_by ? ` by ${block.created_by}` : ""}
          {block.expires_at ? `, until ${formatLogTime(block.expires_at)}` : ""}
          {block.reason ? ` · ${block.reason}` : ""}
        </div>
      </div>
      {can("admin") && (
        <button type="button" className="btn sm" disabled={busy} onClick={unblock}>
          <ShieldOff size={14} />
          {busy ? "Removing…" : "Unblock"}
        </button>
      )}
    </div>
  );
}

/** Every blocked address and whether each host has applied the list. */
export function BlockedPanel({ onIP }: { onIP: (ip: string) => void }) {
  const { tick } = useApp();
  const [n, setN] = useState(0);
  const q = useQuery(`sec-blocks#${tick}-${n}`, (signal) => securityApi.blocks(signal));
  const [adding, setAdding] = useState(false);
  const [address, setAddress] = useState("");
  const { can } = useSession();
  const toast = useToast();
  const blocks = q.data?.blocks ?? [];
  const result = useMemo(
    () => localResult(["id", "address", "where", "reason", "created_at", "expires_at"], blocks.map((b) => [b.id, b.address, blockWhere(b), b.reason, b.created_at, b.expires_at])),
    [blocks],
  );
  const byId = useMemo(() => new Map(blocks.map((b) => [b.id, b])), [blocks]);
  const unblock = async (b: Block) => {
    try {
      await securityApi.unblock(b.id);
      toast("ok", `${b.address} is no longer blocked.`);
      setN(n + 1);
    } catch (e) {
      toast("err", errMessage(e));
    }
  };
  const columns: Column[] = [
    { key: "address", render: (v) => <span className="mono">{String(v)}</span>, width: 180 },
    { key: "where", label: "Blocked in" },
    { key: "reason", render: (v) => (v ? String(v) : <span className="dim">—</span>) },
    { key: "created_at", label: "Since", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
    { key: "expires_at", label: "Until", render: (v) => (v ? <span className="mono dim">{formatLogTime(String(v))}</span> : <span className="dim">Unblocked by hand</span>), width: 150 },
    ...(q.data?.can_edit
      ? [
          {
            key: "id",
            label: "",
            width: 100,
            render: (v: unknown) => (
              <button
                type="button"
                className="btn sm"
                onClick={(e) => {
                  e.stopPropagation();
                  void unblock(byId.get(Number(v))!);
                }}
              >
                Unblock
              </button>
            ),
          },
        ]
      : []),
  ];
  return (
    <section className="panel span-12">
      <header className="panel-head">
        <h3 className="panel-title">Blocked addresses</h3>
        <InfoTip text="Dropped by the host firewall through the agent, refused by every server SDK, or both. Click an address for everything it did." />
        {can("admin") && (
          <button type="button" className="btn sm sec-head-btn" onClick={() => setAdding(!adding)} aria-expanded={adding}>
            <Ban size={14} />
            Block an address
          </button>
        )}
      </header>
      <div className="panel-body">
      {adding && (
        <div className="sec-block-add">
          <input className="input mono" placeholder="203.0.113.7 or 203.0.113.0/24" value={address} onChange={(e) => setAddress(e.target.value.trim())} aria-label="Address" />
          {address && (
            <BlockForm
              address={address}
              onDone={() => {
                setAdding(false);
                setAddress("");
                setN(n + 1);
              }}
            />
          )}
        </div>
      )}
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {q.data && (
        <>
          {blocks.length === 0 ? (
            <div className="panel-state">Nothing is blocked.</div>
          ) : (
            <DataTable result={result} columns={columns} sortable={false} maxHeight={360} rowKey={(r) => r[0]} onRowClick={(r) => !String(r[1]).includes("/") && onIP(String(r[1]))} />
          )}
          {q.data.hosts.length > 0 && (
            <ul className="sec-block-status" aria-label="Firewall on each host">
              {q.data.hosts.map((h) => {
                const st = hostState(h);
                return (
                  <li key={h.host} title={h.skipped?.length ? `Skipped: ${h.skipped.join(", ")}` : undefined}>
                    <span className={`sec-host-dot ${st.tone}`} aria-hidden />
                    <span className="mono sec-host-name">{h.host}</span>
                    <span className="sec-host-text">
                      {st.text}
                      {h.skipped?.length ? ` · ${h.skipped.length} skipped` : ""}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}
      </div>
    </section>
  );
}

type ActivityType = "all" | "request" | "host" | "log" | "audit";

const ACTIVITY_TYPES: { key: ActivityType; label: string }[] = [
  { key: "all", label: "Everything" },
  { key: "request", label: "Requests" },
  { key: "host", label: "Host and security events" },
  { key: "log", label: "Logs" },
  { key: "audit", label: "Visionary audit" },
];

function typeOf(it: ActivityItem): ActivityType {
  return it.type === "event" ? "host" : it.type;
}

function statusTone(status: number): string {
  if (status >= 200 && status < 300) return "ok";
  if (status >= 500) return "bad";
  return status >= 400 ? "warn" : "";
}

/** What an item says in one line. */
function activityWhat(it: ActivityItem) {
  switch (it.type) {
    case "request":
      return (
        <span className="sec-act-req">
          <span className="method">{it.method}</span>
          <span className="mono">{it.path}</span>
          <span className={`flag ${statusTone(it.status ?? 0)}`}>{it.status}</span>
        </span>
      );
    case "log":
      return (
        <span>
          <span className={`sec-level ${it.severity}`}>{it.severity}</span> {it.text}
        </span>
      );
    case "audit":
      return (
        <span>
          {auditLabel(it.kind ?? "")}
          {it.text ? <span className="mono dim"> {it.text}</span> : null}
        </span>
      );
    default:
      return (
        <span>
          {it.severity && it.severity !== "info" && <SevBadge severity={it.severity} />} {kindLabel(it.kind ?? "")}
          {it.text ? <span className="mono"> {it.text}</span> : null}
          {(it.count ?? 1) > 1 ? <span className="dim"> × {formatCount(it.count!, false)}</span> : null}
        </span>
      );
  }
}

/** One timeline across requests, host events, sudo, logs and the audit log. */
export function ActivityTimeline({ items, onIP, maxHeight = 520 }: { items: ActivityItem[]; onIP?: (ip: string) => void; maxHeight?: number }) {
  const [type, setType] = useState<ActivityType>("all");
  const counts = useMemo(() => {
    const c: Record<string, number> = { all: items.length };
    for (const it of items) c[typeOf(it)] = (c[typeOf(it)] ?? 0) + 1;
    return c;
  }, [items]);
  const shown = type === "all" ? items : items.filter((it) => typeOf(it) === type);
  const result = useMemo(() => localResult(["i", "at", "what", "where"], shown.map((it, i) => [i, it.at, it, it])), [shown]);
  const columns: Column[] = [
    { key: "at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
    { key: "what", label: "What happened", render: (v) => activityWhat(v as ActivityItem) },
    {
      key: "where",
      label: "Where and who",
      width: 240,
      render: (v) => {
        const it = v as ActivityItem;
        return (
          <span className="sec-act-where">
            {it.host && <span className="mono">{it.host}</span>}
            {it.actor && <span>{it.actor}</span>}
            {it.ip && onIP && it.type !== "request" && (
              <button
                type="button"
                className="link-btn mono"
                onClick={(e) => {
                  e.stopPropagation();
                  onIP(it.ip!);
                }}
              >
                {it.ip}
              </button>
            )}
            {it.via && <span className="dim">{it.via}</span>}
          </span>
        );
      },
    },
  ];
  if (items.length === 0) return <div className="panel-state">Nothing in this range.</div>;
  return (
    <>
      <div className="sec-chips" role="tablist" aria-label="Show">
        {ACTIVITY_TYPES.filter((t) => t.key === "all" || counts[t.key]).map((t) => (
          <button type="button" key={t.key} role="tab" aria-selected={type === t.key} className={`sec-chip ${type === t.key ? "on" : ""}`} onClick={() => setType(t.key)}>
            {t.label} <span className="dim">{formatCount(counts[t.key] ?? 0, false)}</span>
          </button>
        ))}
      </div>
      <DataTable result={result} columns={columns} sortable={false} maxHeight={maxHeight} rowKey={(r) => r[0]} />
    </>
  );
}

function verdictText(v: Verdict) {
  const got: string[] = [];
  if (v.logins && (v.failed_logins ?? 0) >= 3) got.push(`signed in ${v.logins} time(s) after ${v.failed_logins} failed attempts`);
  if (v.probes_answered) got.push(`${v.probes_answered} probe request(s) answered 2xx`);

  if (v.ssh_logins) got.push(`${v.ssh_logins} SSH login(s)`);
  if (v.sudo) got.push(`${v.sudo} sudo command(s) after logging in`);
  if (v.outcome === "succeeded") return { tone: "critical", title: "It got somewhere", text: got.join(", ") + "." };
  if (v.outcome === "quiet") return { tone: "ok", title: "Nothing suspicious in this range", text: "No probes, refusals or security events." };
  const tried: string[] = [];
  if (v.probes) tried.push(`${v.probes} probe request(s)`);
  if (v.refused) tried.push(`${v.refused} refused with 401 or 403`);
  return { tone: "warning", title: "Tried, and nothing worked that Visionary can see", text: tried.join(", ") + (tried.length ? "." : "") };
}

/** Everything about one address: what it tried, whether it worked, and blocking it. */
export function IPDrawer({ ip, onClose, findingId, reason }: { ip: string; onClose: () => void; findingId?: number; reason?: string }) {
  const { resolved, setRange } = useApp();
  const { can } = useSession();
  const [n, setN] = useState(0);
  const [blocking, setBlocking] = useState(false);
  const q = useQuery(`sec-ipact-${ip}-${resolved.from.toISOString()}-${resolved.to.toISOString()}-${n}`, (signal) => securityApi.ipActivity(ip, resolved.from, resolved.to, signal));
  const d = q.data;
  const v = d ? verdictText(d.verdict) : null;
  return (
    <Drawer
      onClose={onClose}
      className="wide sec-drawer"
      title={
        <span style={{ display: "inline-flex", gap: 10, alignItems: "center" }}>
          <Globe size={16} />
          <span className="mono">{ip}</span>
        </span>
      }
    >
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {!d && !q.error && <div className="panel-state">Loading…</div>}
      {d && v && (
        <>
          <div className={`sec-verdict ${v.tone}`}>
            {v.tone === "critical" ? <TriangleAlert size={16} /> : <ShieldCheck size={16} />}
            <div>
              <strong>{v.title}</strong>
              {v.text && <div>{v.text}</div>}
            </div>
          </div>
          {d.block ? (
            <BlockState block={d.block} onChange={() => setN(n + 1)} />
          ) : (
            can("admin") &&
            d.public && (
              <div className="sec-actions">
                <button type="button" className={blocking ? "btn" : "btn primary"} onClick={() => setBlocking(!blocking)} aria-expanded={blocking}>
                  <Ban size={14} />
                  Block this address
                </button>
                <button
                  type="button"
                  className="btn"
                  onClick={() => {
                    setRange({ kind: "absolute", from: resolved.from.toISOString(), to: resolved.to.toISOString() });
                    window.location.hash = `#/logs?ip=${encodeURIComponent(ip)}`;
                  }}
                >
                  <ScrollText size={14} />
                  Open in Requests
                </button>
              </div>
            )
          )}
          {blocking && !d.block && (
            <BlockForm
              address={ip}
              findingId={findingId}
              reason={reason}
              onDone={() => {
                setBlocking(false);
                setN(n + 1);
              }}
            />
          )}
          <div className="section-label">About this address</div>
          <IPCard ip={ip} />
          {d.routes.rows.length > 0 && (
            <>
              <div className="section-label">What it asked for</div>
              <DataTable
                result={d.routes}
                sortable={false}
                maxHeight={240}
                columns={[
                  { key: "method", render: (v) => <span className="method">{String(v)}</span>, width: 64 },
                  { key: "route", render: (v) => <span className="mono">{String(v)}</span> },
                  { key: "requests", align: "right", unit: "count", width: 80 },
                  { key: "ok", label: "2xx", align: "right", width: 60, render: (v) => ((toNumber(v) ?? 0) > 0 ? formatCount(toNumber(v)!, false) : <span className="dim">0</span>) },
                  { key: "client_errors", label: "4xx", align: "right", unit: "count", width: 60 },
                  { key: "server_errors", label: "5xx", align: "right", unit: "count", width: 60 },
                ]}
              />
            </>
          )}
          <div className="section-label">Everything it did</div>
          <p className="dim">
            Requests, security and host events from it, sudo by anyone who logged in over SSH from it, log lines of its requests and host logs that mention it, and Visionary sign-ins, between{" "}
            {formatFull(resolved.from)} and {formatFull(resolved.to)}.
          </p>
          <ActivityTimeline items={d.items} />
        </>
      )}
    </Drawer>
  );
}

/** Who asked for one probe path, and what they got. */
export function ProbedPathDrawer({ path, onClose, onIP }: { path: string; onClose: () => void; onIP: (ip: string) => void }) {
  const { resolved } = useApp();
  const q = useQuery(`sec-probed-${path}-${resolved.from.toISOString()}-${resolved.to.toISOString()}`, (signal) => securityApi.probed(path, resolved.from, resolved.to, signal));
  const served = q.data?.statuses.rows.filter((r) => Number(r[0]) >= 200 && Number(r[0]) < 300).reduce((n, r) => n + (toNumber(r[1]) ?? 0), 0) ?? 0;
  return (
    <Drawer
      onClose={onClose}
      className="wide sec-drawer"
      title={
        <span style={{ display: "inline-flex", gap: 10, alignItems: "center" }}>
          <Search size={16} />
          <span className="mono">{path}</span>
        </span>
      }
    >
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {!q.data && !q.error && <div className="panel-state">Loading…</div>}
      {q.data && (
        <>
          {served > 0 ? (
            <div className="sec-verdict critical">
              <TriangleAlert size={16} />
              <div>
                <strong>Answered with 2xx {formatCount(served, false)} time(s)</strong>
                <div>Only scanners ask for this path. Stop serving it and rotate anything it may have exposed.</div>
              </div>
            </div>
          ) : (
            <div className="sec-verdict ok">
              <ShieldCheck size={16} />
              <div>
                <strong>Never served</strong>
                <div>Every request for it was refused or not found.</div>
              </div>
            </div>
          )}
          <p className="dim">By status: {q.data.statuses.rows.map((r) => `${String(r[0])} × ${formatCount(toNumber(r[1]) ?? 0, false)}`).join(", ") || "none"}.</p>
          <div className="section-label">Addresses that asked for it</div>
          <DataTable
            result={q.data.ips}
            sortable={false}
            maxHeight={560}
            onRowClick={(r) => onIP(String(r[0]))}
            columns={[
              { key: "ip", label: "IP", render: (v) => <span className="mono">{String(v)}</span>, width: 170 },
              { key: "hits", align: "right", unit: "count", width: 70 },
              {
                key: "answered_2xx",
                label: "2xx",
                align: "right",
                width: 60,
                render: (v) => ((toNumber(v) ?? 0) > 0 ? <span className="flag bad">{formatCount(toNumber(v)!, false)}</span> : <span className="dim">0</span>),
              },
              { key: "first_seen", label: "First", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
              { key: "last_seen", label: "Last", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
              { key: "user_agent", label: "User agent", render: (v) => <span className="mono dim">{v ? String(v) : "—"}</span> },
            ]}
          />
        </>
      )}
    </Drawer>
  );
}

const HOST_KINDS = ["ssh_login", "ssh_failed", "ssh_bruteforce", "sudo", "ssh_key_added", "ssh_key_removed", "file_changed", "suspicious_process", "port_opened", "port_closed", "container_privileged", "container_root", "container_new_image", "updates_pending", "reboot"];

/** Every event the agent reported, filterable and paged, newest first. */
export function HostEventsPanel({ onIP }: { onIP: (ip: string) => void }) {
  const { resolved, tick } = useApp();
  const [filters, setFilters] = useState({ host: "", kind: "", user: "" });
  const [typed, setTyped] = useState({ host: "", user: "" });
  const [older, setOlder] = useState<ActivityItem[]>([]);
  const [more, setMore] = useState<boolean | null>(null);
  const [selected, setSelected] = useState<ActivityItem | null>(null);
  const key = `sec-hostev-${resolved.from.toISOString()}-${resolved.to.toISOString()}-${JSON.stringify(filters)}#${tick}`;
  const q = useQuery(key, (signal) => securityApi.hostEvents(resolved.from, resolved.to, filters, signal));
  const [lastKey, setLastKey] = useState(key);
  if (lastKey !== key) {
    setLastKey(key);
    setOlder([]);
    setMore(null);
  }
  const rows = useMemo(() => [...(q.data?.events ?? []), ...older], [q.data, older]);
  const hasMore = more ?? q.data?.more ?? false;
  const loadMore = async () => {
    const last = rows[rows.length - 1];
    if (!last?.ref) return;
    const next = await securityApi.hostEvents(resolved.from, resolved.to, { ...filters, before: String(last.ref) });
    setOlder((o) => [...o, ...next.events]);
    setMore(next.more);
  };
  const apply = () => setFilters({ ...filters, ...typed });
  const kinds = q.data?.kinds ?? {};
  const result = useMemo(() => localResult(["ref", "at", "event"], rows.map((it) => [it.ref, it.at, it])), [rows]);
  const columns: Column[] = [
    { key: "at", label: "Time", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 160 },
    { key: "event", label: "Host", width: 150, render: (v) => <span className="mono">{(v as ActivityItem).host || "—"}</span> },
    { key: "event", label: "Event", width: 190, render: (v) => activityWhat({ ...(v as ActivityItem), text: undefined }) },
    { key: "event", label: "User", width: 120, render: (v) => (v as ActivityItem).actor || <span className="dim">—</span> },
    { key: "event", label: "From", width: 150, render: (v) => ((v as ActivityItem).ip ? <span className="mono">{(v as ActivityItem).ip}</span> : <span className="dim">—</span>) },
    { key: "event", label: "Detail", render: (v) => ((v as ActivityItem).text ? <span className="mono">{(v as ActivityItem).text}</span> : <span className="dim">—</span>) },
  ];
  return (
    <section className="panel span-12" aria-busy={q.loading || undefined}>
      {q.loading && <div className="progress" aria-hidden />}
      <header className="panel-head">
        <h3 className="panel-title">Host events</h3>
        <InfoTip text="Every SSH login and failure, sudo, key and file change, process, port, container and update the agent reported. Click one for its detail and what else happened on the host around it." />
      </header>
      <div className="panel-body">
      <div className="filter-row sec-filters" role="search">
        <Select
          label="Event"
          align="left"
          value={filters.kind}
          maxWidth={240}
          onChange={(kind) => setFilters({ ...filters, kind })}
          options={[
            { value: "", label: "Every event" },
            ...HOST_KINDS.filter((k) => kinds[k] || k === filters.kind).map((k) => ({ value: k, label: `${kindLabel(k)} (${formatCount(kinds[k] ?? 0, false)})` })),
          ]}
        />
        <input className="input" placeholder="Host" value={typed.host} style={{ width: 150 }} aria-label="Host" onChange={(e) => setTyped({ ...typed, host: e.target.value })} onBlur={apply} onKeyDown={(e) => e.key === "Enter" && apply()} />
        <input className="input" placeholder="User" value={typed.user} style={{ width: 130 }} aria-label="User" onChange={(e) => setTyped({ ...typed, user: e.target.value })} onBlur={apply} onKeyDown={(e) => e.key === "Enter" && apply()} />
        {(filters.host || filters.kind || filters.user) && (
          <button
            type="button"
            className="btn ghost"
            onClick={() => {
              setTyped({ host: "", user: "" });
              setFilters({ host: "", kind: "", user: "" });
            }}
          >
            Clear filters
          </button>
        )}
      </div>
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {q.data &&
        (rows.length === 0 ? (
          <div className="panel-state">No host events in this range.</div>
        ) : (
          <DataTable result={result} columns={columns} sortable={false} maxHeight={720} rowKey={(r) => r[0]} onRowClick={(r) => setSelected(r[2] as ActivityItem)} />
        ))}
      {hasMore && (
        <div className="panel-foot">
          <button type="button" className="btn" onClick={loadMore}>
            Load older events
          </button>
        </div>
      )}
      {selected && <HostEventDrawer event={selected} onClose={() => setSelected(null)} onIP={onIP} />}
      </div>
    </section>
  );
}

/** One host event and what else happened on the host around it. */
export function HostEventDrawer({ event, onClose, onIP }: { event: ActivityItem; onClose: () => void; onIP: (ip: string) => void }) {
  const at = new Date(event.at).getTime();
  const from = new Date(at - 30 * 60_000);
  const to = new Date(at + 30 * 60_000);
  const q = useQuery(`sec-hostaround-${event.ref}`, (signal) => securityApi.hostEvents(from, to, { host: event.host ?? "" }, signal));
  const detail = Object.entries(event.detail ?? {}).filter(([k]) => k !== "summary");
  return (
    <Drawer
      onClose={onClose}
      className="wide sec-drawer"
      title={
        <span style={{ display: "inline-flex", gap: 10, alignItems: "center" }}>
          {event.severity && <SevBadge severity={event.severity} />}
          {kindLabel(event.kind ?? "")}
        </span>
      }
    >
      <dl className="kv">
        <dt>When</dt>
        <dd>{formatFull(new Date(event.at))}</dd>
        <dt>Host</dt>
        <dd>
          <a className="mono" href={`#/system?instance=${encodeURIComponent(event.host ?? "")}`}>
            {event.host}
          </a>
        </dd>
        {event.actor && (
          <>
            <dt>User</dt>
            <dd>{event.actor}</dd>
          </>
        )}
        {event.ip && (
          <>
            <dt>From</dt>
            <dd className="sec-ip-row">
              <span className="mono">{event.ip}</span>
              <button type="button" className="btn sm" onClick={() => onIP(event.ip!)}>
                <Globe size={14} />
                Everything from this IP
              </button>
            </dd>
          </>
        )}
        {event.text && (
          <>
            <dt>Detail</dt>
            <dd className="mono">{event.text}</dd>
          </>
        )}
        {(event.count ?? 1) > 1 && (
          <>
            <dt>Occurrences</dt>
            <dd>{formatCount(event.count!, false)}</dd>
          </>
        )}
        {detail.map(([k, v]) => (
          <span key={k} style={{ display: "contents" }}>
            <dt>{titleCase(k.replace(/_/g, " "))}</dt>
            <dd className="mono">{typeof v === "object" ? JSON.stringify(v) : String(v)}</dd>
          </span>
        ))}
      </dl>
      <div className="section-label">On {event.host} within 30 minutes</div>
      {q.error && <div className="panel-state error">{linkify(q.error.message)}</div>}
      {q.data && <ActivityTimeline items={q.data.events} onIP={onIP} maxHeight={420} />}
    </Drawer>
  );
}
