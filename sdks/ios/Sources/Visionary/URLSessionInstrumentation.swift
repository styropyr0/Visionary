import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

// Two ways to time URLSession calls; the README compares them.
//
// 1. Metrics (preferred): URLSession hands every task's URLSessionTaskMetrics to its delegate.
//    Exact timings from the loading system, no change to how requests run, but it needs a delegate
//    on each session you want measured (URLSession.shared has none).
// 2. VisionaryURLProtocol: catches requests of any session whose configuration lists it, with no
//    delegate. It re-issues each request on its own session, so it adds a hop, does not apply the
//    original session's delegate (auth challenges, certificate pinning) and does not work for
//    background sessions.

extension Visionary {
    /// Records one task from its metrics. Call it from
    /// `urlSession(_:task:didFinishCollecting:)` in your own session delegate.
    public static func record(_ metrics: URLSessionTaskMetrics, for task: URLSessionTask) {
        guard let client, let request = task.originalRequest, let url = request.url else { return }
        let status = (task.response as? HTTPURLResponse)?.statusCode ?? 0
        var attrs: [(String, AttributeValue)] = []
        if let last = metrics.transactionMetrics.last {
            if let start = last.requestStartDate, let first = last.responseStartDate {
                attrs.append(("ttfb_ms", .number(Client.round(first.timeIntervalSince(start) * 1000) ?? 0)))
            }
            if let proto = last.networkProtocolName { attrs.append(("protocol", .string(proto))) }
            attrs.append(("reused", .number(last.isReusedConnection ? 1 : 0)))
        }
        if metrics.redirectCount > 0 { attrs.append(("redirects", .number(Double(metrics.redirectCount)))) }
        client.api(
            method: request.httpMethod ?? "GET",
            url: url,
            status: status,
            durationMs: metrics.taskInterval.duration * 1000,
            attrs: attrs
        )
    }
}

/// A session delegate that records every task's metrics. Use it as is, or subclass it and call
/// `super` if you override `urlSession(_:task:didFinishCollecting:)`.
///
/// ```swift
/// let session = URLSession(configuration: .default, delegate: VisionarySessionDelegate(), delegateQueue: nil)
/// ```
open class VisionarySessionDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    open func urlSession(_ session: URLSession, task: URLSessionTask, didFinishCollecting metrics: URLSessionTaskMetrics) {
        Visionary.record(metrics, for: task)
    }
}

/// Automatic capture for sessions you can't give a delegate. Add it to a configuration before
/// creating the session:
///
/// ```swift
/// let config = URLSessionConfiguration.default
/// config.protocolClasses = [VisionaryURLProtocol.self] + (config.protocolClasses ?? [])
/// let session = URLSession(configuration: config)
/// ```
///
/// `URLProtocol.registerClass(VisionaryURLProtocol.self)` covers `URLSession.shared` too.
open class VisionaryURLProtocol: URLProtocol {
    private static let handledKey = "io.github.styropyr0.visionary.handled"
    private static let forwarder = Forwarder()
    private static let session: URLSession = {
        let config = URLSessionConfiguration.default
        config.protocolClasses = (config.protocolClasses ?? []).filter { $0 != VisionaryURLProtocol.self }
        return URLSession(configuration: config, delegate: forwarder, delegateQueue: nil)
    }()

    private var innerTask: URLSessionDataTask?
    private var start = Date()
    private var status = 0
    private var redirectedAway = false

    override open class func canInit(with request: URLRequest) -> Bool {
        guard Visionary.client != nil,
              let url = request.url,
              let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https",
              !url.path.hasSuffix(Transport.ingestPath),
              URLProtocol.property(forKey: handledKey, in: request) == nil
        else { return false }
        return true
    }

    override open class func canonicalRequest(for request: URLRequest) -> URLRequest {
        request
    }

    override open func startLoading() {
        guard let inner = (request as NSURLRequest).mutableCopy() as? NSMutableURLRequest else {
            client?.urlProtocol(self, didFailWithError: URLError(.unknown))
            return
        }
        URLProtocol.setProperty(true, forKey: VisionaryURLProtocol.handledKey, in: inner)
        start = Date()
        #if canImport(FoundationNetworking)
        // swift-corelibs-foundation calls startLoading on a work queue shared by all sessions, and
        // creating a task there deadlocks. Darwin has no such restriction.
        let request = inner as URLRequest
        DispatchQueue.global().async { self.load(request) }
        #else
        load(inner as URLRequest)
        #endif
    }

    private func load(_ inner: URLRequest) {
        let task = VisionaryURLProtocol.session.dataTask(with: inner)
        VisionaryURLProtocol.forwarder.register(self, for: task)
        innerTask = task
        task.resume()
    }

    override open func stopLoading() {
        innerTask?.cancel()
    }

    fileprivate func received(_ response: URLResponse) {
        if redirectedAway { return }
        status = (response as? HTTPURLResponse)?.statusCode ?? 0
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
    }

    fileprivate func received(_ data: Data) {
        if redirectedAway { return }
        client?.urlProtocol(self, didLoad: data)
    }

    fileprivate func redirected(to newRequest: URLRequest, from response: HTTPURLResponse) {
        // Let the caller's session follow the redirect: it issues a new request that comes back through us.
        var next = newRequest
        if let mutable = (newRequest as NSURLRequest).mutableCopy() as? NSMutableURLRequest {
            URLProtocol.removeProperty(forKey: VisionaryURLProtocol.handledKey, in: mutable)
            next = mutable as URLRequest
        }
        redirectedAway = true
        record(status: response.statusCode)
        client?.urlProtocol(self, wasRedirectedTo: next, redirectResponse: response)
        innerTask?.cancel()
    }

    fileprivate func finished(_ error: Error?) {
        if redirectedAway { return }
        if let error {
            record(status: 0)
            client?.urlProtocol(self, didFailWithError: error)
        } else {
            record(status: status)
            client?.urlProtocolDidFinishLoading(self)
        }
    }

    private func record(status: Int) {
        guard let url = request.url else { return }
        Visionary.client?.api(
            method: request.httpMethod ?? "GET",
            url: url,
            status: status,
            durationMs: Date().timeIntervalSince(start) * 1000
        )
    }

    /// Routes the shared session's delegate callbacks to the protocol instance that owns each task.
    private final class Forwarder: NSObject, URLSessionDataDelegate, @unchecked Sendable {
        private let lock = NSLock()
        private var owners: [Int: VisionaryURLProtocol] = [:]

        func register(_ owner: VisionaryURLProtocol, for task: URLSessionTask) {
            lock.lock()
            owners[task.taskIdentifier] = owner
            lock.unlock()
        }

        private func owner(_ task: URLSessionTask, remove: Bool = false) -> VisionaryURLProtocol? {
            lock.lock()
            defer { lock.unlock() }
            return remove ? owners.removeValue(forKey: task.taskIdentifier) : owners[task.taskIdentifier]
        }

        func urlSession(
            _ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
            completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
        ) {
            owner(dataTask)?.received(response)
            completionHandler(.allow)
        }

        func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
            owner(dataTask)?.received(data)
        }

        func urlSession(
            _ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
            newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void
        ) {
            owner(task)?.redirected(to: request, from: response)
            completionHandler(nil)
        }

        func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
            owner(task, remove: true)?.finished(error)
        }
    }
}
