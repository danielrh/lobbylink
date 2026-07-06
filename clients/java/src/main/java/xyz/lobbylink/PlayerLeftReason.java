package xyz.lobbylink;

/** Why a {@link Event.PlayerLeft} fired. */
public enum PlayerLeftReason {
    /** The player called close(); their slot is now free. */
    EXPLICIT_LEAVE,
    /** The player's signaling dropped. An established DataChannel may still work. */
    DISCONNECTED;

    /** Anything other than "explicit-leave" counts as a disconnect (mirrors the other clients). */
    public static PlayerLeftReason fromWire(String reason) {
        return "explicit-leave".equals(reason) ? EXPLICIT_LEAVE : DISCONNECTED;
    }
}
