# Visionary for Spring Boot

A Spring Boot starter that collects what Visionary analyses, straight from a
servlet (Spring MVC) application:

- one row per HTTP request in `api_logs`, with the number and total time of its
  database and outbound HTTP calls,
- the database statements and outbound HTTP calls of slow or failed requests in `spans`,
- unhandled and reported exceptions in `errors`, and a deploy marker per release in `releases`,
- periodic host, process, JVM, connection-pool and throughput samples in `system_metrics`,
- CPU profiles in `profiles`, when turned on from the dashboard,

written in batches on background threads to Cloudflare D1, PostgreSQL, MySQL/MariaDB
or SQLite. The Visionary server reads those tables for its dashboards.

It follows the same contract as the other Visionary SDKs; see
[`sdks/SPEC.md`](../SPEC.md). The Go SDK in [`sdks/go`](../go) is the reference
implementation.

Requirements: Java 17+, Spring Boot 3.x, a servlet web application (Tomcat, Jetty or Undertow).

## Install

From Maven Central, where each release tag is published (the version is the tag without the
`v`; see the [releases](https://github.com/styropyr0/Visionary/releases)).

Maven:

```xml
<dependency>
    <groupId>io.github.styropyr0</groupId>
    <artifactId>visionary-spring-boot-starter</artifactId>
    <version>0.4.0</version>
</dependency>
```

Gradle:

```kotlin
implementation("io.github.styropyr0:visionary-spring-boot-starter:0.4.0")
```

**JitPack**, as an alternative: JitPack builds the whole repository from a tag, and the
repository's `jitpack.yml` builds only the Android SDK, so this starter is not available there.
To use an unreleased commit, build it into your local Maven repository instead:

```sh
cd sdks/java
./mvnw install        # Windows: mvnw.cmd install
```

and depend on `io.github.styropyr0:visionary-spring-boot-starter:0.1.0-SNAPSHOT` (the version in
`pom.xml`) with `mavenLocal()` in your Gradle `repositories`.

## Quickstart

1. Add the dependency (and the JDBC driver for your database, see below).
2. Point it at a database:

   ```sh
   export VISIONARY_DATABASE_URL=postgres://user:pass@db.internal:5432/app
   ```

3. Start the application. The starter auto-configures itself, creates its tables
   if they don't exist, and starts logging. You will see one line like
   `visionary: writing to jdbc:postgresql://db.internal:5432/app (postgres); requests on ...`.

With no database configured, the starter logs one warning and does nothing else;
the application starts and serves exactly as before. A database that goes down
later never affects requests: writes fail on the background threads, are retried,
and are logged.

## Choosing the database

The first match wins:

| Setting | Backend |
|---|---|
| `VISIONARY_DATABASE_URL=d1://` | Cloudflare D1, using the `D1_*` variables |
| `VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/db` (or `postgresql://`) | PostgreSQL |
| `VISIONARY_DATABASE_URL=mysql://user:pass@host:3306/db` (or `mariadb://`) | MySQL 8+ / MariaDB |
| `VISIONARY_DATABASE_URL=sqlite:///absolute/path.db`, `sqlite://relative.db` | SQLite |
| `VISIONARY_DATABASE_URL=jdbc:postgresql:…`, `jdbc:mysql:…`, `jdbc:mariadb:…`, `jdbc:sqlite:…` | the JDBC URL as given |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` set (and no URL) | Cloudflare D1 |
| `VISIONARY_USE_APP_DATASOURCE=true` (and none of the above) | the application's own `DataSource` bean |

Query parameters on `postgres://` and `mysql://` URLs are passed to the driver
(for example `?sslmode=require` or `?currentSchema=metrics`); user and password
are taken from the URL and may be percent-encoded. On Windows,
`sqlite:///C:/data/visionary.db` is an absolute path.

D1 is reached over its REST API with the JDK `HttpClient` and needs no driver.
`D1_API_BASE` overrides `https://api.cloudflare.com/client/v4`.

The JDBC drivers are the application's choice and are not pulled in by the starter:

| Database | Driver dependency |
|---|---|
| PostgreSQL | `org.postgresql:postgresql` |
| MySQL | `com.mysql:mysql-connector-j` |
| MariaDB | `org.mariadb.jdbc:mariadb-java-client` |
| SQLite | `org.xerial:sqlite-jdbc` |

With its own URL the starter keeps a single connection for its background threads
and reconnects after errors. With `VISIONARY_USE_APP_DATASOURCE=true` it borrows
connections from the application's pool instead and detects the dialect from the
database metadata. In both cases each batch is written in one transaction.

## Attaching the user

Call `Visionary.setUser` once the user is known, typically in auth code:

```java
import io.github.styropyr0.visionary.Visionary;

Visionary.setUser(request, user.getId(), user.getUsername(), user.getRole());
// or just the id:
Visionary.setUser(request, user.getId());
```

When nothing is set and Spring Security is on the classpath, the authenticated
principal's name (`Authentication.getName()`, anonymous users excluded) is stored
as `user_id`.

To fill in handle and role from an id, declare a `VisionaryUserLookup` bean. It
runs on the background writer thread, once per distinct user per batch, only for
users missing a handle or role; keep it cheap (a cache) and return `null` for
unknown ids:

```java
@Bean
VisionaryUserLookup visionaryUserLookup(UserCache users) {
    return id -> users.find(id)
            .map(u -> new VisionaryUser(id, u.username(), u.role()))
            .orElse(null);
}
```

## Service and release

Set `VISIONARY_SERVICE` when several services share one database. The release
(`version` column) is `VISIONARY_RELEASE`, else the first commit variable a platform
sets (`RAILWAY_GIT_COMMIT_SHA`, `RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`,
`HEROKU_SLUG_COMMIT`, `SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA`, `K_REVISION`);
commit hashes are shortened to 12 characters. Both are stored on every `api_logs`,
`system_metrics`, `errors` and `spans` row (spans: service only). When the release
is known, one `releases` row is written at start (`sdk` = `java`, `runtime` such as
`OpenJDK 21.0.4`), which the dashboards draw as a deploy marker.

## Spans

The filter records the request; the calls inside it are recorded only when
their client is hooked: the wrapped `DataSource` and the Spring HTTP clients
below. Vendor SDKs with their own HTTP stack (the AWS SDK, Firebase) and CPU
work are not seen until you wrap them in `Visionary.span`. Anything left
shows as **Untracked** in the request's breakdown; see
[What the middleware sees](../../Readme.md#what-the-middleware-sees-and-what-needs-code).

Captured automatically, with no code changes:

- **JDBC**: every `DataSource` bean of the application is wrapped in a lightweight
  `java.lang.reflect.Proxy` (connections, statements and result sets), so JPA/Hibernate,
  `JdbcTemplate`, jOOQ, MyBatis and plain JDBC are all seen. Each `execute*` call is a
  `db` span named after its first keyword and main table (`SELECT users`, `UPDATE trips`),
  with target `postgresql/<database>` and the statement as detail, its literals replaced
  by `?` and `IN (?, ?, ...)` collapsed to `IN (?)`; parameter values are never stored.
  Row counts are the affected rows of updates and the rows read from result sets.
  `commit` and `rollback` are spans too. Visionary's own writes are never traced.
- **HTTP clients**: `RestTemplate`s built from Boot's `RestTemplateBuilder`, `RestClient`s
  built from Boot's `RestClient.Builder` and, when WebFlux is on the classpath,
  `WebClient`s built from Boot's `WebClient.Builder` get an interceptor. Each call is an
  `http` span named `METHOD host`, with the URL without its query string; a transport
  error or a 5xx response marks it `error`. Clients created with `new RestTemplate()` or
  `RestClient.create()` are not seen.
- **Anything else**, by hand:

  ```java
  try (VisionarySpan span = Visionary.span("cache", "GET session")) {
      span.target("redis/sessions");
      return cache.get(key);
  }
  ```

  Kinds are `db`, `http`, `cache`, `queue`, `background` and `custom`.

Every logged request gets `db_count`, `db_ms`, `http_count` and `http_ms` (all calls,
also beyond the cap). Span rows are stored for every request, at most `VISIONARY_SPAN_MAX`
(default 100) per request, in start order. Spans outside a request (scheduled jobs,
background threads) are stored on their own when they take at least
`VISIONARY_SPAN_THRESHOLD_MS` (default 100).

Spans join the request through a thread-local. Work handed to another thread joins it when:

- it runs on Boot's task executor (`@Async`, MVC `Callable`/`WebAsyncTask`): the starter
  registers a `VisionaryTaskDecorator` unless the application declares its own
  `TaskDecorator`; then add `Visionary.wrap(...)` to yours, or combine both with Spring's
  `CompositeTaskDecorator`;
- it is wrapped by hand: `executor.submit(Visionary.wrap(task))`;
- it is started with `Visionary.background` or `Visionary.wrap(name, task)` (below).

Not propagated: `CompletableFuture.supplyAsync` on the common pool, raw `new Thread`,
`parallelStream`, and reactor operators that move work to other schedulers (a `WebClient`
call joins the request only when it is subscribed on the request thread, as with
`block()`). Calls made there are spans outside any request.

### Background work

Work a handler starts and does not wait for stays linked to its request:

```java
@PostMapping("/signup")
User signup(@RequestBody SignupForm form) {
    User user = users.create(form);
    Visionary.background("send welcome email", () -> mailer.sendWelcome(user));
    return user;
}
```

`Visionary.background(name, runnable)` runs the task on a Visionary daemon thread (a pool
that grows as needed and drops threads idle for a minute, so tasks never wait for each
other) and returns a `CompletableFuture<Void>`. For your own executor, wrap the task:
`executor.submit(Visionary.wrap("rebuild index", () -> index.rebuild(id)))`; a `Callable`
works too. Daemon threads do not hold up shutdown, so work that must not be lost belongs
in a job queue.

Each call is recorded as one span of kind `background` with the name you gave, from when
the task started to when it finished, with status `error` and the message if it threw (the
exception is rethrown, or fails the future). The calls made inside it are spans of their
own with the request's `request_id` and their `offset_ms` from the request's start, even
after the response was sent; `offset_ms + duration_ms` past the request's `duration_ms`
shows they ran after it. Spans that end after the request was logged are not counted in
its `db_*`/`http_*` totals, and `background` spans never are. They are stored whatever
their duration, up to `VISIONARY_SPAN_MAX` per request (on top of the spans stored with
the request); further ones only when they take at least `VISIONARY_SPAN_THRESHOLD_MS`.
Outside a request the helpers still record the span, with no `request_id`, stored when it
reaches the threshold.

The wrapped DataSource keeps every interface of the original and answers
`unwrap`/`isWrapperFor` with it, so Boot's Hikari metrics and pool code still find the pool.
Code that injects the concrete class (`@Autowired HikariDataSource`) instead of
`DataSource` no longer matches; inject `DataSource` and `unwrap` it, or set
`VISIONARY_SPANS=false`.

## Errors

Recorded with `handled = 0`, without changing what the client gets:

- exceptions that escape the filter chain (the container's 500), and
- exceptions Spring MVC's `HandlerExceptionResolver`s turn into a 5xx response
  (`@ExceptionHandler`, `@ResponseStatus`, `ResponseStatusException`).

Report errors the application handles itself with `Visionary.captureException(e)`
(`handled = 1`); inside a request it is linked to it (request id, method, route, path,
user). Each row has the exception class, message (1000 characters), the
`printStackTrace` output (16 KB), the release and a fingerprint that groups repeats: the
first 16 hex characters of SHA-1 over the class name and the top five frames from the
application's own code as `pkg.Class.method@File.java` (no line numbers), joined with
newlines. Frames in `java.*`, `javax.*`, `jakarta.*`, `sun.*`, `jdk.*`,
`org.springframework.*`, `org.apache.*` and the SDK are skipped; when no in-app frame is
left, the top five frames of any kind are used, and only an exception without any stack
frames is hashed by its class name and message (digit runs replaced by `0`). At most 20
rows per fingerprint per minute are written by each instance.

## CPU profiles

Off until you turn it on in the dashboard (**Settings → Profiling**); turning it off there
stops it again. No redeploy either way: the starter reads the `profile.*` rows of
`visionary_settings` at start and every 30 seconds on its own thread. The Visionary server
creates that table and `profiles`; until it has, profiling stays off and nothing is written.

While it is on for this service (`VISIONARY_SERVICE` in the dashboard's service list, or the
list is empty), the starter samples the CPU for the configured duration (default 10 s) every
interval (default 5 minutes), skipping a turn when no request arrived since the last one.
**Profile now** in the dashboard takes one profile at once, even while scheduled profiling is
off. Set `VISIONARY_PROFILE=false` in a service that must never be sampled: it then ignores the
dashboard and does not even read the settings.

Sampling uses JDK Flight Recorder in-process (`jdk.ExecutionSample` every 10 ms). JFR records
only threads that are running Java code, so idle, sleeping, blocked and I/O-waiting threads are
left out; the starter's own threads are skipped too. It costs a few percent of one CPU while a
profile runs and nothing in between, and it is not biased towards safepoints. The first profile
raises JFR's stack depth to 256 frames when JFR has not been started yet (deeper stacks begin
with a `(truncated)` frame). On a runtime without the `jdk.jfr` module (some `jlink` images) it
falls back to `ThreadMXBean`: every 10 ms, the stacks of RUNNABLE threads whose CPU time advanced.
That fallback is costlier per sample and safepoint-biased (a hot loop is credited to its nearest
safepoint), but still records the same rows. Native frames are not recorded in either mode.

Each profile is one row: when it started, service, instance, release, `sdk` = `java`, the measured
duration, the sample count, `routes` (JSON: `"METHOD /route/{id}"` to the milliseconds the
requests that finished during the profile took; the path when no route matched) and `folded`:
one `frame;frame;... count` line per distinct stack, outermost caller first, heaviest first up to
90 000 bytes with the rest summed into one `(other) n` line. A scheduled profile with fewer than
20 samples is dropped, and kept otherwise even when no request finished during it (`routes` is
then `{}`); a **Profile now** one is kept with any samples. Frames read `pkg.Class.method file:line`,
where the file says whose code it is:

- the JDK (`java.`, `javax.`, `jdk.`, `sun.`, `com.sun.`): `std:java/util/HashMap.java`;
- the application (the packages of its `@SpringBootApplication`, or any class loaded from a
  directory or a Boot jar's `BOOT-INF/classes`): `com/example/orders/OrderService.java:42`;
- everything else, this starter included: `lib:` and the jar's name without its version,
  `lib:spring-webmvc/DispatcherServlet.java:1089` (the top two package segments when the jar is
  unknown).

JFR does not record source file names, so the file is the outer class's name plus `.java`
(a Kotlin class shows as `Foo.java`). Profiles follow Settings → Retention, and
`VISIONARY_LOG_RETENTION_DAYS` when it is set.

## What is captured

Per request (see SPEC.md for the full column list): request id (incoming
`X-Request-ID` up to 128 characters, else a random 32-hex id, echoed on the
response), start time, method, path, the matched route pattern (for example
`/users/{id}`, from Spring MVC's best matching pattern), query string, final
status, duration, client IP (first `X-Forwarded-For` hop, else the peer), user
agent, user, request headers as JSON, content types, request and response bodies
up to `VISIONARY_LOG_MAX_BODY_BYTES` each with truncation flags, full body sizes,
and for responses with status 400 or higher the `error.code` / `error.key` values
of a JSON error body.

Bodies are skipped for multipart, image, video, audio, `application/octet-stream`,
`application/zip` and `application/pdf` content. The response body is copied as it
streams to the client, so streaming responses (`StreamingResponseBody`,
`SseEmitter`, async controllers) still stream; only the first
`VISIONARY_LOG_MAX_BODY_BYTES` are kept. Async requests are logged when they
complete. The filter runs first in the chain, so a handler exception that the
container turns into a 500 is logged as 500.

Redacted before anything is stored (case-insensitive, extendable with the
`VISIONARY_LOG_REDACT_*` variables):

- headers `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`, `X-Auth-Token`;
- JSON and form fields `password`, `passwd`, `secret`, `client_secret`, `token`,
  `access_token`, `refresh_token`, `id_token`, `api_key`, `apikey`, `otp`, `pin`,
  `card_number`, `cvv`, `private_key`, in request and response bodies, including
  bodies cut off at the size cap.

System samples every `VISIONARY_SYSTEM_INTERVAL` (counters are deltas or
per-second rates since the previous sample; the first reading is taken at start).
Values a platform doesn't offer are stored as NULL. No extra dependencies: the
JVM's MXBeans everywhere, plus `/proc` on Linux.

| Group | Columns | Source | Available on |
|---|---|---|---|
| CPU | `host_cpu_percent`, `cpu_count` | `OperatingSystemMXBean`, `availableProcessors()` | all |
| | `cpu_user_percent`, `cpu_system_percent`, `cpu_iowait_percent`, `cpu_steal_percent`, `ctx_switches_ps`, `procs_running`, `procs_blocked` | `/proc/stat` | Linux |
| | `load_1` | `getSystemLoadAverage()` | Linux, macOS (`load_5`/`load_15` are always NULL) |
| Memory | `host_mem_used_mb`, `host_mem_total_mb`, `host_mem_percent`, `swap_used_mb`, `swap_total_mb` | `OperatingSystemMXBean` | all |
| | `host_mem_available_mb` | `/proc/meminfo` MemAvailable; free memory elsewhere | all |
| | `host_mem_cached_mb` | `/proc/meminfo` Cached + SReclaimable | Linux |
| Disk | `disk_used_percent`, `disk_free_gb`, `disk_total_gb` (working directory's filesystem) | `java.io.File` | all |
| | `disk_read_kbps`, `disk_write_kbps`, `disk_read_iops`, `disk_write_iops`, `disk_await_ms`, `disk_busy_percent` (whole disks only, partitions skipped) | `/proc/diskstats` | Linux |
| Network | `net_rx_kbps`, `net_tx_kbps`, `net_rx_pps`, `net_tx_pps`, `net_errors`, `net_drops` (all interfaces) | `/proc/net/dev` | Linux |
| | `tcp_established`, `tcp_retrans` | `/proc/net/snmp` | Linux |
| Processes | `host_procs` | numeric entries of `/proc` | Linux |
| This process | `proc_cpu_percent`, `proc_threads`, `goroutines` (live JVM threads) | MXBeans | all |
| | `proc_open_fds`, `proc_max_fds` | `UnixOperatingSystemMXBean` | Linux, macOS |
| | `proc_rss_mb`, `proc_read_kbps`, `proc_write_kbps` | `/proc/self/status`, `/proc/self/io` | Linux |
| Container | `container_cpu_cores`, `container_cpu_limit`, `container_cpu_throttled_percent`, `container_mem_used_mb`, `container_mem_limit_mb`, `container_oom_kills` (the process's own cgroup; filled only in a container or when the cgroup sets a CPU or memory limit) | `/sys/fs/cgroup` (v2 or v1) | Linux |
| JVM | `heap_alloc_mb`, `heap_inuse_mb` (heap used / committed), `gc_count`, `gc_pause_ms`, `gc_cpu_percent` | `MemoryMXBean`, `GarbageCollectorMXBean` | all |
| Pool | `db_total_conns`, `db_idle_conns`, `db_acquired_conns`, `db_max_conns` | `HikariPoolMXBean`, when the app's `DataSource` is a `HikariDataSource` | all |
| HTTP | `http_requests`, `http_errors`, `http_rps`, `http_in_flight`, `uptime_s` | the filter | all |

Always NULL in this SDK: `load_5`, `load_15`, `heap_objects`, `disk_inodes_percent`
(the JDK has no inode counts), and `db_acquires`, `db_empty_acquires`,
`db_acquire_wait_ms` (HikariCP's MXBean has no cumulative acquire counters).
Inside a container, the `/proc` figures describe what the container can see: host
CPU, memory and disks, but only its own processes and network namespace.

Tables created by an earlier version gain the newer `api_logs` and `system_metrics`
columns at startup (`ALTER TABLE ... ADD COLUMN`), so upgrading needs no manual migration.

## Configuration

Everything is read from the environment variables in SPEC.md, through Spring's
`Environment`, so the same names also work as `-D` system properties or in
`application.properties`.

| Variable | Default |
|---|---|
| `VISIONARY_DATABASE_URL` | unset; see above |
| `VISIONARY_USE_APP_DATASOURCE` | `false` |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN`, `D1_API_BASE` | for D1 only |
| `VISIONARY_INSTANCE` | hostname |
| `VISIONARY_LOG_REQUESTS` | `true` |
| `VISIONARY_LOG_PATH_PREFIX` | empty (log everything) |
| `VISIONARY_LOG_BODIES` | `true` |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` |
| `VISIONARY_LOG_RETENTION_DAYS` | `0` (off; above 0 the starter also deletes older `api_logs`, `spans`, `errors` and `profiles` rows) |
| `VISIONARY_LOG_REDACT_HEADERS`, `VISIONARY_LOG_REDACT_FIELDS`, `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | empty (comma lists, added to the defaults) |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `VISIONARY_LOG_ERROR_KEY_FIELD` | `error.code`, `error.key` |
| `VISIONARY_SYSTEM_METRICS` | `true` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` (plain number = seconds; also `500ms`, `1m`) |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` (off; above 0 the starter also deletes older `system_metrics` rows) |
| `VISIONARY_SERVICE` | empty (NULL) |
| `VISIONARY_RELEASE` | the platform's commit variable, else empty (NULL) |
| `VISIONARY_SPANS` | `true` (`false` also removes the DataSource wrapper and leaves the totals NULL) |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` |
| `VISIONARY_SPAN_MAX` | `100` |
| `VISIONARY_ERRORS` | `true` |
| `VISIONARY_PROFILE` | `true` (follow the dashboard; `false` never profiles, whatever it says) |

Booleans accept `true/false/1/0/yes/no`.

The Visionary server applies Settings → Retention to every table; the two
`*_RETENTION_DAYS` variables only matter when the service writes to a database no
Visionary server reads.

## Behaviour notes

- Requests, background spans and errors go into one bounded in-memory queue (5000
  entries; a request and its spans are one entry) that never blocks; when
  it is full, entries are dropped and the count is logged. The writer flushes every
  50 entries or 3 seconds, retrying a failed batch twice (after 1 s and 2 s).
- System samples are written 4 at a time. While the database is unreachable, up to
  an hour of samples is kept and written once it is back.
- Old rows are deleted by the Visionary server (Settings → Retention). With a retention
  variable above 0 the starter also purges, a minute after start and then daily, in chunks of 5000.
  `spans`, `errors` and `profiles` follow `VISIONARY_LOG_RETENTION_DAYS`; `releases` are kept.
- On shutdown, after the web server has stopped accepting requests, queued requests
  and buffered samples are flushed (at most 10 seconds each).
- The schemas shipped in the jar are copies of `sdks/go/schema/<dialect>/`.
