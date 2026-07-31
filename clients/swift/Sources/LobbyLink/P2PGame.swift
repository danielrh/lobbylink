import Foundation

/// A live membership in one room: the signaling session plus a mesh of
/// WebRTC DataChannels to every other player. Create one with
/// `connect(_:)`, drive it by looping on `nextEvent()`, and free the
/// slot with `close()`.
///
///     let game = try P2PGame.connect(ConnectOptions(server: "https://…", code: "MYROOM"))
///     while let ev = game.nextEvent() {
///         if case .peerState(let id, "connected") = ev {
///             try game.sendReliable(to: id, data: Data("hello".utf8))
///         }
///     }
public final class P2PGame: @unchecked Sendable {
    private let options: ConnectOptions
    private let signaling: Signaling
    private let eventQueue = EventQueue()

    // stateCond guards every mutable field below; it is broadcast when
    // peers change, the game closes, or the handshake completes.
    private let stateCond = NSCondition()
    private var codeValue = ""
    private var selfIdValue = 0
    private var maxPlayersValue = 0
    private var startedValue = false
    private var resumeTokenValue = ""
    private var iceServersValue: [ICEServer] = []
    private var iceURIs: [String] = []
    private var roster: [PlayerInfo] = []
    private var peers: [Int: PeerLink] = [:]
    private var rebuilds: [Int: Int] = [:]
    private var closed = false
    private var fatal = false
    private var gameOver = false
    private var handshakePending = true
    private var handshakeFailure: LobbyError?

    private init(options: ConnectOptions, signaling: Signaling) {
        self.options = options
        self.signaling = signaling
    }

    deinit {
        close()
    }

    /// Joins (optionally creating) or claims a slot in a room, blocking
    /// until the lobby returns "joined" or an error. The returned game
    /// is live: peers connect in the background and surface as
    /// `.peerState` events. Throws `LobbyError` with a stable code.
    public static func connect(_ options: ConnectOptions) throws -> P2PGame {
        guard Wire.validCode(options.code) else {
            throw LobbyError(code: "invalid-code", message: "room code must be 4-64 chars of [A-Za-z0-9_-]")
        }
        let urlString = try Wire.signalingURL(options.server)
        guard let url = URL(string: urlString) else {
            throw LobbyError(code: "invalid-server-url", message: "invalid server URL: \(options.server)")
        }
        let origin = options.origin ?? Wire.defaultOrigin(urlString)
        let timeout = options.connectTimeout > 0 ? options.connectTimeout : 20

        let signaling = Signaling(url: url, origin: origin, timeout: timeout)
        let game = P2PGame(options: options, signaling: signaling)
        signaling.onText = { [weak game] text in game?.handleRaw(text) }
        signaling.onClosed = { [weak game] in game?.handleTransportClosed() }
        signaling.start()
        game.sendJoin()

        if let failure = game.awaitHandshake(deadline: Date().addingTimeInterval(timeout), url: urlString) {
            game.abandon()
            throw failure
        }
        return game
    }

    // -- accessors ------------------------------------------------------------

    /// The room code.
    public var code: String { codeValue }

    /// Our stable player ID (0..maxPlayers-1).
    public var selfId: Int { selfIdValue }

    /// The room size.
    public var maxPlayers: Int { maxPlayersValue }

    /// The hidden token that resumes this slot after a disconnect. It
    /// rotates on every (re)join; persisted to `tokenFile` if one was set.
    public var resumeToken: String { resumeTokenValue }

    /// The ICE set in use (server-issued plus ConnectOptions).
    public var iceServers: [ICEServer] { iceServersValue }

    /// Whether the room reached its start condition.
    public var started: Bool {
        stateCond.lock()
        defer { stateCond.unlock() }
        return startedValue
    }

    /// A snapshot of all room slots (one entry per slot, id == index).
    public var players: [PlayerInfo] {
        stateCond.lock()
        defer { stateCond.unlock() }
        return roster
    }

    // -- events ---------------------------------------------------------------

    /// Blocks for the next lobby/peer/message event. Returns nil once
    /// the game is closed (after which it always returns nil).
    public func nextEvent() -> Event? {
        eventQueue.next()
    }

    /// Like `nextEvent()` but gives up after `timeout`. Returns nil on
    /// timeout OR when the game is closed.
    public func nextEvent(timeout: TimeInterval) -> Event? {
        eventQueue.next(timeout: timeout)
    }

