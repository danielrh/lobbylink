//! Native backend: tokio + tokio-tungstenite (rustls) + the `webrtc`
//! crate. See `actor.rs` for the state owner and `link.rs` for the
//! per-peer session/sender machinery.

mod actor;
mod link;

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex as StdMutex};
use std::time::Duration;

use bytes::Bytes;
use futures_util::{SinkExt, StreamExt};
use tokio::sync::{mpsc, oneshot};
use tokio_tungstenite::connect_async;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::http::header::ORIGIN;
use tokio_tungstenite::tungstenite::Message;
use webrtc::ice_transport::ice_server::RTCIceServer;

use crate::core::error::LobbyError;
use crate::core::events::Event;
use crate::core::framing::MAX_RELIABLE_MESSAGE;
use crate::core::limits::{CONNECT_TIMEOUT_MS, MAX_BEST_EFFORT};
use crate::core::options::{validate_code, ConnectOptions};
use crate::core::protocol::{encode_client_message, ClientMessage, IceServer, ServerMessage};
use crate::core::roster::{check_target, roster_from_snapshot, PlayerId, PlayerInfo};
use crate::core::url::{default_origin, signaling_url};
use crate::platform::warn;

use actor::{Actor, Cmd, Internal};

pub(crate) struct Shared {
    pub started: AtomicBool,
    pub closed: AtomicBool,
    pub roster: StdMutex<Vec<PlayerInfo>>,
}

/// A joined lobby: roster events plus two DataChannels to every other
/// player. Created with [`P2PGame::connect`].
pub struct P2PGame {
    code: String,
    self_id: PlayerId,
    max_players: u16,
    resume_token: String,
    ice_servers: Vec<IceServer>,
    shared: Arc<Shared>,
    cmd_tx: mpsc::UnboundedSender<Cmd>,
    event_rx: futures_channel::mpsc::UnboundedReceiver<Event>,
}

