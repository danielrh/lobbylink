//! lobbylink client: lobby membership over a WebSocket signaling server
//! plus peer-to-peer WebRTC DataChannels between all players.
//!
//! One crate, two backends selected by target triple:
//! - native (anything that is not wasm32): tokio + tokio-tungstenite +
//!   the `webrtc` crate.
//! - wasm32-unknown-unknown: the BROWSER's WebSocket and
//!   RTCPeerConnection via web-sys. Designed to be embedded in an
//!   existing wasm-bindgen Rust app: plain-Rust API, no exported
//!   `#[wasm_bindgen]` items, `crate-type = ["lib"]` only.
//!
//! Both backends share `core` for everything wire-visible (signaling
//! JSON, reliable framing, reassembly, roster rules), so they
//! interoperate with each other and with the TypeScript client
//! (`clients/ts/src/index.ts`, the reference implementation).
//!
//! The public API follows the repo implementation guide §8; extras
//! that exist in the TS client (player-replaced / candidate-pair /
//! lobby-error / signaling-closed events, `force_relay`) are carried
//! over. No `Send` bounds anywhere: JS values on the wasm side are
//! `!Send`.

mod core;
mod platform;
mod util;

#[cfg(not(target_arch = "wasm32"))]
mod native;
#[cfg(target_arch = "wasm32")]
mod wasm;

pub use crate::core::error::LobbyError;
pub use crate::core::events::{Event, MessageKind, PlayerLeftReason};
pub use crate::core::options::{ConnectOptions, CreateOptions, ReconnectPolicy, StorageKind};
pub use crate::core::protocol::IceServer;
pub use crate::core::roster::{PlayerId, PlayerInfo};

#[cfg(not(target_arch = "wasm32"))]
pub use crate::native::P2PGame;
#[cfg(target_arch = "wasm32")]
pub use crate::wasm::P2PGame;

/// Convenience alias: every fallible API returns a [`LobbyError`].
pub type Result<T> = std::result::Result<T, LobbyError>;
