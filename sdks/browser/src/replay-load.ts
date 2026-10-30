import type { ReplayContext, ReplayHandle } from "./replay";
import type { ReplayOptions } from "./types";

declare const __IIFE__: boolean;

// Read at load time: document.currentScript is only set while the script runs.
const scriptSrc = typeof document !== "undefined" ? (document.currentScript as HTMLScriptElement | null)?.src || "" : "";

let handle: ReplayHandle | null = null;
let errored = false;

/**
 * The session's replay mode, decided once per tab: "1" records it all, "e"
 * keeps the last minute in memory for an error, "x" is an "e" session that
 * had one and now records it all, "" records nothing.
 */
function replayMode(sid: string, o: ReplayOptions): string {
  try {
    const kept = sessionStorage.getItem("vis.replay");
    if (kept && kept.startsWith(sid + ":")) return kept.slice(sid.length + 1);
  } catch {
    // No storage: decide for this page only.
  }
  const mode = Math.random() < (o.sampleRate ?? 0) ? "1" : Math.random() < (o.onErrorSampleRate ?? 0) ? "e" : "";
  try {
    sessionStorage.setItem("vis.replay", `${sid}:${mode}`);
  } catch {
    // As above.
  }
  return mode;
}

type Module = { record: (ctx: ReplayContext) => ReplayHandle };

function loadScript(o: ReplayOptions): Promise<Module> {
  return new Promise((ok, fail) => {
    const s = document.createElement("script");
    s.src = o.src || new URL("visionary-replay.min.js", scriptSrc || location.href).href;
    s.async = true;
    s.onload = () => {
      const g = (window as unknown as { VisionaryReplay?: Module }).VisionaryReplay;
      if (g) ok(g);
      else fail();
    };
    s.onerror = fail;
    document.head.appendChild(s);
  });
}

// The script-tag build has no chunks to import, so it loads a second script.
const load = (o: ReplayOptions): Promise<Module> => (__IIFE__ ? loadScript(o) : import("./replay"));

/** Loads the recorder when this session is to be recorded. */
export function startReplay(ctx: Omit<ReplayContext, "mode">) {
  const mode = replayMode(ctx.sid, ctx.options);
  if (!mode) return;
  load(ctx.options).then(
    (m) => {
      handle = m.record({ ...ctx, mode });
      if (errored) handle.error();
    },
    () => {},
  );
}

/** Tells the recorder an error happened, which sends an error-sampled session's buffer. */
export function replayError() {
  errored = true;
  handle?.error();
}
