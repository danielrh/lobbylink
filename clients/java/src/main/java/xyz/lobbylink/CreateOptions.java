package xyz.lobbylink;

/**
 * Room-creation options, passed to {@link ConnectOptions#create(CreateOptions)}.
 * Omit it entirely to only ever <em>join</em> an existing room.
 *
 * <p>The boolean defaults mirror the server's own defaults. Setters return
 * {@code this} so you can chain, e.g.
 * {@code new CreateOptions(4).waitUntilFull(true)}.
 */
public final class CreateOptions {
    public int maxPlayers;
    public boolean waitUntilFull = false;
    public boolean allowLateJoin = true;
    public boolean allowReconnect = true;
    public boolean allowReplacement = true;
    public ReconnectPolicy reconnectPolicy = null; // null => server default
    public Long claimAfterMs = null;               // null => server default

    public CreateOptions(int maxPlayers) {
        this.maxPlayers = maxPlayers;
    }

    public CreateOptions waitUntilFull(boolean v) { this.waitUntilFull = v; return this; }
    public CreateOptions allowLateJoin(boolean v) { this.allowLateJoin = v; return this; }
    public CreateOptions allowReconnect(boolean v) { this.allowReconnect = v; return this; }
    public CreateOptions allowReplacement(boolean v) { this.allowReplacement = v; return this; }
    public CreateOptions reconnectPolicy(ReconnectPolicy v) { this.reconnectPolicy = v; return this; }
    public CreateOptions claimAfterMs(long v) { this.claimAfterMs = v; return this; }
}
