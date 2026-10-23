import { useMemo, type ReactNode } from "react";
import { ArrowLeft, Printer } from "lucide-react";
import type { Result } from "../lib/api";
import { formatCount, formatPercent, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { formatFull, formatLogTime } from "../lib/time";
import { auditLabel, kindLabel, rowObjects, sourceLabel, securityApi, useSecurityPanel } from "../lib/security";
import { linkify } from "../components/ErrorNotice";

/** The range in the link (#/security/report?from=&to=), else the picker's. */
function reportRange(fallback: { from: Date; to: Date }): { from: Date; to: Date } {
  const p = new URLSearchParams(window.location.hash.split("?")[1] ?? "");
  const from = new Date(p.get("from") ?? "");
  const to = new Date(p.get("to") ?? "");
  if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime()) || to <= from) return fallback;
  return { from, to };
}

/**
 * A printable security report over one range: executive summary, findings
 * by severity and kind, top IPs and paths, host status and audit actions.
 * "Print / Save as PDF" uses the browser's print dialog; @media print in
 * Security.css hides the app chrome.
 */
export function SecurityReport() {
  const { resolved } = useApp();
  // Read once: the report stays on the range it was opened with.
  const range = useMemo(() => reportRange(resolved), []); // eslint-disable-line react-hooks/exhaustive-deps
  const { from, to } = range;
  const meta = useQuery("sec-report-meta", (signal) => securityApi.meta(signal));
  const summary = useSecurityPanel("summary", from, to);
  const byKind = useSecurityPanel("findings_by_kind", from, to);
  const findingIPs = useSecurityPanel("finding_ips", from, to);
  const topIPs = useSecurityPanel("top_ips", from, to);
  const paths = useSecurityPanel("probed_paths", from, to);
  const hosts = useSecurityPanel("host_status", from, to);
  const admin = meta.data?.is_admin ?? false;
  const audit = useQuery(admin ? `sec-report-audit-${from.toISOString()}-${to.toISOString()}` : null, (signal) => securityApi.audit(from, to, signal));

  const s = rowObjects(summary.data)[0] ?? {};
  const n = (k: string) => toNumber(s[k]) ?? 0;
  const kinds = rowObjects(byKind.data);
  const hostRows = rowObjects(hosts.data);
  const pendingHosts = hostRows.filter((h) => (toNumber(h.pending_updates) ?? 0) > 0);
  const sshTotal = hostRows.reduce((a, h) => a + (toNumber(h.ssh_failures) ?? 0), 0);
  const back = "#/security";
  const loading = summary.loading || byKind.loading || hosts.loading;

  const headline = useMemo(() => {
    if (!summary.data) return [];
    const lines: string[] = [];
    const total = n("findings");
    lines.push(
      total === 0
        ? "No security findings in this period."
        : `${formatCount(total, false)} security finding(s): ${formatCount(n("critical"), false)} critical, ${formatCount(n("warning"), false)} warning and ${formatCount(n("info"), false)} info.`,
    );
    const top = kinds[0];
    if (top) lines.push(`Most frequent: ${kindLabel(String(top.kind))} (${formatCount(toNumber(top.findings) ?? 0, false)} finding(s), ${formatCount(toNumber(top.occurrences) ?? 0, false)} occurrence(s)).`);
    lines.push(`${formatCount(n("failed_logins"), false)} failed login(s); ${formatPercent(toNumber(s.blocked_rate) ?? 0)} of ${formatCount(n("requests"), false)} requests were blocked or forbidden (401, 403, 429).`);
    if (n("scanner_ips") > 0) lines.push(`${formatCount(n("scanner_ips"), false)} IP(s) scanned for probe paths.`);
    if (n("hosts") > 0) {
      lines.push(
        `${formatCount(n("hosts"), false)} host(s) monitored: ${formatCount(sshTotal, false)} failed SSH login(s), ${pendingHosts.length} host(s) with security updates pending.`,
      );
    } else {
      lines.push("No host agent reported in this period, so host security is not covered.");
    }
    return lines;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [summary.data, byKind.data, hosts.data]);

  return (
    <div className="content sec-report">
      <div className="sec-report-bar no-print">
        <a className="btn ghost" href={back}>
          <ArrowLeft size={14} />
          Security
        </a>
        <button type="button" className="btn primary" onClick={() => window.print()} disabled={loading}>
          <Printer size={14} />
          Print / Save as PDF
        </button>
      </div>
      <article className="sec-paper">
        <header className="sec-paper-head">
          <h1>Security report</h1>
          <p>
            {formatFull(from)} to {formatFull(to)}
            <span className="dim"> · generated {formatFull(new Date())}</span>
          </p>
        </header>
        {summary.error && <div className="panel-state error">{linkify(summary.error.message)}</div>}

        <ReportSection title="Executive summary">
          {headline.length === 0 ? <p className="dim">Loading…</p> : <ul className="sec-bullets">{headline.map((l) => <li key={l}>{l}</li>)}</ul>}
        </ReportSection>

        <ReportSection title="Findings by severity">
          <div className="sec-figures">
            <Figure label="Critical" value={n("critical")} tone="critical" />
            <Figure label="Warning" value={n("warning")} tone="warning" />
            <Figure label="Info" value={n("info")} />
            <Figure label="Failed logins" value={n("failed_logins")} />
            <Figure label="Scanner IPs" value={n("scanner_ips")} />
            <Figure label="SSH failures" value={n("ssh_failures")} />
          </div>
          <ReportTable
            result={byKind.data}
            empty="No findings."
            cols={[
              ["kind", "Kind", (v) => kindLabel(String(v))],
              ["source", "Source", (v) => (v ? sourceLabel(String(v)) : "—")],
              ["findings", "Findings", num],
              ["occurrences", "Occurrences", num],
              ["critical", "Critical", num],
              ["warning", "Warning", num],
              ["last_seen", "Last seen", time],
            ]}
          />
        </ReportSection>

        <ReportSection title="Top IPs">
          <h3>Behind the most findings</h3>
          <ReportTable
            result={findingIPs.data}
            empty="No findings with an IP."
            cols={[
              ["ip", "IP", mono],
              ["findings", "Findings", num],
              ["occurrences", "Occurrences", num],
              ["critical", "Critical", num],
              ["kinds", "Kinds", num],
              ["last_seen", "Last seen", time],
            ]}
          />
          <h3>With the most failed logins and probes</h3>
          <ReportTable
            result={topIPs.data}
            empty="No client errors."
            limit={10}
            cols={[
              ["ip", "IP", mono],
              ["requests", "Requests", num],
              ["failed_logins", "Failed logins", num],
              ["unauthorized", "401", num],
              ["forbidden", "403", num],
              ["probes", "Probes", num],
            ]}
          />
        </ReportSection>

        <ReportSection title="Top probed paths">
          <ReportTable
            result={paths.data}
            empty="No probe paths were requested."
            limit={15}
            cols={[
              ["path", "Path", mono],
              ["hits", "Hits", num],
              ["ips", "IPs", num],
              ["answered_2xx", "Answered 2xx", num],
            ]}
          />
        </ReportSection>

        <ReportSection title="Host security">
          {hostRows.length === 0 ? (
            <p className="dim">No host agent reported in this period. Install the Visionary host agent on your VMs to see SSH logins, open ports and pending updates.</p>
          ) : (
            <ReportTable
              result={hosts.data}
              cols={[
                ["host", "Host", mono],
                ["ssh_failures", "SSH failures", num],
                ["ssh_logins", "SSH logins", num],
                ["sudo", "sudo", num],
                ["ports_opened", "Ports opened", num],
                ["pending_updates", "Pending updates", num],
                ["last_event", "Last event", time],
              ]}
            />
          )}
        </ReportSection>

        <ReportSection title="Visionary audit">
          {!admin ? (
            <p className="dim">Only admins can include Visionary's audit log.</p>
          ) : !audit.data ? (
            <p className="dim">{audit.error ? audit.error.message : "Loading…"}</p>
          ) : audit.data.entries.length === 0 ? (
            <p className="dim">No sensitive actions in this period.</p>
          ) : (
            <>
              <p>{audit.data.counts.map((c) => `${auditLabel(c.action)}: ${c.count}`).join(" · ")}</p>
              <table className="data sec-print-table">
                <thead>
                  <tr>
                    <th>Time</th>
                    <th>Action</th>
                    <th>User</th>
                    <th>Target</th>
                    <th>IP</th>
                  </tr>
                </thead>
                <tbody>
                  {audit.data.entries.slice(0, 50).map((e) => (
                    <tr key={e.id}>
                      <td className="mono">{formatLogTime(e.created_at)}</td>
                      <td>{auditLabel(e.action)}</td>
                      <td>{e.user_name || e.user_id || "—"}</td>
                      <td className="mono">{e.target || "—"}</td>
                      <td className="mono">{e.ip || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </>
          )}
        </ReportSection>
      </article>
    </div>
  );
}

function ReportSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="sec-paper-section">
      <h2>{title}</h2>
      {children}
    </section>
  );
}

function Figure({ label, value, tone }: { label: string; value: number; tone?: "critical" | "warning" }) {
  return (
    <div className={`sec-figure ${tone && value > 0 ? tone : ""}`}>
      <div className="sec-figure-value">{formatCount(value, false)}</div>
      <div className="sec-figure-label">{label}</div>
    </div>
  );
}

type Render = (v: unknown) => ReactNode;
const num: Render = (v) => (v == null ? "—" : formatCount(toNumber(v) ?? 0, false));
const mono: Render = (v) => <span className="mono">{v == null ? "—" : String(v)}</span>;
const time: Render = (v) => (v ? <span className="mono">{formatLogTime(String(v))}</span> : "—");

/** A plain table that prints in full (no scrolling box). */
function ReportTable({ result, cols, empty, limit = 20 }: { result: Result | undefined; cols: [string, string, Render?][]; empty?: string; limit?: number }) {
  if (!result) return <p className="dim">Loading…</p>;
  if (result.rows.length === 0) return <p className="dim">{empty ?? "No rows."}</p>;
  const idx = cols.map(([k]) => result.columns.indexOf(k));
  return (
    <table className="data sec-print-table">
      <thead>
        <tr>
          {cols.map(([k, label, render]) => (
            <th key={k} className={render === num ? "num" : undefined}>
              {label}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {result.rows.slice(0, limit).map((row, ri) => (
          <tr key={ri}>
            {cols.map(([k, , render], ci) => {
              const v = idx[ci] >= 0 ? row[idx[ci]] : null;
              return (
                <td key={k} className={render === num ? "num" : undefined}>
                  {render ? render(v) : v == null || v === "" ? "—" : String(v)}
                </td>
              );
            })}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
