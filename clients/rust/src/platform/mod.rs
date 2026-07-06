//! The only platform shims the shared code needs: a warning log, a
//! millisecond clock (std::time::Instant aborts on
//! wasm32-unknown-unknown) and an async sleep. Task spawning stays
//! backend-local (tokio::spawn vs spawn_local) on purpose.

#[cfg(not(target_arch = "wasm32"))]
mod native_impl;
#[cfg(not(target_arch = "wasm32"))]
pub use native_impl::*;

#[cfg(target_arch = "wasm32")]
mod wasm_impl;
#[cfg(target_arch = "wasm32")]
pub use wasm_impl::*;
