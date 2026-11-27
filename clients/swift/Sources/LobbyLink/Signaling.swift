import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// The signaling WebSocket: a thin ordered-text-message wrapper around
/// URLSessionWebSocketTask. `onText`/`onClosed` must be assigned before
/// `start()` and are never reassigned; they are invoked serially from
/// the receive loop.
final class Signaling: @unchecked Sendable {
    // Server messages are small JSON (SDPs top out around a few KiB).
    private static let readLimit = 1 << 20

    private let session: URLSession
    private let task: URLSessionWebSocketTask
    private let writeLock = NSLock()
    private let stateLock = NSLock()
    private var closed = false

    var onText: ((String) -> Void)?
    var onClosed: (() -> Void)?

    /// Prepares the connection; nothing is sent until `start()`.
    /// `origin` "" sends no Origin header.
    init(url: URL, origin: String, timeout: TimeInterval) {
        var request = URLRequest(url: url)
        request.timeoutInterval = timeout
        if !origin.isEmpty {
            request.setValue(origin, forHTTPHeaderField: "Origin")
        }
        session = URLSession(configuration: .default)
        task = session.webSocketTask(with: request)
        task.maximumMessageSize = Signaling.readLimit
    }

    func start() {
        task.resume()
        receiveNext()
    }

    private func receiveNext() {
        task.receive { [weak self] result in
            guard let self else { return }
            switch result {
            case .success(let message):
                switch message {
                case .string(let text):
                    self.onText?(text)
                case .data(let data):
                    if let text = String(data: data, encoding: .utf8) {
                        self.onText?(text)
                    }
                @unknown default:
                    break
                }
                self.receiveNext()
            case .failure:
                self.reportClosed()
            }
        }
    }

    private func reportClosed() {
        stateLock.lock()
        let alreadyClosed = closed
        closed = true
        stateLock.unlock()
        if !alreadyClosed {
            onClosed?()
        }
    }

    /// Sends one JSON text message; the lock keeps the enqueue order
    /// deterministic across threads. Transport failures surface through
    /// the receive loop, not here.
    func send(json obj: [String: Any]) {
        guard let data = try? JSONSerialization.data(withJSONObject: obj),
              let text = String(data: data, encoding: .utf8) else { return }
        writeLock.lock()
        defer { writeLock.unlock() }
        task.send(.string(text)) { _ in }
    }

    /// Like `send(json:)` but waits (up to `timeout`) for the message
    /// to be handed to the transport — used for the final leave, which
    /// would otherwise race the close frame.
    func sendSync(json obj: [String: Any], timeout: TimeInterval) {
        guard let data = try? JSONSerialization.data(withJSONObject: obj),
              let text = String(data: data, encoding: .utf8) else { return }
        let done = DispatchSemaphore(value: 0)
        writeLock.lock()
        task.send(.string(text)) { _ in done.signal() }
        writeLock.unlock()
        _ = done.wait(timeout: .now() + timeout)
    }

    /// Closes the socket without reporting `onClosed`.
    func close() {
        stateLock.lock()
        closed = true
        stateLock.unlock()
        task.cancel(with: .normalClosure, reason: nil)
        session.finishTasksAndInvalidate()
    }
}
