export const REDACTED = "[REDACTED]";

const DEFAULT_REDACT_HEADERS = ["Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token"];
const DEFAULT_REDACT_FIELDS = [
  "password", "passwd", "secret", "client_secret", "token", "access_token", "refresh_token", "id_token",
  "api_key", "apikey", "otp", "pin", "card_number", "cvv", "private_key",
];

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

function keyPattern(names: string[]): string {
  return names
    .map((n) => n.trim())
    .filter(Boolean)
    .map(escapeRe)
    .join("|");
}

/** Matches "key": value pairs. Regex-based on purpose, so bodies cut off by
 * the capture cap (invalid JSON) are still scrubbed. */
function jsonSecrets(keys: string): RegExp {
  return new RegExp(`("(?:${keys})"\\s*:\\s*)("(?:[^"\\\\]|\\\\.)*"?|[^,}\\]\\s]+)`, "gi");
}

/** Go's http.CanonicalHeaderKey, so every SDK stores the same keys. */
export function canonicalHeader(name: string): string {
  return name
    .split("-")
    .map((p) => p.slice(0, 1).toUpperCase() + p.slice(1).toLowerCase())
    .join("-");
}

export class Redactor {
  private readonly headerSet: Set<string>;
  private readonly respJson: RegExp;
  private readonly reqJson: RegExp;
  private readonly form: RegExp;

  constructor(extraHeaders: string[], extraFields: string[], requestOnly: string[]) {
    this.headerSet = new Set(
      [...DEFAULT_REDACT_HEADERS, ...extraHeaders].map((h) => h.trim().toLowerCase()).filter(Boolean),
    );
    const both = keyPattern([...DEFAULT_REDACT_FIELDS, ...extraFields]);
    const extra = keyPattern(requestOnly);
    const reqKeys = extra ? both + "|" + extra : both;
    this.respJson = jsonSecrets(both);
    this.reqJson = jsonSecrets(reqKeys);
    this.form = new RegExp(`(^|&)(${reqKeys})=[^&]*`, "gi");
  }

  /** JSON object of the headers (multiple values joined with ", "), sorted by
   * name, sensitive values replaced. */
  headers(raw: Record<string, string | string[] | undefined>): string {
    const out: Record<string, string> = {};
    for (const [name, value] of Object.entries(raw)) {
      if (value === undefined) continue;
      const key = canonicalHeader(name);
      const v = Array.isArray(value) ? value.join(", ") : value;
      out[key] = key in out ? out[key] + ", " + v : v;
    }
    const keys = Object.keys(out).sort();
    if (keys.length === 0) return "";
    const sorted: Record<string, string> = {};
    for (const k of keys) sorted[k] = this.headerSet.has(k.toLowerCase()) ? REDACTED : out[k]!;
    return JSON.stringify(sorted);
  }

  requestBody(body: Buffer, contentType: string): string {
    if (body.length === 0) return "";
    const text = body.toString("utf8");
    if (contentType.toLowerCase().includes("application/x-www-form-urlencoded")) {
      return text.replace(this.form, `$1$2=${REDACTED}`);
    }
    return text.replace(this.reqJson, `$1"${REDACTED}"`);
  }

  responseBody(body: Buffer): string {
    if (body.length === 0) return "";
    return body.toString("utf8").replace(this.respJson, `$1"${REDACTED}"`);
  }
}

/** The error code and key at dotted paths in a JSON error body. Bodies that
 * don't parse (e.g. truncated) fall back to a match on the last segment. */
export function errorFields(body: Buffer, codePath: string, keyPath: string): [string, string] {
  if (body.length === 0) return ["", ""];
  const text = body.toString("utf8");
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch {
    return [matchField(text, codePath), matchField(text, keyPath)];
  }
  return [lookup(doc, codePath), lookup(doc, keyPath)];
}

function lookup(doc: unknown, path: string): string {
  if (!path) return "";
  let v: unknown = doc;
  for (const seg of path.split(".")) {
    if (v === null || typeof v !== "object" || Array.isArray(v)) return "";
    v = (v as Record<string, unknown>)[seg];
  }
  if (typeof v === "string") return v;
  if (typeof v === "number" && Number.isFinite(v)) return plainDecimal(v);
  return "";
}

/** A number without exponent notation, like Go's FormatFloat(v, 'f', -1). */
function plainDecimal(v: number): string {
  const s = String(v);
  if (!/e/i.test(s)) return s;
  if (Number.isInteger(v)) return BigInt(v).toString();
  return v.toFixed(20).replace(/0+$/, "").replace(/\.$/, "");
}

function matchField(text: string, path: string): string {
  if (!path) return "";
  const last = path.slice(path.lastIndexOf(".") + 1);
  const m = new RegExp(`"${escapeRe(last)}"\\s*:\\s*"([^"]*)"`).exec(text);
  return m ? m[1]! : "";
}
