use bytes::Bytes;

use super::roster::PlayerId;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MessageKind {
    Reliable,
    BestEffort,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PlayerLeftReason {
    ExplicitLeave,
    Disconnected,
}

impl PlayerLeftReason {
    /// Anything other than "explicit-leave" counts as a disconnect,
    /// mirroring the TS client.
    pub fn from_wire(reason: &str) -> Self {
        if reason == "explicit-leave" {
            PlayerLeftReason::ExplicitLeave
        } else {
            PlayerLeftReason::Disconnected
        }
    }
}

#[derive(Debug, Clone)]
pub enum Event {
    Message {
        from: PlayerId,
        kind: MessageKind,
        data: Bytes,
    },
    PlayerJoined {
        player_id: PlayerId,
    },
    PlayerLeft {
        player_id: PlayerId,
        reason: PlayerLeftReason,
    },
    PlayerRejoined {
        player_id: PlayerId,
        was_replacement: bool,
    },
    PlayerReplaced {
        player_id: PlayerId,
    },
    Started,
    /// Peer connection state change; `state` uses the browser's
    /// lowercase strings ("new", "connecting", "connected",
    /// "disconnected", "failed", "closed") on both backends.
    PeerState {
        player_id: PlayerId,
        state: String,
    },
    /// Selected ICE candidate types (host/srflx/prflx/relay) once a
    /// peer connection reaches "connected". Best-effort debug info.
    CandidatePair {
        player_id: PlayerId,
        local: String,
        remote: String,
    },
    /// Non-fatal error reported by the lobby server.
    LobbyError {
        code: String,
        message: String,
    },
    /// The signaling WebSocket is gone. Established DataChannels keep
    /// working unless code is "replaced", "session-superseded" or
    /// "room-expired", in which case the game is over and peers are
    /// torn down. A plain transport drop uses code "connection-lost".
    SignalingClosed {
        code: String,
        message: String,
    },
}
