import { useEffect, useState } from "react";
import { AlertTriangle } from "lucide-react";
import type { Meta } from "../lib/api";
import { load, save, useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { alertsApi } from "../lib/alerts";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { ActivityTab } from "../components/alerts/ActivityTab";
import { ChannelsTab } from "../components/alerts/ChannelsTab";
import { DigestTab } from "../components/alerts/DigestTab";
import { RulesTab } from "../components/alerts/RulesTab";
import { SLOsTab } from "../components/alerts/SLOsTab";
import "./Alerts.css";

type Tab = "rules" | "activity" | "channels" | "slos" | "digest";
const TABS: { key: Tab; label: string }[] = [
  { key: "rules", label: "Rules" },
  { key: "activity", label: "Activity" },
  { key: "channels", label: "Channels" },
  { key: "slos", label: "SLOs" },
  { key: "digest", label: "Digest" },
];

export function Alerts({ meta }: { meta?: Meta }) {
  void meta;
  const { tick } = useApp();
  const [tab, setTabState] = useState<Tab>(() => {
    const t = load<Tab>("va.alerts.tab", "rules");
    return TABS.some((x) => x.key === t) ? t : "rules";
  });
  const setTab = (t: Tab) => {
    setTabState(t);
    save("va.alerts.tab", t);
  };
  const [version, setVersion] = useState(0);
  const changed = () => setVersion((v) => v + 1);

  // Rule states change on the server every interval; keep the list current.
  useEffect(() => {
    const id = window.setInterval(() => document.visibilityState === "visible" && setVersion((v) => v + 1), 20_000);
    return () => window.clearInterval(id);
  }, []);

  const am = useQuery(`alerts-meta#${tick}`, (signal) => alertsApi.meta(signal));
  const rules = useQuery(`alerts-rules#${tick}.${version}`, (signal) => alertsApi.rules(signal));
  const channels = useQuery(`alerts-channels#${tick}.${version}`, (signal) => alertsApi.channels(signal));
  const firing = (rules.data?.rules ?? []).filter((r) => r.enabled && r.status.state === "firing").length;
  const error = am.error ?? rules.error ?? channels.error;

  return (
    <>
      <TopBar
        title="Alerts"
        section="Manage"
        actions={
          <>
            {tab === "activity" && <TimeRangePicker />}
            <RefreshControl busy={rules.loading} />
          </>
        }
      />
      <div className="content al-page">
        <nav className="tabs" role="tablist" aria-label="Alerts">
          {TABS.map((t) => (
            <button key={t.key} role="tab" aria-selected={tab === t.key} className={tab === t.key ? "on" : ""} onClick={() => setTab(t.key)}>
              {t.label}
              {t.key === "rules" && firing > 0 && <span className="al-tab-count" aria-label={`${firing} firing`}>{firing}</span>}
            </button>
          ))}
        </nav>
        {error && !am.data ? (
          <div className="notice">
            <AlertTriangle size={16} />
            <span>{error.message}</span>
          </div>
        ) : !am.data || !rules.data || !channels.data ? (
          <div className="panel-state" style={{ height: 240 }} />
        ) : (
          <>
            {tab === "rules" && <RulesTab meta={am.data} rules={rules.data.rules} channels={channels.data.channels} loading={rules.loading} onChange={changed} />}
            {tab === "activity" && <ActivityTab rules={rules.data.rules} refreshKey={version} />}
            {tab === "channels" && <ChannelsTab meta={am.data} channels={channels.data.channels} onChange={changed} />}
            {tab === "slos" && <SLOsTab meta={am.data} refreshKey={0} />}
            {tab === "digest" && <DigestTab meta={am.data} channels={channels.data.channels} onChange={changed} />}
          </>
        )}
      </div>
    </>
  );
}
