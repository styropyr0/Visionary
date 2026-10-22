import { useMemo, useState } from "react";
import { Flame, Play } from "lucide-react";
import type { Result } from "../lib/api";
import { formatCount, formatMs, toNumber } from "../lib/format";
import { useInsights, type FlameResponse, type ProfilesResponse } from "../lib/insights";
import { formatLogTime } from "../lib/time";
import { profileNow, profiledServices, useSettings, type ProfileSettings } from "../lib/settings";
import { useQuery } from "../lib/useQuery";
import { useToast } from "../components/Toast";
import { DataTable, type Column } from "../components/DataTable";
import { ErrorNotice } from "../components/ErrorNotice";
import { FlameGraph } from "../components/FlameGraph";
import { InfoTip } from "../components/InfoTip";
import { Panel } from "../components/Panel";
import { Select } from "../components/Select";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import "../components/telemetry/perf.css";
import "./Insights.css";
import { HelpLink } from "../components/Help";

/** The routes column: {"GET /api/x": busy ms}, busiest first. */
function busiest(v: unknown): [string, number][] {
  try {
    const m = JSON.parse(String(v ?? "{}")) as Record<string, number>;
    return Object.entries(m).sort((a, b) => b[1] - a[1]);
  } catch {
    return [];
  }
}

/**
 * Sampled CPU profiles from the SDKs, merged into one flame graph: all of
 * them, the ones taken while a route was busy, or a single one.
 */
export function Profiles() {
  const toast = useToast();
  const [route, setRoute] = useState("");
  const [id, setId] = useState<number | null>(null);
  // "*" is every service; "" is the services that set no name.
  const [service, setServiceState] = useState(() => new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("service") ?? "*");
  const setService = (v: string) => {
    setServiceState(v);
    window.history.replaceState(null, "", v === "*" ? "#/profiles" : `#/profiles?service=${encodeURIComponent(v)}`);
  };
  const only: Record<string, string> = service === "*" ? {} : { service };
  const list = useInsights<ProfilesResponse>("profiles", only);
  const flame = useInsights<FlameResponse>("profiles/flame", { ...only, ...(id !== null ? { id: String(id) } : route ? { route } : {}) });
  const { settings } = useSettings();
  const services = useQuery("profiled-services", profiledServices);
  const serviceOptions = useMemo(
    () => [
      { value: "*", label: "Every service" },
      ...(services.data?.services ?? []).filter((s) => s.profiles > 0).map((s) => ({ value: s.service, label: s.service || "(no name)" })),
    ],
    [services.data],
  );
  const [asking, setAsking] = useState(false);
  const askNow = async () => {
    setAsking(true);
    try {
      await profileNow();
      toast("ok", "Asked for a profile. Refresh in about a minute to see it.");
    } catch (e) {
      toast("err", e instanceof Error ? e.message : String(e));
    } finally {
      setAsking(false);
    }
  };
  const options = useMemo(
    () => [{ value: "", label: "Every profile" }, ...(list.data?.routes ?? []).map((r) => ({ value: r.route, label: `${r.route} · ${formatMs(r.busy_ms)} busy` }))],
    [list.data],
  );
  const shown = list.data ? withoutRoutes(list.data.profiles, route) : undefined;
  const empty = list.data && list.data.profiles.rows.length === 0;

  return (
    <>
      <TopBar
        title="CPU profiles"
        section="API"
        actions={
          <>
            {serviceOptions.length > 2 && (
              <Select label="Service" value={service} options={serviceOptions} onChange={(v) => (setService(v), setRoute(""), setId(null))} maxWidth={220} />
            )}
            {settings?.can_edit && (
              <button type="button" className="btn" disabled={asking} onClick={askNow} title="Take one profile from each profiled service now">
                <Play size={14} />
                Profile now
              </button>
            )}
            <TimeRangePicker />
            <RefreshControl busy={list.loading || flame.loading} />
          </>
        }
      />
      <div className="content">
        {list.error && !list.data && <ErrorNotice title="Could not load profiles" error={list.error} style={{ marginBottom: 12 }} />}
        {settings && <ProfilingStatus p={settings.profile} />}
        <div className="grid">
          {empty ? (
            <section className="panel span-12">
              <div className="panel-state empty-why">
                <div>
                  No CPU profiles in this range.{" "}
                  {settings?.profile.enabled
                    ? `The next one is taken within ${formatMs(settings.profile.interval_s * 1000)} while requests are being served.`
                    : "Profiling is off."}
                </div>
                <HelpLink topic="profile">How CPU profiles work</HelpLink>
              </div>
            </section>
          ) : (
            <>
              {/* Not a Panel: a flame graph has no query result to inspect. */}
              <section className="panel span-12" aria-busy={flame.loading || undefined}>
                {flame.loading && <div className="progress" aria-hidden />}
                <header className="panel-head">
                  <h3 className="panel-title">{id !== null ? `Profile #${id}` : route ? `While ${route} was busy` : "Every profile in the range"}</h3>
                  <InfoTip text="Each frame is as wide as the CPU samples taken in it and the functions it called; callers above, callees below. Click a frame to zoom in. The table below the graph lists the functions that used the most CPU themselves, which is usually where to look first." />
                  <div className="panel-actions">
                    {id !== null && (
                      <button type="button" className="btn sm" onClick={() => setId(null)}>
                        Back to all
                      </button>
                    )}
                    <Select label="Profiles" value={route} options={options} onChange={(v) => (setRoute(v), setId(null))} maxWidth={360} />
                  </div>
                </header>
                <div className="panel-body" style={{ minHeight: 260 }}>
                  {flame.error && !flame.data ? (
                    <div className="panel-state error">{flame.error.message}</div>
                  ) : !flame.data ? null : flame.data.profiles === 0 ? (
                    <div className="panel-state">No profile was taken while this route was busy.</div>
                  ) : (
                    <div className="prof-flame">
                      <div className="dim prof-note">
                        {formatCount(flame.data.root.value)} samples from {formatCount(flame.data.profiles)} {flame.data.profiles === 1 ? "profile" : "profiles"}
                      </div>
                      <FlameGraph key={`${id}-${route}`} root={flame.data.root} />
                    </div>
                  )}
                </div>
              </section>
              <Panel
                className="span-12"
                title="Profiles"
                description="Each profile, with how long its routes' requests took while it ran. Open one to see only its flame graph."
                result={shown}
                loading={list.loading}
                bodyHeight={160}
                table={
                  shown && (
                    <DataTable
                      result={shown}
                      columns={profileColumns}
                      maxHeight={360}
                      onRowClick={(row) => setId(toNumber(row[0]))}
                      rowKey={(row) => row[0]}
                      selectedKey={id ?? undefined}
                    />
                  )
                }
              />
            </>
          )}
        </div>
      </div>
    </>
  );
}

