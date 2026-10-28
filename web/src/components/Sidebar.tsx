import { useEffect, useState } from "react";
import {
  Github,
  Activity,
  Bell,
  Bookmark,
  BookOpen,
  Bug,
  Database,
  Flame,
  Gauge,
  Network,
  Boxes,
  LayoutDashboard,
  LayoutGrid,
  LogOut,
  Logs,
  MonitorSmartphone,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  ScrollText,
  Server,
  Settings,
  ShieldAlert,
  Sparkles,
  Sun,
} from "lucide-react";
import type { SessionUser } from "../lib/api";
import { Modal } from "./Modal";
import { AboutDrawer } from "./AboutDrawer";
import { getProject, versionLabel } from "../lib/project";
import { useQuery } from "../lib/useQuery";
import { useApp } from "../lib/state";
import { LOGO_EYE, LOGO_SUN, LOGO_VIEWBOX } from "./logo";
import { useHoverTip } from "./InfoTip";
import { HELP } from "../lib/help";
import { useSettings } from "../lib/settings";
import { alertsApi, type AlertsSummary } from "../lib/alerts";

export type Route =
  | "overview"
  | "performance"
  | "queries"
  | "dependencies"
  | "profiles"
  | "system"
  | "containers"
  | "errors"
  | "frontend"
  | "uptime"
  | "security"
  | "explore"
  | "applogs"
  | "logs"
  | "dashboards"
  | "saved"
  | "alerts"
  | "settings"
  | "guide";

export const ROUTES: Route[] = ["overview", "performance", "queries", "dependencies", "profiles", "system", "containers", "errors", "frontend", "uptime", "security", "explore", "applogs", "logs", "dashboards", "saved", "alerts", "settings", "guide"];

export const NAV: { group: string; items: { route: Route; label: string; icon: typeof LayoutDashboard }[] }[] = [
  {
    group: "Monitor",
    items: [
      { route: "overview", label: "API overview", icon: LayoutDashboard },
      { route: "performance", label: "Performance", icon: Gauge },
      { route: "queries", label: "Queries", icon: Database },
      { route: "dependencies", label: "Dependencies", icon: Network },
      { route: "profiles", label: "CPU profiles", icon: Flame },
      { route: "system", label: "System", icon: Server },
      { route: "containers", label: "Containers", icon: Boxes },
      { route: "errors", label: "Errors", icon: Bug },
      { route: "frontend", label: "Frontend", icon: MonitorSmartphone },
      { route: "uptime", label: "Uptime", icon: Activity },
      { route: "security", label: "Security", icon: ShieldAlert },
    ],
  },
  {
    group: "Investigate",
    items: [
      { route: "explore", label: "Explore", icon: Sparkles },
      { route: "applogs", label: "Logs", icon: Logs },
      { route: "logs", label: "Request logs", icon: ScrollText },
      { route: "dashboards", label: "Dashboards", icon: LayoutGrid },
      { route: "saved", label: "Saved panels", icon: Bookmark },
    ],
  },
  {
    group: "Manage",
    items: [
      { route: "alerts", label: "Alerts", icon: Bell },
      { route: "settings", label: "Settings", icon: Settings },
      { route: "guide", label: "How to use", icon: BookOpen },
    ],
  },
];

interface Props {
  route: Route;
  collapsed: boolean;
  onToggle: () => void;
  onSignOut: () => void;
  user: SessionUser | null;
}

/** The sun-and-eye mark. Ink colors follow the theme (--logo-sun, --logo-eye). */
export function BrandMark() {
  return (
    <svg className="brand-mark" viewBox={LOGO_VIEWBOX} aria-hidden>
      <path fill="var(--logo-sun)" fillRule="evenodd" d={LOGO_SUN} />
      <path fill="var(--logo-eye)" fillRule="evenodd" d={LOGO_EYE} />
    </svg>
  );
}

/** A count on a page link: something there needs someone. */
interface NavBadge {
  count: number;
  /** urgent is red; otherwise neutral (firing but acknowledged). */
  urgent: boolean;
  title: string;
}

