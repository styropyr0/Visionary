# Visionary for Android

A small Kotlin library that reports what a native Android app experiences to a Visionary
server: screen load times, API call timings, handled errors, crashes and your own events.
They show up in Visionary's Frontend views next to your backend's `api_logs`.

The SDK talks to the server's client ingest endpoint (`POST /api/ingest/client`, see
[`sdks/SPEC.md`](../SPEC.md), "Client ingest API"). It never touches your database.

- minSdk 24, Kotlin or Java.
- No runtime dependencies beyond the Kotlin standard library and AndroidX annotations.
- OkHttp is optional: `VisionaryInterceptor` uses the OkHttp your app already ships (4.x).

## Install

The library is not on Maven Central yet. Pick one of these.

**As a Gradle module.** Copy (or git-submodule) `sdks/android/visionary` into your project, then:

```kotlin
// settings.gradle.kts
include(":visionary")

// app/build.gradle.kts
dependencies {
    implementation(project(":visionary"))
}
```

**From your local Maven repository.**

```sh
cd sdks/android
./gradlew :visionary:publishReleasePublicationToMavenLocal
```

```kotlin
// settings.gradle.kts: repositories { mavenLocal(); google(); mavenCentral() }
dependencies {
    implementation("io.github.styropyr0:visionary-android:0.1.0")
}
```

**JitPack style.** JitPack builds from the repository root, so this needs a `jitpack.yml` there
that runs `cd sdks/android && ./gradlew :visionary:publishReleasePublicationToMavenLocal`. With
that in place and a tag built:

```kotlin
// settings.gradle.kts: repositories { maven("https://jitpack.io") }
dependencies {
    implementation("com.github.styropyr0.visionary:visionary-android:<tag>")
}
```

Building needs JDK 17+ and the Android SDK (platform 35); `./gradlew assembleRelease` produces
`visionary/build/outputs/aar/visionary-release.aar`.

## Quickstart

Create a client key on the Visionary server (Settings, or `VISIONARY_CLIENT_KEYS`), then start
the SDK in your `Application`:

```kotlin
class App : Application() {
    override fun onCreate() {
        super.onCreate()
        Visionary.init(
            this,
            VisionaryConfig(
                endpoint = "https://visionary.example.com",
                key = "pk_live_...",
                appVersion = BuildConfig.VERSION_NAME, // optional, defaults to versionName
                version = BuildConfig.GIT_SHA,          // optional, groups releases
                sampleRate = 1.0,                       // fraction of sessions that report
            ),
        )
    }
}
```

Then, anywhere:

```kotlin
Visionary.setUser("u_42")                       // null to clear
Visionary.track("checkout_started", value = 129.99, attrs = mapOf("plan" to "pro"))
Visionary.captureException(e)                   // a handled error
Visionary.trackScreen("TripDetails", 240.0)     // Compose destinations, fragments
Visionary.reportFullyDrawn(this)                // in an Activity, once real content is shown
```

API timings, with your OkHttp client:

```kotlin
val client = OkHttpClient.Builder()
    .addInterceptor(VisionaryInterceptor())
    .build()
```

Java works too: `Visionary.init(this, new VisionaryConfig(endpoint, key))`,
`Visionary.track("name")`, `Visionary.captureException(e)`.

Other settings on `VisionaryConfig`: `flushIntervalMs` (10 s), `maxQueueSize` (500),
`trackScreens` (true), `captureCrashes` (true), `gzip` (false; enable only if your server
accepts `Content-Encoding: gzip`), `debug` (false; logs to Logcat under `Visionary`).

## What is captured

Every request carries `app: "android"`, `app_version`, `version`, a random `session_id` per
process launch, `user_id` (when set), `device` (`Build.MODEL`, e.g. `Pixel 8`) and `os`
(`Android 16`).

| Event | When | Fields |
|---|---|---|
| `screen` | Each activity's first frame | `name`: activity class, `duration_ms`: from `onCreate` to the first `OnDrawListener` callback of its window |
| `custom` `fully_drawn` | `Visionary.reportFullyDrawn(activity)` | `duration_ms` since `onCreate`, `attrs.screen` |
| `api` | Every call through `VisionaryInterceptor` | `name`: `GET /api/trips/:id` (numeric, UUID, long hex and long token segments become `:id`), `url` without query string or credentials, `method`, `status` (0 when the call failed without a response), `duration_ms` until the response headers arrived |
| `error` | `captureException`, and uncaught exceptions | `name`: exception class, `message` (1000 chars), `stack` (16 KB), `attrs.handled` (1 / 0), `attrs.thread`, `attrs.screen` (the resumed activity) |
| `custom` | `track` | `value`, `duration_ms`, flat `attrs` (strings and numbers) |

Add the interceptor with `addInterceptor` to time a whole logical call (redirects and retries
included), or with `addNetworkInterceptor` to time each network request. Requests to the
Visionary ingest endpoint itself are never reported.

## How it behaves

- **Batching.** Events go to an in-memory queue and are sent by one background thread every
  `flushIntervalMs`, as soon as 100 are waiting, and when the app goes to the background (the
  last started activity stops; configuration changes don't count). A request holds at most 100
  events and stays under the server's 64 KB limit.
- **Offline.** When a request gets no response, the events stay queued and are written to
  `noBackupFilesDir/visionary/queue.jsonl`; they are also written when the app goes to the
  background. The next launch sends them. The queue holds `maxQueueSize` events; beyond that the
  oldest are dropped.
- **Server answers.** 2xx: done. 4xx (bad payload or key, 413, 429): the batch is dropped, since
  sending it again won't help. 5xx: the batch is retried once at the next flush, then dropped.
- **Delivery is at least once.** If the process dies between the server accepting a batch and
  the SDK removing it from disk, it is sent again on the next launch.
- **Crashes.** The SDK installs a default uncaught exception handler on top of the existing one.
  On a crash it writes the error and every queued event to disk synchronously, then calls the
  previous handler, so the app crashes exactly as before (and other crash reporters still run).
  The report goes out on the next launch, under the crashed session's id and user.
- **Sampling.** `sampleRate` is decided once per launch. Sessions outside the sample send only
  errors and crashes.
- **Safety.** Nothing is sent or recorded before `init`; no SDK call throws into the app; a
  failure on the background thread is logged (with `debug`) and the thread keeps running.

## Limits

- Only Java/Kotlin exceptions. Native (NDK) crashes and ANRs are not captured; use a full crash
  reporter if you need them, it can run next to this one.
- Screen timings cover activities. For Compose navigation or fragments, call `trackScreen`
  with your own measurement.
- `init` must run in `Application.onCreate` to see the first activity and count foreground
  activities correctly.
