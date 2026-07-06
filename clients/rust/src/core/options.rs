use serde::Serialize;

use super::error::LobbyError;
use super::protocol::IceServer;
use super::roster::PlayerId;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum ReconnectPolicy {
    TokenOnly,
    TokenOrClaimAfterTimeout,
    ClaimAfterTimeout,
    HostApproval,
}

/// Room creation options; the explicit booleans mirror the server
/// defaults (`CreateOptions::new` fills them in).
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CreateOptions {
    pub max_players: u16,
    pub wait_until_full: bool,
    pub allow_late_join: bool,
    pub allow_reconnect: bool,
    pub allow_replacement: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reconnect_policy: Option<ReconnectPolicy>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub claim_after_ms: Option<u64>,
}

impl CreateOptions {
    pub fn new(max_players: u16) -> Self {
        Self {
            max_players,
            wait_until_full: false,
            allow_late_join: true,
            allow_reconnect: true,
            allow_replacement: true,
            reconnect_policy: None,
            claim_after_ms: None,
        }
    }
}

/// Which browser storage backs `storage_key` on wasm: Local survives
/// a browser restart but is SHARED BY ALL TABS (two tabs with the
/// same key steal each other's slot via token resume); Session is
/// per-tab and survives reload — the safe default. Unused on native.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum StorageKind {
    Local,
    #[default]
    Session,
}

#[derive(Debug, Clone, Default)]
pub struct ConnectOptions {
    /// "https://host[:port][/path]" or "wss://host[:port][/path]/ws".
    pub server: String,
    /// Optional app policy id for hosted static sites.
    pub app_id: Option<String>,
    /// Room code, 4-64 chars of [A-Za-z0-9_-].
    pub code: String,
    /// Create the room if it does not exist.
    pub create: Option<CreateOptions>,
    /// Explicit resume token; overrides the stored one.
    pub resume_token: Option<String>,
    /// Claim a specific slot after losing the resume token (claim-slot).
    pub claim_player_id: Option<PlayerId>,
    /// Extra ICE servers, appended to the ones issued by the server.
    pub ice_servers: Vec<IceServer>,
    /// Force TURN relay (ice transport policy "relay"); for TURN testing.
    pub force_relay: bool,

    /// File used for automatic resume-token persistence. Use a
    /// per-process/per-instance path: two clients sharing one token
    /// store supersede each other.
    #[cfg(not(target_arch = "wasm32"))]
    pub storage_path: Option<std::path::PathBuf>,
    /// Origin header for the WS handshake. The server allowlists
    /// origins; defaults to the http(s) origin of `server` (which is
    /// allowlisted on servers that host their own web client). An
    /// empty string omits the header entirely — for local servers
    /// running with --allow-no-origin.
    #[cfg(not(target_arch = "wasm32"))]
    pub origin: Option<String>,

    /// Storage key for automatic resume-token persistence (browser).
    #[cfg(target_arch = "wasm32")]
    pub storage_key: Option<String>,
    /// Which storage backs `storage_key`; defaults to Session.
    #[cfg(target_arch = "wasm32")]
    pub storage: StorageKind,
}

pub fn validate_code(code: &str) -> Result<(), LobbyError> {
    let ok = (4..=64).contains(&code.len())
        && code
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-');
    if ok {
        Ok(())
    } else {
        Err(LobbyError::new(
            "invalid-code",
            "room code must be 4-64 chars of [A-Za-z0-9_-]",
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn code_validation() {
        assert!(validate_code("ROOM").is_ok());
        assert!(validate_code("a_b-9").is_ok());
        assert!(validate_code(&"x".repeat(64)).is_ok());
        assert!(validate_code("abc").is_err());
        assert!(validate_code(&"x".repeat(65)).is_err());
        assert!(validate_code("bad room").is_err());
        assert!(validate_code("bad!").is_err());
        assert!(validate_code("").is_err());
    }
}
