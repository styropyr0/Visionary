import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowRight, BookOpen, ListChecks, Rocket, Search, X } from "lucide-react";
import { NAV } from "../components/Sidebar";
import { TopBar } from "../components/Toolbar";
import { rich } from "../components/Help";
import { HELP } from "../lib/help";
import { GUIDE, type GuideSection, type GuideStep } from "../lib/guide";
import "./Guide.css";

// Screenshots ship with the build; a section whose file is missing just has no picture.
const SHOTS = import.meta.glob("../assets/guide/*.webp", { eager: true, query: "?url", import: "default" }) as Record<string, string>;
const shot = (name?: string) => (name ? SHOTS[`../assets/guide/${name}.webp`] : undefined);

/** Guide text: **labels** in bold, `code` as code. */
function text(t: string): ReactNode {
  return t.split(/(\*\*[^*]+\*\*|`[^`]+`)/).map((part, i) =>
    part.startsWith("**") ? <strong key={i} className="guide-ui">{part.slice(2, -2)}</strong> : part.startsWith("`") ? <code key={i}>{part.slice(1, -1)}</code> : <Fragment key={i}>{part}</Fragment>,
  );
}

const ICONS = Object.fromEntries(NAV.flatMap((g) => g.items.map((i) => [i.route, i.icon])));

/** The section named in "#/guide/alerts". */
function hashSection(): string {
  return window.location.hash.replace(/^#\/?guide\/?/, "").split("?")[0];
}

function matches(s: GuideSection, q: string): boolean {
  if (!q) return true;
  const help = s.route ? HELP[s.route] : undefined;
  const text = [s.title, s.intro, help?.what, ...(help?.read ?? []), ...s.steps.flatMap((st) => [st.title, st.text])].join(" ").toLowerCase();
  return q
    .toLowerCase()
    .split(/\s+/)
    .every((w) => text.includes(w));
}

export function Guide() {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(GUIDE[0].id);
  const [zoom, setZoom] = useState<{ src: string; caption: string } | null>(null);
  const body = useRef<HTMLDivElement>(null);
  const shown = useMemo(() => GUIDE.filter((s) => matches(s, query)), [query]);

  // Open at the section in the link, and follow the table of contents.
  useEffect(() => {
    const go = () => {
      const id = hashSection();
      if (id) document.getElementById(`guide-${id}`)?.scrollIntoView({ block: "start" });
    };
    go();
    window.addEventListener("hashchange", go);
    return () => window.removeEventListener("hashchange", go);
  }, []);

  // The section at the top of the page is the one marked in the contents.
  useEffect(() => {
    const root = body.current;
    if (!root) return;
    const io = new IntersectionObserver(
      (entries) => {
        const top = entries.filter((e) => e.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
        if (top) setActive(top.target.id.replace(/^guide-/, ""));
      },
      { root, rootMargin: "0px 0px -70% 0px" },
    );
    root.querySelectorAll(".guide-section").forEach((el) => io.observe(el));
    return () => io.disconnect();
  }, [shown]);

  const groups = useMemo(() => {
    const out: { group: string; items: GuideSection[] }[] = [];
    for (const s of shown) {
      const last = out[out.length - 1];
      if (last && last.group === s.group) last.items.push(s);
      else out.push({ group: s.group, items: [s] });
    }
    return out;
  }, [shown]);

  return (
    <>
      <TopBar title="How to use" help={false} />
      <div className="content guide" ref={body}>
        <nav className="guide-toc" aria-label="Contents">
          <label className="guide-search">
            <Search size={13} />
            <input className="input" value={query} placeholder="Search the guide" onChange={(e) => setQuery(e.target.value)} aria-label="Search the guide" />
            {query && (
              <button type="button" className="icon-btn" onClick={() => setQuery("")} aria-label="Clear search">
                <X size={13} />
              </button>
            )}
          </label>
          {groups.map(({ group, items }) => (
            <div className="guide-toc-group" key={group}>
              <div className="nav-label">{group}</div>
              {items.map((s) => {
                const Icon = s.route ? ICONS[s.route] : Rocket;
                return (
                  <a key={s.id} href={`#/guide/${s.id}`} className={`guide-toc-item ${active === s.id ? "active" : ""}`} aria-current={active === s.id ? "location" : undefined}>
                    <Icon size={14} />
                    {s.title}
                  </a>
                );
              })}
            </div>
          ))}
          {shown.length === 0 && <p className="dim guide-none">Nothing matches “{query}”.</p>}
        </nav>

        <div className="guide-body">
          {shown.map((s) => (
            <Section key={s.id} section={s} onZoom={setZoom} />
          ))}
        </div>
      </div>
      {zoom && (
        <div className="guide-zoom" role="dialog" aria-modal="true" aria-label={zoom.caption} onClick={() => setZoom(null)}>
          <button type="button" className="icon-btn" aria-label="Close" onClick={() => setZoom(null)}>
            <X size={16} />
          </button>
          <img src={zoom.src} alt={zoom.caption} />
          <p>{zoom.caption}</p>
        </div>
      )}
    </>
  );
}

function Section({ section: s, onZoom }: { section: GuideSection; onZoom: (z: { src: string; caption: string }) => void }) {
  const help = s.route ? HELP[s.route] : undefined;
  const Icon = s.route ? ICONS[s.route] : Rocket;
  return (
    <section className="panel guide-section" id={`guide-${s.id}`} aria-labelledby={`guide-h-${s.id}`}>
      <header className="guide-head">
        <span className="guide-icon" aria-hidden>
          <Icon size={18} />
        </span>
        <div>
          <h2 id={`guide-h-${s.id}`}>{s.title}</h2>
          <p>{text(s.intro)}</p>
        </div>
        {s.route && (
          <a className="btn sm" href={`#/${s.route}`}>
            Open {s.title} <ArrowRight size={13} />
          </a>
        )}
      </header>

      <Figure name={s.shot} caption={s.caption ?? s.title} onZoom={onZoom} />

      {help && (
        <div className="guide-block">
          <h3>
            <BookOpen size={14} /> What it shows
          </h3>
          <p>{rich(help.what)}</p>
        </div>
      )}

      {s.steps.length > 0 && (
        <div className="guide-block">
          <h3>
            <ListChecks size={14} /> How to use it
          </h3>
          <ol className="guide-steps">
            {s.steps.map((st) => (
              <Step key={st.title} step={st} onZoom={onZoom} />
            ))}
          </ol>
        </div>
      )}

      {help && help.read.length > 0 && (
        <div className="guide-block">
          <h3>Good to know</h3>
          <ul className="help-list">
            {help.read.map((r) => (
              <li key={r}>{rich(r)}</li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

function Step({ step, onZoom }: { step: GuideStep; onZoom: (z: { src: string; caption: string }) => void }) {
  return (
    <li>
      <span className="guide-step-title">{step.title}</span>
      <p>{text(step.text)}</p>
      <Figure name={step.shot} caption={step.caption ?? step.title} onZoom={onZoom} />
    </li>
  );
}

function Figure({ name, caption, onZoom }: { name?: string; caption: string; onZoom: (z: { src: string; caption: string }) => void }) {
  const src = shot(name);
  if (!src) return null;
  return (
    <figure className="guide-figure">
      <button type="button" onClick={() => onZoom({ src, caption })} aria-label={`Enlarge: ${caption}`}>
        <img src={src} alt={caption} loading="lazy" width={1440} height={1000} />
      </button>
      <figcaption>{caption}</figcaption>
    </figure>
  );
}
