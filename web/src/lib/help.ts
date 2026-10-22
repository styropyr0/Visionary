import type { Route } from "../components/Sidebar";

/**
 * What each page shows and how to fill it, for the sidebar tips and the help
 * drawer. Text marks code with `backticks`.
 */

export type Lang = "go" | "node" | "python" | "ruby" | "java" | "dotnet";

export const LANGS: { key: Lang; name: string }[] = [
  { key: "go", name: "Go" },
  { key: "node", name: "Node" },
  { key: "python", name: "Python" },
  { key: "ruby", name: "Ruby" },
  { key: "java", name: "Java" },
  { key: "dotnet", name: ".NET" },
];

/** One language's way to do a setup step: a note, code, or both. */
export interface LangSetup {
  note?: string;
  code?: string;
  /** What the code is, for snippets that aren't a language's code ("Environment"). */
  label?: string;
}

export interface SetupTopic {
  id: string;
  title: string;
  intro: string;
  /** Per language; a language left out does not support it yet. */
  langs?: Partial<Record<Lang, LangSetup>>;
  /** For steps that are the same everywhere (environment, an installer). */
  any?: LangSetup;
}

export interface PageHelp {
  title: string;
  /** One or two sentences, for the sidebar tip. */
  tip: string;
  what: string;
  read: string[];
  setup: string[];
}

