//! Signaling wire types (JSON over the WebSocket). Field and tag
//! names must match the server / TS client exactly.

use serde::{Deserialize, Deserializer, Serialize};

use super::options::CreateOptions;
use super::roster::PlayerId;

/// Server error codes after which the WebSocket will not come back.
pub fn is_fatal_code(code: &str) -> bool {
    matches!(
        code,
        "replaced" | "session-superseded" | "room-expired" | "slow-consumer"
    )
}

/// Fatal codes that also mean our peers are gone / we left the room.
pub fn is_game_over_code(code: &str) -> bool {
    matches!(code, "replaced" | "session-superseded" | "room-expired")
}

// ---------------------------------------------------------------------------
// Client -> server
// ---------------------------------------------------------------------------

#[derive(Debug, Serialize)]
#[serde(tag = "type", rename_all = "kebab-case")]
pub enum ClientMessage<'a> {
    Join {
        code: &'a str,
        #[serde(rename = "appId", skip_serializing_if = "Option::is_none")]
        app_id: Option<&'a str>,
        #[serde(rename = "resumeToken", skip_serializing_if = "Option::is_none")]
        resume_token: Option<&'a str>,
        #[serde(skip_serializing_if = "Option::is_none")]
        create: Option<&'a CreateOptions>,
    },
    ClaimSlot {
        code: &'a str,
        #[serde(rename = "playerId")]
        player_id: PlayerId,
        #[serde(rename = "appId", skip_serializing_if = "Option::is_none")]
        app_id: Option<&'a str>,
    },
    Signal {
        to: PlayerId,
        payload: &'a SignalPayload,
    },
    Leave,
}

pub fn encode_client_message(msg: &ClientMessage<'_>) -> String {
    // Our own types cannot fail to serialize.
    serde_json::to_string(msg).expect("client message serialization")
}

// ---------------------------------------------------------------------------
// Server -> client
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize)]
pub struct WirePlayer {
    pub id: PlayerId,
    #[serde(default)]
    pub occupied: bool,
    #[serde(default)]
    pub connected: bool,
    // lastSeenMsAgo is ignored (unknown fields are skipped by serde).
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct JoinedMsg {
    pub code: String,
    pub self_id: PlayerId,
    pub max_players: u16,
    #[serde(default)]
    pub started: bool,
    pub resume_token: String,
    pub players: Vec<WirePlayer>,
    #[serde(default)]
    pub ice_servers: Vec<IceServer>,
}

/// WebRTC signal relayed through the lobby. The ICE candidate is kept
/// as raw JSON (RTCIceCandidateInit); clients pass it through without
/// interpreting it. `candidate` null/absent = end-of-candidates.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "kebab-case")]
pub enum SignalPayload {
    Offer {
        sdp: String,
    },
    Answer {
        sdp: String,
    },
    Ice {
        #[serde(skip_serializing_if = "Option::is_none")]
        candidate: Option<serde_json::Value>,
    },
    /// Unknown kinds are ignored for forward compatibility.
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(tag = "type", rename_all = "kebab-case")]
pub enum ServerMessage {
    Joined(JoinedMsg),
    #[serde(rename_all = "camelCase")]
    PlayerJoined {
        player_id: PlayerId,
        players: Vec<WirePlayer>,
    },
    #[serde(rename_all = "camelCase")]
    PlayerLeft {
        player_id: PlayerId,
        reason: String,
    },
    #[serde(rename_all = "camelCase")]
    PlayerRejoined {
        player_id: PlayerId,
        was_replacement: bool,
    },
    #[serde(rename_all = "camelCase")]
    PlayerReplaced {
        player_id: PlayerId,
    },
    RoomStarted,
    Signal {
        from: PlayerId,
        payload: SignalPayload,
    },
    Error {
        code: String,
        message: String,
    },
    /// Unknown message types are ignored for forward compatibility.
    #[serde(other)]
    Unknown,
}

// ---------------------------------------------------------------------------
// ICE servers
// ---------------------------------------------------------------------------

/// One RTCIceServer entry as issued by the server (or supplied by the
/// user). `urls` accepts a single string or an array on the wire.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct IceServer {
    #[serde(deserialize_with = "string_or_seq", default)]
    pub urls: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub username: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub credential: Option<String>,
}

