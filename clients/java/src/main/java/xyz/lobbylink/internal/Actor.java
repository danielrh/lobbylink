package xyz.lobbylink.internal;

import java.nio.ByteBuffer;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ThreadFactory;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicReference;

import dev.onvoid.webrtc.CreateSessionDescriptionObserver;
import dev.onvoid.webrtc.PeerConnectionFactory;
import dev.onvoid.webrtc.PeerConnectionObserver;
import dev.onvoid.webrtc.RTCAnswerOptions;
import dev.onvoid.webrtc.RTCConfiguration;
import dev.onvoid.webrtc.RTCDataChannel;
import dev.onvoid.webrtc.RTCDataChannelBuffer;
import dev.onvoid.webrtc.RTCDataChannelInit;
import dev.onvoid.webrtc.RTCDataChannelObserver;
import dev.onvoid.webrtc.RTCDataChannelState;
import dev.onvoid.webrtc.RTCIceCandidate;
import dev.onvoid.webrtc.RTCIceServer;
import dev.onvoid.webrtc.RTCIceTransportPolicy;
import dev.onvoid.webrtc.RTCOfferOptions;
import dev.onvoid.webrtc.RTCPeerConnection;
import dev.onvoid.webrtc.RTCPeerConnectionState;
import dev.onvoid.webrtc.RTCSdpType;
import dev.onvoid.webrtc.RTCSessionDescription;
import dev.onvoid.webrtc.RTCSignalingState;
import dev.onvoid.webrtc.RTCStats;
import dev.onvoid.webrtc.RTCStatsType;

import xyz.lobbylink.Event;
import xyz.lobbylink.LobbyException;
import xyz.lobbylink.MessageKind;
import xyz.lobbylink.PlayerInfo;
import xyz.lobbylink.PlayerLeftReason;

/**
 * Single-owner actor: all lobby/roster/peer state lives here and is only ever
 * touched on {@link #exec}, a single actor thread. Public API calls, the
 * signaling reader, and WebRTC callbacks all hand work to it via {@link #post},
 * so callbacks can never race the state or deadlock against it. Reliable sends
 * run on a dedicated per-peer thread that serializes chunks and applies
 * backpressure.
 */
public final class Actor {
    private final ExecutorService exec;
    private final ScheduledExecutorService scheduler;
    private final ExecutorService pcCloser;

    private final int selfId;
    private final int maxPlayers;
    private final Signaling signaling;
    private final BlockingQueue<Object> events;
    private final Object poison;
    private final BlockingQueue<Object> inbox;
    private final Object closedSentinel;

    private final AtomicReference<List<PlayerInfo>> rosterMirror;
    private final AtomicBoolean startedFlag;
    private final AtomicBoolean closedFlag;

    private final List<RTCIceServer> iceServers;
    private final boolean forceRelay;
    private final Path storagePath;

    private final PeerConnectionFactory factory;

    // Actor-thread-only state:
    private List<PlayerInfo> roster;
    private final Map<Integer, PeerLink> peers = new HashMap<>();
    private final Map<Integer, SendChannel> sendChannels = new HashMap<>();
    private final Map<Integer, Integer> rebuildCounts = new HashMap<>();
    private long epochCounter = 0;
    private boolean fatalSeen = false;
    private boolean closed = false;
    private boolean shuttingDown = false;

    private Thread reader;
    private final CompletableFuture<Void> shutdownFuture = new CompletableFuture<>();

    public Actor(int selfId, int maxPlayers, Signaling signaling,
                 BlockingQueue<Object> events, Object poison,
                 BlockingQueue<Object> inbox, Object closedSentinel,
                 AtomicReference<List<PlayerInfo>> rosterMirror, AtomicBoolean startedFlag,
                 AtomicBoolean closedFlag, List<PlayerInfo> roster,
                 List<RTCIceServer> iceServers, boolean forceRelay, Path storagePath,
                 PeerConnectionFactory factory) {
        this.selfId = selfId;
        this.maxPlayers = maxPlayers;
        this.signaling = signaling;
        this.events = events;
        this.poison = poison;
        this.inbox = inbox;
        this.closedSentinel = closedSentinel;
        this.rosterMirror = rosterMirror;
        this.startedFlag = startedFlag;
        this.closedFlag = closedFlag;
        this.roster = roster;
        this.iceServers = iceServers;
        this.forceRelay = forceRelay;
        this.storagePath = storagePath;
        this.factory = factory;
        this.exec = Executors.newSingleThreadExecutor(daemon("lobbylink-actor"));
        this.scheduler = Executors.newSingleThreadScheduledExecutor(daemon("lobbylink-sched"));
        this.pcCloser = Executors.newCachedThreadPool(daemon("lobbylink-pc-close"));
    }

