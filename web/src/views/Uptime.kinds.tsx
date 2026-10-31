import { AlertTriangle, Globe, HeartPulse, Network, Plug, Radar, ShieldCheck, Signpost, Workflow, X, Plus, type LucideIcon } from "lucide-react";
import { Select } from "../components/Select";
import { formatMs } from "../lib/format";
import { useApp } from "../lib/state";
import { useQuery } from "../lib/useQuery";
import type { CheckInput, CheckKind, UptimeCheck } from "../lib/uptime";
import { formatInterval } from "../lib/uptime";
import { getPingStatus, type DnsConfig, type DnsType, type HeartbeatConfig, type PingConfig, type SslConfig, type TcpConfig } from "../lib/uptimeKinds";

export const KIND_INFO: Record<CheckKind, { label: string; icon: LucideIcon; text: string }> = {
  http: { label: "HTTP", icon: Network, text: "One request to a URL, checked for status, body text and response time." },
  api: { label: "API steps", icon: Workflow, text: "Several requests in order that share values: sign in, then call with the token. Assert on status, JSON, headers and time." },
  browser: { label: "Browser", icon: Globe, text: "Opens your pages in a headless browser and plays steps: sign in, click through, check what is shown." },
  tcp: { label: "TCP port", icon: Plug, text: "A port accepts connections, such as a database, mail or SSH server; optionally checks what it answers." },
  dns: { label: "DNS", icon: Signpost, text: "A name resolves (A, AAAA, CNAME, MX, TXT, NS), optionally to the values you expect." },
  ping: { label: "Ping", icon: Radar, text: "ICMP echo to a host, with packet loss and round trip time." },
  ssl: { label: "Certificate", icon: ShieldCheck, text: "A TLS certificate is valid, trusted, names the host and is not about to expire. No HTTP request." },
  heartbeat: { label: "Heartbeat", icon: HeartPulse, text: "Your cron job, backup or worker calls a secret URL when it runs; down when a ping is late or reports a failure." },
};

export const KIND_ORDER: CheckKind[] = ["http", "api", "browser", "tcp", "dns", "ping", "ssl", "heartbeat"];

/** Kinds whose settings live in their own field of the check. */
export const SETTINGS_KINDS: CheckKind[] = ["api", "tcp", "dns", "ping", "ssl", "heartbeat"];

export function KindIcon({ kind, size = 14 }: { kind: CheckKind; size?: number }) {
  const Icon = (KIND_INFO[kind] ?? KIND_INFO.http).icon;
  return <Icon size={size} aria-label={KIND_INFO[kind]?.label} className="up-kind-icon" />;
}

/** The kind chooser of a new check, with what each kind does. */
export function KindPicker({ value, onChange }: { value: CheckKind; onChange: (k: CheckKind) => void }) {
  return (
    <div className="field">
      <span>Kind</span>
      <div className="up-kinds" role="radiogroup" aria-label="Kind of check">
        {KIND_ORDER.map((k) => {
          const info = KIND_INFO[k];
          const Icon = info.icon;
          return (
            <button key={k} type="button" role="radio" aria-checked={value === k} className={`up-kind ${value === k ? "on" : ""}`} onClick={() => onChange(k)} title={info.text}>
              <Icon size={15} aria-hidden />
              <span>{info.label}</span>
            </button>
          );
        })}
      </div>
      <small>{KIND_INFO[value].text}</small>
    </div>
  );
}

/** Defaults a new check of each kind starts from. */
export function kindDefaults(k: CheckKind): Partial<CheckInput> {
  switch (k) {
    case "api":
      return { interval_s: 300, timeout_ms: 30000, api: { requests: [{ method: "GET", url: "https://", assert: [{ source: "status", op: "in", value: "200-299" }] }] } };
    case "tcp":
      return { interval_s: 60, timeout_ms: 10000, tcp: { host: "", port: 443 } };
    case "dns":
      return { interval_s: 300, timeout_ms: 10000, dns: { name: "", record_type: "A", match: "" } };
    case "ping":
      return { interval_s: 60, timeout_ms: 2000, ping: { host: "", packets: 4, max_loss_pct: 50 } };
    case "ssl":
      return { interval_s: 3600, timeout_ms: 10000, ssl: { host: "", port: 443, warn_days: 14, fail_days: 3 } };
    case "heartbeat":
      return { interval_s: 60, timeout_ms: 1000, heartbeat: { period_s: 86400, grace_s: 1800 } };
  }
  return {};
}