/** A page link whose tip says what the page is for. */
function NavItem({ route, label, icon: Icon, active, badge }: { route: Route; label: string; icon: typeof LayoutDashboard; active: boolean; badge?: NavBadge }) {
  const { props, bubble } = useHoverTip<HTMLAnchorElement>(
    <>
      <span className="tip-title">{label}</span>
      {badge && <span className="tip-badge">{badge.title}</span>}
      {HELP[route].tip}
    </>,
    "right",
  );
  return (
    <>
      <a {...props} href={`#/${route}`} className={`nav-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined}>
        <Icon size={16} />
        <span className="nav-text">{label}</span>
        {badge && (
          <span className={`nav-badge ${badge.urgent ? "urgent" : ""}`} aria-label={badge.title}>
            {badge.count > 99 ? "99+" : badge.count}
          </span>
        )}
      </a>
      {bubble}
    </>
  );
}

/** Firing alerts and down uptime checks, refreshed every 30 seconds while the page is open. */
function useAlertBadges(): Partial<Record<Route, NavBadge>> {
  const [summary, setSummary] = useState<AlertsSummary | null>(null);
  useEffect(() => {
    let ctrl = new AbortController();
    const load = () => {
      if (document.visibilityState !== "visible") return;
      ctrl.abort();
      ctrl = new AbortController();
      alertsApi
        .summary(ctrl.signal)
        .then(setSummary)
        .catch(() => undefined);
    };
    load();
    const id = window.setInterval(load, 30_000);
    document.addEventListener("visibilitychange", load);
    return () => {
      ctrl.abort();
      window.clearInterval(id);
      document.removeEventListener("visibilitychange", load);
    };
  }, []);
  const out: Partial<Record<Route, NavBadge>> = {};
  if (!summary) return out;
  if (summary.firing_series > 0) {
    const n = summary.unacked || summary.firing_series;
    out.alerts = {
      count: n,
      urgent: summary.unacked > 0,
      title: summary.unacked > 0 ? `${summary.unacked} firing alert${summary.unacked === 1 ? "" : "s"} nobody acknowledged` : `${summary.firing_series} firing, all acknowledged`,
    };
  }
  if (summary.checks_down > 0) {
    out.uptime = {
      count: summary.checks_down,
      urgent: true,
      title: `Down: ${summary.down.join(", ")}${summary.checks_down > summary.down.length ? ` and ${summary.checks_down - summary.down.length} more` : ""}`,
    };
  }
  return out;
}

/** The Visionary role (admin, editor, viewer) from the session. */
function roleLabel(user: SessionUser & { role?: string }): string {
  const r = user.role ?? "";
  return r ? r.charAt(0).toUpperCase() + r.slice(1) : "Signed in";
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const letters = parts.length > 1 ? parts[0][0] + parts[parts.length - 1][0] : (parts[0] ?? "?").slice(0, 2);
  return letters.toUpperCase();
}

export function Sidebar({ route, collapsed, onToggle, onSignOut, user }: Props) {
  const { theme, setTheme } = useApp();
  const [confirmSignOut, setConfirmSignOut] = useState(false);
  const [aboutOpen, setAboutOpen] = useState(false);
  // Fetched again each time the About panel opens, so it shows fresh deployments.
  const [projectRun, setProjectRun] = useState(0);
  const project = useQuery(`project#${projectRun}`, getProject);
  const version = versionLabel(project.data);
  // Pages the team hid in Settings → Pages; the open page always shows.
  const hidden = new Set(useSettings().settings?.hidden_pages ?? []);
  const badges = useAlertBadges();
  const shownNav = NAV.map(({ group, items }) => ({ group, items: items.filter((i) => i.route === route || !hidden.has(i.route)) })).filter((g) => g.items.length > 0);
  return (
    <aside className="sidebar">
      <div className="brand">
        <BrandMark />
        <div className="brand-text">
          <span className="brand-name">Visionary</span>
          <button type="button"
            className={`brand-version ${project.data?.update_available ? "update" : ""}`}
            onClick={() => {
              setAboutOpen(true);
              setProjectRun((n) => n + 1);
            }}
            title={project.data?.update_available ? `${project.data.latest} is available` : "About Visionary"}
            aria-label={`About Visionary${version ? `, ${version}` : ""}`}
          >
            {version || "About"}
            <Github size={12} aria-hidden />
          </button>
        </div>
      </div>
      {aboutOpen && <AboutDrawer info={project.data} error={project.error} onRefresh={() => setProjectRun((n) => n + 1)} onClose={() => setAboutOpen(false)} />}
      <nav className="nav" aria-label="Main">
        {shownNav.map(({ group, items }) => (
          <div className="nav-group" key={group}>
            <div className="nav-label">{group}</div>
            {items.map(({ route: r, label, icon }) => (
              <NavItem key={r} route={r} label={label} icon={icon} active={route === r} badge={badges[r]} />
            ))}
          </div>
        ))}
      </nav>
      <div className="sidebar-foot">
        <button type="button"
          className="nav-item"
          onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          title={collapsed ? "Toggle theme" : undefined}
        >
          {theme === "dark" ? <Sun size={16} /> : <Moon size={16} />}
          <span className="nav-text">{theme === "dark" ? "Light theme" : "Dark theme"}</span>
        </button>
        <button type="button" className="nav-item" onClick={onToggle} title={collapsed ? "Expand sidebar" : undefined}>
          {collapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}
          <span className="nav-text">Collapse</span>
        </button>
        <button type="button" className="nav-item danger" onClick={() => setConfirmSignOut(true)} title={collapsed ? "Sign out" : undefined}>
          <LogOut size={16} />
          <span className="nav-text">Sign out</span>
        </button>
        {confirmSignOut && (
          <Modal
            title="Sign out"
            narrow
            onClose={() => setConfirmSignOut(false)}
            footer={
              <>
                <button type="button" className="btn ghost" onClick={() => setConfirmSignOut(false)}>
                  Cancel
                </button>
                <button type="button" className="btn danger" autoFocus onClick={onSignOut}>
                  <LogOut size={14} />
                  Sign out
                </button>
              </>
            }
          >
            <p style={{ margin: 0 }}>Sign out of Visionary on this device? You’ll need to sign in again to see your data.</p>
          </Modal>
        )}
        {user && (
          <div className="user-chip" title={collapsed ? `${user.name} · ${roleLabel(user)}${user.handle ? ` · @${user.handle}` : ""}` : undefined}>
            <span className="avatar" aria-hidden>
              {initials(user.name || user.handle)}
            </span>
            <span className="nav-text user-text">
              <span className="user-name">{user.name || `@${user.handle}`}</span>
              <span className="user-role">{roleLabel(user)}{user.handle ? ` · @${user.handle}` : ""}</span>
            </span>
          </div>
        )}
      </div>
    </aside>
  );
}