    private static ThreadFactory daemon(String name) {
        return r -> {
            Thread t = new Thread(r, name);
            t.setDaemon(true);
            return t;
        };
    }

    private static double nowMs() {
        return System.nanoTime() / 1_000_000.0;
    }

    private void post(Runnable r) {
        try {
            exec.execute(r);
        } catch (RejectedExecutionException e) {
            // actor already shut down; drop.
        }
    }

    /** Kick off: launch the signaling reader and send the initial offers. */
    public void start() {
        reader = new Thread(() -> {
            try {
                while (true) {
                    Object item = inbox.take();
                    if (item == closedSentinel) {
                        post(this::handleWsClosed);
                        break;
                    }
                    String text = (String) item;
                    post(() -> handleServerText(text));
                }
            } catch (InterruptedException e) {
                // shutting down
            }
        }, "lobbylink-reader");
        reader.setDaemon(true);
        reader.start();

        post(() -> {
            // Lower ID initiates: offer to every occupied+connected peer with a
            // higher ID. Lower-id peers will offer to us on their player-joined.
            for (PlayerInfo p : roster) {
                if (p.id() != selfId && p.occupied() && p.connected() && selfId < p.id()) {
                    initiatePeer(p.id());
                }
            }
        });
    }

    public CompletableFuture<Void> shutdownFuture() {
        return shutdownFuture;
    }

    public CompletableFuture<Void> requestClose() {
        post(this::doShutdown);
        return shutdownFuture;
    }

    /** Free the actor's executors after the shutdown future has completed. */
    public void stopExecutors() {
        if (reader != null) reader.interrupt();
        scheduler.shutdownNow();
        exec.shutdownNow();
        pcCloser.shutdownNow();
    }

    // ------------------------------------------------------------- API commands

    public CompletableFuture<Void> submitReliable(int to, byte[] data) {
        CompletableFuture<Void> done = new CompletableFuture<>();
        post(() -> {
            if (closed) {
                done.completeExceptionally(closedException());
                return;
            }
            SendChannel ch = sendChannelFor(to);
            ch.queue.add(new SendJob(data, done));
        });
        return done;
    }

    public void submitBestEffort(int to, byte[] data) {
        post(() -> bestEffortTo(to, data));
    }

    public void submitBroadcast(byte[] data) {
        post(() -> {
            for (PlayerInfo p : roster) {
                if (p.id() != selfId && p.occupied()) {
                    bestEffortTo(p.id(), data.clone());
                }
            }
        });
    }

    /** Best-effort contract: silently dropped with no open channel or a full buffer. */
    private void bestEffortTo(int to, byte[] data) {
        PeerLink link = peers.get(to);
        if (link == null || link.isClosed() || link.bestEffort.getState() != RTCDataChannelState.OPEN) {
            return;
        }
        if (link.bestEffort.getBufferedAmount() > Limits.SEND_HIGH_WATER) {
            return; // best-effort: drop
        }
        try {
            link.bestEffort.send(new RTCDataChannelBuffer(ByteBuffer.wrap(data), true));
        } catch (Exception e) {
            // best-effort: drop
        }
    }

    // ------------------------------------------------------------- teardown

