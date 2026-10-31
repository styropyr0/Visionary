import { useMemo, useState } from "react";
import { AlertTriangle, KeyRound, MapPin, Plus, Trash2 } from "lucide-react";
import { CopyButton, Modal } from "../components/Modal";
import { Select } from "../components/Select";
import { useToast } from "../components/Toast";
import { ErrorNotice } from "../components/ErrorNotice";
import { InfoTip } from "../components/InfoTip";
import { useChartTokens } from "../components/VizView";
import type { Result } from "../lib/api";
import { formatMs, toNumber } from "../lib/format";
import { formatRelative } from "../lib/time";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import {
  createLocation,
  deleteLocation,
  formatUptime,
  getPanel,
  listLocations,
  locationColor,
  LOCATION_STATE_LABEL,
  locationOrder,
  newLocationKey,
  RULE_LABEL,
  type CheckKind,
  type LocationRule,
  type LocationState,
  type UptimeLocation,
} from "../lib/uptime";

/** Uptime → Locations: where checks can run from, and the keys their runners use. */
export function LocationsTab() {
  const { tick } = useApp();
  const toast = useToast();
  const [version, setVersion] = useState(0);
  const list = useQuery(`up-locations#${tick}#${version}`, (signal) => listLocations(signal));
  const [adding, setAdding] = useState(false);
  const [secret, setSecret] = useState<{ name: string; secret: string } | null>(null);
  const [removing, setRemoving] = useState<UptimeLocation | null>(null);
  const [rekeying, setRekeying] = useState<UptimeLocation | null>(null);
  const reload = () => setVersion((v) => v + 1);
  const data = list.data;
  const admin = data?.can_admin ?? false;

  const rekey = async (l: UptimeLocation) => {
    try {
      const res = await newLocationKey(l.name);
      setSecret({ name: l.name, secret: res.secret });
      reload();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
    setRekeying(null);
  };

  const remove = async (l: UptimeLocation) => {
    try {
      await deleteLocation(l.name);
      toast("ok", `Removed ${l.name}`);
      reload();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
    setRemoving(null);
  };

  return (
    <div className="grid">
      {list.error && !data && <ErrorNotice title="Could not load locations" error={list.error} />}
      <section className="panel span-12" aria-busy={list.loading || undefined}>
        {list.loading && <div className="progress" aria-hidden />}
        <header className="panel-head">
          <h3 className="panel-title">Locations</h3>
          <div className="panel-actions">
            {admin && (
              <button type="button" className="btn primary sm" onClick={() => setAdding(true)}>
                <Plus size={14} />
                Add location
              </button>
            )}
          </div>
        </header>
        <p className="up-loc-intro dim">
          A check runs from the server unless you pick more places in its form. Each other location is a host agent started with <code>VISIONARY_AGENT_LOCATION</code> and that
          location's key, on a VPS in another region or inside a private network. A location that stops reporting for {Math.round((data?.stale_after_s ?? 120) / 60)} minutes is
          marked stale and left out of its checks' state.
        </p>
        {data && (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Location</th>
                  <th style={{ width: 130 }}>State</th>
                  <th style={{ width: 130 }}>Last seen</th>
                  <th style={{ width: 110 }}>Version</th>
                  <th style={{ width: 150 }}>Browser checks</th>
                  <th className="num" style={{ width: 80 }}>
                    Checks
                  </th>
                  {admin && <th style={{ width: 96 }} aria-label="Actions" />}
                </tr>
              </thead>
              <tbody>
                {data.locations.map((l) => (
                  <tr key={l.name}>
                    <td className="up-name">
                      <span className="up-loc-name">
                        <MapPin size={13} aria-hidden />
                        {l.name}
                      </span>
                      <span className="mono dim">{l.builtin ? "The Visionary server itself" : l.host || (l.key_prefix ? `key ${l.key_prefix}…` : "no key yet")}</span>
                    </td>
                    <td>
                      <span className={`up-loc-state ${l.state}`}>
                        <i aria-hidden />
                        {l.state === "online" ? "Online" : l.state === "offline" ? "Offline" : "Never connected"}
                      </span>
                    </td>
                    <td className="dim" title={l.last_seen_at}>
                      {l.builtin ? "now" : l.last_seen_at ? formatRelative(l.last_seen_at) : "never"}
                    </td>
                    <td className="mono dim">{l.builtin ? "—" : (l.version ?? "—")}</td>
                    <td className="dim" title={l.browser_version}>
                      {l.browser ? "Yes" : l.builtin || l.last_seen_at ? "No browser found" : "—"}
                    </td>
                    <td className="num">{l.checks}</td>
                    {admin && (
                      <td className="num">
                        {!l.builtin && (
                          <div className="up-actions">
                            <button type="button" className="icon-btn" title="New key (the old one stops working)" aria-label={`New key for ${l.name}`} onClick={() => setRekeying(l)}>
                              <KeyRound size={14} />
                            </button>
                            <button type="button" className="icon-btn" title="Remove" aria-label={`Remove ${l.name}`} onClick={() => setRemoving(l)}>
                              <Trash2 size={14} />
                            </button>
                          </div>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {adding && (
        <AddLocationModal
          onClose={() => setAdding(false)}
          onCreated={(name, key) => {
            setAdding(false);
            setSecret({ name, secret: key });
            reload();
          }}
        />
      )}
      {secret && <KeyModal name={secret.name} secret={secret.secret} onClose={() => setSecret(null)} />}
      {rekeying && (
        <Modal
          title="New location key"
          narrow
          onClose={() => setRekeying(null)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setRekeying(null)}>
                Cancel
              </button>
              <button type="button" className="btn primary" onClick={() => void rekey(rekeying)}>
                Make a new key
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>The runner of {rekeying.name} stops working with its current key until you give it the new one.</p>
        </Modal>
      )}
      {removing && (
        <Modal
          title="Remove location"
          narrow
          onClose={() => setRemoving(null)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setRemoving(null)}>
                Cancel
              </button>
              <button type="button" className="btn danger" onClick={() => void remove(removing)}>
                Remove location
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>
            Remove {removing.name} and revoke its key? Its results stay with their checks. A location that checks still run from can't be removed; take it out of them first.
          </p>
        </Modal>
      )}
    </div>
  );
}

function AddLocationModal({ onClose, onCreated }: { onClose: () => void; onCreated: (name: string, secret: string) => void }) {
  const [name, setName] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      const res = await createLocation(name.trim().toLowerCase());
      onCreated(res.location.name, res.secret);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
    setBusy(false);
  };
  return (
    <Modal
      title="Add location"
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={() => void submit()} disabled={busy || !name.trim()}>
            Add location
          </button>
        </>
      }
    >
      <form
        className="up-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <label className="field">
          <span>Name</span>
          <input className="input mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="frankfurt-vps" maxLength={48} autoFocus />
          <small>Lowercase letters, digits, dots, dashes and underscores. Checks and the status page show it.</small>
        </label>
        {err && <div className="form-error">{err}</div>}
      </form>
    </Modal>
  );
}

function KeyModal({ name, secret, onClose }: { name: string; secret: string; onClose: () => void }) {
  const origin = window.location.origin;
  const env = `VISIONARY_URL=${origin}\nVISIONARY_KEY=${secret}\nVISIONARY_AGENT_LOCATION=${name}`;
  return (
    <Modal
      title={`Run ${name}`}
      onClose={onClose}
      footer={
        <button type="button" className="btn primary" onClick={onClose}>
          Done
        </button>
      }
    >
      <div className="notice">
        <AlertTriangle size={16} />
        <span>This is the only time the key is shown. Store it somewhere safe now.</span>
      </div>
      <div className="up-key-secret">
        <code className="mono">{secret}</code>
        <CopyButton text={secret} />
      </div>
      <p className="up-loc-intro">Start the host agent on the machine that should run the checks, with these variables:</p>
      <div className="up-key-secret">
        <pre className="mono">{env}</pre>
        <CopyButton text={env} />
      </div>
      <p className="up-loc-intro dim">
        In Docker, the repository's <code>deploy/location/compose.yaml</code> takes the same three variables. Browser checks run there only when Chrome or Chromium is installed (the{" "}
        <code>location-browser</code> image has it). Then pick {name} in a check's form.
      </p>
    </Modal>
  );
}

/** Where a check runs from, and how its locations combine. Part of the check form. */
export function LocationsField({
  kind,
  value,
  rule,
  onChange,
  onRule,
}: {
  kind: CheckKind;
  value: string[];
  rule: LocationRule;
  onChange: (v: string[]) => void;
  onRule: (r: LocationRule) => void;
}) {
  const list = useQuery("up-locations-form", (signal) => listLocations(signal));
  const locs = list.data?.locations ?? [];
  if (locs.length < 2 && value.length <= 1 && (value[0] ?? "server") === "server") {
    return (
      <div className="field">
        <span>Runs from</span>
        <small>
          The Visionary server. To run it from more places too, add locations under <a href="#/uptime?tab=locations">Uptime → Locations</a>.
        </small>
      </div>
    );
  }
  const names = locationOrder([...new Set([...locs.map((l) => l.name), ...value])]);
  const toggle = (n: string, on: boolean) => {
    const next = on ? [...value, n] : value.filter((x) => x !== n);
    onChange(locationOrder(next.length ? next : ["server"]));
  };
  return (
    <div className="field">
      <span>Runs from</span>
      <div className="up-loc-picks" role="group" aria-label="Locations">
        {names.map((n) => {
          const l = locs.find((x) => x.name === n);
          const on = value.includes(n);
          const noBrowser = (kind === "browser" && !!l && !l.browser) || (kind !== "browser" && !!l?.kinds?.length && !l.kinds.includes(kind));
          const why = noBrowser
            ? kind === "browser"
              ? "No Chrome or Chromium found there, so browser checks can't run from it"
              : `Its runner can't run ${kind} checks (it may be an older agent, or ping is not allowed there)`
            : l && l.state !== "online"
              ? l.state === "new"
                ? "Its runner has not connected yet"
                : "Its runner is not reporting"
              : undefined;
          return (
            <label key={n} className={`up-loc-pick ${on ? "on" : ""} ${noBrowser ? "off" : ""}`} title={why}>
              <input type="checkbox" checked={on} disabled={noBrowser && !on} onChange={(e) => toggle(n, e.target.checked)} />
              <span className={`up-loc-state ${l?.state ?? "offline"}`}>
                <i aria-hidden />
              </span>
              {n}
              {noBrowser && <span className="dim">{kind === "browser" ? "no browser" : "can't run it"}</span>}
            </label>
          );
        })}
      </div>
      {value.length > 1 && (
        <div className="up-row up-loc-rule">
          <span>Down when</span>
          <div style={{ width: 210 }}>
            <Select
              label="Down when"
              value={rule}
              align="left"
              onChange={(v) => onRule(v as LocationRule)}
              options={(Object.keys(RULE_LABEL) as LocationRule[]).map((r) => ({ value: r, label: RULE_LABEL[r] }))}
            />
          </div>
        </div>
      )}
      <small>
        {value.length > 1
          ? "Each location runs it on the schedule. Locations that stop reporting are left out of the vote."
          : "Pick more than one to see whether it is down everywhere or only somewhere."}
      </small>
    </div>
  );
}

/** One dot per location with its state, for the checks list. */
export function LocationDots({ locations }: { locations: LocationState[] }) {
  return (
    <span className="up-loc-dots" aria-label="Locations">
      {locations.map((l) => (
        <span key={l.name} className={`up-loc-dot ${l.state}`} title={`${l.name}: ${LOCATION_STATE_LABEL[l.state]}${l.last_error && l.state === "down" ? ` · ${l.last_error}` : ""}`}>
          <i aria-hidden />
          {l.name}
        </span>
      ))}
    </span>
  );
}

interface LocationSeries {
  name: string;
  checks: number;
  failed: number;
  avgMs: number | null;
  /** Response time per bucket, null where nothing passed. */
  points: (number | null)[];
  /** Failed share per bucket, null where nothing ran. */
  fails: (number | null)[];
}

/** Rows of [t, location, ...values] turned into one series per location over the same buckets. */
function byLocation(res: Result | undefined, value: (row: unknown[]) => number | null, names: string[]) {
  const ts = [...new Set((res?.rows ?? []).map((r) => String(r[0])))].sort();
  const at = new Map(ts.map((t, i) => [t, i]));
  const out = new Map(names.map((n) => [n, Array<number | null>(ts.length).fill(null)]));
  for (const r of res?.rows ?? []) {
    const line = out.get(String(r[1]));
    if (line) line[at.get(String(r[0]))!] = value(r);
  }
  return out;
}

/**
 * Per location: its state now, uptime and response time over the range, and
 * a small chart of each, colored by location the same way on every check.
 */
export function LocationsPanel({ id, names, states, from, to, refresh }: { id: number; names: string[]; states: LocationState[]; from: Date; to: Date; refresh: string }) {
  const tokens = useChartTokens();
  const all = useQuery("up-locations-colors", (signal) => listLocations(signal));
  const totals = useQuery(`up-loc-tot-${refresh}`, (signal) => getPanel(id, "locations", from, to, signal));
  const lat = useQuery(`up-loc-lat-${refresh}`, (signal) => getPanel(id, "location_latency", from, to, signal));
  const avail = useQuery(`up-loc-av-${refresh}`, (signal) => getPanel(id, "location_availability", from, to, signal));
  const order = locationOrder(names);
  const orderKey = order.join(",");
  const allNames = (all.data?.locations ?? []).map((l) => l.name);
  const series = useMemo<LocationSeries[]>(() => {
    const names = orderKey.split(",");
    const points = byLocation(lat.data, (r) => toNumber(r[2]), names);
    const fails = byLocation(
      avail.data,
      (r) => {
        const n = toNumber(r[2]) ?? 0;
        return n ? (100 * (toNumber(r[3]) ?? 0)) / n : null;
      },
      names,
    );
    return names.map((name) => {
      const row = totals.data?.rows.find((r) => r[0] === name);
      return {
        name,
        checks: toNumber(row?.[1]) ?? 0,
        failed: toNumber(row?.[2]) ?? 0,
        avgMs: toNumber(row?.[3]),
        points: points.get(name) ?? [],
        fails: fails.get(name) ?? [],
      };
    });
  }, [totals.data, lat.data, avail.data, orderKey]);
  const loading = totals.loading || lat.loading || avail.loading;
  return (
    <section className="panel span-12" aria-busy={loading || undefined}>
      {loading && <div className="progress" aria-hidden />}
      <header className="panel-head">
        <h3 className="panel-title">
          By location
          <InfoTip text="Each location's own results over the range: uptime, average response time of the runs that passed there, and both over time. The check's state above combines them by its rule." />
        </h3>
      </header>
      {totals.error && <div className="panel-state error">{totals.error.message}</div>}
      <div className="up-loc-grid">
        {series.map((s) => {
          const color = locationColor(s.name, allNames, tokens.series);
          const st = states.find((x) => x.name === s.name);
          return (
            <div className="up-loc-card" key={s.name}>
              <div className="up-loc-card-head">
                <span className="up-loc-swatch" style={{ background: color }} aria-hidden />
                <strong>{s.name}</strong>
                {st && (
                  <span className={`up-loc-dot ${st.state}`} title={st.last_error && st.state === "down" ? st.last_error : undefined}>
                    <i aria-hidden />
                    {LOCATION_STATE_LABEL[st.state]}
                  </span>
                )}
              </div>
              <div className="up-loc-figures">
                <div>
                  <span className="dim">Uptime</span>
                  <b>{formatUptime(s.checks ? (100 * (s.checks - s.failed)) / s.checks : null)}</b>
                </div>
                <div>
                  <span className="dim">Avg response</span>
                  <b>{s.avgMs === null ? "—" : formatMs(s.avgMs)}</b>
                </div>
                <div>
                  <span className="dim">Runs</span>
                  <b>{s.checks.toLocaleString()}</b>
                </div>
              </div>
              <MiniLine points={s.points} color={color} label={`Response time from ${s.name}`} format={formatMs} empty="No passing runs" />
              <MiniLine points={s.fails} color={tokens.status.critical} label={`Failed runs from ${s.name}`} format={(v) => `${Math.round(v)}%`} max={100} empty="No runs" />
            </div>
          );
        })}
      </div>
    </section>
  );
}

function MiniLine({ points, color, label, format, max, empty }: { points: (number | null)[]; color: string; label: string; format: (v: number) => string; max?: number; empty: string }) {
  const w = 240;
  const h = 34;
  const vals = points.filter((p): p is number => p !== null);
  if (vals.length === 0) return <div className="up-loc-mini none dim">{empty}</div>;
  const top = max ?? Math.max(...vals, 1);
  const x = (i: number) => (points.length < 2 ? w / 2 : (i / (points.length - 1)) * w);
  const y = (v: number) => h - 2 - (v / top) * (h - 4);
  let d = "";
  let pen = false;
  points.forEach((p, i) => {
    if (p === null) {
      pen = false;
      return;
    }
    d += `${pen ? "L" : "M"}${x(i).toFixed(1)},${y(p).toFixed(1)}`;
    // A lone point is drawn as a dot by the round cap.
    if (!pen && (points[i + 1] ?? null) === null) d += "h0.1";
    pen = true;
  });
  const peak = Math.max(...vals);
  return (
    <div className="up-loc-mini" title={`${label}: up to ${format(peak)}`}>
      <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" role="img" aria-label={`${label}: up to ${format(peak)}`}>
        <path d={d} fill="none" stroke={color} strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
      </svg>
    </div>
  );
}
