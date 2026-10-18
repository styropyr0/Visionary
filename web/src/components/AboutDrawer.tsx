import { useEffect, useMemo, useState } from "react";
import { ArrowUpCircle, BookOpen, Boxes, CheckCircle2, CircleAlert, ExternalLink, Github, Package, Rocket, Tag } from "lucide-react";
import { Drawer, CopyButton } from "./Modal";
import { BrandMark } from "./Sidebar";
import { formatDateTime, formatRelative } from "../lib/time";
import { integrations, REPO, versionLabel, type ProjectInfo } from "../lib/project";

const LINKS = [
  { icon: Github, label: "Source code", href: REPO },
  { icon: BookOpen, label: "Documentation", href: `${REPO}#readme` },
  { icon: Boxes, label: "SDK contract", href: `${REPO}/blob/main/sdks/SPEC.md` },
  { icon: CircleAlert, label: "Report an issue", href: `${REPO}/issues` },
];

const PACKAGES = [
  { name: "Server image", where: "ghcr.io/styropyr0/visionary", href: `${REPO}/pkgs/container/visionary` },
  { name: "Go SDK", where: "pkg.go.dev", href: "https://pkg.go.dev/github.com/styropyr0/visionary/sdks/go" },
  { name: "Android SDK", where: "JitPack", href: "https://jitpack.io/#styropyr0/Visionary" },
  { name: "Other SDKs", where: "Install from the repository", href: `${REPO}/tree/main/sdks` },
];

/** Version, releases, recent deployments, links and setup steps. */
export function AboutDrawer({
  info,
  error,
  onRefresh,
  onClose,
}: {
  info?: ProjectInfo;
  error?: Error;
  onRefresh: () => void;
  onClose: () => void;
}) {
  // The server checks GitHub in the background; ask again shortly while it does.
  const pending = info?.releases_pending;
  useEffect(() => {
    if (!pending) return;
    const id = window.setTimeout(onRefresh, 2500);
    return () => window.clearTimeout(id);
  }, [pending, info, onRefresh]);
  const running = versionLabel(info);
  const origin = window.location.origin;
  const guides = useMemo(() => integrations(origin, info?.latest || (info?.version ? (info.version.startsWith("v") ? info.version : `v${info.version}`) : "")), [origin, info?.latest, info?.version]);
  const [platform, setPlatform] = useState(guides[0].key);
  const guide = guides.find((g) => g.key === platform) ?? guides[0];

  return (
    <Drawer title="About Visionary" onClose={onClose}>
      <div className="about">
        <header className="about-head">
          <BrandMark />
          <div>
            <div className="about-name">
              Visionary {running && <span className="about-version">{running}</span>}
            </div>
            <UpdateStatus info={info} error={error} />
          </div>
        </header>

        <section className="about-section">
          <h3>
            <Tag size={14} /> Releases
          </h3>
          {info?.releases?.length ? (
            <ul className="about-list">
              {info.releases.slice(0, 5).map((r) => (
                <li key={r.tag}>
                  <a href={r.url} target="_blank" rel="noreferrer" className="mono">
                    {r.tag}
                  </a>
                  {(r.tag === info.version || r.tag === `v${info.version}`) && <span className="about-pill">Running</span>}
                  {r.tag === info.latest && r.tag !== info.version && <span className="about-pill new">Latest</span>}
                  <span className="dim" title={r.date ? formatDateTime(new Date(r.date)) : undefined}>
                    {r.date ? formatRelative(r.date) : ""}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="dim">{info?.releases_error ?? (!info || info.releases_pending ? "Checking for releases…" : "Release checks are turned off.")}</p>
          )}
        </section>

        <section className="about-section">
          <h3>
            <Rocket size={14} /> Your latest deployments
          </h3>
          {info?.deployments.length ? (
            <ul className="about-list">
              {info.deployments.map((d) => (
                <li key={`${d.service}@${d.version}`}>
                  <span className="about-service">{d.service || "Unnamed service"}</span>
                  <span className="mono" title={d.version}>
                    {d.version.length > 12 ? d.version.slice(0, 7) : d.version}
                  </span>
                  <span className="dim" title={formatDateTime(new Date(d.deployed_at))}>
                    {formatRelative(d.deployed_at)}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="dim">{info ? "None recorded yet. Each SDK records one when a service starts with a new version." : "Loading…"}</p>
          )}
        </section>

        <section className="about-section">
          <h3>
            <ExternalLink size={14} /> Links
          </h3>
          <div className="about-links">
            {LINKS.map(({ icon: Icon, label, href }) => (
              <a key={label} href={href} target="_blank" rel="noreferrer" className="about-link">
                <Icon size={15} />
                {label}
              </a>
            ))}
          </div>
        </section>

        <section className="about-section">
          <h3>
            <Package size={14} /> Packages
          </h3>
          <ul className="about-list">
            {PACKAGES.map((p) => (
              <li key={p.name}>
                <span className="about-service">{p.name}</span>
                <a href={p.href} target="_blank" rel="noreferrer">
                  {p.where}
                </a>
              </li>
            ))}
          </ul>
        </section>

        <section className="about-section">
          <h3>
            <Boxes size={14} /> Connect your app
          </h3>
          <div className="about-platforms" role="tablist" aria-label="Platform">
            {guides.map((g) => (
              <button type="button" key={g.key} role="tab" aria-selected={g.key === platform} className={g.key === platform ? "on" : ""} onClick={() => setPlatform(g.key)}>
                {g.name}
              </button>
            ))}
          </div>
          <p className="about-how">{guide.how}</p>
          {guide.steps.map((s) => (
            <div className="about-step" key={s.label}>
              <div className="about-step-head">
                <span>{s.label}</span>
                <CopyButton text={s.code} />
              </div>
              <pre className="mono">{s.code}</pre>
            </div>
          ))}
          <a href={guide.guide} target="_blank" rel="noreferrer" className="about-guide">
            Full {guide.name} guide <ExternalLink size={12} />
          </a>
        </section>
      </div>
    </Drawer>
  );
}

function UpdateStatus({ info, error }: { info?: ProjectInfo; error?: Error }) {
  if (error) return <div className="about-status dim">Could not load version details.</div>;
  if (!info || info.releases_pending) return <div className="about-status dim">Checking for updates…</div>;
  const commit = info.commit ? (
    <a href={`${REPO}/commit/${info.commit}`} target="_blank" rel="noreferrer" className="mono">
      {info.commit.slice(0, 7)}
    </a>
  ) : null;
  if (info.update_available && info.latest) {
    return (
      <div className="about-status warn">
        <ArrowUpCircle size={14} />
        <a href={`${REPO}/tree/${info.latest}`} target="_blank" rel="noreferrer">
          {info.latest} is available
        </a>
        {commit && <span className="dim">· running {commit}</span>}
      </div>
    );
  }
  if (info.latest && info.version) {
    return (
      <div className="about-status ok">
        <CheckCircle2 size={14} />
        Up to date{commit && <span className="dim"> · {commit}</span>}
      </div>
    );
  }
  return <div className="about-status dim">{commit ? <>Built from commit {commit}</> : "Development build"}</div>;
}