fn string_or_seq<'de, D: Deserializer<'de>>(d: D) -> Result<Vec<String>, D::Error> {
    #[derive(Deserialize)]
    #[serde(untagged)]
    enum OneOrMany {
        One(String),
        Many(Vec<String>),
    }
    Ok(match OneOrMany::deserialize(d)? {
        OneOrMany::One(s) => vec![s],
        OneOrMany::Many(v) => v,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn client_messages_encode_like_ts() {
        let create = CreateOptions::new(4);
        let msg = ClientMessage::Join {
            code: "ROOM",
            app_id: None,
            resume_token: Some("tok"),
            create: Some(&create),
        };
        let v: serde_json::Value =
            serde_json::from_str(&encode_client_message(&msg)).unwrap();
        assert_eq!(v["type"], "join");
        assert_eq!(v["code"], "ROOM");
        assert_eq!(v["resumeToken"], "tok");
        assert_eq!(v["create"]["maxPlayers"], 4);
        assert_eq!(v["create"]["waitUntilFull"], false);
        assert_eq!(v["create"]["allowLateJoin"], true);
        assert_eq!(v["create"]["allowReconnect"], true);
        assert_eq!(v["create"]["allowReplacement"], true);
        assert!(v["create"].get("reconnectPolicy").is_none());
        assert!(v["create"].get("claimAfterMs").is_none());
        assert!(v.get("appId").is_none());

        let msg = ClientMessage::ClaimSlot {
            code: "ROOM",
            player_id: 2,
            app_id: Some("app"),
        };
        let v: serde_json::Value =
            serde_json::from_str(&encode_client_message(&msg)).unwrap();
        assert_eq!(v["type"], "claim-slot");
        assert_eq!(v["playerId"], 2);
        assert_eq!(v["appId"], "app");

        let payload = SignalPayload::Offer { sdp: "SDP".into() };
        let msg = ClientMessage::Signal { to: 1, payload: &payload };
        let v: serde_json::Value =
            serde_json::from_str(&encode_client_message(&msg)).unwrap();
        assert_eq!(v["type"], "signal");
        assert_eq!(v["to"], 1);
        assert_eq!(v["payload"]["kind"], "offer");
        assert_eq!(v["payload"]["sdp"], "SDP");

        assert_eq!(
            encode_client_message(&ClientMessage::Leave),
            r#"{"type":"leave"}"#
        );
    }

    #[test]
    fn reconnect_policy_strings() {
        let mut create = CreateOptions::new(2);
        create.reconnect_policy = Some(crate::core::options::ReconnectPolicy::TokenOrClaimAfterTimeout);
        create.claim_after_ms = Some(40_000);
        let v = serde_json::to_value(&create).unwrap();
        assert_eq!(v["reconnectPolicy"], "token-or-claim-after-timeout");
        assert_eq!(v["claimAfterMs"], 40_000);
    }

    #[test]
    fn parses_joined() {
        let raw = r#"{"type":"joined","code":"ROOM","selfId":0,"maxPlayers":2,
            "started":false,"resumeToken":"tok",
            "players":[{"id":0,"occupied":true,"connected":true,"lastSeenMsAgo":0},
                       {"id":1,"occupied":false,"connected":false}],
            "iceServers":[{"urls":"stun:stun.example.com"},
                          {"urls":["turn:t.example.com"],"username":"u","credential":"c"}]}"#;
        let msg: ServerMessage = serde_json::from_str(raw).unwrap();
        let ServerMessage::Joined(j) = msg else {
            panic!("expected joined")
        };
        assert_eq!(j.self_id, 0);
        assert_eq!(j.max_players, 2);
        assert_eq!(j.resume_token, "tok");
        assert_eq!(j.players.len(), 2);
        assert!(j.players[0].occupied && j.players[0].connected);
        assert_eq!(j.ice_servers[0].urls, vec!["stun:stun.example.com"]);
        assert_eq!(j.ice_servers[1].urls, vec!["turn:t.example.com"]);
        assert_eq!(j.ice_servers[1].username.as_deref(), Some("u"));
    }

    #[test]
    fn parses_joined_without_ice_servers() {
        let raw = r#"{"type":"joined","code":"ROOM","selfId":1,"maxPlayers":2,
            "started":true,"resumeToken":"t","players":[]}"#;
        let ServerMessage::Joined(j) = serde_json::from_str(raw).unwrap() else {
            panic!()
        };
        assert!(j.started);
        assert!(j.ice_servers.is_empty());
    }

    #[test]
    fn parses_server_messages() {
        let m: ServerMessage = serde_json::from_str(
            r#"{"type":"player-left","playerId":1,"reason":"explicit-leave"}"#,
        )
        .unwrap();
        assert!(matches!(
            m,
            ServerMessage::PlayerLeft { player_id: 1, ref reason } if reason == "explicit-leave"
        ));

        let m: ServerMessage = serde_json::from_str(
            r#"{"type":"player-rejoined","playerId":0,"wasReplacement":true}"#,
        )
        .unwrap();
        assert!(matches!(
            m,
            ServerMessage::PlayerRejoined { player_id: 0, was_replacement: true }
        ));

        let m: ServerMessage = serde_json::from_str(r#"{"type":"room-started"}"#).unwrap();
        assert!(matches!(m, ServerMessage::RoomStarted));

        let m: ServerMessage = serde_json::from_str(
            r#"{"type":"signal","from":1,"payload":{"kind":"ice","candidate":{"candidate":"x","sdpMid":"0"}}}"#,
        )
        .unwrap();
        let ServerMessage::Signal { from: 1, payload: SignalPayload::Ice { candidate: Some(c) } } = m
        else {
            panic!("expected ice signal")
        };
        assert_eq!(c["sdpMid"], "0");

        // null candidate -> None (end-of-candidates)
        let m: ServerMessage = serde_json::from_str(
            r#"{"type":"signal","from":1,"payload":{"kind":"ice","candidate":null}}"#,
        )
        .unwrap();
        assert!(matches!(
            m,
            ServerMessage::Signal { payload: SignalPayload::Ice { candidate: None }, .. }
        ));
    }

    #[test]
    fn unknown_types_are_forward_compatible() {
        let m: ServerMessage =
            serde_json::from_str(r#"{"type":"totally-new","x":1}"#).unwrap();
        assert!(matches!(m, ServerMessage::Unknown));
        let m: ServerMessage = serde_json::from_str(
            r#"{"type":"signal","from":0,"payload":{"kind":"future-thing","data":1}}"#,
        )
        .unwrap();
        assert!(matches!(
            m,
            ServerMessage::Signal { payload: SignalPayload::Unknown, .. }
        ));
    }

    #[test]
    fn fatal_code_sets() {
        for c in ["replaced", "session-superseded", "room-expired", "slow-consumer"] {
            assert!(is_fatal_code(c));
        }
        assert!(!is_fatal_code("room-full"));
        assert!(is_game_over_code("room-expired"));
        assert!(!is_game_over_code("slow-consumer"));
    }
}
