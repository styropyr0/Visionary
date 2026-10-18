import { useCallback, useEffect, useState } from "react";
import { api, UNAUTHORIZED_EVENT, type Meta, type SessionInfo, type SessionUser } from "./lib/api";
import { load, save } from "./lib/state";
import { useQuery } from "./lib/useQuery";
import { ROUTES, Sidebar, type Route } from "./components/Sidebar";
import { Explore } from "./views/Explore";
import { Login } from "./views/Login";
import { Logs } from "./views/Logs";
import { Overview } from "./views/Overview";
import { Performance } from "./views/Performance";
import { Saved } from "./views/Saved";
import { System } from "./views/System";
import { Alerts } from "./views/Alerts";
import { Dashboards } from "./views/Dashboards";
import { Errors } from "./views/Errors";
import { Frontend } from "./views/Frontend";
import { Settings } from "./views/Settings";
import { Uptime } from "./views/Uptime";
import { Hosts } from "./views/Hosts";


function readRoute(): Route {
  // "#/dashboards/12?from=..." is the dashboards view; the rest is the view's own.
  const r = window.location.hash.replace(/^#\/?/, "").split(/[?/]/)[0] as Route;
  return ROUTES.includes(r) ? r : "overview";
}

type Auth = "checking" | "in" | "out";

// The plain "not signed in" answer needs no explanation on the sign-in screen;
// anything else (role removed, session ended) is shown there.
const SIGN_IN_REQUIRED = "sign in required";

export function App() {
  const [auth, setAuth] = useState<Auth>("checking");
  const [user, setUser] = useState<SessionUser | null>(null);
  const [notice, setNotice] = useState("");
  const [route, setRoute] = useState<Route>(readRoute);
  const [collapsed, setCollapsed] = useState(() => load("va.sidebar", false));

  useEffect(() => {
    api<SessionInfo>("/api/session")
      .then((s) => {
        setUser(s.user);
        setAuth("in");
      })
      .catch(() => setAuth("out"));
    const onUnauth = (e: Event) => {
      const msg = (e as CustomEvent<string>).detail;
      setNotice(msg && msg !== SIGN_IN_REQUIRED ? msg : "");
      setUser(null);
      setAuth("out");
    };
    const onHash = () => setRoute(readRoute());
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauth);
    window.addEventListener("hashchange", onHash);
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauth);
      window.removeEventListener("hashchange", onHash);
    };
  }, []);

  const signOut = useCallback(async () => {
    await api("/api/logout", { method: "POST", json: {} }).catch(() => undefined);
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
      <main className="main">
        {route === "overview" && <Overview />}
        {route === "performance" && <Performance meta={meta.data} />}
        {route === "system" && <System />}
        {route === "hosts" && <Hosts />}
        {route === "errors" && <Errors />}
        {route === "frontend" && <Frontend />}
        {route === "uptime" && <Uptime />}
        {route === "dashboards" && <Dashboards />}
        {route === "alerts" && <Alerts meta={meta.data} />}
        {route === "settings" && <Settings user={user} />}
        {route === "explore" && <Explore meta={meta.data} />}
        {route === "saved" && <Saved />}
        {route === "logs" && <Logs meta={meta.data} />}
      </main>
    </div>
  );
}