/** One line naming what a check watches, for the list and the detail header. */
export function describeTarget(c: UptimeCheck): string {
  switch (c.kind) {
    case "browser":
      return `Browser · ${c.steps?.length ?? 0} steps · ${c.url}`;
    case "api": {
      const n = c.api?.requests.length ?? 0;
      return `API · ${n} ${n === 1 ? "request" : "requests"} · ${c.url}`;
    }
    case "tcp":
      return `TCP ${hostPort(c.tcp?.host ?? "", c.tcp?.port ?? 0)}`;
    case "dns":
      return `DNS ${c.dns?.record_type ?? "A"} ${c.dns?.name ?? ""}${c.dns?.server ? ` @ ${c.dns.server}` : ""}`;
    case "ping":
      return `Ping ${c.ping?.host ?? ""}`;
    case "ssl":
      return `Certificate ${hostPort(c.ssl?.host ?? "", c.ssl?.port ?? 443)}`;
    case "heartbeat":
      return `Heartbeat · ${heartbeatSchedule(c.heartbeat)}`;
  }
  return `${c.method} ${c.url}`;
}

/** What the check expects, for the detail header's second line. */
export function describeExpect(c: UptimeCheck): string {
  switch (c.kind) {
    case "browser":
      return ` · ${c.steps?.length ?? 0} steps`;
    case "http":
      return ` · expects ${c.expect_status}${c.body_contains ? ` and “${c.body_contains}”` : ""}`;
    case "api":
      return ` · ${c.api?.requests.reduce((n, r) => n + (r.assert?.length ?? 0), 0) ?? 0} assertions`;
    case "tcp":
      return c.tcp?.expect ? ` · expects “${c.tcp.expect}”` : "";
    case "dns":
      return c.dns?.match ? ` · ${c.dns.match} ${c.dns.values?.join(", ")}` : "";
    case "ping":
      return ` · ${c.ping?.packets ?? 4} packets, fails above ${c.ping?.max_loss_pct ?? 0}% loss`;
    case "ssl":
      return ` · warns under ${c.ssl?.warn_days} days, fails under ${c.ssl?.fail_days} days${c.ssl?.min_tls ? ` · TLS ${c.ssl.min_tls}+` : ""}`;
  }
  return "";
}

function hostPort(h: string, p: number) {
  return `${h.includes(":") ? `[${h}]` : h}:${p}`;
}

export function heartbeatSchedule(h?: HeartbeatConfig): string {
  if (!h) return "";
  const grace = h.grace_s ? `, ${formatInterval(h.grace_s)} grace` : "";
  if (h.cron) return `cron ${h.cron}${h.timezone && h.timezone !== "UTC" ? ` (${h.timezone})` : " (UTC)"}${grace}`;
  return `every ${formatInterval(h.period_s ?? 0)}${grace}`;
}

/** Says when ping checks cannot run on this server. */
export function PingNotice({ always }: { always?: boolean }) {
  const { tick } = useApp();
  const st = useQuery(`uptime-ping#${tick}`, (signal) => getPingStatus(signal));
  const p = st.data;
  if (!p || (p.available && !always)) return null;
  if (p.available) {
    return (
      <div className="notice up-browser-ok" role="status">
        <Radar size={16} aria-hidden />
        <span>Pings with {p.method === "windows" ? "the Windows ICMP helper" : `an ICMP ${p.method}`}, no extra privileges needed.</span>
      </div>
    );
  }
  return (
    <div className="notice up-coverage span-12" role="status">
      <AlertTriangle size={16} aria-hidden />
      <div className="up-coverage-text">
        <strong>Ping checks are paused: {p.error}</strong>
        <span className="dim">{p.help}</span>
      </div>
    </div>
  );
}

