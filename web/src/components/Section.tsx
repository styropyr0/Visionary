import type { ReactNode } from "react";

/** A full-width heading that groups the panels after it. */
export function Section({ title, text }: { title: string; text?: ReactNode }) {
  return (
    <div className="span-12 section-title">
      <h2>{title}</h2>
      {text && <p>{text}</p>}
    </div>
  );
}
