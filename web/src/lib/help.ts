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

/** This server's address, for snippets that post to it. */
const origin = typeof location === "undefined" ? "https://visionary.example.com" : location.origin;

export const TOPICS: Record<string, SetupTopic> = {
  sdk: {
    id: "sdk",
    title: "1. Add the SDK to your service",
    intro:
      "Install the package and add its middleware before your routes. The middleware records every request (route, status, duration, user, bodies, errors) and samples the process every few seconds. It writes straight to the database in the background, so it never slows a request, and a database that is down never breaks one.",
    langs: {
      go: {
        note: "Install: `go get github.com/styropyr0/visionary/sdks/go`, plus the driver for your database URL (`github.com/jackc/pgx/v5`, `github.com/go-sql-driver/mysql` or `modernc.org/sqlite`; D1 needs none).",
        code: `import (
	_ "github.com/jackc/pgx/v5/stdlib" // the driver for your database URL
	visionary "github.com/styropyr0/visionary/sdks/go"
)

agent, err := visionary.New(visionary.FromEnv())
if err != nil {
	log.Print(err) // collection is off; a nil agent is a no-op
}
agent.Start(ctx)
defer agent.Close(context.Background())

http.ListenAndServe(":8080", agent.Middleware(mux))`,
      },
      node: {
        note: "Install: `npm install visionary-sdk`, plus `pg`, `mysql2` or `better-sqlite3` for your database (D1 needs none). Express, Fastify, Koa and NestJS are supported.",
        code: `import visionary from "visionary-sdk";

visionary.init();                       // reads VISIONARY_DATABASE_URL
app.use(visionary.express());           // or visionary.fastify(), visionary.koa()
// ... routes ...
app.use(visionary.expressErrorHandler());`,
      },
      python: {
        note: "Install: `pip install \"visionary-sdk[postgres]\"` (or `[mysql]`; plain `visionary-sdk` for SQLite and D1). Add `[system]` for host CPU and memory on macOS and Windows.",
        code: `# Django: settings.py, first in MIDDLEWARE
"visionary.integrations.django.VisionaryMiddleware",

# FastAPI / Starlette
from visionary.integrations.asgi import VisionaryASGIMiddleware
app.add_middleware(VisionaryASGIMiddleware)

# Flask
from visionary.integrations.flask import Visionary
Visionary(app)`,
      },
      ruby: {
        note: "Add the gem and run `bundle install`. The app needs its database's driver (`pg`, `mysql2` or `sqlite3`). Rails loads the middleware on its own; other Rack apps (Sinatra, Hanami, Roda, Grape) add it first in `config.ru`.",
        code: `# Gemfile
gem "visionary-sdk", require: "visionary"

# config.ru (not Rails)
require "visionary"
use Visionary::Middleware
run MyApp`,
      },
      java: {
        note: "Spring Boot 3 and Java 17+. Add the starter (Maven below; Gradle: `implementation(\"io.github.styropyr0:visionary-spring-boot-starter:0.7.1\")`) and the JDBC driver for your database. It configures itself: no code needed.",
        code: `<dependency>
  <groupId>io.github.styropyr0</groupId>
  <artifactId>visionary-spring-boot-starter</artifactId>
  <version>0.7.1</version>
</dependency>`,
      },
      dotnet: {
        note: "Install: `dotnet add package Visionary.AspNetCore` (.NET 8+). Npgsql, MySqlConnector and EF Core are picked up on their own.",
        code: `using Styropyr0.Visionary;

builder.Services.AddVisionary();
var app = builder.Build();
app.UseVisionary(); // first, before exception handlers, auth and routing`,
      },
    },
  },
  connect: {
    id: "connect",
    title: "2. Point it at the same database",
    intro:
      "Server SDKs never call this server: they write to a database, and this server reads the same one. So give the service the exact database this Visionary server uses, and a name. A different database, schema or file means nothing shows up here. Browser and mobile apps, OpenTelemetry and the host agent are different: they post to this server over HTTPS with a key.",
    any: {
      label: "Service environment",
      note: "One of `postgres://`, `mysql://` or `sqlite:///path/to/file.db` (on Windows `sqlite:///C:/path/file.db`; SQLite only works when the service and this server share the file). For Cloudflare D1, leave the URL out and set `D1_ACCOUNT_ID`, `D1_DATABASE_ID` and `D1_API_TOKEN` to the same D1 database. In a `.env` file, comments start with `#`; a line starting with `;` stops the whole file from loading.",
      code: `VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/visionary   # the same database as this server
VISIONARY_SERVICE=my-api                                          # any name; tells services apart`,
    },
  },
  verify: {
    id: "verify",
    title: "3. Check that data arrives",
    intro:
      "Deploy or restart the service and send it one request. It shows up on the API overview and in Request logs within a few seconds, and the service appears in the service picker. Open Performance → Slowest requests and open one: time no call accounts for shows as Untracked, which points at a database or HTTP client that still needs its hook.",
    any: {
      note: "Nothing yet? Look at the service's log. `no database configured … collection is off` means `VISIONARY_DATABASE_URL` (or the `D1_*` variables) is not set in that process. A connection error means the URL is wrong or unreachable from there. No message and still no data: check the service writes to the same database (and schema, or SQLite file) this server reads. `visionary: queue full` means the database can't keep up; SQLite and D1 suit modest traffic, PostgreSQL and MySQL the rest.",
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
      "Visionary never connects to your database. When you ask for a plan, it queues the request and a service that was given an explainer for that database runs `EXPLAIN` (never `EXPLAIN ANALYZE`, so nothing is executed) and sends the plan back within about 10 seconds. The explainer's target must match the statement's target exactly, as shown on the statement: `postgresql/<dbname>` or `mysql/<dbname>`. If the plan stays pending, no running service has an explainer with that target. Go and Node have explainers; `VISIONARY_EXPLAIN=false` turns them off without a code change.",
    langs: {
      go: {
        note: "Build the config, add the explainer, then create the agent. `PgxExplainer` takes its target from the pool's database name.",
        code: `cfg := visionary.FromEnv()
cfg.Explain = []*visionary.Explainer{visionary.PgxExplainer(pool)} // target postgresql/<dbname>
// database/sql: visionary.SQLExplainer(db, visionary.MySQL, "mysql/shop")
agent, err := visionary.New(cfg)`,
      },
      node: {
        note: "The target is worked out from the pool's database; pass `{ target: \"postgresql/shop\" }` when it differs from the statement's.",
        code: `visionary.init({ explain: [visionary.explainWith(pool)] }); // pg or mysql2`,
      },
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
      "No code needed: this server calls the URL itself on a schedule, from outside your app, so it notices when the whole service is down, not just one route. Choose the method, the status codes that count as up (200-299 by default), optionally a word the body must contain, how often, the timeout, and a response time above which the check counts as degraded. Headers can carry a token; values that look secret are never shown again. Failing checks open incidents and alert: by the rule for every check, created with the first one, or by the check's own alert settings in its form.",
    any: { note: "Checks can't reach private or loopback addresses (localhost, 10.x, 192.168.x) unless an admin turns on Settings → Server → Other → Uptime checks may reach private addresses." },
  },
  statuspage: {
    id: "statuspage",
    title: "Publish a status page",
    intro:
      "A public page at /status (and /status.json) shows the checks you mark Show on the public status page, with their uptime and incidents; their URLs only when you tick that too.",
    any: { note: "An admin turns it on and sets its title in Settings → Status page. Then open an uptime check and tick Show on the public status page." },
  },
  agent: {
    id: "agent",
    title: "Install the host agent",
    intro:
      "The SDKs see the process they run in. The agent watches a whole Linux machine, app or not: CPU, memory, disks, processes, systemd services, Docker or Podman containers, SSH logins and open ports. It runs as a systemd service and posts here over HTTPS with a Server ingest key from Settings → API keys; it never needs database credentials. The machine shows up in System within a minute, and its containers in Containers.",
    any: { code: `curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \\
  | sudo VISIONARY_URL=${origin} VISIONARY_KEY=<server key> sh` },
  },
  clientkey: {
    id: "clientkey",
    title: "1. Create a client key",
    intro:
      "Web and mobile apps don't write to the database; they post to this server with a public client key. Client keys can only send events, so they are safe to ship in an app.",
    any: { note: "Create one with the Client ingest scope in Settings → API keys (or list your own under Settings → Server → Ingest → Client keys). For a website, also add its origin to Settings → Server → Ingest → Allowed web origins, which applies after a restart (wildcards like `https://*.example.com` work; empty allows any)." },
  },
  browser: {
    id: "browser",
    title: "2. Add the browser SDK",
    intro:
      "Run `npm install visionary-browser` and call `init` once, early, with this server's URL and the client key. A script tag works too: load `https://unpkg.com/visionary-browser@0.1.0/dist/visionary.min.js` and call `Visionary.init` with the same options. Page loads, Web Vitals, route changes, API calls and errors show up on Frontend within a few seconds of the first visit.",
    any: {
      label: "JavaScript",
      code: `import Visionary from "visionary-browser";

Visionary.init({
  endpoint: "${origin}",
  key: "pk_…",            // the client key
  app_version: "1.0.0",
});
Visionary.setUser(user.id); // optional`,
    },
  },
  ios: {
    id: "ios",
    title: "2. Add the iOS SDK",
    intro:
      "Swift Package Manager, iOS 14+: in Xcode, File → Add Package Dependencies, enter `https://github.com/styropyr0/Visionary.git` and add the `Visionary` library to your app target. Start it first thing at launch. Screens, API calls, errors and crashes show up on Frontend.",
    any: {
      label: "Swift",
      code: `import Visionary

Visionary.start(config: VisionaryConfig(
    endpoint: URL(string: "${origin}")!,
    key: "pk_…"
))`,
    },
  },
  android: {
    id: "android",
    title: "2. Add the Android SDK",
    intro:
      "minSdk 24. Add `implementation(\"io.github.styropyr0:visionary-android:0.7.1\")` from Maven Central and start it in your `Application`. For API timings, add `VisionaryInterceptor` to your OkHttp client. Screens, API calls, errors and crashes show up on Frontend.",
    any: {
      label: "Kotlin",
      code: `class App : Application() {
    override fun onCreate() {
        super.onCreate()
        Visionary.init(this, VisionaryConfig(
            endpoint = "${origin}",
            key = "pk_…",
        ))
    }
}`,
    },
  },
  otlp: {
    id: "otlp",
    title: "Or send OpenTelemetry",
    intro:
      "A service that already has an OpenTelemetry SDK can report here without a Visionary SDK. Point its OTLP/HTTP exporter at this server with a Server ingest key from Settings → API keys. Traces become requests and calls, exceptions become errors, metrics fill System. gRPC is not served; use the HTTP exporter. Whether spans and errors are kept, and how many, is set in Settings → Server → Ingest.",
    any: {
      label: "Service environment",
      code: `OTEL_EXPORTER_OTLP_ENDPOINT=${origin}
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer <server key>"
OTEL_SERVICE_NAME=my-api`,
    },
  },
  analyst: {
    id: "analyst",
    title: "Turn on the analyst",
    intro:
      "Explore and Ask need a model. Query results are sent to the model provider; IPs, user agents, headers and bodies are masked first.",
    any: { note: "An admin adds an OpenAI API key in Settings → Server → AI analyst. Any OpenAI-compatible API works: set its API base URL and model there too. Product name and Notes about your data help the answers." },
  },
  email: {
    id: "email",
    title: "Send alerts",
    intro:
      "A rule watches a metric or a query and notifies its channels. Slack, Discord, Teams, Google Chat and webhooks need only their URL; PagerDuty, Opsgenie, Telegram and SMS (Twilio) need their key. Add channels under Alerts → Channels.",
    any: { note: "Email needs an SMTP server: an admin fills in host, port, user, password and from address in Settings → Server → Email. Then add a rule, or start from a preset, and pick the channels it notifies." },
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
    setup: ["sdk", "connect", "verify", "user", "release", "otlp"],
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
    setup: ["sdk", "connect", "agent", "otlp"],
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
    setup: ["clientkey", "browser", "ios", "android"],
  },
  uptime: {
    title: "Uptime",
    tip: "Checks that call your endpoints from outside on a schedule, with response times, incidents and an optional public status page.",
    what: "HTTP checks this server runs on a schedule, independent of any SDK. Each check shows its availability, response time and incidents; the list says when a check would go down without anyone hearing.",
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
      "Settings → Server → Security detectors turns the API detectors off and tunes them; host events still show.",
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
    setup: ["analyst"],
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
    tip: "Rules that notify chat, email, paging or SMS when errors, latency, hosts, uptime, Web Vitals or anything you can query go wrong, with acknowledgements, escalation and silences.",
    what: "Alert rules, the channels they notify, their activity, silences and the weekly digest. A rule watches a metric or a query and fires when it crosses its threshold for long enough; presets cover the common cases.",
    read: [
      "Acknowledge a firing rule to stop its reminders and escalation; the channels that were told hear who took it.",
      "Silences hold back notifications during a maintenance window or quiet hours. Rules keep recording, and Activity says what was held back and why.",
      "Activity shows each channel's delivery. A failed send is retried for about 20 minutes before it is marked failed.",
      "Email needs an SMTP server in Settings → Server → Email.",
    ],
    setup: ["email"],
  },
  settings: {
    title: "Settings",
    tip: "Your preferences, the team, API keys, thresholds, retention and deleting data, CPU profiling, which pages show, the status page and the audit log.",
    what: "Preferences are yours alone: start page, the time range and auto-refresh a visit starts with, theme, time zone and clock. The rest is the team's: who can sign in and with which role, the server's own settings (the AI analyst, sessions, email, ingest keys and limits, security detectors), API keys, the Apdex and SLO thresholds (with routes that have their own), how long each kind of data is kept and deleting it, CPU profiling, which pages the sidebar shows, the public status page, and the audit log.",
    read: [
      "Changes reach every server sharing the database within 30 seconds; no restart or environment variable is needed.",
      "Admins change the team's settings and see the audit log; editors change alerts and dashboards and can ask for a profile now; viewers only read. Everyone sets their own preferences.",
      "Hidden pages stay reachable by their link; Settings always shows.",
      "Deleting data needs a preview first and typing delete to confirm; it is recorded in the audit log and never touches settings, dashboards, alert rules or uptime checks.",
      "To connect your apps, follow the steps below: a server SDK in each service, the browser or mobile SDK with a client key, OpenTelemetry, or the host agent.",
    ],
    setup: ["sdk", "connect", "verify", "clientkey", "browser", "ios", "android", "otlp", "agent", "explain", "analyst", "email"],
  },

};
