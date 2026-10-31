import type { Route } from "../components/Sidebar";
import { junitStep } from "./ci";

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
const hostname = typeof location === "undefined" ? "visionary.example.com" : location.hostname;

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
      "Deploy or restart the service and send it one request. It shows up on the API overview and in Requests within a few seconds, and the service appears in the service picker. Open Performance → Slowest requests and open one: time no call accounts for shows as Untracked, which points at a database or HTTP client that still needs its hook.",
    any: {
      note: "Nothing yet? Look at the service's log. `no database configured … collection is off` means `VISIONARY_DATABASE_URL` (or the `D1_*` variables) is not set in that process. A connection error means the URL is wrong or unreachable from there. No message and still no data: check the service writes to the same database (and schema, or SQLite file) this server reads. `visionary: queue full` means the database can't keep up; SQLite and D1 suit modest traffic, PostgreSQL and MySQL the rest.",
    },
  },
  user: {
    id: "user",
    title: "Say who made each request",
    intro: "Attach the signed-in user once authentication has run, and Requests, Security and the analyst can name them.",
    langs: {
      go: { code: `visionary.SetUser(r.Context(), visionary.User{ID: "42", Handle: "ada", Role: "ADMIN"})` },
      node: { code: `visionary.setUser({ id: user.id, handle: user.email, role: user.role });` },
      python: { code: `visionary.set_user(user.id, handle=user.email, role="admin")` },
      ruby: { code: `before_action { Visionary.set_user(current_user.id, handle: current_user.email, role: current_user.role) if current_user }` },
      java: { code: `Visionary.setUser(request, user.getId(), user.getUsername(), user.getRole());` },
      dotnet: { code: `Visionary.SetUser(ctx, id, handle: "ann", role: "admin");` },
    },
  },
  login: {
    id: "login",
    title: "Record sign-in attempts",
    intro:
      "In your login handler, record the account each sign-in tried: the email or username typed, whether or not it exists or the password was right. Security can then tell one address trying many accounts, many addresses trying one account, and someone signed in as one user trying another's password. Without it, failed sign-ins count per address only.",
    langs: {
      go: { code: `visionary.LoginAttempt(r.Context(), req.Email)` },
      node: { code: `visionary.loginAttempt(req.body.email);` },
      python: { code: `visionary.login_attempt(form.email)` },
      ruby: { code: `Visionary.login_attempt(params[:email])` },
      java: { code: `Visionary.loginAttempt(request, form.email());` },
      dotnet: { code: `Visionary.LoginAttempt(ctx, form.Email);` },
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
      "No code needed: this server calls the URL itself on a schedule, from outside your app, so it notices when the whole service is down, not just one route. Choose the method, the status codes that count as up (200-299 by default), optionally a word the body must contain, how often, the timeout, and a response time above which the check counts as degraded. Headers can carry a token; values that look secret are never shown again. Failing checks open incidents and alert: by the rule for every check, created with the first one, or by the check's own alert settings in its form. Tags (smoke, checkout, deploy) group checks, for the list's Tag filter and for running a group from CI.",
    any: { note: "Checks can't reach private or loopback addresses (localhost, 10.x, 192.168.x) unless an admin turns on Settings → Server → Other → Uptime checks may reach private addresses." },
  },
  browsercheck: {
    id: "browsercheck",
    title: "Script a browser check",
    intro:
      "A browser check opens your pages in a headless Chrome on this server and plays steps in order: go to a URL, click a button or link (by CSS selector or its visible text), type into a field, press a key, choose an option, wait for an element, text or the next page, check that text is or isn't shown, check the URL or how many elements match, pause, or run a JavaScript check. Each run starts in a fresh private window. Type passwords as {{secret.NAME}}: secrets are kept on the server and masked in results. A run records each step's time, the page's Web Vitals (LCP, CLS, TTFB), console errors and failed requests, and a screenshot when a step fails. It shares the schedule, incidents, alerts and status page of other checks.",
    any: {
      note: "The server needs Chrome, Chromium or Edge. It looks in Settings → Server → Other → Browser for browser checks, then VISIONARY_CHROME, then the usual install places; until it finds one, browser checks are paused. The default Docker image has none; build the variant that does:",
      code: `docker build --target browser -t visionary:browser .`,
      label: "Docker",
    },
  },
  statuspage: {
    id: "statuspage",
    title: "Publish a status page",
    intro:
      "A public page at /status (and /status.json) shows the checks you mark Show on the public status page, with their uptime and incidents; their URLs only when you tick that too. Declared incidents marked for it show there too, with the updates you publish.",
    any: { note: "An admin turns it on and sets its title in Settings → Status page. Then open an uptime check and tick Show on the public status page." },
  },
  agent: {
    id: "agent",
    title: "Install the host agent",
    intro:
      "The SDKs see the process they run in. The agent watches a whole Linux machine, app or not: CPU, memory, disks, processes, systemd services, Docker or Podman containers, SSH logins and open ports, and for Security new SSH keys, changes to system files (passwd, sudoers, cron, systemd units), suspicious processes and risky containers. It runs as a systemd service and posts here over HTTPS with a Server ingest key from Settings → API keys; it never needs database credentials. The machine shows up in System within a minute, and its containers in Containers.",
    any: { code: `curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \\
  | sudo VISIONARY_URL=${origin} VISIONARY_KEY=<server key> sh` },
  },
  clientkey: {
    id: "clientkey",
    title: "1. Create a client key",
    intro:
      "Web and mobile apps don't write to the database; they post to this server with a public client key. Client keys can only send events, so they are safe to ship in an app.",
    any: { note: "Create one with the Client ingest scope in Settings → API keys (or list your own under Settings → Server → Ingest → Client keys). For a website, also add its origin to Settings → Server → Ingest → Allowed web origins (wildcards like `https://*.example.com` work; empty allows any)." },
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
  replay: {
    id: "replay",
    title: "Session replay (browser, optional)",
    intro:
      "Records what users see and do on the page so a session can be watched next to its timeline. Turn on Settings → Server → Ingest → Accept session replays, then add `replay` to `init`. `sampleRate` records that share of sessions; `onErrorSampleRate` keeps the last minute of the others in memory and sends it only when an error happens. Input values are recorded as asterisks (`maskInputs`, on by default); passwords, emails, phone numbers, card and hidden fields are never recorded; `maskAllText` masks all text, `data-visionary-mask` one element's text, and `data-visionary-block` or `blockSelector` records an element as an empty box. Other sites' frames are never recorded. The recorder (about 4 KB gzipped) loads only in sessions it records. A recorded minute is typically 20 to 200 KB, kept 7 days by default (Settings → Data) and capped by Session replay storage.",
    any: {
      label: "JavaScript",
      code: `Visionary.init({
  endpoint: "${origin}",
  key: "pk_…",
  replay: { sampleRate: 0.05, onErrorSampleRate: 1 },
});

<div data-visionary-block>…</div>  <!-- never recorded -->
<p data-visionary-mask>…</p>       <!-- text recorded as *** -->`,
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
      "A service that already has an OpenTelemetry SDK can report here without a Visionary SDK. Point its OTLP exporter at this server with a Server ingest key from Settings → API keys. Traces become requests and calls, exceptions become errors, metrics fill System. OTLP/HTTP works on this address; for OTLP/gRPC, turn on Accept OpenTelemetry over gRPC in Settings → Server → Ingest and restart, then send to port 4317 (plaintext, so put a TLS proxy in front for traffic that leaves your network). Whether spans and errors are kept, and how many, is set in Settings → Server → Ingest.",
    any: {
      label: "Service environment",
      code: `OTEL_EXPORTER_OTLP_ENDPOINT=${origin}
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer <server key>"
OTEL_SERVICE_NAME=my-api

# Or OTLP/gRPC, once it is on in Settings → Server → Ingest
OTEL_EXPORTER_OTLP_ENDPOINT=http://${hostname}:4317
OTEL_EXPORTER_OTLP_PROTOCOL=grpc`,
    },
  },
  tracing: {
    id: "tracing",
    title: "Follow requests across services",
    intro:
      "A request that calls another service passes its trace on in the W3C `traceparent` header. The service it reaches records the same trace ID, so both requests, their calls and their log lines show up as one trace, and the call becomes an arrow on the service map. Give each service its own `VISIONARY_SERVICE` name. OpenTelemetry services take part as they are: they read and send the same header.",
    langs: {
      go: {
        note: "Automatic once the middleware is in: it continues an incoming `traceparent` or starts a trace, and `visionary.Transport` (and `http.DefaultClient`) send it on. Requests need the handler's context. Wrap your own work with `StartSpanContext` so the calls inside it nest under it; `visionary.Go` keeps the trace for background work.",
        code: `client := &http.Client{Transport: visionary.Transport(nil)}
req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://stock/items/42", nil)
resp, err := client.Do(req) // the stock service continues this trace

ctx, end := visionary.StartSpanContext(r.Context(), visionary.KindCustom, "build report")
err = buildReport(ctx) // its calls nest under "build report"
end(err)`,
      },
      node: {
        note: "Automatic once the integration is in: every request continues an incoming `traceparent` or starts a trace, and `fetch`, `undici`, `http` and `https` send it on (even with spans off). Calls made inside `visionary.span` or `visionary.background` nest under them.",
        code: `await fetch("http://stock/items/42"); // the stock service continues this trace

await visionary.span("custom", "build report", () => buildReport()); // its calls nest under it`,
      },
      python: {
        note: "Automatic for incoming requests once the middleware is in. Outbound calls send it once `instrument_http()` hooks requests, httpx and urllib3. Calls made inside `visionary.span` or `visionary.background` nest under them.",
        code: `visionary.instrument_http()  # once at startup
requests.get("http://stock/items/42")  # the stock service continues this trace

with visionary.span("custom", "build report"):
    build_report()  # its calls nest under "build report"`,
      },
      ruby: {
        note: "Automatic once the middleware is in: it continues an incoming `traceparent` or starts a trace, and `Net::HTTP` (Faraday's default adapter, HTTParty) sends it on. Wrap your own work in `Visionary.span` so the calls inside it nest under it; `Visionary.background` keeps the trace for background work.",
        code: `Net::HTTP.get(URI("http://stock/items/42")) # the stock service continues this trace

Visionary.span("build report") do
  build_report # its calls nest under "build report"
end

Visionary.background("send receipt") { Mailer.receipt(order).deliver_now }`,
      },
      java: {
        note: "Automatic with the starter: every request continues an incoming `traceparent` or starts a trace, and `RestClient`, `RestTemplate` and `WebClient` built from Boot's builders send it on. A `Visionary.span(...)` is the parent of the calls made inside it until it is closed; `Visionary.background`, `Visionary.wrap` and `@Async` keep the trace.",
        code: `@Bean
RestClient stock(RestClient.Builder builder) { // sends traceparent
    return builder.baseUrl("http://stock").build();
}

try (VisionarySpan span = Visionary.span("custom", "build report")) {
    report = buildReport(); // its calls nest under "build report"
}`,
      },
      dotnet: {
        note: "Automatic once `app.UseVisionary()` is in: a request continues an incoming `traceparent` (or ASP.NET Core's activity that read it) or starts a trace, and every `HttpClient`, `IHttpClientFactory` clients included, sends it on. A `Visionary.StartSpan` is the parent of the calls made while it is open; `Visionary.Background` keeps the trace for background work.",
        code: `app.MapGet("/orders/{id}", async (int id, IHttpClientFactory clients) =>
{
    using var span = Visionary.StartSpan("custom", "price order");
    // the stock service continues this trace, nested under "price order"
    return await clients.CreateClient().GetStringAsync($"http://stock/items/{id}");
});`,
      },
    },
  },
  metrics: {
    id: "metrics",
    title: "Record custom metrics",
    intro:
      "Counters, gauges and histograms your code defines land on the Metrics page. The SDK adds them up in memory and writes each series every 15 seconds, so recording one never waits for the database. Labels are key/value pairs; each distinct set is one series, and a metric keeps at most 1000 of them, so never put user or request ids in a label.",
    langs: {
      go: {
        note: "A counter counts things that happen, a gauge holds a level, a histogram records values such as durations. Keep a handle for hot paths. Units and histogram buckets are optional.",
        code: `visionary.Counter("orders_placed", "region", region).Add(1)
visionary.Gauge("queue_depth", "queue", "emails").Set(float64(n))

took := visionary.Histogram("checkout_ms", "method", method)
took.Observe(float64(time.Since(start).Milliseconds()))

visionary.DescribeMetric("checkout_ms", visionary.MetricOptions{Unit: "ms", Buckets: []float64{50, 100, 250, 500, 1000}})`,
      },
      node: {
        note: "Labels are an object. Calls work before `init()`.",
        code: `visionary.counter("orders_placed", { region }).add(1);
visionary.gauge("queue_depth", { queue: "emails" }).set(queue.length);

const took = visionary.histogram("checkout_ms", { method });
took.observe(Date.now() - start);

visionary.describeMetric("checkout_ms", { unit: "ms", buckets: [50, 100, 250, 500, 1000] });`,
      },
      python: {
        note: "Labels are keyword arguments (or a dict). Calls work before `init()`.",
        code: `visionary.counter("orders_placed", region=region).add(1)
visionary.gauge("queue_depth", queue="emails").set(len(queue))

took = visionary.histogram("checkout_ms", method=method)
took.observe((time.monotonic() - start) * 1000)

visionary.describe_metric("checkout_ms", unit="ms", buckets=[50, 100, 250, 500, 1000])`,
      },
      ruby: {
        note: "A counter counts things that happen, a gauge holds a level, a histogram records values such as durations. Labels are keywords or a Hash. Keep a handle for hot paths. Units and histogram buckets are optional.",
        code: `Visionary.counter("orders_placed", region: region).add(1)
Visionary.gauge("queue_depth", queue: "emails").set(jobs.size)

took = Visionary.histogram("checkout_ms", method: method)
took.observe(elapsed_ms)

Visionary.describe_metric("checkout_ms", unit: "ms", buckets: [50, 100, 250, 500, 1000])`,
      },
      java: {
        note: "A counter counts things that happen, a gauge holds a level, a histogram records values such as durations. Labels are key/value pairs (or a `Map`). Keep a handle for hot paths. Units and histogram buckets are optional.",
        code: `Visionary.counter("orders_placed", "region", region).add(1);
Visionary.gauge("queue_depth", "queue", "emails").set(depth);

VisionaryHistogram took = Visionary.histogram("checkout_ms", "method", method);
took.observe(elapsedMs);

Visionary.describeMetric("checkout_ms", "ms", 50, 100, 250, 500, 1000);`,
      },
      dotnet: {
        note: "A counter counts things that happen, a gauge holds a level, a histogram records values such as durations. Labels are tuples or a dictionary; keep a handle for hot paths. Units and histogram buckets are optional. Meters named in `VISIONARY_METRICS_METERS` (System.Diagnostics.Metrics) are recorded too.",
        code: `Visionary.Counter("orders_placed", ("region", region)).Add(1);
Visionary.Gauge("queue_depth", ("queue", "emails")).Set(depth);

var took = Visionary.Histogram("checkout_ms", ("method", method));
took.Observe(elapsed.TotalMilliseconds);

Visionary.DescribeMetric("checkout_ms", unit: "ms", buckets: new double[] { 50, 100, 250, 500, 1000 });`,
      },
    },
    any: {
      label: "Prometheus (prometheus.yml)",
      code: `remote_write:
  - url: ${origin}/api/v1/write
    authorization:
      credentials: <server key>

# OpenTelemetry: metrics that are not host metrics land on Metrics too
OTEL_EXPORTER_OTLP_ENDPOINT=${origin}`,
    },
  },
  ci: {
    id: "ci",
    title: "Connect GitHub Actions",
    intro:
      "GitHub sends every workflow run and job to this server as it moves from queued to finished. In the repository (or organization) open Settings → Webhooks → Add webhook: the payload URL below, content type `application/json`, a long random secret, and under “Let me select individual events” tick “Workflow runs” and “Workflow jobs”. Save the same secret in Settings → Server → CI. `?service=` names the service the runs belong to; without it, the repository's name is used.",
    any: {
      label: "Payload URL",
      code: `${origin}/api/ingest/ci/github?service=my-api`,
    },
  },
  junit: {
    id: "junit",
    title: "Upload test results",
    intro:
      "Add a step after your tests that posts their JUnit XML with a server key from Settings → API keys (saved as the `VISIONARY_KEY` secret). pytest (`--junitxml=junit.xml`), Jest with jest-junit, Maven Surefire and Gradle, go-junit-report and most other runners write it. The run id ties the results to the run; several `-F file=@…` send several reports, and gzip'd files are fine. Surefire's reruns that passed count as flaky.",
    any: {
      label: "GitHub Actions step",
      code: junitStep(origin),
    },
  },
  cirun: {
    id: "cirun",
    title: "Or post runs from any CI",
    intro:
      "GitLab, CircleCI, Buildkite, Jenkins or a script post each run as JSON with a server key, as often as it changes; the same `provider`, `repo` and `run_id` update one run. `status` takes your CI's words (`running`, `success`, `failed`, `canceled`...), times are RFC 3339, and `jobs` is optional. Send a list or `{\"runs\": [...]}` for several. Tests go to the JUnit endpoint with the same `provider`, `repo` and `run_id`.",
    any: {
      label: "curl",
      code: `curl -X POST ${origin}/api/ingest/ci/runs \\
  -H "Authorization: Bearer <server key>" \\
  -d '{"provider": "gitlab", "repo": "shop/api", "workflow": "pipeline", "run_id": "9001",
       "number": 412, "branch": "main", "default_branch": "main", "commit": "<sha>",
       "actor": "ann", "status": "success", "service": "api", "url": "https://gitlab.example.com/...",
       "queued_at": "2026-10-30T09:00:00Z", "started_at": "2026-10-30T09:00:40Z", "finished_at": "2026-10-30T09:12:00Z",
       "jobs": [{"id": 1, "name": "test", "status": "success",
                 "started_at": "2026-10-30T09:01:00Z", "finished_at": "2026-10-30T09:11:00Z"}]}'`,
    },
  },
  grafana: {
    id: "grafana",
    title: "Use Visionary in Grafana",
    intro:
      "Visionary answers the Prometheus HTTP API, so Grafana (or any Prometheus client) can chart your custom metrics and Visionary's own visionary_* series with PromQL. Create a key with the Read scope in Settings → API keys first.",
    any: {
      label: "Grafana → Connections → Data sources → Add → Prometheus",
      code: `Prometheus server URL:  ${origin}/api/prom
Authentication:         Basic authentication
  User:                 grafana (any name)
  Password:             <read key, vis_…>
HTTP method:            POST (GET works too)

# or, as provisioning (grafana/provisioning/datasources/visionary.yml)
apiVersion: 1
datasources:
  - name: Visionary
    type: prometheus
    url: ${origin}/api/prom
    basicAuth: true
    basicAuthUser: grafana
    secureJsonData:
      basicAuthPassword: <read key>

# Any other client: Authorization: Bearer <read key>
curl -H "Authorization: Bearer <read key>" \\
  "${origin}/api/prom/api/v1/query?query=sum(rate(visionary_http_requests_total[5m]))"`,
    },
  },
  terraform: {
    id: "terraform",
    title: "Configuration as code",
    intro:
      "Keep alert rules, channels, silences, uptime checks (HTTP and browser), dashboards and API keys in Terraform with the styropyr0/visionary provider. Create a key with the Editor scope in Settings → API keys (Admin to manage API keys too). Bring in what already exists with terraform import and its id, e.g. `terraform import visionary_alert_rule.slow 12`. To copy everything between installs without Terraform, use Settings → Configuration.",
    any: {
      label: "main.tf",
      code: `terraform {
  required_providers {
    visionary = { source = "styropyr0/visionary" }
  }
}

# or set VISIONARY_URL and VISIONARY_API_KEY
provider "visionary" {
  url     = "${origin}"
  api_key = var.visionary_key
}

resource "visionary_alert_channel" "ops" {
  name = "Ops email"
  kind = "email"
  to   = ["ops@example.com"]
}

resource "visionary_alert_rule" "slow" {
  name      = "Slow API"
  severity  = "warning"
  metric    = "latency_p95"
  op        = ">"
  threshold = 800
  window    = "5m"
  channels  = [visionary_alert_channel.ops.id]
}

resource "visionary_uptime_check" "home" {
  name       = "Home page"
  url        = "https://example.com"
  interval_s = 60
}`,
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
  sso: {
    id: "sso",
    title: "Sign in with your identity provider",
    intro:
      "People sign in with Google, Microsoft Entra ID, Okta, Auth0, Keycloak or any OpenID Connect or SAML 2.0 provider, and their groups decide their role. It works next to the other ways in, so a password account can stay as a way back in.",
    any: {
      note: "An admin opens Settings → Sign-in → Single sign-on, sets the public URL, adds a provider and saves it switched off. Register the redirect URL (OpenID Connect) or upload the metadata (SAML) at the provider, then Test sign-in shows what the provider sends and the role it gives. Switch it on and save. For SCIM, create a key of scope SCIM under API keys and give the provider the SCIM base URL. Locked out? Set VISIONARY_ENABLE_AUTH=false on the server and restart.",
    },
  },
  replicas: {
    id: "replicas",
    title: "Run several replicas",
    intro:
      "Two or more servers can share one PostgreSQL, MySQL or D1 database behind a load balancer (SQLite only on one machine). Work that must happen once, such as alert checks and deliveries, digests, uptime probes, rollups and retention, runs on one replica at a time under a lease, and another takes over within 40 seconds if it stops. Settings → Server → Replicas shows which replica holds what. Use /api/readyz as the readiness check and /api/healthz for liveness. Rate limits, ingest buffers and the gRPC and syslog listeners are per replica, and Restart restarts only the replica that answered.",
    any: {
      label: "Kubernetes",
      note: "With Docker Compose, run two containers of the image on the same database behind a proxy such as Caddy or nginx that health-checks /api/readyz; deploy/replicas in the repository has both examples.",
      code: `apiVersion: apps/v1
kind: Deployment
metadata: { name: visionary }
spec:
  replicas: 2
  selector: { matchLabels: { app: visionary } }
  template:
    metadata: { labels: { app: visionary } }
    spec:
      containers:
        - name: visionary
          image: ghcr.io/styropyr0/visionary:latest
          env:
            - name: VISIONARY_DATABASE_URL
              valueFrom: { secretKeyRef: { name: visionary-db, key: url } }
          ports: [{ name: http, containerPort: 8090 }]
          readinessProbe: { httpGet: { path: /api/readyz, port: http } }
          livenessProbe: { httpGet: { path: /api/healthz, port: http } }
---
apiVersion: v1
kind: Service
metadata: { name: visionary }
spec:
  selector: { app: visionary }
  ports: [{ port: 80, targetPort: http }]`,
    },
  },
  workspaces: {
    id: "workspaces",
    title: "Give teams their own services",
    intro:
      "A workspace is a set of services (exact names, or patterns such as `payments-*`) and its members: people, identity provider or SCIM groups, and API keys, each with a role there. Members see only those services everywhere: every page, Explore and their own SQL, dashboards, alerts and the Prometheus API. Admins always see everything, and with no workspaces nothing changes.",
    any: {
      note: "An admin opens Settings → Workspaces, clicks New workspace, lists the services and watches the preview of what they match, then adds members. Choose what people outside every workspace see: everything, or nothing. Under What belongs to a workspace, give alert rules, uptime checks, dashboards and saved views a workspace. Rows without a service name are seen only by people who see everything.",
    },
  },
  logs: {
    id: "logs",
    title: "Send your application's logs",
    intro:
      "Route your logger to the SDK and every line lands in Logs, linked to the request it was logged in. What the app prints doesn't change. Info and above is kept; debug lines are kept for requests that fail or are slow. Settings → Logs changes this for all services, without a redeploy.",
    langs: {
      go: {
        note: "Wrap your slog handler, and log with the request's context so lines link to it. CaptureStdLog copies the log package (and slog's default handler).",
        code: `slog.SetDefault(slog.New(visionary.LogHandler(slog.NewJSONHandler(os.Stdout, nil))))
defer agent.CaptureStdLog()()

slog.InfoContext(r.Context(), "order placed", "order_id", id)`,
      },
      node: {
        note: "console, pino or winston:",
        code: `visionary.captureConsole();

const logger = pino({}, visionary.pinoStream());
const logger = winston.createLogger({ transports: [new winston.transports.Console(), visionary.winstonTransport()] });`,
      },
      python: {
        note: "Adds a handler to the root logger, after logging is configured (basicConfig, dictConfig, Django LOGGING). Existing handlers print what they printed before.",
        code: `visionary.capture_logging()

logging.getLogger(__name__).info("order placed", extra={"order_id": order.id})`,
      },
      ruby: {
        note: "In Rails, from an initializer; elsewhere, wrap the logger you use.",
        code: `Visionary.capture_rails_logger

logger = Visionary.logger(Logger.new($stdout))`,
      },
      java: {
        note: "Logback: turn it on for the root logger, or declare the appender in logback-spring.xml. Debug lines are captured for loggers set to DEBUG.",
        code: `VISIONARY_LOG_CAPTURE=true

<appender name="VISIONARY" class="io.github.styropyr0.visionary.VisionaryLogbackAppender"/>
<root level="INFO"><appender-ref ref="VISIONARY"/></root>`,
      },
      dotnet: { code: `builder.Logging.AddVisionary();` },
    },
  },
  logship: {
    id: "logship",
    title: "Send logs from shippers, OpenTelemetry and syslog",
    intro:
      "Anything that ships logs can send them here with a server ingest key (Settings → API keys): the key goes in Authorization (Bearer, ApiKey, or Basic with the key as the password), x-visionary-key, or ?key=.",
    any: {
      label: "Endpoints",
      code: `# OpenTelemetry: every log record
OTEL_EXPORTER_OTLP_ENDPOINT=${origin}

# Elasticsearch bulk: Fluent Bit (es), Vector (elasticsearch), Filebeat, Logstash
${origin}/api/ingest/logs          # the Elasticsearch URL; bulk goes to /_bulk

# Loki push (JSON): Promtail, Grafana Alloy, Fluent Bit (loki), Vector (loki)
${origin}/loki/api/v1/push

# Anything: a JSON array, NDJSON or plain text, one line each
curl -H "Authorization: Bearer <server key>" --data-binary @app.log \\
  "${origin}/api/ingest/logs?service=billing"

# Syslog (UDP and TCP): set an address in Settings → Logs, e.g. :5514`,
    },
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
      "Click a route or a user to open their requests in Requests.",
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
      "A plan's warnings point at full scans of large tables and sorts an index would avoid, with the CREATE INDEX to copy when the filter is simple. Explain in plain words asks the analyst.",
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
  metrics: {
    title: "Metrics",
    tip: "Counters, gauges and histograms your services record, from the SDKs, OpenTelemetry and Prometheus, charted by label.",
    what: "Every custom metric your services send: from the SDK's Counter, Gauge and Histogram, OpenTelemetry metrics that are not host metrics, and Prometheus remote_write. Pick one to chart it over the range: sum, average, max, min or rate per second, and p50, p95 or p99 for histograms; group by a label, the service or the instance, and filter by label values. PromQL in the top bar writes any metric math instead, over custom metrics and Visionary's own data.",
    read: [
      "PromQL: selectors with =, !=, =~ and !~, offset, rate, irate, increase, delta, avg/min/max/sum/count/last_over_time, sum, avg, min, max, count, topk, bottomk and quantile by or without labels, arithmetic and comparisons between vectors with on, ignoring and group_left, and, or, unless, abs, ceil, floor, round, clamp_min, clamp_max and histogram_quantile. No subqueries or @.",
      "Custom metrics keep their names; their labels plus service and instance are labels. Counters are stored as increases, so rate and increase are exact, and a bare counter reads as its running total over the range. A histogram h is also h_bucket{le}, h_sum and h_count.",
      "Built-in series: visionary_http_requests_total{service, route, method, status}, visionary_http_request_duration_ms_bucket{service, route, method, le} with _sum and _count, visionary_spans_total{service, kind, status}, visionary_span_duration_ms_bucket{service, kind, le}, visionary_errors_total{service, type, route}, and visionary_system_<column>{service, instance} gauges such as host_cpu_percent, host_mem_percent, proc_rss_mb, goroutines and db_acquired_conns.",
      "Readings are bucketed so a query reads at most about 11,000 points per series; a chart shows the 50 largest series.",
      "A counter's row is its increase over the interval, so Sum is how many happened in each bucket and Rate per second divides by the bucket's length.",
      "A gauge is a level: Average and Max read it per bucket; Sum adds the series together, e.g. queue depth over all queues.",
      "Percentiles are estimated from the histogram's buckets, so they are as precise as the buckets are narrow.",
      "Grouping shows the 20 largest groups. A metric keeps at most 1000 label sets; the list says when new ones were dropped.",
      "Add to dashboard keeps the aggregation, grouping and filters; a dashboard's service variable narrows it. Alerts → New rule → Custom metric alerts on it; PromQL expression alerts on a query, per series.",
    ],
    setup: ["metrics", "grafana", "otlp"],
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
    what: "Data from the browser, Android and iOS SDKs: Core Web Vitals per page, screen load times per device, JavaScript and app errors, and sessions, with a replay of what the user saw when session replay is on.",
    read: [
      "Web Vitals are rated good, needs improvement or poor by Google's thresholds, at the 75th percentile.",
      "Slow screens on low-end devices show up in the device breakdown.",
      "Sessions with a play icon beside their id have a replay: open one and choose Replay. The marks on the bar are its page loads, API calls, errors and server logs; click one to jump there.",
    ],
    setup: ["clientkey", "browser", "replay", "ios", "android"],
  },
  uptime: {
    title: "Uptime",
    tip: "Checks that call your endpoints from outside on a schedule, with response times, incidents and an optional public status page.",
    what: "HTTP checks and scripted browser checks this server runs on a schedule, independent of any SDK. Each check shows its availability, response time and incidents; the list says when a check would go down without anyone hearing.",
    read: [
      "A check fails after its retries, so a single slow answer does not open an incident.",
      "A browser check's runs show each step, Web Vitals, console and network errors, and a screenshot of the page when a step failed. It is paused while the server has no browser.",
      "The public status page shows the checks you mark as public, once it is turned on in Settings → Status page.",
    ],
    setup: ["checks", "browsercheck", "statuspage"],
  },
  ci: {
    title: "CI",
    tip: "Your CI pipelines and tests: success rate, duration and queue time, failing and slow jobs and tests, flaky tests, and the deploy each commit became.",
    what: "Every workflow run GitHub Actions or your CI sent, with its jobs and the test results uploaded for it. Pipelines shows the success rate per workflow over time, p50 and p95 duration, how long runs waited for a runner, the slowest and most failing jobs, and the runs themselves. Tests shows failures by test, the slowest tests and those getting slower; Flaky lists tests that passed and failed on the same code. A run opens with its jobs on a timeline, its failed tests and, when a release's version names its commit, the deploy with the service's traffic before and after.",
    read: [
      "The success rate counts runs that passed or failed; cancelled and skipped runs say nothing about the code and are left out.",
      "Queue time is the wait from a run being queued until its first job started on a runner; a growing queue means too few runners.",
      "A test is flaky when it passed and failed on one commit (a retry that passed, or a rerun Surefire reported), or failed on its own between passes on the default branch at least twice. Its score is the share of the commits it ran on where it flaked.",
      "A run links to a deploy when a release's version is, or contains, a commit SHA prefix of 7 or more characters: `a1b2c3d`, `1.4.0+a1b2c3d`, or git describe's `v1.4.0-3-ga1b2c3d`. Compare versions on Performance links back to the run that built each version.",
      "Alerts → New rule → CI watches the pipeline failure rate and p95 duration per workflow, job queue time and new flaky tests. Settings → Data keeps CI data 90 days by default.",
    ],
    setup: ["ci", "junit", "cirun"],
  },
  security: {
    title: "Security",
    tip: "Attacks and account takeovers found in your traffic, events from your machines, and changes to Visionary itself, with what to do about each.",
    what: "Findings from the API detectors: brute force, credential stuffing and targeted accounts on login routes (also slow and spread across many addresses), someone signed in as one user trying another's account, new places and devices, impossible travel, one session used from many places, sensitive changes right after a risky sign-in, scraping and ID guessing, injection attempts, per-route limits, sign-up and code floods, scanners, request floods and bursts of 403s. With the host agent: SSH brute force, logins, sudo, new SSH keys, changed system files, suspicious processes, risky containers, opened ports and pending updates. From Visionary's own audit log: failed sign-ins to this dashboard, secrets shown, role and sign-in changes. Rules adds your own detectors over requests, app logs, security events and the audit log (a count over a threshold, a value a user or host never had, one thing then another, or a jump over the usual), from a library tagged with MITRE ATT&CK; what they find shows under Signals. Each finding or signal can be acknowledged, resolved or marked a false positive, and acted on.",
    read: [
      "Each finding links to the requests behind it; click an account for its sign-in history, or About this IP for where an address is and what else it did.",
      "Don't flag this again stops one kind of finding for one IP or account; Not flagged lists those rules.",
      "A signal is one finding per rule and group (an IP, a user, a host) per window; its count grows while the activity goes on. Open one for its matched events, with links to their requests, logs and traces.",
      "Test against the last 24 hours shows what a rule would have raised before you save it. A rule's suppressions skip a field's value, such as your own monitor's IP, for a while or for good.",
      "New-value rules learn what each group had over their learning period the first time they run, then flag only what's new. Rules run every fifth of their window (1 to 10 minutes) and read only that window.",
      "Places, impossible travel and networks need a location database, and blocklists raise findings from listed addresses: both in Settings → Server → Threat intelligence.",
      "Settings → Server tunes every detector; host events still show when the API detectors are off.",
    ],
    setup: ["sdk", "user", "login", "agent"],
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
  applogs: {
    title: "Logs",
    tip: "Every line your services log: search, live tail, patterns, volume and alerts, each line linked to the request that wrote it.",
    what: "The lines your services log, from the SDKs' logging hooks, OpenTelemetry, shippers (Fluent Bit, Vector, Filebeat, Logstash, Promtail), syslog and the host agent. Search with a query language, watch new lines live, open a line for its fields, the lines around it and its request. Patterns groups lines by message; Volume shows where storage goes; Hidden failures finds errors behind successful responses; Releases shows what a deploy changed in the logs.",
    read: [
      "Lines below the level in Settings → Logs (info by default) are kept only for requests that failed or were slow, so every failure comes with its debug lines.",
      "Search: words match the message, field:value matches a column or a field, level:error+ means error or worse, -term leaves out, OR between alternatives, /regex/ for a pattern. The ? button lists the syntax.",
      "Click a field's value to add it to the search; click + or − on a line's field to keep or leave it out.",
      "Drag across the chart to zoom the time range; Zoom out goes back one step at a time, then doubles the range.",
      "Debug a service or a user for 15 minutes from a line, without a redeploy; alert when a line happens again; reduce a chatty line from Volume.",
      "Secrets in lines are scrubbed before they are stored; anything that slips through is redacted and reported in Security.",
      "Searching words in messages stays fast at millions of lines on PostgreSQL, which gets a text index for it. On MySQL, SQLite and D1 it reads every line in the time range, so keep the range short or log less there.",
    ],
    setup: ["logs", "logship"],
  },
  logs: {
    title: "Requests",
    tip: "Every request with its route, status, duration, user, headers and bodies, searchable and filterable.",
    what: "The raw log of requests your services served. Filter by route, status, user or text, and open one for its headers, redacted bodies, error and the timeline of its calls.",
    read: [
      "Bodies are stored up to a size cap and with secrets redacted.",
      "The breakdown in a request shows each database and outbound call, and Untracked for time no call covers.",
    ],
    setup: ["sdk", "user", "db", "http", "background"],
  },
  traces: {
    title: "Traces",
    tip: "Follow one request through every service it called, and see which services call which on a map.",
    what: "A trace is everything one piece of work did across your services: the request that started it, the requests it made to other services, and every database and outbound call along the way, on one timeline with the log lines and errors that carry its trace ID. Search finds traces by service, route, call, duration and errors; the service map draws the services and what they call, with each arrow's rate, failures and p95.",
    read: [
      "In a trace, each colour is a service and each row sits under the span it was made in. Outlined bars failed; requests are the paler bars that their calls sit on.",
      "Duration runs from the first request's start to the end of the last one. Work left running after a response can end later.",
      "A service shows up in a trace only when its caller sends `traceparent` and it records it. Arrows between services come from those links; calls to a host nothing traced answered go to that host.",
      "Open a request in a trace for its own breakdown, or a log line's trace from Logs.",
    ],
    setup: ["sdk", "tracing", "http", "otlp"],
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
  incidents: {
    title: "Incidents",
    tip: "Declared incidents with a severity, a commander and responders, a timeline, status updates to your channels and the status page, and a postmortem.",
    what: "Every incident someone declared, with MTTA, MTTM and MTTR and the count by severity over the range. An incident moves from investigating to identified, monitoring and resolved; its timeline collects status and role changes, notes, the updates sent, and on its own the linked alerts firing and resolving, linked checks failing and recovering, and deploys of the affected services.",
    read: [
      "Declare from scratch here, or with Declare incident on a firing alert rule, an uptime check, an error issue or a log line: the form fills in and the source is linked.",
      "Times are measured from when the impact started: acknowledged when someone takes command or moves the incident on, mitigated at monitoring, resolved at resolved.",
      "A status update goes to the alert channels you pick (retried like alerts) and, if you tick it, to the public status page, where only the title and published updates show.",
      "The postmortem drafts itself from the timeline and the affected services' traffic; with the AI analyst on, the model writes a first version. Export it as Markdown.",
      "Editors declare and update incidents; viewers read them; only admins delete one. Every change is in the audit log.",
    ],
    setup: ["statuspage", "analyst"],
  },
  settings: {
    title: "Settings",
    tip: "Your preferences, the team and its workspaces, API keys, thresholds, retention and deleting data, logs, CPU profiling, which pages show, the status page, configuration export and import, and the audit log.",
    what: "Preferences are yours alone: start page, the time range and auto-refresh a visit starts with, theme, time zone and clock. The rest is the team's: who can sign in and with which role (including single sign-on and SCIM provisioning), workspaces that limit people to some services, the server's own settings (the AI analyst, sessions, email, ingest keys and limits, security detectors), API keys, the Apdex and SLO thresholds (with routes that have their own), how long each kind of data is kept and deleting it, what logs are kept, dropped and parsed (and the archive), CPU profiling, which pages the sidebar shows, the public status page, the team's configuration as one file, and the audit log.",
    read: [
      "Changes reach every replica sharing the database within seconds; no restart or environment variable is needed. Settings → Server → Replicas lists the replicas and the background jobs each runs.",
      "Single sign-on (OpenID Connect or SAML) works next to the other ways in; its people get accounts without a password, checked again every minute, and SCIM can add, change and switch them off. Sign-ins and SCIM changes go to the audit log.",
      "Admins change the team's settings and see the audit log; editors change alerts and dashboards and can ask for a profile now; viewers only read. Everyone sets their own preferences.",
      "Workspaces are enforced by the server on every read, so a member can't reach another team's services by editing a link or writing SQL. Someone in several workspaces switches under the logo; data without a service name is seen only by people who see everything.",
      "Hidden pages stay reachable by their link; Settings always shows.",
      "API keys: Read calls the HTTP API as a viewer and never changes anything; Editor and Admin call it with those roles' rights, for Terraform and scripts; Server and Client ingest send data; SCIM provisions people.",
      "Configuration exports alert rules, channels, silences, uptime checks, saved panels, dashboards, saved log searches and hidden pages as one YAML or JSON file, matched by name. Channel URLs and keys, secret headers and browser secrets stay behind. Importing shows a preview of what is created, updated or unchanged before anything is written, and never deletes.",
      "Deleting data needs a preview first and typing delete to confirm; it is recorded in the audit log and never touches settings, dashboards, alert rules or uptime checks.",
      "Time partitioning (Data, PostgreSQL only) keeps one partition per day for the busiest tables, so retention drops whole days and charts read only theirs. It pays off from a few million rows a day; converting a table with rows copies it first, so do it at a quiet hour.",
      "To connect your apps, follow the steps below: a server SDK in each service, the browser or mobile SDK with a client key, OpenTelemetry, or the host agent.",
    ],
    setup: ["sdk", "connect", "verify", "clientkey", "browser", "ios", "android", "otlp", "agent", "explain", "analyst", "email", "sso", "replicas", "workspaces", "terraform"],
  },
  guide: {
    title: "How to use",
    tip: "A tour of every page: what it is for and how to use it, with screenshots.",
    what: "One section per page, in the order of the sidebar, with the steps to get things done there. Search to find a feature; each section opens its page.",
    read: [],
    setup: ["sdk", "connect", "verify"],
  },

};
