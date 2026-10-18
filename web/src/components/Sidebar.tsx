import { useState } from "react";
import {
  Activity,
  Bell,
  Bookmark,
  Bug,
  Gauge,
  LayoutDashboard,
  LayoutGrid,
  LogOut,
  MonitorSmartphone,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  ScrollText,
  Server,
  Settings,
  Sparkles,
  Sun,
} from "lucide-react";
import type { SessionUser } from "../lib/api";
import { Modal } from "./Modal";
import { useApp } from "../lib/state";
import { LOGO_EYE, LOGO_SUN, LOGO_VIEWBOX } from "./logo";

export type Route =
  | "overview"
  | "performance"
  | "system"
  | "errors"
  | "frontend"
  | "uptime"
  | "explore"
  | "logs"
  | "dashboards"
  | "saved"
  | "alerts"
  | "settings";

export const ROUTES: Route[] = ["overview", "performance", "system", "errors", "frontend", "uptime", "explore", "logs", "dashboards", "saved", "alerts", "settings"];

const NAV: { group: string; items: { route: Route; label: string; icon: typeof LayoutDashboard }[] }[] = [
  {
    group: "Monitor",
    items: [
      { route: "overview", label: "API overview", icon: LayoutDashboard },
      { route: "performance", label: "Performance", icon: Gauge },
      { route: "system", label: "System", icon: Server },
      { route: "errors", label: "Errors", icon: Bug },
      { route: "frontend", label: "Frontend", icon: MonitorSmartphone },
      { route: "uptime", label: "Uptime", icon: Activity },
    ],
  },
  {
    group: "Investigate",
    items: [
      { route: "explore", label: "Explore", icon: Sparkles },
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
  return (
    <aside className="sidebar">
      <div className="brand">
        <BrandMark />
        <div className="brand-text">
          <span className="brand-name">Visionary</span>
        </div>
      </div>
      <nav className="nav" aria-label="Main">
        {NAV.map(({ group, items }) => (
          <div className="nav-group" key={group}>
            <div className="nav-label">{group}</div>
            {items.map(({ route: r, label, icon: Icon }) => (
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
          </div>
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
        <button className="nav-item danger" onClick={() => setConfirmSignOut(true)} title={collapsed ? "Sign out" : undefined}>
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
                <button className="btn ghost" onClick={() => setConfirmSignOut(false)}>
                  Cancel
                </button>
                <button className="btn danger" autoFocus onClick={onSignOut}>
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
