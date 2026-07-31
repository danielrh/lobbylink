import Foundation
import XCTest
import LobbyLink
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
#if canImport(Glibc)
import Glibc
#endif

// Integration tests build the lobby server from this repository and run
// real WebSocket + WebRTC sessions against it on loopback. They skip
// themselves when the server cannot be built or started.

/// Builds and runs one p2p-lobby-server for the whole test bundle.
final class ServerHarness: NSObject, XCTestObservation {
    static let shared = ServerHarness()

    /// http://127.0.0.1:PORT when the server is up; nil otherwise.
    private(set) var baseURL: String?
    private var process: Process?

    override private init() {
        super.init()
        XCTestObservationCenter.shared.addTestObserver(self)
        do {
            baseURL = try launch()
        } catch {
            FileHandle.standardError.write(Data("integration server unavailable: \(error)\n".utf8))
        }
    }

    func testBundleDidFinish(_ testBundle: Bundle) {
        process?.terminate()
        process?.waitUntilExit()
    }

    private func launch() throws -> String {
        // Tests/LobbyLinkTests/IntegrationTests.swift -> repo root.
        let root = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // LobbyLinkTests
            .deletingLastPathComponent() // Tests
            .deletingLastPathComponent() // swift
            .deletingLastPathComponent() // clients
            .deletingLastPathComponent() // repo root
        guard FileManager.default.fileExists(atPath: root.appendingPathComponent("cmd/p2p-lobby-server").path) else {
            throw HarnessError("not in the repo checkout")
        }
        let dir = "/tmp/lobbylink-swift-test"
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let bin = dir + "/server"

        let build = Process()
        build.executableURL = URL(fileURLWithPath: "/usr/local/go/bin/go")
        build.arguments = ["build", "-o", bin, "./cmd/p2p-lobby-server"]
        build.currentDirectoryURL = root
        try build.run()
        build.waitUntilExit()
        guard build.terminationStatus == 0 else {
            throw HarnessError("server build failed (\(build.terminationStatus))")
        }

        guard let port = ServerHarness.freeLoopbackPort() else {
            throw HarnessError("no free loopback port")
        }
        let origin = "http://127.0.0.1:\(port)"
        let server = Process()
        server.executableURL = URL(fileURLWithPath: bin)
        server.arguments = [
            "--listen-http", "127.0.0.1:\(port)",
            "--allowed-origin", origin,
            "--public-url", origin,
        ]
        try server.run()
        process = server

        for _ in 0..<100 {
            if ServerHarness.httpOK(origin + "/healthz") {
                return origin
            }
            Thread.sleep(forTimeInterval: 0.05)
        }
        server.terminate()
        throw HarnessError("server at \(origin) never became healthy")
    }

    /// Binds port 0 on loopback and reports the kernel-chosen port.
    private static func freeLoopbackPort() -> Int? {
        #if os(Linux)
        let fd = socket(AF_INET, Int32(SOCK_STREAM.rawValue), 0)
        #else
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        #endif
        guard fd >= 0 else { return nil }
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        addr.sin_port = 0
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        guard bound == 0 else { return nil }
        var out = sockaddr_in()
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let got = withUnsafeMutablePointer(to: &out) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                getsockname(fd, $0, &len)
            }
        }
        guard got == 0 else { return nil }
        return Int(UInt16(bigEndian: out.sin_port))
    }

    private static func httpOK(_ url: String) -> Bool {
        guard let u = URL(string: url) else { return false }
        let done = DispatchSemaphore(value: 0)
        var ok = false
        URLSession.shared.dataTask(with: u) { _, response, _ in
            ok = (response as? HTTPURLResponse)?.statusCode == 200
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + 2)
        return ok
    }

    struct HarnessError: Error, CustomStringConvertible {
        let description: String
        init(_ description: String) { self.description = description }
    }
}

final class IntegrationTests: XCTestCase {
    private func requireServer() throws -> String {
        guard let url = ServerHarness.shared.baseURL else {
            throw XCTSkip("local lobby server unavailable")
        }
        return url
    }

    private func randomCode() -> String {
        String(format: "SWTEST-%06d", Int.random(in: 0..<1_000_000))
    }

