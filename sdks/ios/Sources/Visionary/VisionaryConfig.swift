import Foundation

/// Settings for `Visionary.start(config:)`.
public struct VisionaryConfig {
    /// The Visionary server, e.g. `https://visionary.example.com`. Events go to
    /// `<endpoint>/api/ingest/client` unless the URL already ends with that path.
    public var endpoint: URL
    /// A public client key (`VISIONARY_CLIENT_KEYS` or one created in Settings).
    public var key: String
    /// The user-facing version; defaults to `CFBundleShortVersionString`.
    public var appVersion: String?
    /// The build or commit releases are grouped by; defaults to `CFBundleVersion`.
    public var version: String?
    /// Fraction of app launches (sessions) that report events, 0 to 1. Errors and crashes are always reported.
    public var sampleRate: Double

    /// How often queued events are sent while the app runs.
    public var flushInterval: TimeInterval = 10
    /// Events kept in memory and on disk while offline; the oldest are dropped beyond this.
    public var maxQueueSize = 500
    /// Record uncaught Objective-C exceptions and fatal signals, sent on the next launch.
    public var captureCrashes = true
    /// Time every view controller of the app automatically by swizzling `viewDidLoad` and
    /// `viewDidAppear(_:)`. Off by default; see `Visionary.screenDidLoad(_:)` for the manual API.
    public var swizzleScreens = false
    /// Where pending events and crash reports are kept; defaults to Application Support/Visionary.
    public var storageDirectory: URL?
    /// Print what the SDK does to the console.
    public var debug = false

    public init(endpoint: URL, key: String, appVersion: String? = nil, version: String? = nil, sampleRate: Double = 1) {
        self.endpoint = endpoint
        self.key = key
        self.appVersion = appVersion
        self.version = version
        self.sampleRate = sampleRate
    }
}
