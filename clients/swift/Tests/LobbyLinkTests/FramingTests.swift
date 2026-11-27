import Foundation
import XCTest
@testable import LobbyLink

final class FramingTests: XCTestCase {
    func testFrameRoundTrip() throws {
        let payload = Data("hello, frame".utf8)
        let f = try Framing.parseFrame(Framing.makeFrame(msgID: 7, seq: 2, total: 5, payload: payload))
        XCTAssertEqual(f.msgID, 7)
        XCTAssertEqual(f.seq, 2)
        XCTAssertEqual(f.total, 5)
        XCTAssertEqual(Data(f.payload), payload)
    }

    func testFrameHeaderLayout() {
        // Byte-for-byte layout check against the wire contract.
        let frame = Framing.makeFrame(msgID: 0x0102_0304, seq: 0x0506_0708, total: 0x090A_0B0C, payload: [0xFF])
        let want: [UInt8] = [
            0x4C, 0x01,
            0x01, 0x02, 0x03, 0x04,
            0x05, 0x06, 0x07, 0x08,
            0x09, 0x0A, 0x0B, 0x0C,
            0x00, 0x00, 0x00, 0x01,
            0xFF,
        ]
        XCTAssertEqual(frame, want)
    }

    func testParseFrameRejects() {
        let good = Framing.makeFrame(msgID: 1, seq: 0, total: 1, payload: Data("x".utf8))
        let cases: [(String, ([UInt8]) -> [UInt8])] = [
            ("short", { Array($0[..<(Framing.headerLen - 1)]) }),
            ("magic", { var b = $0; b[0] = 0x4D; return b }),
            ("version", { var b = $0; b[1] = 0x02; return b }),
            ("zero total", { var b = $0; b[13] = 0; return b }),
            ("seq >= total", { var b = $0; b[9] = 9; return b }),
            ("len mismatch", { $0 + [0x00] }),
        ]
        for (name, mutate) in cases {
            XCTAssertThrowsError(try Framing.parseFrame(mutate(good)), name)
        }
    }

    func testReassemblerOrderAndDedup() {
        let r = Reassembler()
        let now = Date()
        let full = [UInt8](repeating: 0, count: 40_000).enumerated().map { i, _ in UInt8(truncatingIfNeeded: i) }
        let total = Framing.chunkCount(full.count)
        XCTAssertEqual(total, 3)
        func chunk(_ seq: UInt32) -> Frame {
            let start = Int(seq) * Framing.chunkPayload
            let end = min(start + Framing.chunkPayload, full.count)
            return Frame(msgID: 1, seq: seq, total: total, payload: Array(full[start..<end]))
        }
        // Out of order, with a duplicate.
        XCTAssertNil(r.push(chunk(2), now: now), "incomplete message returned early")
        XCTAssertNil(r.push(chunk(0), now: now), "incomplete message returned early")
        XCTAssertNil(r.push(chunk(0), now: now), "duplicate chunk completed message")
        XCTAssertEqual(r.push(chunk(1), now: now), Data(full))
    }

    func testReassemblerTimeout() {
        let r = Reassembler()
        let start = Date()
        XCTAssertNil(r.push(Frame(msgID: 1, seq: 0, total: 2, payload: [UInt8]("a".utf8)), now: start))
        // The half-done message is pruned once stale; the late second
        // half then starts a fresh (incomplete) entry instead of
        // completing.
        let late = start.addingTimeInterval(Framing.reassemblyTimeout + 1)
        XCTAssertNil(r.push(Frame(msgID: 1, seq: 1, total: 2, payload: [UInt8]("b".utf8)), now: late),
                     "timed-out reassembly still completed")
    }

    func testChunkCount() {
        XCTAssertEqual(Framing.chunkCount(0), 1)
        XCTAssertEqual(Framing.chunkCount(1), 1)
        XCTAssertEqual(Framing.chunkCount(Framing.chunkPayload), 1)
        XCTAssertEqual(Framing.chunkCount(Framing.chunkPayload + 1), 2)
    }
}
