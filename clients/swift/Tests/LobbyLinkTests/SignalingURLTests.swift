import XCTest
@testable import LobbyLink

final class SignalingURLTests: XCTestCase {
    func testSignalingURL() throws {
        let cases: [String: String] = [
            "https://example.com": "wss://example.com/ws",
            "https://example.com/": "wss://example.com/ws",
            "https://example.com/lobbylink": "wss://example.com/lobbylink/ws",
            "https://example.com/lobbylink/ws": "wss://example.com/lobbylink/ws",
            "http://127.0.0.1:8787": "ws://127.0.0.1:8787/ws",
            "wss://example.com:4443/ws?x=1#f": "wss://example.com:4443/ws",
            "ws://localhost:8787/lobbylink///": "ws://localhost:8787/lobbylink/ws",
        ]
        for (input, want) in cases {
            XCTAssertEqual(try Wire.signalingURL(input), want, input)
        }
        for bad in ["example.com", "ftp://example.com", "https://"] {
            XCTAssertThrowsError(try Wire.signalingURL(bad), bad)
        }
    }

    func testDefaultOrigin() {
        XCTAssertEqual(Wire.defaultOrigin("wss://example.com/lobbylink/ws"), "https://example.com")
        XCTAssertEqual(Wire.defaultOrigin("ws://127.0.0.1:8787/ws"), "http://127.0.0.1:8787")
    }

    func testValidCode() {
        XCTAssertTrue(Wire.validCode("ROOM"))
        XCTAssertTrue(Wire.validCode("abc_DEF-123"))
        XCTAssertFalse(Wire.validCode("abc"))          // too short
        XCTAssertFalse(Wire.validCode("has space"))
        XCTAssertFalse(Wire.validCode(String(repeating: "x", count: 65)))
    }

    func testICEURIs() {
        let plain = ICEServer(urls: ["stun:stun.example.com:3478"])
        let turn = ICEServer(urls: ["turn:turn.example.com:3478?transport=udp"],
                             username: "user name", credential: "p@ss:word")
        XCTAssertEqual(Wire.iceURIs([plain, turn]), [
            "stun:stun.example.com:3478",
            "turn:user%20name:p%40ss%3Aword@turn.example.com:3478?transport=udp",
        ])
    }
}
