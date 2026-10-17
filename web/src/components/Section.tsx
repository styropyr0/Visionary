/** A full-width heading that groups the panels after it. */
export function Section({ title, text }: { title: string; text?: string }) {
  return (
    <div className="span-12 section-title">
      <h2>{title}</h2>
      {text && <p>{text}</p>}
    </div>
  );
}
