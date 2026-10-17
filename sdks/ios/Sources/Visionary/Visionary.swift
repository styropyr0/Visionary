import Foundation

/// Visionary for iOS: screen timings, API timings, errors, crashes and custom events, sent in
/// batches to a Visionary server. Call `start(config:)` once, early in
/// `application(_:didFinishLaunchingWithOptions:)`; every other call is a no-op until then.
public enum Visionary {
    private static let lock = NSLock()
    private static var _client: Client?

    static var client: Client? {
        lock.lock()
        defer { lock.unlock() }
        return _client
    }

    /// Starts collection. Call it from the main thread; later calls are ignored.
    public static func start(config: VisionaryConfig) {
        lock.lock()
        guard _client == nil else {
            lock.unlock()
            return
        }
        let client = Client(config: config)
        _client = client
        lock.unlock()
        client.start()
        #if canImport(UIKit) && !os(watchOS)
        if config.swizzleScreens { ScreenTiming.swizzle() }
        #endif
    }

    /// Attaches a user id to events recorded from now on; `nil` clears it.
    public static func setUser(_ id: String?) {
        client?.setUser(id)
    }

    /// Records a custom event.
    public static func track(
        _ name: String,
        value: Double? = nil,
        durationMs: Double? = nil,
        attributes: [String: AttributeValue] = [:]
    ) {
        client?.track(name, value: value, durationMs: durationMs, attrs: sorted(attributes))
    }

    /// Records a handled error (`attrs.handled = 1`) with the current call stack.
    public static func captureError(_ error: Error, attributes: [String: AttributeValue] = [:]) {
        client?.error(error, attrs: sorted(attributes))
    }

    /// Records a screen timing yourself, e.g. for SwiftUI views.
    public static func trackScreen(_ name: String, durationMs: Double, attributes: [String: AttributeValue] = [:]) {
        client?.screen(name, durationMs: durationMs, attrs: sorted(attributes))
    }

    /// Records an API call made with something other than URLSession. `status` is 0 when the call
    /// failed without a response.
    public static func trackRequest(method: String, url: URL, status: Int, durationMs: Double) {
        client?.api(method: method, url: url, status: status, durationMs: durationMs)
    }

    /// Sends queued events now, in the background.
    public static func flush() {
        client?.flush()
    }

    private static func sorted(_ attrs: [String: AttributeValue]) -> [(String, AttributeValue)] {
        attrs.sorted { $0.key < $1.key }.map { ($0.key, $0.value) }
    }
}
