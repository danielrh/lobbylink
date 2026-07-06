package xyz.lobbylink.internal;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import org.junit.jupiter.api.Test;

class FramingTest {
    private static byte[] frame(int msgId, int seq, int total, byte[] payload) {
        return Framing.makeFrame(msgId, seq, total, payload, 0, payload.length);
    }

    @Test
    void roundtrip() throws Exception {
        byte[] f = frame(7, 2, 5, "hello".getBytes());
        assertEquals(Framing.HEADER_LEN + 5, f.length);
        Framing.Frame parsed = Framing.parse(f);
        assertEquals(7, parsed.msgId);
        assertEquals(2, parsed.seq);
        assertEquals(5, parsed.total);
        assertArrayEquals("hello".getBytes(), parsed.payload);
    }

    @Test
    void zeroBytePayload() throws Exception {
        Framing.Frame parsed = Framing.parse(frame(0, 0, 1, new byte[0]));
        assertEquals(0, parsed.payload.length);
        assertEquals(1, parsed.total);
    }

    @Test
    void headerLayoutIsExact() {
        // Byte-for-byte check against the shared wire layout.
        byte[] f = frame(0x01020304, 0x05060708, 0x090A0B0C, new byte[]{(byte) 0xAA});
        byte[] expected = {
                0x4C, 0x01,
                0x01, 0x02, 0x03, 0x04,
                0x05, 0x06, 0x07, 0x08,
                0x09, 0x0A, 0x0B, 0x0C,
                0x00, 0x00, 0x00, 0x01,
                (byte) 0xAA,
        };
        assertArrayEquals(expected, f);
    }

    @Test
    void rejectsBadFrames() {
        assertThrows(Framing.FramingException.class, () -> Framing.parse(new byte[4]));

        byte[] badMagic = frame(1, 0, 1, "x".getBytes());
        badMagic[0] = 0x4D;
        assertThrows(Framing.FramingException.class, () -> Framing.parse(badMagic));

        byte[] badVersion = frame(1, 0, 1, "x".getBytes());
        badVersion[1] = 0x02;
        assertThrows(Framing.FramingException.class, () -> Framing.parse(badVersion));

        byte[] totalTooBig = frame(1, 0, 4097, "x".getBytes());
        assertThrows(Framing.FramingException.class, () -> Framing.parse(totalTooBig));

        byte[] seqOutOfRange = frame(1, 3, 3, "x".getBytes());
        assertThrows(Framing.FramingException.class, () -> Framing.parse(seqOutOfRange));

        byte[] lenMismatch = frame(1, 0, 1, "xy".getBytes());
        byte[] shortened = new byte[lenMismatch.length - 1];
        System.arraycopy(lenMismatch, 0, shortened, 0, shortened.length);
        assertThrows(Framing.FramingException.class, () -> Framing.parse(shortened));
    }

    @Test
    void chunkCounts() {
        assertEquals(1, Framing.chunkCount(0));
        assertEquals(1, Framing.chunkCount(1));
        assertEquals(1, Framing.chunkCount(Framing.CHUNK_PAYLOAD));
        assertEquals(2, Framing.chunkCount(Framing.CHUNK_PAYLOAD + 1));
        assertEquals(19, Framing.chunkCount(300 * 1024));
    }
}