    /// Drains events until match returns true, failing on timeout.
    private func waitEvent(_ game: P2PGame, _ what: String, timeout: TimeInterval = 30,
                           _ match: (Event) -> Bool) {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let remaining = max(0.05, deadline.timeIntervalSinceNow)
            guard let ev = game.nextEvent(timeout: remaining) else { continue }
            if match(ev) { return }
        }
        XCTFail("timed out waiting for \(what)")
    }

    private func waitConnected(_ game: P2PGame, peer: Int) {
        waitEvent(game, "peer \(peer) connected") { ev in
            if case .peerState(let id, let state) = ev { return id == peer && state == "connected" }
            return false
        }
    }

    func testTwoPeersExchangeMessages() throws {
        let server = try requireServer()
        let code = randomCode()

        var createOpts = ConnectOptions(server: server, code: code)
        createOpts.create = CreateOptions(maxPlayers: 2)
        let a = try P2PGame.connect(createOpts)
        defer { a.close() }
        XCTAssertEqual(a.selfId, 0)
        XCTAssertEqual(a.maxPlayers, 2)
        XCTAssertFalse(a.resumeToken.isEmpty, "no resume token issued")

        let b = try P2PGame.connect(ConnectOptions(server: server, code: code))
        defer { b.close() }
        XCTAssertEqual(b.selfId, 1)

        waitConnected(a, peer: 1)
        waitConnected(b, peer: 0)

        // Reliable, single chunk, both directions.
        try a.sendReliable(to: 1, data: Data("hello from 0".utf8))
        waitEvent(b, "reliable hello") { ev in
            if case .message(let from, let kind, let data) = ev {
                return from == 0 && kind == .reliable && data == Data("hello from 0".utf8)
            }
            return false
        }
        try b.sendReliable(to: 0, data: Data("hello from 1".utf8))
        waitEvent(a, "reliable reply") { ev in
            if case .message(let from, let kind, let data) = ev {
                return from == 1 && kind == .reliable && data == Data("hello from 1".utf8)
            }
            return false
        }

        // Reliable, multi-chunk (100 KiB crosses the 16 KiB chunk size).
        var big = Data(capacity: 100 * 1024)
        for _ in 0..<(25 * 1024) {
            big.append(contentsOf: [0xA5, 0x5A, 0x00, 0xFF])
        }
        try a.sendReliable(to: 1, data: big)
        waitEvent(b, "chunked reliable message") { ev in
            if case .message(let from, let kind, let data) = ev {
                return from == 0 && kind == .reliable && data == big
            }
            return false
        }

        // Best-effort datagrams are lossy in principle but never dropped
        // on an idle loopback link; send a burst and require at least one.
        for _ in 0..<20 {
            try b.broadcastBestEffort(Data("ping".utf8))
            Thread.sleep(forTimeInterval: 0.01)
        }
        waitEvent(a, "best-effort ping") { ev in
            if case .message(let from, let kind, let data) = ev {
                return from == 1 && kind == .bestEffort && data == Data("ping".utf8)
            }
            return false
        }

        // Explicit leave frees the slot and notifies the peer.
        b.close()
        waitEvent(a, "player-left explicit") { ev in
            if case .playerLeft(let id, let reason) = ev { return id == 1 && reason == "explicit-leave" }
            return false
        }
        for p in a.players where p.id == 1 {
            XCTAssertFalse(p.occupied, "slot 1 still occupied after explicit leave")
        }
    }

    func testSendValidation() throws {
        let server = try requireServer()
        var opts = ConnectOptions(server: server, code: randomCode())
        opts.create = CreateOptions(maxPlayers: 3)
        let g = try P2PGame.connect(opts)
        defer { g.close() }

        func expectCode(_ code: String, _ body: () throws -> Void) {
            XCTAssertThrowsError(try body()) { error in
                XCTAssertEqual((error as? LobbyError)?.code, code, "\(error)")
            }
        }
        expectCode("invalid-target") { try g.sendBestEffort(to: 0, data: Data("x".utf8)) } // self
        expectCode("invalid-target") { try g.sendBestEffort(to: 7, data: Data("x".utf8)) }
        expectCode("message-too-large") { try g.sendBestEffort(to: 1, data: Data(count: 16_001)) }
        expectCode("target-unavailable") { try g.sendReliable(to: 1, data: Data("x".utf8)) } // empty slot
    }

    func testJoinErrors() throws {
        let server = try requireServer()
        XCTAssertThrowsError(try P2PGame.connect(ConnectOptions(server: server, code: randomCode()))) { error in
            XCTAssertEqual((error as? LobbyError)?.code, "room-not-found", "\(error)")
        }

        let code = randomCode()
        var opts = ConnectOptions(server: server, code: code)
        opts.create = CreateOptions(maxPlayers: 1)
        let first = try P2PGame.connect(opts)
        defer { first.close() }
        XCTAssertThrowsError(try P2PGame.connect(ConnectOptions(server: server, code: code))) { error in
            XCTAssertEqual((error as? LobbyError)?.code, "room-full", "\(error)")
        }
    }
}
