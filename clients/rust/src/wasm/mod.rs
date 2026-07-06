//! wasm32-unknown-unknown backend: the browser's WebSocket and
//! RTCPeerConnection via web-sys. Plain-Rust API for embedding in an
//! existing wasm-bindgen app — no exported `#[wasm_bindgen]` items.
//! Everything here is `!Send` (JS values); run it under
//! `wasm_bindgen_futures::spawn_local` or any single-threaded wasm
//! executor.

mod inner;
mod link;

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use bytes::Bytes;
use futures_channel::mpsc::UnboundedReceiver;
use wasm_bindgen::prelude::*;
use wasm_bindgen::JsCast;
use web_sys::{BinaryType, CloseEvent, MessageEvent, WebSocket};

use crate::core::error::LobbyError;
use crate::core::events::Event;
use crate::core::framing::MAX_RELIABLE_MESSAGE;
use crate::core::limits::{CONNECT_TIMEOUT_MS, MAX_BEST_EFFORT};
use crate::core::options::{validate_code, ConnectOptions};
use crate::core::protocol::{
    encode_client_message, ClientMessage, IceServer, JoinedMsg, ServerMessage,
};
use crate::core::roster::{check_target, roster_from_snapshot, PlayerId, PlayerInfo};
use crate::core::url::signaling_url;
use crate::util::{recv, timeout_ms};

use inner::{js_err, Inner};

/// A joined lobby: roster events plus two DataChannels to every other
/// player. Created with [`P2PGame::connect`].
pub struct P2PGame {
    inner: Rc<RefCell<Inner>>,
    event_rx: UnboundedReceiver<Event>,
    code: String,
    self_id: PlayerId,
    max_players: u16,
    resume_token: String,
    ice_servers: Vec<IceServer>,
}

