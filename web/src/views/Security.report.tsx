import { useEffect, useMemo, type ReactNode } from "react";
import { AlertTriangle, ArrowLeft, CheckCircle2, FileDown, Printer, ShieldAlert } from "lucide-react";
import { api, rangeQuery, type Meta, type Result } from "../lib/api";
import { formatCount, formatPercent, toNumber } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import { dateFormat, displayZone, formatFull, formatLogTime } from "../lib/time";
import { auditLabel, kindLabel, rowObjects, sourceLabel, securityApi, useSecurityPanel } from "../lib/security";
import { linkify } from "../components/ErrorNotice";
import { BrandMark } from "../components/Sidebar";

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
  const app = useQuery("meta", (signal) => api<Meta>("/api/meta", { signal }));
  const appName = app.data?.app_name && app.data.app_name !== "the application" ? app.data.app_name : "";
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
  const answered = rowObjects(paths.data).filter((p) => (toNumber(p.answered_2xx) ?? 0) > 0);
  const topScanner = rowObjects(topIPs.data).sort((a, b) => (toNumber(b.probes) ?? 0) - (toNumber(a.probes) ?? 0))[0];

  // The saved PDF is named after the page title.
  useEffect(() => {
    const before = document.title;
    const day = dateFormat({ year: "numeric", month: "short", day: "numeric" });
    document.title = `Security report${appName ? ` – ${appName}` : ""} – ${day.format(from)} to ${day.format(to)}`;
    return () => {
      document.title = before;
    };
  }, [appName, from, to]);

  const posture: Posture | null = !summary.data
    ? null
    : n("critical") > 0 || answered.length > 0
      ? { tone: "critical", title: "Action needed", text: n("critical") > 0 ? `${formatCount(n("critical"), false)} critical finding(s) in this period.` : "Probe paths were answered with a success response." }
      : n("warning") > 0 || pendingHosts.length > 0
        ? { tone: "warning", title: "Worth a look", text: "No critical findings, but there are warnings or pending security updates." }
        : { tone: "ok", title: "All clear", text: "No critical or warning findings in this period." };

  // What to do next, most urgent first, from the same numbers as the report.
  const actions = useMemo(() => {
    if (!summary.data) return [];
    const list: string[] = [];
    if (n("critical") > 0) list.push(`Review the ${formatCount(n("critical"), false)} critical finding(s) under Security → Findings and mark each one handled or a false positive.`);
    if (answered.length > 0) {
      const top = answered.slice(0, 3).map((p) => String(p.path)).join(", ");
      list.push(`${answered.length} probed path(s) returned a success response (${top}). Make sure none of them expose files, admin pages or configuration.`);
    }
    if (pendingHosts.length > 0) list.push(`Apply security updates on ${pendingHosts.slice(0, 5).map((h) => String(h.host)).join(", ")}${pendingHosts.length > 5 ? " and others" : ""}.`);
    if (sshTotal > 0) list.push(`${formatCount(sshTotal, false)} failed SSH login(s): allow key-only SSH and limit who can reach port 22.`);
    if (topScanner && (toNumber(topScanner.probes) ?? 0) > 0) list.push(`Consider blocking ${String(topScanner.ip)}, behind ${formatCount(toNumber(topScanner.probes) ?? 0, false)} probe(s).`);
    if (n("failed_logins") > 50) list.push(`${formatCount(n("failed_logins"), false)} failed logins: check that sign-in routes are rate limited.`);
    if (n("hosts") === 0) list.push("Install the Visionary host agent on your servers to cover SSH, open ports and pending updates.");
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [summary.data, paths.data, hosts.data, topIPs.data]);

  const stamp = dateFormat({ year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  const days = Math.max(1, Math.round((to.getTime() - from.getTime()) / 86_400_000));

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
        <div className="sec-report-actions">
          <button type="button" className="btn" onClick={() => window.print()} disabled={loading}>
            <Printer size={14} />
            Print
          </button>
          {/* The server lays the same report out as an A4 PDF, in the viewer's time zone. */}
          <a className="btn primary" href={`/api/security/report.pdf?${rangeQuery(from, to, { tz: displayZone() })}`} download>
            <FileDown size={14} />
            Download PDF
          </a>
        </div>
      </div>
      <article className="sec-paper">
        <header className="sec-paper-head">
          <div className="sec-paper-brand">
            <BrandMark />
            <span>Visionary</span>
            {appName && <span className="sec-paper-app">{appName}</span>}
          </div>
          <h1>Security report</h1>
          <dl className="sec-paper-meta">
            <div>
              <dt>Period</dt>
              <dd>
                {stamp.format(from)} – {stamp.format(to)}{" "}
                <span className="dim">
                  ({days === 1 ? "1 day" : `${days} days`}, {displayZone()})
                </span>
              </dd>
            </div>
            <div>
              <dt>Generated</dt>
              <dd>{formatFull(new Date())}</dd>
            </div>
          </dl>
        </header>
        {summary.error && <div className="panel-state error">{linkify(summary.error.message)}</div>}

        {posture && <PostureBox posture={posture} />}

        <ReportSection title="Executive summary">
          {headline.length === 0 ? <p className="dim">Loading…</p> : <ul className="sec-bullets">{headline.map((l) => <li key={l}>{l}</li>)}</ul>}
        </ReportSection>

        {actions.length > 0 && (
          <ReportSection title="Recommended actions">
            <ol className="sec-steps">
              {actions.map((a) => (
                <li key={a}>{a}</li>
              ))}
            </ol>
          </ReportSection>
        )}

        <ReportSection title="Findings by severity">
          <div className="sec-figures">
            <Figure label="Critical" value={n("critical")} tone="critical" />
            <Figure label="Warning" value={n("warning")} tone="warning" />
            <Figure label="Info" value={n("info")} />
            <Figure label="Failed logins" value={n("failed_logins")} />
            <Figure label="Scanner IPs" value={n("scanner_ips")} />
            <Figure label="SSH failures" value={n("ssh_failures")} />
          </div>
          <SeverityBar critical={n("critical")} warning={n("warning")} info={n("info")} />
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

        <footer className="sec-paper-foot">
          Findings come from Visionary's detectors over request logs{n("hosts") > 0 ? " and the host agent" : ""}, over the period above. Tables show the top rows only. This report contains IP addresses and account names: share it on a need-to-know basis.
        </footer>
      </article>
    </div>
  );
}

interface Posture {
  tone: "critical" | "warning" | "ok";
  title: string;
  text: string;
}

const POSTURE_ICON = { critical: ShieldAlert, warning: AlertTriangle, ok: CheckCircle2 };

function PostureBox({ posture }: { posture: Posture }) {
  const Icon = POSTURE_ICON[posture.tone];
  return (
    <div className={`sec-posture ${posture.tone}`}>
      <Icon size={20} aria-hidden />
      <div>
        <div className="sec-posture-title">{posture.title}</div>
        <div className="sec-posture-text">{posture.text}</div>
      </div>
    </div>
  );
}

/** Findings split by severity in one bar, with the counts under it. */
function SeverityBar({ critical, warning, info }: { critical: number; warning: number; info: number }) {
  const total = critical + warning + info;
  if (total === 0) return null;
  const parts = [
    { key: "critical", label: "Critical", value: critical },
    { key: "warning", label: "Warning", value: warning },
    { key: "info", label: "Info", value: info },
  ].filter((p) => p.value > 0);
  return (
    <div className="sec-sevbar-wrap">
      <div className="sec-sevbar" role="img" aria-label={parts.map((p) => `${p.label} ${p.value}`).join(", ")}>
        {parts.map((p) => (
          <span key={p.key} className={p.key} style={{ flexGrow: p.value }} />
        ))}
      </div>
      <div className="sec-sevbar-legend">
        {parts.map((p) => (
          <span key={p.key}>
            <i className={p.key} aria-hidden />
            {p.label} {formatCount(p.value, false)} ({formatPercent((100 * p.value) / total)})
          </span>
        ))}
      </div>
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
