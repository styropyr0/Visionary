import Foundation
#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

/// A fatal signal recorded by a previous launch.
struct PendingCrash {
    let date: Date
    let signal: Int32
    let stack: String
    let meta: Meta?

    var name: String { CrashReporter.signalName(signal) }
}

/// Minimal crash capture. Uncaught Objective-C exceptions are recorded with Foundation (the process
/// is still healthy then); fatal signals can only use async-signal-safe calls, so the handler
/// writes the signal number, the time and raw backtrace symbols to a pre-chosen file and nothing else.
/// The client turns that file into an `error` event on the next launch.
enum CrashReporter {
    static let signals: [Int32] = [SIGABRT, SIGBUS, SIGFPE, SIGILL, SIGSEGV, SIGTRAP]
    private static let signalFile = "crash-signal.txt"
    private static let contextFile = "crash-context.txt"

    static func install(directory: URL) {
        guard signalPath == nil else { return }
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        signalPath = strdup(directory.appendingPathComponent(signalFile).path)
        frames = .allocate(capacity: Int(maxFrames))
        resolveBacktrace()
        installAlternateStack()
        let previous = UnsafeMutablePointer<sigaction>.allocate(capacity: 65)
        previous.initialize(repeating: sigaction(), count: 65)
        previousActions = previous
        for sig in signals {
            var action = sigaction()
            #if canImport(Darwin)
            action.__sigaction_u.__sa_sigaction = visionarySignalHandler
            #else
            action.__sigaction_handler.sa_sigaction = visionarySignalHandler
            #endif
            action.sa_flags = Int32(SA_SIGINFO | SA_ONSTACK)
            sigemptyset(&action.sa_mask)
            sigaction(sig, &action, previous + Int(sig))
        }
    }

    /// Gives the handler its own stack on the installing (main) thread, so a stack overflow there
    /// can still be recorded.
    private static func installAlternateStack() {
        let size = 64 * 1024
        var stack = stack_t()
        stack.ss_sp = UnsafeMutableRawPointer.allocate(byteCount: size, alignment: 16)
        stack.ss_size = size
        stack.ss_flags = 0
        sigaltstack(&stack, nil)
    }

    #if canImport(ObjectiveC)
    /// Uncaught Objective-C exceptions (and Swift code calling into them) reach `callback` on the
    /// crashing thread, then the previously installed handler.
    static func installExceptionHandler(_ callback: @escaping (NSException) -> Void) {
        guard exceptionCallback == nil else { return }
        exceptionCallback = callback
        previousExceptionHandler = NSGetUncaughtExceptionHandler()
        NSSetUncaughtExceptionHandler(visionaryExceptionHandler)
    }
    #endif

    /// Remembers the envelope fields so a signal report can be attributed on the next launch.
    static func saveContext(_ meta: Meta, directory: URL) {
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try? Data(meta.line.utf8).write(to: directory.appendingPathComponent(contextFile), options: .atomic)
    }

    /// The signal report left by the previous launch, if any, removed from disk.
    static func takePending(directory: URL) -> PendingCrash? {
        let file = directory.appendingPathComponent(signalFile)
        defer { try? FileManager.default.removeItem(at: file) }
        guard let text = try? String(contentsOf: file, encoding: .utf8) else { return nil }
        var sig: Int32 = 0
        var date = (try? FileManager.default.attributesOfItem(atPath: file.path)[.modificationDate] as? Date) ?? Date()
        var stack: [Substring] = []
        for line in text.split(separator: "\n") {
            if line.hasPrefix("signal "), let n = Int32(line.dropFirst(7)) {
                sig = n
            } else if line.hasPrefix("time "), let t = TimeInterval(line.dropFirst(5)) {
                date = Date(timeIntervalSince1970: t)
            } else {
                stack.append(line)
            }
        }
        guard sig != 0 else { return nil }
        let context = try? String(contentsOf: directory.appendingPathComponent(contextFile), encoding: .utf8)
        let meta = context.flatMap { Meta(fields: $0.split(separator: "\t", omittingEmptySubsequences: false)) }
        return PendingCrash(date: date, signal: sig, stack: stack.joined(separator: "\n"), meta: meta)
    }

