import CDataChannel
import Foundation

// C-convention trampolines: libdatachannel calls back on its own
// threads with the user pointer we registered, an Unmanaged reference
// to the PeerLink. The link stays retained by its P2PGame until
// rtcDelete* has drained all callbacks, so takeUnretainedValue is safe.

private let descriptionTrampoline: rtcDescriptionCallbackFunc = { _, sdp, type, ptr in
    guard let ptr, let sdp, let type else { return }
    let link = Unmanaged<PeerLink>.fromOpaque(ptr).takeUnretainedValue()
    link.onLocalDescription(sdp: String(cString: sdp), type: String(cString: type))
}

private let candidateTrampoline: rtcCandidateCallbackFunc = { _, cand, mid, ptr in
    guard let ptr, let cand else { return }
    let link = Unmanaged<PeerLink>.fromOpaque(ptr).takeUnretainedValue()
    link.onLocalCandidate(String(cString: cand), mid: mid.map { String(cString: $0) })
}

private let stateTrampoline: rtcStateChangeCallbackFunc = { _, state, ptr in
    guard let ptr else { return }
    let link = Unmanaged<PeerLink>.fromOpaque(ptr).takeUnretainedValue()
    link.onStateChange(state)
}

private let openTrampoline: rtcOpenCallbackFunc = { id, ptr in
    guard let ptr else { return }
    Unmanaged<PeerLink>.fromOpaque(ptr).takeUnretainedValue().onChannelOpen(id)
}

private let closedTrampoline: rtcClosedCallbackFunc = { _, ptr in
    guard let ptr else { return }
    Unmanaged<PeerLink>.fromOpaque(ptr).takeUnretainedValue().onChannelClosed()
}

private let messageTrampoline: rtcMessageCallbackFunc = { id, message, size, ptr in
    guard let ptr, let message else { return }
    let link = Unmanaged<PeerLink>.fromOpaque(ptr).takeUnretainedValue()
    // size >= 0 is binary; negative is a null-terminated string of
    // length -size-1. Peers send binary, but accept both.
    let count = size >= 0 ? Int(size) : Int(-size) - 1
    link.onChannelMessage(id, Data(bytes: message, count: count))
}

private let bufferedLowTrampoline: rtcBufferedAmountLowCallbackFunc = { _, ptr in
    guard let ptr else { return }
    Unmanaged<PeerLink>.fromOpaque(ptr).takeUnretainedValue().onBufferedAmountLow()
}

/// One RTCPeerConnection plus the two pre-negotiated DataChannels to a
/// single remote player, over libdatachannel's C API.
final class PeerLink: @unchecked Sendable {
    static let reliableStream: UInt16 = 1
    static let bestEffortStream: UInt16 = 2
    static let sendHighWater = 1 << 20
    static let sendLowWater = 256 * 1024
    static let channelTimeout: TimeInterval = 30
    static let maxPeerRebuilds = 3

    let playerID: Int
    let initiator: Bool
    private let pc: Int32
    weak var game: P2PGame?

    // cond guards all mutable state below and is signaled on channel
    // open, buffered-amount-low, and close.
    private let cond = NSCondition()
    private var closed = false
    private var reliableDC: Int32 = -1
    private var bestEffortDC: Int32 = -1
    private var reliableOpen = false
    private var nextMsgID: UInt32 = 0
    private let reassembler = Reassembler()
    /// ICE candidates that arrived before the remote description; nil
    /// once the remote description is applied (then add directly).
    private var pendingCandidates: [(candidate: String, mid: String?)]? = []

    /// Serializes reliable sends to this peer.
    private let sendLock = NSLock()

    /// Last connection state seen, for the rebuild-still-failed check.
    private var lastState: rtcState = RTC_NEW

    private static let initLogger: Void = rtcInitLogger(RTC_LOG_NONE, nil)

