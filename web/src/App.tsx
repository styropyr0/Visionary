import { useCallback, useEffect, useRef, useState } from "react";
import { ShieldAlert } from "lucide-react";
import { api, TWO_STEP_SETUP_EVENT, UNAUTHORIZED_EVENT, type Meta, type SessionInfo, type SessionUser } from "./lib/api";
import { TwoStepRequired } from "./components/TwoStep";
import { load, save } from "./lib/state";
import { useQuery } from "./lib/useQuery";
import { ROUTES, Sidebar, type Route } from "./components/Sidebar";
import { HelpHost } from "./components/Help";
import { loadPrefs, prefs } from "./lib/prefs";
import { MetaContext } from "./lib/meta";
import { clearSession, refreshSession } from "./lib/team";
import { Explore } from "./views/Explore";
import { Login } from "./views/Login";
import { Logs } from "./views/Logs";
import { AppLogs } from "./views/AppLogs";
import { Overview } from "./views/Overview";
import { Performance } from "./views/Performance";
import { Queries } from "./views/Queries";
import { Dependencies } from "./views/Dependencies";
import { Profiles } from "./views/Profiles";
import { Saved } from "./views/Saved";
import { System } from "./views/System";
import { Alerts } from "./views/Alerts";
import { Dashboards } from "./views/Dashboards";
import { Errors } from "./views/Errors";
import { Frontend } from "./views/Frontend";
import { Settings } from "./views/Settings";
import { Uptime } from "./views/Uptime";
import { Containers } from "./views/Containers";
import { Security } from "./views/Security";
import { Guide } from "./views/Guide";


