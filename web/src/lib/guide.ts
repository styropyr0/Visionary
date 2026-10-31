import { Shapes, type LucideIcon } from "lucide-react";
import type { Route } from "../components/Sidebar";

/**
 * The How to use page: a section per page, in sidebar order, with the steps to
 * get something done there. What each page shows comes from HELP; this adds
 * the doing. Text marks code and labels with **backticks**. A shot names a file
 * in assets/guide/ without its extension.
 */

export interface GuideStep {
  title: string;
  text: string;
  shot?: string;
  caption?: string;
}

export interface GuideSection {
  id: string;
  group: string;
  title: string;
  /** The page it describes; its HELP text fills "What it shows" and "Good to know". */
  route?: Route;
  /** For a section with no page of its own; a page's section takes the page's icon. */
  icon?: LucideIcon;
  intro: string;
  shot?: string;
  caption?: string;
  steps: GuideStep[];
}

export const GUIDE: GuideSection[] = [
  {
    id: "start",
    group: "Start here",
    title: "Getting started",
    intro:
      "Visionary reads one database that your services write to. Add an SDK to each service, give it the same database as this server, and every page fills in within seconds. Nothing on this page needs a restart.",
    shot: "start",
    caption: "The API overview of a shop with two services, a few minutes after they were connected.",
    steps: [
      {
        title: "Connect a service",
        text: "Open **Help** in the top right of any page and follow **1. Add the SDK to your service**: install the package, add its middleware, and set `VISIONARY_DATABASE_URL` and `VISIONARY_SERVICE`. Browser and mobile apps post to this server instead, with a client key from **Settings → API keys**.",
      },
      {
        title: "Pick a time range",
        text: "Every page follows the range in the top bar: **Last 15 minutes** up to **Last 30 days**, or **Custom range** with a start and end. Tiles compare with the period before, of the same length.",
      },
      {
        title: "Refresh, or let it refresh",
        text: "The round arrow reloads the page. The menu next to it refreshes on its own every 30 seconds to 15 minutes; it needs a relative range such as **Last 1 hour**.",
      },
      {
        title: "Ask for help where you are",
        text: "**Help** on each page says what it shows, how to read it and what it needs from your code, with snippets in your language. Panels have **Show table** to see the numbers and **Inspect query** to see the SQL behind them.",
      },
      {
        title: "Make it yours",
        text: "**Settings → Preferences** sets your start page, the range and refresh a visit starts with, the theme, time zone and clock. The moon or sun at the bottom of the sidebar switches the theme, and **Collapse** folds the sidebar to icons.",
      },
    ],
  },
  {
    id: "concepts",
    group: "Start here",
    title: "How the data fits together",
    icon: Shapes,
    intro:
      "A few words that every page uses: services, requests and their calls, traces and logs, and where each comes from. Knowing which source fills which page tells you what to add when a page stays empty.",
    steps: [
      {
        title: "Services",
        text: "A service is one app that sends data: `shop-api`, a worker, a second backend. It is only a name stamped on everything that app writes, so there is nothing to register: a service appears as soon as data arrives with its name. Server SDKs take it from `VISIONARY_SERVICE`, OpenTelemetry from `OTEL_SERVICE_NAME`, browser and mobile SDKs from the app name you give them. Without one, rows have no service and several apps can't be told apart. Service filters, the service map and **Workspaces** all go by this name.",
      },
      {
        title: "Instances and versions",
        text: "An **instance** is one running copy of a service: a pod, a container, a machine (the hostname unless you set one). Three replicas are one service with three instances. A **version** is the release that is deployed, from `VISIONARY_RELEASE` or the platform's commit variable; it draws the deploy markers and the before-and-after-deploy comparisons.",
      },
      {
        title: "Requests and their calls",
        text: "A **request** is one call your service answered, written by the SDK's middleware (or an OpenTelemetry server span). Each database query, outbound HTTP call, cache or queue call made while answering it is a **span** of that request. **Requests**, **API overview**, **Performance**, **Queries** and **Dependencies** are all built from these rows, so they need the SDK (or OpenTelemetry instrumentation) inside the app.",
      },
      {
        title: "Traces",
        text: "A **trace** is the same requests and calls, followed across services. Each row also carries a trace ID; a service that calls another passes it on in the `traceparent` header, and the service it reaches records its work under the same ID. With one service, a trace is that request's waterfall. With several, **Traces** joins every service's piece of one user action into one timeline and draws the service map. It is not a separate collector: the same SDK rows, with three IDs added.",
      },
      {
        title: "Logs, and what they don't do",
        text: "**Logs** holds lines from the SDKs, the host agent, log shippers, syslog, OpenTelemetry and plain HTTP. SDK lines are linked to the request that wrote them; others are linked when they carry a `trace_id` or `request_id`. A log line never becomes a request or a query: an nginx access line is searchable in **Logs** with its method, path and status as fields, but it doesn't show in **Requests**, **Performance** or **Queries**. Use the SDK for linked, request-level detail and the agent or a shipper as the catch-all for everything else the machine writes, crashes included.",
      },
      {
        title: "Production and staging",
        text: "There is no environment switch: keep each environment apart by giving it its own database and Visionary server, so staging's tests can't slow prod, page anyone or show prod data to testers. **Settings → Configuration** (or Terraform) copies alert rules, dashboards and checks from one install to the other. If you'd rather share one install for non-prod builds, put the environment in the service name (`shop-api-staging`) and filter alerts by service; never let two environments share one service name.",
      },
    ],
  },

  {
    id: "overview",
    group: "Monitor",
    title: "API overview",
    route: "overview",
    intro: "The first page to look at: whether traffic, errors and latency are normal, and which routes and users are behind them.",
    shot: "overview",
    steps: [
      {
        title: "Read the tiles first",
        text: "**Requests**, **Server errors**, **Client errors**, **Median latency**, **p95 latency** and **Active users**, each against the previous period. A red arrow is a change for the worse.",
      },
      {
        title: "Find when it changed",
        text: "**Requests by status**, **Latency percentiles** and **Error rate** show the range over time. Deploy marks on the charts line a step up with the release that caused it.",
      },
      {
        title: "Find where",
        text: "**Routes**, **Top errors** and **Most active users** rank what was busiest and what failed. Then open the route in **Performance** or its requests in **Requests**.",
      },
    ],
  },
  {
    id: "performance",
    group: "Monitor",
    title: "Performance",
    route: "performance",
    intro: "Why the API is slow and for whom: satisfaction scores, the routes that cost the most time, and every call a slow request made.",
    shot: "performance",
    steps: [
      {
        title: "Check the score",
        text: "**Apdex**, **Within N ms** and **Error budget left** say how many requests met your target. The target is set in **Settings → Thresholds**, for all routes or per route.",
      },
      {
        title: "Rank the routes",
        text: "**Routes by server time** puts the routes where speeding up pays most at the top; open one for its latency over time, a request of it call by call, its status codes and slowest requests. **Regressions** lists routes slower than in the previous period.",
      },
      {
        title: "Open a slow request",
        text: "Click a row in **Slowest requests** for its breakdown: each database query and outbound call on a timeline, and **Untracked** for time no call covers. Switch to **By route** to group them, and **Exclude** routes that are slow on purpose.",
        shot: "performance-request",
        caption: "A slow request: its call to api.openai.com takes all of its 1.5 seconds.",
      },
      {
        title: "See where a route's time goes",
        text: "Under **Database and outbound calls**, pick a route in **Endpoint breakdown** to split its time into database, outbound HTTP and its own code. **N+1 suspects** lists calls repeated 10 or more times in one request.",
      },
      {
        title: "Compare two versions",
        text: "Under **Releases**, choose a **Baseline version** and a **Compared version** to see each route side by side. It needs requests from both versions in the range.",
      },
    ],
  },
  {
    id: "queries",
    group: "Monitor",
    title: "Queries",
    route: "queries",
    intro:
      "Every SQL statement your services run, the routes that run them, and what a deploy changed. Statements come from the SDK's database hook (or OpenTelemetry database spans); a database's own slow-query log goes to Logs, not here.",
    shot: "queries",
    steps: [
      {
        title: "Find the statement to tune",
        text: "**Catalogue** lists each distinct statement with its calls, latency and total time. Sort by total time; click a statement for its calls over time, distribution, the routes that run it and its slowest calls.",
        shot: "queries-detail",
        caption: "One statement: its latency, the routes that run it and its slowest calls.",
      },
      {
        title: "Spot N+1 loops",
        text: "**Per route** shows how many queries a request makes. A route whose queries per request run far above its median is usually querying once per item.",
      },
      {
        title: "Check a deploy",
        text: "**After a deploy** compares each statement before and after a release. Pick the **Deploy** and the **Window** either side.",
      },
      {
        title: "Ask for a plan",
        text: "Open a statement and look at its **Query plan**: your SDK runs EXPLAIN, Visionary points out full scans and suggests the index to add, and **Explain in plain words** asks the analyst. This needs the explainer hook; see **Help** on this page. Anywhere else a statement appears (a request's calls, a route), **Open in Query Viewer** brings you here with it open.",
      },
    ],
  },
  {
    id: "dependencies",
    group: "Monitor",
    title: "Dependencies",
    route: "dependencies",
    intro: "The third-party APIs your services call, watched like routes of your own.",
    shot: "dependencies",
    steps: [
      {
        title: "Compare the hosts",
        text: "The tiles give total calls, failures and the slowest host. **p95 by host** shows which one slowed down and when.",
      },
      {
        title: "Open a host",
        text: "Click a host in the table for its URLs, the routes that call it and its slowest calls. Click a slow call to open the request it was part of.",
      },
    ],
  },
  {
    id: "profiles",
    group: "Monitor",
    title: "CPU profiles",
    route: "profiles",
    intro: "Flame graphs of where your services spend CPU while they serve requests.",
    shot: "profiles",
    steps: [
      {
        title: "Turn profiling on",
        text: "An admin turns it on in **Settings → Profiling**, for every service or chosen ones, and sets how often a profile is taken. **Profile now** asks for one straight away; it shows up in about a minute.",
      },
      {
        title: "Read the graph",
        text: "Each frame is as wide as the CPU spent in it and what it called. Click a frame to zoom in. The table under the graph lists the functions that did the most work themselves.",
      },
      {
        title: "Narrow it down",
        text: "Pick a route in **Profiles** to merge only the profiles taken while it was busy, or click a profile in the table to see just that one. **Service** appears when several services are profiled.",
      },
    ],
  },
  {
    id: "system",
    group: "Monitor",
    title: "System",
    route: "system",
    intro: "The machines and processes behind your services: CPU, memory, disk, network, the runtime and the database pool.",
    shot: "system",
    steps: [
      {
        title: "Pick an instance",
        text: "**Instance** in the top bar narrows the page to one server. Each SDK reports the process it runs in; the host agent adds the whole machine.",
      },
      {
        title: "Look for saturation",
        text: "The tiles show the latest reading. Charts follow under **CPU**, **Memory**, **Disk**, **Network**, **Process and runtime** and **Database pool**; waiting for a database connection above zero means the pool is too small.",
      },
      {
        title: "Investigate a spike",
        text: "**Notable moments** lists the busiest intervals. **Investigate** opens Explore with the question asked for you.",
      },
      {
        title: "Add the host agent",
        text: "For drives, top processes, failed systemd services and containers, install the agent on the machine. The steps are under **Add the host agent** on this page.",
      },
    ],
  },
  {
    id: "containers",
    group: "Monitor",
    title: "Containers",
    route: "containers",
    intro: "Docker and Podman containers on the machines the host agent watches, and services on container platforms that report their own limits.",
    shot: "containers",
    caption: "Before any container reports, the page shows how to connect one.",
    steps: [
      {
        title: "Connect containers",
        text: "Install the host agent on the machine running Docker or Podman. A service on Railway, Render, Kubernetes, Cloud Run, ECS or Azure Container Apps shows here on its own through its SDK.",
      },
      {
        title: "Find the unhealthy ones",
        text: "**Running**, **Unhealthy**, **Restarts** and **Out-of-memory kills** sum up the range. Search the table and click a container for its CPU and memory against its limits, its I/O and its events.",
      },
    ],
  },
  {
    id: "metrics",
    group: "Monitor",
    title: "Metrics",
    route: "metrics",
    intro: "Your own numbers: orders placed, queue depth, checkout time. Record them in code, with OpenTelemetry or from Prometheus, then chart, compare and alert on them.",
    shot: "metrics",
    caption: "checkout_ms at p95, grouped by method, over the last 24 hours.",
    steps: [
      {
        title: "Record a metric",
        text: "In Go, `visionary.Counter(\"orders_placed\", \"region\", \"eu\").Add(1)`, `visionary.Gauge(...).Set(v)` or `visionary.Histogram(...).Observe(v)`. Prometheus sends to `/api/v1/write` with a server key; OpenTelemetry metrics arrive on their own. **Help** has the snippets.",
      },
      {
        title: "Chart it",
        text: "Pick the metric in the list. **Aggregation** says how readings combine: **Sum** or **Rate per second** for counters, **Average** or **Max** for gauges, **p95** for histograms. **Group by** splits the chart by a label, the service or the instance.",
      },
      {
        title: "Narrow it",
        text: "Choose a label and a value, then **Filter**; remove a filter with its ×. **Service** in the top bar keeps one service. The address keeps the whole view, so copy it to share.",
      },
      {
        title: "Keep it and alert on it",
        text: "**Add to dashboard** on the chart puts it on a dashboard as it is. In **Alerts**, a rule on **Custom metric** watches the same numbers over its window, per label value when grouped.",
      },
      {
        title: "Query with PromQL",
        text: "Switch the top bar to **PromQL** and write a query, e.g. `sum by (route) (rate(visionary_http_requests_total[5m]))`. **Tab** completes metric names, functions, labels and values; **Ctrl+Enter** runs it. An error points at the character it is about. On a long range a chart point covers a whole bucket, so `[5m]` reads the bucket's span rather than its last five minutes. Your own metrics keep their names; Visionary's requests, spans, errors and system readings are `visionary_*` series. **Add to dashboard** keeps the query, a dashboard's **Add panel → PromQL** writes one there, and an alert on **PromQL expression** fires per series it returns.",
        shot: "metrics-promql",
        caption: "A PromQL query: orders per second by region.",
      },
      {
        title: "Use Visionary in Grafana",
        text: "Create a key with the **Read** scope in **Settings → API keys**. In Grafana add a **Prometheus** data source with this server's address followed by `/api/prom` as its URL, **Basic authentication** with any user name and the key as password, then **Save & test**. Grafana's metric browser lists the same names; other clients send the key as `Authorization: Bearer`. **Help** has a provisioning file.",
      },
    ],
  },
  {
    id: "errors",
    group: "Monitor",
    title: "Errors",
    route: "errors",
    intro: "Exceptions and panics grouped into issues, with the stack, the versions they appear in and the requests that raised them.",
    shot: "errors",
    steps: [
      {
        title: "Triage the list",
        text: "Filter **Status** to **New** or **Regressed** to see what this range brought. Search by type, message or route.",
      },
      {
        title: "Open an issue",
        text: "Click an issue for its occurrences over time, the versions and instances it appears on, its stack trace, and the routes and users it reached. **View request** on a recent occurrence opens the request that failed.",
        shot: "errors-issue",
        caption: "One issue: when it happens, where it comes from and who it reached.",
      },
      {
        title: "Ask why",
        text: "**Ask the analyst** opens Explore with a question about this issue already written.",
      },
    ],
  },
  {
    id: "frontend",
    group: "Monitor",
    title: "Frontend",
    route: "frontend",
    intro: "What your users experience in the browser and in your mobile apps.",
    shot: "frontend",
    steps: [
      {
        title: "Connect an app",
        text: "Create a client key in **Settings → API keys**, then add the browser, Android or iOS SDK; **Help** on this page has the snippet. For a website, also add its origin under **Settings → Server → Ingest**.",
      },
      {
        title: "Check Web Vitals",
        text: "LCP, INP, CLS, FCP and TTFB are rated good, needs improvement or poor at the 75th percentile, per page. **App** in the top bar narrows to Web, Android or iOS.",
      },
      {
        title: "Compare devices and versions",
        text: "**Breakdown** splits the numbers by app, app version, device, OS or country, so a slow screen on low-end phones stands out.",
      },
      {
        title: "Open a client error",
        text: "Click a row in **Error groups** for its message, stack trace, device, page and its latest occurrences.",
      },
      {
        title: "Turn on session replay",
        text: "Switch on **Accept session replays** in **Settings → Server → Ingest**, then pass `replay: { sampleRate: 0.05, onErrorSampleRate: 1 }` to the browser SDK's `init`: 5% of sessions are recorded whole, and the others send their last minute only when an error happens. Inputs are masked in the browser; passwords, emails, phone numbers and card fields are never recorded. Add `data-visionary-mask` to an element to mask its text, or `data-visionary-block` to leave it out. Replays are kept 7 days (**Settings → Data**) and **Session replay storage** caps their size; the oldest go first.",
      },
      {
        title: "Watch a session",
        text: "In **Recent sessions**, a play icon marks sessions with a replay. Open one and choose **Replay**: play, pause, change the speed, or click the bar. The marks on it are page loads, API calls, errors and server logs; click one to jump just before it. **Skip inactivity** jumps over idle stretches.",
        shot: "frontend-replay",
        caption: "A session's replay, with its page loads, API calls and errors on the scrubber.",
      },
    ],
  },
  {
    id: "uptime",
    group: "Monitor",
    title: "Uptime",
    route: "uptime",
    intro: "Checks this server runs against your endpoints, or through your pages in a headless browser, on a schedule, with incidents and a public status page.",
    shot: "uptime",
    steps: [
      {
        title: "Add a check",
        text: "Click **New check**. Give it a name and URL; set the expected status, text the body must contain, how often to check, the timeout and **Degraded above** for slow answers. Under **Alerts when it goes down**, keep **The rule for every check** or give it its own settings.",
        shot: "uptime-form",
        caption: "A new check: what to call, what counts as up, and who hears when it is down.",
      },
      {
        title: "Script a browser check",
        text: "In **New check**, pick **Browser steps**. Start with **Go to URL**, then add steps: click by CSS selector or visible text, type into a field, press a key, choose an option, wait for an element, text or the next page, and check text, the URL or an element count. Put passwords under **Secrets** and type them as `{{secret.NAME}}`. The server needs Chrome, Chromium or Edge; the form says which one it uses, or how to install one.",
        shot: "uptime-browser",
        caption: "A browser check: each step's result, timings and Web Vitals.",
      },
      {
        title: "Watch the list",
        text: "Each row shows its state, the last 90 days, uptime and response time over 24 hours. The row buttons run a check now, pause it or edit it. Give checks **Tags** in their form (`smoke`, `checkout`) to group them; the **Tag** menu above the list shows one group.",
      },
      {
        title: "Open a check",
        text: "Click a check for uptime over 24 hours to 90 days, response time, where the time goes (DNS, connect, TLS, first byte), its certificate, incidents and recent results. A browser check shows Web Vitals, each step's time and failures, and its runs; click a run for its steps, the screenshot of a failure, console errors and failed requests.",
        shot: "uptime-detail",
        caption: "One check: response time split into its phases, and its incidents.",
      },
      {
        title: "Publish a status page",
        text: "Tick **Show on the public status page** on the checks you want public, then switch on **Public status page** in **Settings → Status page**. **Status page** in the top bar opens it.",
      },
    ],
  },
  {
    id: "ci",
    group: "Monitor",
    title: "CI",
    route: "ci",
    intro: "Your pipelines and tests next to what they shipped: which workflows fail and why, what is slow, which tests are flaky, and how a deploy changed latency and errors.",
    shot: "ci",
    caption: "Pipelines by workflow and branch: success rate, duration and queue time.",
    steps: [
      {
        title: "Connect GitHub Actions",
        text: "In **Settings → Server → CI**, save a long random **GitHub webhook secret**. In the GitHub repository (or organization), open **Settings → Webhooks → Add webhook**: payload URL `https://<this server>/api/ingest/ci/github?service=<service>`, content type `application/json`, the same secret, and under **Let me select individual events** tick **Workflow runs** and **Workflow jobs**. GitHub's **Recent Deliveries** should answer 202; a 401 means the secrets differ.",
      },
      {
        title: "Upload test results",
        text: "Create a **server** key in **Settings → API keys** and save it as the repository secret `VISIONARY_KEY`. After the step that runs your tests, add the step from **Help → Upload test results**: it posts the JUnit XML with the run's id, branch and commit, even when the tests failed.",
      },
      {
        title: "Other CI systems",
        text: "GitLab, CircleCI, Buildkite or Jenkins post each run to `/api/ingest/ci/runs` with a server key whenever it changes, and JUnit to `/api/ingest/ci/junit` with the same `provider`, `repo` and `run_id`. **Help** has both requests.",
      },
      {
        title: "Read the pipelines",
        text: "**Pipelines** shows runs by outcome, the success rate of the busiest workflows and p50, p95 and queue time over the range, then each workflow per branch, the slowest jobs and the jobs failing most. **Workflow**, **Branch** and **Service** in the top bar narrow everything.",
      },
      {
        title: "Open a run",
        text: "Click a run for its jobs on a timeline, hatched while they waited for a runner; open a job for its steps. Below are its failed tests with their messages (**Flaky** marks known flaky ones), the slowest tests, and under **Deployed as** each release of its commit with requests, p95, 5xx rate and exceptions in the hour before and after.",
        shot: "ci-run",
        caption: "One run: its jobs on a timeline and its tests.",
      },
      {
        title: "Find flaky and slow tests",
        text: "**Tests** lists failures by test, the slowest tests and the ones getting slower; **Flaky** ranks flaky tests by score, with their last results and **New** on those first seen in the range. Click a test for its history across branches and commits.",
        shot: "ci-flaky",
        caption: "Tests that passed and failed on the same commit, with a flakiness score.",
      },
      {
        title: "Alert on it",
        text: "In **Alerts → New rule**, the **CI** group has the pipeline failure rate (per workflow), p95 duration, job queue time and new flaky tests, with presets to start from.",
      },
    ],
  },
  {
    id: "security",
    group: "Monitor",
    title: "Security",
    route: "security",
    intro: "Attacks and account takeovers found in your request log, events from your machines, and who changed what in Visionary, with what to do about each.",
    shot: "security",
    steps: [
      {
        title: "Record sign-in attempts",
        text: "In your login handler, call the SDK's **LoginAttempt** (Go), **loginAttempt** (Node, Java), **login_attempt** (Python, Ruby) or **LoginAttempt** (.NET) with the email or username typed. Without it Security counts failed sign-ins per address only; with it, it can tell one address trying many accounts, many addresses trying one account, and someone signed in as one user trying another's password.",
      },
      {
        title: "Review findings",
        text: "**Findings** lists what the detectors found. Filter by source (**API detectors**, **Host agent**, **Visionary itself**), kind, severity, status, host or IP. The **Status** column shows what's been looked at.",
      },
      {
        title: "Open and triage a finding",
        text: "Click a finding for the requests behind it. Mark it **Acknowledged**, **Resolved** or **False positive** and add a note, so the team knows who looked at it. **Requests from this IP** shows everything the address sent; **Investigate** asks the analyst.",
        shot: "security-finding",
        caption: "A scanner probing for secrets, with the requests it sent.",
      },
      {
        title: "Look up an account or an address",
        text: "**Account timeline** on a finding shows that account's sign-ins with place and device, its findings, and where it signed in from before. **About this IP** shows where an address is, who runs it, which blocklists name it, and what else it did this week.",
      },
      {
        title: "Act on a finding",
        text: "**Block IP** and **End sessions** POST a signed JSON call to the **Action webhook** in **Settings → Server → Response**, for your firewall, gateway or app to act on: `{\"action\": \"block_ip\", \"ip\": \"…\", \"finding\": {…}}` or `{\"action\": \"end_sessions\", \"account\": \"…\"}`, signed with HMAC-SHA256 in **X-Visionary-Signature**. **Ignore this IP** makes every detector skip the address, for your own monitors.",
      },
      {
        title: "Stop a false alarm",
        text: "**Don't flag this again** stops that kind of finding for the IP or the account, forever or for a while, and marks the finding a false positive. **Not flagged** lists these rules; add one there by hand or remove one to start flagging again.",
      },
      {
        title: "Turn on detection rules",
        text: "**Rules** has a library of ready-made rules, each tagged with its MITRE ATT&CK tactic and technique: brute force and credential stuffing, failed logins then a success, a sign-in from a new country, an admin from a new IP, spikes in 401 and 403, path traversal and SQL injection in paths, mass exports, secrets in logs, SSH failures then a login, sudo by a new user, new SSH keys, new API keys and more. **Turn on** runs one as it is; **Customize** opens it to change first.",
        shot: "security-rules",
        caption: "Detection rules and the library of templates, tagged with MITRE ATT&CK.",
      },
      {
        title: "Write your own rule",
        text: "**New rule** picks what to read (**Requests**, **App logs** with the Logs search syntax, **Security events** from the host agent and detectors, or **Visionary audit log**), what to match, and the logic: **Threshold** (a count, or a number of different values, at or over N in the window per group), **New value** (a value a group hasn't had in its learning period, such as a new country for a user), **Sequence** (N of one thing, then another, by the same group within the window) or **Anomaly** (a group far above its usual count). Group by up to three fields such as `ip`, `user` or `host`. **Test against the last 24 hours** shows what it would have raised before you save. Pick channels under **Notify** to hear about new signals, or alert on Security findings with source `rule`.",
      },
      {
        title: "Investigate a signal",
        text: "**Signals** lists what the rules found, with the same filters and triage as findings. Open one for the rule, its MITRE tags, the group's values with links to their requests, logs and account timeline, and every matched event around it, each linked to its request's logs and trace. **Suppress** on a value adds it to the rule's suppressions, so that IP or user stops raising it for as long as you choose.",
        shot: "security-signals",
        caption: "Signals raised by the rules, newest first.",
      },
      {
        title: "Add places and blocklists",
        text: "In **Settings → Server → Threat intelligence**, point **Location database** at a GeoLite2-City or DB-IP City Lite `.mmdb` file on the server to get places on findings and the new-location and impossible-travel checks, and **Network database** at an ASN file to name who runs each address. **Blocklist feeds** takes URLs of IP lists, such as `https://check.torproject.org/torbulkexitlist`; findings from listed addresses go up one level. **Threat intelligence** on the Security page shows whether they loaded.",
      },
      {
        title: "Protect Visionary itself",
        text: "**Settings → Server → Sessions**: **Allow Visionary only from** limits the dashboard to your addresses (SDKs, OpenTelemetry, the host agent and the status page keep working), and **Require two-step sign-in** makes everyone with a Visionary account use an authenticator app. Each person sets theirs up under **Settings → Members & roles → Two-step sign-in**. Failed sign-ins to Visionary, secrets shown and role changes appear as findings, so alerts can tell you.",
      },
      {
        title: "Export the audit log",
        text: "Admins find **Audit export** at the bottom of the page: **Export the range shown** downloads a signed file where each line hashes the one before. **Check an export** says whether a file is intact, or which line was changed or removed.",
      },
      {
        title: "Tune or share",
        text: "**Settings → Server** has a group per area: **Security detectors**, **Account takeover**, **API abuse**, **Threat intelligence** and **Response**. **Route limits** there sets per-minute limits for routes such as `POST /api/export* 10`. **Report** makes a printable summary of the range: a verdict, recommended actions and the top findings, IPs and paths. **Download PDF** saves it as an A4 report in your time zone; **Print** uses the browser instead.",
      },
    ],
  },

  {
    id: "explore",
    group: "Investigate",
    title: "Explore",
    route: "explore",
    intro: "Ask a question about your data in plain words and get an answer with charts.",
    shot: "explore",
    caption: "Explore before an OpenAI API key is set: it says where to add one.",
    steps: [
      {
        title: "Turn the analyst on",
        text: "An admin adds an OpenAI API key, or any OpenAI-compatible API, in **Settings → Server → AI analyst**. Until then the question box is off; dashboards and saved panels still work.",
      },
      {
        title: "Ask",
        text: "Type a question such as “why did checkout get slower yesterday?” and press Enter, or pick a suggestion. Shift+Enter adds a new line.",
      },
      {
        title: "Check the working",
        text: "**Investigated with N queries** expands to every SQL query the analyst ran and the rows it got back. The analyst only reads; it cannot change your data.",
      },
      {
        title: "Keep a chart",
        text: "The bookmark on a chart saves it to **Saved panels**; the grid icon saves it and puts it straight on a dashboard. Past analyses stay under **Recent**, in this browser.",
      },
    ],
  },
  {
    id: "applogs",
    group: "Investigate",
    title: "Logs",
    route: "applogs",
    intro: "Every line your services log, from the SDKs, OpenTelemetry, shippers, syslog and the host agent, linked to the request that wrote it.",
    shot: "applogs",
    steps: [
      {
        title: "Send logs",
        text: "In an SDK, route your logger to Visionary (the **Help** button on the page has each language's line); or point OpenTelemetry, Fluent Bit, Vector, Filebeat, Logstash or Promtail at this server with a server key; or set `VISIONARY_AGENT_LOGS` on the host agent. Info and above is kept; a failed or slow request also keeps its debug lines.",
      },
      {
        title: "Catch everything, not just what the app logs",
        text: "The SDK only sees what your code logs while it runs. To also keep crashes, panics, startup failures and other programs' output, collect from outside the app: the host agent with `VISIONARY_AGENT_LOGS=journald,docker,/var/log/myapp/*.log` (the systemd journal, every container's output, any files, followed through rotation), a shipper or a node-level DaemonSet in Kubernetes, your platform's log drain pointed at `/loki/api/v1/push` or `/api/ingest/logs`, or the syslog receiver in **Settings → Logs**. These lines go to Logs only; they never become requests or queries.",
      },
      {
        title: "Collect nginx or Apache logs",
        text: "On the machine, `VISIONARY_AGENT_LOGS=/var/log/nginx/*.log`; in Docker, `docker` (the official image writes to its output); or, with no agent, `access_log syslog:server=<this server>:<port>,tag=nginx combined;` once the syslog receiver is on. Lines in the standard `combined` format are read into `client.ip`, `http.method`, `http.path`, `http.status`, `http.bytes`, `http.referer` and `user_agent`, with 5xx as errors and 4xx as warnings, so `http.status:>=500` finds failures. Error-log lines and a custom `log_format` need a parse rule in **Settings → Logs**, or switch nginx to a JSON `log_format`, which is read without one.",
      },
      {
        title: "Search",
        text: "Type words to match the message, `field:value` for a column or a field (`service:api`, `order_id:42`), `level:error+` for errors and worse, `route:/checkout` or `status:5xx` for lines of those requests, `-word` to leave out, `OR` between alternatives and `/regex/`. The **?** lists it all. With the analyst on, **Ask** turns a question into a search and **Explain** reads the lines for you.",
      },
      {
        title: "Narrow it down",
        text: "Drag across the chart to zoom into a moment; **Zoom out** steps back. Open a field on the left for its top values, then **+** to keep one or **−** to leave it out; the column icon shows the field as a column. **Live tail** streams new lines as they arrive.",
      },
      {
        title: "Open a line",
        text: "Click a line for all its fields, **Lines around it** from the same source, **Open its request** with its calls on a timeline, and **Lines like this**. Editors can **Alert when it happens again**, or keep a service's or a user's debug lines for 15 minutes without a redeploy.",
        shot: "applogs-line",
        caption: "One line: its pattern, the release it first appeared in, and every field.",
      },
      {
        title: "See the patterns",
        text: "**Patterns** groups lines by message with numbers and ids taken out, busiest first, with a trend and the release each first appeared in. **Releases** compares two releases: what one logs that the other doesn't.",
        shot: "applogs-patterns",
        caption: "Lines grouped into messages; New marks those first seen in the range.",
      },
      {
        title: "Keep it lean",
        text: "**Volume** shows where storage goes, by service, source, level and the code line that logs it, and the busy messages nobody reads. **Reduce** adds a drop rule for a chatty line. **Hidden failures** lists errors logged in requests that still answered 2xx.",
        shot: "applogs-volume",
        caption: "Where log volume comes from, down to the line of code.",
      },
      {
        title: "Set what is kept",
        text: "**Settings → Logs** sets the level, the buffer for failed and slow requests, caps, level overrides, drop rules, parse rules for plain text, extra redaction, what viewers may see, the syslog receiver and the archive; services follow it within 30 seconds.",
      },
    ],
  },
  {
    id: "logs",
    group: "Investigate",
    title: "Requests",
    route: "logs",
    intro:
      "Every request your services served, searchable, with its headers, bodies and calls. Rows come from the SDK's middleware or OpenTelemetry server spans; log lines, including nginx access lines, stay in Logs.",
    shot: "logs",
    steps: [
      {
        title: "Filter",
        text: "Narrow by status class, method, route, user (ID or @handle) or IP, or search for a path or an exact request ID. Links from other pages arrive with the filters already set.",
      },
      {
        title: "Open a request",
        text: "Click a row for its status, duration, user and service, the errors it raised, its database and outbound calls, and the **Response body**, **Request body** and **Request headers**, with secrets redacted.",
        shot: "logs-detail",
        caption: "One request: every query and outbound call it made, and its body.",
      },
      {
        title: "Go further back",
        text: "**Load older requests** at the bottom continues the list.",
      },
    ],
  },
  {
    id: "traces",
    group: "Investigate",
    title: "Traces",
    route: "traces",
    intro: "One request followed through every service it called, and the map of which services call which.",
    shot: "traces",
    caption: "Traces of the last 24 hours, with the services each one touched.",
    steps: [
      {
        title: "Traces and requests",
        text: "A trace is built from the same rows as **Requests**: the requests and calls the SDK already writes, each carrying a trace ID. A request shows one service's part; a trace joins the parts of every service the user's action reached. With a single service, a trace and its request's waterfall show the same thing.",
      },
      {
        title: "Find a trace",
        text: "**Search** lists the traces that started in the range. Narrow them by **Services** (a comma list finds traces that touched all of them), **Route**, a call's name or target such as `SELECT users` or `api.stripe.com`, **Min ms**, or **Only with errors**. Click a row to open it.",
      },
      {
        title: "Read a trace",
        text: "The timeline puts every request and call of every service on one time axis, each under the span it was made in. Colours mean the kind, the same on every page: slate for a request, blue for a database query, orange for an outbound HTTP call, green for a cache, amber for a queue, dark green for background work; each request row names its service. Failed rows are outlined. Click a row for its service, target and statement; on a request, **Open the request** shows its full breakdown, body and headers. Errors and log lines that carry the trace are listed with it.",
        shot: "trace",
        caption: "One checkout through four services, with the log lines it wrote.",
      },
      {
        title: "Get there from anywhere",
        text: "A request in **Requests** or **Performance** has **Open trace** above its calls, and a log line in **Logs** has **Open its trace**. A trace's address is `#/traces/<trace id>`, so you can paste one from a log or another tool.",
      },
      {
        title: "Use the service map",
        text: "**Service map** draws each service and the databases, hosts, caches and queues it calls. Arrows are thicker for more calls and turn amber or red with failures; hover one for its rate, failures and p95, or **Show table** for all of them. Drag the canvas to pan, scroll or pinch to zoom, drag a box to move it; the buttons in the corner zoom, fit the map and put moved boxes back. Click an arrow or a box to search its traces.",
        shot: "traces-map",
        caption: "Services, the databases and hosts they call, and the calls between them.",
      },
      {
        title: "Connect more services",
        text: "Calls between services join up when the caller sends `traceparent` and the service it calls records it. See **Help** on this page, **Follow requests across services**.",
      },
    ],
  },
  {
    id: "dashboards",
    group: "Investigate",
    title: "Dashboards",
    route: "dashboards",
    intro: "Pages of your own: the panels you care about, arranged on a grid and shared with the team.",
    shot: "dashboards",
    steps: [
      {
        title: "Create one",
        text: "Click **New dashboard**, name it, choose whether it follows the global time range, and tick the variables it offers (**Instance**, **Route**, **Service**).",
      },
      {
        title: "Add and arrange panels",
        text: "In edit mode, **Add panel** offers your saved panels and the built-in ones from API overview, Performance and System. Drag a panel's header to move it and its corner to resize, then **Save**. Charts elsewhere in Visionary have an **Add to dashboard** icon (the grid) that puts them on a dashboard with their filters.",
      },
      {
        title: "Share it",
        text: "The link icon copies a link with the current range and selections. **TV** shows it full screen with a clock beside the title, refreshes on its own and can cycle through dashboards; press Esc to leave.",
      },
    ],
  },
  {
    id: "saved",
    group: "Investigate",
    title: "Saved panels",
    route: "saved",
    intro: "Charts saved from Explore, for the whole team.",
    shot: "saved",
    steps: [
      {
        title: "Reuse a panel",
        text: "**Add to dashboard** puts it on an existing dashboard or a new one. **Inspect query** shows its SQL, and the table icon shows its numbers.",
      },
    ],
  },

  {
    id: "alerts",
    group: "Manage",
    title: "Alerts",
    route: "alerts",
    intro: "Rules that watch any metric and tell the right people, with acknowledgements, escalation and silences.",
    shot: "alerts",
    caption: "Rules, firing and quiet, with their last value and recent trend.",
    steps: [
      {
        title: "Add where alerts go",
        text: "In **Channels**, **Add channel** and pick Slack, Discord, Microsoft Teams, Google Chat, Webhook, Email, PagerDuty, Opsgenie, Telegram or SMS. **Send test** checks it works. A webhook with a signing secret carries an `X-Visionary-Signature` header to verify.",
        shot: "alerts-channels",
        caption: "Channels, each with a test button and its weekly digest setting.",
      },
      {
        title: "Create a rule",
        text: "Click **New rule** and start from a preset, or a blank rule. Choose a **Threshold**, **Anomaly** or **SLO burn rate** rule, the metric and its scope (route, service, instance…), the condition and window, how long it must hold (**For**), its severity, and the channels to notify. The **Last 24 hours** chart shows when it would have fired.",
        shot: "alerts-rule",
        caption: "A rule open for editing: the preview shows it would fire on today's checkout errors.",
      },
      {
        title: "Fine-tune it",
        text: "Under **When data stops, flapping and resolving**, decide what missing data means and whether to say when it resolves. **Escalation** adds channels after a delay, **Message** sets the title and mentions, and **Depends on** holds it back while another rule fires.",
      },
      {
        title: "Acknowledge what you are handling",
        text: "When a rule fires, click its double check mark and **Acknowledge**, with an optional note. Reminders and escalation stop, and the channels that were told hear who took it.",
        shot: "alerts-ack",
        caption: "Acknowledging a firing rule.",
      },
      {
        title: "Mute or silence",
        text: "The bell mutes one rule for 30 minutes to a week. For planned work, **Silences** → **New silence** holds back notifications in a maintenance window or in weekly quiet hours, by severity, rule or label.",
        shot: "alerts-silences",
        caption: "A weekly quiet window for warnings.",
      },
      {
        title: "Check what was sent",
        text: "**Activity** lists every firing and resolve with what each channel was told, and whether delivery worked or is being retried. **CSV** downloads it.",
        shot: "alerts-activity",
        caption: "Activity: what fired, and each channel's delivery.",
      },
      {
        title: "Get a weekly digest",
        text: "**Digest** previews this week's summary of traffic, latency, alerts, errors and uptime. Under **Delivery**, pick the channels, weekday and hour, or **Send now**. Email and Telegram channels also get the digest as a PDF, and **PDF** downloads it.",
      },
    ],
  },
  {
    id: "incidents",
    group: "Manage",
    title: "Incidents",
    route: "incidents",
    intro: "When something is broken for users, declare an incident: one place for who is on it, what is known, what was said, and afterwards what to change.",
    shot: "incidents",
    caption: "Open and resolved incidents with their severity, time to resolve and owner.",
    steps: [
      {
        title: "Declare it",
        text: "Click **Declare incident** on this page, or on a firing rule in **Alerts**, a check in **Uptime**, an issue in **Errors** or a line in **Logs**, which fills in the title, severity, services and start time and links the source. Pick **SEV1** (critical) to **SEV4** (low), name the commander, and tick the channels to tell.",
      },
      {
        title: "Assign the roles",
        text: "Under **Responders**, **Take command** makes you the incident commander; **Assign** adds a communications or operations lead, a scribe or a responder. The commander shows in the list, the notifications and the postmortem.",
      },
      {
        title: "Move it along",
        text: "The steps under the title move it from **Investigating** to **Identified**, **Monitoring** and **Resolved**, each stamped with its time. The tiles show the time to acknowledge, mitigate and resolve.",
      },
      {
        title: "Keep the timeline",
        text: "Add a **Note** with a link to a chart, request or trace as you go. Linked alerts firing and resolving, linked checks failing and recovering, and deploys of the affected services appear on their own; untick them to see only what people wrote.",
        shot: "incident",
        caption: "A resolved SEV1: its status steps, times, responders and timeline.",
      },
      {
        title: "Tell people",
        text: "Switch to **Status update**, write what is affected and what happens next, optionally move the status, and pick the channels and the **Status page**. Each channel's delivery shows on the update.",
      },
      {
        title: "Write the postmortem",
        text: "On **Postmortem**, **Draft it** fills the summary, impact and timeline from the data, and with the AI analyst on also the root cause, lessons and action items. Edit it, give each action item an owner and tick it off when done, and **Download Markdown** for your wiki.",
        shot: "incident-postmortem",
        caption: "The postmortem drafted from the timeline, with action items.",
      },
    ],
  },
  {
    id: "settings",
    group: "Manage",
    title: "Settings",
    route: "settings",
    intro: "Your own preferences, and the team's: who can sign in, keys, thresholds, data, and how the server behaves.",
    shot: "settings",
    steps: [
      {
        title: "Set your preferences",
        text: "**Preferences** is yours alone: start page, time range, auto-refresh, theme, time zone and clock.",
      },
      {
        title: "Bring in the team",
        text: "**Members & roles** and **Sign-in** decide how people sign in and as what: viewers read, editors change alerts and dashboards, admins change everything. With Visionary accounts, each person can turn on **Two-step sign-in** there, and an admin can turn it off for someone who lost their phone.",
      },
      {
        title: "Create keys",
        text: "**API keys** → **Create key**: server keys for OpenTelemetry and the host agent, client keys for browser and mobile apps, read keys for scripts, editor or admin keys for Terraform. A key is shown once.",
      },
      {
        title: "Set targets and retention",
        text: "**Thresholds** sets the Apdex target and SLO, with routes that have their own. **Data** sets how long each kind of data is kept, and deletes data after a preview.",
      },
      {
        title: "Partition busy tables",
        text: "On PostgreSQL, **Data → Time partitioning** splits requests, spans, logs, metrics and frontend events into one partition per day. Retention then drops whole days instead of deleting rows, and charts read only the days they show. Turn it on when the database takes a few million rows a day; below that it changes little. Empty tables convert at once; for a table with rows, **Convert** copies it while writes continue and pauses them only for the final swap, so pick a quiet hour and have room for a second copy.",
      },
      {
        title: "Configure the server",
        text: "**Server** holds the AI analyst key, sessions (with the allow list and two-step requirement), email (SMTP), how often alerts run, ingest keys and limits, the security detectors, threat intelligence and the action webhook, and other switches. A stored secret shows with the eye button, and showing it is recorded in the audit log. Changes reach every server within seconds; a few are marked **After restart**, and once one is saved, **Restart now** at the top applies it in a few seconds.",
      },
      {
        title: "Run several replicas",
        text: "Start two or more servers on the same PostgreSQL, MySQL or D1 database behind a load balancer, with `/api/readyz` as the readiness check. Each background job (alert checks, deliveries, uptime probes, rollups, retention) runs on one replica at a time and moves to another within 40 seconds if that one stops. **Server → Replicas** lists each replica, its version, its last heartbeat and the jobs it holds. **Restart now** restarts only the replica that answered; restart the others from your platform.",
      },
      {
        title: "Tidy the sidebar",
        text: "**Pages** hides pages your team does not use, for everyone. Hidden pages still open from their link.",
      },
    ],
  },
  {
    id: "sso",
    group: "Manage",
    title: "Single sign-on and SCIM",
    intro:
      "Let people sign in with the account they already have at Google, Microsoft Entra ID, Okta, Auth0, Keycloak or any OpenID Connect or SAML 2.0 provider, and let the provider add and remove them. It all lives in **Settings → Sign-in → Single sign-on**, applies without a restart, and works next to the other ways in.",
    shot: "settings-sso",
    caption: "Settings → Sign-in: the way in, single sign-on providers and SCIM.",
    steps: [
      {
        title: "Keep a way back in",
        text: "Before you start, choose **Accounts** under **How people sign in** and add an admin account for yourself. Its buttons show on the sign-in screen next to the form, so you can still get in if the provider has a bad day. Choose **Single sign-on only** later if you want just the buttons. If you are ever locked out, set `VISIONARY_ENABLE_AUTH=false` on the server and restart it.",
      },
      {
        title: "Add a provider",
        text: "Check the **Public URL** (the address people open Visionary at), click **Add provider**, pick **OpenID Connect** or **SAML 2.0** and leave it switched off. Save once to get the addresses to register at the provider: the **Redirect URL** for OpenID Connect, the **Entity ID and metadata** and **ACS URL** for SAML.",
      },
      {
        title: "Google",
        text: "In Google Cloud Console → APIs & Services → Credentials, create an **OAuth client ID** of type **Web application** and add the Redirect URL. In Visionary pick **Google**, paste the client ID and secret. Google sends no groups: set **Everyone else** to a role and list your Workspace domain under **Allowed email domains**.",
      },
      {
        title: "Microsoft Entra ID",
        text: "In Entra → App registrations, register an app with the Redirect URL as a **Web** platform and create a client secret. The issuer is `https://login.microsoftonline.com/<tenant-id>/v2.0`. For roles, add **App roles** (such as `Visionary.Admin`), assign people to them and map them with the claim `roles`; groups arrive as object IDs in `groups` if you turn on the groups claim. For SAML, create an Enterprise application, upload Visionary's metadata and paste the **App Federation Metadata Url**.",
      },
      {
        title: "Okta",
        text: "Create an **OIDC Web Application** with the Redirect URL as a sign-in redirect URI; the issuer is your Okta domain (or its authorization server). Under **Sign On**, add a **groups** claim (filter, for example, starts with `visionary`) and map those groups. For SAML, create a SAML 2.0 app with the ACS URL as **Single sign-on URL** and the entity ID as **Audience URI**, add a group attribute statement named `groups`, then paste the app's metadata URL.",
      },
      {
        title: "Auth0 and Keycloak",
        text: "**Auth0:** create a **Regular Web Application**, add the Redirect URL to **Allowed Callback URLs**; the issuer is `https://<tenant>.auth0.com/`. Roles need a post-login Action that sets a namespaced claim such as `https://example.com/roles`, which you name as the groups claim. **Keycloak:** create an OpenID Connect client with client authentication on and the Redirect URL as a valid redirect URI; the issuer is `https://<host>/realms/<realm>`. Add a **Group Membership** mapper with the claim `groups` and **Full group path** off.",
      },
      {
        title: "Map groups to roles",
        text: "**Groups and roles** maps the provider's groups to Visionary roles, such as `visionary-admins=admin,engineering=editor`; the highest one wins. **Everyone else** is the role for people in none of them, or **No access** to turn them away. **Allowed email domains** limits who gets in, and **Create accounts** makes an account the first time someone signs in.",
      },
      {
        title: "Test, then switch it on",
        text: "**Test sign-in** opens the provider in a new tab and, instead of signing you in, shows what the provider sent and the role it would give, or exactly what went wrong. When it looks right, switch the provider on and save. Every sign-in, refusal and change is in the audit log.",
      },
      {
        title: "Provision with SCIM",
        text: "Under **API keys**, create a key of scope **SCIM**. At the provider, turn on SCIM provisioning with the **SCIM base URL** from Settings and the key as a bearer token (Okta: **API Token**; Entra: **Secret Token**). People it adds show under **People from single sign-on**; switching someone off at the provider signs them out at once. To take roles from SCIM groups, fill in **Groups and roles** under **Provisioning (SCIM)**.",
      },
    ],
  },
  {
    id: "workspaces",
    group: "Manage",
    title: "Workspaces",
    intro:
      "Workspaces let each team see only its own services. A workspace names services and members, and the server filters every read for its members: pages, Explore, the analyst, dashboards, alerts, exports and the Prometheus API. Admins always see everything, and with no workspaces everyone does.",
    shot: "settings-workspaces",
    caption: "Two workspaces, each a set of services and its members.",
    steps: [
      {
        title: "Create a workspace",
        text: "In **Settings → Workspaces**, click **New workspace**, name it and list its services, one per line: an exact name such as `checkout`, or a pattern with `*` at the start or end such as `payments-*`. The preview shows which services seen in the last 30 days it matches.",
      },
      {
        title: "Add members",
        text: "Add people, groups and read API keys, each as **Viewer**, **Editor** or **Admin** of the workspace. Groups are what your identity provider sends at sign-in, SCIM group names, or the roles of a delegated sign-in. Inside the workspace that role applies instead of the person's own; a workspace admin manages its members but is never an admin of the whole server.",
      },
      {
        title: "Decide about everyone else",
        text: "**People outside every workspace** see every service, as before, or nothing until you add them to one. Data without a service name, such as rows from older SDKs, is seen only by people who see everything.",
      },
      {
        title: "Give things a workspace",
        text: "Under **What belongs to a workspace**, pick a workspace for alert rules, uptime checks, dashboards and saved log views. A workspace's alert rules watch only its services, and its checks and rules are seen only by its members. What someone creates inside a workspace belongs to it.",
      },
      {
        title: "Switch between workspaces",
        text: "Someone in several workspaces, and every admin, gets a picker under the logo. **All my workspaces** shows them together; picking one narrows every page to its services until you pick again.",
      },
    ],
  },
  {
    id: "config-as-code",
    group: "Manage",
    title: "Configuration as code",
    intro:
      "Keep alerts, uptime checks and dashboards in git: manage them with Terraform, or move the whole setup between installs as one file from **Settings → Configuration**.",
    shot: "settings-config",
    caption: "Export everything as one file, or preview an import before applying it.",
    steps: [
      {
        title: "Create a key for Terraform",
        text: "In **Settings → API keys**, create a key with the **Editor** scope, or **Admin** if Terraform should manage API keys too. Give it to the provider as `api_key`, or as `VISIONARY_API_KEY`, with this server's address as `url` or `VISIONARY_URL`.",
      },
      {
        title: "Write the configuration",
        text: "Require the provider `styropyr0/visionary`. **Help → Configuration as code** on the Settings page has a starting `main.tf` with a channel, a rule and a check. Resources: `visionary_alert_rule`, `visionary_alert_channel`, `visionary_alert_silence`, `visionary_uptime_check` (HTTP, or a browser check with steps), `visionary_dashboard` (its panels as JSON, written with `jsonencode([...])`) and `visionary_api_key`. Data sources: `visionary_services` and `visionary_alert_channel`, which finds a channel by name.",
      },
      {
        title: "Plan and apply",
        text: "`terraform plan` shows what would change, `terraform apply` makes it so. Channel URLs and keys, and a new key's secret, are kept in the state as sensitive values; a key's secret is only there for the key Terraform created. A channel's kind can't change, so changing it replaces the channel.",
      },
      {
        title: "Bring in what you already have",
        text: "Take over an existing object with its id, e.g. `terraform import visionary_alert_rule.slow 12`. Ids are in the HTTP API's lists, such as `/api/alerts/rules`. A channel's URL and key are never read back, so set them in the configuration.",
      },
      {
        title: "Export and import a file",
        text: "Without Terraform, **Settings → Configuration → Export** downloads alert rules, channels, silences, uptime checks, saved panels, dashboards, saved log searches and hidden pages as YAML or JSON, matched by name. Secrets stay behind. On the other install, **Import** → **Preview changes** lists what would be created, updated or left unchanged, and why anything is skipped; **Apply** writes it. Importing never deletes.",
      },
    ],
  },
];
