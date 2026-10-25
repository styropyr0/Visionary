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
        text: "**Routes**, **Top errors** and **Most active users** rank what was busiest and what failed. Then open the route in **Performance** or its requests in **Request logs**.",
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
        text: "**Routes by server time** puts the routes where speeding up pays most at the top. **Regressions** lists routes slower than in the previous period.",
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
    intro: "Every SQL statement your services run, the routes that run them, and what a deploy changed.",
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
        text: "Open a statement and look at its **Query plan**: your SDK runs EXPLAIN, Visionary points out full scans and missing indexes, and **Explain in plain words** asks the analyst. This needs the explainer hook; see **Help** on this page.",
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
    ],
  },
  {
    id: "uptime",
    group: "Monitor",
    title: "Uptime",
    route: "uptime",
    intro: "Checks this server runs against your endpoints on a schedule, with incidents and a public status page.",
    shot: "uptime",
    steps: [
      {
        title: "Add a check",
        text: "Click **New check**. Give it a name and URL; set the expected status, text the body must contain, how often to check, the timeout and **Degraded above** for slow answers. Under **Alerts when it goes down**, keep **The rule for every check** or give it its own settings.",
        shot: "uptime-form",
        caption: "A new check: what to call, what counts as up, and who hears when it is down.",
      },
      {
        title: "Watch the list",
        text: "Each row shows its state, the last 90 days, uptime and response time over 24 hours. The row buttons run a check now, pause it or edit it.",
      },
      {
        title: "Open a check",
        text: "Click a check for uptime over 24 hours to 90 days, response time, where the time goes (DNS, connect, TLS, first byte), its certificate, incidents and recent results.",
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
    id: "security",
    group: "Monitor",
    title: "Security",
    route: "security",
    intro: "Suspicious traffic found in your request log, events from your machines, and who changed what in Visionary.",
    shot: "security",
    steps: [
      {
        title: "Review findings",
        text: "**Findings** lists brute-force logins, credential stuffing, scanners, request floods and bursts of 403s. Filter by source, kind, severity, host or IP.",
      },
      {
        title: "Open a finding",
        text: "Click a finding for the requests behind it. **Request logs for this IP** shows everything the address sent; **Investigate** asks the analyst.",
        shot: "security-finding",
        caption: "A scanner probing for secrets, with the requests it sent.",
      },
      {
        title: "Tune or share",
        text: "**Settings → Server → Security detectors** sets how sensitive each detector is and which IPs to ignore. **Report** makes a printable summary of the range.",
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
        text: "The bookmark on a chart saves it to **Saved panels**, ready to add to a dashboard. Past analyses stay under **Recent**, in this browser.",
      },
    ],
  },
  {
    id: "logs",
    group: "Investigate",
    title: "Request logs",
    route: "logs",
    intro: "Every request your services served, searchable, with its headers, bodies and calls.",
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
        text: "In edit mode, **Add panel** offers your saved panels and the built-in ones from API overview, Performance and System. Drag a panel's header to move it and its corner to resize, then **Save**.",
      },
      {
        title: "Share it",
        text: "The link icon copies a link with the current range and selections. **TV** shows it full screen and can cycle through dashboards; press Esc to leave.",
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
        text: "**Digest** previews this week's summary of traffic, latency, alerts, errors and uptime. Under **Delivery**, pick the channels, weekday and hour, or **Send now**.",
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
        text: "**Members & roles** and **Sign-in** decide how people sign in and as what: viewers read, editors change alerts and dashboards, admins change everything.",
      },
      {
        title: "Create keys",
        text: "**API keys** → **Create key**: server keys for OpenTelemetry and the host agent, client keys for browser and mobile apps, read keys for scripts. A key is shown once.",
      },
      {
        title: "Set targets and retention",
        text: "**Thresholds** sets the Apdex target and SLO, with routes that have their own. **Data** sets how long each kind of data is kept, and deletes data after a preview.",
      },
      {
        title: "Configure the server",
        text: "**Server** holds the AI analyst key, sessions, email (SMTP), how often alerts run, ingest keys and limits, security detectors, and other switches. Changes reach every server within 30 seconds; a few are marked **After restart**.",
      },
      {
        title: "Tidy the sidebar",
        text: "**Pages** hides pages your team does not use, for everyone. Hidden pages still open from their link.",
      },
    ],
  },
];
