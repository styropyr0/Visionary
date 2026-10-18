import type { CSSProperties, ReactNode } from "react";
import { AlertTriangle, ExternalLink, RotateCw } from "lucide-react";
import { useApp } from "../lib/state";

const URL_RE = /https?:\/\/[^\s<>"')]+/g;

/** Message text with any URLs turned into short links (host and path, no scheme). */
export function linkify(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(URL_RE)) {
    // A sentence's closing punctuation is not part of the link.
    const url = m[0].replace(/[.,;:!?]+$/, "");
    const at = m.index ?? 0;
    out.push(text.slice(last, at));
    const label = url.replace(/^https?:\/\//, "").replace(/\/$/, "");
    out.push(
      <a key={at} href={url} target="_blank" rel="noreferrer" className="error-link">
        {label}
        <ExternalLink size={12} aria-hidden />
      </a>,
    );
    last = at + url.length;
  }
  out.push(text.slice(last));
  return out;
}

/**
 * A request that failed: what could not load, the server's reason, and a retry.
 * Retry re-runs the page's queries unless onRetry says otherwise; pass
 * onRetry={null} where retrying makes no sense.
 */
export function ErrorNotice({
  title,
  error,
  onRetry,
  className = "",
  style,
}: {
  title: string;
  error: Error | string;
  onRetry?: (() => void) | null;
  className?: string;
  style?: CSSProperties;
}) {
  const { refresh } = useApp();
  const message = typeof error === "string" ? error : error.message;
  const retry = onRetry === undefined ? refresh : onRetry;
  return (
    <div className={`error-notice ${className}`} role="alert" style={style}>
      <span className="error-notice-icon">
        <AlertTriangle size={16} />
      </span>
      <div className="error-notice-body">
        <strong>{title}</strong>
        {message && <p>{linkify(message)}</p>}
      </div>
      {retry && (
        <button className="btn" onClick={retry}>
          <RotateCw size={14} />
          Retry
        </button>
      )}
    </div>
  );
}
