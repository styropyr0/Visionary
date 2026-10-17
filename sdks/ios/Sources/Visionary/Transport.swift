import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

enum SendResult {
    /// 2xx: the server took the batch.
    case sent
    /// 4xx: the payload or key is wrong (or rate limited); retrying won't help.
    case rejected(Int)
    /// 5xx: try once more later.
    case serverError(Int)
    /// No response at all: keep the events until the network is back.
    case offline
}

/// Posts one JSON envelope to the ingest endpoint on its own ephemeral session; the URLSession
/// instrumentation skips requests to the ingest path, so the SDK never reports itself.
final class Transport {
    static let ingestPath = "/api/ingest/client"

    let url: URL
    private let key: String
    private let session: URLSession

    init(endpoint: URL, key: String) {
        url = Transport.ingestURL(endpoint)
        self.key = key
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 15
        config.httpCookieStorage = nil
        config.urlCache = nil
        session = URLSession(configuration: config)
    }

    /// Blocks the calling (worker) thread until the server answers.
    func send(_ body: String) -> SendResult {
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.httpBody = Data(body.utf8)
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(key)", forHTTPHeaderField: "Authorization")

        let done = DispatchSemaphore(value: 0)
        let result = ResultBox()
        session.dataTask(with: request) { _, response, _ in
            if let code = (response as? HTTPURLResponse)?.statusCode {
                switch code {
                case 200..<300: result.value = .sent
                case 400..<500: result.value = .rejected(code)
                default: result.value = .serverError(code)
                }
            }
            done.signal()
        }.resume()
        done.wait()
        return result.value
    }

    /// Written by the completion handler before the semaphore is signalled, read after the wait.
    private final class ResultBox: @unchecked Sendable {
        var value = SendResult.offline
    }

    static func ingestURL(_ endpoint: URL) -> URL {
        var base = endpoint.absoluteString
        while base.hasSuffix("/") { base.removeLast() }
        if base.hasSuffix(ingestPath) { return URL(string: base) ?? endpoint }
        return URL(string: base + ingestPath) ?? endpoint
    }
}
