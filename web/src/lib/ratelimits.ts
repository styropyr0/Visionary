import { api, rangeQuery } from "./api";

export type RLAction = "limit" | "block" | "allow";
export type RLAlgorithm = "sliding" | "fixed" | "token_bucket";
export type RLOutcome = "limited" | "blocked" | "would_limit" | "would_block";

/** One rule as stored in ratelimit.config; empty fields mean the default. */
export interface RLRule {
  id?: string;
  name?: string;
  enabled?: boolean;
  mode?: "enforce" | "monitor" | "";
  action: RLAction;
  services?: string[];
  methods?: string[];
  paths?: string[];
  ips?: string[];
  countries?: string[];
  match?: Record<string, string[]>;
  key?: string;
  limit?: number;
  window_s?: number;
  algorithm?: RLAlgorithm | "";
  burst?: number;
  limit_by?: string;
  limits?: Record<string, number>;
  status?: number;
  body?: string;
  content_type?: string;
}

export interface RLConfig {
  enabled?: boolean;
  ip_header?: string;
  country_headers?: string[];
  rules: RLRule[];
}

export interface RLView {
  config: RLConfig;
  version: string;
  updated_at: string;
  can_edit: boolean;
  route_limits: { method: string; path: string; per_min: number }[];
  default_country_headers: string[];
}

export interface RLRuleStats {
  rule_id: string;
  rule_name: string;
  limited: number;
  blocked: number;
  would_limit: number;
  would_block: number;
  ips: number;
  last: string;
}

export interface RLClient {
  ip: string;
  country: string;
  user_id: string;
  hits: number;
  enforced: number;
  rules: number;
  last: string;
  place?: string;
  org?: string;
}

export interface RLEvent {
  created_at: string;
  service: string;
  instance: string;
  rule_id: string;
  rule_name: string;
  outcome: RLOutcome;
  key_value: string;
  ip: string;
  country: string;
  method: string;
  path: string;
  user_id: string;
  hits: number;
}

export interface RLActivity {
  rules: RLRuleStats[];
  series: { t: number; enforced: number; monitored: number }[];
  clients: RLClient[];
  recent: RLEvent[];
  bucket: number;
}

export const rateLimitsApi = {
  get: (signal?: AbortSignal) => api<RLView>("/api/ratelimits", { signal }),
  save: (c: RLConfig) => api<RLView>("/api/ratelimits", { method: "PUT", json: c }),
  activity: (from: Date, to: Date, signal?: AbortSignal) => api<RLActivity>(`/api/ratelimits/activity?${rangeQuery(from, to)}`, { signal }),
};

export const OUTCOME_LABELS: Record<RLOutcome, string> = {
  limited: "Limited",
  blocked: "Blocked",
  would_limit: "Would limit",
  would_block: "Would block",
};

/** Properties a rule can key on, match or pick its limit by. */
export const KEY_OPTIONS: { value: string; label: string; user?: boolean }[] = [
  { value: "ip", label: "Client IP" },
  { value: "user", label: "User", user: true },
  { value: "role", label: "User role", user: true },
  { value: "country", label: "Country" },
  { value: "global", label: "Everyone together" },
  { value: "header:", label: "Request header…" },
  { value: "query:", label: "Query parameter…" },
  { value: "attr:", label: "App attribute…", user: true },
];

/** Whether a property is only known once the app's auth code ran. */
export function needsUser(prop: string | undefined): boolean {
  return !!prop && (prop === "user" || prop === "role" || prop.startsWith("attr:"));
}

export function ruleNeedsUser(r: RLRule): boolean {
  return (r.action === "limit" && (needsUser(r.key) || needsUser(r.limit_by))) || Object.keys(r.match ?? {}).some(needsUser);
}

export function propLabel(prop: string | undefined): string {
  if (!prop || prop === "ip") return "IP";
  if (prop === "global") return "everyone";
  if (prop.startsWith("header:")) return prop.slice(7);
  if (prop.startsWith("query:")) return `?${prop.slice(6)}`;
  if (prop.startsWith("attr:")) return prop.slice(5);
  return prop;
}

export function windowLabel(s: number | undefined): string {
  const n = s ?? 0;
  if (n > 0 && n % 86400 === 0) return n === 86400 ? "day" : `${n / 86400} days`;
  if (n > 0 && n % 3600 === 0) return n === 3600 ? "hour" : `${n / 3600} hours`;
  if (n > 0 && n % 60 === 0) return n === 60 ? "minute" : `${n / 60} minutes`;
  return n === 1 ? "second" : `${n} seconds`;
}

/** "60 a minute per IP" and the like. */
export function ruleSummary(r: RLRule): string {
  if (r.action === "allow") return "Never limited";
  if (r.action === "block") return `Refused with ${r.status || 403}`;
  const per = r.key === "global" ? "in all" : `per ${propLabel(r.key)}`;
  const tiers = r.limit_by && r.limits && Object.keys(r.limits).length ? `, by ${propLabel(r.limit_by)}` : "";
  const alg = r.algorithm === "token_bucket" ? `, bursts of ${r.burst || r.limit || 0}` : r.algorithm === "fixed" ? ", fixed window" : "";
  return `${r.limit ?? 0} a ${windowLabel(r.window_s)} ${per}${tiers}${alg}`;
}

export function scopeSummary(r: RLRule): string {
  const parts: string[] = [];
  const methods = r.methods?.length ? r.methods.join(", ") : "";
  const paths = r.paths?.length ? r.paths.join(", ") : "";
  parts.push([methods, paths].filter(Boolean).join(" ") || "Every request");
  if (r.ips?.length) parts.push(`${r.ips.length === 1 ? r.ips[0] : `${r.ips.length} IPs or ranges`}`);
  if (r.countries?.length) parts.push(`from ${r.countries.join(", ")}`);
  for (const [k, v] of Object.entries(r.match ?? {})) parts.push(v.length ? `${propLabel(k)} = ${v.join(" or ")}` : `with ${propLabel(k)}`);
  if (r.services?.length) parts.push(`on ${r.services.join(", ")}`);
  return parts.join(" · ");
}
