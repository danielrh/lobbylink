package xyz.lobbylink;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicReference;

import dev.onvoid.webrtc.PeerConnectionFactory;
import dev.onvoid.webrtc.RTCIceServer;

import xyz.lobbylink.internal.Actor;
import xyz.lobbylink.internal.Framing;
import xyz.lobbylink.internal.Json;
import xyz.lobbylink.internal.Limits;
import xyz.lobbylink.internal.Log;
import xyz.lobbylink.internal.Protocol;
import xyz.lobbylink.internal.Roster;
import xyz.lobbylink.internal.Signaling;
import xyz.lobbylink.internal.SignalingUrl;
import xyz.lobbylink.internal.Storage;

/**
 * A joined lobby: roster/peer events plus a reliable and a best-effort
 * DataChannel to every other player. Create one with
 * {@link #connect(ConnectOptions)}, drive it by looping on {@link #nextEvent()},
 * and always {@link #close()} it (or use try-with-resources).
 *
 * <pre>{@code
 * try (P2PGame game = P2PGame.connect(
 *         new ConnectOptions("https://pqrstuvw.xyz/lobbylink", "MYROOM")
 *             .create(new CreateOptions(2)))) {
 *     Event ev;
 *     while ((ev = game.nextEvent()) != null) {
 *         if (ev instanceof Event.PeerState ps && ps.state().equals("connected")) {
 *             game.sendReliable(ps.playerId(), "hello".getBytes());
 *         } else if (ev instanceof Event.Message msg) {
 *             System.out.println("from " + msg.from() + ": " + new String(msg.data()));
 *         }
 *     }
 * }
 * }</pre>
 */
public final class P2PGame implements AutoCloseable {
    private static final Object POISON = new Object();

    private final String code;
    private final int selfId;
    private final int maxPlayers;
    private final String resumeToken;
    private final List<IceServer> iceServers;

    private final Actor actor;
    private final BlockingQueue<Object> events;
    private final AtomicReference<List<PlayerInfo>> rosterMirror;
    private final AtomicBoolean startedFlag;
    private final AtomicBoolean closedFlag;
    private final AtomicBoolean closedByUser = new AtomicBoolean(false);

    private P2PGame(String code, int selfId, int maxPlayers, String resumeToken,
                    List<IceServer> iceServers, Actor actor, BlockingQueue<Object> events,
                    AtomicReference<List<PlayerInfo>> rosterMirror, AtomicBoolean startedFlag,
                    AtomicBoolean closedFlag) {
        this.code = code;
        this.selfId = selfId;
        this.maxPlayers = maxPlayers;
        this.resumeToken = resumeToken;
        this.iceServers = iceServers;
        this.actor = actor;
        this.events = events;
        this.rosterMirror = rosterMirror;
        this.startedFlag = startedFlag;
        this.closedFlag = closedFlag;
    }

