const ID = /^(\d+|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{16,})$/i;

/**
 * Replaces id-like path segments with :id, as sdks/SPEC.md defines them (the
 * mobile SDKs and the server use the same rule): all digits, a UUID, 16 or
 * more hex characters, or 20 or more characters containing a digit.
 */
export function normalizePath(path: string): string {
  return path
    .split("/")
    .map((s) => (s && (ID.test(s) || (s.length >= 20 && /\d/.test(s))) ? ":id" : s))
    .join("/");
}

/** The URL without query string or fragment, which may hold tokens. */
export function bareUrl(href: string): string {
  try {
    const u = new URL(href, location.href);
    return u.origin + u.pathname;
  } catch {
    return href.split(/[?#]/)[0];
  }
}

export const now = () => performance.now();

export const round = (n: number, d = 1) => {
  const p = 10 ** d;
  return Math.round(n * p) / p;
};

export function randomId(): string {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

export function safe<A extends unknown[]>(fn: (...a: A) => void): (...a: A) => void {
  return (...a) => {
    try {
      fn(...a);
    } catch {
      // Monitoring must never break the page.
    }
  };
}

/** Runs fn whenever the page is hidden or unloaded (both may fire). */
export function onHidden(fn: () => void) {
  const run = () => {
    if (document.visibilityState === "hidden") fn();
  };
  addEventListener("visibilitychange", run, true);
  addEventListener("pagehide", fn, true);
}