/** Whether the SDKs are taking profiles, from Settings → Profiling. */
function ProfilingStatus({ p }: { p: ProfileSettings }) {
  const who = p.services.length === 0 ? "every service" : p.services.join(", ");
  return (
    <div className={`notice prof-status ${p.enabled ? "on" : ""}`}>
      <Flame size={15} />
      <span>
        {p.enabled ? (
          <>
            Profiling is on for <strong>{who}</strong>: {formatMs(p.duration_ms)} of samples every {formatMs(p.interval_s * 1000)} while requests are being served.
          </>
        ) : (
          <>Profiling is off. Turn it on to sample where your services spend CPU; use Profile now for a one-off.</>
        )}
      </span>
      <a className="btn sm ghost" href="#/settings?tab=profiling">
        {p.enabled ? "Change" : "Turn on"}
      </a>
    </div>
  );
}

/** The profiles a route filter keeps. */
function withoutRoutes(res: Result, route: string): Result {
  if (!route) return res;
  const ri = res.columns.indexOf("routes");
  return { ...res, rows: res.rows.filter((r) => busiest(r[ri]).some(([k]) => k === route)) };
}

const profileColumns: Column[] = [
  { key: "created_at", label: "Taken", render: (v) => <span className="mono dim">{formatLogTime(String(v))}</span>, width: 150 },
  { key: "service", render: (v, row) => <span>{v ? String(v) : <span className="dim">—</span>} <span className="dim">{String(row[3] ?? "")}</span></span>, width: 200 },
  { key: "version", render: (v) => (v ? <span className="mono">{String(v).slice(0, 12)}</span> : <span className="dim">—</span>), width: 110 },
  { key: "duration_ms", label: "Length", align: "right", render: (v) => formatMs(toNumber(v) ?? 0), width: 80 },
  { key: "samples", align: "right", unit: "count", width: 80 },
  {
    key: "routes",
    label: "Busiest routes",
    render: (v) => {
      const top = busiest(v).slice(0, 3);
      return top.length === 0 ? (
        <span className="dim">—</span>
      ) : (
        <span className="span-routes">
          {top.map(([r, ms]) => (
            <span key={r} className="mono">
              {r} <span className="dim">{formatMs(ms)}</span>
            </span>
          ))}
        </span>
      );
    },
  },
];