    // -- sending --------------------------------------------------------------

    /// Sends one datagram on the unordered, no-retransmit channel. It is
    /// silently dropped if the peer link is not up or its buffer is full
    /// (that is the best-effort contract); errors are caller mistakes
    /// only: bad target or payload over 16000 bytes.
    public func sendBestEffort(to: Int, data: Data) throws {
        try checkTarget(to)
        guard data.count <= Framing.maxBestEffort else {
            throw LobbyError(code: "message-too-large",
                             message: "best-effort payload \(data.count) exceeds \(Framing.maxBestEffort) bytes")
        }
        stateCond.lock()
        let link = peers[to]
        stateCond.unlock()
        link?.sendBestEffort(data)
    }

    /// Sends the datagram to every other occupied slot.
    public func broadcastBestEffort(_ data: Data) throws {
        guard data.count <= Framing.maxBestEffort else {
            throw LobbyError(code: "message-too-large",
                             message: "best-effort payload \(data.count) exceeds \(Framing.maxBestEffort) bytes")
        }
        stateCond.lock()
        let links = roster.compactMap { p in
            p.id != selfIdValue && p.occupied ? peers[p.id] : nil
        }
        stateCond.unlock()
        for link in links {
            link.sendBestEffort(data)
        }
    }

    /// Sends an ordered, reliable message (chunked, up to 16 MiB),
    /// blocking until every chunk is handed to the transport. It waits
    /// up to 30 s for a usable channel to the target.
    public func sendReliable(to: Int, data: Data) throws {
        try checkTarget(to)
        stateCond.lock()
        let occupied = to < roster.count && roster[to].occupied
        stateCond.unlock()
        guard occupied else {
            throw LobbyError(code: "target-unavailable", message: "no player in slot \(to)")
        }
        guard data.count <= Framing.maxReliableMessage else {
            throw LobbyError(code: "message-too-large",
                             message: "reliable payload \(data.count) exceeds \(Framing.maxReliableMessage) bytes")
        }
        let link = try awaitLink(to)
        try link.sendReliableMessage(data)
    }

    // -- close ----------------------------------------------------------------

    /// Leaves the room (freeing our slot), releases all resources,
    /// clears the stored resume token, and ends the event stream. Safe
    /// to call more than once.
    public func close() {
        stateCond.lock()
        if closed {
            stateCond.unlock()
            return
        }
        closed = true
        stateCond.broadcast()
        stateCond.unlock()

        signaling.sendSync(json: Wire.leaveMessage, timeout: 10)
        signaling.close()
        teardownPeers()
        if let tokenFile = options.tokenFile {
            try? FileManager.default.removeItem(atPath: tokenFile)
        }
        eventQueue.close()
    }

    // -- handshake ------------------------------------------------------------

    private func sendJoin() {
        let msg: [String: Any]
        if let claim = options.claimPlayerId {
            msg = Wire.claimSlotMessage(code: options.code, playerId: claim, appId: options.appId)
        } else {
            var token = options.resumeToken ?? ""
            if token.isEmpty, let tokenFile = options.tokenFile,
               let stored = try? String(contentsOfFile: tokenFile, encoding: .utf8) {
                token = stored
            }
            msg = Wire.joinMessage(code: options.code, appId: options.appId,
                                   resumeToken: token, create: options.create)
        }
        signaling.send(json: msg)
    }

    /// Blocks until the handshake resolves; returns the failure to
    /// throw, or nil on success.
    private func awaitHandshake(deadline: Date, url: String) -> LobbyError? {
        stateCond.lock()
        defer { stateCond.unlock() }
        while handshakePending {
            if !stateCond.wait(until: deadline) {
                handshakePending = false
                return LobbyError(code: "connect-timeout", message: "timed out connecting to \(url)")
            }
        }
        return handshakeFailure
    }

    /// Internal cleanup for a failed connect: no leave message, no slot
    /// was held.
    private func abandon() {
        stateCond.lock()
        closed = true
        stateCond.unlock()
        signaling.close()
        teardownPeers()
        eventQueue.close()
    }

