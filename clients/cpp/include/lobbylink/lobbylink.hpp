// lobbylink C++ client: lobby membership over a WebSocket signaling
// server plus direct peer-to-peer WebRTC DataChannels (via
// libdatachannel) between every pair of players. Wire-compatible with
// the TypeScript (browser), Rust, Go, and Java clients — a C++ game
// and a browser game can share one room.
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
//
// Threading: connect() blocks until the lobby accepts or rejects the
// join. Everything after that is event-driven: libdatachannel invokes
// its callbacks on internal threads, and this library funnels all of
// them into one thread-safe queue you drain with nextEvent(). Send
// calls are safe from any thread.

#ifndef LOBBYLINK_LOBBYLINK_HPP
#define LOBBYLINK_LOBBYLINK_HPP

#include <chrono>
#include <cstdint>
#include <memory>
#include <optional>
#include <stdexcept>
#include <string>
#include <vector>

namespace lobbylink {

// LobbyError is a lobby failure with a stable machine-readable code.
// Server-reported codes (e.g. "room-full", "slot-not-claimable") pass
// through unchanged; client-side failures use codes like
// "connect-timeout", "invalid-target", "message-too-large",
// "channel-timeout", "send-failed", "closed".
class LobbyError : public std::runtime_error {
public:
    LobbyError(std::string code, const std::string &message)
        : std::runtime_error(code + ": " + message), code_(std::move(code)),
          message_(message) {}
    const std::string &code() const { return code_; }
    const std::string &message() const { return message_; }

private:
    std::string code_;
    std::string message_;
};

// CreateOptions configure room creation for the first player in.
// Unset optionals use the server defaults (waitUntilFull=false,
// allowLateJoin=true, allowReconnect=true, allowReplacement=true,
// reconnectPolicy=token-or-claim-after-timeout).
struct CreateOptions {
    int maxPlayers = 0;
    std::optional<bool> waitUntilFull;
    std::optional<bool> allowLateJoin;
    std::optional<bool> allowReconnect;
    std::optional<bool> allowReplacement;
    // "token-only", "token-or-claim-after-timeout", "claim-after-timeout".
    std::string reconnectPolicy;
    std::optional<std::chrono::milliseconds> claimAfter;

    CreateOptions() = default;
    explicit CreateOptions(int maxPlayers_) : maxPlayers(maxPlayers_) {}
};

// ICEServer mirrors the WebRTC RTCIceServer dictionary.
struct ICEServer {
    std::vector<std::string> urls;
    std::string username;
    std::string credential;
};

// ConnectOptions configure P2PGame::connect.
struct ConnectOptions {
    // "https://host[:port][/path]" or a ws(s) URL; "/ws" is appended
    // automatically, so subpath deployments work unchanged.
    std::string server;
    // Room code, 4-64 chars of [A-Za-z0-9_-].
    std::string code;
    // Optional app policy id for hosted static sites.
    std::string appId;
    // Create the room if it does not exist; nullopt joins only.
    std::optional<CreateOptions> create;
    // Explicit resume token; overrides the one stored in tokenFile.
    std::string resumeToken;
    // Claim a specific silent slot after the resume token is gone
    // (claim-slot); nullopt for a normal join.
    std::optional<int> claimPlayerId;
    // If set, persists the hidden resume token across process restarts
    // (the native analog of the browser storageKey). Use a per-process
    // path: two clients sharing one file steal each other's slot.
    std::string tokenFile;
    // Overrides the Origin header. nullopt derives it from the server
    // URL (e.g. "https://host:port"), which production servers accept
    // without extra config; "" sends no Origin header (servers running
    // --allow-no-origin).
    std::optional<std::string> origin;
    // Extra ICE servers, appended to the set issued by the server.
    std::vector<ICEServer> iceServers;
    // Force TURN relay (iceTransportPolicy "relay"); for TURN testing.
    bool forceRelay = false;
    // Bounds the join handshake; <= 0 means 20s.
    std::chrono::milliseconds connectTimeout{0};

