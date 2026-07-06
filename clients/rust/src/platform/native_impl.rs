use std::sync::OnceLock;
use std::time::{Duration, Instant};

pub fn warn(msg: &str) {
    eprintln!("[lobbylink] {msg}");
}

/// Milliseconds on a monotonic clock (origin: first call).
pub fn now_ms() -> f64 {
    static START: OnceLock<Instant> = OnceLock::new();
    START.get_or_init(Instant::now).elapsed().as_secs_f64() * 1000.0
}

pub async fn sleep_ms(ms: u64) {
    tokio::time::sleep(Duration::from_millis(ms)).await;
}