export const TOPICS: Record<string, SetupTopic> = {
  sdk: {
    id: "sdk",
    title: "Add the SDK",
    intro:
      "The SDK's middleware records every request (route, status, duration, user, bodies, errors) and samples the process every few seconds. It writes straight to the database this server reads, in the background, so it never slows a request. Set `VISIONARY_DATABASE_URL` and `VISIONARY_SERVICE` in the service's environment.",
    langs: {
      go: {
        code: `import visionary "github.com/styropyr0/visionary/sdks/go"

agent, _ := visionary.New(visionary.FromEnv()) // nil agent = collection off
agent.Start(ctx)
defer agent.Close(context.Background())

http.ListenAndServe(":8080", agent.Middleware(mux))`,
      },
      node: {
        code: `import visionary from "visionary-sdk";

visionary.init();
app.use(visionary.express());          // or visionary.fastify(), visionary.koa()
// ... routes ...
app.use(visionary.expressErrorHandler());`,
      },
      python: {
        code: `# Django settings.py, first in MIDDLEWARE
"visionary.integrations.django.VisionaryMiddleware",

# FastAPI / Starlette
app.add_middleware(VisionaryASGIMiddleware)

# Flask
Visionary(app)`,
      },
      ruby: { note: "Rails loads it from the Gemfile on its own. Other Rack apps add the middleware first in `config.ru`.", code: `# Gemfile
gem "visionary-sdk", require: "visionary"

# config.ru (not Rails)
use Visionary::Middleware` },
      java: { note: "The Spring Boot starter configures itself; add the JDBC driver for your database too.", code: `<dependency>
  <groupId>io.github.styropyr0</groupId>
  <artifactId>visionary-spring-boot-starter</artifactId>
  <version>0.4.0</version>
</dependency>` },
      dotnet: {
        code: `builder.Services.AddVisionary();
var app = builder.Build();
app.UseVisionary(); // first, before exception handlers, auth and routing`,
      },
    },
  },
  user: {
    id: "user",
    title: "Say who made each request",
    intro: "Attach the signed-in user once authentication has run, and Request logs, Security and the analyst can name them.",
    langs: {
      go: { code: `visionary.SetUser(r.Context(), visionary.User{ID: "42", Handle: "ada", Role: "ADMIN"})` },
      node: { code: `visionary.setUser({ id: user.id, handle: user.email, role: user.role });` },
      python: { code: `visionary.set_user(user.id, handle=user.email, role="admin")` },
      ruby: { code: `before_action { Visionary.set_user(current_user.id, handle: current_user.email, role: current_user.role) if current_user }` },
      java: { code: `Visionary.setUser(request, user.getId(), user.getUsername(), user.getRole());` },
      dotnet: { code: `Visionary.SetUser(ctx, id, handle: "ann", role: "admin");` },
    },
  },
  db: {
    id: "db",
    title: "Record database queries",
    intro:
      "One line where the database pool is created. Each statement becomes a span with its values replaced by `?`; parameter values are never stored. Without it, Queries stays empty and request breakdowns show the time as Untracked.",
    langs: {
      go: {
        code: `poolCfg.ConnConfig.Tracer = visionary.PgxTracer(poolCfg.ConnConfig.Tracer) // pgx

db, err := visionary.OpenDB("postgres", dsn, "postgresql/shop")    // database/sql, like sql.Open`,
      },
      node: {
        code: `visionary.instrumentPg(pg);                               // pg: every Client and Pool
const pool = visionary.instrumentMysql2(mysql.createPool(url)); // mysql2
const db = visionary.instrumentSqlite(new Database("app.db"));  // better-sqlite3
const prisma = new PrismaClient().$extends(visionary.prismaExtension());`,
      },
      python: {
        note: "The Django ORM is recorded on its own.",
        code: `engine = visionary.instrument_sqlalchemy(create_engine(DSN))  # SQLAlchemy
conn = visionary.instrument_connection(conn)                  # psycopg, PyMySQL, sqlite3`,
      },
      ruby: { note: "Automatic: every ActiveRecord query is recorded." },
      java: { note: "Automatic: every `DataSource` bean is wrapped, so JPA/Hibernate, `JdbcTemplate`, jOOQ and plain JDBC are all seen." },
      dotnet: { note: "Automatic: Npgsql, MySqlConnector and EF Core commands on any provider." },
    },
  },
  http: {
    id: "http",
    title: "Record outbound HTTP calls",
    intro:
      "Each call becomes a span named `METHOD host`, failed on a transport error or a 5xx answer. Query strings are never stored. Vendor SDKs (AWS S3 or R2, OpenAI, Stripe, Firebase) build their own HTTP clients, so hand them a traced one or their calls stay invisible.",
    langs: {
      go: {
        note: "`http.DefaultClient` is traced for you. Every client you build needs the transport, and each request needs the handler's context, or the call cannot join its request.",
        code: `client := &http.Client{Transport: visionary.Transport(nil)}
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
resp, err := client.Do(req)

// AWS SDK v2 (S3, R2, SQS, SES)
traced := &http.Client{Transport: visionary.Transport(awshttp.NewBuildableClient().GetTransport())}
cfg, _ := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithHTTPClient(traced))`,
      },
      node: { note: "Automatic: `http`, `https` and `fetch` are patched by `init()`, which covers most vendor SDKs too." },
      python: { code: `visionary.instrument_http()  # once at startup: requests, httpx, urllib3
# or set VISIONARY_INSTRUMENT_HTTP=true` },
      ruby: { note: "Automatic: `Net::HTTP` and the clients built on it (Faraday's default adapter, HTTParty)." },
      java: { note: "Automatic for `RestTemplate`, `RestClient` and `WebClient` built from Boot's builders. Clients made with `new RestTemplate()` or `RestClient.create()` are not seen." },
      dotnet: { note: "Automatic: every `HttpClient`, including `IHttpClientFactory` clients." },
    },
  },
  spans: {
    id: "spans",
    title: "Name everything else",
    intro:
      "Redis, queues, gRPC and your own CPU work (hashing, image resizing, building a report) make no database or HTTP call, so their time shows as Untracked. Wrap them in a span and they get a name. Spans of kind custom also give Queries its call depth.",
    langs: {
      go: { code: `end := visionary.StartSpan(ctx, visionary.KindCache, "GET session")
val, err := rdb.Get(ctx, key).Result()
end(err)` },
      node: { code: `const user = await visionary.span("cache", "GET user", () => redis.get(key));` },
      python: { code: `with visionary.span("cache", "GET session"):
    value = redis.get(key)` },
      ruby: { code: `Visionary.span("GET session", kind: "cache", target: "redis") { redis.get(key) }` },
      java: { code: `try (VisionarySpan span = Visionary.span("cache", "GET session")) {
    span.target("redis/sessions");
    return cache.get(key);
}` },
      dotnet: { code: `using (var span = Visionary.StartSpan("cache", "GET session", "redis/sessions"))
{
    value = await redis.StringGetAsync(key);
}` },
    },
  },
  background: {
    id: "background",
    title: "Show work a request leaves running",
    intro:
      "Work a handler starts and doesn't wait for (sending an email, resizing an upload) still belongs to its request. Start it with the SDK's background helper and the request's breakdown shows it as one span, with the calls it made and when it finished after the response. It isn't cut short when the response is sent, and it doesn't count towards the request's time.",
    langs: {
      go: { code: `visionary.Go(r.Context(), "send welcome email", func(ctx context.Context) error {
    return mailer.Send(ctx, user.Email)
})` },
      node: { note: "Calls made after the response are linked to their request on their own; the helper names the work.", code: `visionary.background("send welcome email", () => mailer.send(user.email));` },
      python: { code: `visionary.background("send welcome email", send_welcome, user.email)  # starts a thread

with visionary.background_span("send welcome email"):  # inside your own executor or task
    send_welcome(user.email)` },
      ruby: { code: `Visionary.background("send welcome email") { Mailer.welcome(user).deliver_now }` },
      java: { code: `Visionary.background("send welcome email", () -> mailer.send(user.getEmail()));

executor.submit(Visionary.wrap("send welcome email", () -> mailer.send(user.getEmail())));` },
      dotnet: { code: `Visionary.Background("send welcome email", ct => mailer.SendAsync(user.Email, ct));` },
    },
  },
  errors: {
    id: "errors",
    title: "Report handled errors",
    intro: "Unhandled exceptions and panics are recorded on their own. Errors your code catches and turns into a response are only seen when you report them.",
    langs: {
      go: { code: `visionary.CaptureError(r.Context(), err)` },
      node: { code: `visionary.captureException(err);` },
      python: { code: `visionary.capture_exception()  # inside except, or capture_exception(exc)` },
      ruby: { code: `Visionary.capture_exception(e)` },
      java: { code: `Visionary.captureException(e);` },
      dotnet: { code: `Visionary.CaptureException(e);` },
    },
  },
  release: {
    id: "release",
    title: "Tag requests with a version",
    intro:
      "Every SDK reads the version from `VISIONARY_RELEASE`, or from the commit variable of Railway, Render, Vercel, Heroku or GitHub Actions. It goes on every request and error, and each service start with a new version is recorded as a deploy: the marks on charts, version comparisons and After a deploy.",
    any: { code: `VISIONARY_RELEASE=$(git rev-parse --short HEAD)`, label: "Environment" },
  },
  graphql: {
    id: "graphql",
    title: "Record GraphQL operations",
    intro: "Each operation becomes a span with its document (literals replaced by `?`), so Queries can measure its depth, field count and the database work its resolvers did.",
    langs: {
      go: { note: "From a gqlgen `AroundOperations` extension:", code: `oc := graphql.GetOperationContext(ctx)
end := visionary.GraphQLOperation(ctx, string(oc.Operation.Operation), oc.OperationName, oc.RawQuery)
defer end(nil)` },
      node: { code: `new ApolloServer({ typeDefs, resolvers, plugins: [visionary.apolloPlugin()] });

// any other server
await visionary.graphqlOperation("query", operationName, source, () => execute(args));` },
    },
  },
  explain: {
    id: "explain",
    title: "Allow query plans",
    intro:
      "Visionary never connects to your database. When you ask for a plan, it queues the request and your SDK runs `EXPLAIN` (never `EXPLAIN ANALYZE`, so nothing is executed) and sends the plan back within about 10 seconds. `VISIONARY_EXPLAIN=false` turns it off without a code change.",
    langs: {
      go: { code: `cfg.Explain = []*visionary.Explainer{visionary.PgxExplainer(pool)}
// database/sql: visionary.SQLExplainer(db, visionary.MySQL, "mysql/shop")` },
      node: { code: `visionary.init({ explain: [visionary.explainWith(pool)] }); // pg or mysql2` },
    },
  },
  profile: {
    id: "profile",
    title: "Turn on CPU profiles",
    intro:
      "No code needed: every server SDK takes CPU profiles once an admin turns them on in Settings → Profiling, for every service or only the ones chosen there, and stops within 30 seconds when they are turned off. By default each profile samples for 10 seconds every 5 minutes, only while requests are being served. It costs a few percent of one CPU while sampling and nothing in between. Profile now, on the CPU profiles page, takes one at once.",
    any: { note: "To keep a service from ever being profiled, whatever the dashboard says, set this in its environment:", code: `VISIONARY_PROFILE=false`, label: "Environment" },
  },
  checks: {
    id: "checks",
    title: "Add an uptime check",
    intro:
      "No code needed: this server calls the URL itself on a schedule, from outside your app, so it notices when the whole service is down, not just one route. Choose the method, the status codes that count as up (200-299 by default), optionally a word the body must contain, how often, the timeout, and a response time above which the check counts as degraded. Headers can carry a token; values that look secret are never shown again. Failing checks open incidents and can alert.",
    any: { note: "Checks can't reach private or loopback addresses (localhost, 10.x, 192.168.x) unless the server allows it:", code: `VISIONARY_UPTIME_ALLOW_PRIVATE=true`, label: "Server environment" },
  },
  statuspage: {
    id: "statuspage",
    title: "Publish a status page",
    intro:
      "A public page at /status (and /status.json) shows the checks you mark Show on the public status page, with their uptime and incidents; their URLs only when you tick that too. An admin turns it on and sets its title in Settings → Status page.",
  },
  agent: {
    id: "agent",
    title: "Install the host agent",
    intro:
      "The SDKs see the process they run in. The agent watches a whole Linux machine, app or not: CPU, memory, disks, processes, systemd services, Docker or Podman containers, SSH logins and open ports. It posts here with a server ingest key from Settings → API keys.",
    any: { code: `curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \\
  | sudo VISIONARY_URL=${typeof location === "undefined" ? "https://visionary.example.com" : location.origin} VISIONARY_KEY=<server key> sh` },
  },
  client: {
    id: "client",
    title: "Add the browser or mobile SDK",
    intro:
      "Web and mobile apps post to this server with a public client key (Settings → API keys, or `VISIONARY_CLIENT_KEYS`). Browsers must also be allowed in `VISIONARY_CLIENT_ORIGINS` when it is set. The Android and iOS SDKs are in the repository's `sdks` folder.",
    any: { code: `<script src="https://unpkg.com/visionary-browser/dist/visionary.min.js"></script>
<script>
  Visionary.init({ endpoint: "${typeof location === "undefined" ? "https://visionary.example.com" : location.origin}", key: "pk_…", app_version: "1.0.0" });
</script>` },
  },
};

