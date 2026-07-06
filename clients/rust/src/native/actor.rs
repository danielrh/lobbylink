//! Single-owner actor: all lobby/roster/peer state lives here.
//! API methods and WebRTC callbacks reach it via unbounded channels,
//! so callbacks can never deadlock against it.

use std::collections::hash_map::Entry;
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::atomic::Ordering;
use std::sync::Arc;

use bytes::Bytes;
use futures_util::SinkExt;
use tokio::net::TcpStream;
use tokio::sync::{mpsc, oneshot, watch};
use tokio_tungstenite::tungstenite::Message;
use tokio_tungstenite::{MaybeTlsStream, WebSocketStream};
use webrtc::api::API;
use webrtc::data_channel::data_channel_init::RTCDataChannelInit;
use webrtc::data_channel::data_channel_message::DataChannelMessage;
use webrtc::data_channel::data_channel_state::RTCDataChannelState;
use webrtc::ice_transport::ice_candidate::{RTCIceCandidate, RTCIceCandidateInit};
use webrtc::ice_transport::ice_server::RTCIceServer;
use webrtc::peer_connection::configuration::RTCConfiguration;
use webrtc::peer_connection::policy::ice_transport_policy::RTCIceTransportPolicy;
use webrtc::peer_connection::peer_connection_state::RTCPeerConnectionState;
use webrtc::peer_connection::sdp::session_description::RTCSessionDescription;
use webrtc::peer_connection::signaling_state::RTCSignalingState;

use super::link::{sender_task, Link, SendJob};
use super::{storage, Shared};
use crate::core::events::{Event, MessageKind, PlayerLeftReason};
use crate::core::framing::parse_frame;
use crate::core::limits::{
    BEST_EFFORT_CHANNEL_ID, BEST_EFFORT_LABEL, MAX_PEER_REBUILDS, RELIABLE_CHANNEL_ID,
    RELIABLE_LABEL, SEND_HIGH_WATER, SEND_LOW_WATER,
};
use crate::core::protocol::{
    encode_client_message, is_fatal_code, is_game_over_code, ClientMessage, ServerMessage,
    SignalPayload,
};
use crate::core::reassembly::Reassembler;
use crate::core::roster::{roster_from_snapshot, PlayerId, PlayerInfo};
use crate::platform::warn;

type WsSink = futures_util::stream::SplitSink<
    WebSocketStream<MaybeTlsStream<TcpStream>>,
    Message,
>;

pub(super) enum Cmd {
    SendReliable {
        to: PlayerId,
        data: Bytes,
        done: oneshot::Sender<Result<(), crate::core::error::LobbyError>>,
    },
    SendBestEffort {
        to: PlayerId,
        data: Bytes,
    },
    BroadcastBestEffort {
        data: Bytes,
    },
    Close {
        done: Option<oneshot::Sender<()>>,
    },
}

pub(super) enum Internal {
    Ws(ServerMessage),
    WsClosed,
    Ice {
        player_id: PlayerId,
        epoch: u64,
        candidate: serde_json::Value,
    },
    State {
        player_id: PlayerId,
        epoch: u64,
        state: RTCPeerConnectionState,
    },
    Reliable {
        player_id: PlayerId,
        epoch: u64,
        data: Bytes,
    },
    BestEffort {
        player_id: PlayerId,
        epoch: u64,
        data: Bytes,
    },
    Pair {
        player_id: PlayerId,
        epoch: u64,
        local: String,
        remote: String,
    },
    Rebuild {
        player_id: PlayerId,
        epoch: u64,
    },
}

struct Peer {
    link: Arc<Link>,
    reassembler: Reassembler,
    /// Remote ICE candidates queued until the remote description is
    /// set; None once flushed (later candidates are added directly).
    pending_candidates: Option<Vec<serde_json::Value>>,
}

