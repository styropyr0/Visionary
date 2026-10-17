import { ApiError, UNAUTHORIZED_EVENT } from "./api";

export interface StreamEvent {
  event: string;
  data: unknown;
}

/**
 * POSTs json and reads the server-sent event stream it answers with, calling
 * onEvent for each event. Resolves when the stream ends.
 */
export async function postStream(
  path: string,
  json: unknown,
  onEvent: (e: StreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify(json),
    signal,
  });
  if (!res.ok || !res.body) {
    let msg = res.statusText;
    try {
      const body = await res.json();
      if (body && typeof body.error === "string") msg = body.error;
    } catch {
      // keep the status text
    }
    if (res.status === 401) window.dispatchEvent(new CustomEvent<string>(UNAUTHORIZED_EVENT, { detail: msg }));
    throw new ApiError(res.status, msg);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let sep: number;
    while ((sep = buf.indexOf("\n\n")) >= 0) {
      const chunk = buf.slice(0, sep);
      buf = buf.slice(sep + 2);
      let event = "message";
      const data: string[] = [];
      for (const line of chunk.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (data.length === 0) continue;
      try {
        onEvent({ event, data: JSON.parse(data.join("\n")) });
      } catch {
        // ignore a malformed event rather than abort the stream
      }
    }
  }
}
