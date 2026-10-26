# Visionary for Spring Boot

Add one dependency to a Spring Boot application and Visionary shows every
request, its database statements and outbound calls, exceptions, host and JVM
metrics, and CPU profiles. No code is needed: the starter configures itself,
writes rows to your database on background threads, and the Visionary server
reads the same database for its dashboards.

Requirements: Java 17+, Spring Boot 3.x, a servlet (Spring MVC) application on
Tomcat, Jetty or Undertow. WebFlux servers are not supported; WebFlux's
`WebClient` is. The contract every SDK follows is [`sdks/SPEC.md`](../SPEC.md).

## Quick start

Four steps, about 10 minutes. You need the Visionary server running against a
database (see the [main README](../../Readme.md#get-started)).

**1. Add the starter and your database's JDBC driver.** The current release
is `0.7.1` (see [releases](https://github.com/styropyr0/Visionary/releases);
the version is the tag without the `v`).

Maven:

```xml
<dependency>
    <groupId>io.github.styropyr0</groupId>
    <artifactId>visionary-spring-boot-starter</artifactId>
    <version>0.7.1</version>
</dependency>
<dependency>
    <groupId>org.postgresql</groupId>   <!-- the driver for your database, see Databases -->
    <artifactId>postgresql</artifactId>
</dependency>
```

Gradle:

```kotlin
implementation("io.github.styropyr0:visionary-spring-boot-starter:0.7.1")
runtimeOnly("org.postgresql:postgresql")
```

**2. Point it at the database the server uses.** As environment variables:

```sh
VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/visionary
VISIONARY_SERVICE=orders-api   # any name; tells services apart on the dashboard
```

The same names also work as `-D` system properties or in
`application.properties` (`VISIONARY_SERVICE=orders-api`; use the names
exactly as written, not `visionary.service`).

**3. Start the application and check the log.** You see one line like:

```
visionary: writing to jdbc:postgresql://host:5432/visionary; service orders-api, version (none); requests on (prefix '', bodies true); spans on (threshold 100.0ms, max 100); errors on; system metrics every 15000ms
```

The starter creates its tables if they don't exist.

**4. Send a request.** It shows up on the API overview within a few seconds,
with route, status, time, user and any exception. The System page fills
within 15 seconds. If nothing shows up, see
[Troubleshooting](#troubleshooting).

That is the whole integration. The starter stops with the application and
flushes what is queued (up to 10 seconds). With no database configured it logs
one warning and does nothing else; a database that goes down later never
affects requests.

## Install from source

The starter is on Maven Central only. JitPack is not an option: the
repository's `jitpack.yml` builds only the Android SDK. To try an unreleased
commit, install it into your local Maven repository:

```sh
cd sdks/java
./mvnw install        # Windows: mvnw.cmd install
```

and depend on `io.github.styropyr0:visionary-spring-boot-starter:0.1.0-SNAPSHOT`
(the version in `pom.xml`), with `mavenLocal()` in your Gradle
`repositories`.

## Databases

The application and the Visionary server must point at the same database. The
first match wins:

| Setting | Backend | Driver dependency |
|---|---|---|
| `VISIONARY_DATABASE_URL=d1://` | Cloudflare D1, using the `D1_*` variables | none |
| `VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/db` (or `postgresql://`) | PostgreSQL | `org.postgresql:postgresql` |
| `VISIONARY_DATABASE_URL=mysql://user:pass@host:3306/db` (or `mariadb://`) | MySQL 8+ / MariaDB | `com.mysql:mysql-connector-j` or `org.mariadb.jdbc:mariadb-java-client` |
| `VISIONARY_DATABASE_URL=sqlite:///absolute/path.db` (or `sqlite://relative.db`) | SQLite | `org.xerial:sqlite-jdbc` |
| `VISIONARY_DATABASE_URL=jdbc:postgresql:…`, `jdbc:mysql:…`, `jdbc:mariadb:…`, `jdbc:sqlite:…` | the JDBC URL as given | as above |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` set, no URL | Cloudflare D1 | none |
| `VISIONARY_USE_APP_DATASOURCE=true`, none of the above | the application's own `DataSource` bean | the app's |

- **Query parameters** on `postgres://` and `mysql://` URLs are passed to the
  driver (`?sslmode=require`, `?currentSchema=metrics`). User and password
  come from the URL and may be percent-encoded.
- **SQLite on Windows:** `sqlite:///C:/data/visionary.db` is an absolute
  path.
- **SQLite shared with the server** on one host: both processes write the
  file, so let each wait for the other. The server's URL takes
  `?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)`; the starter uses
  the xerial driver, whose form is
  `sqlite:///C:/data/visionary.db?busy_timeout=5000&journal_mode=WAL`.
- **D1** is reached over its REST API with the JDK `HttpClient`. The API
  token needs D1 edit access. `D1_API_BASE` overrides
  `https://api.cloudflare.com/client/v4`.
- **Own URL:** the starter keeps one connection for its background threads
  and reconnects after errors. **App DataSource:** it borrows connections from
  the application's pool and detects the dialect from the database. Either
  way each batch is one transaction.

Old rows are deleted by the Visionary server, following Settings → Data (Retention).

## Recipes

Most of the starter works with no code. These are the places where a line or
two adds more.

### Requests (automatic)

A filter registered first in the chain logs every request: method, path, the
matched Spring MVC route (`/users/{id}`), query, status, duration, client IP,
user, headers, redacted bodies. Async requests (`Callable`, `DeferredResult`,
`SseEmitter`, `StreamingResponseBody`) are logged when they complete and still
stream. A handler exception the container turns into a 500 is logged as 500.

Log only part of the app:

```sh
VISIONARY_LOG_PATH_PREFIX=/api
```

### Signed-in user

With Spring Security on the classpath, the authenticated principal's name
(`Authentication.getName()`, anonymous users excluded) is stored as `user_id`
by itself. To set the user yourself, typically in auth code:

```java
import io.github.styropyr0.visionary.Visionary;

Visionary.setUser(request, user.getId(), user.getUsername(), user.getRole());
// or just the id:
Visionary.setUser(request, user.getId());
```

To let the security detectors spot one IP trying many accounts, many IPs
trying one account, or a signed-in user trying someone else's, record the
account each sign-in tried, whether or not it exists or the password was right:

```java
@PostMapping("/login")
ResponseEntity<?> login(@RequestBody LoginForm form, HttpServletRequest request) {
    Visionary.loginAttempt(request, form.email());
    return auth.authenticate(form.email(), form.password())
            .map(user -> ResponseEntity.ok(tokens.issue(user)))
            .orElse(ResponseEntity.status(401).build());
}
```

The value is trimmed and clipped to 255 characters (`login_account`).

To fill in handle and role from an id, declare a `VisionaryUserLookup` bean.
It runs on the background writer, once per distinct user per batch, only for
users missing a handle or role. Keep it cheap (a cache) and return `null` for
unknown ids:

```java
@Bean
VisionaryUserLookup visionaryUserLookup(UserCache users) {
    return id -> users.find(id)
            .map(u -> new VisionaryUser(id, u.username(), u.role()))
            .orElse(null);
}
```

### Database statements (automatic)

Every `DataSource` bean is wrapped in a lightweight proxy, so JPA/Hibernate,
`JdbcTemplate`, jOOQ, MyBatis and plain JDBC are all seen. Each `execute*`
call is a `db` span named after its first keyword and main table
(`SELECT users`, `UPDATE trips`), with target `postgresql/<database>` and the
sanitized statement. `commit` and `rollback` are spans too. Visionary's own
writes are never traced.

The wrapper keeps every interface of the original and answers
`unwrap`/`isWrapperFor`, so Boot's Hikari metrics still find the pool. Code
that injects the concrete class (`@Autowired HikariDataSource`) no longer
matches; inject `DataSource` and `unwrap` it, or set `VISIONARY_SPANS=false`.

With HikariCP, pool size and usage appear on the System page.

### Outbound HTTP (automatic, with Boot's builders)

Clients built from Boot's builders get an interceptor:

```java
@Bean
RestClient payments(RestClient.Builder builder) {          // seen
    return builder.baseUrl("https://api.stripe.com").build();
}

@Bean
RestTemplate legacy(RestTemplateBuilder builder) {          // seen
    return builder.build();
}

@Bean
WebClient search(WebClient.Builder builder) {              // seen, with WebFlux on the classpath
    return builder.baseUrl("https://search.internal").build();
}
```

Each call is an `http` span named `METHOD host`, with the URL without its
query string; a transport error or a 5xx marks it `error`. Clients made with
`new RestTemplate()`, `RestClient.create()` or `WebClient.create()` are not
seen. A `WebClient` call joins the request only when it is subscribed on the
request thread, as with `block()`.

### Vendor SDKs, caches, queues and your own code

The AWS SDK, Firebase and other libraries with their own HTTP stack, Redis
clients and CPU-heavy code are not seen until you wrap them:

```java
try (VisionarySpan span = Visionary.span("cache", "GET session")) {
    span.target("redis/sessions");
    return cache.get(key);
}
```

Kinds: `db`, `http`, `cache`, `queue`, `background`, `custom` (anything else
is stored as `custom`). A span also takes `.detail(...)`, `.rowCount(...)` and
`.error(e)`. Keep names short and free of ids, so they group. Time no span
covers shows as **Untracked** in the request's breakdown; see
[What the middleware sees](../../Readme.md#what-the-middleware-sees-and-what-needs-code).

### @Async and executors

Spans join the request through a thread-local. Work on another thread joins
it when:

- it runs on Boot's task executor (`@Async`, MVC `Callable`/`WebAsyncTask`):
  the starter registers a `VisionaryTaskDecorator` unless the application
  declares its own `TaskDecorator`; then add `Visionary.wrap(...)` to yours,
  or combine both with Spring's `CompositeTaskDecorator`;
- you wrap it: `executor.submit(Visionary.wrap(task))` (`Runnable` or
  `Callable`).

Not propagated: `CompletableFuture.supplyAsync` on the common pool, raw
`new Thread`, `parallelStream`, and reactor operators that move work to other
schedulers. Calls made there are spans outside any request.

### Work that outlives the response

```java
@PostMapping("/signup")
User signup(@RequestBody SignupForm form) {
    User user = users.create(form);
    Visionary.background("send welcome email", () -> mailer.sendWelcome(user));
    return user;
}
```

`Visionary.background(name, runnable)` runs the task on a Visionary daemon
thread and returns a `CompletableFuture<Void>`. For your own executor:
`executor.submit(Visionary.wrap("rebuild index", () -> index.rebuild(id)))`.

The request's breakdown shows the task as one `background` span, from start to
finish, with status `error` if it threw (the exception is rethrown, or fails
the future). The calls inside it keep the request's `request_id`, even after
the response; they are not counted in its `db_*`/`http_*` totals. Daemon
threads do not hold up shutdown, so work that must not be lost belongs in a
job queue.

### Errors

Recorded by themselves, with `handled = 0` and without changing what the
client gets:

- exceptions that escape the filter chain (the container's 500);
- exceptions Spring MVC's `HandlerExceptionResolver`s turn into a 5xx
  (`@ExceptionHandler`, `@ResponseStatus`, `ResponseStatusException`).

Report errors you handle yourself:

```java
try {
    payments.charge(order);
} catch (PaymentException e) {
    Visionary.captureException(e); // handled = 1, linked to the current request
    return ResponseEntity.status(402).build();
}
```

Each row has the exception class, message (1000 characters), stack trace
(16 KB), the release and a fingerprint that groups repeats: the class name
plus the top five frames of the application's own code
(`pkg.Class.method@File.java`, no line numbers). Frames in `java.*`,
`javax.*`, `jakarta.*`, `sun.*`, `jdk.*`, `org.springframework.*`,
`org.apache.*` and the SDK are skipped. At most 20 rows per fingerprint per
minute are written by each instance.

### Query plans

Not in this starter. The Queries page's **Explain** button needs an SDK that
can run `EXPLAIN` on your database (Go or Node today). For a database only a
Spring service talks to, the plan request stays pending.

### CPU profiles

No code. Off until you turn it on in the dashboard, under Settings →
Profiling, and you can turn it off there at any time, with no redeploy. The
same page picks which services take part (by `VISIONARY_SERVICE`; an empty
list means all), how often (default every 5 minutes) and how long (default
10 s), and has a **Profile now** button. The starter reads these settings at
start and every 30 seconds and logs `visionary: CPU profiling on (...)` or
`off`.

Sampling uses JDK Flight Recorder in-process (`jdk.ExecutionSample` every
10 ms). It costs a few percent of one CPU while a profile runs and nothing in
between. It skips a turn when no request arrived since the last one, and keeps
a profile with at least 20 samples (a Profile now one with any). Without the
`jdk.jfr` module (some `jlink` images) it falls back to `ThreadMXBean`
sampling, which costs more per sample and is safepoint-biased.

Frames read `pkg.Class.method file:line`: the JDK as `std:...`, your
application (the packages of its `@SpringBootApplication`, or classes from a
directory or `BOOT-INF/classes`) as a plain path, everything else as
`lib:<jar name>/...`.

Set `VISIONARY_PROFILE=false` in a service that must never be sampled: it then
ignores the dashboard and does not read the settings.

### System metrics (automatic)

Every 15 seconds (`VISIONARY_SYSTEM_INTERVAL`): host CPU, memory, disk,
network; this process's CPU, threads and file descriptors; container cgroup
limits and throttling; JVM heap and GC; the Hikari pool; request throughput.
No extra dependencies: the JVM's MXBeans everywhere, plus `/proc` on Linux.
Values a platform does not offer are stored as NULL. See
[System metric columns](#system-metric-columns).

### Bodies and redaction

Request and response bodies are kept up to 8 KB each
(`VISIONARY_LOG_MAX_BODY_BYTES`). Before anything is stored, values are
replaced, case-insensitively, for:

- headers `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`,
  `X-Api-Key`, `X-Auth-Token`;
- JSON and form fields `password`, `passwd`, `secret`, `client_secret`,
  `token`, `access_token`, `refresh_token`, `id_token`, `api_key`, `apikey`,
  `otp`, `pin`, `card_number`, `cvv`, `private_key`, in request and response
  bodies, including bodies cut off at the size cap.

Add your own:

```sh
VISIONARY_LOG_REDACT_HEADERS=X-Session
VISIONARY_LOG_REDACT_FIELDS=ssn,dob
VISIONARY_LOG_REDACT_REQUEST_FIELDS=code   # request bodies only
```

Multipart, image, video, audio, `application/octet-stream`, `application/zip`
and `application/pdf` bodies are never stored. `VISIONARY_LOG_BODIES=false`
keeps sizes only.

### Release markers

```sh
VISIONARY_RELEASE=v1.4.2
```

Without it, the release comes from `RAILWAY_GIT_COMMIT_SHA`,
`RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`, `HEROKU_SLUG_COMMIT`,
`SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA` or `K_REVISION` (commit hashes
cut to 12 characters). When known, one `releases` row is written at start
(`sdk` = `java`, `runtime` such as `OpenJDK 21.0.4`) and the charts show the
deploy.

## What is stored

| Table | What |
|---|---|
| `api_logs` | one row per request, with its call totals (`db_count`, `db_ms`, `http_count`, `http_ms`) |
| `spans` | the database statements, outbound calls and custom spans of each request, and slow calls outside requests |
| `errors` | unhandled and reported exceptions |
| `releases` | one deploy marker per start, when the release is known |
| `system_metrics` | host, process, JVM, pool and throughput samples |
| `profiles` | CPU profiles, only while turned on in the dashboard |

- **Request id:** an incoming `X-Request-ID` up to 128 characters, else a
  random 32-hex id, echoed on the response.
- **Error codes:** for responses with status 400 or higher, the `error.code`
  and `error.key` values of a JSON error body.
- **Spans:** at most `VISIONARY_SPAN_MAX` (100) per request, in start order;
  the totals count all calls, also beyond the cap. Spans outside a request
  (scheduled jobs, background threads) are stored when they take at least
  `VISIONARY_SPAN_THRESHOLD_MS` (100).
- **Statements** have literals replaced by `?` and `IN (?, ?, ...)` collapsed
  to `IN (?)`. Parameter values are never stored. Row counts are the affected
  rows of updates and the rows read from result sets.
- **Writing:** requests, spans and errors go into one in-memory queue of 5000
  entries that never blocks; when it is full, entries are dropped and the
  count is logged. The writer flushes every 50 entries or 3 seconds, retrying
  a failed batch twice. System samples are written 4 at a time; while the
  database is unreachable, up to an hour of them is kept.
- **Upgrades:** tables from an earlier version gain the new columns at start
  (`ALTER TABLE ... ADD COLUMN`). The schemas in the jar are copies of
  `sdks/go/schema/<dialect>/`.

### System metric columns

Counters are deltas or per-second rates since the previous sample.

| Group | Columns | Source | Available on |
|---|---|---|---|
| CPU | `host_cpu_percent`, `cpu_count` | `OperatingSystemMXBean`, `availableProcessors()` | all |
| | `cpu_user_percent`, `cpu_system_percent`, `cpu_iowait_percent`, `cpu_steal_percent`, `ctx_switches_ps`, `procs_running`, `procs_blocked` | `/proc/stat` | Linux |
| | `load_1` | `getSystemLoadAverage()` | Linux, macOS |
| Memory | `host_mem_used_mb`, `host_mem_total_mb`, `host_mem_percent`, `swap_used_mb`, `swap_total_mb` | `OperatingSystemMXBean` | all |
| | `host_mem_available_mb` | `/proc/meminfo` MemAvailable; free memory elsewhere | all |
| | `host_mem_cached_mb` | `/proc/meminfo` Cached + SReclaimable | Linux |
| Disk | `disk_used_percent`, `disk_free_gb`, `disk_total_gb` (working directory's filesystem) | `java.io.File` | all |
| | `disk_read_kbps`, `disk_write_kbps`, `disk_read_iops`, `disk_write_iops`, `disk_await_ms`, `disk_busy_percent` (whole disks only) | `/proc/diskstats` | Linux |
| Network | `net_rx_kbps`, `net_tx_kbps`, `net_rx_pps`, `net_tx_pps`, `net_errors`, `net_drops` | `/proc/net/dev` | Linux |
| | `tcp_established`, `tcp_retrans` | `/proc/net/snmp` | Linux |
| Processes | `host_procs` | numeric entries of `/proc` | Linux |
| This process | `proc_cpu_percent`, `proc_threads`, `goroutines` (live JVM threads) | MXBeans | all |
| | `proc_open_fds`, `proc_max_fds` | `UnixOperatingSystemMXBean` | Linux, macOS |
| | `proc_rss_mb`, `proc_read_kbps`, `proc_write_kbps` | `/proc/self/status`, `/proc/self/io` | Linux |
| Container | `container_cpu_cores`, `container_cpu_limit`, `container_cpu_throttled_percent`, `container_mem_used_mb`, `container_mem_limit_mb`, `container_oom_kills` (only in a container or when the cgroup sets a limit) | `/sys/fs/cgroup` (v2 or v1) | Linux |
| JVM | `heap_alloc_mb`, `heap_inuse_mb` (heap used / committed), `gc_count`, `gc_pause_ms`, `gc_cpu_percent` | `MemoryMXBean`, `GarbageCollectorMXBean` | all |
| Pool | `db_total_conns`, `db_idle_conns`, `db_acquired_conns`, `db_max_conns` | `HikariPoolMXBean`, when the app's `DataSource` is a `HikariDataSource` | all |
| HTTP | `http_requests`, `http_errors`, `http_rps`, `http_in_flight`, `uptime_s` | the filter | all |

Always NULL in this SDK: `load_5`, `load_15`, `heap_objects`,
`disk_inodes_percent`, `db_acquires`, `db_empty_acquires`,
`db_acquire_wait_ms`. Inside a container, the `/proc` figures describe what
the container can see: host CPU, memory and disks, but only its own processes
and network.

## Configuration

Read through Spring's `Environment`: environment variables, `-D` system
properties, or `application.properties` under the same names. Booleans take
`true`/`false`/`1`/`0`/`yes`/`no`.

| Variable | Default | Meaning |
|---|---|---|
| `VISIONARY_DATABASE_URL` | unset | Where rows go. See [Databases](#databases). |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | unset | Cloudflare D1. |
| `D1_API_BASE` | `https://api.cloudflare.com/client/v4` | Another D1 API URL (a proxy). |
| `VISIONARY_USE_APP_DATASOURCE` | `false` | Write through the application's own `DataSource` when no URL or D1 is set. |
| `VISIONARY_SERVICE` | none | Names the service when several share a database. |
| `VISIONARY_RELEASE` | the platform's commit variable | The version on deploy markers. See [Release markers](#release-markers). |
| `VISIONARY_INSTANCE` | hostname | Labels this server on the System page. |
| `VISIONARY_LOG_REQUESTS` | `true` | Log requests to `api_logs`. |
| `VISIONARY_LOG_PATH_PREFIX` | all paths | Log only paths starting with this, e.g. `/api`. |
| `VISIONARY_LOG_BODIES` | `true` | Store request and response bodies (redacted, capped). |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` | Bytes kept per body. |
| `VISIONARY_LOG_REDACT_HEADERS` | none | Comma list of headers to redact, added to the defaults. |
| `VISIONARY_LOG_REDACT_FIELDS` | none | Comma list of JSON/form fields to redact, added to the defaults. |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | none | Fields redacted in request bodies only. |
| `VISIONARY_LOG_ERROR_CODE_FIELD` | `error.code` | Dotted path in JSON error responses, stored as `error_code`. |
| `VISIONARY_LOG_ERROR_KEY_FIELD` | `error.key` | The same, stored as `error_key`. |
| `VISIONARY_LOG_RETENTION_DAYS` | `0` (off) | Set only when no Visionary server reads this database: the starter then deletes `api_logs`, `spans`, `errors` and `profiles` rows older than this, a minute after start and then daily. `releases` are kept. |
| `VISIONARY_SPANS` | `true` | Record statements and outbound calls. `false` also removes the `DataSource` wrapper and leaves the totals NULL. |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | Keep spans outside requests at least this slow. |
| `VISIONARY_SPAN_MAX` | `100` | Spans kept per request. |
| `VISIONARY_ERRORS` | `true` | Record exceptions. |
| `VISIONARY_PROFILE` | `true` (follow the dashboard) | `false`: never take CPU profiles, whatever Settings → Profiling says. |
| `VISIONARY_SYSTEM_METRICS` | `true` | Record `system_metrics`. |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | Sample interval; a plain number is seconds, also `500ms`, `1m`. |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` (off) | Like `VISIONARY_LOG_RETENTION_DAYS`, for `system_metrics`. |

Everything else (sign-in, alerts, retention, profiling, thresholds) is set in
the Visionary dashboard under Settings, not in the service.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `visionary: no database configured ...; collection is off` | No database set, or set under another name (`visionary.database-url`). | Set `VISIONARY_DATABASE_URL` or the three `D1_*` variables, with these exact names. |
| `visionary: disabled: no JDBC driver for ...` | The driver for the URL is not on the classpath. | Add the driver from [Databases](#databases). |
| `visionary: disabled: VISIONARY_USE_APP_DATASOURCE=true but the application has no single DataSource bean` | Zero or several `DataSource` beans. | Use `VISIONARY_DATABASE_URL` instead, or mark one bean `@Primary`. |
| No `visionary: writing to ...` line at all | The app is not a servlet web application (WebFlux server, or no web server). | Use Spring MVC; this starter needs a servlet container. |
| Nothing shows up, no errors | The app and the server use different databases (another file, another D1 database, another schema). | Use the exact same URL or D1 IDs in both. For SQLite, use an absolute path. |
| Some requests are missing | `VISIONARY_LOG_PATH_PREFIX` excludes them. | Unset it, or set the prefix your routes really start with. |
| Outbound calls missing, or time shows as Untracked | The client was built with `new RestTemplate()` or `create()`, the call ran on a thread the request does not reach, or it goes through a vendor SDK. | Build clients from Boot's builders, wrap tasks with `Visionary.wrap`, or wrap the call in `Visionary.span`. |
| `No qualifying bean of type HikariDataSource` at startup | Code injects the concrete pool class, which the span wrapper hides. | Inject `DataSource` and `unwrap(HikariDataSource.class)`, or set `VISIONARY_SPANS=false`. |
| Explain on the Queries page stays pending forever | This starter has no explainer, and no other service registered one for that database (its target must match the span's, e.g. `postgresql/<dbname>`). | Run a Go or Node service with an explainer for that database, or read the plan with `EXPLAIN` yourself. |
| No CPU profiles | Profiling is off in the dashboard, the service is not in its list, or no request arrived since the last turn. | Turn it on in Settings → Profiling and include the service. Check `VISIONARY_PROFILE` is not `false`. |
| `visionary: CPU profiling off: cannot read visionary_settings` | The Visionary server has not created its tables in this database yet. | Start the server against the same database. |
| D1 writes fail with `exceeded D1's free tier daily row read limit` | Cloudflare's free plan limit is used up for today. | Wait for the reset at midnight UTC, move to a paid Workers plan, or use PostgreSQL or MySQL. |
| `visionary: request queue full; N entries dropped` | Writes cannot keep up, usually SQLite or D1 under heavy traffic. | Use PostgreSQL or MySQL. |
| `database is locked` with SQLite | The app and the server write the same file. | Add the busy timeout and WAL parameters from [Databases](#databases) to both URLs. |
| Last requests before shutdown are missing, or `visionary: request log flush timed out` | The JVM was killed before the flush finished (`kill -9`, a container stop timeout under 10 s). | Stop the app gracefully (SIGTERM) and give it at least 10 seconds. |
