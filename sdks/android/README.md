# Visionary for Android

Add this Kotlin library to your app and Visionary shows how the app behaves for real users:
screen load times, API call timings, handled errors, crashes and your own events. They appear
on the dashboard's **Frontend** page, next to your backend's requests.

- minSdk 24, Kotlin or Java.
- No runtime dependencies beyond the Kotlin standard library and AndroidX Annotation. OkHttp is
  optional: `VisionaryInterceptor` uses the OkHttp 4.x your app already ships.
- The SDK only talks to your Visionary server's client ingest endpoint
  (`POST /api/ingest/client`, see [`sdks/SPEC.md`](../SPEC.md), "Client ingest API").

## Quick start

Four steps, about 10 minutes. You need a running Visionary server (see the
[main README](../../Readme.md#get-started)).

**1. Add the dependency** from Maven Central:

```kotlin
// settings.gradle.kts: dependencyResolutionManagement { repositories { google(); mavenCentral() } }

// app/build.gradle.kts
dependencies {
    implementation("io.github.styropyr0:visionary-android:0.7.1")
}
```

**2. Create a client key.** In the dashboard, open **Settings → API keys**, create a key with
the **Client ingest** scope and copy it (it starts with `vis_` and is shown once). A client key
ships inside your app, so treat it as public: it can only send events.

**3. Start the SDK in `Application.onCreate`.**

```kotlin
class App : Application() {
    override fun onCreate() {
        super.onCreate()
        Visionary.init(this, VisionaryConfig(
            endpoint = "https://visionary.example.com",
            key = "vis_...",
        ))
    }
}
```

Register the class in `AndroidManifest.xml` if you don't have one yet:

```xml
<application android:name=".App" ...>
```

**4. Run the app and check the dashboard.** Open **Frontend** and pick **Android** in the
**App** menu. Within about 10 seconds (the flush interval), or as soon as you send the app to the
background, you see a session under **Sessions and users** and each activity under **Mobile
screens**. Nothing showing? See [Troubleshooting](#troubleshooting).

Activity screen timing and crash capture are on by default. Everything else is one or two lines
each.

## Install

### Maven Central (recommended)

```kotlin
dependencies {
    implementation("io.github.styropyr0:visionary-android:0.7.1")
}
```

Each release tag is published with the version being the tag without the `v`; see the
[releases](https://github.com/styropyr0/Visionary/releases).

### JitPack

Each release tag from `v0.3.0` is also built by JitPack, through the `jitpack.yml` at the
repository root. The first request for a new tag starts the build, which takes a couple of
minutes; the status is at https://jitpack.io/#styropyr0/Visionary.

```kotlin
// settings.gradle.kts
dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
        maven("https://jitpack.io")
    }
}

// app/build.gradle.kts
dependencies {
    implementation("com.github.styropyr0:Visionary:v0.7.1")
}
```

### As a Gradle module

Copy (or git-submodule) `sdks/android/visionary` into your project, then:

```kotlin
// settings.gradle.kts
include(":visionary")

// app/build.gradle.kts
dependencies {
    implementation(project(":visionary"))
}
```

### From your local Maven repository

```sh
cd sdks/android
./gradlew :visionary:publishReleasePublicationToMavenLocal
```

```kotlin
// settings.gradle.kts: repositories { mavenLocal(); google(); mavenCentral() }
dependencies {
    implementation("io.github.styropyr0:visionary-android:dev")
}
```

Building needs JDK 17+ and the Android SDK (platform 35). `./gradlew assembleRelease` produces
`visionary/build/outputs/aar/visionary-release.aar`.

### Permissions, ProGuard and R8

- **INTERNET** is declared in the library's manifest and merged into your app. Nothing to add.
- **R8 / ProGuard:** no rules to add. The library ships a consumer rule (`-dontwarn okhttp3.**`,
  so apps without OkHttp build cleanly) and uses no reflection. Stack traces from minified builds
  arrive obfuscated; there is no mapping upload, so retrace them locally with your
  `mapping.txt`.

## Integration recipes

Every call is a no-op until `Visionary.init` has run, and none of them throw. Call `init` from
`Application.onCreate`: it needs to see the first activity to time it and to tell when the app
goes to the background.

### Java

```java
public class App extends Application {
    @Override public void onCreate() {
        super.onCreate();
        VisionaryConfig config = new VisionaryConfig("https://visionary.example.com", "vis_...");
        config.setDebug(true);
        Visionary.init(this, config);
    }
}
```

`Visionary.track("name")`, `Visionary.captureException(e)` and the other calls have Java
overloads for the optional arguments.

### Screen timing: Activities

On by default (`trackScreens = true`). Nothing to add.

Records one `screen` event per activity: `name` is the activity's class name, `duration_ms` runs
from `onCreate` to the first draw of its window. The resumed activity is also attached to errors
as `attrs.screen`.

When the first frame is only a spinner, also report when the real content is shown:

```kotlin
// in the Activity, after the data has loaded and is on screen
Visionary.reportFullyDrawn(this)
```

Calls `Activity.reportFullyDrawn()` and records a `custom` event named `fully_drawn` with
`duration_ms` since `onCreate` and `attrs.screen`, once per activity.

### Screen timing: Jetpack Compose

Composable destinations inside one activity are not separate screens to the SDK. Time them
yourself and report with `trackScreen`, for example from first composition to the first frame:

```kotlin
@Composable
fun TrackScreen(name: String) {
    val start = remember { System.nanoTime() }
    LaunchedEffect(Unit) {
        withFrameNanos { }    // waits for the next frame
        Visionary.trackScreen(name, (System.nanoTime() - start) / 1e6)
    }
}

// in each destination
composable("trip/{id}") {
    TrackScreen("TripDetails")
    TripDetails()
}
```

Records a `screen` event each time the destination enters composition. To time until the data
is shown, call `trackScreen` after it has loaded instead.

### Screen timing: Fragments

```kotlin
class TripFragment : Fragment(R.layout.trip) {
    private var createdAt = 0L

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        createdAt = System.nanoTime()
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        view.doOnPreDraw {    // androidx.core:core-ktx
            Visionary.trackScreen("TripDetails", (System.nanoTime() - createdAt) / 1e6)
        }
    }
}
```

Records a `screen` event with your name and duration. Compose and fragment screens don't set
`attrs.screen` on errors (that comes from the resumed activity); pass
`mapOf("screen" to "TripDetails")` to `captureException` if you need it.

### Network calls: OkHttp

```kotlin
val client = OkHttpClient.Builder()
    .addInterceptor(VisionaryInterceptor())
    .build()
```

Records an `api` event per call: `method`, `status` (0 when the call failed without a
response) and `duration_ms` until the response headers arrived. With `addInterceptor` it times a
whole logical call, redirects and retries included. With `addNetworkInterceptor` it times each
network request instead.

### Network calls: Retrofit

Retrofit uses OkHttp, so give it a client with the interceptor:

```kotlin
val retrofit = Retrofit.Builder()
    .baseUrl("https://api.example.com/")
    .client(OkHttpClient.Builder().addInterceptor(VisionaryInterceptor()).build())
    .addConverterFactory(MoshiConverterFactory.create())
    .build()
```

The same works for Coil, Glide's OkHttp integration, Apollo and anything else that accepts an
`OkHttpClient`.

Calls made with `HttpURLConnection`, Ktor's non-OkHttp engines or other stacks are not captured.
Requests to the Visionary ingest endpoint itself are never reported.

### Handled errors

```kotlin
try {
    checkout.submit()
} catch (e: IOException) {
    Visionary.captureException(e, mapOf("step" to "payment"))
}
```

Records an `error` event: `name` is the exception class, `message` (1000 characters), `stack`
(16 KB), `attrs.handled = 1`, `attrs.thread`, `attrs.screen` and your attributes. Errors are
always sent, even in sessions outside `sampleRate`.

### Crashes

On by default (`captureCrashes = true`). Nothing to add.

The SDK installs a default uncaught exception handler on top of the existing one. On a crash it
writes the error and every queued event to disk synchronously, then calls the previous handler,
so the app crashes exactly as before and other crash reporters (Crashlytics, Sentry) still run.
The report is sent on the next launch as an `error` event with `attrs.handled = 0`, under the
crashed session's id and user.

Only Java and Kotlin exceptions are captured. Native (NDK) crashes and ANRs are not; use a full
crash reporter for those, next to this one.

To test it, throw from a debug-only button (`throw RuntimeException("test")`), then launch the
app again.

### Users: sign-in and sign-out

```kotlin
Visionary.setUser(user.id)   // after sign-in, and at launch if already signed in
Visionary.setUser(null)      // on sign-out
```

Attaches `user_id` to events recorded from then on (trimmed, at most 200 characters). Use a
stable internal id, not an email address. Crash reports keep the user of the session that
crashed.

### Custom events

```kotlin
Visionary.track("checkout_started", value = 129.99, attrs = mapOf("plan" to "pro", "items" to 3))
Visionary.track("search", durationMs = 312.0)
```

Records a `custom` event with `name`, optional `value`, optional `duration_ms` and flat `attrs`.
Attribute values should be strings or numbers; booleans become `"true"`/`"false"`, other objects
their `toString()`, and `null` values are skipped. The server keeps at most 32 attributes per
event, keys up to 64 characters and string values up to 256 characters.

### Logs

Send a line to Logs with this session and user:

```kotlin
Visionary.log("warn", "cart total mismatch", mapOf("cart" to cart.id))
```

Levels are `"debug"`, `"info"`, `"warn"` and `"error"`. Calls through the OkHttp interceptor also
carry the response's `X-Request-ID`, which links them to the server's request and its logs: open
the session under Frontend → Recent sessions.

### Sessions

There is nothing to call. Each process launch gets a random `session_id`; every event of that
launch carries it. Signing out does not start a new session. Sessions outside `sampleRate` send
only errors and crashes (the choice is made once per launch).

### Offline buffering and flush

Automatic. Events are sent every `flushIntervalMs` (10 s), as soon as 100 are waiting, and when
the app goes to the background (the last started activity stops; configuration changes don't
count). Without a network, events are kept on disk and sent on a later flush or the next launch.
To send now:

```kotlin
Visionary.flush()   // sends on the SDK's background thread; returns at once
```

### Release and version tagging

```kotlin
VisionaryConfig(
    endpoint = "https://visionary.example.com",
    key = "vis_...",
    appVersion = BuildConfig.VERSION_NAME,   // default: the package's versionName
    version = BuildConfig.GIT_SHA,           // default: none
)
```

`appVersion` is the user-facing version (the **By app version** breakdown on Frontend).
`version` is the build or commit releases are grouped by; it has no default. A short git hash
works well, for example from a `buildConfigField`:

```kotlin
// app/build.gradle.kts
android {
    buildFeatures { buildConfig = true }
    defaultConfig {
        val sha = providers.exec { commandLine("git", "rev-parse", "--short=12", "HEAD") }
            .standardOutput.asText.get().trim()
        buildConfigField("String", "GIT_SHA", "\"$sha\"")
    }
}
```

### Local development server

Point the SDK at your machine while developing:

- **Emulator:** `http://10.0.2.2:8090`. `localhost` inside the emulator is the emulator itself;
  `10.0.2.2` is your computer.
- **Real device on the same Wi-Fi:** your computer's LAN address, e.g.
  `http://192.168.1.20:8090`. Or run `adb reverse tcp:8090 tcp:8090` and use
  `http://localhost:8090`.

Android blocks plain `http` (cleartext) by default from API 28. Allow it for your dev host in
debug builds only. Create `app/src/debug/res/xml/network_security_config.xml`:

```xml
<?xml version="1.0" encoding="utf-8"?>
<network-security-config>
    <domain-config cleartextTrafficPermitted="true">
        <domain includeSubdomains="false">10.0.2.2</domain>
        <domain includeSubdomains="false">localhost</domain>
    </domain-config>
</network-security-config>
```

and reference it from `app/src/debug/AndroidManifest.xml`:

```xml
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <application android:networkSecurityConfig="@xml/network_security_config" />
</manifest>
```

Use HTTPS in release builds. A handy pattern is a `buildConfigField` for the endpoint per build
type.

### Debug logging

```kotlin
Visionary.init(this, VisionaryConfig(endpoint, key).apply { debug = true })
```

Logs what the SDK does to Logcat under the tag `Visionary`: the session id and URL at start,
each batch sent, and every rejection with its status code. Also logged without `debug`: a
warning if `init` itself fails.

## Configuration reference

`VisionaryConfig(endpoint, key, appVersion, version, sampleRate)`; the other options are
properties you set before calling `Visionary.init` (`.apply { ... }` in Kotlin, setters in Java).

| Option | Type | Default | What it does |
|---|---|---|---|
| `endpoint` | `String` | required | Your Visionary server, e.g. `https://visionary.example.com`. Events go to `<endpoint>/api/ingest/client` unless the URL already ends with that path. |
| `key` | `String` | required | A client key (see [Client keys](#client-keys)). Sent as `Authorization: Bearer <key>`. |
| `appVersion` | `String?` | the package's `versionName` | User-facing version. |
| `version` | `String?` | none | Build or commit that releases are grouped by. |
| `sampleRate` | `Double` | `1.0` | Fraction of launches (0.0 to 1.0) that send screens, API calls and custom events. Errors and crashes are always sent. |
| `flushIntervalMs` | `Long` | `10_000` | Milliseconds between sends (minimum 1000). |
| `maxQueueSize` | `Int` | `500` | Events kept in memory and on disk; the oldest are dropped beyond this. |
| `trackScreens` | `Boolean` | `true` | Time each activity from `onCreate` to its first frame. |
| `captureCrashes` | `Boolean` | `true` | Install an uncaught exception handler (the previous one still runs). |
| `gzip` | `Boolean` | `false` | Compress request bodies (`Content-Encoding: gzip`). The Visionary server accepts gzip; leave it off if a proxy in front of it doesn't. |
| `debug` | `Boolean` | `false` | Log SDK activity to Logcat under `Visionary`. |

### Public API

| Call | What it does |
|---|---|
| `Visionary.init(context, config)` | Starts the SDK. Main thread, from `Application.onCreate`; later calls are ignored. |
| `Visionary.setUser(id)` | Sets or clears (`null`) the user id. |
| `Visionary.track(name, value, durationMs, attrs)` | Custom event. |
| `Visionary.captureException(error, attrs)` | Handled exception. |
| `Visionary.trackScreen(name, durationMs, attrs)` | Screen timing you measured. |
| `Visionary.reportFullyDrawn(activity)` | `fully_drawn` event and `Activity.reportFullyDrawn()`. |
| `Visionary.flush()` | Send queued events now, in the background. |
| `VisionaryInterceptor()` | OkHttp interceptor that records every call. |

## Client keys

Two ways to get one, both in the dashboard (admins only):

- **Settings → API keys**, scope **Client ingest**. Each key has a name, an optional expiry, and
  can be revoked on its own. Recommended.
- **Settings → Server → Ingest → Client keys**: a comma-separated list of keys you choose
  yourself.

A **Server ingest** or **Read** key does not work here; the server answers 401.

## What is captured

Every batch carries `app: "android"`, `app_version`, `version`, `session_id`, `user_id` (when
set), `device` (`Build.MODEL`, e.g. `Pixel 8`) and `os` (e.g. `Android 16`).

| Event | Recorded by | Fields |
|---|---|---|
| `screen` | Each activity's first frame, `trackScreen` | `name` (activity class or your name), `duration_ms`, `attrs` |
| `custom` `fully_drawn` | `reportFullyDrawn(activity)` | `duration_ms` since `onCreate`, `attrs.screen` |
| `api` | `VisionaryInterceptor` | `name` like `GET /api/trips/:id`, `url`, `method`, `status` (0 when the call failed without a response), `duration_ms` |
| `error` | `captureException`, uncaught exceptions | `name` (exception class), `message` (1000 characters), `stack` (16 KB), `attrs.handled` (1 / 0), `attrs.thread`, `attrs.screen` (the resumed activity) |
| `custom` | `track` | `name`, `value`, `duration_ms`, flat `attrs` |

## Privacy

- **URLs:** the `url` field is scheme, host, port and path only: no query string, fragment or
  credentials. In the event `name`, numeric, UUID, long hex and long token path segments become
  `:id`; the `url` field keeps the real path, so avoid putting personal data in paths.
- **Bodies and headers** of your requests are never read or sent.
- **Device data:** `Build.MODEL` and the Android version only. No advertising id, no Android
  id. The session id is random per launch.
- **User id:** only what you pass to `setUser`.
- **Attributes and messages:** sent as you give them. Don't put tokens or personal data in
  `track` or `captureException` attributes, or in exception messages you control.
- **Storage:** pending events live in `noBackupFilesDir/visionary`, which is excluded from
  backups.
- The server may add a country from your proxy's or CDN's country header.

## How delivery works

- **Batching.** Events go to an in-memory queue and are sent by one background thread. A
  request holds at most 100 events and stays under the server's 64 KB limit.
- **Offline.** When a request gets no response, the events stay queued and are written to
  `noBackupFilesDir/visionary/queue.jsonl` (also when the app goes to the background). They are
  sent on a later flush or the next launch. At most `maxQueueSize` events are kept; the oldest go
  first. Events older than 24 hours are stored with a time 24 hours before they arrive.
- **Server answers.** 2xx: done. 4xx (bad payload or key, 413, 429): the batch is dropped,
  because sending it again won't help. 5xx (including 503 when the server's queue is full):
  retried once at the next flush, then dropped.
- **At least once.** If the process dies between the server accepting a batch and the SDK
  removing it from disk, it is sent again on the next launch.
- **Safety.** Nothing is recorded before `init`. No call throws into the app. A failure on the
  background thread is logged (with `debug`) and the thread keeps running.

## Troubleshooting

Turn on `debug = true` first and filter Logcat by the tag `Visionary`.

| Symptom | Cause | Fix |
|---|---|---|
| Nothing on Frontend, Logcat shows `offline; N events kept` | The server isn't reachable at `endpoint`. | Open the endpoint URL in the device's browser; the dashboard should load. Check the scheme, host and port (default `8090`). |
| Works against production, not against your computer | `localhost` in the emulator is the emulator. | Use `http://10.0.2.2:8090`, or `adb reverse` on a real device. |
| `offline` against an `http://` endpoint, `CLEARTEXT communication ... not permitted` in Logcat | Cleartext traffic is blocked. | Use HTTPS, or allow your dev host in a debug network security config (see [Local development server](#local-development-server)). |
| `server rejected N events with 401` | The key is wrong, revoked, expired, or not a client key. | Create a **Client ingest** key in **Settings → API keys** and paste it exactly. |
| `server rejected N events with 403` | The request carried an `Origin` header that isn't in **Allowed web origins**. | The SDK sends no `Origin`; check for a proxy adding one, or clear the list in **Settings → Server → Ingest**. |
| `server rejected N events with 429` | Rate limited. | Raise **Frontend events per IP per minute** or **per key per minute** in **Settings → Server → Ingest** (0 turns a limit off; takes effect after a server restart). Many test devices behind one IP hit the per-IP limit first. |
| `server rejected N events with 400`, `unsupported Content-Encoding` | `gzip = true` and a proxy strips or mangles it. | Turn `gzip` off. |
| `server rejected N events with 413` | A batch is over 64 KB (very large attributes). | Send fewer or shorter attributes. |
| Events arrive late | Batching. | Expect up to `flushIntervalMs` (10 s) plus the dashboard refresh. Lower it while testing, or call `Visionary.flush()`. |
| Only errors show up | The session is outside `sampleRate`. | Use `sampleRate = 1.0` while testing. |
| No screens | `init` runs after the first activity, outside `Application`, or `trackScreens = false`. | Call `init` in `Application.onCreate` and register the class in the manifest. For Compose and fragments use the recipes above. |
| No API calls | The HTTP client has no `VisionaryInterceptor`. | Add it to every `OkHttpClient` you build, including the one given to Retrofit. |
| No crash after a test crash | The app wasn't launched again. | Relaunch; the report is sent on the next launch. |
| No Logcat output at all | `init` was never called, or `debug` wasn't set on the config passed to it. | Call `Visionary.init` in `Application.onCreate` with `debug = true`. |
