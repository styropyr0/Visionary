import { useMemo, useState } from "react";
import { AlertTriangle, Bell, EyeOff, Sparkles } from "lucide-react";
import { api, type Result } from "../lib/api";
import { formatBytes, formatCount, formatPercent } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatRelative } from "../lib/time";
import { Select } from "../components/Select";
import { Modal } from "../components/Modal";
import { useToast } from "../components/Toast";
import { ErrorNotice } from "../components/ErrorNotice";
import { useSession } from "../lib/team";
import { alertFromSearch, asLevel, fieldTerm, logSettingsApi, logsApi, templateLiteral, type CompareChange, type Pattern } from "../lib/applogs";
import { LevelBadge } from "./AppLogs";

/** A small bar chart of counts, for a pattern's trend. */
function Spark({ points }: { points: number[] }) {
  const max = Math.max(1, ...points);
  return (
    <span className="lg-spark" aria-hidden>
      {points.map((p, i) => (
        <span key={i} style={{ height: `${Math.max(p > 0 ? 8 : 0, (100 * p) / max)}%` }} />
      ))}
    </span>
  );
}

/** Message templates with <*> shown as placeholders. */
export function Template({ text }: { text: string }) {
  return (
    <span className="mono lg-template">
      {text.split("<*>").map((part, i, all) => (
        <span key={i}>
          {part}
          {i < all.length - 1 && <span className="lg-ph">•</span>}
        </span>
      ))}
    </span>
  );
}

function useRun() {
  const { resolved, tick } = useApp();
  return { resolved, key: `${resolved.from.getTime()}-${resolved.to.getTime()}-${tick}` };
}

// ── Patterns ──────────────────────────────────────────────────────────────