pub(super) struct Actor {
    self_id: PlayerId,
    max_players: u16,
    ws_tx: WsSink,
    roster: Vec<PlayerInfo>,
    peers: HashMap<PlayerId, Peer>,
    link_watch: HashMap<PlayerId, watch::Sender<Option<Arc<Link>>>>,
    send_queues: HashMap<PlayerId, mpsc::UnboundedSender<SendJob>>,
    rebuild_counts: HashMap<PlayerId, u32>,
    epoch_counter: u64,
    events: futures_channel::mpsc::UnboundedSender<Event>,
    internal_tx: mpsc::UnboundedSender<Internal>,
    shared: Arc<Shared>,
    api: API,
    ice_servers: Vec<RTCIceServer>,
    force_relay: bool,
    storage_path: Option<PathBuf>,
    fatal_seen: bool,
    closed: bool,
}

#[allow(clippy::too_many_arguments)]
impl Actor {
    pub fn new(
        self_id: PlayerId,
        max_players: u16,
        ws_tx: WsSink,
        roster: Vec<PlayerInfo>,
        events: futures_channel::mpsc::UnboundedSender<Event>,
        internal_tx: mpsc::UnboundedSender<Internal>,
        shared: Arc<Shared>,
        ice_servers: Vec<RTCIceServer>,
        force_relay: bool,
        storage_path: Option<PathBuf>,
    ) -> Self {
        Self {
            self_id,
            max_players,
            ws_tx,
            roster,
            peers: HashMap::new(),
            link_watch: HashMap::new(),
            send_queues: HashMap::new(),
            rebuild_counts: HashMap::new(),
            epoch_counter: 0,
            events,
            internal_tx,
            shared,
            api: webrtc::api::APIBuilder::new().build(),
            ice_servers,
            force_relay,
            storage_path,
            fatal_seen: false,
            closed: false,
        }
    }

    pub async fn run(
        mut self,
        mut cmd_rx: mpsc::UnboundedReceiver<Cmd>,
        mut internal_rx: mpsc::UnboundedReceiver<Internal>,
    ) {
        // Lower ID initiates: offer to every occupied+connected peer
        // with a higher ID. Peers with lower ids will offer to us when
        // they see our player-joined/rejoined.
        let targets: Vec<PlayerId> = self
            .roster
            .iter()
            .filter(|p| {
                p.id != self.self_id && p.occupied && p.connected && self.self_id < p.id
            })
            .map(|p| p.id)
            .collect();
        for player_id in targets {
            self.initiate_peer(player_id).await;
        }
        loop {
            tokio::select! {
                cmd = cmd_rx.recv() => match cmd {
                    Some(Cmd::Close { done }) => {
                        self.shutdown().await;
                        if let Some(done) = done {
                            let _ = done.send(());
                        }
                        break;
                    }
                    Some(cmd) => self.handle_cmd(cmd),
                    // The P2PGame was dropped without close(): leave anyway.
                    None => {
                        self.shutdown().await;
                        break;
                    }
                },
                Some(internal) = internal_rx.recv() => self.handle_internal(internal).await,
            }
        }
    }

    // -- teardown -------------------------------------------------------------

    async fn shutdown(&mut self) {
        self.closed = true;
        self.shared.closed.store(true, Ordering::SeqCst);
        let leave = encode_client_message(&ClientMessage::Leave);
        let _ = self.ws_tx.send(Message::text(leave)).await;
        let _ = self.ws_tx.close().await;
        self.teardown_peers();
        storage::clear(self.storage_path.as_deref());
    }

    fn teardown_peers(&mut self) {
        let ids: Vec<PlayerId> = self.peers.keys().copied().collect();
        for id in ids {
            self.close_peer(id);
        }
        // Dropping the watch senders / queues fails pending and future
        // sends with "closed".
        self.link_watch.clear();
        self.send_queues.clear();
    }

    fn close_peer(&mut self, player_id: PlayerId) {
        if let Some(peer) = self.peers.remove(&player_id) {
            peer.link.close();
            if let Some(watch_tx) = self.link_watch.get(&player_id) {
                watch_tx.send_replace(None);
            }
            let pc = peer.link.pc.clone();
            tokio::spawn(async move {
                let _ = pc.close().await;
            });
        }
    }

