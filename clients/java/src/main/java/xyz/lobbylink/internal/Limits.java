package xyz.lobbylink.internal;

/** Transport/behavioral tunables shared with the other clients. */
public final class Limits {
    private Limits() {}

    /** Best-effort payload hard cap (bytes). Stay under ~1200 to avoid SCTP fragmentation. */
    public static final int MAX_BEST_EFFORT = 16_000;
    /** Pause reliable chunk sends above this bufferedAmount... */
    public static final long SEND_HIGH_WATER = 1L << 20;
    /** ...and resume once it drains below this. */
    public static final long SEND_LOW_WATER = 256L * 1024;
    public static final long CONNECT_TIMEOUT_MS = 20_000;
    /** How long a reliable send waits for a usable channel to the target. */
    public static final long CHANNEL_TIMEOUT_MS = 30_000;
    /** Fallback poll interval while waiting for the send buffer to drain. */
    public static final long DRAIN_POLL_MS = 200;
    public static final String RELIABLE_LABEL = "reliable";
    public static final String BEST_EFFORT_LABEL = "best-effort";
    public static final int RELIABLE_CHANNEL_ID = 1;
    public static final int BEST_EFFORT_CHANNEL_ID = 2;
    /** Automatic ICE-failure rebuilds per peer before giving up. */
    public static final int MAX_PEER_REBUILDS = 3;
}
