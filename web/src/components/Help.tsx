import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";
import { BookOpen, CircleHelp, ExternalLink, ListChecks, Wrench } from "lucide-react";
import type { Route } from "./Sidebar";
import { CopyButton, Drawer } from "./Modal";
import { HELP, LANGS, TOPICS, type Lang, type LangSetup } from "../lib/help";
import { REPO } from "../lib/project";
import { usePref } from "../lib/prefs";
import { load } from "../lib/state";

const OPEN_EVENT = "va:help";

/** Opens the help drawer of the current page, scrolled to a setup step. */
export function openHelp(topic?: string) {
  window.dispatchEvent(new CustomEvent<string | undefined>(OPEN_EVENT, { detail: topic }));
}

/** Text with `code` spans. */
export function rich(text: string): ReactNode {
  return text.split(/`([^`]+)`/).map((part, i) => (i % 2 ? <code key={i}>{part}</code> : <Fragment key={i}>{part}</Fragment>));
}

/** The top bar's button. */
export function HelpButton() {
  return (
    <button type="button" className="btn ghost sm help-btn" onClick={() => openHelp()} title="What this page shows and how to fill it">
      <CircleHelp size={14} />
      <span>Help</span>
    </button>
  );
}

/** A link in an empty state to the setup step that would fill it. */
export function HelpLink({ topic, children }: { topic: string; children: ReactNode }) {
  return (
    <button type="button" className="help-link" onClick={() => openHelp(topic)}>
      <Wrench size={12} />
      {children}
    </button>
  );
}

/** Mounted once; shows the drawer for the route open when it is asked for. */
export function HelpHost({ route }: { route: Route }) {
  const [open, setOpen] = useState<{ topic?: string } | null>(null);
  useEffect(() => {
    const onOpen = (e: Event) => setOpen({ topic: (e as CustomEvent<string | undefined>).detail });
    window.addEventListener(OPEN_EVENT, onOpen);
    return () => window.removeEventListener(OPEN_EVENT, onOpen);
  }, []);
  // Another page has other help.
  useEffect(() => setOpen(null), [route]);
  return open ? <HelpDrawer route={route} topic={open.topic} onClose={() => setOpen(null)} /> : null;
}

function HelpDrawer({ route, topic, onClose }: { route: Route; topic?: string; onClose: () => void }) {
  const page = HELP[route];
  const ids = topic && !page.setup.includes(topic) ? [topic, ...page.setup] : page.setup;
  const topics = ids.map((id) => TOPICS[id]).filter(Boolean);
  const [lang, pick] = usePref("helpLang", load<string>("va.help.lang", "go")) as [Lang, (l: Lang) => void];
  const focused = useRef<HTMLElement>(null);

  useEffect(() => {
    focused.current?.scrollIntoView({ block: "start" });
  }, []);

  return (
    <Drawer title={`Help: ${page.title}`} onClose={onClose} className="help-drawer">
      <div className="about help">
        <section className="about-section">
          <h3>
            <BookOpen size={14} /> What this page shows
          </h3>
          <p>{rich(page.what)}</p>
        </section>

        {page.read.length > 0 && (
          <section className="about-section">
            <h3>
              <ListChecks size={14} /> How to read it
            </h3>
            <ul className="help-list">
              {page.read.map((r) => (
                <li key={r}>{rich(r)}</li>
              ))}
            </ul>
          </section>
        )}

        <section className="about-section">
          <h3>
            <Wrench size={14} /> What it needs
          </h3>
          {topics.length === 0 ? (
            <p className="dim">Nothing in your code: this page works from what the Visionary server already has, and you set it up here.</p>
          ) : (
            <>
              {topics.some((t) => t.langs) && (
                <div className="about-platforms help-langs" role="tablist" aria-label="Language">
                  {LANGS.map((l) => (
                    <button type="button" key={l.key} role="tab" aria-selected={l.key === lang} className={l.key === lang ? "on" : ""} onClick={() => pick(l.key)}>
                      {l.name}
                    </button>
                  ))}
                </div>
              )}
              {topics.map((t) => (
                <article key={t.id} ref={t.id === topic ? focused : undefined} className={`help-topic ${t.id === topic ? "focus" : ""}`}>
                  <h4>{t.title}</h4>
                  <p>{rich(t.intro)}</p>
                  <HowTo setup={t.any ?? t.langs?.[lang]} lang={t.any ? undefined : LANGS.find((l) => l.key === lang)?.name} />
                </article>
              ))}
            </>
          )}
        </section>

        <a href={`${REPO}#readme`} target="_blank" rel="noreferrer" className="about-guide">
          Full integration guide <ExternalLink size={12} />
        </a>
      </div>
    </Drawer>
  );
}

function HowTo({ setup, lang }: { setup?: LangSetup; lang?: string }) {
  if (!setup) return <p className="help-missing">Not available in the {lang} SDK yet.</p>;
  return (
    <>
      {setup.note && <p className="help-note">{rich(setup.note)}</p>}
      {setup.code && (
        <div className="about-step">
          <div className="about-step-head">
            <span>{setup.label ?? lang ?? "Run"}</span>
            <CopyButton text={setup.code} />
          </div>
          <pre className="mono">{setup.code}</pre>
        </div>
      )}
    </>
  );
}
