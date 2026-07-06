package xyz.lobbylink.internal;

import java.util.HashMap;
import java.util.Iterator;
import java.util.Map;

/**
 * Per-sender reassembly of chunked reliable messages, keyed by msgId. One
 * Reassembler per peer link (a rebuilt link restarts its msgId counter, so the
 * reassembler is recreated with it). Not thread-safe: driven only from the
 * actor thread.
 */
public final class Reassembler {
    /** Incomplete messages are dropped after this long. */
    public static final double TIMEOUT_MS = 30_000.0;

    private static final class Entry {
        int total;
        byte[][] chunks;
        int received;
        long bytes;
        double startedAtMs;
    }

    private final Map<Integer, Entry> inflight = new HashMap<>();

    /**
     * Feed one frame; returns the full message when it completes, else null.
     * {@code nowMs} is any monotonic-enough clock (used only for the 30 s
     * incomplete-message timeout).
     */
    public byte[] push(Framing.Frame frame, double nowMs) {
        prune(nowMs);
        Entry e = inflight.get(frame.msgId);
        if (e != null && e.total != frame.total) {
            // msgId reuse with different geometry: treat as a new message.
            inflight.remove(frame.msgId);
            e = null;
        }
        if (e == null) {
            e = new Entry();
            e.total = frame.total;
            e.chunks = new byte[frame.total][];
            e.startedAtMs = nowMs;
            inflight.put(frame.msgId, e);
        }
        if (e.chunks[frame.seq] == null) {
            e.chunks[frame.seq] = frame.payload;
            e.received++;
            e.bytes += frame.payload.length;
            if (e.bytes > Framing.MAX_RELIABLE_MESSAGE) {
                Log.warn("dropping oversized reliable message (" + e.bytes + " bytes)");
                inflight.remove(frame.msgId);
                return null;
            }
        }
        if (e.received < e.total) {
            return null;
        }
        inflight.remove(frame.msgId);
        byte[] out = new byte[(int) e.bytes];
        int p = 0;
        for (byte[] c : e.chunks) {
            System.arraycopy(c, 0, out, p, c.length);
            p += c.length;
        }
        return out;
    }

    private void prune(double nowMs) {
        Iterator<Map.Entry<Integer, Entry>> it = inflight.entrySet().iterator();
        while (it.hasNext()) {
            Map.Entry<Integer, Entry> me = it.next();
            if (nowMs - me.getValue().startedAtMs > TIMEOUT_MS) {
                Log.warn("dropping incomplete reliable message " + me.getKey() + " (timeout)");
                it.remove();
            }
        }
    }
}