    private func applyJoined(_ obj: [String: Any]) {
        let joinedMaxPlayers = Wire.int(obj, "maxPlayers")
        let allIce = Wire.iceServers(obj["iceServers"]) + options.iceServers
        let token = Wire.str(obj, "resumeToken")

        stateCond.lock()
        codeValue = Wire.str(obj, "code", options.code)
        selfIdValue = Wire.int(obj, "selfId")
        maxPlayersValue = joinedMaxPlayers
        startedValue = Wire.bool(obj, "started")
        resumeTokenValue = token
        iceServersValue = allIce
        iceURIs = Wire.iceURIs(allIce)
        roster = Wire.roster(maxPlayers: joinedMaxPlayers, players: obj["players"])
        handshakePending = false
        handshakeFailure = nil
        // Lower ID initiates: offer to every connected peer with a
        // higher ID. Lower-ID peers offer to us when they see
        // player-joined.
        let toInitiate = roster.filter {
            $0.id != selfIdValue && $0.occupied && $0.connected && selfIdValue < $0.id
        }.map(\.id)
        stateCond.broadcast()
        stateCond.unlock()

        if let tokenFile = options.tokenFile {
            if !FileManager.default.createFile(atPath: tokenFile, contents: Data(token.utf8),
                                               attributes: [.posixPermissions: 0o600]) {
                logWarn("cannot persist resume token to \(tokenFile)")
            }
        }
        for id in toInitiate {
            initiatePeer(id)
        }
    }

    // -- signaling in ---------------------------------------------------------

    private func handleRaw(_ text: String) {
        guard let obj = Wire.parse(text) else { return }
        let type = Wire.str(obj, "type")
        stateCond.lock()
        let pending = handshakePending
        stateCond.unlock()
        if pending {
            switch type {
            case "joined":
                applyJoined(obj)
            case "error":
                let failure = LobbyError(code: Wire.str(obj, "code", "internal"), message: Wire.str(obj, "message"))
                stateCond.lock()
                handshakePending = false
                handshakeFailure = failure
                stateCond.broadcast()
                stateCond.unlock()
            default:
                break // anything else before "joined" is unexpected; ignore
            }
            return
        }
        handleServerMessage(type, obj)
    }

    private func handleTransportClosed() {
        stateCond.lock()
        if handshakePending {
            handshakePending = false
            handshakeFailure = LobbyError(code: "connection-failed",
                                          message: "connection failed or closed before join completed")
            stateCond.broadcast()
            stateCond.unlock()
            return
        }
        let report = !closed && !fatal
        fatal = true
        stateCond.unlock()
        if report {
            emit(.signalingClosed(code: "connection-lost",
                                  message: "signaling connection lost; existing peer channels stay up"))
        }
    }

    private func handleServerMessage(_ type: String, _ obj: [String: Any]) {
        switch type {
        case "player-joined":
            let playerID = Wire.int(obj, "playerId")
            stateCond.lock()
            roster = Wire.roster(maxPlayers: maxPlayersValue, players: obj["players"])
            stateCond.unlock()
            emit(.playerJoined(id: playerID))
            resetPeer(playerID)
        case "player-left":
            let playerID = Wire.int(obj, "playerId")
            let reason = Wire.str(obj, "reason") == "explicit-leave" ? "explicit-leave" : "disconnected"
            stateCond.lock()
            if playerID >= 0, playerID < roster.count {
                let occupied = reason == "explicit-leave" ? false : roster[playerID].occupied
                roster[playerID] = PlayerInfo(id: playerID, occupied: occupied, connected: false)
            }
            stateCond.unlock()
            if reason == "explicit-leave" {
                closePeer(playerID)
            }
            // On "disconnected" the peer only lost signaling; an
            // established DataChannel may well still be alive, so keep it.
            emit(.playerLeft(id: playerID, reason: reason))
        case "player-rejoined":
            let playerID = Wire.int(obj, "playerId")
            markOccupied(playerID)
            emit(.playerRejoined(id: playerID, wasReplacement: Wire.bool(obj, "wasReplacement")))
            resetPeer(playerID)
        case "player-replaced":
            let playerID = Wire.int(obj, "playerId")
            markOccupied(playerID)
            emit(.playerReplaced(id: playerID))
            resetPeer(playerID)
        case "room-started":
            stateCond.lock()
            startedValue = true
            stateCond.unlock()
            emit(.started)
        case "signal":
            handleSignal(from: Wire.int(obj, "from"), payload: obj["payload"] as? [String: Any])
        case "error":
            handleErrorMessage(code: Wire.str(obj, "code", "internal"), message: Wire.str(obj, "message"))
        case "joined":
            break // only expected once, handled during connect
        default:
            break // unknown message types are ignored for forward compatibility
        }
    }

