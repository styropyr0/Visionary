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
  /** open until someone triages it. */
  status: FindingStatus;
  note: string;
  status_by: string;
  status_at: string;
}

export type FindingStatus = "open" | "acknowledged" | "resolved" | "false_positive";

export const STATUS_LABELS: Record<FindingStatus, string> = {
  open: "Open",
  acknowledged: "Acknowledged",
  resolved: "Resolved",
  false_positive: "False positive",
};

export interface FindingDetail {
  finding: Finding;
  from: string;
  to: string;
  match?: "ip" | "user" | "role" | "audit";
  requests?: Result;
  statuses?: Result;
  /** The audit entries behind a finding from Visionary's own audit log. */
  audit?: Result;
}

export interface Suppression {
  id: number;
  created_at: string;
  created_by: string;
  kind: string;
  ip?: string;
  actor?: string;
  note?: string;
  expires_at?: string;
}

export interface GeoInfo {
  country?: string;
  country_name?: string;
  city?: string;
  asn?: number;
  org?: string;
}

export interface IPInfo {
  ip: string;
  public: boolean;
  geo: GeoInfo;
  place: string;
  listed_in: string[] | null;
  ignored: boolean;
  findings: { kind: string; findings: number; occurrences: number; severity: string; last: string }[] | null;
  traffic?: { days: number; requests: number; first: string; last: string; failed_logins: number; logins: number; client_errors: number; server_errors: number; accounts: number; user_agents: number };
  accounts: { account: string; requests: number; failed_logins: number }[] | null;
  portal?: { failed_sign_ins: number; sign_ins: number };
}

export interface AccountTimeline {
  account: string;
  days: number;
  sign_ins: number;
  failed_sign_ins: number;
  ips: number;
  geo: boolean;
  logins: { at: string; ip: string; place: string; device: string; status: number; route: string; failed: boolean }[] | null;
  findings: Finding[] | null;
  known: { kind: string; value: string; first_seen: string; last_seen: string; hits: number }[] | null;
}

export interface IntelStatus {
  city: { path: string; open: boolean; error: string };
  asn: { path: string; open: boolean; error: string };
  blocklists: { url: string; name: string; entries: number; error?: string; fetched?: string }[] | null;
  fetched?: string;
}

export type FindingAction = "block_ip" | "end_sessions" | "ignore_ip" | "suppress";

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
  status: string;
}

export const securityApi = {
  meta: (signal?: AbortSignal) => api<SecurityMeta>("/api/security/meta", { signal }),
  panel: (name: string, from: Date, to: Date, signal?: AbortSignal) => api<Result>(`/api/security/panels/${name}?${rangeQuery(from, to)}`, { signal }),
  findings: (from: Date, to: Date, filters: Partial<FindingFilters> & { before?: string }, signal?: AbortSignal) =>
    api<{ findings: Finding[]; more: boolean }>(`/api/security/findings?${rangeQuery(from, to, filters as Record<string, string>)}`, { signal }),
  finding: (id: number, signal?: AbortSignal) => api<FindingDetail>(`/api/security/findings/${id}`, { signal }),
  audit: (from: Date, to: Date, signal?: AbortSignal) =>
    api<{ entries: AuditEntry[]; counts: { action: string; count: number }[] }>(`/api/security/audit?${rangeQuery(from, to)}`, { signal }),
  updateFinding: (id: number, change: { status?: FindingStatus; note?: string }) => api<Finding>(`/api/security/findings/${id}`, { method: "PATCH", json: change }),
  action: (id: number, action: FindingAction, extra: { account?: string; scope?: "ip" | "actor"; days?: number; note?: string } = {}) =>
    api<Record<string, unknown>>(`/api/security/findings/${id}/actions`, { method: "POST", json: { action, ...extra } }),
  suppressions: (signal?: AbortSignal) => api<{ suppressions: Suppression[] }>("/api/security/suppressions", { signal }),
  addSuppression: (s: { kind: string; ip?: string; actor?: string; note?: string; days?: number }) => api<Suppression>("/api/security/suppressions", { method: "POST", json: s }),
  deleteSuppression: (id: number) => api(`/api/security/suppressions/${id}`, { method: "DELETE" }),
  account: (account: string, days: number, signal?: AbortSignal) => api<AccountTimeline>(`/api/security/accounts/${encodeURIComponent(account)}?days=${days}`, { signal }),
  ip: (ip: string, signal?: AbortSignal) => api<IPInfo>(`/api/security/ip/${encodeURIComponent(ip)}`, { signal }),
  intel: (refresh = false, signal?: AbortSignal) => api<IntelStatus>(`/api/security/intel${refresh ? "?refresh=1" : ""}`, { signal }),
  auditExportUrl: (from: Date, to: Date) => `/api/security/audit/export?${rangeQuery(from, to)}`,
  verifyAudit: (file: string) =>
    api<{ valid: boolean; entries?: number; from?: string; to?: string; exported_at?: string; exported_by?: string; line?: number; error?: string }>("/api/security/audit/verify", {
      method: "POST",
      json: { file },
    }),
};

