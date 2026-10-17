import { Bookmark, Gauge, LayoutDashboard, LogOut, Moon, PanelLeftClose, PanelLeftOpen, ScrollText, Server, Sparkles, Sun } from "lucide-react";
import type { SessionUser } from "../lib/api";
import { useApp } from "../lib/state";
import { LOGO_EYE, LOGO_SUN, LOGO_VIEWBOX } from "./logo";

export type Route = "overview" | "performance" | "system" | "explore" | "saved" | "logs";

const NAV: { route: Route; label: string; icon: typeof LayoutDashboard }[] = [
  { route: "overview", label: "API overview", icon: LayoutDashboard },
  { route: "performance", label: "Performance", icon: Gauge },
  { route: "system", label: "System", icon: Server },
  { route: "explore", label: "Explore", icon: Sparkles },
  { route: "saved", label: "Saved panels", icon: Bookmark },
  { route: "logs", label: "Request logs", icon: ScrollText },
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

function roleLabel(roles: string[]): string {
  const r = roles[0] ?? "";
  return r ? r.charAt(0) + r.slice(1).toLowerCase() : "Signed in";
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const letters = parts.length > 1 ? parts[0][0] + parts[parts.length - 1][0] : (parts[0] ?? "?").slice(0, 2);
  return letters.toUpperCase();
}

export function Sidebar({ route, collapsed, onToggle, onSignOut, user }: Props) {
  const { theme, setTheme } = useApp();
  return (
    <aside className="sidebar">
      <div className="brand">
        <BrandMark />
        <div className="brand-text">
          <span className="brand-name">Visionary</span>
        </div>
      </div>
      <nav className="nav" aria-label="Main">
        <div className="nav-label">Dashboards</div>
        {NAV.map(({ route: r, label, icon: Icon }) => (
          <a
            key={r}
            href={`#/${r}`}
            className={`nav-item ${route === r ? "active" : ""}`}
            aria-current={route === r ? "page" : undefined}
            title={collapsed ? label : undefined}
          >
            <Icon size={16} />
            <span className="nav-text">{label}</span>
          </a>
        ))}
      </nav>
      <div className="sidebar-foot">
        <button
          className="nav-item"
          onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          title={collapsed ? "Toggle theme" : undefined}
        >
          {theme === "dark" ? <Sun size={16} /> : <Moon size={16} />}
          <span className="nav-text">{theme === "dark" ? "Light theme" : "Dark theme"}</span>
        </button>
        <button className="nav-item" onClick={onToggle} title={collapsed ? "Expand sidebar" : undefined}>
          {collapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}
          <span className="nav-text">Collapse</span>
        </button>
        <button className="nav-item" onClick={onSignOut} title={collapsed ? "Sign out" : undefined}>
          <LogOut size={16} />
          <span className="nav-text">Sign out</span>
        </button>
        {user && (
          <div className="user-chip" title={collapsed ? `${user.name} · @${user.handle}` : undefined}>
            <span className="avatar" aria-hidden>
              {initials(user.name || user.handle)}
            </span>
            <span className="nav-text user-text">
              <span className="user-name">{user.name || `@${user.handle}`}</span>
              <span className="user-role">{roleLabel(user.roles)}{user.handle ? ` · @${user.handle}` : ""}</span>
            </span>
          </div>
        )}
      </div>
    </aside>
  );
}