    private func handleErrorMessage(code: String, message: String) {
        if isFatalCode(code) {
            stateCond.lock()
            fatal = true
            stateCond.unlock()
            if isGameOverCode(code) {
                teardownPeers()
                // "session-superseded" means our own token resumed
                // elsewhere; that process owns the token file now.
                if let tokenFile = options.tokenFile, code != "session-superseded" {
                    try? FileManager.default.removeItem(atPath: tokenFile)
                }
            }
            emit(.signalingClosed(code: code, message: message))
        } else {
            emit(.lobbyError(code: code, message: message))
        }
    }

    private func markOccupied(_ playerID: Int) {
        stateCond.lock()
        if playerID >= 0, playerID < roster.count {
            roster[playerID] = PlayerInfo(id: playerID, occupied: true, connected: true)
        }
        stateCond.unlock()
    }

    // -- WebRTC ---------------------------------------------------------------

    func sendSignal(to: Int, payload: [String: Any]) {
        signaling.send(json: Wire.signalMessage(to: to, payload: payload))
    }

    private func handleSignal(from: Int, payload: [String: Any]?) {
        guard let payload, from != selfIdValue else { return }
        stateCond.lock()
        let dead = closed
        let link = peers[from]
        stateCond.unlock()
        if dead { return }
        switch Wire.str(payload, "kind") {
        case "offer":
            if selfIdValue < from {
                logWarn("ignoring offer from higher-ID player \(from) (protocol says we offer)")
                return
            }
            // Every incoming offer starts a fresh session (initial
            // connect or the initiator rebuilding after a failure).
            guard let fresh = createLink(playerID: from, initiator: false) else { return }
            fresh.acceptOffer(sdp: Wire.str(payload, "sdp"))
        case "answer":
            guard let link, link.acceptAnswer(sdp: Wire.str(payload, "sdp")) else {
                logWarn("ignoring stale answer from \(from)")
                return
            }
        case "ice":
            guard let link, !link.isClosed else { return }
            // A null/absent candidate is the optional end-of-candidates
            // marker; ignore it.
            guard let cand = payload["candidate"] as? [String: Any] else { return }
            let candidate = Wire.str(cand, "candidate")
            guard !candidate.isEmpty else { return }
            link.addRemoteCandidate(candidate, mid: cand["sdpMid"] as? String)
        default:
            break
        }
    }

    /// Registers a fresh link to playerID, replacing (and closing) any
    /// existing one.
    private func createLink(playerID: Int, initiator: Bool) -> PeerLink? {
        let link: PeerLink
        do {
            link = try PeerLink(playerID: playerID, initiator: initiator, iceURIs: iceURIs,
                                forceRelay: options.forceRelay, game: self)
        } catch {
            logWarn("cannot create peer connection to player \(playerID): \(error)")
            return nil
        }
        stateCond.lock()
        if closed {
            stateCond.unlock()
            link.close()
            return nil
        }
        let old = peers[playerID]
        peers[playerID] = link
        stateCond.broadcast()
        stateCond.unlock()
        if let old {
            DispatchQueue.global().async { old.close() }
        }
        return link
    }

    private func initiatePeer(_ playerID: Int) {
        guard let link = createLink(playerID: playerID, initiator: true) else { return }
        link.startInitiator()
    }

    /// Drops any old link after a peer got a new session and re-offers
    /// if we are the initiator.
    private func resetPeer(_ playerID: Int) {
        guard playerID != selfIdValue else { return }
        stateCond.lock()
        let old = peers.removeValue(forKey: playerID)
        rebuilds[playerID] = nil
        let initiate = selfIdValue < playerID && !closed
        stateCond.unlock()
        old?.close()
        if initiate {
            initiatePeer(playerID)
        }
    }

    // Called by PeerLink from libdatachannel threads.
    func handlePeerState(_ link: PeerLink, state: String) {
        emit(.peerState(id: link.playerID, state: state))
        switch state {
        case "connected":
            stateCond.lock()
            rebuilds[link.playerID] = nil
            stateCond.unlock()
        case "failed":
            handlePeerFailure(link)
        default:
            break
        }
    }

