import { useEffect, useState } from "react";
import { api } from "./api";

/** Team-wide settings (Settings → Profiling and Pages), shared by every viewer. */
export interface ProfileSettings {
  enabled: boolean;
  services: string[];
  interval_s: number;
  duration_ms: number;
  /** When "Profile now" was last pressed. */
  now?: string;
  updated_at?: string;
}

export interface TeamSettings {
  profile: ProfileSettings;
  hidden_pages: string[];
  can_admin: boolean;
  can_edit: boolean;
}

export interface ProfiledService {
  service: string;
  sdks: string[];
  instances: number;
  last_seen: string;
  last_profile?: string;
  profiles: number;
}

// One copy for the whole app, so the sidebar follows a change made in Settings.
let cached: TeamSettings | undefined;
let pending: Promise<TeamSettings> | undefined;
const listeners = new Set<(s: TeamSettings) => void>();

function publish(s: TeamSettings): TeamSettings {
  cached = s;
  listeners.forEach((l) => l(s));
  return s;
}

export function loadSettings(force = false): Promise<TeamSettings> {
  if (cached && !force) return Promise.resolve(cached);
  pending ??= api<TeamSettings>("/api/settings")
    .then(publish)
    .finally(() => (pending = undefined));
  return pending;
}

export function useSettings(): { settings?: TeamSettings; error?: Error } {
  const [settings, setSettings] = useState(cached);
  const [error, setError] = useState<Error>();
  useEffect(() => {
    let live = true;
    const l = (s: TeamSettings) => live && setSettings(s);
    listeners.add(l);
    loadSettings()
      .then(l)
      .catch((e: Error) => live && setError(e));
    return () => {
      live = false;
      listeners.delete(l);
    };
  }, []);
  return { settings, error };
}

export const saveProfileSettings = (p: Pick<ProfileSettings, "enabled" | "services" | "interval_s" | "duration_ms">) =>
  api<TeamSettings>("/api/settings/profile", { method: "PUT", json: p }).then(publish);

export const profileNow = () => api<TeamSettings>("/api/settings/profile/now", { method: "POST", json: {} }).then(publish);

export const saveHiddenPages = (hidden: string[]) => api<TeamSettings>("/api/settings/pages", { method: "PUT", json: { hidden_pages: hidden } }).then(publish);

export const profiledServices = (signal?: AbortSignal) => api<{ services: ProfiledService[] }>("/api/settings/profile/services", { signal });

/** What the server itself runs on: Settings → Thresholds, Data and Status page. */
export interface RouteApdex {
  method: string;
  route: string;
  ms: number;
}

export interface ServerSettings {
  apdex: { default_ms: number; routes: RouteApdex[] };
  slo_target: number;
  retention: Record<string, number>;
  status_page: boolean;
  status_title: string;
}

export interface RetentionKind {
  key: string;
  label: string;
  description: string;
  default_days: number;
}

export interface ServerSettingsView {
  settings: ServerSettings;
  retention_kinds: RetentionKind[];
  can_admin: boolean;
}

export const getServerSettings = (signal?: AbortSignal) => api<ServerSettingsView>("/api/settings/server", { signal });

export const saveThresholds = (apdex: ServerSettings["apdex"], slo_target: number) =>
  api<ServerSettingsView>("/api/settings/thresholds", { method: "PUT", json: { apdex, slo_target } });

export const saveRetention = (retention: Record<string, number>) => api<ServerSettingsView>("/api/settings/retention", { method: "PUT", json: { retention } });

export const retentionUsage = (signal?: AbortSignal) => api<{ usage: Record<string, { rows: number; oldest: string }> }>("/api/settings/retention/usage", { signal });

export const saveStatusPage = (enabled: boolean, title: string) => api<ServerSettingsView>("/api/settings/status-page", { method: "PUT", json: { enabled, title } });
