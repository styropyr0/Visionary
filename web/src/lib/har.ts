import type { BrowserRunDetail, NetRequest } from "./uptime";

/** HAR 1.2 from a run's recorded requests. Headers and bodies are not kept, so they are empty. */
export function runHar(run: BrowserRunDetail, checkName: string) {
  const t0 = new Date(run.created_at).getTime();
  const query = (url: string) => {
    try {
      return [...new URL(url).searchParams].map(([name, value]) => ({ name, value }));
    } catch {
      return [];
    }
  };
  const entry = (r: NetRequest) => ({
    pageref: "page_1",
    startedDateTime: new Date(t0 + r.start_ms).toISOString(),
    time: r.duration_ms,
    request: { method: r.method || "GET", url: r.url, httpVersion: "", cookies: [], headers: [], queryString: query(r.url), headersSize: -1, bodySize: -1 },
    response: {
      status: r.status ?? 0,
      statusText: r.failed ? r.error ?? "" : "",
      httpVersion: "",
      cookies: [],
      headers: [],
      content: { size: r.size ?? 0, mimeType: r.mime ?? "" },
      redirectURL: "",
      headersSize: -1,
      bodySize: r.size ?? -1,
    },
    cache: {},
    timings: { send: 0, wait: r.duration_ms, receive: 0 },
    _resourceType: (r.type ?? "").toLowerCase(),
    ...(r.failed ? { _error: r.error ?? "failed" } : {}),
  });
  return {
    log: {
      version: "1.2",
      creator: { name: "Visionary", version: "1" },
      pages: [{ startedDateTime: new Date(t0).toISOString(), id: "page_1", title: checkName, pageTimings: { onContentLoad: -1, onLoad: -1 } }],
      entries: run.requests.map(entry),
    },
  };
}

export function downloadHar(run: BrowserRunDetail, checkName: string) {
  const blob = new Blob([JSON.stringify(runHar(run, checkName), null, 2)], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = `${checkName.replace(/[^\w.-]+/g, "-").replace(/^-|-$/g, "") || "run"}-${run.created_at.replace(/[:.]/g, "-")}.har`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
