package xyz.lobbylink.internal;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import dev.onvoid.webrtc.RTCDataChannel;
import dev.onvoid.webrtc.RTCPeerConnection;

/**
 * One WebRTC session to one remote player: the peer connection plus the two
 * pre-negotiated data channels. {@code lock} is notified when the reliable
 * channel opens/closes, when the link is torn down, and when the send buffer
 * drains below the low-water mark — that's how the per-peer sender thread waits.
 */
public final class PeerLink {
    public final int playerId;
    public final boolean initiator;
    /** Generation counter; callbacks from a torn-down session carry a stale epoch. */
    public final long epoch;
    public final RTCPeerConnection pc;
    public final RTCDataChannel reliable;
    public final RTCDataChannel bestEffort;

    public volatile boolean closed = false;
    public volatile boolean reliableOpen = false;
    /** Per-link reliable message counter; only touched by this peer's sender thread. */
    public int nextMsgId = 0;
    /** Notified on reliable open/close, link close, and buffer drain. */
    public final Object lock = new Object();

    // Actor-thread-only state:
    public final Reassembler reassembler = new Reassembler();
    /** Remote ICE candidates queued until the remote description is set; null once flushed. */
    public List<Map<String, Object>> pendingCandidates = new ArrayList<>();

    public PeerLink(int playerId, boolean initiator, long epoch,
                    RTCPeerConnection pc, RTCDataChannel reliable, RTCDataChannel bestEffort) {
        this.playerId = playerId;
        this.initiator = initiator;
        this.epoch = epoch;
        this.pc = pc;
        this.reliable = reliable;
        this.bestEffort = bestEffort;
    }

    public boolean isClosed() {
        return closed;
    }

    public void close() {
        closed = true;
        signal();
    }

    /** Wake anyone waiting on this link's state (sender thread). */
    public void signal() {
        synchronized (lock) {
            lock.notifyAll();
        }
    }
}