export function PatternsTab({ q, onSearch }: { q: string; onSearch: (q: string) => void }) {
  const { resolved, key } = useRun();
  const toast = useToast();
  const { can } = useSession();
  const res = useQuery(`patterns:${key}:${q}`, (signal) => logsApi.patterns(resolved.from, resolved.to, q, signal));
  const [onlyNew, setOnlyNew] = useState(false);
  const list = (res.data?.patterns ?? []).filter((p) => !onlyNew || p.new);
  const alert = async (p: Pattern) => {
    try {
      const r = await alertFromSearch(`Again: ${p.template.slice(0, 80)}`, `pattern:${p.id}`, p.service ?? undefined);
      toast("ok", "Alert created. Pick where it notifies in Alerts.");
      window.location.hash = `#/alerts?rule=${r.id}`;
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };
  if (res.error) return <ErrorNotice title="Could not load patterns" error={res.error} />;
  return (
    <section className="panel lg-section">
      <header className="lg-section-head">
        <div>
          <h3 className="panel-title">Message patterns</h3>
          <p className="dim">
            Lines grouped by their message with numbers, ids and the like taken out, busiest first.
            {res.data && ` ${formatCount(res.data.total)} lines in this range.`}
            {res.data && res.data.pending > 0 && ` ${formatCount(res.data.pending)} new lines are still being grouped.`}
          </p>
        </div>
        <label className="lg-check">
          <input type="checkbox" checked={onlyNew} onChange={(e) => setOnlyNew(e.target.checked)} /> Only new in this range
        </label>
      </header>
      {!res.data ? (
        <div className="panel-state">Loading…</div>
      ) : list.length === 0 ? (
        <div className="panel-state">{onlyNew ? "No pattern first appeared in this range." : "No lines in this range."}</div>
      ) : (
        <div className="table-wrap">
          <table className="data lg-patterns">
            <thead>
              <tr>
                <th>Pattern</th>
                <th>Level</th>
                <th style={{ textAlign: "right" }}>Lines</th>
                <th>Trend</th>
                <th>First seen</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((p) => (
                <tr key={p.id}>
                  <td>
                    <button type="button" className="link-btn lg-pattern-link" onClick={() => onSearch(`pattern:${p.id}`)}>
                      <Template text={p.template} />
                    </button>
                    <div className="dim lg-pattern-sub">
                      {p.service ?? "no service"}
                      {p.code_file && ` · ${p.code_file}${p.code_line ? `:${p.code_line}` : ""}`}
                      {p.new && <span className="pill lg-new">New</span>}
                    </div>
                  </td>
                  <td>
                    <LevelBadge level={asLevel(p.level)} />
                  </td>
                  <td style={{ textAlign: "right" }}>{formatCount(p.count)}</td>
                  <td>
                    <Spark points={p.trend} />
                  </td>
                  <td className="dim" title={p.first_seen ? formatFull(new Date(p.first_seen)) : ""}>
                    {p.first_seen ? formatRelative(p.first_seen) : "—"}
                    {p.first_version && <div className="mono">{p.first_version}</div>}
                  </td>
                  <td className="lg-row-actions">
                    {can("edit") && (
                      <button type="button" className="icon-btn" aria-label="Alert when this happens again" title="Alert when this happens again" onClick={() => alert(p)}>
                        <Bell size={14} />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// ── Volume ────────────────────────────────────────────────────────────────

function rows(res: Result | undefined): Record<string, unknown>[] {
  if (!res) return [];
  return res.rows.map((r) => Object.fromEntries(res.columns.map((c, i) => [c, r[i]])));
}

function Bars({ title, data, label, total, onPick }: { title: string; data: Record<string, unknown>[]; label: string; total: number; onPick?: (v: unknown) => void }) {
  return (
    <section className="panel lg-bars">
      <h3 className="panel-title">{title}</h3>
      {data.length === 0 ? (
        <div className="dim">Nothing</div>
      ) : (
        <ul>
          {data.map((d, i) => {
            const bytes = Number(d.bytes ?? 0);
            return (
              <li key={i}>
                <button type="button" className="link-btn lg-bar-name" onClick={() => onPick?.(d[label])} disabled={!onPick}>
                  {d[label] === null || d[label] === undefined ? <span className="dim">none</span> : String(d[label])}
                </button>
                <span className="lg-bar-track">
                  <span className="lg-bar-fill" style={{ width: `${total ? (100 * bytes) / total : 0}%` }} />
                </span>
                <span className="dim lg-bar-num">
                  {formatBytes(bytes)} · {formatCount(Number(d.lines ?? 0))}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

export function VolumeTab({ q, onSearch }: { q: string; onSearch: (q: string) => void }) {
  const { resolved, key } = useRun();
  const { can } = useSession();
  const res = useQuery(`volume:${key}:${q}`, (signal) => logsApi.volume(resolved.from, resolved.to, q, signal));
  const [act, setAct] = useState<{ service: string | null; text: string; label: string } | null>(null);
  if (res.error) return <ErrorNotice title="Could not load volume" error={res.error} />;
  if (!res.data) return <div className="panel-state">Loading…</div>;
  const d = res.data;
  const code = rows(d.code);
  return (
    <div className="lg-volume">
      <div className="lg-stats">
        <div className="panel lg-stat">
          <div className="dim">Lines</div>
          <div className="lg-stat-value">{formatCount(d.lines)}</div>
        </div>
        <div className="panel lg-stat">
          <div className="dim">Stored</div>
          <div className="lg-stat-value">{formatBytes(d.bytes)}</div>
        </div>
      </div>
      <div className="lg-bars-grid">
        <Bars title="By service" data={rows(d.services)} label="service" total={d.bytes} onPick={(v) => v && onSearch(fieldTerm("service", v))} />
        <Bars title="By source" data={rows(d.sources)} label="source" total={d.bytes} onPick={(v) => v && onSearch(fieldTerm("source", v))} />
        <Bars title="By level" data={rows(d.levels)} label="level" total={d.bytes} onPick={(v) => v && onSearch(`level:${String(v)}`)} />
      </div>

      <section className="panel lg-section">
        <header className="lg-section-head">
          <div>
            <h3 className="panel-title">The code that logs the most</h3>
            <p className="dim">Where volume comes from, by the line of code that logs it (SDKs that record it). Lower a chatty one or drop it.</p>
          </div>
        </header>
        {code.length === 0 ? (
          <div className="panel-state">No line in this range says where it was logged. The Go, Python and Ruby SDKs record it; shippers and OpenTelemetry may.</div>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Code</th>
                  <th>Example</th>
                  <th style={{ textAlign: "right" }}>Share</th>
                  <th style={{ textAlign: "right" }}>Lines</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {code.map((c, i) => {
                  const where = `${String(c.code_file)}${c.code_line ? `:${String(c.code_line)}` : ""}`;
                  return (
                    <tr key={i}>
                      <td>
                        <button type="button" className="link-btn mono" onClick={() => onSearch([c.service ? fieldTerm("service", c.service) : "", fieldTerm("file", c.code_file), c.code_line ? `line:${String(c.code_line)}` : ""].filter(Boolean).join(" "))}>
                          {where}
                        </button>
                        <div className="dim">{String(c.service ?? "no service")}</div>
                      </td>
                      <td className="mono lg-ellipsis" title={String(c.sample ?? "")}>
                        {String(c.sample ?? "")}
                      </td>
                      <td style={{ textAlign: "right" }}>{formatPercent(d.bytes ? (100 * Number(c.bytes)) / d.bytes : 0)}</td>
                      <td style={{ textAlign: "right" }}>{formatCount(Number(c.lines))}</td>
                      <td className="lg-row-actions">
                        {can("edit") && (
                          <button type="button" className="btn sm" onClick={() => setAct({ service: (c.service as string) ?? null, text: String(c.sample ?? ""), label: where })}>
                            Reduce
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
      </section>

      <section className="panel lg-section">
        <header className="lg-section-head">
          <div>
            <h3 className="panel-title">
              <EyeOff size={15} /> Busy and unread
            </h3>
            <p className="dim">Patterns that make up at least 2% of what was stored, that nobody has opened in 30 days and no alert watches.</p>
          </div>
        </header>
        {d.unread.length === 0 ? (
          <div className="panel-state">Nothing here: every busy pattern is read or watched.</div>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Pattern</th>
                  <th style={{ textAlign: "right" }}>Share</th>
                  <th style={{ textAlign: "right" }}>Lines</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {d.unread.map((u) => (
                  <tr key={u.id}>
                    <td>
                      <button type="button" className="link-btn" onClick={() => onSearch(`pattern:${u.id}`)}>
                        <Template text={u.template} />
                      </button>
                      <div className="dim">
                        {u.service ?? "no service"}
                        {u.code_file && ` · ${u.code_file}${u.code_line ? `:${u.code_line}` : ""}`}
                      </div>
                    </td>
                    <td style={{ textAlign: "right" }}>{formatPercent(100 * u.share)}</td>
                    <td style={{ textAlign: "right" }}>{formatCount(u.lines)}</td>
                    <td className="lg-row-actions">
                      {can("edit") && (
                        <button type="button" className="btn sm" onClick={() => setAct({ service: u.service, text: templateLiteral(u.template), label: u.template })}>
                          Reduce
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {act && <ReduceDialog target={act} onClose={() => setAct(null)} />}
    </div>
  );
}

/** Lower a chatty line or drop it, as a drop rule every SDK and shipper follows. */
function ReduceDialog({ target, onClose }: { target: { service: string | null; text: string; label: string }; onClose: () => void }) {
  const toast = useToast();
  const [contains, setContains] = useState(templateLiteral(target.text).slice(0, 120) || target.text.slice(0, 120));
  const [level, setLevel] = useState("info");
  const [saving, setSaving] = useState(false);
  const save = async () => {
    setSaving(true);
    try {
      await logSettingsApi.addDrop({ service: target.service ?? "", contains, level });
      toast("ok", "Drop rule added. Services follow it within 30 seconds; see Settings → Logs.");
      onClose();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };
  return (
    <Modal
      title="Reduce this line"
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={saving || contains.trim().length < 3} onClick={save}>
            {saving ? "Adding…" : "Add drop rule"}
          </button>
        </>
      }
    >
      <div className="lg-reduce">
        <p className="dim">
          Lines of <span className="mono">{target.service ?? "every service"}</span> that contain the text below are dropped before they are stored, at the levels you pick.
          Change or remove the rule in Settings → Logs.
        </p>
        <label className="field">
          <span>Contains</span>
          <input className="input mono" value={contains} onChange={(e) => setContains(e.target.value)} />
        </label>
        <label className="field">
          <span>Drop</span>
          <Select
            label="Drop at"
            align="left"
            value={level}
            onChange={setLevel}
            options={[
              { value: "debug", label: "Debug and trace lines" },
              { value: "info", label: "Info and below" },
              { value: "warn", label: "Warn and below" },
              { value: "fatal", label: "Every level" },
            ]}
          />
        </label>
      </div>
    </Modal>
  );
}

// ── Hidden failures ───────────────────────────────────────────────────────

export function HiddenTab({ q, onSearch }: { q: string; onSearch: (q: string) => void }) {
  const { resolved, key } = useRun();
  const res = useQuery(`hidden:${key}:${q}`, (signal) => logsApi.hidden(resolved.from, resolved.to, q, signal));
  if (res.error) return <ErrorNotice title="Could not load hidden failures" error={res.error} />;
  const list = res.data?.failures ?? [];
  return (
    <section className="panel lg-section">
      <header className="lg-section-head">
        <div>
          <h3 className="panel-title">
            <AlertTriangle size={15} /> Errors behind successful responses
          </h3>
          <p className="dim">
            Requests that logged an error but answered 2xx or 3xx: something failed and the client was told it worked. Each row is a message and the route it happened on.
          </p>
        </div>
      </header>
      {!res.data ? (
        <div className="panel-state">Loading…</div>
      ) : list.length === 0 ? (
        <div className="panel-state">None in this range: every request that logged an error also answered with one.</div>
      ) : (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Message</th>
                <th>Route</th>
                <th style={{ textAlign: "right" }}>Requests</th>
                <th style={{ textAlign: "right" }}>Lines</th>
                <th>Last</th>
              </tr>
            </thead>
            <tbody>
              {list.map((f, i) => (
                <tr key={i}>
                  <td>
                    <button type="button" className="link-btn" onClick={() => onSearch(f.pattern ? `pattern:${f.pattern} level:error+` : `request:${f.sample_request}`)}>
                      {f.template ? <Template text={f.template} /> : <span className="dim">Not grouped yet</span>}
                    </button>
                  </td>
                  <td className="mono">
                    <span className="method">{f.method}</span> {f.route ?? <span className="dim">no route</span>}
                    <span className="dim"> · {f.status}</span>
                  </td>
                  <td style={{ textAlign: "right" }}>{formatCount(f.requests)}</td>
                  <td style={{ textAlign: "right" }}>{formatCount(f.lines)}</td>
                  <td className="dim">
                    <button type="button" className="link-btn" onClick={() => onSearch(`request:${f.sample_request}`)} title="The lines of the latest such request">
                      {formatRelative(f.last)}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// ── Releases ──────────────────────────────────────────────────────────────

interface VersionRow {
  service: string;
  version: string;
  first_seen: string;
  last_seen: string;
  lines: number;
}

const KIND_LABEL: Record<CompareChange["kind"], string> = { new: "New", gone: "Gone", more: "More often", less: "Less often" };

export function CompareTab({ onSearch }: { onSearch: (q: string) => void }) {
  const { resolved, key } = useRun();
  const versions = useQuery(`log-versions:${key}`, () => api<{ versions: VersionRow[] }>(`/api/applogs/versions?from=${resolved.from.toISOString()}&to=${resolved.to.toISOString()}`));
  const services = useMemo(() => [...new Set((versions.data?.versions ?? []).map((r) => r.service || ""))].filter(Boolean).sort(), [versions.data]);
  const [service, setService] = useState("");
  const svc = service || services[0] || "";
  const ofService = useMemo(
    () => (versions.data?.versions ?? []).filter((r) => (r.service || "") === svc).sort((a, b) => String(b.first_seen ?? "").localeCompare(String(a.first_seen ?? ""))),
    [versions.data, svc],
  );
  const [a, setA] = useState("");
  const [b, setB] = useState("");
  const va = a || ofService[1]?.version || "";
  const vb = b || ofService[0]?.version || "";
  const cmp = useQuery(va && vb && va !== vb ? `log-compare:${key}:${svc}:${va}:${vb}` : null, () => logsApi.compare(resolved.from, resolved.to, va, vb, svc));
  const opts = ofService.map((r) => ({ value: r.version, label: r.version }));
  return (
    <section className="panel lg-section">
      <header className="lg-section-head">
        <div>
          <h3 className="panel-title">
            <Sparkles size={15} /> What a release changed in the logs
          </h3>
          <p className="dim">Messages a release logs that the one before didn't, the ones it stopped logging, and those it logs much more or less often.</p>
        </div>
      </header>
      {versions.error ? (
        <ErrorNotice title="Could not load releases" error={versions.error} />
      ) : !versions.data ? (
        <div className="panel-state">Loading…</div>
      ) : services.length === 0 ? (
        <div className="panel-state">No lines in this range carry a release. Releases come from VISIONARY_RELEASE, the platform's commit variable, or service.version in OpenTelemetry.</div>
      ) : (
        <>
          <div className="filter-row">
            <Select label="Service" align="left" value={svc} onChange={(v) => {
              setService(v);
              setA("");
              setB("");
            }} options={services.map((s) => ({ value: s, label: s }))} />
            <Select label="Before" align="left" value={va} onChange={setA} options={opts.length ? opts : [{ value: "", label: "No release" }]} />
            <span className="dim">→</span>
            <Select label="After" align="left" value={vb} onChange={setB} options={opts.length ? opts : [{ value: "", label: "No release" }]} />
          </div>
          {va === vb || !va || !vb ? (
            <div className="panel-state">Pick two releases of this service.</div>
          ) : cmp.error ? (
            <ErrorNotice title="Could not compare" error={cmp.error} />
          ) : !cmp.data ? (
            <div className="panel-state">Loading…</div>
          ) : cmp.data.changes.length === 0 ? (
            <div className="panel-state">The two releases log the same messages at about the same rates.</div>
          ) : (
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th>Change</th>
                    <th>Message</th>
                    <th style={{ textAlign: "right" }}>{va}</th>
                    <th style={{ textAlign: "right" }}>{vb}</th>
                  </tr>
                </thead>
                <tbody>
                  {cmp.data.changes.map((c) => (
                    <tr key={c.id}>
                      <td>
                        <span className={`pill lg-kind-${c.kind}`}>{KIND_LABEL[c.kind]}</span>
                      </td>
                      <td>
                        <button type="button" className="link-btn" onClick={() => onSearch(`pattern:${c.id}`)}>
                          {c.template ? <Template text={c.template} /> : <span className="mono dim">{c.id}</span>}
                        </button>
                        <div>
                          <LevelBadge level={asLevel(c.level)} />
                        </div>
                      </td>
                      <td style={{ textAlign: "right" }}>
                        {formatCount(c.a)} <span className="dim">({formatPercent(100 * c.share_a)})</span>
                      </td>
                      <td style={{ textAlign: "right" }}>
                        {formatCount(c.b)} <span className="dim">({formatPercent(100 * c.share_b)})</span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  );
}