function readRoute(): Route {
  // "#/dashboards/12?from=..." is the dashboards view; the rest is the view's own.
  const r = window.location.hash.replace(/^#\/?/, "").split(/[?/]/)[0] as Route;
  return ROUTES.includes(r) ? r : "overview";
}

type Auth = "checking" | "in" | "out";

// The plain "not signed in" answer needs no explanation on the sign-in screen;
// anything else (role removed, session ended) is shown there.
const SIGN_IN_REQUIRED = "sign in required";

// Opened at the root: the start page from Settings → Preferences applies.
const openedAtRoot = !window.location.hash || window.location.hash === "#/" || window.location.hash === "#";

// Every tab of the dashboard shares one session cookie. A tab that signs in or
// out says so here, and the others follow instead of keeping the previous
// user's role until they are reloaded.
const SESSION_CHANNEL = "va.session";

function openSessionChannel(): BroadcastChannel | null {
  try {
    return new BroadcastChannel(SESSION_CHANNEL);
  } catch {
    return null;
  }
}

export function App() {
  const [auth, setAuth] = useState<Auth>("checking");
  const [user, setUser] = useState<SessionUser | null>(null);
  const [notice, setNotice] = useState("");
  // The server refused a request until two-step sign-in is set up.
  const [needsTwoStep, setNeedsTwoStep] = useState(false);
  const [route, setRoute] = useState<Route>(readRoute);
  const [collapsed, setCollapsed] = useState(() => load("va.sidebar", false));
  // Bumped when another tab changes who is signed in, so every page mounts
  // again and asks the server what this user may do.
  const [sessionKey, setSessionKey] = useState(0);
  const channel = useRef<BroadcastChannel | null>(null);

  useEffect(() => {
    api<SessionInfo>("/api/session")
      .then((s) => {
        setUser(s.user);
        setAuth("in");
      })
      .catch(() => setAuth("out"));
    const onUnauth = (e: Event) => {
      const msg = (e as CustomEvent<string>).detail;
      setNotice(msg && msg.toLowerCase() !== SIGN_IN_REQUIRED ? msg : "");
      clearSession();
      setUser(null);
      setAuth("out");
    };
    const onHash = () => setRoute(readRoute());
    const onTwoStep = () => setNeedsTwoStep(true);
    window.addEventListener(TWO_STEP_SETUP_EVENT, onTwoStep);
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauth);
    window.addEventListener("hashchange", onHash);
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauth);
      window.removeEventListener(TWO_STEP_SETUP_EVENT, onTwoStep);
      window.removeEventListener("hashchange", onHash);
    };
  }, []);

  useEffect(() => {
    const ch = openSessionChannel();
    channel.current = ch;
    if (!ch) return;
    ch.onmessage = () => {
      // A 401 here signs this tab out through UNAUTHORIZED_EVENT.
      api<SessionInfo>("/api/session")
        .then((s) => {
          refreshSession();
          setNotice("");
          setUser(s.user);
          setAuth("in");
          setSessionKey((k) => k + 1);
        })
        .catch(() => undefined);
    };
    return () => {
      ch.close();
      channel.current = null;
    };
  }, []);

  // The server's copy of the preferences, once signed in. A different time
  // zone or clock than this browser's copy needs every time drawn again.
  useEffect(() => {
    if (auth !== "in") return;
    void loadPrefs().then((redraw) => {
      if (redraw) window.location.reload();
      const start = prefs().startPage;
      if (openedAtRoot && start && ROUTES.includes(start as Route) && readRoute() === "overview") window.location.hash = `#/${start}`;
    });
  }, [auth, sessionKey]);

  const signOut = useCallback(async () => {
    await api("/api/logout", { method: "POST", json: {} }).catch(() => undefined);
    clearSession();
    channel.current?.postMessage("signed-out");
    setNotice("");
    setUser(null);
    setAuth("out");
  }, []);

  const meta = useQuery<Meta>(auth === "in" ? "meta" : null, (signal) => api<Meta>("/api/meta", { signal }));

  if (auth === "checking") return null;
  if (auth === "out") {
    return (
      <Login
        notice={notice}
        onSignedIn={(s) => {
          // Pages read the role from the shared session, which still holds
          // the previous user's until it is loaded again.
          refreshSession();
          channel.current?.postMessage("signed-in");
          setNotice("");
          setUser(s.user);
          setAuth("in");
        }}
      />
    );
  }

  return (
    <div className={`shell ${collapsed ? "collapsed" : ""}`}>
      <Sidebar
        route={route}
        collapsed={collapsed}
        onToggle={() => {
          setCollapsed(!collapsed);
          save("va.sidebar", !collapsed);
        }}
        onSignOut={signOut}
        user={user}
      />
      <MetaContext.Provider value={meta.data}>
        <main className="main" key={sessionKey}>
          {needsTwoStep && (
            <TwoStepRequired
              onSignOut={signOut}
              onDone={() => {
                setNeedsTwoStep(false);
                setSessionKey((k) => k + 1);
              }}
            />
          )}
          {!needsTwoStep && meta.data?.sign_in_open && <OpenBanner why={meta.data.sign_in_open} />}
{!needsTwoStep && (
            <>
            {route === "overview" && <Overview />}
            {route === "performance" && <Performance meta={meta.data} />}
            {route === "queries" && <Queries />}
            {route === "dependencies" && <Dependencies />}
            {route === "profiles" && <Profiles />}
            {route === "system" && <System />}
            {route === "containers" && <Containers />}
            {route === "errors" && <Errors />}
            {route === "frontend" && <Frontend />}
            {route === "uptime" && <Uptime />}
            {route === "security" && <Security />}
            {route === "dashboards" && <Dashboards />}
            {route === "alerts" && <Alerts meta={meta.data} />}
            {route === "settings" && <Settings user={user} />}
            {route === "explore" && <Explore meta={meta.data} />}
            {route === "saved" && <Saved />}
            {route === "applogs" && <AppLogs />}
            {route === "logs" && <Logs meta={meta.data} />}
            {route === "guide" && <Guide />}
            </>
          )}
        </main>
      </MetaContext.Provider>
      <HelpHost route={route} />
    </div>
  );
}

/** Says, on every page, that anyone with the URL is an admin. */
function OpenBanner({ why }: { why: "disabled" | "unconfigured" }) {
  return (
    <div className="open-banner" role="status">
      <ShieldAlert size={15} />
      {why === "disabled" ? (
        <span>
          Authentication is off (<code>VISIONARY_ENABLE_AUTH=false</code>), so anyone who can open this page is an admin. Once sign-in works, remove the variable and restart the server.
        </span>
      ) : (
        <span>Sign-in isn't set up: anyone who can reach this server is an admin.</span>
      )}
      <a className="btn sm" href="#/settings?tab=signin">
        {why === "disabled" ? "Sign-in settings" : "Set up sign-in"}
      </a>
    </div>
  );
}