    static func signalName(_ sig: Int32) -> String {
        switch sig {
        case SIGABRT: return "SIGABRT"
        case SIGBUS: return "SIGBUS"
        case SIGFPE: return "SIGFPE"
        case SIGILL: return "SIGILL"
        case SIGSEGV: return "SIGSEGV"
        case SIGTRAP: return "SIGTRAP"
        default: return "SIG\(sig)"
        }
    }

    // execinfo is not part of Glibc's module map, so look the functions up the same way everywhere.
    private static func resolveBacktrace() {
        guard let handle = dlopen(nil, RTLD_NOW) else { return }
        if let f = dlsym(handle, "backtrace") { backtraceFn = unsafeBitCast(f, to: BacktraceFn.self) }
        if let f = dlsym(handle, "backtrace_symbols_fd") { symbolsFn = unsafeBitCast(f, to: SymbolsFn.self) }
    }
}

private typealias BacktraceFn = @convention(c) (UnsafeMutablePointer<UnsafeMutableRawPointer?>?, Int32) -> Int32
private typealias SymbolsFn = @convention(c) (UnsafePointer<UnsafeMutableRawPointer?>?, Int32, Int32) -> Void

// Everything the signal handler touches is set up in install(), before any signal can arrive.
private let maxFrames: Int32 = 64
private var signalPath: UnsafeMutablePointer<CChar>?
private var frames: UnsafeMutablePointer<UnsafeMutableRawPointer?>?
private var previousActions: UnsafeMutablePointer<sigaction>?
private var backtraceFn: BacktraceFn?
private var symbolsFn: SymbolsFn?
private var exceptionRecorded = false
private var handlingSignal = false

private func visionarySignalHandler(_ sig: Int32, _ info: UnsafeMutablePointer<siginfo_t>?, _ context: UnsafeMutableRawPointer?) {
    // An uncaught exception ends in abort(); it was already recorded with its real stack.
    if !exceptionRecorded, !handlingSignal, let path = signalPath {
        handlingSignal = true
        let fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0o644)
        if fd >= 0 {
            writeStatic(fd, "signal ")
            writeNumber(fd, Int(sig))
            writeStatic(fd, "\ntime ")
            writeNumber(fd, Int(time(nil)))
            writeStatic(fd, "\n")
            if let frames, let backtraceFn, let symbolsFn {
                let count = backtraceFn(frames, maxFrames)
                symbolsFn(frames, count, fd)
            }
            close(fd)
        }
    }
    // Hand the signal to whoever had it before (the default action terminates the process).
    if let previousActions { sigaction(sig, previousActions + Int(sig), nil) }
    // A hardware fault happens again when we return, now delivered to the previous handler with
    // the original context (so other crash reporters still see the real crash). A signal that was
    // sent (abort(), kill) has to be raised again.
    let sent = (info?.pointee.si_code ?? 0) <= 0
    if sent || sig == SIGABRT { raise(sig) }
}

private func writeStatic(_ fd: Int32, _ s: StaticString) {
    _ = write(fd, s.utf8Start, s.utf8CodeUnitCount)
}

private func writeNumber(_ fd: Int32, _ value: Int) {
    var buffer: (UInt64, UInt64, UInt64) = (0, 0, 0) // 24 bytes on the stack, no allocation
    withUnsafeMutableBytes(of: &buffer) { bytes in
        var v = value < 0 ? 0 : value
        var i = bytes.count
        repeat {
            i -= 1
            bytes[i] = UInt8(48 + v % 10)
            v /= 10
        } while v > 0 && i > 0
        _ = write(fd, bytes.baseAddress! + i, bytes.count - i)
    }
}

#if canImport(ObjectiveC)
private var exceptionCallback: ((NSException) -> Void)?
private var previousExceptionHandler: (@convention(c) (NSException) -> Void)?

private func visionaryExceptionHandler(_ exception: NSException) {
    exceptionRecorded = true
    exceptionCallback?(exception)
    previousExceptionHandler?(exception)
}
#endif
