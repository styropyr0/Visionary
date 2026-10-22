import type { ReactNode } from "react";
import { BookOpen, KeyRound } from "lucide-react";
import { CopyButton } from "./Modal";
import { openHelp } from "./Help";

/**
 * What an empty page needs, kept short: a title, one sentence, and each way
 * to fill it side by side with its action. The explanation and every
 * language's code live in the Help drawer, which the footer link opens at
 * the right step.
 */
export function SetupCard({ icon, title, text, topic, children, className = "" }: { icon: ReactNode; title: string; text: ReactNode; topic: string; children: ReactNode; className?: string }) {
  return (
    <section className={`panel sc ${className}`}>
      <header className="sc-head">
        <div className="sc-icon">{icon}</div>
        <div>
          <h2 className="sc-title">{title}</h2>
          <p className="sc-text">{text}</p>
        </div>
      </header>
      <div className="sc-options">{children}</div>
      <footer className="sc-foot">
        <button type="button" className="help-link" onClick={() => openHelp(topic)}>
          <BookOpen size={12} />
          Step-by-step setup and what it records
        </button>
      </footer>
    </section>
  );
}

/** One way to fill the page. */
export function SetupOption({ title, text, children }: { title: string; text: ReactNode; children?: ReactNode }) {
  return (
    <div className="sc-option">
      <h3 className="sc-option-title">{title}</h3>
      <p className="sc-option-text">{text}</p>
      {children && <div className="sc-option-action">{children}</div>}
    </div>
  );
}

/** A command to copy. */
export function SetupCommand({ code }: { code: string }) {
  return (
    <div className="sc-code">
      <pre className="mono">{code}</pre>
      <CopyButton text={code} />
    </div>
  );
}

/** The host agent's two steps: a server key, then the install command. */
export function AgentSteps() {
  const install = `curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \\
  | sudo VISIONARY_URL=${window.location.origin} VISIONARY_KEY=<server key> sh`;
  return (
    <ol className="sc-steps">
      <li>
        <a className="btn sm" href="#/settings?tab=keys">
          <KeyRound size={13} /> Create a server key
        </a>
      </li>
      <li>
        <span className="dim">Run on the machine, with the key:</span>
        <SetupCommand code={install} />
      </li>
    </ol>
  );
}

/** Opens Help at a setup step, as a button. */
export function SetupHelpButton({ topic, children }: { topic: string; children: ReactNode }) {
  return (
    <button type="button" className="btn sm" onClick={() => openHelp(topic)}>
      {children}
    </button>
  );
}
