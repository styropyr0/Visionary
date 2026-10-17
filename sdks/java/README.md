# Visionary for Spring Boot

A Spring Boot starter that collects what Visionary analyses, straight from a
servlet (Spring MVC) application:

- one row per HTTP request in `api_logs`, and
- periodic host, process, JVM, connection-pool and throughput samples in `system_metrics`,

written in batches on background threads to Cloudflare D1, PostgreSQL, MySQL/MariaDB
or SQLite. The Visionary server reads those tables for its dashboards.

It follows the same contract as the other Visionary SDKs; see
[`sdks/SPEC.md`](../SPEC.md). The Go SDK in [`sdks/go`](../go) is the reference
implementation.

Requirements: Java 17+, Spring Boot 3.x, a servlet web application (Tomcat, Jetty or Undertow).

## Install

The artifact is not published to Maven Central yet. Build and install it into your
local Maven repository first:

```sh
cd sdks/java
./mvnw install        # Windows: mvnw.cmd install
```

Then add it to your application.

Maven:

```xml
<dependency>
    <groupId>io.github.styropyr0</groupId>
    <artifactId>visionary-spring-boot-starter</artifactId>
    <version>0.1.0-SNAPSHOT</version>
</dependency>
```

Gradle (with `mavenLocal()` in `repositories`):

```kotlin
implementation("io.github.styropyr0:visionary-spring-boot-starter:0.1.0-SNAPSHOT")
```

## Quickstart

1. Add the dependency (and the JDBC driver for your database, see below).
2. Point it at a database:

   ```sh
   export VISIONARY_DATABASE_URL=postgres://user:pass@db.internal:5432/app
   ```

3. Start the application. The starter auto-configures itself, creates both tables
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
| JVM | `heap_alloc_mb`, `heap_inuse_mb` (heap used / committed), `gc_count`, `gc_pause_ms`, `gc_cpu_percent` | `MemoryMXBean`, `GarbageCollectorMXBean` | all |
| Pool | `db_total_conns`, `db_idle_conns`, `db_acquired_conns`, `db_max_conns` | `HikariPoolMXBean`, when the app's `DataSource` is a `HikariDataSource` | all |
| HTTP | `http_requests`, `http_errors`, `http_rps`, `http_in_flight`, `uptime_s` | the filter | all |

Always NULL in this SDK: `load_5`, `load_15`, `heap_objects`, `disk_inodes_percent`
(the JDK has no inode counts), and `db_acquires`, `db_empty_acquires`,
`db_acquire_wait_ms` (HikariCP's MXBean has no cumulative acquire counters).
Inside a container, the `/proc` figures describe what the container can see: host
CPU, memory and disks, but only its own processes and network namespace.

Tables created by an earlier version gain the newer `system_metrics` columns at
startup (`ALTER TABLE ... ADD COLUMN`), so upgrading needs no manual migration.

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
| `VISIONARY_LOG_RETENTION_DAYS` | `30` (0 keeps rows forever) |
| `VISIONARY_LOG_REDACT_HEADERS`, `VISIONARY_LOG_REDACT_FIELDS`, `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | empty (comma lists, added to the defaults) |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `VISIONARY_LOG_ERROR_KEY_FIELD` | `error.code`, `error.key` |
| `VISIONARY_SYSTEM_METRICS` | `true` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` (plain number = seconds; also `500ms`, `1m`) |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `30` |

Booleans accept `true/false/1/0/yes/no`.

## Behaviour notes

- Requests go into a bounded in-memory queue (5000 entries) that never blocks; when
  it is full, entries are dropped and the count is logged. The writer flushes every
  50 entries or 3 seconds, retrying a failed batch twice (after 1 s and 2 s).
- System samples are written 4 at a time. While the database is unreachable, up to
  an hour of samples is kept and written once it is back.
- Retention runs a minute after start and then daily, deleting in chunks of 5000.
- On shutdown, after the web server has stopped accepting requests, queued requests
  and buffered samples are flushed (at most 10 seconds each).
- The schemas shipped in the jar are copies of `sdks/go/schema/<dialect>/`.
