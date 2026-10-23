# Visionary for iOS

Add this Swift package to your app and Visionary shows how the app behaves for real users:
screen load times, API call timings, handled errors, crashes and your own events. They appear
on the dashboard's **Frontend** page, next to your backend's requests.

- iOS 14+ (the package also builds for macOS 11+, without screen timing), Swift 5.9+, Xcode 15
  or later.
- No dependencies. The SDK only talks to your Visionary server's client ingest endpoint
  (`POST /api/ingest/client`, see [`sdks/SPEC.md`](../SPEC.md), "Client ingest API").

## Quick start

Four steps, about 10 minutes. You need a running Visionary server (see the
[main README](../../Readme.md#get-started)).

**1. Add the package.** In Xcode: **File → Add Package Dependencies**, enter
`https://github.com/styropyr0/Visionary.git`, choose **Up to Next Major Version** from `0.7.1`,
and add the `Visionary` library to your app target.

**2. Create a client key.** In the dashboard, open **Settings → API keys**, create a key with
the **Client ingest** scope and copy it (it starts with `vis_` and is shown once). A client key
ships inside your app, so treat it as public: it can only send events.

**3. Start the SDK at launch.** Call `Visionary.start` once, on the main thread, before anything
else.

SwiftUI:

```swift
import SwiftUI
import Visionary

@main
struct MyApp: App {
    init() {
        Visionary.start(config: VisionaryConfig(
            endpoint: URL(string: "https://visionary.example.com")!,
            key: "vis_..."
        ))
    }

    var body: some Scene {
        WindowGroup { ContentView() }
    }
}
```

UIKit:

```swift
import UIKit
import Visionary

@main
class AppDelegate: UIResponder, UIApplicationDelegate {
    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
        Visionary.start(config: VisionaryConfig(
            endpoint: URL(string: "https://visionary.example.com")!,
            key: "vis_..."
        ))
        return true
    }
}
```

**4. Run the app and check the dashboard.** Open **Frontend** and pick **iOS** in the **App**
menu. Within about 10 seconds (the flush interval), or as soon as you send the app to the
background, you see a session under **Sessions and users**. Screens, API calls and errors appear
once you add the recipes below. Nothing showing? See [Troubleshooting](#troubleshooting).

Crash capture is on by default. Everything else is one or two lines each.

## Install

### Swift Package Manager (Xcode)

**File → Add Package Dependencies**, URL `https://github.com/styropyr0/Visionary.git`,
**Up to Next Major Version** from `0.7.1`, library `Visionary`.

### Swift Package Manager (Package.swift)

```swift
dependencies: [
    .package(url: "https://github.com/styropyr0/Visionary.git", from: "0.7.1"),
],
targets: [
    .target(name: "App", dependencies: [.product(name: "Visionary", package: "Visionary")]),
]
```

Release tags (`v0.7.1`, ...) are the package versions. The `Package.swift` at the repository
root builds only `sdks/ios/Sources/Visionary`, but SwiftPM clones the whole repository (server,
dashboard and the other SDKs too), so the first resolve takes a little longer.

### Local checkout

To work on the SDK itself, open `sdks/ios/Package.swift`, or depend on the folder:
`.package(path: "<checkout>/sdks/ios")` with `.product(name: "Visionary", package: "ios")`.

### CocoaPods

Not supported. The CocoaPods trunk is being sunset (it is scheduled to become read-only), so
Swift Package Manager is the only way to install the SDK.

## Integration recipes

Every call is a no-op until `Visionary.start` has run, and none of them throw.

### Screen timing: UIKit, automatic

Use it when your screens are `UIViewController`s and you want every one timed without code in
each.

```swift
var config = VisionaryConfig(endpoint: url, key: "vis_...")
config.swizzleScreens = true
Visionary.start(config: config)
```

Records one `screen` event per controller instance: `name` is the class name, `duration_ms`
runs from `viewDidLoad` to `viewDidAppear(_:)` (so it includes the push or present animation).
Only classes from your app's main bundle are recorded, not UIKit's navigation or tab containers.
It is off by default because swizzling changes `UIViewController` process-wide and can clash
with other libraries that swizzle the same methods.

### Screen timing: UIKit, manual

Use it when you don't want swizzling, or want only some screens, or a different name.

```swift
override func viewDidLoad() {
    super.viewDidLoad()
    Visionary.screenDidLoad(self)
}

override func viewDidAppear(_ animated: Bool) {
    super.viewDidAppear(animated)
    Visionary.screenDidAppear(self)                 // or name: "TripDetails"
}
```

Records the same `screen` event as the automatic mode, once per controller instance (returning
to the screen is not counted again). `screenDidAppear` also sets the current screen, which is
attached to errors as `attrs.screen`.

### Screen timing: SwiftUI

SwiftUI views have no load/appear pair the SDK can hook, so measure what matters to you and
report it with `trackScreen`. For example, the time until a view's data is on screen:

```swift
struct TripDetailsView: View {
    @StateObject var model: TripModel

    var body: some View {
        TripContent(model: model)
            .task {                                  // iOS 15+; use onAppear on iOS 14
                let start = Date()
                await model.load()
                Visionary.trackScreen("TripDetails", durationMs: Date().timeIntervalSince(start) * 1000)
            }
    }
}
```

Records a `screen` event with your name and duration, plus any `attributes` you pass. It does
not set the current screen for errors; pass `attributes: ["screen": "TripDetails"]` to
`captureError` if you want that.

### Network calls: URLSession with a delegate (recommended)

Use it for sessions you create yourself.

```swift
let session = URLSession(configuration: .default,
                         delegate: VisionarySessionDelegate(),
                         delegateQueue: nil)
```

Already have a delegate? Subclass `VisionarySessionDelegate` (call `super` if you override
`urlSession(_:task:didFinishCollecting:)`), or forward the metrics from your own delegate:

```swift
func urlSession(_ session: URLSession, task: URLSessionTask, didFinishCollecting metrics: URLSessionTaskMetrics) {
    Visionary.record(metrics, for: task)
}
```

Records an `api` event per task with exact timings from the loading system, plus `ttfb_ms`,
`protocol` (`h2`, `h3`), `reused` (connection reuse) and `redirects`. Works for background
sessions. It does not change how requests run.

### Network calls: URLProtocol (automatic)

Use it for `URLSession.shared` and for sessions inside third-party code whose delegate you
don't control.

```swift
// One session configuration:
let config = URLSessionConfiguration.default
config.protocolClasses = [VisionaryURLProtocol.self] + (config.protocolClasses ?? [])
let session = URLSession(configuration: config)

// URLSession.shared:
URLProtocol.registerClass(VisionaryURLProtocol.self)
```

Records an `api` event per request, timed by wall clock. Each request is re-issued on an
internal session, so:

- your session's delegate does not see authentication challenges or certificate pinning for
  it; leave the protocol off sessions that rely on those;
- upload streams and caching follow the internal session;
- each redirect is a separate `api` event;
- background sessions are not supported.

| | Delegate / `record` | `VisionaryURLProtocol` |
|---|---|---|
| Setup | A delegate on each session | One line per configuration |
| Accuracy | Exact; adds `ttfb_ms`, `protocol`, `reused`, `redirects` | Wall clock around the re-issued request |
| Effect on requests | None | Re-issued on an internal session (see above) |
| Background sessions | Yes | No |
| `URLSession.shared` | No (it has no delegate) | Yes, with `registerClass` |

### Network calls: other HTTP stacks

Use it for anything that isn't URLSession (gRPC, WebSocket handshakes, a custom stack).

```swift
Visionary.trackRequest(method: "GET", url: url, status: 200, durationMs: 85)  // status 0: no response
```

Requests to the Visionary ingest endpoint itself are never reported, whichever way you capture.

### Handled errors

Use it in `catch` blocks and failure paths you want to see.

```swift
do {
    try await checkout.submit()
} catch {
    Visionary.captureError(error, attributes: ["step": "payment"])
}
```

Records an `error` event: `name` is the Swift error type (`MyApp.CheckoutError`) or, for an
`NSError`, its domain and code (`NSURLErrorDomain -1001`); `message`; the current call `stack`
(16 KB); `attrs.handled = 1`, `attrs.thread`, `attrs.screen` (from UIKit screen timing) and your
attributes. Errors are always sent, even in sessions outside `sampleRate`.

### Crashes

On by default (`captureCrashes = true`). Nothing to add.

- **Uncaught Objective-C exceptions:** name, reason and `callStackSymbols` are written to disk on
  the crashing thread, then the previously installed handler runs.
- **Fatal signals** (`SIGSEGV`, `SIGBUS`, `SIGILL`, `SIGTRAP`, `SIGFPE`, `SIGABRT`; this includes
  Swift traps such as force-unwrapping `nil` or an index out of range): the handler writes the
  signal, the time and the raw frames from `backtrace_symbols_fd`, then restores the previous
  handler so the process dies normally and other crash reporters still see the fault.

The report is sent on the next launch as an `error` event with `attrs.handled = 0`, under the
crashed session's id and user. See [Crash capture limits](#crash-capture-limits) before you rely
on it.

To test it, add a debug-only button that calls `fatalError()`, tap it with the debugger
detached (the debugger catches the signal first), then launch the app again.

### Users: sign-in and sign-out

```swift
Visionary.setUser(user.id)   // after sign-in, and at launch if already signed in
Visionary.setUser(nil)       // on sign-out
```

Attaches `user_id` to events recorded from then on (trimmed, at most 200 characters). Use a
stable internal id, not an email address. Crash reports keep the user of the session that
crashed.

### Custom events

Use them for product actions and your own timings.

```swift
Visionary.track("checkout_started", value: 129.99, attributes: ["plan": "pro", "items": 3])
Visionary.track("search", durationMs: 312)
```

Records a `custom` event with `name`, optional `value`, optional `duration_ms` and flat
`attributes`. Attribute values are strings or numbers (`AttributeValue`; string, integer and
float literals work directly). The server keeps at most 32 attributes per event, keys up to 64
characters and string values up to 256 characters.

### Sessions

There is nothing to call. Each app launch gets a random `session_id`; every event of that launch
carries it. Signing out does not start a new session. Sessions outside `sampleRate` send only
errors and crashes (the choice is made once per launch).

### Offline buffering and flush

Automatic. Events are sent every `flushInterval` (10 s), as soon as 100 are waiting, and when the
app enters the background (inside a background task, so the request can finish). Without a
network, events are kept on disk and sent on a later flush or the next launch. To send now, for
example before a sign-out that clears local data:

```swift
Visionary.flush()   // sends in the background; returns at once
```

### Release and version tagging

```swift
VisionaryConfig(endpoint: url, key: "vis_...",
                appVersion: "2.4.0",          // default: CFBundleShortVersionString
                version: "a1b2c3d4e5f6")      // default: CFBundleVersion
```

`appVersion` is the user-facing version (the **By app version** breakdown on Frontend).
`version` is the build or commit releases are grouped by; a short git hash works well. Leave
both out to use the bundle's values.

### Local development server

Point the SDK at your machine while developing:

- **Simulator:** `http://localhost:8090` reaches the Mac running the simulator.
- **Real device:** use the Mac's LAN address, e.g. `http://192.168.1.20:8090`. iOS asks the user
  for Local Network access the first time; add `NSLocalNetworkUsageDescription` to Info.plist.

App Transport Security blocks plain `http` to most hosts. For a debug build, allow local
networking in Info.plist:

```xml
<key>NSAppTransportSecurity</key>
<dict>
    <key>NSAllowsLocalNetworking</key>
    <true/>
</dict>
```

Use HTTPS in release builds. No other Info.plist keys or entitlements are needed.

### Debug logging

```swift
config.debug = true
```

Prints what the SDK does to the Xcode console, prefixed `[Visionary]`: the session id and URL
at start, each batch sent, and every rejection with its status code.

## Configuration reference

`VisionaryConfig(endpoint:key:appVersion:version:sampleRate:)`; the other options are
properties you set before calling `Visionary.start(config:)`.

| Option | Type | Default | What it does |
|---|---|---|---|
| `endpoint` | `URL` | required | Your Visionary server, e.g. `https://visionary.example.com`. Events go to `<endpoint>/api/ingest/client` unless the URL already ends with that path. |
| `key` | `String` | required | A client key (see [Client keys](#client-keys)). Sent as `Authorization: Bearer <key>`. |
| `appVersion` | `String?` | `CFBundleShortVersionString` | User-facing version. |
| `version` | `String?` | `CFBundleVersion` | Build or commit that releases are grouped by. |
| `sampleRate` | `Double` | `1` | Fraction of launches (0 to 1) that send screens, API calls and custom events. Errors and crashes are always sent. |
| `flushInterval` | `TimeInterval` | `10` | Seconds between sends while the app runs (minimum 1). |
| `maxQueueSize` | `Int` | `500` | Events kept in memory and on disk; the oldest are dropped beyond this. |
| `captureCrashes` | `Bool` | `true` | Record uncaught exceptions and fatal signals, sent on the next launch. |
| `swizzleScreens` | `Bool` | `false` | Time every view controller of the app automatically. |
| `storageDirectory` | `URL?` | Application Support/Visionary (excluded from backups) | Where pending events and crash reports are kept. |
| `debug` | `Bool` | `false` | Print SDK activity to the console. |

### Public API

| Call | What it does |
|---|---|
| `Visionary.start(config:)` | Starts the SDK. Main thread; later calls are ignored. |
| `Visionary.setUser(_:)` | Sets or clears (`nil`) the user id. |
| `Visionary.track(_:value:durationMs:attributes:)` | Custom event. |
| `Visionary.captureError(_:attributes:)` | Handled error with the current stack. |
| `Visionary.trackScreen(_:durationMs:attributes:)` | Screen timing you measured. |
| `Visionary.screenDidLoad(_:)`, `Visionary.screenDidAppear(_:name:)` | Manual UIKit screen timing. |
| `Visionary.record(_:for:)` | API event from `URLSessionTaskMetrics`. |
| `Visionary.trackRequest(method:url:status:durationMs:)` | API event from another HTTP stack. |
| `Visionary.flush()` | Send queued events now, in the background. |
| `VisionarySessionDelegate` | Session delegate that records every task's metrics. |
| `VisionaryURLProtocol` | URLProtocol that records every request of a session. |

## Client keys

Two ways to get one, both in the dashboard (admins only):

- **Settings → API keys**, scope **Client ingest**. Each key has a name, an optional expiry, and
  can be revoked on its own. Recommended.
- **Settings → Server → Ingest → Client keys**: a comma-separated list of keys you choose
  yourself.

A **Server ingest** or **Read** key does not work here; the server answers 401.

## What is captured

Every batch carries `app: "ios"`, `app_version`, `version`, `session_id`, `user_id` (when set),
`device` (the hardware identifier from `utsname`, e.g. `iPhone15,2`; the simulated device on the
simulator) and `os` (`UIDevice` name and version, e.g. `iOS 26.1`).

| Event | Recorded by | Fields |
|---|---|---|
| `screen` | `screenDidAppear`, swizzled controllers, `trackScreen` | `name` (controller class or your name), `duration_ms`, `attrs` |
| `api` | `VisionarySessionDelegate` / `record`, `VisionaryURLProtocol`, `trackRequest` | `name` like `GET /api/trips/:id`, `url`, `method`, `status` (0 when there was no response), `duration_ms`; metrics add `attrs.ttfb_ms`, `protocol`, `reused`, `redirects` |
| `error` | `captureError`, uncaught exceptions, fatal signals | `name` (error type, `NSError` domain and code, exception name or signal such as `SIGSEGV`), `message` (1000 characters), `stack` (16 KB), `attrs.handled`, `attrs.thread`, `attrs.screen`; signal crashes add `attrs.crash = "signal"` |
| `custom` | `track` | `name`, `value`, `duration_ms`, flat `attrs` |

## Privacy

- **URLs:** the query string, fragment, user name and password are removed before an `api`
  event is recorded. In the event `name`, numeric, UUID, long hex and long token path segments
  become `:id`; the `url` field keeps the real path, so avoid putting personal data in paths.
- **Bodies and headers** of your requests are never read or sent.
- **Device data:** hardware model and OS version only. No advertising id, no device id. The
  session id is random per launch.
- **User id:** only what you pass to `setUser`.
- **Attributes and messages:** sent as you give them. Don't put tokens or personal data in
  `track` or `captureError` attributes, or in error messages you control.
- The server may add a country from your proxy's or CDN's country header.

## How delivery works

- **Batching.** Events are queued in memory and sent from a background queue. A request holds
  at most 100 events and stays under the server's 64 KB limit.
- **Offline.** When a request gets no response, the events stay queued and are written to
  `storageDirectory/queue.jsonl` (also on entering the background). They are sent on a later
  flush or the next launch. At most `maxQueueSize` events are kept; the oldest go first. Events
  older than 24 hours are stored with a time 24 hours before they arrive.
- **Server answers.** 2xx: done. 4xx (bad payload or key, 413, 429): the batch is dropped,
  because sending it again won't help. 5xx (including 503 when the server's queue is full):
  retried once at the next flush, then dropped.
- **At least once.** A batch accepted just before the app is killed can be sent again on the
  next launch.
- **Safety.** Nothing is recorded before `start`. No call throws into the app.

## Crash capture limits

This is a minimal crash recorder, not a replacement for Crashlytics, Sentry or PLCrashReporter.
It gives you crash counts, types and a rough location next to the rest of your Visionary data.

- **No symbolication.** Release builds are stripped, so frames are mostly addresses and image
  names. There is no dSYM upload or server-side symbolication.
- **Only the crashing thread**: no registers, no other threads, no binary image list.
- **Stack overflows** are caught on the main thread only (it gets an alternate signal stack).
- **Not captured:** watchdog terminations (`0x8badf00d`), out-of-memory kills, crashes while
  suspended, and anything killed with `SIGKILL`.
- Several signal-based crash reporters in one app can interfere. If you already use one, set
  `captureCrashes = false` and keep it.

## Troubleshooting

Turn on `config.debug = true` first; most problems show up in the console.

| Symptom | Cause | Fix |
|---|---|---|
| Nothing on Frontend, console shows `offline; N events kept` | The server isn't reachable at `endpoint`. | Open the endpoint URL in Safari on the same device; the dashboard should load. Check the scheme, host and port (default `8090`). |
| Nothing on Frontend, request fails with an ATS error | App Transport Security blocks plain `http`. | Use HTTPS, or add `NSAllowsLocalNetworking` for debug builds (see [Local development server](#local-development-server)). |
| Works on the simulator, not on a phone | `localhost` on a phone is the phone itself. | Use the Mac's LAN address and allow Local Network access. |
| `server rejected N events with 401` | The key is wrong, revoked, expired, or not a client key. | Create a **Client ingest** key in **Settings → API keys** and paste it exactly. |
| `server rejected N events with 403` | The request carried an `Origin` header that isn't in **Allowed web origins**. | Native apps don't send `Origin`; check for a proxy adding one, or clear the list in **Settings → Server → Ingest**. |
| `server rejected N events with 429` | Rate limited. | Raise **Frontend events per IP per minute** or **per key per minute** in **Settings → Server → Ingest** (0 turns a limit off; takes effect after a server restart). Many test devices behind one IP hit the per-IP limit first. |
| `server rejected N events with 413` | A batch is over 64 KB (very large attributes). | Send fewer or shorter attributes. |
| Events arrive late | Batching. | Expect up to `flushInterval` (10 s) plus the dashboard refresh. Lower `flushInterval` while testing, or call `Visionary.flush()`. |
| Only errors show up | The session is outside `sampleRate`. | Use `sampleRate: 1` while testing. |
| No screens | No screen timing is set up. | Set `swizzleScreens = true`, or use the manual or SwiftUI recipe. |
| No API calls | The session has neither the delegate nor the protocol. | Add one of the network recipes; `URLSession.shared` needs `URLProtocol.registerClass`. |
| No crash after a test crash | The debugger caught it, or the app wasn't launched again. | Run without the debugger attached, then relaunch; the report is sent on the next launch. |
| No console output at all | `start` was never called, or `debug` was set after it. | Call `Visionary.start` first in `App.init` or `didFinishLaunching`. |
