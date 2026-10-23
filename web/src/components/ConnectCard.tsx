import { Cable } from "lucide-react";
import { LANGS, TOPICS, type Lang } from "../lib/help";
import { usePref } from "../lib/prefs";
import { load } from "../lib/state";
import { rich } from "./Help";
import { SetupCard, SetupCommand, SetupHelpButton, SetupOption } from "./SetupCard";

/**
 * Shown instead of a page's empty panels while no service has sent it
 * anything: what the page will show, then the two steps to connect the SDK,
 * in the language picked here or in Help.
 */
export function ConnectCard({ title, text }: { title: string; text: string }) {
  const [lang, pick] = usePref("helpLang", load<string>("va.help.lang", "go")) as [Lang, (l: Lang) => void];
  const setup = TOPICS.sdk.langs?.[lang];
  const env = `VISIONARY_DATABASE_URL=<the database this server reads>
VISIONARY_SERVICE=my-api`;
  return (
    <SetupCard icon={<Cable size={20} />} title={title} text={text} topic="sdk">
      <SetupOption title="1. Add the SDK to your service" text={setup?.note ? rich(setup.note) : "Install the package, then add its middleware first, before your routes."}>
        <div className="about-platforms connect-langs" role="tablist" aria-label="Language">
          {LANGS.map((l) => (
            <button type="button" key={l.key} role="tab" aria-selected={l.key === lang} className={l.key === lang ? "on" : ""} onClick={() => pick(l.key)}>
              {l.name}
            </button>
          ))}
        </div>
        {setup?.code && <SetupCommand code={setup.code} />}
      </SetupOption>
      <SetupOption
        title="2. Point it at Visionary"
        text="Set these in the service's environment and deploy. Requests show up here within a few seconds of the first one it serves."
      >
        <SetupCommand code={env} />
        <div className="connect-more">
          <SetupHelpButton topic="db">Also record queries and outbound calls</SetupHelpButton>
        </div>
      </SetupOption>
    </SetupCard>
  );
}
