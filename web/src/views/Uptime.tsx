import { useEffect, useMemo, useState } from "react";
import { Activity, ExternalLink, Pause, Pencil, Play, Plus, RotateCw } from "lucide-react";
import { StatTile } from "../components/StatTile";
import { useToast } from "../components/Toast";
import { RefreshControl, TopBar } from "../components/Toolbar";
import { formatMs } from "../lib/format";
import { useApp } from "../lib/state";
import { formatRelative } from "../lib/time";
import { formatInterval, formatUptime, listChecks, runCheck, setEnabled, type CheckList, type UptimeCheck } from "../lib/uptime";
import { useQuery } from "../lib/useQuery";
import { UptimeDetail } from "./Uptime.detail";
import { CheckDrawer } from "./Uptime.form";
import { BarsLegend, DayBars, Spark, StateBadge } from "./Uptime.parts";
import { ErrorNotice } from "../components/ErrorNotice";
import "./Uptime.css";

/** The selected check lives in the hash (#/uptime?check=3) so detail views can be linked. */
function readCheckParam(): number | null {
  const q = window.location.hash.split("?")[1] ?? "";
  const n = Number(new URLSearchParams(q).get("check"));
  return Number.isInteger(n) && n > 0 ? n : null;
}

export function openCheck(id: number | null) {
  window.location.hash = id ? `#/uptime?check=${id}` : "#/uptime";
}

