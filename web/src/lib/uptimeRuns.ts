import { api } from "./api";

export interface RunItem {
  check_id: number;
  check_name: string;
  kind: string;
  status: "queued" | "running" | "passed" | "failed" | "error" | "skipped";
  url?: string;
  started_at?: string;
  finished_at?: string;
  duration_ms: number | null;
  http_status?: number;
  error?: string;
  detail?: { steps?: { label: string; status: string; duration_ms: number; error?: string }[]; failed_step?: number };
}

export interface RunBatch {
  id: number;
  created_at: string;
  finished_at?: string;
  status: "running" | "passed" | "failed";
  done: boolean;
  release?: string;
  commit?: string;
  environment_url?: string;
  tags: string[];
  created_by?: string;
  total: number;
  passed: number;
  failed: number;
  skipped: number;
  items?: RunItem[];
}

export interface RunInput {
  tags?: string[];
  check_ids?: number[];
  wait?: boolean;
  timeout_s?: number;
  release?: string;
  commit?: string;
  environment_url_override?: string;
}

export const listRuns = (signal?: AbortSignal) => api<{ batches: RunBatch[]; can_run: boolean }>("/api/uptime/runs?limit=20", { signal });

export const getRun = (id: number, signal?: AbortSignal) => api<{ batch: RunBatch }>(`/api/uptime/runs/${id}`, { signal });

export const startRun = (input: RunInput) => api<{ batch: RunBatch }>("/api/uptime/runs", { method: "POST", json: input });

export function runOutcome(status: string): string {
  return status === "running" ? "in_progress" : status;
}
