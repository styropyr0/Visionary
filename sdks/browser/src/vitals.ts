import type { Report } from "./types";
import { onHidden, round, safe } from "./util";

// Google's thresholds: good up to the first value, poor above the second.
const THRESHOLDS: Record<string, [number, number]> = {
  LCP: [2500, 4000],
  INP: [200, 500],
  CLS: [0.1, 0.25],
  FCP: [1800, 3000],
  TTFB: [800, 1800],
};

export function rating(name: string, value: number): string {
  const [good, poor] = THRESHOLDS[name];
  return value <= good ? "good" : value <= poor ? "needs-improvement" : "poor";
}

type Entry = PerformanceEntry & {
  value?: number;
  hadRecentInput?: boolean;
  interactionId?: number;
  activationStart?: number;
  responseStart?: number;
};

function observe(type: string, cb: (entries: Entry[]) => void, opts: Record<string, unknown> = {}) {
  try {
    if (!PerformanceObserver.supportedEntryTypes?.includes(type)) return;
    const po = new PerformanceObserver(safe((list: PerformanceObserverEntryList) => cb(list.getEntries() as Entry[])));
    po.observe({ type, buffered: true, ...opts } as PerformanceObserverInit);
    return po;
  } catch {
    return;
  }
}

/**
 * Web Vitals with PerformanceObserver, following web.dev's definitions:
 * LCP is final at the first input or when the page is hidden; CLS is the
 * largest session window of layout shifts (gaps under 1 s, at most 5 s);
 * INP is the worst interaction, ignoring one in every 50. Each vital is
 * reported once per page load.
 */
export function startVitals(report: Report, pageUrl: string) {
  const sent = new Set<string>();
  const send = (name: string, value: number) => {
    if (sent.has(name) || !(value >= 0)) return;
    sent.add(name);
    const v = name === "CLS" ? round(value, 4) : round(value, 1);
    report({ kind: "web_vital", name, value: v, url: pageUrl, attrs: { rating: rating(name, v) } });
  };

  const nav = performance.getEntriesByType("navigation")[0] as Entry | undefined;
  const activation = nav?.activationStart ?? 0;
  // Vitals of a tab loaded in the background are not what a user saw.
  let hiddenAt = document.visibilityState === "hidden" ? 0 : Infinity;
  addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") hiddenAt = Math.min(hiddenAt, performance.now());
  }, true);

  if (nav?.responseStart) send("TTFB", Math.max(0, nav.responseStart - activation));

  observe("paint", (es) => {
    for (const e of es) {
      if (e.name === "first-contentful-paint" && e.startTime < hiddenAt) send("FCP", Math.max(0, e.startTime - activation));
    }
  });

  let lcp = -1;
  const onLCP = (es: Entry[]) => {
    const e = es[es.length - 1];
    if (e && e.startTime < hiddenAt) lcp = Math.max(0, e.startTime - activation);
  };
  const lcpObserver = observe("largest-contentful-paint", onLCP);
  const finalLCP = safe(() => {
    if (!lcpObserver || sent.has("LCP")) return;
    onLCP(lcpObserver.takeRecords() as Entry[]);
    lcpObserver.disconnect();
    if (lcp >= 0) send("LCP", lcp);
  });
  for (const t of ["keydown", "pointerdown"]) addEventListener(t, () => setTimeout(finalLCP, 0), { once: true, capture: true });

  let cls = 0;
  let windowValue = 0;
  let windowStart = 0;
  let lastShift = 0;
  const onShifts = (es: Entry[]) => {
    for (const e of es) {
      if (e.hadRecentInput) continue;
      if (windowValue && e.startTime - lastShift < 1000 && e.startTime - windowStart < 5000) {
        windowValue += e.value ?? 0;
      } else {
        windowValue = e.value ?? 0;
        windowStart = e.startTime;
      }
      lastShift = e.startTime;
      cls = Math.max(cls, windowValue);
    }
  };
  const clsObserver = observe("layout-shift", onShifts);

  // The ten longest interactions are enough for the 98th percentile of up
  // to 500 interactions.
  const longest: { id: number; d: number }[] = [];
  let interactions = 0;
  const seen = new Set<number>();
  const onEvents = (es: Entry[]) => {
    for (const e of es) {
      const id = e.interactionId;
      if (!id) continue;
      if (!seen.has(id)) {
        seen.add(id);
        interactions++;
      }
      const known = longest.find((x) => x.id === id);
      if (known) known.d = Math.max(known.d, e.duration);
      else longest.push({ id, d: e.duration });
      longest.sort((a, b) => b.d - a.d);
      longest.length = Math.min(longest.length, 10);
    }
  };
  observe("event", onEvents, { durationThreshold: 40 });
  observe("first-input", onEvents);

  onHidden(
    safe(() => {
      finalLCP();
      if (clsObserver) {
        onShifts(clsObserver.takeRecords() as Entry[]);
        send("CLS", cls);
      }
      if (longest.length) send("INP", longest[Math.min(longest.length - 1, Math.floor(interactions / 50))].d);
    }),
  );
}
