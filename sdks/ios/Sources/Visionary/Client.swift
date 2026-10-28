import Foundation
#if canImport(UIKit)
import UIKit
#endif

/// The running SDK: builds events, queues them and sends them in batches on one serial queue.
/// Every entry point is cheap, thread-safe and never throws into the app.
final class Client {
    static let maxEvents = 100
    // The server refuses bodies over 64 KB; leave room for the envelope.
    static let maxEventBytes = 60_000
    private static let maxStack = 16 * 1024

    let config: VisionaryConfig
    let transport: Transport
    private let directory: URL
    private let queue: EventQueue
    private let worker = DispatchQueue(label: "io.github.styropyr0.visionary", qos: .utility)
    private var timer: DispatchSourceTimer?
    private let sampled: Bool
    private let device: String
    private let os: String
    private let stateLock = NSLock()
    private var meta: Meta
    private var screen: String?

    init(config: VisionaryConfig) {
        self.config = config
        directory = config.storageDirectory ?? Client.defaultDirectory()
        transport = Transport(endpoint: config.endpoint, key: config.key)
        queue = EventQueue(file: directory.appendingPathComponent("queue.jsonl"), capacity: config.maxQueueSize)
        sampled = Double.random(in: 0..<1) < config.sampleRate
        device = DeviceInfo.model()
        os = DeviceInfo.os()
        meta = Meta(
            sessionID: UUID().uuidString.lowercased(),
            userID: nil,
            appVersion: Client.nonEmpty(config.appVersion) ?? DeviceInfo.bundleString("CFBundleShortVersionString"),
            version: Client.nonEmpty(config.version) ?? DeviceInfo.bundleString("CFBundleVersion")
        )
    }

    func start() {
        let pending = config.captureCrashes ? CrashReporter.takePending(directory: directory) : nil
        if config.captureCrashes {
            CrashReporter.install(directory: directory)
            #if canImport(ObjectiveC)
            CrashReporter.installExceptionHandler { [weak self] exception in self?.crash(exception) }
            #endif
            CrashReporter.saveContext(currentMeta, directory: directory)
        }
        worker.async { [self] in
            queue.restore()
            if let pending { addSignalCrash(pending) }
        }
        let timer = DispatchSource.makeTimerSource(queue: worker)
        let every = max(1, config.flushInterval)
        timer.schedule(deadline: .now() + every, repeating: every)
        timer.setEventHandler { [weak self] in self?.flushNow() }
        timer.resume()
        self.timer = timer
        observeBackground()
        log("started session \(currentMeta.sessionID), sending to \(transport.url)")
    }

    var currentMeta: Meta { locked { meta } }

    var currentScreen: String? {
        get { locked { screen } }
        set { locked { screen = newValue } }
    }

    func setUser(_ id: String?) {
        let clean = id.map { Meta.clean(String($0.trimmingCharacters(in: .whitespaces).prefix(200))) }
        let updated: Meta = locked {
            meta.userID = (clean?.isEmpty ?? true) ? nil : clean
            return meta
        }
        if config.captureCrashes { CrashReporter.saveContext(updated, directory: directory) }
    }

    // MARK: Events

    func track(_ name: String, value: Double?, durationMs: Double?, attrs: [(String, AttributeValue)]) {
        let e = event("custom", name)
        e.number("value", value).number("duration_ms", Client.round(durationMs)).attrs("attrs", attrs)
        enqueue(e)
    }

    func logLine(_ level: String, message: String, attrs: [(String, AttributeValue)]) {
        let e = event("log", level)
        e.string("message", String(message.prefix(8192))).attrs("attrs", attrs)
        enqueue(e)
    }

    func screen(_ name: String, durationMs: Double, attrs: [(String, AttributeValue)] = []) {
        let e = event("screen", name)
        e.number("duration_ms", Client.round(durationMs)).attrs("attrs", attrs)
        enqueue(e)
    }

    func api(method: String, url: URL, status: Int, durationMs: Double, attrs: [(String, AttributeValue)] = []) {
        if url.path.hasSuffix(Transport.ingestPath) { return }
        let m = method.uppercased()
        var parts = URLComponents(url: url, resolvingAgainstBaseURL: false)
        // No query string or credentials: they often carry tokens and personal data.
        parts?.query = nil
        parts?.fragment = nil
        parts?.user = nil
        parts?.password = nil
        let e = event("api", "\(m) \(Paths.normalize(url.path))")
        e.string("url", parts?.string ?? url.absoluteString)
            .string("method", m)
            .number("status", Double(status))
            .number("duration_ms", Client.round(durationMs))
            .attrs("attrs", attrs)
        enqueue(e)
    }

    func error(_ error: Error, attrs: [(String, AttributeValue)]) {
        let (name, message) = Client.describe(error)
        let stack = Thread.callStackSymbols.dropFirst(2).joined(separator: "\n")
        queue.add(Record(meta: currentMeta, json: errorEvent(name: name, message: message, stack: stack, handled: true, attrs: attrs)))
        flushIfFull()
    }

    #if canImport(ObjectiveC)
    /// Runs on the crashing thread from the uncaught exception handler: record and write to disk now.
    private func crash(_ exception: NSException) {
        let stack = exception.callStackSymbols.joined(separator: "\n")
        let json = errorEvent(name: exception.name.rawValue, message: exception.reason ?? "", stack: stack, handled: false, attrs: [])
        queue.add(Record(meta: currentMeta, json: json))
        queue.persist()
    }
    #endif