impl P2PGame {
    /// Join (optionally creating) or claim a slot in a room.
    pub async fn connect(opts: ConnectOptions) -> Result<Self, LobbyError> {
        validate_code(&opts.code)?;
        let url = signaling_url(&opts.server)?;
        let origin = opts
            .origin
            .clone()
            .unwrap_or_else(|| default_origin(&url));

        let mut request = url.as_str().into_client_request().map_err(|e| {
            LobbyError::new("invalid-server-url", format!("cannot request {url}: {e}"))
        })?;
        // An empty origin means "send no Origin header" (local
        // servers running with --allow-no-origin).
        if !origin.is_empty() {
            let origin_value = origin.parse().map_err(|_| {
                LobbyError::new("invalid-server-url", format!("invalid origin: {origin}"))
            })?;
            request.headers_mut().insert(ORIGIN, origin_value);
        }

        let handshake = async {
            let (ws, _resp) = connect_async(request).await.map_err(|e| {
                LobbyError::new("connection-failed", format!("cannot open {url}: {e}"))
            })?;
            let (mut ws_tx, mut ws_rx) = ws.split();

            let stored_token;
            let join_msg = if let Some(player_id) = opts.claim_player_id {
                ClientMessage::ClaimSlot {
                    code: &opts.code,
                    player_id,
                    app_id: opts.app_id.as_deref(),
                }
            } else {
                stored_token = opts
                    .resume_token
                    .clone()
                    .or_else(|| storage::load(opts.storage_path.as_deref()));
                ClientMessage::Join {
                    code: &opts.code,
                    app_id: opts.app_id.as_deref(),
                    resume_token: stored_token.as_deref(),
                    create: opts.create.as_ref(),
                }
            };
            ws_tx
                .send(Message::text(encode_client_message(&join_msg)))
                .await
                .map_err(|e| {
                    LobbyError::new("connection-failed", format!("WebSocket error on {url}: {e}"))
                })?;

            loop {
                match ws_rx.next().await {
                    Some(Ok(Message::Text(text))) => {
                        match serde_json::from_str::<ServerMessage>(text.as_str()) {
                            Ok(ServerMessage::Joined(joined)) => {
                                return Ok((ws_tx, ws_rx, joined))
                            }
                            Ok(ServerMessage::Error { code, message }) => {
                                return Err(LobbyError::new(code, message))
                            }
                            // Anything else before "joined" is unexpected; ignore.
                            Ok(_) => {}
                            Err(_) => {
                                return Err(LobbyError::new(
                                    "invalid-message",
                                    "server sent malformed JSON",
                                ))
                            }
                        }
                    }
                    Some(Ok(Message::Close(frame))) => {
                        let code = frame.map(|f| f.code.to_string()).unwrap_or_default();
                        return Err(LobbyError::new(
                            "connection-closed",
                            format!("connection closed before join completed ({code})"),
                        ));
                    }
                    Some(Ok(_)) => {}
                    Some(Err(e)) => {
                        return Err(LobbyError::new(
                            "connection-failed",
                            format!("WebSocket error on {url}: {e}"),
                        ))
                    }
                    None => {
                        return Err(LobbyError::new(
                            "connection-closed",
                            "connection closed before join completed",
                        ))
                    }
                }
            }
        };
        let (ws_tx, ws_rx, joined) =
            match tokio::time::timeout(Duration::from_millis(CONNECT_TIMEOUT_MS), handshake)
                .await
            {
                Ok(result) => result?,
                Err(_) => {
                    return Err(LobbyError::new(
                        "connect-timeout",
                        format!("timed out connecting to {url}"),
                    ))
                }
            };
        storage::save(opts.storage_path.as_deref(), &joined.resume_token);

        // ICE servers in use: the server-issued set plus user extras.
        let mut ice_servers = joined.ice_servers.clone();
        ice_servers.extend(opts.ice_servers.iter().cloned());
        let rtc_ice_servers: Vec<RTCIceServer> = ice_servers
            .iter()
            .map(|s| RTCIceServer {
                urls: s.urls.clone(),
                username: s.username.clone().unwrap_or_default(),
                credential: s.credential.clone().unwrap_or_default(),
            })
            .collect();

        let roster = roster_from_snapshot(joined.max_players, &joined.players);
        let shared = Arc::new(Shared {
            started: AtomicBool::new(joined.started),
            closed: AtomicBool::new(false),
            roster: StdMutex::new(roster.clone()),
        });
        let (event_tx, event_rx) = futures_channel::mpsc::unbounded();
        let (cmd_tx, cmd_rx) = mpsc::unbounded_channel();
        let (internal_tx, internal_rx) = mpsc::unbounded_channel();

        let reader_tx = internal_tx.clone();
        tokio::spawn(async move {
            let mut ws_rx = ws_rx;
            loop {
                match ws_rx.next().await {
                    Some(Ok(Message::Text(text))) => {
                        match serde_json::from_str::<ServerMessage>(text.as_str()) {
                            Ok(msg) => {
                                if reader_tx.send(Internal::Ws(msg)).is_err() {
                                    break;
                                }
                            }
                            Err(_) => warn("malformed server message"),
                        }
                    }
                    Some(Ok(Message::Close(_))) | None => {
                        let _ = reader_tx.send(Internal::WsClosed);
                        break;
                    }
                    Some(Ok(_)) => {}
                    Some(Err(_)) => {
                        let _ = reader_tx.send(Internal::WsClosed);
                        break;
                    }
                }
            }
        });

        let actor = Actor::new(
            joined.self_id,
            joined.max_players,
            ws_tx,
            roster,
            event_tx,
            internal_tx,
            shared.clone(),
            rtc_ice_servers,
            opts.force_relay,
            opts.storage_path.clone(),
        );
        tokio::spawn(actor.run(cmd_rx, internal_rx));

        Ok(P2PGame {
            code: joined.code,
            self_id: joined.self_id,
            max_players: joined.max_players,
            resume_token: joined.resume_token,
            ice_servers,
            shared,
            cmd_tx,
            event_rx,
        })
    }

    pub fn code(&self) -> &str {
        &self.code
    }

    pub fn self_id(&self) -> PlayerId {
        self.self_id
    }

    pub fn max_players(&self) -> u16 {
        self.max_players
    }

    /// Rotates on every (re)join; persisted under storage_path if set.
    pub fn resume_token(&self) -> &str {
        &self.resume_token
    }

    /// ICE servers in use: the server-issued set plus any from options.
    pub fn ice_servers(&self) -> &[IceServer] {
        &self.ice_servers
    }

    /// True once the room has reached its start condition.
    pub fn started(&self) -> bool {
        self.shared.started.load(Ordering::SeqCst)
    }

    /// Snapshot of all room slots.
    pub fn players(&self) -> Vec<PlayerInfo> {
        self.shared.roster.lock().expect("roster mirror").clone()
    }

