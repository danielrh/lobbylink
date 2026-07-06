package xyz.lobbylink;

/** How a room lets a disconnected player reclaim their slot. Part of {@link CreateOptions}. */
public enum ReconnectPolicy {
    TOKEN_ONLY("token-only"),
    TOKEN_OR_CLAIM_AFTER_TIMEOUT("token-or-claim-after-timeout"),
    CLAIM_AFTER_TIMEOUT("claim-after-timeout"),
    HOST_APPROVAL("host-approval");

    private final String wire;

    ReconnectPolicy(String wire) {
        this.wire = wire;
    }

    /** The kebab-case token the server expects. */
    public String wireName() {
        return wire;
    }
}
