import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Check, Copy, X } from "lucide-react";
import type { Result } from "../lib/api";
import { formatCount } from "../lib/format";

function useEscape(onClose: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
}

interface ModalProps {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  narrow?: boolean;
}

export function Modal({ title, onClose, children, footer, narrow }: ModalProps) {
  useEscape(onClose);
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className={`modal ${narrow ? "narrow" : ""}`} role="dialog" aria-modal="true" aria-label={title}>
        <div className="modal-head">
          <h2>{title}</h2>
          <button type="button" className="icon-btn" style={{ marginLeft: "auto" }} onClick={onClose} aria-label="Close">
            <X size={16} />
          </button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </>,
    document.body,
  );
}

export function Drawer({ title, onClose, children, actions }: { title: ReactNode; onClose: () => void; children: ReactNode; actions?: ReactNode }) {
  useEscape(onClose);
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-modal="true">
        <div className="drawer-head">
          <h2>{title}</h2>
          <div style={{ marginLeft: "auto", display: "flex", gap: 4 }}>
            {actions}
            <button type="button" className="icon-btn" onClick={onClose} aria-label="Close">
              <X size={16} />
            </button>
          </div>
        </div>
        <div className="drawer-body">{children}</div>
      </aside>
    </>,
    document.body,
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button type="button"
      className="btn ghost sm"
      onClick={() => {
        navigator.clipboard
          ?.writeText(text)
          .then(() => {
            setDone(true);
            window.setTimeout(() => setDone(false), 1400);
          })
          .catch(() => undefined);
      }}
    >
      {done ? <Check size={14} /> : <Copy size={14} />}
      {done ? "Copied" : label}
    </button>
  );
}

export function InspectModal({ title, result, onClose }: { title: string; result: Result; onClose: () => void }) {
  return (
    <Modal title={`Query · ${title}`} onClose={onClose}>
      <div className="meta-line" style={{ border: 0, padding: 0 }}>
        <span>{formatCount(result.rows.length, false)} rows returned{result.truncated ? " (truncated)" : ""}</span>
        <span>{formatCount(result.rows_read, false)} rows read</span>
        <span>{result.duration_ms.toFixed(1)} ms in D1</span>
        {result.bucket > 0 && <span>bucket {result.bucket}s</span>}
      </div>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <span className="section-label">SQL</span>
        <CopyButton text={result.sql} />
      </div>
      <pre className="code mono">{result.sql.trim()}</pre>
    </Modal>
  );
}
