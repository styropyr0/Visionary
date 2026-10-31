// Structural, so the package builds without @playwright/test and fits any version

export interface PWAttachment {
  name: string;
  contentType: string;
  path?: string;
  body?: Buffer;
}

export interface PWError {
  message?: string;
  value?: string;
  stack?: string;
}

export interface PWStep {
  title: string;
  category: string;
  duration: number;
  error?: PWError;
  steps: PWStep[];
}

export interface PWAnnotation {
  type: string;
  description?: string;
}

export interface PWTestResult {
  retry: number;
  startTime: Date;
  duration: number;
  status: "passed" | "failed" | "timedOut" | "skipped" | "interrupted";
  error?: PWError;
  errors?: PWError[];
  attachments: PWAttachment[];
  steps: PWStep[];
  annotations?: PWAnnotation[];
}

export interface PWProject {
  name: string;
  use?: { baseURL?: string; browserName?: string; channel?: string; defaultBrowserType?: string };
}

export interface PWSuite {
  project(): PWProject | undefined;
}

export interface PWTestCase {
  title: string;
  titlePath(): string[];
  expectedStatus: PWTestResult["status"];
  retries: number;
  annotations: PWAnnotation[];
  tags?: string[];
  parent: PWSuite;
  outcome(): "skipped" | "expected" | "unexpected" | "flaky";
}