const num = (v: string) => (v === "" ? 0 : Number(v));

export function TcpFields({ value, onChange }: { value: TcpConfig; onChange: (v: TcpConfig) => void }) {
  const set = (p: Partial<TcpConfig>) => onChange({ ...value, ...p });
  return (
    <>
      <div className="up-row">
        <label className="field" style={{ flex: 1 }}>
          <span>Host</span>
          <input className="input mono" value={value.host} onChange={(e) => set({ host: e.target.value })} placeholder="db.example.com" required />
        </label>
        <label className="field" style={{ width: 110 }}>
          <span>Port</span>
          <input className="input" type="number" min={1} max={65535} value={value.port || ""} onChange={(e) => set({ port: num(e.target.value) })} required />
        </label>
      </div>
      <div className="up-row">
        <label className="field" style={{ flex: 1 }}>
          <span>Send after connecting</span>
          <input className="input mono" value={value.send ?? ""} onChange={(e) => set({ send: e.target.value })} placeholder="Optional, e.g. PING\r\n" maxLength={1024} />
        </label>
        <label className="field" style={{ flex: 1 }}>
          <span>Reply contains</span>
          <input className="input mono" value={value.expect ?? ""} onChange={(e) => set({ expect: e.target.value })} placeholder="Optional, e.g. SSH-2.0" maxLength={512} />
        </label>
      </div>
      <small className="up-hint">Without either, a completed connection passes. Write \r, \n and \t for control characters. The first 4 KB of the reply are searched.</small>
    </>
  );
}

const DNS_TYPES: DnsType[] = ["A", "AAAA", "CNAME", "MX", "TXT", "NS"];

export function DnsFields({ value, onChange }: { value: DnsConfig; onChange: (v: DnsConfig) => void }) {
  const set = (p: Partial<DnsConfig>) => onChange({ ...value, ...p });
  return (
    <>
      <div className="up-row">
        <label className="field" style={{ flex: 1 }}>
          <span>Name</span>
          <input className="input mono" value={value.name} onChange={(e) => set({ name: e.target.value })} placeholder="example.com" required />
        </label>
        <div className="field" style={{ width: 120 }}>
          <span>Record</span>
          <Select label="Record type" value={value.record_type} align="left" onChange={(v) => set({ record_type: v as DnsType })} options={DNS_TYPES.map((t) => ({ value: t, label: t }))} />
        </div>
        <label className="field" style={{ flex: 1 }}>
          <span>DNS server</span>
          <input className="input mono" value={value.server ?? ""} onChange={(e) => set({ server: e.target.value })} placeholder="System resolver, or 1.1.1.1" />
        </label>
      </div>
      <div className="up-row">
        <div className="field" style={{ width: 220 }}>
          <span>Answers must</span>
          <Select
            label="Match"
            value={value.match ?? ""}
            align="left"
            onChange={(v) => set({ match: v as DnsConfig["match"] })}
            options={[
              { value: "", label: "Exist (any answer)" },
              { value: "contains", label: "Contain these values" },
              { value: "equals", label: "Be exactly these values" },
            ]}
          />
        </div>
        {value.match && (
          <label className="field" style={{ flex: 1 }}>
            <span>Values</span>
            <input
              className="input mono"
              value={(value.values ?? []).join(", ")}
              onChange={(e) => set({ values: e.target.value.split(",").map((s) => s.trimStart()) })}
              placeholder={value.record_type === "MX" ? "mx1.example.com" : value.record_type === "TXT" ? "v=spf1 include:_spf.example.com ~all" : "203.0.113.10, 203.0.113.11"}
              required
            />
          </label>
        )}
      </div>
      <small className="up-hint">Comma separated. Names compare without case or the trailing dot; an MX value can be the host alone. Resolution time is recorded as the response time.</small>
    </>
  );
}

