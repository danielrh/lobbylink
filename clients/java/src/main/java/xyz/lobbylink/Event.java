package xyz.lobbylink;

/**
 * Something that happened in the lobby or on a peer connection, returned by
 * {@link P2PGame#nextEvent()}. Handle the ones you care about with a pattern
 * switch or {@code instanceof}:
 *
 * <pre>{@code
 * Event ev = game.nextEvent();
 * if (ev instanceof Event.Message m && m.kind() == MessageKind.RELIABLE) {
 *     handle(m.from(), m.data());
 * }
 * }</pre>
 */
public sealed interface Event {

    /** A datagram arrived from a peer. {@code data} is owned by you. */
    record Message(int from, MessageKind kind, byte[] data) implements Event {}

    /** A new player took a slot. A full roster snapshot is in {@link P2PGame#players()}. */
    record PlayerJoined(int playerId) implements Event {}

    /** A player left. See {@link PlayerLeftReason}. */
    record PlayerLeft(int playerId, PlayerLeftReason reason) implements Event {}

    /** A player reclaimed a slot (token resume or claim). */
    record PlayerRejoined(int playerId, boolean wasReplacement) implements Event {}

    /** Another player took over a slot that was ours/occupied. */
    record PlayerReplaced(int playerId) implements Event {}

    /** The room reached its start condition (e.g. filled when waitUntilFull). */
    record Started() implements Event {}

    /**
     * A peer connection changed state. {@code state} uses the browser's
     * lowercase strings: "new", "connecting", "connected", "disconnected",
     * "failed", "closed". Send to a peer once it is "connected".
     */
    record PeerState(int playerId, String state) implements Event {}

    /**
     * The selected ICE candidate types (host/srflx/prflx/relay) once a peer
     * connection reaches "connected". Best-effort debug info (e.g. to confirm a
     * relay path with forceRelay).
     */
    record CandidatePair(int playerId, String local, String remote) implements Event {}

    /** A non-fatal error reported by the lobby server. */
    record LobbyError(String code, String message) implements Event {}

    /**
     * The signaling WebSocket is gone. Established DataChannels keep working
     * unless {@code code} is "replaced", "session-superseded" or "room-expired",
     * in which case the game is over and peers were torn down. A plain transport
     * drop uses code "connection-lost".
     */
    record SignalingClosed(String code, String message) implements Event {}
}