    // -- events ---------------------------------------------------------------

    fn emit(&self, event: Event) {
        if !self.closed {
            let _ = self.events.unbounded_send(event);
        }
    }

    fn sync_roster(&self) {
        *self.shared.roster.lock().expect("roster mirror") = self.roster.clone();
    }

    // -- API commands -----------------------------------------------------------

    fn handle_cmd(&mut self, cmd: Cmd) {
        match cmd {
            Cmd::SendReliable { to, data, done } => {
                if self.closed {
                    let _ = done.send(Err(crate::core::error::LobbyError::new(
                        "closed",
                        "game is closed",
                    )));
                    return;
                }
                let link_rx = match self.link_watch.entry(to) {
                    Entry::Occupied(e) => e.get().subscribe(),
                    Entry::Vacant(v) => {
                        let current = self.peers.get(&to).map(|p| p.link.clone());
                        v.insert(watch::channel(current).0).subscribe()
                    }
                };
                let queue = self.send_queues.entry(to).or_insert_with(|| {
                    let (tx, rx) = mpsc::unbounded_channel();
                    tokio::spawn(sender_task(to, rx, link_rx));
                    tx
                });
                let _ = queue.send(SendJob { data, done });
            }
            Cmd::SendBestEffort { to, data } => self.best_effort_to(to, data),
            Cmd::BroadcastBestEffort { data } => {
                let targets: Vec<PlayerId> = self
                    .roster
                    .iter()
                    .filter(|p| p.id != self.self_id && p.occupied)
                    .map(|p| p.id)
                    .collect();
                for player_id in targets {
                    self.best_effort_to(player_id, data.clone());
                }
            }
            Cmd::Close { .. } => unreachable!("handled in run()"),
        }
    }

    /// Best-effort contract: silently dropped when there is no open
    /// channel or its buffer is over the high-water mark.
    fn best_effort_to(&self, to: PlayerId, data: Bytes) {
        let Some(peer) = self.peers.get(&to) else { return };
        let link = &peer.link;
        if link.is_closed() || link.best_effort.ready_state() != RTCDataChannelState::Open {
            return;
        }
        let dc = link.best_effort.clone();
        tokio::spawn(async move {
            if dc.buffered_amount().await > SEND_HIGH_WATER {
                return; // best-effort: drop
            }
            let _ = dc.send(&data).await;
        });
    }

    // -- internal messages ------------------------------------------------------

    async fn handle_internal(&mut self, internal: Internal) {
        match internal {
            Internal::Ws(msg) => self.handle_server_message(msg).await,
            Internal::WsClosed => {
                if !self.closed && !self.fatal_seen {
                    self.fatal_seen = true;
                    self.emit(Event::SignalingClosed {
                        code: "connection-lost".into(),
                        message: "signaling connection lost; existing peer channels stay up"
                            .into(),
                    });
                }
            }
            Internal::Ice { player_id, epoch, candidate } => {
                if self.link_epoch_current(player_id, epoch) {
                    self.send_signal(player_id, &SignalPayload::Ice {
                        candidate: Some(candidate),
                    })
                    .await;
                }
            }
            Internal::State { player_id, epoch, state } => {
                self.handle_peer_state(player_id, epoch, state);
            }
            Internal::Reliable { player_id, epoch, data } => {
                let Some(peer) = self.peers.get_mut(&player_id) else { return };
                if peer.link.epoch != epoch {
                    return;
                }
                match parse_frame(&data) {
                    Ok(frame) => {
                        if let Some(message) =
                            peer.reassembler.push(&frame, crate::platform::now_ms())
                        {
                            self.emit(Event::Message {
                                from: player_id,
                                kind: MessageKind::Reliable,
                                data: Bytes::from(message),
                            });
                        }
                    }
                    Err(reason) => warn(&format!(
                        "dropping reliable frame from player {player_id}: {reason}"
                    )),
                }
            }
            Internal::BestEffort { player_id, epoch, data } => {
                if self.link_epoch_current(player_id, epoch) {
                    self.emit(Event::Message {
                        from: player_id,
                        kind: MessageKind::BestEffort,
                        data,
                    });
                }
            }
            Internal::Pair { player_id, epoch, local, remote } => {
                if self.link_epoch_current(player_id, epoch) {
                    self.emit(Event::CandidatePair { player_id, local, remote });
                }
            }
            Internal::Rebuild { player_id, epoch } => {
                if self.closed || !self.link_epoch_current(player_id, epoch) {
                    return;
                }
                let Some(peer) = self.peers.get(&player_id) else { return };
                if peer.link.pc.connection_state() != RTCPeerConnectionState::Failed {
                    return;
                }
                let Some(slot) = self.roster.get(player_id as usize) else { return };
                if !slot.occupied || !slot.connected {
                    return;
                }
                self.initiate_peer(player_id).await;
            }
        }
    }

