package xyz.lobbylink;

/** Which DataChannel a message travelled on. */
public enum MessageKind {
    /** Ordered, chunked, delivered-or-error, up to 16 MiB. */
    RELIABLE,
    /** Unordered, no retransmit, may drop; at most 16000 bytes. */
    BEST_EFFORT,
}
