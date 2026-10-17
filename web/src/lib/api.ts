export type Unit = "count" | "ms" | "percent" | "bytes" | "none";
export type VizType = "timeseries" | "bar" | "pie" | "stat" | "table";

export interface Viz {
  type: VizType;
  x?: string;
  y?: string[];
  series?: string;
  stack?: boolean;
  unit?: Unit;
  /** Color bars by HTTP status class instead of a series color. */
  colorBy?: "status";
}

export interface Result {
  columns: string[];
  rows: unknown[][];
  truncated: boolean;
  rows_read: number;
  duration_ms: number;
  sql: string;
  bucket: number;
  from: string;
  to: string;
}

export interface Answer {
  title: string;
  description: string;
  viz: Viz;
  query: string;
  result: Result;
  attempts: number;
  model: string;
}

export interface Panel {
  id: number;
  title: string;
  description: string;
  prompt: string;
  query: string;
  viz: Viz;
  created_at: string;
}

export interface Meta {
  ask_enabled: boolean;
  app_name: string;
  model: string;
  routes: string[];
  max_rows: number;
  max_range_days: number;
  /** Apdex threshold T in ms: satisfied within T, tolerating within 4T. */
  apdex_ms: number;
  /** SLO: target percent of requests that succeed within T. */
  slo_target: number;
}

export interface SessionUser {
  id: string;
  name: string;
  handle: string;
  roles: string[];
}

/** How sign-in works, served by the server from its auth config. */
export interface AuthConfig {
  provider: string;
  type: "otp" | "password" | "shared" | "none";
  identifier: {
    label: string;
    placeholder?: string;
    input?: "tel" | "email" | "text";
    normalize?: "digits" | "lowercase" | "trim";
    pattern_hint?: string;
  };
  code_length?: number;
  resend_seconds?: number;
  location: "none" | "optional" | "required";
  allowed_roles: string[];
}

export interface SessionInfo {
  user: SessionUser;
  expires_at: string;
}

export interface StatsResponse {
  current: Result;
  previous: Result | null;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

export const UNAUTHORIZED_EVENT = "va:unauthorized";

export async function api<T>(path: string, init: RequestInit & { json?: unknown } = {}): Promise<T> {
  const { json, headers, ...rest } = init;
  const res = await fetch(path, {
    credentials: "same-origin",
    ...rest,
    headers: {
      Accept: "application/json",
      ...(json !== undefined ? { "Content-Type": "application/json" } : {}),
      ...headers,
    },
    body: json !== undefined ? JSON.stringify(json) : rest.body,
  });
  if (res.status === 204) {
    return undefined as T;
  }
  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    // Non-JSON error pages fall through to the status message.
  }
  if (!res.ok) {
    const msg =
      body && typeof body === "object" && "error" in body ? String((body as { error: unknown }).error) : res.statusText;
    if (res.status === 401 && !path.startsWith("/api/auth/")) {
      window.dispatchEvent(new CustomEvent<string>(UNAUTHORIZED_EVENT, { detail: msg }));
    }
    throw new ApiError(res.status, msg || `Request failed (${res.status})`);
  }
  return body as T;
}

export function rangeQuery(from: Date, to: Date, extra: Record<string, string> = {}): string {
  const p = new URLSearchParams({ from: from.toISOString(), to: to.toISOString(), ...extra });
  for (const [k, v] of [...p.entries()]) {
    if (v === "") p.delete(k);
  }
  return p.toString();
}
