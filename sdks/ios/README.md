# Visionary for iOS

A small Swift package that reports what a native iOS app experiences to a Visionary server:
screen load times, API call timings, handled errors, crashes and your own events. They show up
in Visionary's Frontend views next to your backend's `api_logs`.

The SDK talks to the server's client ingest endpoint (`POST /api/ingest/client`, see
[`sdks/SPEC.md`](../SPEC.md), "Client ingest API"). It never touches your database.

- iOS 14+, Swift 5.9+ (Xcode 15 or later). No dependencies.

## Install

Swift Package Manager, from the repository URL. The repository root has a `Package.swift` that
builds this folder, and release tags (`v0.4.0`, ...) are the package versions.

- **Xcode:** File > Add Package Dependencies, enter `https://github.com/styropyr0/Visionary.git`,
  choose "Up to Next Major Version" from `0.4.0`, and add the `Visionary` library to your app
  target.
- **Package.swift:**

```swift
dependencies: [
    .package(url: "https://github.com/styropyr0/Visionary.git", from: "0.4.0"),
],
targets: [
    .target(name: "App", dependencies: [.product(name: "Visionary", package: "Visionary")]),
]
```

SwiftPM clones the whole repository (the server, the dashboard and the other SDKs too), but
builds only `sdks/ios/Sources/Visionary`.

To work on the SDK itself, open `sdks/ios/Package.swift` (or use `.package(path:
"<checkout>/sdks/ios")`, with `package: "ios"` in the product dependency).

There is no CocoaPods pod: the CocoaPods trunk is being sunset (it is scheduled to become
read-only), so Swift Package Manager is the only supported way to install the SDK.

## Quickstart

Create a client key on the Visionary server (Settings, or `VISIONARY_CLIENT_KEYS`), then start
the SDK first thing at launch:

```swift
import Visionary

func application(_ application: UIApplication,
                 didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
    var config = VisionaryConfig(
        endpoint: URL(string: "https://visionary.example.com")!,
        key: "pk_live_...",
        version: "a1b2c3d4e5f6",   // optional; defaults to CFBundleVersion
        sampleRate: 1              // fraction of sessions that report
    )
    config.swizzleScreens = true   // optional, see "Screen timings"
    Visionary.start(config: config)
    return true
}
```

Then, anywhere:

```swift
Visionary.setUser("u_42")                                   // nil to clear
Visionary.track("checkout_started", value: 129.99, attributes: ["plan": "pro", "items": 3])
Visionary.captureError(error)                               // a handled error
Visionary.trackScreen("TripDetails", durationMs: 240)       // SwiftUI views, custom flows
Visionary.trackRequest(method: "GET", url: url, status: 200, durationMs: 85) // other HTTP stacks
```

Other settings on `VisionaryConfig`: `appVersion` (defaults to `CFBundleShortVersionString`),
`flushInterval` (10 s), `maxQueueSize` (500), `captureCrashes` (true), `storageDirectory`
(Application Support/Visionary, excluded from backups), `debug` (prints to the console).

### API timings

Two ways to capture URLSession calls:

1. **Task metrics (recommended).** Give your session `VisionarySessionDelegate`, or call
   `Visionary.record(metrics, for: task)` from your own delegate's
   `urlSession(_:task:didFinishCollecting:)`:

   ```swift
   let session = URLSession(configuration: .default, delegate: VisionarySessionDelegate(), delegateQueue: nil)
   ```

2. **`VisionaryURLProtocol` (automatic).** For sessions you don't control the delegate of:

   ```swift
   let config = URLSessionConfiguration.default
   config.protocolClasses = [VisionaryURLProtocol.self] + (config.protocolClasses ?? [])
   let session = URLSession(configuration: config)
   // or, for URLSession.shared: URLProtocol.registerClass(VisionaryURLProtocol.self)
   ```

| | Task metrics | URLProtocol |
|---|---|---|
| Setup | A delegate on each session | One line per configuration |
| Accuracy | Exact, from the loading system; adds `ttfb_ms`, `protocol` (h2, h3), `reused` connection, `redirects` | Wall clock around the re-issued request |
| Effect on requests | None | Each request is re-issued on an internal session: your session's delegate does not see auth challenges or certificate pinning for it, upload streams and caching follow the internal session, and each redirect is a separate API event |
| Background sessions | Works | Not supported by URLProtocol |
| `URLSession.shared` | No (it has no delegate) | Yes, via `registerClass` |

Prefer metrics for sessions you create; use the protocol for third-party code or
`URLSession.shared`, and leave it off sessions that rely on authentication challenges or
pinning. Requests to the Visionary endpoint itself are never reported.