export const HELP: Record<Route, PageHelp> = {
  overview: {
    title: "API overview",
    tip: "Traffic, errors and latency of every service at a glance, with the routes and users behind them.",
    what: "The headline numbers for the time range: requests, error rate, latency percentiles and their change from the previous period, then the busiest and slowest routes, status codes and top users.",
    read: [
      "Each tile compares with the previous period of the same length; the arrow is green when the change is good.",
      "Deploy marks on the charts show when a new version started, so a step in a line can be matched to a release.",
      "Click a route or a user to open their requests in Request logs.",
    ],
    setup: ["sdk", "user", "release"],
  },
  performance: {
    title: "Performance",
    tip: "Where time goes: Apdex, SLOs, the slowest routes and requests, and each request's database and outbound calls on a timeline.",
    what: "How fast the API is for its users and why. Apdex and the SLO summarise satisfaction; the route table ranks routes by busy time; slowest requests open a breakdown of every call they made; the endpoint breakdown splits a route's time into database, outbound HTTP and its own code.",
    read: [
      "Busy time (count × duration) is where optimising pays off most, more than the single slowest request.",
      "In a request's breakdown, Untracked is time no recorded call covers: a client without a hook, or your own CPU work. The hatched row sits where the time went.",
      "Exclude routes from Slowest requests when they are slow by design (exports, uploads), and group by route to compare them.",
      "N+1 flags a call repeated 10 or more times in one request.",
    ],
    setup: ["sdk", "db", "http", "spans", "background", "release"],
  },
  queries: {
    title: "Queries",
    tip: "Every SQL statement with its rate, latency and routes; queries per request; what a deploy changed; GraphQL operations; and query plans.",
    what: "Four views of the database work your services do. Catalogue lists each distinct statement; Per route shows how many queries a request makes and how deep its calls nest; After a deploy compares each statement before and after a release; GraphQL shows each operation's depth, field count and the queries it caused. Open a statement to see its plan.",
    read: [
      "Total time (calls × average) ranks the statements worth tuning first. New marks a statement that did not run in the previous period.",
      "Queries per request well above its median on some requests usually means a loop issuing one query per item (N+1).",
      "Depth counts only spans that wrap other calls (custom and GraphQL spans); database and HTTP calls never nest.",
      "A plan's warnings point at full scans of large tables and sorts an index would avoid. Explain in plain words asks the analyst.",
    ],
    setup: ["db", "release", "graphql", "explain", "spans"],
  },
  dependencies: {
    title: "Dependencies",
    tip: "Every third-party API your services call (Stripe, S3, OpenAI…), with its rate, failures and latency, like a route of your own.",
    what: "Outbound HTTP calls grouped by host: how often each is called, how often it fails, how slow it is against the previous period, and which of your routes call it. Open a host for its slowest calls.",
    read: [
      "A host whose p95 rose while your code did not change is the dependency slowing you down.",
      "Failures count transport errors and 5xx answers; a 4xx is the caller's mistake and is not counted.",
      "Calls from vendor SDKs appear only once they use a traced HTTP client.",
    ],
    setup: ["http"],
  },
  profiles: {
    title: "CPU profiles",
    tip: "Flame graphs of where your services spend CPU while serving requests, by service or by the routes that were busy. Turned on in Settings.",
    what: "Sampled CPU profiles merged into one flame graph. Filter to a service, to a route to see only the profiles taken while it was busy, or open a single profile. The table under the graph lists the functions that used the most CPU themselves. Profiling is off until an admin turns it on in Settings → Profiling.",
    read: [
      "Each frame is as wide as the samples taken in it and everything it called; callers above, callees below. Click to zoom, search to highlight.",
      "Colours separate your code, third-party libraries, the language's standard library, and runtime work such as garbage collection.",
      "Start with the self-time table: a function high there is doing the work, not just calling it.",
    ],
    setup: ["profile"],
  },
  system: {
    title: "System",
    tip: "CPU, memory, disk, network, runtime and database pool of every instance, from the SDKs and the host agent.",
    what: "Resource use of each instance: host CPU and memory, the process's own CPU and memory, garbage collection and threads, database connections in use and time spent waiting for one. Machines with the host agent add disks, processes and services.",
    read: [
      "Waiting for a connection above zero means the pool is too small for the traffic, or queries hold connections too long.",
      "In a container the host charts describe the machine underneath; the container panel shows what the service may use.",
    ],
    setup: ["sdk", "agent"],
  },
  containers: {
    title: "Containers",
    tip: "Docker and Podman containers: CPU and memory against their limits, restarts, OOM kills and health checks.",
    what: "Every container the host agent sees, or that an SDK reports from inside its own container: state, limits, CPU and memory against those limits, throttling, and events such as restarts, exits, out-of-memory kills and failing health checks.",
    read: [
      "CPU is a percent of the container's limit, or of all the machine's CPUs when it has none.",
      "Throttling means the container wanted more CPU than its limit allowed.",
      "Containers reported by an SDK show their own usage only; restarts and exits need the host agent.",
    ],
    setup: ["agent", "sdk"],
  },
  errors: {
    title: "Errors",
    tip: "Exceptions and panics grouped into issues, with stack traces, affected users and the request that raised them.",
    what: "Every exception the SDKs captured, grouped by type and place into issues. Each issue shows how often it happens, since which version, the users it reached, its stack trace and the requests that raised it.",
    read: [
      "Handled errors are the ones your code caught and reported; unhandled ones crashed the request.",
      "First seen and the version column tell whether a release introduced the issue.",
    ],
    setup: ["errors", "release", "user"],
  },
  frontend: {
    title: "Frontend",
    tip: "What your users experience in the browser and in mobile apps: Web Vitals, page and screen loads, and client errors.",
    what: "Data from the browser, Android and iOS SDKs: Core Web Vitals per page, screen load times per device, JavaScript and app errors, and sessions.",
    read: [
      "Web Vitals are rated good, needs improvement or poor by Google's thresholds, at the 75th percentile.",
      "Slow screens on low-end devices show up in the device breakdown.",
    ],
    setup: ["client"],
  },
  uptime: {
    title: "Uptime",
    tip: "Checks that call your endpoints from outside on a schedule, with response times, incidents and an optional public status page.",
    what: "HTTP checks this server runs on a schedule, independent of any SDK. Each check shows its availability, response time and incidents; failing checks can alert.",
    read: [
      "A check fails after its retries, so a single slow answer does not open an incident.",
      "The public status page shows the checks you mark as public, once it is turned on in Settings → Status page.",
    ],
    setup: ["checks", "statuspage"],
  },
  security: {
    title: "Security",
    tip: "Suspicious traffic and host events: brute-force logins, scanners, request floods, SSH logins, opened ports, and the audit log.",
    what: "Findings from the API detectors (brute force and credential stuffing on login routes, accounts targeted from many addresses, scanners probing paths like `/.env`, request floods, bursts of 403s) and, with the host agent, SSH logins, sudo, opened ports and pending updates. The audit log records who changed what in Visionary.",
    read: [
      "Each finding links to the requests behind it, so you can see what was sent and by whom.",
      "`VISIONARY_SECURITY=false` turns the API detectors off; host events still show.",
    ],
    setup: ["sdk", "user", "agent"],
  },
  explore: {
    title: "Explore",
    tip: "Ask questions about your data in plain words, or write SQL, and get charts and tables back.",
    what: "The analyst turns a question into read-only SQL over your telemetry tables, runs it and shows the result as a chart or table. You can edit the SQL, and save a result as a panel for a dashboard.",
    read: [
      "The analyst only reads; it cannot change your data.",
      "Check the SQL it wrote when a number looks off, and refine the question.",
    ],
    setup: [],
  },
  logs: {
    title: "Request logs",
    tip: "Every request with its route, status, duration, user, headers and bodies, searchable and filterable.",
    what: "The raw log of requests your services served. Filter by route, status, user or text, and open one for its headers, redacted bodies, error and the timeline of its calls.",
    read: [
      "Bodies are stored up to a size cap and with secrets redacted.",
      "The breakdown in a request shows each database and outbound call, and Untracked for time no call covers.",
    ],
    setup: ["sdk", "user", "db", "http", "background"],
  },
  dashboards: {
    title: "Dashboards",
    tip: "Your own pages of charts, built from saved panels and arranged as you like.",
    what: "Dashboards collect panels you saved from Explore or other pages, arranged on a grid and shared with your team.",
    read: ["Every panel follows the dashboard's time range."],
    setup: [],
  },
  saved: {
    title: "Saved panels",
    tip: "Charts and queries you saved, ready to reuse on dashboards or open again.",
    what: "Every panel you saved, with its query. Open one to run it again, or add it to a dashboard.",
    read: [],
    setup: [],
  },
  alerts: {
    title: "Alerts",
    tip: "Rules that notify Slack, Discord, email or a webhook when error rate, latency, uptime or anything you can query crosses a threshold.",
    what: "Alert rules, the channels they notify, their history and the weekly digest. A rule watches a metric or a query and fires when it crosses its threshold for long enough.",
    read: [
      "Each firing is kept in history with the value that triggered it.",
      "Email needs `VISIONARY_SMTP_HOST` and `VISIONARY_SMTP_FROM` on the server.",
    ],
    setup: [],
  },
  settings: {
    title: "Settings",
    tip: "Your preferences, the team, API keys, thresholds, retention and deleting data, CPU profiling, which pages show, the status page and the audit log.",
    what: "Preferences are yours alone: start page, the time range and auto-refresh a visit starts with, theme, time zone and clock. The rest is the team's: who can sign in and with which role, API keys, the Apdex and SLO thresholds (with routes that have their own), how long each kind of data is kept and deleting it, CPU profiling, which pages the sidebar shows, the public status page, and the audit log.",
    read: [
      "Changes reach every server sharing the database within 30 seconds; no restart or environment variable is needed.",
      "Admins change the team's settings and see the audit log; editors change alerts and dashboards and can ask for a profile now; viewers only read. Everyone sets their own preferences.",
      "Hidden pages stay reachable by their link; Settings always shows.",
      "Deleting data needs a preview first and typing delete to confirm; it is recorded in the audit log and never touches settings, dashboards, alert rules or uptime checks.",
    ],
    setup: [],
  },

};
