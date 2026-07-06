package xyz.lobbylink.internal;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

import org.junit.jupiter.api.Test;

class ReassemblerTest {
    private static byte[] push(Reassembler r, int msgId, int seq, int total, byte[] payload, double now)
            throws Exception {
        Framing.Frame f = Framing.parse(Framing.makeFrame(msgId, seq, total, payload, 0, payload.length));
        return r.push(f, now);
    }

    @Test
    void singleFrameMessage() throws Exception {
        Reassembler r = new Reassembler();
        assertArrayEquals("hi".getBytes(), push(r, 0, 0, 1, "hi".getBytes(), 0));
    }

    @Test
    void outOfOrderAndDuplicates() throws Exception {
        Reassembler r = new Reassembler();
        assertNull(push(r, 5, 2, 3, "c".getBytes(), 0));
        assertNull(push(r, 5, 0, 3, "a".getBytes(), 1));
        assertNull(push(r, 5, 0, 3, "X".getBytes(), 2)); // duplicate seq ignored
        assertArrayEquals("abc".getBytes(), push(r, 5, 1, 3, "b".getBytes(), 3));
    }

    @Test
    void interleavedMessages() throws Exception {
        Reassembler r = new Reassembler();
        assertNull(push(r, 1, 0, 2, "a".getBytes(), 0));
        assertNull(push(r, 2, 0, 2, "x".getBytes(), 0));
        assertArrayEquals("xy".getBytes(), push(r, 2, 1, 2, "y".getBytes(), 0));
        assertArrayEquals("ab".getBytes(), push(r, 1, 1, 2, "b".getBytes(), 0));
    }

    @Test
    void msgIdReuseWithNewGeometryRestarts() throws Exception {
        Reassembler r = new Reassembler();
        assertNull(push(r, 9, 0, 3, "a".getBytes(), 0));
        assertNull(push(r, 9, 0, 2, "x".getBytes(), 1)); // different total: fresh message
        assertArrayEquals("xy".getBytes(), push(r, 9, 1, 2, "y".getBytes(), 2));
    }

    @Test
    void incompleteTimesOut() throws Exception {
        Reassembler r = new Reassembler();
        assertNull(push(r, 3, 0, 2, "a".getBytes(), 0));
        // Past the 30 s window the half-built message is pruned, so the second
        // chunk starts a new (incomplete) entry...
        assertNull(push(r, 3, 1, 2, "b".getBytes(), 40_000));
        // ...which the next chunk completes.
        assertArrayEquals("ab".getBytes(), push(r, 3, 0, 2, "a".getBytes(), 40_001));
    }

    @Test
    void zeroByteMessage() throws Exception {
        Reassembler r = new Reassembler();
        assertArrayEquals(new byte[0], push(r, 0, 0, 1, new byte[0], 0));
    }
}