    /// Send one datagram on the unordered, no-retransmit channel.
    /// Silently dropped if the channel is not open or its buffer is
    /// full (that is the best-effort contract). Errors only on caller
    /// mistakes: bad target or payload over 16000 bytes.
    pub async fn send_best_effort(&self, to: PlayerId, data: Bytes) -> Result<(), LobbyError> {
        check_target(to, self.self_id, self.max_players)?;
        if data.len() > MAX_BEST_EFFORT {
            return Err(LobbyError::new(
                "message-too-large",
                format!("best-effort payload {} exceeds {MAX_BEST_EFFORT} bytes", data.len()),
            ));
        }
        let _ = self.cmd_tx.send(Cmd::SendBestEffort { to, data });
        Ok(())
    }

    /// send_best_effort to every other occupied slot.
    pub async fn broadcast_best_effort(&self, data: Bytes) -> Result<(), LobbyError> {
        if data.len() > MAX_BEST_EFFORT {
            return Err(LobbyError::new(
                "message-too-large",
                format!("best-effort payload {} exceeds {MAX_BEST_EFFORT} bytes", data.len()),
            ));
        }
        let _ = self.cmd_tx.send(Cmd::BroadcastBestEffort { data });
        Ok(())
    }

    /// Send a reliable, ordered message (chunked over the reliable
    /// channel, up to 16 MiB). Resolves once every chunk has been
    /// handed to the transport; errors if the peer link cannot be
    /// established or dies mid-send. Sends to the same peer are
    /// serialized.
    pub async fn send_reliable(&self, to: PlayerId, data: Bytes) -> Result<(), LobbyError> {
        check_target(to, self.self_id, self.max_players)?;
        if self.shared.closed.load(Ordering::SeqCst) {
            return Err(LobbyError::new("closed", "game is closed"));
        }
        let occupied = self
            .shared
            .roster
            .lock()
            .expect("roster mirror")
            .get(to as usize)
            .is_some_and(|slot| slot.occupied);
        if !occupied {
            return Err(LobbyError::new(
                "target-unavailable",
                format!("no player in slot {to}"),
            ));
        }
        if data.len() > MAX_RELIABLE_MESSAGE {
            return Err(LobbyError::new(
                "message-too-large",
                format!(
                    "reliable payload {} exceeds {MAX_RELIABLE_MESSAGE} bytes",
                    data.len()
                ),
            ));
        }
        let (done_tx, done_rx) = oneshot::channel();
        self.cmd_tx
            .send(Cmd::SendReliable { to, data, done: done_tx })
            .map_err(|_| LobbyError::new("closed", "game is closed"))?;
        done_rx
            .await
            .map_err(|_| LobbyError::new("closed", "game is closed"))?
    }

    /// Next lobby/peer/message event; None after close().
    pub async fn next_event(&mut self) -> Option<Event> {
        crate::util::recv(&mut self.event_rx).await
    }

    /// Leave the room and release all resources. Sends an explicit
    /// leave (freeing our slot) and clears any stored resume token.
    pub async fn close(self) -> Result<(), LobbyError> {
        let (done_tx, done_rx) = oneshot::channel();
        if self
            .cmd_tx
            .send(Cmd::Close { done: Some(done_tx) })
            .is_ok()
        {
            let _ = done_rx.await;
        }
        Ok(())
    }
}

impl std::fmt::Debug for P2PGame {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("P2PGame")
            .field("code", &self.code)
            .field("self_id", &self.self_id)
            .field("max_players", &self.max_players)
            .finish_non_exhaustive()
    }
}

impl Drop for P2PGame {
    fn drop(&mut self) {
        // Dropping without close(): the actor still leaves the room.
        let _ = self.cmd_tx.send(Cmd::Close { done: None });
    }
}

pub(crate) mod storage {
    use std::fs;
    use std::path::Path;

    /// Best-effort resume-token persistence: storage failures mean
    /// resume just won't work, mirroring the TS client.
    pub fn load(path: Option<&Path>) -> Option<String> {
        let token = fs::read_to_string(path?).ok()?;
        let token = token.trim();
        (!token.is_empty()).then(|| token.to_string())
    }

    pub fn save(path: Option<&Path>, token: &str) {
        if let Some(path) = path {
            let _ = fs::write(path, token);
        }
    }

    pub fn clear(path: Option<&Path>) {
        if let Some(path) = path {
            let _ = fs::remove_file(path);
        }
    }
}