export function PingFields({ value, onChange }: { value: PingConfig; onChange: (v: PingConfig) => void }) {
  const set = (p: Partial<PingConfig>) => onChange({ ...value, ...p });
  return (
    <div className="up-row">
      <label className="field" style={{ flex: 1 }}>
        <span>Host</span>
        <input className="input mono" value={value.host} onChange={(e) => set({ host: e.target.value })} placeholder="gateway.example.com or 203.0.113.1" required />
      </label>
      <label className="field" style={{ width: 110 }}>
        <span>Packets</span>
        <input className="input" type="number" min={1} max={20} value={value.packets || ""} onChange={(e) => set({ packets: num(e.target.value) })} />
      </label>
      <label className="field" style={{ width: 150 }}>
        <span>Fail above loss (%)</span>
        <input className="input" type="number" min={0} max={99} value={value.max_loss_pct} onChange={(e) => set({ max_loss_pct: num(e.target.value) })} />
      </label>
    </div>
  );
}

export function SslFields({ value, onChange }: { value: SslConfig; onChange: (v: SslConfig) => void }) {
  const set = (p: Partial<SslConfig>) => onChange({ ...value, ...p });
  return (
    <>
      <div className="up-row">
        <label className="field" style={{ flex: 1 }}>
          <span>Host</span>
          <input className="input mono" value={value.host} onChange={(e) => set({ host: e.target.value })} placeholder="example.com" required />
        </label>
        <label className="field" style={{ width: 110 }}>
          <span>Port</span>
          <input className="input" type="number" min={1} max={65535} value={value.port || ""} onChange={(e) => set({ port: num(e.target.value) })} />
        </label>
        <label className="field" style={{ flex: 1 }}>
          <span>Server name (SNI)</span>
          <input className="input mono" value={value.server_name ?? ""} onChange={(e) => set({ server_name: e.target.value })} placeholder="Same as the host" />
        </label>
      </div>
      <div className="up-row">
        <label className="field" style={{ flex: 1 }}>
          <span>Degraded under (days left)</span>
          <input className="input" type="number" min={0} max={365} value={value.warn_days} onChange={(e) => set({ warn_days: num(e.target.value) })} />
        </label>
        <label className="field" style={{ flex: 1 }}>
          <span>Down under (days left)</span>
          <input className="input" type="number" min={0} max={365} value={value.fail_days} onChange={(e) => set({ fail_days: num(e.target.value) })} />
        </label>
        <div className="field" style={{ flex: 1 }}>
          <span>Minimum TLS</span>
          <Select
            label="Minimum TLS version"
            value={value.min_tls ?? ""}
            align="left"
            onChange={(v) => set({ min_tls: v as SslConfig["min_tls"] })}
            options={[{ value: "", label: "Any" }, ...["1.0", "1.1", "1.2", "1.3"].map((v) => ({ value: v, label: `TLS ${v}` }))]}
          />
        </div>
      </div>
      <fieldset className="up-checks">
        <label>
          <input type="checkbox" checked={!value.ignore_chain} onChange={(e) => set({ ignore_chain: !e.target.checked })} />
          The chain must be trusted (turn off for self-signed certificates)
        </label>
        <label>
          <input type="checkbox" checked={!value.ignore_hostname} onChange={(e) => set({ ignore_hostname: !e.target.checked })} />
          The certificate must name the host
        </label>
      </fieldset>
    </>
  );
}

const PERIODS = [300, 900, 1800, 3600, 7200, 21600, 43200, 86400, 604800];
const GRACES = [60, 300, 900, 1800, 3600, 7200, 21600, 86400];

