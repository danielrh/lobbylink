//! Tunables shared by both backends. Framing-specific limits live in
//! `framing.rs`; these are the transport/behavioral ones.

/// Best-effort payload hard cap (bytes). Stay under ~1200 to avoid
/// SCTP fragmentation, where losing any fragment loses the message.
pub const MAX_BEST_EFFORT: usize = 16_000;
/// Pause reliable chunk sends above this bufferedAmount...
pub const SEND_HIGH_WATER: usize = 1 << 20;
/// ...and resume once it drains below this.
pub const SEND_LOW_WATER: usize = 256 * 1024;
pub const CONNECT_TIMEOUT_MS: u64 = 20_000;
/// How long send_reliable waits for a usable channel to the target.
pub const CHANNEL_TIMEOUT_MS: u64 = 30_000;
/// Fallback poll interval while waiting for the send buffer to drain.
pub const DRAIN_POLL_MS: u64 = 200;
pub const RELIABLE_LABEL: &str = "reliable";
pub const BEST_EFFORT_LABEL: &str = "best-effort";
pub const RELIABLE_CHANNEL_ID: u16 = 1;
pub const BEST_EFFORT_CHANNEL_ID: u16 = 2;
/// Automatic ICE-failure rebuilds per peer before giving up.
pub const MAX_PEER_REBUILDS: u32 = 3;
