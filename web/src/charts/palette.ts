export interface ChartTokens {
  series: string[];
  surface: string;
  surface2: string;
  border: string;
  grid: string;
  axis: string;
  text1: string;
  text2: string;
  text3: string;
  hover: string;
  status: { good: string; info: string; warning: string; critical: string };
}

// Read from CSS so charts switch with the theme. Called again whenever the
// theme changes.
export function readTokens(): ChartTokens {
  const css = getComputedStyle(document.documentElement);
  const v = (name: string) => css.getPropertyValue(name).trim();
  return {
    series: [1, 2, 3, 4, 5, 6, 7, 8].map((i) => v(`--series-${i}`)),
    surface: v("--surface-1"),
    surface2: v("--surface-2"),
    border: v("--border-strong"),
    grid: v("--grid"),
    axis: v("--axis"),
    text1: v("--text-1"),
    text2: v("--text-2"),
    text3: v("--text-3"),
    hover: v("--hover-wash"),
    status: { good: v("--good"), info: v("--series-1"), warning: v("--warning"), critical: v("--critical") },
  };
}

/**
 * Status meaning gets status colors; everything else takes categorical slots
 * in fixed order. Returns null when the name has no status meaning.
 */
export function statusColor(name: string, t: ChartTokens): string | null {
  const n = name.toLowerCase();
  if (/^2\d\d$|^2xx$/.test(n)) return t.status.good;
  if (/^3\d\d$|^3xx$/.test(n)) return t.status.info;
  if (/^4\d\d$|^4xx$|client_error/.test(n)) return t.status.warning;
  if (/^5\d\d$|^5xx$|server_error/.test(n)) return t.status.critical;
  return null;
}