/** What each finding kind means, for labels and the filter. */
export const KINDS: Record<string, { label: string; source: "api" | "host" | "audit" | "logs"; text: string }> = {
  secret_in_logs: {
    label: "Secret in logs",
    source: "logs",
    text: "A credential or card number reached the stored logs. It was redacted there; fix the code that logs it, and rotate the credential if it is real.",
  },
  auth_bruteforce: { label: "Brute force", source: "api", text: "Many failed sign-ins from one IP." },
  credential_stuffing: { label: "Credential stuffing", source: "api", text: "One IP failing to sign in as many different accounts." },
  account_targeted: { label: "Account targeted", source: "api", text: "Many IPs failing to sign in as one account." },
  scanner: { label: "Scanner", source: "api", text: "Requests for probe paths such as /.env or /wp-admin, or a scanner's user agent." },
  rate_abuse: { label: "Rate abuse", source: "api", text: "An IP or user far above its usual request rate." },
  forbidden_spike: { label: "Forbidden spike", source: "api", text: "A jump in 403 responses for one user or role: possible privilege probing." },
  login_as_other: { label: "Signing in as someone else", source: "api", text: "A browser signed in as one user trying to sign in as another account." },
  auth_bruteforce_slow: { label: "Slow brute force", source: "api", text: "Many failed sign-ins from one IP, paced to stay under the per-window limit." },
  account_targeted_slow: { label: "Slow account targeting", source: "api", text: "Many IPs failing on one account over the long window." },
  distributed_login_attack: { label: "Distributed attack", source: "api", text: "Many addresses each failing a few sign-ins: a botnet spreading its guesses." },
  new_login_location: { label: "New sign-in location", source: "api", text: "A sign-in from a country the account never used before." },
  new_login_device: { label: "New device", source: "api", text: "A sign-in with a browser and system the account never used before." },
  impossible_travel: { label: "Impossible travel", source: "api", text: "Two sign-ins too far apart for the time between them." },
  listed_ip_login: { label: "Sign-in from a listed IP", source: "api", text: "A sign-in from an address a blocklist names." },
  session_spread: { label: "Session used from many places", source: "api", text: "One account used from many addresses at once, in different countries or on different devices." },
  risky_account_change: { label: "Change after risky sign-in", source: "api", text: "A password, email, MFA or payout change soon after a risky sign-in." },
  enumeration: { label: "Enumeration", source: "api", text: "Many different records read through one route: scraping, or walking through IDs." },
  idor_probe: { label: "Access probing", source: "api", text: "Many different records refused (403/404) through one route: trying records that aren't yours." },
  injection_attempt: { label: "Injection attempt", source: "api", text: "SQL, script, template, command, JNDI or traversal patterns in a path or query string." },
  route_rate_limit: { label: "Route limit", source: "api", text: "A route called more often than its per-minute limit." },
  signup_abuse: { label: "Sign-up flood", source: "api", text: "Many sign-ups from one IP." },
  otp_abuse: { label: "Code flood", source: "api", text: "Many sign-in codes sent from one IP or to one account." },
  portal_bruteforce: { label: "Visionary brute force", source: "audit", text: "Many failed sign-ins to this dashboard from one IP." },
  portal_account_targeted: { label: "Visionary account targeted", source: "audit", text: "Many IPs failing to sign in to this dashboard as one account." },
  portal_secret_viewed: { label: "Secret shown", source: "audit", text: "Someone showed a stored secret in Settings → Server." },
  portal_account_created: { label: "Account added", source: "audit", text: "A Visionary account was added." },
  portal_account_changed: { label: "Account changed", source: "audit", text: "A Visionary account's role, password or state changed." },
  portal_account_deleted: { label: "Account deleted", source: "audit", text: "A Visionary account was deleted." },
  portal_roles_changed: { label: "Roles changed", source: "audit", text: "Who gets which role in Visionary changed." },
  portal_signin_changed: { label: "Sign-in changed", source: "audit", text: "How people sign in to Visionary changed." },
  portal_api_key_created: { label: "API key created", source: "audit", text: "A Visionary API key was created." },
  portal_data_deleted: { label: "Data deleted", source: "audit", text: "Stored data was deleted from Settings → Data." },
  portal_password_changed: { label: "Password changed", source: "audit", text: "Someone changed their Visionary password." },
  portal_ip_blocked: { label: "Address refused", source: "audit", text: "A request to Visionary came from an address not on the allow list." },
  portal_two_step_reset: { label: "Two-step reset", source: "audit", text: "An admin turned off two-step sign-in for an account." },
  ssh_bruteforce: { label: "SSH brute force", source: "host", text: "Many failed SSH logins on a host from one address." },
  ssh_key_added: { label: "SSH key added", source: "host", text: "A new key in an authorized_keys file." },
  ssh_key_removed: { label: "SSH key removed", source: "host", text: "A key left an authorized_keys file." },
  file_changed: { label: "File changed", source: "host", text: "A watched system file (passwd, sudoers, cron, systemd units…) changed." },
  suspicious_process: { label: "Suspicious process", source: "host", text: "A miner, a process running from /tmp or a deleted file, or a shell on a network socket." },
  container_privileged: { label: "Privileged container", source: "host", text: "A container with privileged mode, host networking, the Docker socket or broad capabilities." },
  container_root: { label: "Container as root", source: "host", text: "A running container whose user is root." },
  container_new_image: { label: "New container image", source: "host", text: "A container running an image this host hadn't run before." },
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
  return source === "api" ? "API" : source === "audit" ? "Visionary" : source === "logs" ? "Logs" : titleCase(source);
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
  "auth.ip_blocked": "Refused (not on the allow list)",
  "auth.two_step_enabled": "Two-step sign-in turned on",
  "auth.two_step_disabled": "Two-step sign-in turned off",
  "auth.two_step_reset": "Two-step sign-in reset by an admin",
  "settings.secret_viewed": "Secret shown",
  "security.finding_updated": "Finding triaged",
  "security.action": "Security action sent",
  "security.suppression_created": "Suppression added",
  "security.suppression_deleted": "Suppression removed",
  "security.audit_exported": "Audit log exported",
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