    fn link_epoch_current(&self, player_id: PlayerId, epoch: u64) -> bool {
        self.peers
            .get(&player_id)
            .is_some_and(|p| p.link.epoch == epoch && !p.link.is_closed())
    }

    fn handle_peer_state(
        &mut self,
        player_id: PlayerId,
        epoch: u64,
        state: RTCPeerConnectionState,
    ) {
        if !self.link_epoch_current(player_id, epoch) {
            return;
        }
        self.emit(Event::PeerState { player_id, state: state.to_string() });
        match state {
            RTCPeerConnectionState::Connected => {
                self.rebuild_counts.remove(&player_id);
                let link = self.peers[&player_id].link.clone();
                let itx = self.internal_tx.clone();
                tokio::spawn(async move {
                    let pair = link
                        .pc
                        .sctp()
                        .transport()
                        .ice_transport()
                        .get_selected_candidate_pair()
                        .await;
                    if let Some(pair) = pair {
                        let _ = itx.send(Internal::Pair {
                            player_id,
                            epoch,
                            local: pair.local.typ.to_string(),
                            remote: pair.remote.typ.to_string(),
                        });
                    }
                });
            }
            RTCPeerConnectionState::Failed => self.handle_peer_failure(player_id, epoch),
            _ => {}
        }
    }

    /// Initiator rebuilds on failure with linear backoff, at most
    /// MAX_PEER_REBUILDS times until the next successful connect.
    fn handle_peer_failure(&mut self, player_id: PlayerId, epoch: u64) {
        let Some(peer) = self.peers.get(&player_id) else { return };
        if !peer.link.initiator || self.closed {
            return;
        }
        let count = self.rebuild_counts.get(&player_id).copied().unwrap_or(0) + 1;
        self.rebuild_counts.insert(player_id, count);
        if count > MAX_PEER_REBUILDS {
            warn(&format!(
                "giving up on player {player_id} after {MAX_PEER_REBUILDS} rebuilds"
            ));
            return;
        }
        let itx = self.internal_tx.clone();
        tokio::spawn(async move {
            crate::platform::sleep_ms(1000 * count as u64).await;
            let _ = itx.send(Internal::Rebuild { player_id, epoch });
        });
    }

    // -- lobby message handling ----------------------------------------------------