    /// Creates the peer connection and registers signaling callbacks.
    /// The caller registers the link in its peer table, then calls
    /// `startInitiator()` or `acceptOffer(_:)` — libdatachannel
    /// auto-negotiates, so channel creation (initiator) and
    /// setRemoteDescription (answerer) trigger the offer/answer.
    init(playerID: Int, initiator: Bool, iceURIs: [String], forceRelay: Bool, game: P2PGame) throws {
        _ = PeerLink.initLogger
        self.playerID = playerID
        self.initiator = initiator
        self.game = game

        var config = rtcConfiguration()
        if forceRelay {
            config.iceTransportPolicy = RTC_TRANSPORT_POLICY_RELAY
        }
        var cStrings: [UnsafeMutablePointer<CChar>?] = iceURIs.map { strdup($0) }
        defer { cStrings.forEach { free($0) } }
        pc = cStrings.withUnsafeMutableBufferPointer { buf -> Int32 in
            if let base = buf.baseAddress, !iceURIs.isEmpty {
                return base.withMemoryRebound(to: UnsafePointer<CChar>?.self, capacity: buf.count) { servers in
                    config.iceServers = servers
                    config.iceServersCount = Int32(buf.count)
                    return rtcCreatePeerConnection(&config)
                }
            }
            return rtcCreatePeerConnection(&config)
        }
        guard pc >= 0 else {
            throw LobbyError(code: "internal", message: "rtcCreatePeerConnection failed (\(pc))")
        }
        rtcSetUserPointer(pc, Unmanaged.passUnretained(self).toOpaque())
        rtcSetLocalDescriptionCallback(pc, descriptionTrampoline)
        rtcSetLocalCandidateCallback(pc, candidateTrampoline)
        rtcSetStateChangeCallback(pc, stateTrampoline)
    }

    /// Initiator side: creating the first channel triggers the
    /// auto-negotiated offer, delivered via `onLocalDescription`.
    func startInitiator() {
        createChannels()
    }

    /// Answerer side: applying the remote offer triggers the
    /// auto-negotiated answer; the pre-negotiated channels are created
    /// after (they need no SDP presence beyond the application m-line).
    func acceptOffer(sdp: String) {
        let rc = rtcSetRemoteDescription(pc, sdp, "offer")
        guard rc >= 0 else {
            logWarn("setRemoteDescription(offer) from player \(playerID) failed (\(rc))")
            return
        }
        createChannels()
        remoteDescriptionApplied()
    }

    /// Initiator side: applies the peer's answer and flushes queued
    /// candidates. Returns false for a stale/duplicate answer.
    func acceptAnswer(sdp: String) -> Bool {
        cond.lock()
        let stale = closed || !initiator || pendingCandidates == nil
        cond.unlock()
        if stale { return false }
        let rc = rtcSetRemoteDescription(pc, sdp, "answer")
        guard rc >= 0 else {
            logWarn("setRemoteDescription(answer) from player \(playerID) failed (\(rc))")
            return true
        }
        remoteDescriptionApplied()
        return true
    }

    /// Adds a remote ICE candidate, queueing it while the remote
    /// description is not applied yet.
    func addRemoteCandidate(_ candidate: String, mid: String?) {
        cond.lock()
        if closed {
            cond.unlock()
            return
        }
        if pendingCandidates != nil {
            pendingCandidates!.append((candidate, mid))
            cond.unlock()
            return
        }
        cond.unlock()
        addCandidateNow(candidate, mid: mid)
    }

    var isClosed: Bool {
        cond.lock()
        defer { cond.unlock() }
        return closed
    }

    /// Sends an ordered, reliable message (chunked, applying
    /// backpressure), blocking until every chunk is handed to the
    /// transport. Sends to one peer are serialized.
    func sendReliableMessage(_ data: Data) throws {
        sendLock.lock()
        defer { sendLock.unlock() }
        try waitReliableOpen()
        cond.lock()
        let msgID = nextMsgID
        nextMsgID &+= 1
        let dc = reliableDC
        cond.unlock()

        let total = Framing.chunkCount(data.count)
        var seq: UInt32 = 0
        var offset = data.startIndex
        while seq < total {
            if isClosed {
                throw LobbyError(code: "send-failed", message: "connection to player \(playerID) closed mid-send")
            }
            if rtcGetBufferedAmount(dc) > Int32(PeerLink.sendHighWater) {
                do {
                    try awaitDrain(dc)
                } catch let err as LobbyError {
                    throw LobbyError(code: "send-failed", message: "send to player \(playerID) failed: \(err)")
                }
            }
            let end = min(offset + Framing.chunkPayload, data.endIndex)
            let frame = Framing.makeFrame(msgID: msgID, seq: seq, total: total, payload: data[offset..<end])
            let rc = frame.withUnsafeBufferPointer { buf -> Int32 in
                buf.baseAddress!.withMemoryRebound(to: CChar.self, capacity: buf.count) {
                    rtcSendMessage(dc, $0, Int32(buf.count))
                }
            }
            if rc < 0 {
                throw LobbyError(code: "send-failed", message: "send to player \(playerID) failed (\(rc))")
            }
            seq &+= 1
            offset = end
        }
    }

