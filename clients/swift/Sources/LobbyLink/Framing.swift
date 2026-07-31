import Foundation

/// Reliable-channel framing, wire-compatible with the TS client
/// (clients/ts/README.md "Wire contract"): big-endian header, one frame
/// per SCTP message.
enum Framing {
    static let magic: UInt8 = 0x4C // 'L'
    static let version: UInt8 = 0x01
    static let headerLen = 18

    static let chunkPayload = 16 * 1024
    static let maxFramePayload = 64 * 1024
    static let maxReliableMessage = 16 * 1024 * 1024
    static let maxReassemblyChunks: UInt32 = 4096
    static let reassemblyTimeout: TimeInterval = 30
    static let maxBestEffort = 16_000

    static func chunkCount(_ n: Int) -> UInt32 {
        n == 0 ? 1 : UInt32((n + chunkPayload - 1) / chunkPayload)
    }

    static func makeFrame(msgID: UInt32, seq: UInt32, total: UInt32, payload: some Collection<UInt8>) -> [UInt8] {
        var frame = [UInt8](repeating: 0, count: headerLen + payload.count)
        frame[0] = magic
        frame[1] = version
        putU32(&frame, at: 2, msgID)
        putU32(&frame, at: 6, seq)
        putU32(&frame, at: 10, total)
        putU32(&frame, at: 14, UInt32(payload.count))
        frame.replaceSubrange(headerLen..., with: payload)
        return frame
    }

    static func parseFrame(_ buf: [UInt8]) throws -> Frame {
        guard buf.count >= headerLen else { throw FrameError.invalid("frame shorter than header") }
        guard buf[0] == magic else { throw FrameError.invalid("bad frame magic") }
        guard buf[1] == version else { throw FrameError.invalid("unsupported frame version") }
        let msgID = u32(buf, at: 2)
        let seq = u32(buf, at: 6)
        let total = u32(buf, at: 10)
        let payloadLen = u32(buf, at: 14)
        guard total >= 1, total <= maxReassemblyChunks else { throw FrameError.invalid("bad frame total") }
        guard seq < total else { throw FrameError.invalid("frame seq out of range") }
        guard payloadLen <= UInt32(maxFramePayload) else { throw FrameError.invalid("frame payload too large") }
        guard Int(payloadLen) == buf.count - headerLen else { throw FrameError.invalid("frame payload length mismatch") }
        return Frame(msgID: msgID, seq: seq, total: total, payload: Array(buf[headerLen...]))
    }

    private static func putU32(_ buf: inout [UInt8], at offset: Int, _ v: UInt32) {
        buf[offset] = UInt8(truncatingIfNeeded: v >> 24)
        buf[offset + 1] = UInt8(truncatingIfNeeded: v >> 16)
        buf[offset + 2] = UInt8(truncatingIfNeeded: v >> 8)
        buf[offset + 3] = UInt8(truncatingIfNeeded: v)
    }

    private static func u32(_ buf: [UInt8], at offset: Int) -> UInt32 {
        UInt32(buf[offset]) << 24 | UInt32(buf[offset + 1]) << 16
            | UInt32(buf[offset + 2]) << 8 | UInt32(buf[offset + 3])
    }
}

enum FrameError: Error {
    case invalid(String)
}

struct Frame {
    let msgID: UInt32
    let seq: UInt32
    let total: UInt32
    let payload: [UInt8]
}

/// Rebuilds chunked reliable messages from one sender, keyed by msgId.
/// Incomplete messages are dropped after 30 s. Not thread-safe; the
/// owning peer link serializes access.
final class Reassembler {
    private struct Entry {
        let total: UInt32
        var chunks: [[UInt8]?]
        var received: UInt32 = 0
        var bytes = 0
        let startedAt: Date
    }

    private var inflight: [UInt32: Entry] = [:]

    /// Feeds one frame; returns the complete message once the last
    /// chunk arrives, else nil.
    func push(_ f: Frame, now: Date) -> Data? {
        for (id, entry) in inflight where now.timeIntervalSince(entry.startedAt) > Framing.reassemblyTimeout {
            inflight.removeValue(forKey: id)
        }
        var entry = inflight[f.msgID]
        if let e = entry, e.total != f.total {
            // msgId reuse with different geometry: treat as a new message.
            entry = nil
        }
        if entry == nil {
            entry = Entry(total: f.total, chunks: [[UInt8]?](repeating: nil, count: Int(f.total)), startedAt: now)
        }
        var e = entry!
        if e.chunks[Int(f.seq)] == nil {
            e.chunks[Int(f.seq)] = f.payload
            e.received += 1
            e.bytes += f.payload.count
            if e.bytes > Framing.maxReliableMessage {
                inflight.removeValue(forKey: f.msgID)
                return nil
            }
        }
        if e.received < e.total {
            inflight[f.msgID] = e
            return nil
        }
        inflight.removeValue(forKey: f.msgID)
        var out = Data(capacity: e.bytes)
        for chunk in e.chunks {
            out.append(contentsOf: chunk ?? [])
        }
        return out
    }
}
