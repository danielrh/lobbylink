//! Platform-independent core: everything wire-visible lives here so
//! the native and wasm backends cannot diverge on the wire.

pub mod error;
pub mod events;
pub mod framing;
pub mod limits;
pub mod options;
pub mod protocol;
pub mod reassembly;
pub mod roster;
pub mod url;