    async fn handle_server_message(&mut self, msg: ServerMessage) {
        match msg {
            ServerMessage::PlayerJoined { player_id, players } => {
                self.roster = roster_from_snapshot(self.max_players, &players);
                self.sync_roster();
                self.emit(Event::PlayerJoined { player_id });
                self.reset_peer(player_id).await;
            }
            ServerMessage::PlayerLeft { player_id, reason } => {
                let reason = PlayerLeftReason::from_wire(&reason);
                if let Some(slot) = self.roster.get_mut(player_id as usize) {
                    if reason == PlayerLeftReason::ExplicitLeave {
                        slot.occupied = false;
                    }
                    slot.connected = false;
                }
                self.sync_roster();
                if reason == PlayerLeftReason::ExplicitLeave {
                    self.close_peer(player_id);
                }
                // On "disconnected" the peer only lost signaling; an
                // established DataChannel may well still be alive.
                self.emit(Event::PlayerLeft { player_id, reason });
            }
            ServerMessage::PlayerRejoined { player_id, was_replacement } => {
                if let Some(slot) = self.roster.get_mut(player_id as usize) {
                    slot.occupied = true;
                    slot.connected = true;
                }
                self.sync_roster();
                self.emit(Event::PlayerRejoined { player_id, was_replacement });
                self.reset_peer(player_id).await;
            }
            ServerMessage::PlayerReplaced { player_id } => {
                if let Some(slot) = self.roster.get_mut(player_id as usize) {
                    slot.occupied = true;
                    slot.connected = true;
                }
                self.sync_roster();
                self.emit(Event::PlayerReplaced { player_id });
                self.reset_peer(player_id).await;
            }
            ServerMessage::RoomStarted => {
                self.shared.started.store(true, Ordering::SeqCst);
                self.emit(Event::Started);
            }
            ServerMessage::Signal { from, payload } => {
                self.handle_signal(from, payload).await;
            }
            ServerMessage::Error { code, message } => {
                if is_fatal_code(&code) {
                    self.fatal_seen = true;
                    if is_game_over_code(&code) {
                        self.teardown_peers();
                        // "session-superseded" means our own token
                        // resumed from another process, which owns the
                        // new token in the same store — don't clobber.
                        if code != "session-superseded" {
                            storage::clear(self.storage_path.as_deref());
                        }
                    }
                    self.emit(Event::SignalingClosed { code, message });
                } else {
                    self.emit(Event::LobbyError { code, message });
                }
            }
            // "joined" is only expected once, handled in connect().
            ServerMessage::Joined(_) | ServerMessage::Unknown => {}
        }
    }

    /// A peer got a new session: drop the old link, re-offer if initiator.
    async fn reset_peer(&mut self, player_id: PlayerId) {
        if player_id == self.self_id {
            return;
        }
        self.close_peer(player_id);
        self.rebuild_counts.remove(&player_id);
        if self.self_id < player_id {
            self.initiate_peer(player_id).await;
        }
    }

    // -- WebRTC signaling -----------------------------------------------------------

    async fn send_signal(&mut self, to: PlayerId, payload: &SignalPayload) {
        if self.closed {
            return;
        }
        let text = encode_client_message(&ClientMessage::Signal { to, payload });
        // If the socket died the reader task reports it.
        let _ = self.ws_tx.send(Message::text(text)).await;
    }

    async fn initiate_peer(&mut self, player_id: PlayerId) {
        if self.closed {
            return;
        }
        let link = match self.create_link(player_id, true).await {
            Ok(link) => link,
            Err(e) => {
                warn(&format!("creating peer connection to player {player_id} failed: {e}"));
                return;
            }
        };
        let offer = match link.pc.create_offer(None).await {
            Ok(offer) => offer,
            Err(e) => {
                warn(&format!("offer to player {player_id} failed: {e}"));
                return;
            }
        };
        if let Err(e) = link.pc.set_local_description(offer).await {
            warn(&format!("offer to player {player_id} failed: {e}"));
            return;
        }
        let Some(desc) = link.pc.local_description().await else { return };
        self.send_signal(player_id, &SignalPayload::Offer { sdp: desc.sdp })
            .await;
    }

