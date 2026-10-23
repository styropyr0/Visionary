import { api, rangeQuery, type Result } from "./api";
import { useApp } from "./state";
import { useQuery } from "./useQuery";
import { dateFormat } from "./time";
import { titleCase } from "./format";

export type FindingSeverity = "info" | "warning" | "critical";

export interface Finding {
  id: number;
  created_at: string;
  source: "api" | "host" | string;
  kind: string;
  severity: FindingSeverity;
  host: string;
  service: string;
  actor: string;
  ip: string;
  target: string;
  detail: Record<string, unknown>;
  summary: string;
  count: number;
}

export interface FindingDetail {
  finding: Finding;
  from: string;
  to: string;
  match?: "ip" | "user" | "role";
  requests?: Result;
  statuses?: Result;
}

export interface AuditEntry {
  id: number;
  created_at: string;
  user_id: string;
  user_name: string;
  role: string;
  action: string;
  target: string;
  detail: Record<string, unknown>;
  ip: string;
}

export interface SecurityMeta {
  enabled: boolean;
  interval: string;
  window: string;
  dedupe: string;
  last_run: string;
  runs: number;
  error: string;
  hosts: number;
  is_admin: boolean;
  thresholds: Record<string, number>;
}

export interface FindingFilters {
  source: string;
  kind: string;
  severity: string;
  host: string;
  ip: string;
}

export const securityApi = {
  meta: (signal?: AbortSignal) => api<SecurityMeta>("/api/security/meta", { signal }),
  panel: (name: string, from: Date, to: Date, signal?: AbortSignal) => api<Result>(`/api/security/panels/${name}?${rangeQuery(from, to)}`, { signal }),
  findings: (from: Date, to: Date, filters: Partial<FindingFilters> & { before?: string }, signal?: AbortSignal) =>
    api<{ findings: Finding[]; more: boolean }>(`/api/security/findings?${rangeQuery(from, to, filters as Record<string, string>)}`, { signal }),
  finding: (id: number, signal?: AbortSignal) => api<FindingDetail>(`/api/security/findings/${id}`, { signal }),
  audit: (from: Date, to: Date, signal?: AbortSignal) =>
    api<{ entries: AuditEntry[]; counts: { action: string; count: number }[] }>(`/api/security/audit?${rangeQuery(from, to)}`, { signal }),
};

/** What each finding kind means, for labels and the filter. */
export const KINDS: Record<string, { label: string; source: "api" | "host"; text: string }> = {
  auth_bruteforce: { label: "Brute force", source: "api", text: "Many failed sign-ins from one IP." },
  credential_stuffing: { label: "Credential stuffing", source: "api", text: "One IP failing to sign in as many different accounts." },
  account_targeted: { label: "Account targeted", source: "api", text: "Many IPs failing to sign in as one account." },
  scanner: { label: "Scanner", source: "api", text: "Requests for probe paths such as /.env or /wp-admin, or a scanner's user agent." },
  rate_abuse: { label: "Rate abuse", source: "api", text: "An IP or user far above its usual request rate." },
  forbidden_spike: { label: "Forbidden spike", source: "api", text: "A jump in 403 responses for one user or role: possible privilege probing." },
  ssh_failed: { label: "SSH failed logins", source: "host", text: "Failed SSH logins on a host." },
  ssh_login: { label: "SSH login", source: "host", text: "A successful SSH login." },
  sudo: { label: "sudo", source: "host", text: "A command run with sudo." },
  port_opened: { label: "Port opened", source: "host", text: "A new listening port on a host." },
  port_closed: { label: "Port closed", source: "host", text: "A listening port went away." },
  updates_pending: { label: "Updates pending", source: "host", text: "Security updates waiting to be installed." },
  reboot: { label: "Reboot", source: "host", text: "A host restarted." },
};

export function kindLabel(kind: string): string {
  return KINDS[kind]?.label ?? titleCase(kind);
}

/** Where a finding came from, as a short label. */
export function sourceLabel(source: string): string {
  return source === "api" ? "API" : titleCase(source);
}

/** Readable names for Visionary's own audit actions. */
export const AUDIT_ACTIONS: Record<string, string> = {
  "auth.sign_in": "Signed in",
  "auth.sign_in_failed": "Sign-in failed",
  "auth.sign_in_refused": "Sign-in refused (role)",
  "auth.sign_out": "Signed out",
  "api_key.create": "API key created",
  "api_key.revoke": "API key revoked",
  "alert.channel.create": "Channel added",
  "alert.channel.update": "Channel changed",
  "alert.channel.delete": "Channel deleted",
  "alert.channel.test": "Channel tested",
  "team.role_mapping": "Sign-in mode or role mapping changed",
};

export function auditLabel(action: string): string {
  return AUDIT_ACTIONS[action] ?? action;
}

/** The analyst question for a finding. IPs never go to the model, so it names the finding by id. */
export function investigateQuestion(f: Finding): string {
  const when = dateFormat({ month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(f.created_at));
  const who = f.host ? `host ${f.host}` : f.ip ? "the IP in that finding" : f.actor ? `${f.actor}` : "it";
  return `Investigate security finding #${f.id} (${kindLabel(f.kind)}, ${f.severity}) first seen around ${when}. Using security_events id ${f.id} and api_logs joined on its ip, what did ${who} do before and after, which routes and statuses, did any request succeed, and is it still going on?`;
}

export function rowObjects(r: Result | undefined): Record<string, unknown>[] {
  if (!r) return [];
  return r.rows.map((row) => Object.fromEntries(r.columns.map((c, i) => [c, row[i]])));
}

/** One of the Security page's fixed panels, over the picker's range or the one given. */
export function useSecurityPanel(name: string, from?: Date, to?: Date) {
  const { resolved, tick } = useApp();
  const f = from ?? resolved.from;
  const t = to ?? resolved.to;
  return useQuery<Result>(`sec-${name}?${f.toISOString()}-${t.toISOString()}#${tick}`, (signal) => securityApi.panel(name, f, t, signal));
}
