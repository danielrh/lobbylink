package xyz.lobbylink;

/**
 * Failure carrying a stable, machine-readable {@link #getCode() code}.
 *
 * <p>Server-reported codes (e.g. {@code "room-full"}, {@code "slot-not-claimable"})
 * pass through unchanged. Client-side failures use codes such as
 * {@code "connect-timeout"}, {@code "connection-lost"}, {@code "invalid-target"},
 * {@code "message-too-large"}, {@code "channel-timeout"}, {@code "send-failed"} and
 * {@code "closed"}.
 */
public class LobbyException extends Exception {
    private static final long serialVersionUID = 1L;

    private final String code;

    public LobbyException(String code, String message) {
        super(code + ": " + message);
        this.code = code;
    }

    /** Stable identifier for programmatic handling. */
    public String getCode() {
        return code;
    }
}