export function Uptime() {
  const [selected, setSelected] = useState(readCheckParam);
  useEffect(() => {
    const onHash = () => setSelected(readCheckParam());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return selected ? <UptimeDetail key={selected} id={selected} /> : <UptimeList />;
}

function UptimeList() {
  const { tick } = useApp();
  const toast = useToast();
  const [version, setVersion] = useState(0);
  const list = useQuery(`uptime-list#${tick}#${version}`, (signal) => listChecks(signal));
  const [editing, setEditing] = useState<UptimeCheck | "new" | null>(null);
  const [running, setRunning] = useState<number | null>(null);
  const reload = () => setVersion((v) => v + 1);

  const run = async (c: UptimeCheck) => {
    setRunning(c.id);
    try {
      const { result } = await runCheck(c.id);
      toast(result.ok ? "ok" : "err", result.ok ? `${c.name}: up, ${formatMs(result.duration_ms)}` : `${c.name}: ${result.error ?? "failed"}`);
      reload();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setRunning(null);
    }
  };

  const toggle = async (c: UptimeCheck) => {
    try {
      await setEnabled(c.id, !c.enabled);
      toast("ok", c.enabled ? `Paused “${c.name}”` : `Resumed “${c.name}”`);
      reload();
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    }
  };

  const data = list.data;
  const canEdit = data?.can_edit ?? false;
  return (
    <>
      <TopBar
        title="Uptime"
        section="Monitor"
        actions={
          <>
            <a className="btn ghost" href="/status" target="_blank" rel="noreferrer" title="Public status page (needs VISIONARY_STATUS_PAGE=true)">
              <ExternalLink size={14} />
              Status page
            </a>
            <RefreshControl busy={list.loading} />
            {canEdit && (
              <button className="btn primary" onClick={() => setEditing("new")}>
                <Plus size={14} />
                New check
              </button>
            )}
          </>
        }
      />
      <div className="content">
        {list.error && !data && <ErrorNotice title="Could not load checks" error={list.error} />}
        {data && data.checks.length === 0 && (
          <section className="panel">
            <div className="empty">
              <div className="empty-icon">
                <Activity size={20} />
              </div>
              <h3>No uptime checks yet</h3>
              <p>
                A check requests a URL on a schedule from this server and records whether it answered as expected, and how fast. Results feed this
                view, alerts, the analyst and the public status page.
              </p>
              {canEdit && (
                <button className="btn primary" onClick={() => setEditing("new")}>
                  <Plus size={14} />
                  Add a check
                </button>
              )}
            </div>
          </section>
        )}
        {data && data.checks.length > 0 && (
          <div className="grid">
            <Tiles data={data} loading={list.loading} />
            <section className="panel span-12" aria-busy={list.loading || undefined}>
              {list.loading && <div className="progress" aria-hidden />}
              <header className="panel-head">
                <h3 className="panel-title">Checks</h3>
                <div className="panel-actions">
                  <BarsLegend />
                </div>
              </header>
              <div className="table-wrap">
                <table className="data up-table">
                  <thead>
                    <tr>
                      <th style={{ width: 110 }}>State</th>
                      <th>Check</th>
                      <th style={{ width: "32%" }}>Last 90 days</th>
                      <th className="num" style={{ width: 96 }}>
                        Uptime 24h
                      </th>
                      <th style={{ width: 196 }}>Response time 24h</th>
                      <th style={{ width: 96 }}>Last check</th>
                      <th style={{ width: 118 }} aria-label="Actions" />
                    </tr>
                  </thead>
                  <tbody>
                    {data.checks.map(({ check: c, state: s }) => (
                      <tr key={c.id} className="clickable" onClick={() => openCheck(c.id)}>
                        <td>
                          <StateBadge state={s.state} />
                        </td>
                        <td className="up-name">
                          <a href={`#/uptime?check=${c.id}`} onClick={(e) => e.stopPropagation()}>
                            {c.name}
                          </a>
                          <span className="mono dim" title={c.url}>
                            {c.method} {c.url}
                          </span>
                        </td>
                        <td>
                          <DayBars days={s.days} />
                        </td>
                        <td className="num">{formatUptime(s.uptime_24h)}</td>
                        <td>
                          <div className="up-spark-cell">
                            <Spark points={s.spark} />
                            <span className="num">{s.last_at && s.state !== "down" ? formatMs(s.last_duration_ms) : ""}</span>
                          </div>
                        </td>
                        <td className="dim" title={s.last_at}>
                          {s.last_at ? formatRelative(s.last_at) : "never"}
                          <div className="up-sub">every {formatInterval(c.interval_s)}</div>
                        </td>
                        <td className="num" onClick={(e) => e.stopPropagation()}>
                          {canEdit && (
                            <div className="up-actions">
                              <button className="icon-btn" title="Run now" aria-label={`Run ${c.name} now`} disabled={running === c.id} onClick={() => void run(c)}>
                                <RotateCw size={14} className={running === c.id ? "spin" : undefined} />
                              </button>
                              <button className="icon-btn" title={c.enabled ? "Pause" : "Resume"} aria-label={c.enabled ? `Pause ${c.name}` : `Resume ${c.name}`} onClick={() => void toggle(c)}>
                                {c.enabled ? <Pause size={14} /> : <Play size={14} />}
                              </button>
                              <button className="icon-btn" title="Edit" aria-label={`Edit ${c.name}`} onClick={() => setEditing(c)}>
                                <Pencil size={14} />
                              </button>
                            </div>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          </div>
        )}
      </div>
      {editing && (
        <CheckDrawer
          check={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
          }}
          onDeleted={() => {
            setEditing(null);
            reload();
          }}
        />
      )}
    </>
  );
}

function Tiles({ data, loading }: { data: CheckList; loading: boolean }) {
  const t = useMemo(() => {
    const states = data.checks.map((c) => c.state);
    const count = (s: string) => states.filter((x) => x.state === s).length;
    const live = states.filter((s) => s.uptime_24h !== null);
    const avg = live.length ? live.reduce((a, s) => a + (s.uptime_24h ?? 0), 0) / live.length : null;
    const ms = states.filter((s) => s.last_at && (s.state === "up" || s.state === "degraded")).map((s) => s.last_duration_ms);
    return {
      up: count("up") + count("degraded"),
      down: count("down"),
      degraded: count("degraded"),
      paused: count("paused"),
      monitored: states.length - count("paused"),
      avg,
      slowest: ms.length ? Math.max(...ms) : null,
    };
  }, [data]);
  return (
    <>
      <div className="span-3 up-tile">
        <StatTile label="Checks up" value={`${t.up} / ${t.monitored}`} delta={null} deltaLabel="" upIs="neutral" loading={loading} footer={`${t.down} down · ${t.degraded} degraded · ${t.paused} paused`} />
      </div>
      <div className="span-3 up-tile">
        <StatTile label="Open incidents" description="Checks failing right now." value={String(t.down)} delta={null} deltaLabel="" upIs="bad" loading={loading} footer={t.down ? "Open a check for details" : "Nothing is down"} />
      </div>
      <div className="span-3 up-tile">
        <StatTile label="Average uptime (24h)" description="Mean of each check's uptime over the last 24 hours." value={formatUptime(t.avg)} delta={null} deltaLabel="" upIs="good" loading={loading} footer={`${t.monitored} checks monitored`} />
      </div>
      <div className="span-3 up-tile">
        <StatTile label="Slowest latest response" description="The slowest most recent response among checks that are up." value={t.slowest === null ? "—" : formatMs(t.slowest)} delta={null} deltaLabel="" upIs="bad" loading={loading} footer="Among checks that are up" />
      </div>
    </>
  );
}
