import { Gauge } from "lucide-react";
import { useApp } from "../../lib/state";
import { useQuery } from "../../lib/useQuery";
import { alertsApi, describeCondition, type AlertsMeta, type SLOItem } from "../../lib/alerts";
import { formatCount } from "../../lib/format";
import { StateBadge } from "./bits";

const WINDOWS: { key: string; pair: number }[] = [
  { key: "5m", pair: 14.4 },
  { key: "1h", pair: 14.4 },
  { key: "30m", pair: 6 },
  { key: "6h", pair: 6 },
];

export function SLOsTab({ meta, refreshKey }: { meta: AlertsMeta; refreshKey: number }) {
  const { tick } = useApp();
  const slos = useQuery(`slos#${tick}.${refreshKey}`, (signal) => alertsApi.slos(signal));
  const list = slos.data?.slos ?? [];
  if (slos.error) return <div className="notice">{slos.error.message}</div>;
  if (slos.data && list.length === 0) {
    return (
      <div className="empty panel">
        <div className="empty-icon">
          <Gauge size={20} />
        </div>
        <h3>No SLOs yet</h3>
        <p>
          Create a rule of type "SLO burn rate" in Rules, for all traffic, a route or a service. A request is good when it succeeds within {meta.apdex_ms} ms; the
          error budget is what the target leaves over 30 days.
        </p>
      </div>
    );
  }
  return (
    <div className={`al-slos ${slos.loading ? "refetching" : ""}`}>
      {list.map((it) => (
        <SLOCard key={it.rule.id} item={it} meta={meta} />
      ))}
    </div>
  );
}

function SLOCard({ item, meta }: { item: SLOItem; meta: AlertsMeta }) {
  const st = item.status;
  const left = st?.budget_remaining ?? null;
  const pct = left === null ? null : left * 100;
  const tone = pct === null ? "idle" : pct <= 0 ? "critical" : pct < 25 ? "warning" : "good";
  const width = pct === null ? 0 : Math.max(0, Math.min(100, pct));
  const toneLabel = pct === null ? "No traffic" : pct <= 0 ? "Exhausted" : pct < 25 ? "Low" : "Healthy";
  return (
    <section className="panel al-slo">
      <div className="al-slo-head">
        <div>
          <h3>{item.rule.name}</h3>
          <p className="al-hint">{describeCondition(item.rule, meta.metrics)}</p>
        </div>
        <StateBadge state={item.rule.enabled ? item.rule.status.state : "disabled"} />
      </div>
      {item.error && <p className="form-error">{item.error}</p>}
      <div className="al-budget">
        <div className="al-budget-label">
          <span>Error budget remaining</span>
          <strong className="mono">{pct === null ? "—" : `${pct.toFixed(1)}%`}</strong>
          <span className={`al-budget-tone ${tone}`}>{toneLabel}</span>
        </div>
        <div className="al-budget-track" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(width)} aria-label="Error budget remaining">
          <div className={`al-budget-fill ${tone}`} style={{ width: `${width}%` }} />
        </div>
        {st && (
          <p className="al-hint">
            {formatCount(st.bad_30d, false)} bad of {formatCount(st.total_30d, false)} requests in 30 days; the budget allows{" "}
            {formatCount(Math.floor(st.total_30d * (1 - st.target / 100)), false)}.
          </p>
        )}
      </div>
      {st && (
        <table className="data al-burn">
          <thead>
            <tr>
              <th>Window</th>
              <th className="num">Requests</th>
              <th className="num">Burn rate</th>
              <th className="num">Alerts at</th>
            </tr>
          </thead>
          <tbody>
            {WINDOWS.map((w) => {
              const b = st.burn[w.key] ?? 0;
              return (
                <tr key={w.key}>
                  <td>{w.key}</td>
                  <td className="num mono">{formatCount(st.requests[w.key] ?? 0, false)}</td>
                  <td className={`num mono ${b >= w.pair ? "al-hot" : ""}`}>
                    {b.toFixed(2)}×{b >= w.pair && <span className="al-sr"> (above)</span>}
                  </td>
                  <td className="num mono">{w.pair}×</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}
