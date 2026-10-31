import { api, rangeQuery, type Result } from "./api";
import type { Release } from "./telemetry";
import type { RunBatch } from "./uptimeRuns";

export interface CIFilters {
  service: string;
  repo: string;
  workflow: string;
  branch: string;
}

export interface CISummary {
  totals: {
    runs: number;
    success: number;
    failure: number;
    cancelled: number;
    success_rate: number | null;
    p50_ms: number | null;
    p95_ms: number | null;
    queue_p50_ms: number | null;
    queue_p95_ms: number | null;
  };
  outcomes: Result;
  durations: Result;
  rate: Result;
  workflows: Result;
  slow_jobs: Result;
  fail_jobs: Result;
  options: Record<keyof CIFilters, string[]>;
}

export interface CIRun {
  id: number;
  provider: string;
  repo: string;
  workflow: string;
  run_id: string;
  number: number;
  attempt: number;
  event: string;
  branch: string;
  default_branch: string;
  commit: string;
  commit_message: string;
  actor: string;
  status: string;
  conclusion: string;
  url: string;
  service: string;
  created_at: string;
  started_at: string;
  finished_at: string;
  queue_ms: number | null;
  duration_ms: number | null;
  deploys: Release[] | null;
}

export interface CIStep {
  name: string;
  conclusion?: string;
  started_at?: string;
  finished_at?: string;
}

export interface CICounts {
  tests: number;
  passed: number;
  failed: number;
  skipped: number;
}

export interface CIJob {
  id: number;
  name: string;
  status: string;
  conclusion: string;
  runner: string;
  url: string;
  created_at: string;
  started_at: string;
  finished_at: string;
  queue_ms: number | null;
  duration_ms: number | null;
  steps: CIStep[];
  tests?: CICounts;
}

export interface CITest {
  hash: string;
  test: string;
  suite: string;
  file: string;
  job: string;
  status: string;
  duration_ms: number | null;
  message: string;
  flaky: boolean;
}

export interface Traffic {
  requests: number;
  p95_ms: number | null;
  error_rate: number | null;
  errors: number;
}

export interface CIDeploy extends Release {
  before?: Traffic;
  after?: Traffic;
}

export interface CIRunDetail {
  run: CIRun;
  jobs: CIJob[];
  tests: CICounts;
  failed: CITest[];
  slowest: CITest[];
  deploys: CIDeploy[];
  checks?: RunBatch[];
}

export interface CITests {
  totals: { tests: number; results: number; failures: number; skipped: number; failing_tests: number };
  results: Result;
  failing: Result;
  slowest: Result;
  slower: Result;
  midpoint: string;
}

export interface FlakyTest {
  hash: string;
  test: string;
  service: string;
  repo: string;
  file: string;
  runs: number;
  failures: number;
  commits: number;
  mixed_commits: number;
  flip_flops: number;
  score: number;
  reason: "same_commit" | "flip_flop";
  last_failed_at: string;
  last_message: string;
  flaky_at: string;
  first_flagged?: string;
  recent: string[];
}

export interface TestHistory {
  test: string;
  file: string;
  service: string;
  flaky: { at: string; reason: string; commit: string } | null;
  results: { at: string; status: string; duration_ms: number | null; branch: string; commit: string; job: string; message: string; run_id: string | null; workflow: string; number: number }[];
}

export interface CISetup {
  has_runs: boolean;
  has_tests: boolean;
  secret_set: boolean;
  webhook: string;
}

const q = (from: Date, to: Date, f: Partial<CIFilters> & Record<string, string | undefined>) =>
  rangeQuery(from, to, Object.fromEntries(Object.entries(f).map(([k, v]) => [k, v ?? ""])));

export const ciApi = {
  setup: (signal?: AbortSignal) => api<CISetup>("/api/ci/setup", { signal }),
  summary: (from: Date, to: Date, f: CIFilters, signal?: AbortSignal) => api<CISummary>(`/api/ci/summary?${q(from, to, { ...f })}`, { signal }),
  runs: (from: Date, to: Date, f: CIFilters & { outcome: string; q: string }, signal?: AbortSignal) =>
    api<{ runs: CIRun[] }>(`/api/ci/runs?${q(from, to, { ...f })}`, { signal }),
  run: (id: number, signal?: AbortSignal) => api<CIRunDetail>(`/api/ci/runs/${id}`, { signal }),
  tests: (from: Date, to: Date, f: CIFilters, signal?: AbortSignal) => api<CITests>(`/api/ci/tests?${q(from, to, { ...f, workflow: "" })}`, { signal }),
  flaky: (from: Date, to: Date, f: CIFilters, signal?: AbortSignal) =>
    api<{ tests: FlakyTest[]; new: number }>(`/api/ci/flaky?${q(from, to, { ...f, workflow: "" })}`, { signal }),
  history: (hash: string, signal?: AbortSignal) => api<TestHistory>(`/api/ci/tests/history?hash=${encodeURIComponent(hash)}`, { signal }),
  builds: (versions: string[], signal?: AbortSignal) =>
    api<{ builds: Record<string, CIRun> }>(`/api/ci/builds?${versions.map((v) => `version=${encodeURIComponent(v)}`).join("&")}`, { signal }),
};

export const runHref = (id: number) => `#/ci?run=${id}`;

export function outcomeOf(r: { status: string; conclusion: string }): string {
  return r.status === "completed" ? r.conclusion || "completed" : r.status;
}

export const OUTCOME_LABEL: Record<string, string> = {
  success: "Passed",
  failure: "Failed",
  timed_out: "Timed out",
  cancelled: "Cancelled",
  skipped: "Skipped",
  in_progress: "Running",
  queued: "Queued",
  neutral: "Neutral",
  completed: "Completed",
  passed: "Passed",
  failed: "Failed",
  error: "Error",
};

export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 ? `${m}m ${s % 60}s` : `${m}m`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

export const shortSha = (sha: string) => sha.slice(0, 7);

export function junitStep(origin: string): string {
  return `- name: Upload test results to Visionary
  if: \${{ !cancelled() }}
  run: |
    curl -sS --fail-with-body "${origin}/api/ingest/ci/junit" \\
      -H "Authorization: Bearer \${{ secrets.VISIONARY_KEY }}" \\
      -F provider=github -F repo=\${{ github.repository }} \\
      -F run_id=\${{ github.run_id }} -F attempt=\${{ github.run_attempt }} \\
      -F job=\${{ github.job }} -F service=my-api \\
      -F branch=\${{ github.head_ref || github.ref_name }} \\
      -F commit=\${{ github.event.pull_request.head.sha || github.sha }} \\
      -F file=@junit.xml`;
}