    async fn handle_signal(&mut self, from: PlayerId, payload: SignalPayload) {
        if self.closed || from == self.self_id {
            return;
        }
        match payload {
            SignalPayload::Offer { sdp } => {
                if self.self_id < from {
                    warn(&format!(
                        "ignoring offer from higher-ID player {from} (protocol says we offer)"
                    ));
                    return;
                }
                // Every incoming offer starts a fresh session (initial
                // connect or the initiator rebuilding after a failure).
                let link = match self.create_link(from, false).await {
                    Ok(link) => link,
                    Err(e) => {
                        warn(&format!("answering player {from} failed: {e}"));
                        return;
                    }
                };
                let desc = match RTCSessionDescription::offer(sdp) {
                    Ok(desc) => desc,
                    Err(e) => {
                        warn(&format!("signal (offer) from player {from} failed: {e}"));
                        return;
                    }
                };
                if let Err(e) = link.pc.set_remote_description(desc).await {
                    warn(&format!("signal (offer) from player {from} failed: {e}"));
                    return;
                }
                self.flush_candidates(from).await;
                let answer = match link.pc.create_answer(None).await {
                    Ok(answer) => answer,
                    Err(e) => {
                        warn(&format!("signal (offer) from player {from} failed: {e}"));
                        return;
                    }
                };
                if let Err(e) = link.pc.set_local_description(answer).await {
                    warn(&format!("signal (offer) from player {from} failed: {e}"));
                    return;
                }
                let Some(desc) = link.pc.local_description().await else { return };
                self.send_signal(from, &SignalPayload::Answer { sdp: desc.sdp })
                    .await;
            }
            SignalPayload::Answer { sdp } => {
                let Some(link) = self.peers.get(&from).map(|p| p.link.clone()) else {
                    warn(&format!("ignoring stale answer from player {from}"));
                    return;
                };
                if link.is_closed()
                    || link.pc.signaling_state() != RTCSignalingState::HaveLocalOffer
                {
                    warn(&format!("ignoring stale answer from player {from}"));
                    return;
                }
                let desc = match RTCSessionDescription::answer(sdp) {
                    Ok(desc) => desc,
                    Err(e) => {
                        warn(&format!("signal (answer) from player {from} failed: {e}"));
                        return;
                    }
                };
                if let Err(e) = link.pc.set_remote_description(desc).await {
                    warn(&format!("signal (answer) from player {from} failed: {e}"));
                    return;
                }
                self.flush_candidates(from).await;
            }
            SignalPayload::Ice { candidate } => {
                let Some(peer) = self.peers.get_mut(&from) else { return };
                if peer.link.is_closed() {
                    return;
                }
                // null/absent = end-of-candidates; nothing to add.
                let Some(candidate) = candidate else { return };
                if let Some(pending) = peer.pending_candidates.as_mut() {
                    pending.push(candidate);
                } else {
                    let link = peer.link.clone();
                    add_candidate(&link, candidate).await;
                }
            }
            SignalPayload::Unknown => {}
        }
    }

    async fn flush_candidates(&mut self, player_id: PlayerId) {
        let Some(peer) = self.peers.get_mut(&player_id) else { return };
        let Some(pending) = peer.pending_candidates.take() else { return };
        let link = peer.link.clone();
        for candidate in pending {
            add_candidate(&link, candidate).await;
        }
    }

    // -- link construction ------------------------------------------------------------