    /// Writes one datagram, dropping it if the channel is not open or
    /// its buffer is over the high-water mark (that is the best-effort
    /// contract).
    func sendBestEffort(_ data: Data) {
        cond.lock()
        let dc = bestEffortDC
        let dead = closed
        cond.unlock()
        guard !dead, dc >= 0, rtcIsOpen(dc) else { return }
        guard rtcGetBufferedAmount(dc) <= Int32(PeerLink.sendHighWater) else { return }
        if data.isEmpty {
            _ = rtcSendMessage(dc, nil, 0)
            return
        }
        _ = data.withUnsafeBytes { raw in
            rtcSendMessage(dc, raw.baseAddress!.assumingMemoryBound(to: CChar.self), Int32(raw.count))
        }
    }

    /// Tears down the connection. Blocks until in-flight libdatachannel
    /// callbacks have drained, so it must never run on a libdatachannel
    /// callback thread, and the caller must not hold the game lock.
    func close() {
        cond.lock()
        if closed {
            cond.unlock()
            return
        }
        closed = true
        pendingCandidates = nil
        let rel = reliableDC
        let be = bestEffortDC
        cond.broadcast()
        cond.unlock()
        if rel >= 0 { rtcDeleteDataChannel(rel) }
        if be >= 0 { rtcDeleteDataChannel(be) }
        rtcClosePeerConnection(pc)
        rtcDeletePeerConnection(pc)
    }

    // -- channels ------------------------------------------------------------

    private func createChannels() {
        let ptr = Unmanaged.passUnretained(self).toOpaque()

        var reliableInit = rtcDataChannelInit()
        reliableInit.negotiated = true
        reliableInit.manualStream = true
        reliableInit.stream = PeerLink.reliableStream
        let rel = rtcCreateDataChannelEx(pc, "reliable", &reliableInit)

        var bestEffortInit = rtcDataChannelInit()
        bestEffortInit.reliability.unordered = true
        bestEffortInit.reliability.unreliable = true
        bestEffortInit.reliability.maxRetransmits = 0
        bestEffortInit.negotiated = true
        bestEffortInit.manualStream = true
        bestEffortInit.stream = PeerLink.bestEffortStream
        let be = rtcCreateDataChannelEx(pc, "best-effort", &bestEffortInit)

        guard rel >= 0, be >= 0 else {
            logWarn("cannot create data channels to player \(playerID) (\(rel), \(be))")
            return
        }
        cond.lock()
        reliableDC = rel
        bestEffortDC = be
        cond.unlock()

        for dc in [rel, be] {
            rtcSetUserPointer(dc, ptr)
            rtcSetOpenCallback(dc, openTrampoline)
            rtcSetClosedCallback(dc, closedTrampoline)
            rtcSetMessageCallback(dc, messageTrampoline)
        }
        rtcSetBufferedAmountLowThreshold(rel, Int32(PeerLink.sendLowWater))
        rtcSetBufferedAmountLowCallback(rel, bufferedLowTrampoline)
    }

    private func remoteDescriptionApplied() {
        cond.lock()
        let queued = pendingCandidates ?? []
        pendingCandidates = nil
        cond.unlock()
        for (candidate, mid) in queued {
            addCandidateNow(candidate, mid: mid)
        }
    }