    private void doShutdown() {
        if (shuttingDown) return;
        shuttingDown = true;
        closed = true;
        closedFlag.set(true);
        signaling.sendText(Protocol.leave());
        signaling.close();
        teardownPeers();
        Storage.clear(storagePath);
        // Wait for peer connections to finish closing, then release the factory.
        pcCloser.shutdown();
        try {
            pcCloser.awaitTermination(3, TimeUnit.SECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        try {
            factory.dispose();
        } catch (Exception ignore) {
            // native cleanup, best effort
        }
        events.add(poison);
        shutdownFuture.complete(null);
    }

    private void teardownPeers() {
        // Stop senders first so they no longer touch channels, then close peers.
        for (SendChannel ch : sendChannels.values()) {
            stopSender(ch);
        }
        sendChannels.clear();
        List<Integer> ids = new ArrayList<>(peers.keySet());
        for (int id : ids) {
            closePeer(id);
        }
    }

    private void closePeer(int playerId) {
        PeerLink link = peers.remove(playerId);
        if (link == null) return;
        link.close();
        SendChannel ch = sendChannels.get(playerId);
        if (ch != null) ch.setLink(null);
        RTCPeerConnection pc = link.pc;
        RTCDataChannel reliable = link.reliable;
        RTCDataChannel bestEffort = link.bestEffort;
        try {
            pcCloser.execute(() -> {
                try { reliable.unregisterObserver(); } catch (Exception ignore) {}
                try { bestEffort.unregisterObserver(); } catch (Exception ignore) {}
                try { pc.close(); } catch (Exception ignore) {}
            });
        } catch (RejectedExecutionException e) {
            // pcCloser already shut down; native objects get cleaned up on factory dispose.
        }
    }

    // ------------------------------------------------------------- events

    private void emit(Event event) {
        if (!closed) {
            events.add(event);
        }
    }

    private void syncRoster() {
        rosterMirror.set(new ArrayList<>(roster));
    }

    private void setSlot(int playerId, boolean occupied, boolean connected) {
        if (playerId >= 0 && playerId < roster.size()) {
            roster.set(playerId, new PlayerInfo(playerId, occupied, connected));
        }
    }

    // ------------------------------------------------------------- signaling in

    private void handleWsClosed() {
        if (!closed && !fatalSeen) {
            fatalSeen = true;
            emit(new Event.SignalingClosed("connection-lost",
                    "signaling connection lost; existing peer channels stay up"));
        }
    }

    private void handleServerText(String text) {
        if (closed) return;
        Object parsed;
        try {
            parsed = Json.parse(text);
        } catch (Exception e) {
            Log.warn("malformed server message");
            return;
        }
        Map<String, Object> m = Json.asObject(parsed);
        if (m == null) return;
        String type = Json.str(m, "type");
        if (type == null) return;
        switch (type) {
            case "player-joined": {
                int pid = Json.intVal(m, "playerId", -1);
                roster = Roster.fromSnapshot(maxPlayers, Json.asArray(m.get("players")));
                syncRoster();
                emit(new Event.PlayerJoined(pid));
                resetPeer(pid);
                break;
            }
            case "player-left": {
                int pid = Json.intVal(m, "playerId", -1);
                PlayerLeftReason reason = PlayerLeftReason.fromWire(Json.str(m, "reason", "disconnected"));
                if (pid >= 0 && pid < roster.size()) {
                    PlayerInfo cur = roster.get(pid);
                    boolean occ = reason == PlayerLeftReason.EXPLICIT_LEAVE ? false : cur.occupied();
                    roster.set(pid, new PlayerInfo(pid, occ, false));
                }
                syncRoster();
                if (reason == PlayerLeftReason.EXPLICIT_LEAVE) {
                    closePeer(pid);
                }
                // On "disconnected" the peer only lost signaling; a DataChannel may live on.
                emit(new Event.PlayerLeft(pid, reason));
                break;
            }
            case "player-rejoined": {
                int pid = Json.intVal(m, "playerId", -1);
                boolean wasReplacement = Json.bool(m, "wasReplacement", false);
                setSlot(pid, true, true);
                syncRoster();
                emit(new Event.PlayerRejoined(pid, wasReplacement));
                resetPeer(pid);
                break;
            }
            case "player-replaced": {
                int pid = Json.intVal(m, "playerId", -1);
                setSlot(pid, true, true);
                syncRoster();
                emit(new Event.PlayerReplaced(pid));
                resetPeer(pid);
                break;
            }
            case "room-started": {
                startedFlag.set(true);
                emit(new Event.Started());
                break;
            }
            case "signal": {
                int from = Json.intVal(m, "from", -1);
                handleSignal(from, Json.asObject(m.get("payload")));
                break;
            }
            case "error": {
                String code = Json.str(m, "code", "error");
                String message = Json.str(m, "message", "");
                if (Protocol.isFatalCode(code)) {
                    fatalSeen = true;
                    if (Protocol.isGameOverCode(code)) {
                        teardownPeers();
                        // "session-superseded" means our own token resumed from
                        // another process, which owns the new token — don't clobber.
                        if (!code.equals("session-superseded")) {
                            Storage.clear(storagePath);
                        }
                    }
                    emit(new Event.SignalingClosed(code, message));
                } else {
                    emit(new Event.LobbyError(code, message));
                }
                break;
            }
            // "joined" is handled at connect(); unknown types are ignored (forward compat).
            default:
                break;
        }
    }

    /** A peer got a new session: drop the old link, re-offer if we are the initiator. */
    private void resetPeer(int playerId) {
        if (playerId == selfId) return;
        closePeer(playerId);
        rebuildCounts.remove(playerId);
        if (selfId < playerId) {
            initiatePeer(playerId);
        }
    }

    // ------------------------------------------------------------- WebRTC signaling

    private void sendSignal(int to, Map<String, Object> payload) {
        if (closed) return;
        signaling.sendText(Protocol.signal(to, payload));
    }

    private void initiatePeer(int playerId) {
        if (closed) return;
        PeerLink link = createLink(playerId, true);
        if (link == null) return;
        long epoch = link.epoch;
        RTCPeerConnection pc = link.pc;
        pc.createOffer(new RTCOfferOptions(), new CreateSessionDescriptionObserver() {
            @Override
            public void onSuccess(RTCSessionDescription description) {
                pc.setLocalDescription(description, new SetOnly(() ->
                        post(() -> {
                            if (linkEpochCurrent(playerId, epoch)) {
                                sendSignal(playerId, Protocol.offer(description.sdp));
                            }
                        }),
                        err -> Log.warn("offer to player " + playerId + " failed: " + err)));
            }

            @Override
            public void onFailure(String error) {
                Log.warn("offer to player " + playerId + " failed: " + error);
            }
        });
    }

    private void handleSignal(int from, Map<String, Object> payload) {
        if (closed || from == selfId || payload == null) return;
        String kind = Json.str(payload, "kind");
        if (kind == null) return;
        switch (kind) {
            case "offer": {
                if (selfId < from) {
                    Log.warn("ignoring offer from higher-ID player " + from + " (protocol says we offer)");
                    return;
                }
                String sdp = Json.str(payload, "sdp");
                if (sdp == null) return;
                // Every incoming offer starts a fresh session.
                PeerLink link = createLink(from, false);
                if (link == null) return;
                long epoch = link.epoch;
                RTCPeerConnection pc = link.pc;
                pc.setRemoteDescription(new RTCSessionDescription(RTCSdpType.OFFER, sdp),
                        new SetOnly(() -> post(() -> onRemoteOfferSet(from, epoch)),
                                err -> Log.warn("signal (offer) from player " + from + " failed: " + err)));
                break;
            }
            case "answer": {
                PeerLink link = peers.get(from);
                if (link == null) {
                    Log.warn("ignoring stale answer from player " + from);
                    return;
                }
                if (link.isClosed() || link.pc.getSignalingState() != RTCSignalingState.HAVE_LOCAL_OFFER) {
                    Log.warn("ignoring stale answer from player " + from);
                    return;
                }
                String sdp = Json.str(payload, "sdp");
                if (sdp == null) return;
                long epoch = link.epoch;
                link.pc.setRemoteDescription(new RTCSessionDescription(RTCSdpType.ANSWER, sdp),
                        new SetOnly(() -> post(() -> {
                            if (linkEpochCurrent(from, epoch)) flushCandidates(from);
                        }), err -> Log.warn("signal (answer) from player " + from + " failed: " + err)));
                break;
            }
            case "ice": {
                Object cand = payload.get("candidate");
                if (cand == null) return; // null/absent = end-of-candidates
                Map<String, Object> candMap = Json.asObject(cand);
                if (candMap == null) return;
                PeerLink link = peers.get(from);
                if (link == null || link.isClosed()) return;
                if (link.pendingCandidates != null) {
                    link.pendingCandidates.add(candMap);
                } else {
                    addCandidate(link, candMap);
                }
                break;
            }
            default:
                break;
        }
    }

    private void onRemoteOfferSet(int from, long epoch) {
        if (!linkEpochCurrent(from, epoch)) return;
        flushCandidates(from);
        PeerLink link = peers.get(from);
        if (link == null) return;
        RTCPeerConnection pc = link.pc;
        pc.createAnswer(new RTCAnswerOptions(), new CreateSessionDescriptionObserver() {
            @Override
            public void onSuccess(RTCSessionDescription description) {
                pc.setLocalDescription(description, new SetOnly(() ->
                        post(() -> {
                            if (linkEpochCurrent(from, epoch)) {
                                sendSignal(from, Protocol.answer(description.sdp));
                            }
                        }),
                        err -> Log.warn("answer to player " + from + " failed: " + err)));
            }

            @Override
            public void onFailure(String error) {
                Log.warn("answer to player " + from + " failed: " + error);
            }
        });
    }

    private void flushCandidates(int playerId) {
        PeerLink link = peers.get(playerId);
        if (link == null || link.pendingCandidates == null) return;
        List<Map<String, Object>> pending = link.pendingCandidates;
        link.pendingCandidates = null;
        for (Map<String, Object> candidate : pending) {
            addCandidate(link, candidate);
        }
    }

    private void addCandidate(PeerLink link, Map<String, Object> candidate) {
        String sdp = Json.str(candidate, "candidate");
        if (sdp == null || sdp.isEmpty()) return;
        String sdpMid = Json.str(candidate, "sdpMid");
        int sdpMLineIndex = Json.intVal(candidate, "sdpMLineIndex", 0);
        try {
            link.pc.addIceCandidate(new RTCIceCandidate(sdpMid, sdpMLineIndex, sdp));
        } catch (Exception e) {
            if (!link.isClosed()) {
                Log.warn("addIceCandidate for player " + link.playerId + " failed: " + e.getMessage());
            }
        }
    }

    // ------------------------------------------------------------- link construction

    private PeerLink createLink(int playerId, boolean initiator) {
        closePeer(playerId);
        epochCounter++;
        long epoch = epochCounter;

        RTCConfiguration config = new RTCConfiguration();
        config.iceServers.addAll(iceServers);
        config.iceTransportPolicy = forceRelay ? RTCIceTransportPolicy.RELAY : RTCIceTransportPolicy.ALL;

        RTCPeerConnection pc;
        try {
            pc = factory.createPeerConnection(config, new PeerConnectionObserver() {
                @Override
                public void onIceCandidate(RTCIceCandidate candidate) {
                    if (candidate == null) return;
                    Map<String, Object> m = new java.util.LinkedHashMap<>();
                    m.put("candidate", candidate.sdp);
                    m.put("sdpMid", candidate.sdpMid);
                    m.put("sdpMLineIndex", (long) candidate.sdpMLineIndex);
                    post(() -> {
                        if (linkEpochCurrent(playerId, epoch)) {
                            sendSignal(playerId, Protocol.ice(m));
                        }
                    });
                }

                @Override
                public void onConnectionChange(RTCPeerConnectionState state) {
                    post(() -> handlePeerState(playerId, epoch, state));
                }
            });
        } catch (Exception e) {
            Log.warn("creating peer connection to player " + playerId + " failed: " + e.getMessage());
            return null;
        }
        if (pc == null) {
            Log.warn("creating peer connection to player " + playerId + " returned null");
            return null;
        }

        RTCDataChannelInit reliableInit = new RTCDataChannelInit();
        reliableInit.negotiated = true;
        reliableInit.id = Limits.RELIABLE_CHANNEL_ID;
        reliableInit.ordered = true;
        RTCDataChannel reliable = pc.createDataChannel(Limits.RELIABLE_LABEL, reliableInit);

        RTCDataChannelInit bestInit = new RTCDataChannelInit();
        bestInit.negotiated = true;
        bestInit.id = Limits.BEST_EFFORT_CHANNEL_ID;
        bestInit.ordered = false;
        bestInit.maxRetransmits = 0;
        RTCDataChannel bestEffort = pc.createDataChannel(Limits.BEST_EFFORT_LABEL, bestInit);

        PeerLink link = new PeerLink(playerId, initiator, epoch, pc, reliable, bestEffort);

        reliable.registerObserver(new RTCDataChannelObserver() {
            @Override
            public void onStateChange() {
                if (reliable.getState() == RTCDataChannelState.OPEN) {
                    link.reliableOpen = true;
                }
                link.signal();
            }

            @Override
            public void onMessage(RTCDataChannelBuffer buffer) {
                byte[] data = toBytes(buffer);
                post(() -> onReliableData(playerId, epoch, data));
            }

            @Override
            public void onBufferedAmountChange(long previousAmount) {
                if (reliable.getBufferedAmount() <= Limits.SEND_LOW_WATER) {
                    link.signal();
                }
            }
        });

        bestEffort.registerObserver(new RTCDataChannelObserver() {
            @Override
            public void onStateChange() {
            }

            @Override
            public void onMessage(RTCDataChannelBuffer buffer) {
                byte[] data = toBytes(buffer);
                post(() -> {
                    if (linkEpochCurrent(playerId, epoch)) {
                        emit(new Event.Message(playerId, MessageKind.BEST_EFFORT, data));
                    }
                });
            }

            @Override
            public void onBufferedAmountChange(long previousAmount) {
            }
        });

        peers.put(playerId, link);
        SendChannel ch = sendChannels.get(playerId);
        if (ch != null) ch.setLink(link);
        return link;
    }

    private boolean linkEpochCurrent(int playerId, long epoch) {
        PeerLink link = peers.get(playerId);
        return link != null && link.epoch == epoch && !link.isClosed();
    }

    private void onReliableData(int playerId, long epoch, byte[] data) {
        PeerLink link = peers.get(playerId);
        if (link == null || link.epoch != epoch) return;
        try {
            Framing.Frame frame = Framing.parse(data);
            byte[] message = link.reassembler.push(frame, nowMs());
            if (message != null) {
                emit(new Event.Message(playerId, MessageKind.RELIABLE, message));
            }
        } catch (Framing.FramingException e) {
            Log.warn("dropping reliable frame from player " + playerId + ": " + e.getMessage());
        }
    }

    private void handlePeerState(int playerId, long epoch, RTCPeerConnectionState state) {
        if (!linkEpochCurrent(playerId, epoch)) return;
        emit(new Event.PeerState(playerId, state.name().toLowerCase(Locale.ROOT)));
        switch (state) {
            case CONNECTED:
                rebuildCounts.remove(playerId);
                collectCandidatePair(playerId, epoch);
                break;
            case FAILED:
                handlePeerFailure(playerId, epoch);
                break;
            default:
                break;
        }
    }

    private void collectCandidatePair(int playerId, long epoch) {
        PeerLink link = peers.get(playerId);
        if (link == null) return;
        try {
            link.pc.getStats(report -> {
                String[] pair = selectCandidatePair(report.getStats().values());
                if (pair != null) {
                    post(() -> {
                        if (linkEpochCurrent(playerId, epoch)) {
                            emit(new Event.CandidatePair(playerId, pair[0], pair[1]));
                        }
                    });
                }
            });
        } catch (Exception e) {
            // stats are best-effort debug info
        }
    }

    private static String[] selectCandidatePair(java.util.Collection<RTCStats> stats) {
        Map<String, String> candidateType = new HashMap<>();
        List<Map<String, Object>> pairs = new ArrayList<>();
        for (RTCStats s : stats) {
            RTCStatsType t = s.getType();
            if (t == RTCStatsType.LOCAL_CANDIDATE || t == RTCStatsType.REMOTE_CANDIDATE) {
                Object ct = s.getAttributes().get("candidateType");
                if (ct != null) candidateType.put(s.getId(), ct.toString());
            } else if (t == RTCStatsType.CANDIDATE_PAIR) {
                pairs.add(s.getAttributes());
            }
        }
        Map<String, Object> chosen = null;
        for (Map<String, Object> p : pairs) {
            Object state = p.get("state");
            boolean succeeded = state != null && "succeeded".equalsIgnoreCase(state.toString());
            Object nominated = p.get("nominated");
            boolean nom = Boolean.TRUE.equals(nominated) || "true".equalsIgnoreCase(String.valueOf(nominated));
            if (succeeded && nom) {
                chosen = p;
                break;
            }
            if (succeeded && chosen == null) {
                chosen = p;
            }
        }
        if (chosen == null) return null;
        Object localId = chosen.get("localCandidateId");
        Object remoteId = chosen.get("remoteCandidateId");
        String local = candidateType.getOrDefault(String.valueOf(localId), "unknown");
        String remote = candidateType.getOrDefault(String.valueOf(remoteId), "unknown");
        return new String[]{local, remote};
    }

    /** Initiator rebuilds on failure with linear backoff, at most MAX_PEER_REBUILDS times. */
    private void handlePeerFailure(int playerId, long epoch) {
        PeerLink link = peers.get(playerId);
        if (link == null || !link.initiator || closed) return;
        int count = rebuildCounts.getOrDefault(playerId, 0) + 1;
        rebuildCounts.put(playerId, count);
        if (count > Limits.MAX_PEER_REBUILDS) {
            Log.warn("giving up on player " + playerId + " after " + Limits.MAX_PEER_REBUILDS + " rebuilds");
            return;
        }
        try {
            scheduler.schedule(() -> post(() -> rebuild(playerId, epoch)), 1000L * count, TimeUnit.MILLISECONDS);
        } catch (RejectedExecutionException e) {
            // shutting down
        }
    }

    private void rebuild(int playerId, long epoch) {
        if (closed || !linkEpochCurrent(playerId, epoch)) return;
        PeerLink link = peers.get(playerId);
        if (link == null || link.pc.getConnectionState() != RTCPeerConnectionState.FAILED) return;
        if (playerId < 0 || playerId >= roster.size()) return;
        PlayerInfo slot = roster.get(playerId);
        if (!slot.occupied() || !slot.connected()) return;
        initiatePeer(playerId);
    }

    private static byte[] toBytes(RTCDataChannelBuffer buffer) {
        ByteBuffer dup = buffer.data.duplicate();
        byte[] out = new byte[dup.remaining()];
        dup.get(out);
        return out;
    }

    // ------------------------------------------------------------- send queue

    private LobbyException closedException() {
        return new LobbyException("closed", "game is closed");
    }

    private SendChannel sendChannelFor(int playerId) {
        SendChannel ch = sendChannels.get(playerId);
        if (ch == null) {
            ch = new SendChannel(playerId);
            ch.link = peers.get(playerId); // may be null; sender waits for one
            sendChannels.put(playerId, ch);
            SendChannel started = ch;
            Thread t = new Thread(() -> runSender(started), "lobbylink-send-" + playerId);
            t.setDaemon(true);
            ch.thread = t;
            t.start();
        }
        return ch;
    }

    private void stopSender(SendChannel ch) {
        ch.shutdown = true;
        synchronized (ch.linkLock) {
            ch.linkLock.notifyAll();
        }
        if (ch.thread != null) ch.thread.interrupt();
    }

    private void runSender(SendChannel ch) {
        while (!ch.shutdown) {
            SendJob job;
            try {
                job = ch.queue.take();
            } catch (InterruptedException e) {
                break;
            }
            if (ch.shutdown) {
                job.done.completeExceptionally(closedException());
                break;
            }
            try {
                sendOne(ch, job.data);
                job.done.complete(null);
            } catch (LobbyException e) {
                job.done.completeExceptionally(e);
            }
        }
        // Fail anything still queued.
        List<SendJob> remaining = new ArrayList<>();
        ch.queue.drainTo(remaining);
        for (SendJob job : remaining) {
            job.done.completeExceptionally(closedException());
        }
    }

    private void sendOne(SendChannel ch, byte[] data) throws LobbyException {
        PeerLink link = waitLink(ch);
        waitReliableOpen(link);
        int msgId = link.nextMsgId++;
        int total = Framing.chunkCount(data.length);
        for (int seq = 0; seq < total; seq++) {
            if (link.isClosed()) {
                throw new LobbyException("send-failed",
                        "connection to player " + ch.playerId + " closed mid-send");
            }
            waitDrain(link);
            int start = seq * Framing.CHUNK_PAYLOAD;
            int end = Math.min(start + Framing.CHUNK_PAYLOAD, data.length);
            byte[] frame = Framing.makeFrame(msgId, seq, total, data, start, end - start);
            try {
                link.reliable.send(new RTCDataChannelBuffer(ByteBuffer.wrap(frame), true));
            } catch (Exception e) {
                throw new LobbyException("send-failed",
                        "send to player " + ch.playerId + " failed: " + e.getMessage());
            }
        }
    }

    private PeerLink waitLink(SendChannel ch) throws LobbyException {
        long deadline = System.currentTimeMillis() + Limits.CHANNEL_TIMEOUT_MS;
        synchronized (ch.linkLock) {
            while (true) {
                PeerLink l = ch.link;
                if (l != null && !l.isClosed()) return l;
                if (ch.shutdown) throw closedException();
                long rem = deadline - System.currentTimeMillis();
                if (rem <= 0) {
                    throw new LobbyException("channel-timeout",
                            "no WebRTC session with player " + ch.playerId + " within "
                                    + Limits.CHANNEL_TIMEOUT_MS + "ms");
                }
                try {
                    ch.linkLock.wait(rem);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    throw closedException();
                }
            }
        }
    }

    private void waitReliableOpen(PeerLink link) throws LobbyException {
        long deadline = System.currentTimeMillis() + Limits.CHANNEL_TIMEOUT_MS;
        synchronized (link.lock) {
            while (true) {
                if (link.isClosed()) {
                    throw new LobbyException("peer-closed", "channel to player " + link.playerId + " is closed");
                }
                RTCDataChannelState st = link.reliable.getState();
                if (st == RTCDataChannelState.OPEN) return;
                if (st == RTCDataChannelState.CLOSING || st == RTCDataChannelState.CLOSED) {
                    throw new LobbyException("peer-closed", "channel to player " + link.playerId + " closed");
                }
                long rem = deadline - System.currentTimeMillis();
                if (rem <= 0) {
                    throw new LobbyException("channel-timeout",
                            "timed out opening channel to player " + link.playerId);
                }
                try {
                    link.lock.wait(rem);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    throw closedException();
                }
            }
        }
    }

    private void waitDrain(PeerLink link) throws LobbyException {
        if (link.reliable.getBufferedAmount() <= Limits.SEND_HIGH_WATER) return;
        synchronized (link.lock) {
            while (true) {
                if (link.isClosed()) {
                    throw new LobbyException("peer-closed", "channel to player " + link.playerId + " closed");
                }
                if (link.reliable.getBufferedAmount() <= Limits.SEND_LOW_WATER) return;
                try {
                    // Poll fallback in case the low-water event never fires (teardown races).
                    link.lock.wait(Limits.DRAIN_POLL_MS);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    throw closedException();
                }
            }
        }
    }

    // ------------------------------------------------------------- helper types

    /** setLocalDescription / setRemoteDescription observer that just runs a callback. */
    private static final class SetOnly implements dev.onvoid.webrtc.SetSessionDescriptionObserver {
        private final Runnable onSuccess;
        private final java.util.function.Consumer<String> onFailure;

        SetOnly(Runnable onSuccess, java.util.function.Consumer<String> onFailure) {
            this.onSuccess = onSuccess;
            this.onFailure = onFailure;
        }

        @Override
        public void onSuccess() {
            onSuccess.run();
        }

        @Override
        public void onFailure(String error) {
            onFailure.accept(error);
        }
    }

    private static final class SendJob {
        final byte[] data;
        final CompletableFuture<Void> done;

        SendJob(byte[] data, CompletableFuture<Void> done) {
            this.data = data;
            this.done = done;
        }
    }

    /**
     * Per-peer reliable send queue and its current link. {@code link} is only
     * mutated on the actor thread (under linkLock, to publish to the sender).
     */
    private static final class SendChannel {
        final int playerId;
        final BlockingQueue<SendJob> queue = new LinkedBlockingQueue<>();
        final Object linkLock = new Object();
        volatile PeerLink link;
        volatile boolean shutdown = false;
        Thread thread;

        SendChannel(int playerId) {
            this.playerId = playerId;
        }

        void setLink(PeerLink l) {
            synchronized (linkLock) {
                link = l;
                linkLock.notifyAll();
            }
        }
    }
}
