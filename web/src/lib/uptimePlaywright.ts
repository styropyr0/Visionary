import { api, rangeQuery } from "./api";
import type { CheckState, StepStat, UptimeCheck } from "./uptime";

export interface PlaywrightRunSummary {
  id: number;
  created_at: string;
  ok: boolean;
  status: string;
  outcome: string;
  retry: number;
  duration_ms: number | null;
  error: string;
  failed_step: string;
  steps: number;
  browser: string;
  project: string;
  trace_id: string;
  release: string;
  commit: string;
  screenshot: boolean;
  trace: boolean;
}

export interface PlaywrightStep {
  title: string;
  category?: string;
  duration_ms: number;
  error?: string;
  depth?: number;
}

export interface PlaywrightAttachment {
  name: string;
  content_type: string;
  size: number;
  stored?: boolean;
}

export interface PlaywrightRunDetail extends Omit<PlaywrightRunSummary, "failed_step" | "steps" | "screenshot"> {
  started_at: string;
  steps: PlaywrightStep[];
  attachments: PlaywrightAttachment[];
  screenshot: string;
}

export const getPlaywrightRuns = (id: number, from: Date, to: Date, signal?: AbortSignal) =>
  api<{ runs: PlaywrightRunSummary[]; steps: StepStat[] }>(`/api/uptime/checks/${id}/playwright?${rangeQuery(from, to)}`, { signal });

export const getPlaywrightRun = (id: number, run: number, signal?: AbortSignal) => api<PlaywrightRunDetail>(`/api/uptime/checks/${id}/playwright/${run}`, { signal });

export const traceURL = (id: number, run: number) => `/api/uptime/checks/${id}/playwright/${run}/trace`;

const STALE_PREFIX = "No run reported in ";

export function isStale(c: UptimeCheck, s: CheckState | undefined): boolean {
  return c.kind === "playwright" && s?.state === "down" && !!s.last_error?.startsWith(STALE_PREFIX);
}

export const PLAYWRIGHT_INTERVALS = [300, 600, 900, 1800, 3600, 7200, 21600, 43200, 86400, 604800];
