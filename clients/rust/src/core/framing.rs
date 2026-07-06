//! Reliable-channel framing. Must match the TS client byte-for-byte:
//! one binary frame per SCTP message, big-endian:
//!
//! | offset | type | field      | value                              |
//! |-------:|------|------------|------------------------------------|
//! | 0      | u8   | magic      | 0x4C ('L')                         |
//! | 1      | u8   | version    | 0x01                               |
//! | 2      | u32  | msgId      | per-sender counter, wraps mod 2^32 |
//! | 6      | u32  | seq        | chunk index, 0-based               |
//! | 10     | u32  | total      | chunk count, >= 1                  |
//! | 14     | u32  | payloadLen | payload bytes in this frame        |
//! | 18     | ...  | payload    |                                    |

pub const FRAME_MAGIC: u8 = 0x4C;
pub const FRAME_VERSION: u8 = 0x01;
pub const FRAME_HEADER_LEN: usize = 18;
/// Payload bytes per reliable chunk we send.
pub const CHUNK_PAYLOAD: usize = 16 * 1024;
/// A received frame may not carry more payload than this.
pub const MAX_FRAME_PAYLOAD: usize = 64 * 1024;
/// Send- and receive-side cap on one reliable message.
pub const MAX_RELIABLE_MESSAGE: usize = 16 * 1024 * 1024;
pub const MAX_REASSEMBLY_CHUNKS: u32 = 4096;

pub fn make_frame(msg_id: u32, seq: u32, total: u32, payload: &[u8]) -> Vec<u8> {
    let mut frame = Vec::with_capacity(FRAME_HEADER_LEN + payload.len());
    frame.push(FRAME_MAGIC);
    frame.push(FRAME_VERSION);
    frame.extend_from_slice(&msg_id.to_be_bytes());
    frame.extend_from_slice(&seq.to_be_bytes());
    frame.extend_from_slice(&total.to_be_bytes());
    frame.extend_from_slice(&(payload.len() as u32).to_be_bytes());
    frame.extend_from_slice(payload);
    frame
}

/// Number of chunks for a payload of `len` bytes (a 0-byte message is
/// one frame with payloadLen 0).
pub fn chunk_count(len: usize) -> u32 {
    if len == 0 {
        1
    } else {
        len.div_ceil(CHUNK_PAYLOAD) as u32
    }
}

#[derive(Debug, PartialEq, Eq)]
pub struct Frame<'a> {
    pub msg_id: u32,
    pub seq: u32,
    pub total: u32,
    pub payload: &'a [u8],
}

fn be_u32(buf: &[u8], off: usize) -> u32 {
    u32::from_be_bytes([buf[off], buf[off + 1], buf[off + 2], buf[off + 3]])
}

/// Parse one received frame; the error string describes why it must
/// be dropped (receivers warn and drop, never disconnect).
pub fn parse_frame(buf: &[u8]) -> Result<Frame<'_>, &'static str> {
    if buf.len() < FRAME_HEADER_LEN {
        return Err("frame shorter than header");
    }
    if buf[0] != FRAME_MAGIC {
        return Err("bad frame magic");
    }
    if buf[1] != FRAME_VERSION {
        return Err("unsupported frame version");
    }
    let msg_id = be_u32(buf, 2);
    let seq = be_u32(buf, 6);
    let total = be_u32(buf, 10);
    let payload_len = be_u32(buf, 14) as usize;
    if !(1..=MAX_REASSEMBLY_CHUNKS).contains(&total) {
        return Err("bad frame total");
    }
    if seq >= total {
        return Err("frame seq out of range");
    }
    if payload_len > MAX_FRAME_PAYLOAD {
        return Err("frame payload too large");
    }
    if payload_len != buf.len() - FRAME_HEADER_LEN {
        return Err("frame payload length mismatch");
    }
    Ok(Frame {
        msg_id,
        seq,
        total,
        payload: &buf[FRAME_HEADER_LEN..],
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip() {
        let frame = make_frame(7, 2, 5, b"hello");
        assert_eq!(frame.len(), FRAME_HEADER_LEN + 5);
        let parsed = parse_frame(&frame).unwrap();
        assert_eq!(
            parsed,
            Frame { msg_id: 7, seq: 2, total: 5, payload: b"hello" }
        );
    }

    #[test]
    fn zero_byte_payload() {
        let frame = make_frame(0, 0, 1, b"");
        let parsed = parse_frame(&frame).unwrap();
        assert_eq!(parsed.payload, b"");
        assert_eq!(parsed.total, 1);
    }

    #[test]
    fn header_layout_is_exact() {
        // Byte-for-byte check against the TS reference layout.
        let frame = make_frame(0x01020304, 0x05060708, 0x090A0B0C, &[0xAA]);
        assert_eq!(
            frame,
            vec![
                0x4C, 0x01, // magic, version
                0x01, 0x02, 0x03, 0x04, // msgId BE
                0x05, 0x06, 0x07, 0x08, // seq BE
                0x09, 0x0A, 0x0B, 0x0C, // total BE
                0x00, 0x00, 0x00, 0x01, // payloadLen BE
                0xAA,
            ]
        );
    }

    #[test]
    fn rejects_bad_frames() {
        assert_eq!(parse_frame(&[0u8; 4]), Err("frame shorter than header"));
        let mut f = make_frame(1, 0, 1, b"x");
        f[0] = 0x4D;
        assert_eq!(parse_frame(&f), Err("bad frame magic"));
        let mut f = make_frame(1, 0, 1, b"x");
        f[1] = 0x02;
        assert_eq!(parse_frame(&f), Err("unsupported frame version"));
        // total == 0
        let mut f = make_frame(1, 0, 0, b"x");
        f[6..10].copy_from_slice(&0u32.to_be_bytes()); // seq 0
        assert_eq!(parse_frame(&f), Err("bad frame total"));
        // total > 4096
        let f = make_frame(1, 0, 4097, b"x");
        assert_eq!(parse_frame(&f), Err("bad frame total"));
        // seq >= total
        let f = make_frame(1, 3, 3, b"x");
        assert_eq!(parse_frame(&f), Err("frame seq out of range"));
        // length mismatch
        let mut f = make_frame(1, 0, 1, b"xy");
        f.pop();
        assert_eq!(parse_frame(&f), Err("frame payload length mismatch"));
    }

    #[test]
    fn chunk_counts() {
        assert_eq!(chunk_count(0), 1);
        assert_eq!(chunk_count(1), 1);
        assert_eq!(chunk_count(CHUNK_PAYLOAD), 1);
        assert_eq!(chunk_count(CHUNK_PAYLOAD + 1), 2);
        assert_eq!(chunk_count(300 * 1024), 19);
    }
}
