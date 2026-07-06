package xyz.lobbylink;

import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;

/**
 * Everything {@link P2PGame#connect(ConnectOptions)} needs. Only {@code server}
 * and {@code code} are required; the fluent setters cover the rest, e.g.
 *
 * <pre>{@code
 * P2PGame game = P2PGame.connect(new ConnectOptions(
 *         "https://pqrstuvw.xyz/lobbylink", "MYROOM")
 *     .create(new CreateOptions(2)));   // create the room if it doesn't exist
 * }</pre>
 */
public final class ConnectOptions {
    /** "https://host[:port][/path]" or "wss://host[:port][/path]/ws". */
    public String server;
    /** Room code: 4-64 chars of [A-Za-z0-9_-]. */
    public String code;
    /** Optional app-policy id for hosted static sites. */
    public String appId = null;
    /** Create the room if it does not exist; leave null to only join. */
    public CreateOptions create = null;
    /** Explicit resume token; overrides any stored one. */
    public String resumeToken = null;
    /** Claim a specific slot after the resume token is gone (claim-slot). */
    public Integer claimPlayerId = null;
    /** Extra ICE servers, appended to the ones the server issues. */
    public List<IceServer> iceServers = new ArrayList<>();
    /** Force TURN relay (ICE transport policy "relay"); for TURN testing. */
    public boolean forceRelay = false;
    /**
     * File used for automatic resume-token persistence. Use a per-process /
     * per-instance path: two clients sharing one token file supersede each
     * other. Null disables persistence.
     */
    public Path storagePath = null;
    /**
     * Origin header for the WebSocket handshake. The server allowlists origins;
     * null (the default) uses the http(s) origin of {@code server}, which is
     * allowlisted on servers that also host their own web client. An empty
     * string omits the header entirely — for local servers run with
     * {@code --allow-no-origin}.
     */
    public String origin = null;
    /** Print internal warnings (dropped frames, failed peers) to stderr. */
    public boolean verbose = false;

    public ConnectOptions(String server, String code) {
        this.server = server;
        this.code = code;
    }

    public ConnectOptions appId(String v) { this.appId = v; return this; }
    public ConnectOptions create(CreateOptions v) { this.create = v; return this; }
    public ConnectOptions resumeToken(String v) { this.resumeToken = v; return this; }
    public ConnectOptions claimPlayerId(int v) { this.claimPlayerId = v; return this; }
    public ConnectOptions iceServers(List<IceServer> v) { this.iceServers = v; return this; }
    public ConnectOptions addIceServer(IceServer v) { this.iceServers.add(v); return this; }
    public ConnectOptions forceRelay(boolean v) { this.forceRelay = v; return this; }
    public ConnectOptions storagePath(Path v) { this.storagePath = v; return this; }
    public ConnectOptions origin(String v) { this.origin = v; return this; }
    public ConnectOptions verbose(boolean v) { this.verbose = v; return this; }
}
