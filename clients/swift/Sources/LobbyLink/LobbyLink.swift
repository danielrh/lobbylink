// LobbyLink is the Swift client for the lobbylink P2P lobby system:
// lobby membership over a WebSocket signaling server plus direct WebRTC
// DataChannels (via libdatachannel) between every pair of players. It
// is wire-compatible with the TypeScript (browser), Rust, Go, and Java
// clients — a Swift game and a browser game can share one room.
//
// Contract highlights (see clients/ts/README.md for the full wire
// contract):
//
//   - Per peer pair, two pre-negotiated SCTP DataChannels:
//     "reliable" (negotiated id=1, ordered) and "best-effort"
//     (negotiated id=2, unordered, maxRetransmits=0). Both sides create
//     both; the lower player ID of the pair makes the SDP offer.
//   - Reliable payloads are chunked into 16 KiB frames with an 18-byte
//     big-endian header (magic 0x4C, version 1); max 16 MiB.
//   - Best-effort payloads are raw datagrams, at most 16000 bytes, and
//     may be dropped anywhere.

import Foundation

/// A lobby failure with a stable machine-readable code.
/// Server-reported codes (e.g. "room-full", "slot-not-claimable") pass
/// through unchanged; client-side failures use codes like
/// "connect-timeout", "invalid-target", "message-too-large",
/// "channel-timeout", "send-failed", "closed".
public struct LobbyError: Error, CustomStringConvertible, Sendable {
    public let code: String
    public let message: String

    public init(code: String, message: String) {
        self.code = code
        self.message = message
    }

    public var description: String { "\(code): \(message)" }
}

/// Controls how a room slot may be re-acquired.
public enum ReconnectPolicy: String, Sendable {
    case tokenOnly = "token-only"
    case tokenOrClaimAfterTimeout = "token-or-claim-after-timeout"
    case claimAfterTimeout = "claim-after-timeout"
}

/// Room creation options for the first player in. Unset optionals take
/// the server defaults.
public struct CreateOptions: Sendable {
    public var maxPlayers: Int
    public var waitUntilFull: Bool?
    public var allowLateJoin: Bool?
    public var allowReconnect: Bool?
    public var allowReplacement: Bool?
    public var reconnectPolicy: ReconnectPolicy?
    /// Milliseconds of slot silence before a tokenless claim is allowed;
    /// nil for the server default.
    public var claimAfterMs: Int64?

    public init(maxPlayers: Int) {
        self.maxPlayers = maxPlayers
    }
}

/// Mirrors the WebRTC RTCIceServer dictionary.
public struct ICEServer: Sendable {
    public var urls: [String]
    public var username: String?
    public var credential: String?

    public init(urls: [String], username: String? = nil, credential: String? = nil) {
        self.urls = urls
        self.username = username
        self.credential = credential
    }
}

/// Options for `P2PGame.connect`.
public struct ConnectOptions: Sendable {
    /// "https://host[:port][/path]" or a ws(s) URL; "/ws" is appended
    /// automatically, so subpath deployments work unchanged.
    public var server: String
    /// The room code, 4-64 chars of [A-Za-z0-9_-].
    public var code: String
    /// Optional app policy id for hosted static sites.
    public var appId: String?
    /// Makes the room if it does not exist; nil joins only.
    public var create: CreateOptions?
    /// Resumes our old slot after a reconnect. Overrides the token
    /// stored in `tokenFile`.
    public var resumeToken: String?
    /// Claims a specific silent slot after the resume token is gone
    /// (claim-slot); nil for a normal join.
    public var claimPlayerId: Int?
    /// If set, persists the hidden resume token across process restarts
    /// (the native analog of the browser storageKey).
    public var tokenFile: String?
    /// Overrides the Origin header. nil derives it from `server`
    /// (e.g. "https://host:port"), which production servers accept
    /// without extra config; "" sends no Origin header (servers running
    /// --allow-no-origin).
    public var origin: String?
    /// Appended to the ICE server set issued by the server.
    public var iceServers: [ICEServer] = []
    /// Forces TURN (iceTransportPolicy relay); for testing.
    public var forceRelay: Bool = false
    /// Bounds the join handshake; 0 means 20 s.
    public var connectTimeout: TimeInterval = 20

    public init(server: String, code: String) {
        self.server = server
        self.code = code
    }
}

/// Which DataChannel carried a message.
public enum MessageKind: String, Sendable {
    case reliable = "reliable"
    case bestEffort = "best-effort"
}

/// The public snapshot of one room slot.
public struct PlayerInfo: Sendable, Equatable {
    public let id: Int
    public let occupied: Bool
    public let connected: Bool

    public init(id: Int, occupied: Bool, connected: Bool) {
        self.id = id
        self.occupied = occupied
        self.connected = connected
    }
}

/// Something that happened in the lobby or on a peer connection,
/// returned by `P2PGame.nextEvent()`. The stream matches the other
/// clients' event surface.
public enum Event: Sendable {
    /// A payload from another player.
    case message(from: Int, kind: MessageKind, data: Data)
    /// A fresh occupant took an empty slot; `P2PGame.players` has the
    /// full roster snapshot.
    case playerJoined(id: Int)
    /// A player left. Reason "explicit-leave" frees the slot;
    /// "disconnected" only lost signaling — an established DataChannel
    /// to that player may still be alive.
    case playerLeft(id: Int, reason: String)
    /// A token-based resume of a slot.
    case playerRejoined(id: Int, wasReplacement: Bool)
    /// A tokenless claim of a silent occupied slot.
    case playerReplaced(id: Int)
    /// The room reached its start condition.
    case started
    /// The WebRTC connection state to one player, using the browser's
    /// lowercase strings: "new", "connecting", "connected",
    /// "disconnected", "failed", "closed". Send to a peer once it is
    /// "connected".
    case peerState(id: Int, state: String)
    /// A non-fatal error reported by the lobby server.
    case lobbyError(code: String, message: String)
    /// The signaling WebSocket is gone. Established DataChannels keep
    /// working unless `code` is "replaced", "session-superseded" or
    /// "room-expired" (game over, peers torn down). A plain transport
    /// drop uses code "connection-lost".
    case signalingClosed(code: String, message: String)
}

func isFatalCode(_ code: String) -> Bool {
    switch code {
    case "replaced", "session-superseded", "room-expired", "slow-consumer":
        return true
    default:
        return false
    }
}

func isGameOverCode(_ code: String) -> Bool {
    switch code {
    case "replaced", "session-superseded", "room-expired":
        return true
    default:
        return false
    }
}

/// Writes a library warning to stderr; the client has no logging
/// dependency and never prints on the happy path.
func logWarn(_ message: String) {
    FileHandle.standardError.write(Data("lobbylink: \(message)\n".utf8))
}