    /**
     * Join (optionally creating) or claim a slot in a room. Blocks until the
     * lobby returns "joined" or an error; throws {@link LobbyException} with a
     * stable {@link LobbyException#getCode() code} on failure.
     */
    public static P2PGame connect(ConnectOptions opts) throws LobbyException {
        SignalingUrl.validateCode(opts.code);
        Log.setVerbose(opts.verbose);
        String url = SignalingUrl.normalize(opts.server);
        String origin = opts.origin != null ? opts.origin : SignalingUrl.defaultOrigin(url);

        Signaling sig = Signaling.connect(url, origin, Limits.CONNECT_TIMEOUT_MS);

        // All incoming messages funnel through one inbox; the handshake drains it
        // here for "joined", then the Actor's reader takes over — so no message
        // that arrives right after "joined" is lost.
        BlockingQueue<Object> inbox = new LinkedBlockingQueue<>();
        final Object closedSentinel = new Object();
        sig.setHandler(new Signaling.Handler() {
            @Override
            public void onText(String message) {
                inbox.add(message);
            }

            @Override
            public void onClosed() {
                inbox.add(closedSentinel);
            }
        });

        String storedToken = opts.resumeToken != null ? opts.resumeToken : Storage.load(opts.storagePath);
        String joinMsg = opts.claimPlayerId != null
                ? Protocol.claimSlot(opts.code, opts.claimPlayerId, opts.appId)
                : Protocol.join(opts.code, opts.appId, storedToken, opts.create);
        sig.sendText(joinMsg);

        Map<String, Object> joined = awaitJoined(sig, inbox, closedSentinel, url);

        String rcode = Json.str(joined, "code", opts.code);
        int selfId = Json.intVal(joined, "selfId", 0);
        int maxPlayers = Json.intVal(joined, "maxPlayers", 0);
        boolean started = Json.bool(joined, "started", false);
        String resumeToken = Json.str(joined, "resumeToken", "");
        List<IceServer> serverIce = parseIceServers(Json.asArray(joined.get("iceServers")));
        Storage.save(opts.storagePath, resumeToken);

        List<IceServer> allIce = new ArrayList<>(serverIce);
        allIce.addAll(opts.iceServers);
        List<RTCIceServer> rtcIce = toRtc(allIce);

        List<PlayerInfo> roster = Roster.fromSnapshot(maxPlayers, Json.asArray(joined.get("players")));

        BlockingQueue<Object> events = new LinkedBlockingQueue<>();
        AtomicReference<List<PlayerInfo>> rosterMirror = new AtomicReference<>(new ArrayList<>(roster));
        AtomicBoolean startedFlag = new AtomicBoolean(started);
        AtomicBoolean closedFlag = new AtomicBoolean(false);

        PeerConnectionFactory factory;
        try {
            factory = new PeerConnectionFactory();
        } catch (Throwable t) {
            sig.close();
            throw new LobbyException("webrtc-init-failed",
                    "could not initialize the native WebRTC library: " + t.getMessage());
        }

        Actor actor = new Actor(selfId, maxPlayers, sig, events, POISON, inbox, closedSentinel,
                rosterMirror, startedFlag, closedFlag, roster, rtcIce, opts.forceRelay,
                opts.storagePath, factory);
        actor.start();

        return new P2PGame(rcode, selfId, maxPlayers, resumeToken, Collections.unmodifiableList(allIce),
                actor, events, rosterMirror, startedFlag, closedFlag);
    }