    private func addSignalCrash(_ crash: PendingCrash) {
        let e = JSONWriter()
        e.string("ts", Client.timestamp(crash.date))
            .string("kind", "error")
            .string("name", crash.name)
            .string("message", "Fatal signal \(crash.signal) (\(crash.name))")
            .string("stack", String(crash.stack.prefix(Client.maxStack)))
            .attrs("attrs", [("handled", .number(0)), ("crash", .string("signal"))])
        queue.add(Record(meta: crash.meta ?? currentMeta, json: e.build()))
        queue.persist()
    }

    private func errorEvent(name: String, message: String, stack: String, handled: Bool, attrs: [(String, AttributeValue)]) -> String {
        var all: [(String, AttributeValue)] = [("handled", .number(handled ? 1 : 0)), ("thread", .string(Client.threadName()))]
        if let screen = currentScreen { all.append(("screen", .string(screen))) }
        all += attrs
        let e = event("error", name)
        e.string("message", String(message.prefix(1000)))
            .string("stack", String(stack.prefix(Client.maxStack)))
            .attrs("attrs", all)
        return e.build()
    }

    private func event(_ kind: String, _ name: String) -> JSONWriter {
        let e = JSONWriter()
        e.string("ts", Client.timestamp(Date())).string("kind", kind).string("name", String(name.prefix(300)))
        return e
    }

    private func enqueue(_ e: JSONWriter) {
        guard sampled else { return }
        queue.add(Record(meta: currentMeta, json: e.build()))
        flushIfFull()
    }

    private func flushIfFull() {
        if queue.count >= Client.maxEvents { flush() }
    }

    // MARK: Sending

    func flush() {
        worker.async { [weak self] in self?.flushNow() }
    }

    /// Worker queue only.
    private func flushNow() {
        while true {
            let batch = queue.nextBatch(maxEvents: Client.maxEvents, maxBytes: Client.maxEventBytes)
            if batch.isEmpty { return }
            switch transport.send(envelope(batch)) {
            case .sent:
                queue.remove(batch)
                log("sent \(batch.count) events")
            case .rejected(let code):
                queue.remove(batch)
                log("server rejected \(batch.count) events with \(code); dropped")
            case .serverError(let code):
                if batch.allSatisfy(\.retried) {
                    queue.remove(batch)
                    log("server answered \(code) again; dropped \(batch.count) events")
                } else {
                    batch.forEach { $0.retried = true }
                    log("server answered \(code); will retry once")
                }
                return
            case .offline:
                queue.persist()
                log("offline; \(queue.count) events kept")
                return
            }
        }
    }

    private func envelope(_ batch: [Record]) -> String {
        let m = batch[0].meta
        let w = JSONWriter()
        w.string("app", "ios")
            .string("app_version", m.appVersion)
            .string("version", m.version)
            .string("session_id", m.sessionID)
            .string("user_id", m.userID)
            .string("device", device)
            .string("os", os)
            .raw("events", "[" + batch.map(\.json).joined(separator: ",") + "]")
        return w.build()
    }

    /// The app went to the background: send what we can and keep the rest on disk.
    func enteredBackground(_ done: @escaping () -> Void = {}) {
        worker.async { [self] in
            flushNow()
            queue.persist()
            done()
        }
    }

    private func observeBackground() {
        #if canImport(UIKit) && !os(watchOS)
        NotificationCenter.default.addObserver(
            forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main
        ) { [weak self] _ in
            guard let self else { return }
            let app = UIApplication.shared
            var task = UIBackgroundTaskIdentifier.invalid
            task = app.beginBackgroundTask(withName: "Visionary flush") {
                app.endBackgroundTask(task)
                task = .invalid
            }
            self.enteredBackground {
                DispatchQueue.main.async {
                    if task != .invalid { app.endBackgroundTask(task) }
                    task = .invalid
                }
            }
        }
        #endif
    }

    // MARK: Helpers

    func log(_ message: @autoclosure () -> String) {
        if config.debug { print("[Visionary] \(message())") }
    }

    private func locked<T>(_ body: () -> T) -> T {
        stateLock.lock()
        defer { stateLock.unlock() }
        return body()
    }

    private static func defaultDirectory() -> URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        var dir = base.appendingPathComponent("Visionary", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        #if canImport(Darwin)
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try? dir.setResourceValues(values)
        #endif
        return dir
    }

    /// A Swift error's type (`MyApp.CheckoutError`), or an NSError's domain and code.
    static func describe(_ error: Error) -> (name: String, message: String) {
        let type = String(reflecting: Swift.type(of: error))
        if type == "NSError" || type.hasPrefix("__") || type.hasPrefix("NS") {
            let ns = error as NSError
            return ("\(ns.domain) \(ns.code)", ns.localizedDescription)
        }
        return (type, String(describing: error))
    }

    private static func threadName() -> String {
        if Thread.isMainThread { return "main" }
        let name = Thread.current.name ?? ""
        return name.isEmpty ? "background" : name
    }

    private static let formatLock = NSLock()
    private static let formatter: DateFormatter = {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "UTC")
        f.dateFormat = "yyyy-MM-dd'T'HH:mm:ss.SSS'Z'"
        return f
    }()

    static func timestamp(_ date: Date) -> String {
        formatLock.lock()
        defer { formatLock.unlock() }
        return formatter.string(from: date)
    }

    static func round(_ ms: Double?) -> Double? {
        ms.map { ($0 * 1000).rounded() / 1000 }
    }

    private static func nonEmpty(_ s: String?) -> String? {
        guard let s, !s.trimmingCharacters(in: .whitespaces).isEmpty else { return nil }
        return s
    }
}
