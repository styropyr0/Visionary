export type Kind = "page_load" | "web_vital" | "api" | "error" | "custom" | "log";

export type Attrs = Record<string, string | number | boolean>;

/** One event, as sdks/SPEC.md "Client ingest API" defines it. */
export interface ClientEvent {
  ts: string;
  kind: Kind;
  name: string;
  url?: string;
  method?: string;
  status?: number;
  duration_ms?: number;
  value?: number;
  message?: string;
  stack?: string;
  attrs?: Attrs;
}

export interface InitOptions {
  /** The Visionary server, e.g. "https://visionary.example.com". */
  endpoint: string;
  /** A public client key (Settings → API keys, scope Client ingest). */
  key: string;
  /** Your app's version name, e.g. "2.4.1". */
  app_version?: string;
  /** Build or commit, e.g. "a1b2c3d4e5f6". */
  version?: string;
  /** Share of sessions (browser tabs) that report, 0 to 1. Default 1. */
  sampleRate?: number;
  user_id?: string | number | null;
  /** Defaults to "web". */
  app?: string;
  /** fetch/XHR calls to skip, by substring or pattern of the URL. */
  ignoreUrls?: (string | RegExp)[];
  /**
   * Other origins whose fetch/XHR calls get a W3C traceparent header, e.g.
   * "https://api.example.com" or /\.example\.com$/. The page's own origin
   * always does. Their CORS must allow the header (Access-Control-Allow-Headers).
   */
  traceOrigins?: (string | RegExp)[];
  /** Also send console lines to Logs (they still print). Off by default; at most 120 a minute. */
  captureConsole?: boolean;
  /** The lowest console level sent when captureConsole is on. Default "info". */
  logLevel?: LogLevel;
  /** Session replay; off unless set. The recorder is loaded only for sessions it records. */
  replay?: ReplayOptions;
}

export interface ReplayOptions {
  /** Share of sessions recorded from the start, 0 to 1. Default 0. */
  sampleRate?: number;
  /** Share of the other sessions whose last minute is sent when an error happens, 0 to 1. Default 0. */
  onErrorSampleRate?: number;
  /**
   * Input values are recorded as asterisks. Default true. Passwords, emails,
   * phone numbers, card fields and hidden inputs are never recorded either way.
   */
  maskInputs?: boolean;
  /** Record all text as asterisks. Default false; data-visionary-mask masks one element's text. */
  maskAllText?: boolean;
  /** Elements recorded as an empty box, besides those with data-visionary-block. */
  blockSelector?: string;
  /** Where the script-tag build loads the recorder from. Default: visionary-replay.min.js next to visionary.min.js. */
  src?: string;
}

export type LogLevel = "debug" | "info" | "warn" | "error";

export type Report = (e: Omit<ClientEvent, "ts"> & { ts?: string }) => void;
