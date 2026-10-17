import { describeDevice } from "./device";
import { errorEvent, trackErrors } from "./errors";
import { trackPageLoad, trackRouteChanges } from "./navigation";
import { trackNetwork } from "./network";
import { Transport } from "./transport";
import type { Attrs, ClientEvent, InitOptions, Report } from "./types";
import { bareUrl, onHidden, randomId, safe } from "./util";
import { startVitals } from "./vitals";

export type { Attrs, InitOptions } from "./types";

let transport: Transport | null = null;
let started = false;

const report: Report = (e) => {
  transport?.push({ ts: new Date().toISOString(), ...e } as ClientEvent);
};

function sessionId(): string {
  try {
    let id = sessionStorage.getItem("vis.sid");
    if (!id) sessionStorage.setItem("vis.sid", (id = randomId()));
    return id;
  } catch {
    return randomId();
  }
}

/** Whether this session reports; decided once per tab so a session is whole. */
function sampled(sid: string, rate: number): boolean {
  if (rate >= 1) return true;
  if (!(rate > 0)) return false;
  try {
    const kept = sessionStorage.getItem("vis.sample");
    if (kept && kept.startsWith(sid + ":")) return kept.endsWith(":1");
    const yes = Math.random() < rate;
    sessionStorage.setItem("vis.sample", `${sid}:${yes ? 1 : 0}`);
    return yes;
  } catch {
    return Math.random() < rate;
  }
}

/** Starts collecting. Call once, as early as possible in the page. */
export function init(options: InitOptions): void {
  if (started || typeof window === "undefined") return;
  started = true;
  safe(() => {
    if (!options?.endpoint || !options.key) throw new Error("endpoint and key are required");
    const base = options.endpoint.replace(/\/+$/, "");
    const url = /\/api\/ingest\/client$/.test(base) ? base : `${base}/api/ingest/client`;
    const sid = sessionId();
    if (!sampled(sid, options.sampleRate ?? 1)) return;

    const { device, os } = describeDevice();
    const originalFetch = window.fetch;
    transport = new Transport(
      url,
      options.key,
      {
        app: options.app ?? "web",
        app_version: options.app_version,
        version: options.version,
        session_id: sid,
        user_id: options.user_id == null ? undefined : String(options.user_id),
        device,
        os,
      },
      originalFetch,
    );

    const ignore = options.ignoreUrls ?? [];
    const own = new URL(url, location.href).href;
    trackNetwork(report, (u) => {
      const abs = bareUrl(u);
      return abs === own || ignore.some((p) => (typeof p === "string" ? u.includes(p) : p.test(u)));
    });
    trackErrors(report);
    trackPageLoad(report);
    trackRouteChanges(report);
    startVitals(report, bareUrl(location.href));
    // Vitals are reported on hide, so this listener goes last.
    onHidden(() => transport?.flush(true));
  })();
}

/** Attaches a user to the events from now on; null clears it. */
export function setUser(id: string | number | null): void {
  if (!transport) return;
  transport.flush();
  transport.envelope.user_id = id == null ? undefined : String(id);
}

/** Records a custom event, e.g. track("checkout_step", 2, { step: "address" }). */
export function track(name: string, value?: number, attrs?: Attrs): void {
  report({ kind: "custom", name: String(name), value, url: bareUrl(location.href), attrs });
}

/** Records a handled error. */
export function captureException(err: unknown, attrs?: Attrs): void {
  const e = errorEvent(err, attrs);
  if (e) report(e);
}

/** Sends what is queued now. */
export function flush(): void {
  transport?.flush();
}

const Visionary = { init, setUser, track, captureException, flush };
export default Visionary;