    private func addCandidateNow(_ candidate: String, mid: String?) {
        let rc: Int32
        if let mid {
            rc = rtcAddRemoteCandidate(pc, candidate, mid)
        } else {
            rc = rtcAddRemoteCandidate(pc, candidate, nil)
        }
        if rc < 0 && !isClosed {
            logWarn("addRemoteCandidate from player \(playerID) failed (\(rc))")
        }
    }

    // -- waiting -------------------------------------------------------------

    /// Blocks until the reliable channel opens, the link dies, or the
    /// channel timeout passes.
    private func waitReliableOpen() throws {
        cond.lock()
        defer { cond.unlock() }
        let deadline = Date().addingTimeInterval(PeerLink.channelTimeout)
        while !reliableOpen {
            if closed {
                throw LobbyError(code: "peer-closed", message: "connection to player \(playerID) closed")
            }
            if !cond.wait(until: deadline) {
                throw LobbyError(code: "channel-timeout", message: "timed out opening channel to player \(playerID)")
            }
        }
    }

    /// Blocks until bufferedAmount is back under the low-water mark (or
    /// the link dies). Called only with bufferedAmount over the
    /// high-water mark; the timed wait is a fallback poll for teardown
    /// races.
    private func awaitDrain(_ dc: Int32) throws {
        cond.lock()
        defer { cond.unlock() }
        while true {
            if closed {
                throw LobbyError(code: "peer-closed", message: "connection to player \(playerID) closed")
            }
            if rtcGetBufferedAmount(dc) <= Int32(PeerLink.sendLowWater) {
                return
            }
            _ = cond.wait(until: Date().addingTimeInterval(0.2))
        }
    }

    // -- callbacks (libdatachannel threads) ----------------------------------

    fileprivate func onLocalDescription(sdp: String, type: String) {
        guard !isClosed, type == "offer" || type == "answer", let game else { return }
        game.sendSignal(to: playerID, payload: Wire.offerPayload(kind: type, sdp: sdp))
    }

    fileprivate func onLocalCandidate(_ candidate: String, mid: String?) {
        guard !isClosed, let game else { return }
        game.sendSignal(to: playerID, payload: Wire.icePayload(candidate: candidate, sdpMid: mid))
    }

    fileprivate func onStateChange(_ state: rtcState) {
        cond.lock()
        lastState = state
        let dead = closed
        cond.unlock()
        guard !dead, let game else { return }
        game.handlePeerState(self, state: PeerLink.stateName(state))
    }

    var isFailed: Bool {
        cond.lock()
        defer { cond.unlock() }
        return lastState == RTC_FAILED
    }

    fileprivate func onChannelOpen(_ id: Int32) {
        cond.lock()
        if id == reliableDC {
            reliableOpen = true
        }
        cond.broadcast()
        cond.unlock()
    }

    fileprivate func onChannelClosed() {
        cond.lock()
        cond.broadcast()
        cond.unlock()
    }

    fileprivate func onChannelMessage(_ id: Int32, _ data: Data) {
        cond.lock()
        let kind: MessageKind? = id == reliableDC ? .reliable : (id == bestEffortDC ? .bestEffort : nil)
        let dead = closed
        cond.unlock()
        guard !dead, let kind, let game else { return }
        switch kind {
        case .bestEffort:
            game.emitMessage(from: playerID, kind: .bestEffort, data: data)
        case .reliable:
            let frame: Frame
            do {
                frame = try Framing.parseFrame([UInt8](data))
            } catch {
                logWarn("dropping reliable frame from player \(playerID): \(error)")
                return
            }
            cond.lock()
            let full = reassembler.push(frame, now: Date())
            cond.unlock()
            if let full {
                game.emitMessage(from: playerID, kind: .reliable, data: full)
            }
        }
    }

    fileprivate func onBufferedAmountLow() {
        cond.lock()
        cond.broadcast()
        cond.unlock()
    }

    static func stateName(_ state: rtcState) -> String {
        switch state {
        case RTC_NEW: return "new"
        case RTC_CONNECTING: return "connecting"
        case RTC_CONNECTED: return "connected"
        case RTC_DISCONNECTED: return "disconnected"
        case RTC_FAILED: return "failed"
        case RTC_CLOSED: return "closed"
        default: return "unknown"
        }
    }
}
