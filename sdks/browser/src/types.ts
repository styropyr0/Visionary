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
   * Sends console.debug/log/info/warn/error lines to Logs, still printing
   * them. Off by default. At most 120 lines a minute are sent.
   */
  captureConsole?: boolean;
  /** The lowest console level sent when captureConsole is on. Default "info". */
  logLevel?: LogLevel;
}

export type LogLevel = "debug" | "info" | "warn" | "error";

export type Report = (e: Omit<ClientEvent, "ts"> & { ts?: string }) => void;
