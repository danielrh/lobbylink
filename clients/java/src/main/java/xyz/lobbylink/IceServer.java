package xyz.lobbylink;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

/**
 * One STUN/TURN server entry, as issued by the lobby server (via
 * {@code joined.iceServers}) or supplied by you in {@link ConnectOptions}.
 * Clients treat these as pass-through configuration; TURN credentials are
 * minted server-side.
 */
public final class IceServer {
    public final List<String> urls;
    public final String username;   // nullable
    public final String credential; // nullable (TURN password)

    public IceServer(List<String> urls, String username, String credential) {
        this.urls = urls == null ? new ArrayList<>() : new ArrayList<>(urls);
        this.username = username;
        this.credential = credential;
    }

    /** Convenience for a single STUN url with no credentials. */
    public IceServer(String url) {
        this(Arrays.asList(url), null, null);
    }
}
