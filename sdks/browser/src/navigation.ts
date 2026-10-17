import type { Report } from "./types";
import { bareUrl, normalizePath, now, round, safe } from "./util";

type Nav = PerformanceNavigationTiming & { activationStart?: number };

/** The hard page load, from Navigation Timing, once the load event is over. */
export function trackPageLoad(report: Report) {
  const run = safe(() => {
    const nav = performance.getEntriesByType("navigation")[0] as Nav | undefined;
    if (!nav) return;
    const fcp = performance.getEntriesByName("first-contentful-paint")[0];
    const attrs: Record<string, number | string> = {
      ttfb: round(nav.responseStart),
      dom_interactive: round(nav.domInteractive),
      type: nav.type,
    };
    if (fcp) attrs.fcp = round(fcp.startTime);
    report({
      kind: "page_load",
      name: normalizePath(location.pathname),
      url: bareUrl(location.href),
      duration_ms: round(nav.loadEventEnd || nav.duration),
      attrs,
    });
  });
  // loadEventEnd is only set after the load handlers have returned.
  if (document.readyState === "complete") setTimeout(run, 0);
  else addEventListener("load", () => setTimeout(run, 0), { once: true });
}

/**
 * Single-page app route changes, as page loads with soft timing: from the
 * history change until the DOM has been quiet for 100 ms after its last
 * change (at most 10 s). Routes that render asynchronously change nothing at
 * first, so with no change yet the wait is up to 2 s.
 */
export function trackRouteChanges(report: Report) {
  let path = location.pathname;
  let finish: (() => void) | undefined;

  const start = safe(() => {
    if (location.pathname === path) return;
    path = location.pathname;
    finish?.();
    const name = normalizePath(path);
    const url = bareUrl(location.href);
    const t0 = now();
    let last = t0;
    let changed = false;
    const mo = new MutationObserver(() => {
      last = now();
      changed = true;
    });
    mo.observe(document.documentElement, { childList: true, subtree: true, characterData: true });
    const timer = setInterval(() => {
      const t = now();
      if ((changed && t - last >= 100) || (!changed && t - t0 >= 2000) || t - t0 >= 10_000) finish?.();
    }, 50);
    finish = () => {
      finish = undefined;
      clearInterval(timer);
      mo.disconnect();
      report({ kind: "page_load", name, url, duration_ms: round(last - t0), attrs: { soft: 1 } });
    };
  });

  for (const method of ["pushState", "replaceState"] as const) {
    const original = history[method];
    history[method] = function (this: History, ...args: Parameters<History["pushState"]>) {
      const result = original.apply(this, args);
      start();
      return result;
    };
  }
  addEventListener("popstate", start);
}