export function HeartbeatFields({ value, onChange }: { value: HeartbeatConfig; onChange: (v: HeartbeatConfig) => void }) {
  const cron = value.period_s === undefined;
  const periodOptions = [...new Set([...PERIODS, value.period_s ?? 86400])].sort((a, b) => a - b).map((s) => ({ value: String(s), label: `Every ${formatInterval(s)}` }));
  const graceOptions = [...new Set([...GRACES, value.grace_s])].sort((a, b) => a - b).map((s) => ({ value: String(s), label: s === 0 ? "None" : formatInterval(s) }));
  return (
    <>
      <div className="field">
        <span>Expect a ping</span>
        <div className="segmented" role="radiogroup" aria-label="Schedule">
          <button type="button" role="radio" aria-checked={!cron} className={!cron ? "on" : ""} onClick={() => onChange({ grace_s: value.grace_s, period_s: value.period_s ?? 86400 })}>
            Every period
          </button>
          <button type="button" role="radio" aria-checked={cron} className={cron ? "on" : ""} onClick={() => onChange({ grace_s: value.grace_s, cron: value.cron ?? "0 3 * * *", timezone: value.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone })}>
            On a cron schedule
          </button>
        </div>
      </div>
      <div className="up-row">
        {cron ? (
          <>
            <label className="field" style={{ flex: 1 }}>
              <span>Cron expression</span>
              <input className="input mono" value={value.cron ?? ""} onChange={(e) => onChange({ ...value, cron: e.target.value })} placeholder="0 3 * * *" required />
              <small>Minute, hour, day of month, month, day of week; or @hourly, @daily, @weekly.</small>
            </label>
            <label className="field" style={{ flex: 1 }}>
              <span>Time zone</span>
              <input className="input mono" value={value.timezone ?? ""} onChange={(e) => onChange({ ...value, timezone: e.target.value })} placeholder="UTC" />
              <small>The job's zone, such as Europe/Berlin.</small>
            </label>
          </>
        ) : (
          <div className="field" style={{ flex: 1 }}>
            <span>Period</span>
            <Select label="Period" value={String(value.period_s ?? 86400)} align="left" onChange={(v) => onChange({ ...value, period_s: Number(v) })} options={periodOptions} />
          </div>
        )}
        <div className="field" style={{ width: 160 }}>
          <span>Grace time</span>
          <Select label="Grace time" value={String(value.grace_s)} align="left" onChange={(v) => onChange({ ...value, grace_s: Number(v) })} options={graceOptions} />
        </div>
      </div>
      <small className="up-hint">
        The check is down when no ping arrives by the expected time plus the grace time, when the job pings <code>/fail</code> or a non-zero exit code, or when it pings <code>/start</code> and does not finish within the grace time. The URL to call is shown once the check is saved.
      </small>
    </>
  );
}

/** Rows of names and values, such as request headers. */
export function KeyValueRows({ rows, onChange, keyLabel, valueLabel, max, mask }: { rows: { k: string; v: string }[]; onChange: (r: { k: string; v: string }[]) => void; keyLabel: string; valueLabel: string; max: number; mask?: (k: string, v: string) => boolean }) {
  return (
    <>
      {rows.map((h, i) => (
        <div className="up-row" key={i}>
          <input className="input mono" style={{ flex: 1 }} value={h.k} placeholder={keyLabel} aria-label={keyLabel} onChange={(e) => onChange(rows.map((x, j) => (j === i ? { ...x, k: e.target.value } : x)))} />
          <input
            className="input mono"
            style={{ flex: 1.4 }}
            value={h.v}
            placeholder={valueLabel}
            aria-label={valueLabel}
            type={mask?.(h.k, h.v) ? "password" : "text"}
            onChange={(e) => onChange(rows.map((x, j) => (j === i ? { ...x, v: e.target.value } : x)))}
          />
          <button type="button" className="icon-btn" aria-label={`Remove ${keyLabel.toLowerCase()}`} title="Remove" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
            <X size={15} />
          </button>
        </div>
      ))}
      <div>
        <button type="button" className="btn ghost sm" disabled={rows.length >= max} onClick={() => onChange([...rows, { k: "", v: "" }])}>
          <Plus size={14} />
          Add {keyLabel.toLowerCase()}
        </button>
      </div>
    </>
  );
}

export const msText = (v: number | null | undefined) => (v === null || v === undefined ? "—" : formatMs(v));
