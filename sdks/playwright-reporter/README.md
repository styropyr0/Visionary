# visionary-playwright-reporter

A [Playwright](https://playwright.dev) reporter that sends each test run to
[Visionary](https://github.com/styropyr0/Visionary) as an uptime check. Your
end-to-end tests stay in your repository and run where they already run: in CI,
on a schedule, or on your own host. Visionary never runs your test code.

Each test becomes a check of kind **Playwright test**, keyed by its project and
title path (`checkout.spec.ts › Checkout › pays by card`). Every run records:

- passed or failed (after retries), the duration and the number of retries;
- the steps (`test.step()` and the page actions inside them) with their times
  and the step that failed;
- the error message;
- the browser and project;
- the attachments: a screenshot up to 256 KB and a trace up to 768 KB are
  uploaded, the rest are listed by name and size;
- the trace id, when the test sent a `traceparent` (see below);
- the release and commit.

The check then works like any other: uptime %, incidents, alerts after the
number of consecutive failures you choose, and the public status page if you
tick it. When no run arrives within the expected interval, the check is
**stale** and counts as down, like a missed heartbeat.

## Install

```sh
npm i -D visionary-playwright-reporter
```

Create an API key with the **CI** scope in Visionary under Settings → API keys.
It can report test runs and run checks, and nothing else.

```ts
// playwright.config.ts
import { defineConfig } from "@playwright/test";

export default defineConfig({
  reporter: [
    ["list"],
    ["visionary-playwright-reporter", { expectedInterval: "15m" }],
  ],
  use: { trace: "retain-on-failure", screenshot: "only-on-failure" },
});
```

```sh
VISIONARY_URL=https://visionary.example.com VISIONARY_KEY=vis_... npx playwright test
```

Without `VISIONARY_URL` or `VISIONARY_KEY` the reporter prints one line and
reports nothing, so local runs are unaffected. A failure to report never fails
the test run.

## Options

| Option | Default | |
| --- | --- | --- |
| `server` | `VISIONARY_URL` | Visionary's address. |
| `key` | `VISIONARY_KEY` | An API key with the CI scope. |
| `expectedInterval` | 1 hour | How often runs arrive (`600`, `"10m"`, `"1h"`, `"1d"`). A check without a run for longer is stale. It sets new checks; change it later in the check's form. |
| `release` | `VISIONARY_RELEASE` | Shown with each run. |
| `commit` | `VISIONARY_COMMIT`, `GITHUB_SHA`, `CI_COMMIT_SHA`, ... | Shown with each run. |
| `screenshots` | `true` | Upload the first screenshot when it is small enough. |
| `traces` | `true` | Upload the trace when it is small enough. |
| `maxScreenshotBytes` | 262144 | At most 256 KB. |
| `maxTraceBytes` | 786432 | At most 768 KB. |
| `timeoutMs` | 15000 | Per request. |
| `quiet` | `false` | Print only problems. |

Playwright tags become check tags (`@smoke` → `smoke`), so a tag groups
Playwright checks with HTTP and browser checks.

## Run every 10 minutes from GitHub Actions

```yaml
# .github/workflows/synthetics.yml
name: Synthetics
on:
  schedule:
    - cron: "*/10 * * * *"
  workflow_dispatch:

jobs:
  e2e:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 22
          cache: npm
      - run: npm ci
      - run: npx playwright install --with-deps chromium
      - run: npx playwright test
        env:
          VISIONARY_URL: https://visionary.example.com
          VISIONARY_KEY: ${{ secrets.VISIONARY_CI_KEY }}
```

GitHub may start scheduled workflows several minutes late, so give the checks
an `expectedInterval` with room to spare, such as `"20m"` for a 10 minute
schedule.

## Link runs to traces

When your services send traces to Visionary, give each test its own
`traceparent` and the run links to the trace of the requests it made:

```ts
import { test as base } from "@playwright/test";
import { newTraceparent } from "visionary-playwright-reporter";

export const test = base.extend({
  context: async ({ context }, use, testInfo) => {
    const tp = newTraceparent();
    await context.setExtraHTTPHeaders({ traceparent: tp.header });
    testInfo.annotations.push({ type: "traceparent", description: tp.header });
    await use(context);
  },
});
```

A `trace_id` annotation (32 hex characters) works too.

## Declare checks first

A check is made on its first report. To alert on it before then, declare it:
in Uptime → New check → **Playwright test** with the project and the title
path, or with Terraform:

```hcl
resource "visionary_uptime_check" "checkout" {
  name       = "Checkout pays by card"
  kind       = "playwright"
  interval_s = 1200
  playwright = { project = "chromium", title = "checkout.spec.ts › Checkout › pays by card" }
}
```