### Screen timings

Manual (works for any controller):

```swift
override func viewDidLoad() {
    super.viewDidLoad()
    Visionary.screenDidLoad(self)
}

override func viewDidAppear(_ animated: Bool) {
    super.viewDidAppear(animated)
    Visionary.screenDidAppear(self)   // records once per controller instance
}
```

Automatic: set `config.swizzleScreens = true` and the SDK swaps in timing versions of
`viewDidLoad` and `viewDidAppear(_:)` for every `UIViewController`, recording only classes from
your app's main bundle (not UIKit's navigation or tab containers). It's off by default because
swizzling changes UIKit behaviour process-wide and can conflict with other libraries that do the
same. The duration runs from `viewDidLoad` to `viewDidAppear`, so it includes the presentation
animation of pushed or presented screens.

## What is captured

Every request carries `app: "ios"`, `app_version`, `version`, a random `session_id` per launch,
`user_id` (when set), `device` (the hardware identifier from `utsname`, e.g. `iPhone15,2`; the
simulated device on the simulator) and `os` (`UIDevice` name and version, e.g. `iOS 26.1`).

| Event | When | Fields |
|---|---|---|
| `screen` | `screenDidAppear`, swizzled controllers, `trackScreen` | `name`: controller class, `duration_ms` |
| `api` | Metrics delegate, `VisionaryURLProtocol`, `trackRequest` | `name`: `GET /api/trips/:id` (numeric, UUID, long hex and long token segments become `:id`), `url` without query, fragment or credentials, `method`, `status` (0 when there was no response), `duration_ms` |
| `error` | `captureError`, uncaught exceptions, fatal signals | `name`: the Swift error type (`MyApp.CheckoutError`), `NSError` domain and code, exception name or signal (`SIGSEGV`); `message`; `stack` (16 KB); `attrs.handled`, `attrs.thread`, `attrs.screen` |
| `custom` | `track` | `value`, `duration_ms`, flat `attributes` |

## How it behaves

- **Batching.** Events are queued in memory and sent from a background queue every
  `flushInterval`, as soon as 100 are waiting, and when the app enters the background (inside a
  background task, so the request can finish). A request holds at most 100 events and stays
  under the server's 64 KB limit.
- **Offline.** When a request gets no response, the events stay queued and are written to
  `storageDirectory/queue.jsonl`; they are also written on entering the background. The next
  launch sends them. At most `maxQueueSize` events are kept; the oldest go first.
- **Server answers.** 2xx: done. 4xx (bad payload or key, 413, 429): the batch is dropped.
  5xx: retried once at the next flush, then dropped.
- **Delivery is at least once**: a batch accepted just before the app is killed can be sent
  again on the next launch.
- **Sampling.** `sampleRate` is decided once per launch; sessions outside the sample send only
  errors and crashes.

## Crash capture, and its limits

This is a minimal crash recorder, not a replacement for Crashlytics, Sentry or PLCrashReporter.

- **Uncaught Objective-C exceptions** (`NSSetUncaughtExceptionHandler`): the exception name,
  reason and `callStackSymbols` are written to disk on the crashing thread, then the previously
  installed handler runs.
- **Fatal signals** (`SIGSEGV`, `SIGBUS`, `SIGILL`, `SIGTRAP`, `SIGFPE`, `SIGABRT`; this
  includes Swift runtime traps such as force-unwrapping `nil` or an index out of range): the
  handler may only use async-signal-safe calls, so it writes the signal number, the time and
  the raw frames from `backtrace_symbols_fd` to a file, then restores the previous handler so the
  process dies normally (and other crash reporters still receive the original fault).
  On the next launch the report becomes an `error` event attributed to the crashed session's id
  and user.

What it does not do:

- **No symbolication.** Release builds are stripped, so frames are mostly addresses and image
  names, without your function names. There is no dSYM upload or server-side symbolication.
- **Only the crashing thread**, no registers, no other threads' stacks, no binary image list.
- **Stack overflows** are caught on the main thread only (it gets an alternate signal stack);
  overflows on other threads may die without a report.
- **Not captured at all:** watchdog terminations (`0x8badf00d`), out-of-memory kills, crashes
  while the app is suspended, and anything the system kills with `SIGKILL`.
- Several signal-based crash reporters in one app can interfere; if you already use one, set
  `captureCrashes = false` and keep it.

For production crash triage use a full crash reporter; this gives you crash counts, types and a
rough location next to the rest of your Visionary data.