    func emitMessage(from: Int, kind: MessageKind, data: Data) {
        emit(.message(from: from, kind: kind, data: data))
    }

    /// Schedules an initiator-side rebuild with backoff after ICE
    /// failure (up to 3 rebuilds, backoff 1s * count).
    private func handlePeerFailure(_ link: PeerLink) {
        guard link.initiator else { return }
        let playerID = link.playerID
        stateCond.lock()
        if closed {
            stateCond.unlock()
            return
        }
        let count = (rebuilds[playerID] ?? 0) + 1
        rebuilds[playerID] = count
        stateCond.unlock()
        if count > PeerLink.maxPeerRebuilds {
            logWarn("giving up on peer \(playerID) after \(PeerLink.maxPeerRebuilds) rebuilds")
            return
        }
        DispatchQueue.global().asyncAfter(deadline: .now() + .seconds(count)) { [weak self] in
            guard let self else { return }
            self.stateCond.lock()
            let slotOK = playerID < self.roster.count && self.roster[playerID].occupied
                && self.roster[playerID].connected
            let stillCurrent = !self.closed && self.peers[playerID] === link && slotOK
            self.stateCond.unlock()
            guard stillCurrent, link.isFailed else { return }
            self.initiatePeer(playerID)
        }
    }

    // -- data path helpers ----------------------------------------------------

    private func checkTarget(_ to: Int) throws {
        guard to >= 0, to < maxPlayersValue else {
            throw LobbyError(code: "invalid-target", message: "player id \(to) out of range 0..\(maxPlayersValue - 1)")
        }
        guard to != selfIdValue else {
            throw LobbyError(code: "invalid-target", message: "cannot send to yourself")
        }
    }

    /// Resolves the current link to a peer, waiting for one to be
    /// created if necessary.
    private func awaitLink(_ playerID: Int) throws -> PeerLink {
        stateCond.lock()
        defer { stateCond.unlock() }
        let deadline = Date().addingTimeInterval(PeerLink.channelTimeout)
        while true {
            if closed || gameOver {
                throw LobbyError(code: "closed", message: "game is closed")
            }
            if let link = peers[playerID], !link.isClosed {
                return link
            }
            if !stateCond.wait(until: deadline) {
                throw LobbyError(code: "channel-timeout",
                                 message: "no WebRTC session with player \(playerID) within \(Int(PeerLink.channelTimeout))s")
            }
        }
    }

    // -- teardown -------------------------------------------------------------

    private func closePeer(_ playerID: Int) {
        stateCond.lock()
        let link = peers.removeValue(forKey: playerID)
        stateCond.unlock()
        link?.close()
    }

    private func teardownPeers() {
        stateCond.lock()
        let links = Array(peers.values)
        peers.removeAll()
        gameOver = true
        stateCond.broadcast()
        stateCond.unlock()
        for link in links {
            link.close()
        }
    }

    private func emit(_ ev: Event) {
        stateCond.lock()
        let dead = closed
        stateCond.unlock()
        if dead { return }
        eventQueue.push(ev)
    }
}

/// An unbounded FIFO with blocking and timed takes; closing wakes every
/// waiter, and queued events drain before nil is returned.
private final class EventQueue: @unchecked Sendable {
    private let cond = NSCondition()
    private var queue: [Event] = []
    private var closed = false

    func push(_ ev: Event) {
        cond.lock()
        if !closed {
            queue.append(ev)
            cond.signal()
        }
        cond.unlock()
    }

    func close() {
        cond.lock()
        closed = true
        cond.broadcast()
        cond.unlock()
    }

    func next() -> Event? {
        cond.lock()
        defer { cond.unlock() }
        while queue.isEmpty && !closed {
            cond.wait()
        }
        return queue.isEmpty ? nil : queue.removeFirst()
    }

    func next(timeout: TimeInterval) -> Event? {
        cond.lock()
        defer { cond.unlock() }
        let deadline = Date().addingTimeInterval(timeout)
        while queue.isEmpty && !closed {
            if !cond.wait(until: deadline) {
                return nil
            }
        }
        return queue.isEmpty ? nil : queue.removeFirst()
    }
}