    private static Map<String, Object> awaitJoined(Signaling sig, BlockingQueue<Object> inbox,
                                                   Object closedSentinel, String url) throws LobbyException {
        long deadline = System.currentTimeMillis() + Limits.CONNECT_TIMEOUT_MS;
        while (true) {
            long rem = deadline - System.currentTimeMillis();
            if (rem <= 0) {
                sig.close();
                throw new LobbyException("connect-timeout", "timed out connecting to " + url);
            }
            Object item;
            try {
                item = inbox.poll(rem, TimeUnit.MILLISECONDS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                sig.close();
                throw new LobbyException("connection-failed", "interrupted while connecting to " + url);
            }
            if (item == null) {
                sig.close();
                throw new LobbyException("connect-timeout", "timed out connecting to " + url);
            }
            if (item == closedSentinel) {
                sig.close();
                throw new LobbyException("connection-closed", "connection closed before join completed");
            }
            Map<String, Object> m;
            try {
                m = Json.asObject(Json.parse((String) item));
            } catch (Exception e) {
                sig.close();
                throw new LobbyException("invalid-message", "server sent malformed JSON");
            }
            if (m == null) continue;
            String type = Json.str(m, "type");
            if ("joined".equals(type)) {
                return m;
            }
            if ("error".equals(type)) {
                sig.close();
                throw new LobbyException(Json.str(m, "code", "error"), Json.str(m, "message", ""));
            }
            // Ignore anything else before "joined".
        }
    }

    // ------------------------------------------------------------- accessors

    public String code() {
        return code;
    }

    public int selfId() {
        return selfId;
    }

    public int maxPlayers() {
        return maxPlayers;
    }

    /** Rotates on every (re)join; persisted to storagePath if one was set. */
    public String resumeToken() {
        return resumeToken;
    }

    /** ICE servers in use: the server-issued set plus any you supplied. */
    public List<IceServer> iceServers() {
        return iceServers;
    }

    /** True once the room has reached its start condition. */
    public boolean started() {
        return startedFlag.get();
    }

    /** Snapshot of all room slots (one entry per slot, id == index). */
    public List<PlayerInfo> players() {
        return rosterMirror.get();
    }

    // ------------------------------------------------------------- sending

    /**
     * Send one datagram on the unordered, no-retransmit channel. Silently
     * dropped if the channel is not open or its buffer is full — that is the
     * best-effort contract. Throws only on caller mistakes: a bad target or a
     * payload over 16000 bytes.
     */
    public void sendBestEffort(int to, byte[] data) throws LobbyException {
        Roster.checkTarget(to, selfId, maxPlayers);
        if (data.length > Limits.MAX_BEST_EFFORT) {
            throw new LobbyException("message-too-large",
                    "best-effort payload " + data.length + " exceeds " + Limits.MAX_BEST_EFFORT + " bytes");
        }
        actor.submitBestEffort(to, data.clone());
    }

    /** {@link #sendBestEffort} to every other occupied slot. */
    public void broadcastBestEffort(byte[] data) throws LobbyException {
        if (data.length > Limits.MAX_BEST_EFFORT) {
            throw new LobbyException("message-too-large",
                    "best-effort payload " + data.length + " exceeds " + Limits.MAX_BEST_EFFORT + " bytes");
        }
        actor.submitBroadcast(data.clone());
    }

    /**
     * Send a reliable, ordered message (chunked over the reliable channel, up to
     * 16 MiB). Blocks until every chunk has been handed to the transport; throws
     * if the peer link cannot be established or dies mid-send. Sends to one peer
     * are serialized and keep their order.
     */
    public void sendReliable(int to, byte[] data) throws LobbyException {
        Roster.checkTarget(to, selfId, maxPlayers);
        if (closedFlag.get()) {
            throw new LobbyException("closed", "game is closed");
        }
        List<PlayerInfo> r = rosterMirror.get();
        if (to >= r.size() || !r.get(to).occupied()) {
            throw new LobbyException("target-unavailable", "no player in slot " + to);
        }
        if (data.length > Framing.MAX_RELIABLE_MESSAGE) {
            throw new LobbyException("message-too-large",
                    "reliable payload " + data.length + " exceeds " + Framing.MAX_RELIABLE_MESSAGE + " bytes");
        }
        CompletableFuture<Void> done = actor.submitReliable(to, data.clone());
        try {
            done.get();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new LobbyException("send-failed", "reliable send interrupted");
        } catch (ExecutionException e) {
            Throwable c = e.getCause();
            if (c instanceof LobbyException le) throw le;
            throw new LobbyException("send-failed", c == null ? "send failed" : c.getMessage());
        }
    }

    // ------------------------------------------------------------- events

    /**
     * Block for the next lobby/peer/message event. Returns null once the game is
     * closed (after which it always returns null).
     */
    public Event nextEvent() throws InterruptedException {
        Object o = events.take();
        if (o == POISON) {
            events.add(POISON); // keep the sentinel for any later callers
            return null;
        }
        return (Event) o;
    }

    /**
     * Like {@link #nextEvent()} but gives up after {@code timeoutMillis}.
     * Returns null on timeout OR when the game is closed.
     */
    public Event nextEvent(long timeoutMillis) throws InterruptedException {
        Object o = events.poll(timeoutMillis, TimeUnit.MILLISECONDS);
        if (o == null) return null; // timeout
        if (o == POISON) {
            events.add(POISON);
            return null;
        }
        return (Event) o;
    }

    // ------------------------------------------------------------- close

    /**
     * Leave the room and release all resources: sends an explicit leave (freeing
     * our slot), tears down every peer connection, and clears any stored resume
     * token. Safe to call more than once. After this, {@link #nextEvent()}
     * returns null.
     */
    @Override
    public void close() {
        if (closedByUser.getAndSet(true)) {
            return;
        }
        actor.requestClose();
        try {
            actor.shutdownFuture().get(5, TimeUnit.SECONDS);
        } catch (Exception ignore) {
            // proceed to release executors regardless
        }
        actor.stopExecutors();
    }

    // ------------------------------------------------------------- ICE helpers

    private static List<IceServer> parseIceServers(List<Object> arr) {
        List<IceServer> out = new ArrayList<>();
        if (arr == null) return out;
        for (Object o : arr) {
            Map<String, Object> m = Json.asObject(o);
            if (m == null) continue;
            List<String> urls = new ArrayList<>();
            Object u = m.get("urls");
            if (u instanceof String s) {
                urls.add(s);
            } else {
                List<Object> list = Json.asArray(u);
                if (list != null) {
                    for (Object item : list) {
                        if (item != null) urls.add(item.toString());
                    }
                }
            }
            out.add(new IceServer(urls, Json.str(m, "username"), Json.str(m, "credential")));
        }
        return out;
    }

    private static List<RTCIceServer> toRtc(List<IceServer> list) {
        List<RTCIceServer> out = new ArrayList<>();
        for (IceServer s : list) {
            RTCIceServer r = new RTCIceServer();
            r.urls = new ArrayList<>(s.urls);
            if (s.username != null) r.username = s.username;
            if (s.credential != null) r.password = s.credential;
            out.add(r);
        }
        return out;
    }
}
