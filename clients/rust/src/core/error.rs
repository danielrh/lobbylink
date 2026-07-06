use thiserror::Error;

/// Error carrying a stable machine-readable `code`. Server-reported
/// codes (e.g. "room-full", "slot-not-claimable") pass through
/// unchanged; client-side failures use codes like "connect-timeout",
/// "connection-lost", "invalid-target", "message-too-large",
/// "channel-timeout", "send-failed", "closed".
#[derive(Debug, Clone, PartialEq, Eq, Error)]
#[error("{code}: {message}")]
pub struct LobbyError {
    pub code: String,
    pub message: String,
}

impl LobbyError {
    pub fn new(code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
        }
    }
}