impl P2PGame {
    /// Join (optionally creating) or claim a slot in a room.
    pub async fn connect(opts: ConnectOptions) -> Result<Self, LobbyError> {
        validate_code(&opts.code)?;
        let url = signaling_url(&opts.server)?;
        let ws = WebSocket::new(&url).map_err(|e| {
            LobbyError::new("connection-failed", format!("cannot open {url}: {}", js_err(&e)))
        })?;
        ws.set_binary_type(BinaryType::Arraybuffer);

        // First joined/error/close/timeout wins the handshake.
        let (result_tx, mut result_rx) =
            futures_channel::mpsc::unbounded::<Result<JoinedMsg, LobbyError>>();

        let join_text = {
            let token = if opts.claim_player_id.is_none() {
                opts.resume_token
                    .clone()
                    .or_else(|| storage::load(opts.storage_key.as_deref(), opts.storage))
            } else {
                None
            };
            let msg = if let Some(player_id) = opts.claim_player_id {
                ClientMessage::ClaimSlot {
                    code: &opts.code,
                    player_id,
                    app_id: opts.app_id.as_deref(),
                }
            } else {
                ClientMessage::Join {
                    code: &opts.code,
                    app_id: opts.app_id.as_deref(),
                    resume_token: token.as_deref(),
                    create: opts.create.as_ref(),
                }
            };
            encode_client_message(&msg)
        };

        let onopen = {
            let ws = ws.clone();
            let tx = result_tx.clone();
            Closure::<dyn FnMut()>::new(move || {
                if let Err(e) = ws.send_with_str(&join_text) {
                    let _ = tx.unbounded_send(Err(LobbyError::new(
                        "connection-failed",
                        format!("cannot send join: {}", js_err(&e)),
                    )));
                }
            })
        };
        ws.set_onopen(Some(onopen.as_ref().unchecked_ref()));
        let onmessage = {
            let tx = result_tx.clone();
            Closure::<dyn FnMut(MessageEvent)>::new(move |ev: MessageEvent| {
                let Some(text) = ev.data().as_string() else { return };
                match serde_json::from_str::<ServerMessage>(&text) {
                    Ok(ServerMessage::Joined(joined)) => {
                        let _ = tx.unbounded_send(Ok(joined));
                    }
                    Ok(ServerMessage::Error { code, message }) => {
                        let _ = tx.unbounded_send(Err(LobbyError::new(code, message)));
                    }
                    // Anything else before "joined" is unexpected; ignore.
                    Ok(_) => {}
                    Err(_) => {
                        let _ = tx.unbounded_send(Err(LobbyError::new(
                            "invalid-message",
                            "server sent malformed JSON",
                        )));
                    }
                }
            })
        };
        ws.set_onmessage(Some(onmessage.as_ref().unchecked_ref()));
        let onerror = {
            let tx = result_tx.clone();
            let url = url.clone();
            Closure::<dyn FnMut(JsValue)>::new(move |_| {
                let _ = tx.unbounded_send(Err(LobbyError::new(
                    "connection-failed",
                    format!("WebSocket error on {url}"),
                )));
            })
        };
        ws.set_onerror(Some(onerror.as_ref().unchecked_ref()));
        let onclose = {
            let tx = result_tx.clone();
            Closure::<dyn FnMut(CloseEvent)>::new(move |ev: CloseEvent| {
                let _ = tx.unbounded_send(Err(LobbyError::new(
                    "connection-closed",
                    format!("connection closed before join completed ({})", ev.code()),
                )));
            })
        };
        ws.set_onclose(Some(onclose.as_ref().unchecked_ref()));

        let result = match timeout_ms(CONNECT_TIMEOUT_MS, recv(&mut result_rx)).await {
            Some(Some(result)) => result,
            Some(None) => Err(LobbyError::new("connection-failed", "handshake aborted")),
            None => Err(LobbyError::new(
                "connect-timeout",
                format!("timed out connecting to {url}"),
            )),
        };
        // Detach the handshake handlers whatever happened.
        ws.set_onopen(None);
        ws.set_onmessage(None);
        ws.set_onerror(None);
        ws.set_onclose(None);
        drop((onopen, onmessage, onerror, onclose));
        let joined = match result {
            Ok(joined) => joined,
            Err(e) => {
                let _ = ws.close();
                return Err(e);
            }
        };
        storage::save(opts.storage_key.as_deref(), opts.storage, &joined.resume_token);

        // ICE servers in use: the server-issued set plus user extras.
        let mut ice_servers = joined.ice_servers.clone();
        ice_servers.extend(opts.ice_servers.iter().cloned());

        let roster = roster_from_snapshot(joined.max_players, &joined.players);
        let (event_tx, event_rx) = futures_channel::mpsc::unbounded();
        let rc = Rc::new(RefCell::new(Inner {
            ws,
            self_id: joined.self_id,
            max_players: joined.max_players,
            roster: roster.clone(),
            started: joined.started,
            closed: false,
            fatal_seen: false,
            peers: HashMap::new(),
            rebuild_counts: HashMap::new(),
            epoch_counter: 0,
            events: event_tx,
            ice_servers: ice_servers.clone(),
            force_relay: opts.force_relay,
            storage_key: opts.storage_key.clone(),
            storage_kind: opts.storage,
            link_waiters: HashMap::new(),
            send_queues: HashMap::new(),
            ws_closures: Vec::new(),
        }));
        inner::install_ws_handlers(&rc);

        // Lower ID initiates: offer to every occupied+connected peer
        // with a higher ID.
        for slot in &roster {
            if slot.id != joined.self_id
                && slot.occupied
                && slot.connected
                && joined.self_id < slot.id
            {
                inner::initiate_peer(&rc, slot.id);
            }
        }

        Ok(P2PGame {
            inner: rc,
            event_rx,
            code: joined.code,
            self_id: joined.self_id,
            max_players: joined.max_players,
            resume_token: joined.resume_token,
            ice_servers,
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

    /// Rotates on every (re)join; persisted under storage_key if set.
    pub fn resume_token(&self) -> &str {
        &self.resume_token
    }

    /// ICE servers in use: the server-issued set plus any from options.
    pub fn ice_servers(&self) -> &[IceServer] {
        &self.ice_servers
    }

    /// True once the room has reached its start condition.
    pub fn started(&self) -> bool {
        self.inner.borrow().started
    }

    /// Snapshot of all room slots.
    pub fn players(&self) -> Vec<PlayerInfo> {
        self.inner.borrow().roster.clone()
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
        let inner = self.inner.borrow();
        if !inner.closed {
            link::best_effort_to(&inner, to, &data);
        }
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
        let inner = self.inner.borrow();
        if inner.closed {
            return Ok(());
        }
        for slot in &inner.roster {
            if slot.id != self.self_id && slot.occupied {
                link::best_effort_to(&inner, slot.id, &data);
            }
        }
        Ok(())
    }

    /// Send a reliable, ordered message (chunked over the reliable
    /// channel, up to 16 MiB). Resolves once every chunk has been
    /// handed to the transport; errors if the peer link cannot be
    /// established or dies mid-send. Sends to the same peer are
    /// serialized.
    pub async fn send_reliable(&self, to: PlayerId, data: Bytes) -> Result<(), LobbyError> {
        check_target(to, self.self_id, self.max_players)?;
        {
            let inner = self.inner.borrow();
            if inner.closed {
                return Err(LobbyError::new("closed", "game is closed"));
            }
            if !inner
                .roster
                .get(to as usize)
                .is_some_and(|slot| slot.occupied)
            {
                return Err(LobbyError::new(
                    "target-unavailable",
                    format!("no player in slot {to}"),
                ));
            }
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
        link::queue_reliable(&self.inner, to, data)
            .await
            .map_err(|_| LobbyError::new("closed", "game is closed"))?
    }

    /// Next lobby/peer/message event; None after close().
    pub async fn next_event(&mut self) -> Option<Event> {
        recv(&mut self.event_rx).await
    }

    /// Leave the room and release all resources. Sends an explicit
    /// leave (freeing our slot) and clears any stored resume token.
    pub async fn close(self) -> Result<(), LobbyError> {
        inner::shutdown(&self.inner);
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
        // Dropping without close() still leaves the room.
        inner::shutdown(&self.inner);
    }
}

pub(crate) mod storage {
    use web_sys::Storage;

    use crate::core::options::StorageKind;

    /// Best-effort persistence: storage failures (private mode,
    /// quota) mean resume just won't work, mirroring the TS client.
    fn backend(kind: StorageKind) -> Option<Storage> {
        let window = web_sys::window()?;
        match kind {
            StorageKind::Local => window.local_storage().ok().flatten(),
            StorageKind::Session => window.session_storage().ok().flatten(),
        }
    }

    pub fn load(key: Option<&str>, kind: StorageKind) -> Option<String> {
        backend(kind)?.get_item(key?).ok().flatten()
    }

    pub fn save(key: Option<&str>, kind: StorageKind, token: &str) {
        if let (Some(key), Some(storage)) = (key, backend(kind)) {
            let _ = storage.set_item(key, token);
        }
    }

    pub fn clear(key: Option<&str>, kind: StorageKind) {
        if let (Some(key), Some(storage)) = (key, backend(kind)) {
            let _ = storage.remove_item(key);
        }
    }
}
