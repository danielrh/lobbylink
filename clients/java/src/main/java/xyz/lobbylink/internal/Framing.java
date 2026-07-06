package xyz.lobbylink.internal;

import java.util.Arrays;

/**
 * Reliable-channel framing. Must match the other clients byte-for-byte: one
 * binary frame per SCTP message, big-endian:
 *
 * <pre>
 * | offset | type | field      | value                              |
 * |-------:|------|------------|------------------------------------|
 * | 0      | u8   | magic      | 0x4C ('L')                         |
 * | 1      | u8   | version    | 0x01                               |
 * | 2      | u32  | msgId      | per-sender counter, wraps mod 2^32 |
 * | 6      | u32  | seq        | chunk index, 0-based               |
 * | 10     | u32  | total      | chunk count, >= 1                  |
 * | 14     | u32  | payloadLen | payload bytes in this frame        |
 * | 18     | ...  | payload    |                                    |
 * </pre>
 */
public final class Framing {
    private Framing() {}

    public static final byte MAGIC = 0x4C;
    public static final byte VERSION = 0x01;
    public static final int HEADER_LEN = 18;
    /** Payload bytes per reliable chunk we send. */
    public static final int CHUNK_PAYLOAD = 16 * 1024;
    /** A received frame may not carry more payload than this. */
    public static final int MAX_FRAME_PAYLOAD = 64 * 1024;
    /** Send- and receive-side cap on one reliable message. */
    public static final int MAX_RELIABLE_MESSAGE = 16 * 1024 * 1024;
    public static final int MAX_REASSEMBLY_CHUNKS = 4096;

    public static byte[] makeFrame(int msgId, int seq, int total, byte[] payload, int off, int len) {
        byte[] f = new byte[HEADER_LEN + len];
        f[0] = MAGIC;
        f[1] = VERSION;
        putU32(f, 2, msgId);
        putU32(f, 6, seq);
        putU32(f, 10, total);
        putU32(f, 14, len);
        System.arraycopy(payload, off, f, HEADER_LEN, len);
        return f;
    }

    /** Number of chunks for a payload of {@code len} bytes (0 bytes is one frame). */
    public static int chunkCount(int len) {
        return len == 0 ? 1 : ((len + CHUNK_PAYLOAD - 1) / CHUNK_PAYLOAD);
    }

    public static final class Frame {
        public final int msgId;
        public final int seq;
        public final int total;
        public final byte[] payload;

        Frame(int msgId, int seq, int total, byte[] payload) {
            this.msgId = msgId;
            this.seq = seq;
            this.total = total;
            this.payload = payload;
        }
    }

    /** Thrown for a frame that must be dropped (receivers warn and drop, never disconnect). */
    public static final class FramingException extends Exception {
        private static final long serialVersionUID = 1L;
        FramingException(String m) {
            super(m);
        }
    }

    public static Frame parse(byte[] buf) throws FramingException {
        if (buf.length < HEADER_LEN) throw new FramingException("frame shorter than header");
        if (buf[0] != MAGIC) throw new FramingException("bad frame magic");
        if (buf[1] != VERSION) throw new FramingException("unsupported frame version");
        long msgId = getU32(buf, 2);
        long seq = getU32(buf, 6);
        long total = getU32(buf, 10);
        long payloadLen = getU32(buf, 14);
        if (total < 1 || total > MAX_REASSEMBLY_CHUNKS) throw new FramingException("bad frame total");
        if (seq >= total) throw new FramingException("frame seq out of range");
        if (payloadLen > MAX_FRAME_PAYLOAD) throw new FramingException("frame payload too large");
        if (payloadLen != buf.length - HEADER_LEN) throw new FramingException("frame payload length mismatch");
        byte[] payload = Arrays.copyOfRange(buf, HEADER_LEN, buf.length);
        return new Frame((int) msgId, (int) seq, (int) total, payload);
    }

    static void putU32(byte[] b, int o, int v) {
        b[o] = (byte) (v >>> 24);
        b[o + 1] = (byte) (v >>> 16);
        b[o + 2] = (byte) (v >>> 8);
        b[o + 3] = (byte) v;
    }

    static long getU32(byte[] b, int o) {
        return ((long) (b[o] & 0xFF) << 24)
                | ((b[o + 1] & 0xFF) << 16)
                | ((b[o + 2] & 0xFF) << 8)
                | (b[o + 3] & 0xFF);
    }
}
