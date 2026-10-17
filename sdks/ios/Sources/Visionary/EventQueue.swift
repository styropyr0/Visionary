import Foundation

/// The envelope fields an event was recorded under. Events are only batched with equal meta.
struct Meta: Equatable {
    var sessionID: String
    var userID: String?
    var appVersion: String?
    var version: String?

    /// One line, tab separated, used on disk for queued events and the crash context.
    var line: String {
        [sessionID, userID, appVersion, version].map { Meta.clean($0) }.joined(separator: "\t")
    }

    init(sessionID: String, userID: String?, appVersion: String?, version: String?) {
        self.sessionID = sessionID
        self.userID = userID
        self.appVersion = appVersion
        self.version = version
    }

    init?(fields: [Substring]) {
        guard fields.count >= 4, !fields[0].isEmpty else { return nil }
        func field(_ i: Int) -> String? { fields[i].isEmpty ? nil : String(fields[i]) }
        self.init(sessionID: String(fields[0]), userID: field(1), appVersion: field(2), version: field(3))
    }

    static func clean(_ s: String?) -> String {
        (s ?? "").replacingOccurrences(of: "\t", with: " ")
            .replacingOccurrences(of: "\n", with: " ")
            .replacingOccurrences(of: "\r", with: " ")
    }
}

/// One serialized event waiting to be sent. A class on purpose: batches are removed by identity.
final class Record {
    let meta: Meta
    let json: String
    let bytes: Int
    var retried = false

    init(meta: Meta, json: String) {
        self.meta = meta
        self.json = json
        bytes = json.utf8.count
    }
}

/// Pending events, bounded, in memory, mirrored to `file` while offline or in the background.
/// Thread-safe: the worker, the exception handler and callers on any thread use it.
final class EventQueue {
    private let file: URL
    private let capacity: Int
    private let lock = NSLock()
    private var records: [Record] = []
    private var mirrored = false

    init(file: URL, capacity: Int) {
        self.file = file
        self.capacity = max(1, capacity)
    }

    var count: Int { locked { records.count } }

    func add(_ record: Record) {
        locked {
            if records.count >= capacity { records.removeFirst(records.count - capacity + 1) }
            records.append(record)
        }
    }

    /// The next batch: leading events that share the first event's meta, within the count and size limits.
    func nextBatch(maxEvents: Int, maxBytes: Int) -> [Record] {
        locked {
            guard let first = records.first else { return [] }
            var batch: [Record] = []
            var bytes = 0
            for r in records {
                if r.meta != first.meta || batch.count >= maxEvents { break }
                if !batch.isEmpty && bytes + r.bytes + 1 > maxBytes { break }
                batch.append(r)
                bytes += r.bytes + 1
            }
            return batch
        }
    }

    func remove(_ batch: [Record]) {
        locked {
            let gone = Set(batch.map(ObjectIdentifier.init))
            records.removeAll { gone.contains(ObjectIdentifier($0)) }
            if mirrored { persistLocked() }
        }
    }

    /// Writes every pending event to disk (atomically) so it survives the process.
    func persist() {
        locked { persistLocked() }
    }

    /// Loads events saved by a previous launch ahead of new ones.
    func restore() {
        locked {
            guard let text = try? String(contentsOf: file, encoding: .utf8) else { return }
            var restored: [Record] = []
            for line in text.split(separator: "\n") {
                let parts = line.split(separator: "\t", maxSplits: 4, omittingEmptySubsequences: false)
                guard parts.count == 5, parts[4].hasPrefix("{"), let meta = Meta(fields: parts) else { continue }
                restored.append(Record(meta: meta, json: String(parts[4])))
            }
            records = Array((restored + records).suffix(capacity))
            mirrored = true
        }
    }

    private func persistLocked() {
        if records.isEmpty {
            try? FileManager.default.removeItem(at: file)
            mirrored = false
            return
        }
        var text = ""
        for r in records {
            text += r.meta.line + "\t" + r.json + "\n"
        }
        do {
            try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
            try Data(text.utf8).write(to: file, options: .atomic)
            mirrored = true
        } catch {
            // Disk full or not writable: the events stay in memory.
        }
    }

    private func locked<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }
}
