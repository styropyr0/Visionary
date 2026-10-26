import type { Config, LookupResult } from "./config.js";
import type { Redactor } from "./redact.js";
import { errorFields } from "./redact.js";
import type { Value } from "./store/sql.js";
import { formatTs } from "./util.js";

/** The raw capture of one request. Redaction, JSON encoding and user lookup
 * happen on the writer, off the request path. */
export interface Entry {
  requestId: string;
  startedAt: number;
  method: string;
  path: string;
  route: string;
  query: string;
  status: number;
  durationMs: number;
  ip: string;
  userAgent: string;
  userId: string;
  userHandle: string;
  userRole: string;
  /** The account a sign-in tried (loginAttempt). */
  loginAccount: string;
  headers: Record<string, string | string[] | undefined>;
  requestContentType: string;
  requestBody: Buffer;
  requestTruncated: boolean;
  requestSize: number;
  responseContentType: string;
  responseBody: Buffer;
  responseTruncated: boolean;
  responseSize: number;
  service: string;
  version: string;
  /** null when the SDK cannot see that kind of call */
  dbCount: number | null;
  dbMs: number | null;
  httpCount: number | null;
  httpMs: number | null;
}

const orNull = (s: string): string | null => s || null;

/** Trims a login identifier to what the column holds (255 characters). */
export function clipLogin(s: string): string {
  const t = String(s ?? "").trim();
  return t.length > 255 ? Array.from(t).slice(0, 255).join("") : t;
}

/** The api_logs row in REQUEST_COLUMNS order. */
export function requestValues(e: Entry, cfg: Config, redact: Redactor, users: Map<string, LookupResult>): Value[] {
  let handle = e.userHandle;
  let role = e.userRole;
  const found = users.get(e.userId);
  if (found) {
    handle ||= found.handle ?? "";
    role ||= found.role ?? "";
  }
  let code = "";
  let key = "";
  if (e.status >= 400) [code, key] = errorFields(e.responseBody, cfg.errorCodeField, cfg.errorKeyField);
  const reqBody = cfg.captureBodies ? redact.requestBody(e.requestBody, e.requestContentType) : "";
  const respBody = cfg.captureBodies ? redact.responseBody(e.responseBody) : "";
  return [
    e.requestId,
    formatTs(e.startedAt),
    e.method,
    e.path,
    orNull(e.route),
    orNull(e.query),
    e.status,
    Math.round(e.durationMs * 1000) / 1000,
    orNull(e.ip),
    orNull(e.userAgent),
    orNull(e.userId),
    orNull(handle),
    orNull(role),
    orNull(redact.headers(e.headers)),
    orNull(e.requestContentType),
    orNull(reqBody),
    e.requestTruncated ? 1 : 0,
    e.requestSize,
    orNull(e.responseContentType),
    orNull(respBody),
    e.responseTruncated ? 1 : 0,
    e.responseSize,
    orNull(code),
    orNull(key),
    orNull(e.service),
    orNull(e.version),
    e.dbCount,
    e.dbMs === null ? null : Math.round(e.dbMs * 1000) / 1000,
    e.httpCount,
    e.httpMs === null ? null : Math.round(e.httpMs * 1000) / 1000,
    orNull(clipLogin(e.loginAccount)),
  ];
}