    ConnectOptions() = default;
    ConnectOptions(std::string server_, std::string code_)
        : server(std::move(server_)), code(std::move(code_)) {}
};

// MessageKind says which DataChannel carried a message.
enum class MessageKind { Reliable, BestEffort };

// PlayerInfo is the public snapshot of one room slot.
struct PlayerInfo {
    int id = 0;
    bool occupied = false;
    bool connected = false;
};

// Event is one entry of the game's event stream; switch on `type` and
// read the fields that type populates (documented per enumerator).
// The stream matches the other clients' event surface.
struct Event {
    enum class Type {
        // A payload from another player: playerId (sender), kind, data.
        Message,
        // A fresh occupant of an empty slot: playerId.
        PlayerJoined,
        // A leave: playerId, reason. Reason "explicit-leave" frees the
        // slot; "disconnected" only lost signaling — an established
        // DataChannel to that player may still be alive.
        PlayerLeft,
        // A token-based resume of a slot: playerId, wasReplacement.
        PlayerRejoined,
        // A tokenless claim of a silent slot: playerId.
        PlayerReplaced,
        // The room reached its start condition (no fields).
        Started,
        // WebRTC connection state to one player: playerId, state — the
        // browser's lowercase strings ("new", "connecting",
        // "connected", "disconnected", "failed", "closed"). Send to a
        // peer once it is "connected".
        PeerState,
        // A non-fatal error reported by the lobby server: code, message.
        LobbyError,
        // The signaling WebSocket is gone: code, message. Established
        // DataChannels keep working unless code is "replaced",
        // "session-superseded" or "room-expired" (game over, peers torn
        // down). A plain transport drop uses code "connection-lost".
        SignalingClosed,
        // close() was called and the queue is drained; no more events.
        Closed,
    };

    Type type = Type::Closed;
    int playerId = -1;
    MessageKind kind = MessageKind::Reliable;
    std::vector<std::uint8_t> data;
    std::string reason;
    bool wasReplacement = false;
    std::string state;
    std::string code;
    std::string message;
};

// P2PGame is a live membership in one room: the signaling session plus
// a mesh of WebRTC DataChannels to every other player. Create one with
// connect(); receive with nextEvent(); free the slot with close().
class P2PGame {
public:
    // Joins (optionally creating) or claims a slot in a room. Blocks
    // until the lobby returns joined or an error; throws LobbyError on
    // failure. The returned game is live: peers connect in the
    // background and surface as PeerState events.
    static std::unique_ptr<P2PGame> connect(const ConnectOptions &options);

    // Calls close().
    ~P2PGame();

    P2PGame(const P2PGame &) = delete;
    P2PGame &operator=(const P2PGame &) = delete;

    // Accessors. selfId is our stable player ID (0..maxPlayers-1);
    // resumeToken rotates on every (re)join.
    int selfId() const;
    const std::string &code() const;
    int maxPlayers() const;
    bool started() const;
    const std::string &resumeToken() const;
    std::vector<PlayerInfo> players() const;
    // The ICE set in use: server-issued plus ConnectOptions extras.
    std::vector<ICEServer> iceServers() const;

    // Blocks for the next event. After close(), drains the remaining
    // queue and then returns Type::Closed events forever.
    Event nextEvent();
    // As above, but returns nullopt if no event arrives within timeout.
    std::optional<Event> nextEvent(std::chrono::milliseconds timeout);

    // Sends an ordered, reliable message (chunked, up to 16 MiB),
    // blocking until every chunk is handed to the transport (waiting
    // out backpressure). Waits up to 30s for a usable channel to the
    // target. Throws LobbyError on failure.
    void sendReliable(int to, const std::uint8_t *data, std::size_t size);
    void sendReliable(int to, const std::vector<std::uint8_t> &data);

    // Sends one datagram on the unordered, no-retransmit channel. It is
    // silently dropped if the peer link is not up or its buffer is full
    // (that is the best-effort contract); throws only on caller
    // mistakes: bad target or payload over 16000 bytes.
    void sendBestEffort(int to, const std::uint8_t *data, std::size_t size);
    void sendBestEffort(int to, const std::vector<std::uint8_t> &data);

    // sendBestEffort to every other occupied slot.
    void broadcastBestEffort(const std::uint8_t *data, std::size_t size);
    void broadcastBestEffort(const std::vector<std::uint8_t> &data);

    // Leaves the room (freeing our slot), tears down all peers, removes
    // the stored resume token, and ends the event stream. Idempotent;
    // also called by the destructor.
    void close();

    // Implementation detail (opaque; public only so internal helper
    // types can name it).
    struct Impl;

private:
    P2PGame();
    std::unique_ptr<Impl> impl_;
};

} // namespace lobbylink

#endif // LOBBYLINK_LOBBYLINK_HPP