    async fn create_link(
        &mut self,
        player_id: PlayerId,
        initiator: bool,
    ) -> Result<Arc<Link>, webrtc::Error> {
        self.close_peer(player_id);
        self.epoch_counter += 1;
        let epoch = self.epoch_counter;

        let config = RTCConfiguration {
            ice_servers: self.ice_servers.clone(),
            ice_transport_policy: if self.force_relay {
                RTCIceTransportPolicy::Relay
            } else {
                RTCIceTransportPolicy::All
            },
            ..Default::default()
        };
        let pc = Arc::new(self.api.new_peer_connection(config).await?);
        let reliable = pc
            .create_data_channel(
                RELIABLE_LABEL,
                Some(RTCDataChannelInit {
                    ordered: Some(true),
                    negotiated: Some(RELIABLE_CHANNEL_ID),
                    ..Default::default()
                }),
            )
            .await?;
        let best_effort = pc
            .create_data_channel(
                BEST_EFFORT_LABEL,
                Some(RTCDataChannelInit {
                    ordered: Some(false),
                    max_retransmits: Some(0),
                    negotiated: Some(BEST_EFFORT_CHANNEL_ID),
                    ..Default::default()
                }),
            )
            .await?;

        let link = Arc::new(Link {
            player_id,
            initiator,
            epoch,
            pc: pc.clone(),
            reliable: reliable.clone(),
            best_effort: best_effort.clone(),
            closed: Default::default(),
            reliable_open: Default::default(),
            state_notify: Default::default(),
            drain_notify: Default::default(),
            next_msg_id: Default::default(),
        });

        let itx = self.internal_tx.clone();
        pc.on_ice_candidate(Box::new(move |candidate: Option<RTCIceCandidate>| {
            let itx = itx.clone();
            Box::pin(async move {
                // TS skips sending the null end-of-candidates marker.
                let Some(candidate) = candidate else { return };
                match candidate.to_json() {
                    Ok(init) => {
                        if let Ok(value) = serde_json::to_value(&init) {
                            let _ = itx.send(Internal::Ice { player_id, epoch, candidate: value });
                        }
                    }
                    Err(e) => warn(&format!("serializing ICE candidate failed: {e}")),
                }
            })
        }));

        let itx = self.internal_tx.clone();
        pc.on_peer_connection_state_change(Box::new(move |state| {
            let itx = itx.clone();
            Box::pin(async move {
                let _ = itx.send(Internal::State { player_id, epoch, state });
            })
        }));

        let itx = self.internal_tx.clone();
        reliable.on_message(Box::new(move |msg: DataChannelMessage| {
            let itx = itx.clone();
            Box::pin(async move {
                let _ = itx.send(Internal::Reliable { player_id, epoch, data: msg.data });
            })
        }));

        let itx = self.internal_tx.clone();
        best_effort.on_message(Box::new(move |msg: DataChannelMessage| {
            let itx = itx.clone();
            Box::pin(async move {
                let _ = itx.send(Internal::BestEffort { player_id, epoch, data: msg.data });
            })
        }));

        {
            let link = link.clone();
            reliable.on_open(Box::new(move || {
                Box::pin(async move {
                    link.reliable_open.store(true, Ordering::SeqCst);
                    link.state_notify.notify_waiters();
                })
            }));
        }
        {
            let link = link.clone();
            reliable.on_close(Box::new(move || {
                let link = link.clone();
                Box::pin(async move {
                    link.state_notify.notify_waiters();
                })
            }));
        }
        reliable.set_buffered_amount_low_threshold(SEND_LOW_WATER).await;
        {
            let link = link.clone();
            reliable
                .on_buffered_amount_low(Box::new(move || {
                    let link = link.clone();
                    Box::pin(async move {
                        link.drain_notify.notify_waiters();
                    })
                }))
                .await;
        }

        self.peers.insert(
            player_id,
            Peer {
                link: link.clone(),
                reassembler: Reassembler::new(),
                pending_candidates: Some(Vec::new()),
            },
        );
        match self.link_watch.entry(player_id) {
            Entry::Occupied(e) => {
                e.get().send_replace(Some(link.clone()));
            }
            Entry::Vacant(v) => {
                v.insert(watch::channel(Some(link.clone())).0);
            }
        }
        Ok(link)
    }
}

async fn add_candidate(link: &Link, candidate: serde_json::Value) {
    let init: RTCIceCandidateInit = match serde_json::from_value(candidate) {
        Ok(init) => init,
        Err(e) => {
            warn(&format!(
                "malformed ICE candidate from player {}: {e}",
                link.player_id
            ));
            return;
        }
    };
    if let Err(e) = link.pc.add_ice_candidate(init).await {
        if !link.is_closed() {
            warn(&format!(
                "addIceCandidate for player {} failed: {e}",
                link.player_id
            ));
        }
    }
}
