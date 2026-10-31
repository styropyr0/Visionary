/** The visionary_settings name of the Settings → Data skip list. */
export const SKIP_SETTING = "requests.skip";
const POLL_MS = 30_000;

/** Reads the stored value; anything but a JSON array of strings skips nothing. */
export function parseSkip(value: string): string[] {
  let v: unknown;
  try {
    v = JSON.parse(value);
  } catch {
    return [];
  }
  if (!Array.isArray(v) || !v.every((p) => typeof p === "string")) return [];
  return v.map((p: string) => p.trim()).filter((p) => p !== "");
}

/** Whether path equals an entry, or starts with one ending in "*". */
export function matchSkip(list: readonly string[], path: string): boolean {
  for (const p of list) {
    if (p.endsWith("*") ? path.startsWith(p.slice(0, -1)) : path === p) return true;
  }
  return false;
}

/** Keeps the skip list fresh, off the request path. */
export class SkipList {
  private list: string[] = [];
  private timer?: NodeJS.Timeout;
  private polling = false;
  private stopped = false;

  constructor(private readonly read: () => Promise<string[][]>) {}

  start(): void {
    this.timer = setInterval(() => void this.poll(), POLL_MS);
    this.timer.unref();
    void this.poll();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearInterval(this.timer);
  }

  apply(rows: string[][]): void {
    let list: string[] = [];
    for (const [name, value] of rows) if (name === SKIP_SETTING) list = parseSkip(value ?? "");
    this.list = list;
  }

  async poll(): Promise<void> {
    if (this.polling || this.stopped) return;
    this.polling = true;
    try {
      this.apply(await this.read());
    } catch {
      // no server or table yet: keep the last list
    } finally {
      this.polling = false;
    }
  }

  has(path: string): boolean {
    return this.list.length > 0 && matchSkip(this.list, path);
  }
}
