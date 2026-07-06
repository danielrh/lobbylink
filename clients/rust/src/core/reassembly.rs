//! Per-sender reassembly of chunked reliable messages, keyed by
//! msgId. One Reassembler per peer link (a rebuilt link restarts its
//! msgId counter, so the reassembler must be recreated with it).

use std::collections::HashMap;

use super::framing::{Frame, MAX_RELIABLE_MESSAGE};
use crate::platform::warn;

/// Incomplete messages are dropped after this long.
pub const REASSEMBLY_TIMEOUT_MS: f64 = 30_000.0;

struct Entry {
    total: u32,
    chunks: Vec<Option<Vec<u8>>>,
    received: u32,
    bytes: usize,
    started_at_ms: f64,
}

#[derive(Default)]
pub struct Reassembler {
    inflight: HashMap<u32, Entry>,
}

impl Reassembler {
    pub fn new() -> Self {
        Self::default()
    }

    /// Feed one frame; returns the full message when it completes.
    /// `now_ms` is any monotonic-enough millisecond clock (used only
    /// for the 30 s incomplete-message timeout).
    pub fn push(&mut self, frame: &Frame<'_>, now_ms: f64) -> Option<Vec<u8>> {
        self.prune(now_ms);
        if let Some(entry) = self.inflight.get(&frame.msg_id) {
            if entry.total != frame.total {
                // msgId reuse with different geometry: a new message.
                self.inflight.remove(&frame.msg_id);
            }
        }
        let entry = self.inflight.entry(frame.msg_id).or_insert_with(|| Entry {
            total: frame.total,
            chunks: (0..frame.total).map(|_| None).collect(),
            received: 0,
            bytes: 0,
            started_at_ms: now_ms,
        });
        let slot = &mut entry.chunks[frame.seq as usize];
        if slot.is_none() {
            *slot = Some(frame.payload.to_vec());
            entry.received += 1;
            entry.bytes += frame.payload.len();
            if entry.bytes > MAX_RELIABLE_MESSAGE {
                warn(&format!(
                    "dropping oversized reliable message ({} bytes)",
                    entry.bytes
                ));
                self.inflight.remove(&frame.msg_id);
                return None;
            }
        }
        if entry.received < entry.total {
            return None;
        }
        let entry = self.inflight.remove(&frame.msg_id).expect("entry present");
        let mut out = Vec::with_capacity(entry.bytes);
        for chunk in entry.chunks {
            out.extend_from_slice(&chunk.expect("all chunks received"));
        }
        Some(out)
    }

    fn prune(&mut self, now_ms: f64) {
        self.inflight.retain(|msg_id, entry| {
            let keep = now_ms - entry.started_at_ms <= REASSEMBLY_TIMEOUT_MS;
            if !keep {
                warn(&format!(
                    "dropping incomplete reliable message {msg_id} (timeout)"
                ));
            }
            keep
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::core::framing::{make_frame, parse_frame};

    fn push(r: &mut Reassembler, bytes: &[u8], now: f64) -> Option<Vec<u8>> {
        let frame = parse_frame(bytes).unwrap();
        r.push(&frame, now)
    }

    #[test]
    fn single_frame_message() {
        let mut r = Reassembler::new();
        let out = push(&mut r, &make_frame(0, 0, 1, b"hi"), 0.0).unwrap();
        assert_eq!(out, b"hi");
    }

    #[test]
    fn out_of_order_and_duplicates() {
        let mut r = Reassembler::new();
        assert!(push(&mut r, &make_frame(5, 2, 3, b"c"), 0.0).is_none());
        assert!(push(&mut r, &make_frame(5, 0, 3, b"a"), 1.0).is_none());
        // duplicate seq is ignored
        assert!(push(&mut r, &make_frame(5, 0, 3, b"X"), 2.0).is_none());
        let out = push(&mut r, &make_frame(5, 1, 3, b"b"), 3.0).unwrap();
        assert_eq!(out, b"abc");
    }

    #[test]
    fn interleaved_messages() {
        let mut r = Reassembler::new();
        assert!(push(&mut r, &make_frame(1, 0, 2, b"a"), 0.0).is_none());
        assert!(push(&mut r, &make_frame(2, 0, 2, b"x"), 0.0).is_none());
        assert_eq!(push(&mut r, &make_frame(2, 1, 2, b"y"), 0.0).unwrap(), b"xy");
        assert_eq!(push(&mut r, &make_frame(1, 1, 2, b"b"), 0.0).unwrap(), b"ab");
    }

    #[test]
    fn msg_id_reuse_with_new_geometry_restarts() {
        let mut r = Reassembler::new();
        assert!(push(&mut r, &make_frame(9, 0, 3, b"a"), 0.0).is_none());
        // Same msgId, different total: treated as a fresh message.
        assert!(push(&mut r, &make_frame(9, 0, 2, b"x"), 1.0).is_none());
        assert_eq!(push(&mut r, &make_frame(9, 1, 2, b"y"), 2.0).unwrap(), b"xy");
    }

    #[test]
    fn incomplete_times_out() {
        let mut r = Reassembler::new();
        assert!(push(&mut r, &make_frame(3, 0, 2, b"a"), 0.0).is_none());
        // Past the 30 s window the half-built message is pruned, so
        // the second chunk starts a new (incomplete) entry.
        assert!(push(&mut r, &make_frame(3, 1, 2, b"b"), 40_000.0).is_none());
        assert!(push(&mut r, &make_frame(3, 0, 2, b"a"), 40_001.0).is_some());
    }

    #[test]
    fn zero_byte_message() {
        let mut r = Reassembler::new();
        let out = push(&mut r, &make_frame(0, 0, 1, b""), 0.0).unwrap();
        assert!(out.is_empty());
    }
}
