//! Shared mutable state plus lobby/signaling handlers for the wasm
//! backend. All state lives behind one Rc<RefCell<Inner>>; long-lived
//! JS closures capture only Weak references so nothing cycles.

use std::any::Any;
use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use futures_channel::mpsc::UnboundedSender;
use futures_channel::oneshot;
use wasm_bindgen::prelude::*;
use wasm_bindgen::JsCast;
use wasm_bindgen_futures::{spawn_local, JsFuture};
use web_sys::{
    CloseEvent, MessageEvent, RtcConfiguration, RtcIceCandidateInit, RtcIceTransportPolicy,
    RtcPeerConnection, RtcSdpType, RtcSessionDescriptionInit, RtcSignalingState, WebSocket,
};

use super::link::{create_link, Link, SendQueue};
use super::storage;
use crate::core::error::LobbyError;
use crate::core::events::{Event, PlayerLeftReason};
use crate::core::limits::MAX_PEER_REBUILDS;
use crate::core::options::StorageKind;
use crate::core::protocol::{
    encode_client_message, is_fatal_code, is_game_over_code, ClientMessage, IceServer,
    ServerMessage, SignalPayload,
};
use crate::core::roster::{roster_from_snapshot, PlayerId, PlayerInfo};
use crate::platform::warn;

pub(super) struct Inner {
    pub ws: WebSocket,
    pub self_id: PlayerId,
    pub max_players: u16,
    pub roster: Vec<PlayerInfo>,
    pub started: bool,
    pub closed: bool,
    pub fatal_seen: bool,
    pub peers: HashMap<PlayerId, Rc<Link>>,
    pub rebuild_counts: HashMap<PlayerId, u32>,
    pub epoch_counter: u64,
    pub events: UnboundedSender<Event>,
    pub ice_servers: Vec<IceServer>,
    pub force_relay: bool,
    pub storage_key: Option<String>,
    pub storage_kind: StorageKind,
    pub link_waiters: HashMap<PlayerId, Vec<oneshot::Sender<Rc<Link>>>>,
    pub send_queues: HashMap<PlayerId, Rc<RefCell<SendQueue>>>,
    /// Keeps the permanent WebSocket handlers alive.
    pub ws_closures: Vec<Box<dyn Any>>,
}

impl Inner {
    pub fn rtc_config(&self) -> Result<RtcConfiguration, LobbyError> {
        let config = RtcConfiguration::new();
        let json = serde_json::to_string(&self.ice_servers)
            .map_err(|e| LobbyError::new("peer-failed", format!("ice servers: {e}")))?;
        let servers = js_sys::JSON::parse(&json)
            .map_err(|e| LobbyError::new("peer-failed", js_err(&e)))?;
        config.set_ice_servers(&servers);
        if self.force_relay {
            config.set_ice_transport_policy(RtcIceTransportPolicy::Relay);
        }
        Ok(config)
    }
}

pub(super) fn js_err(value: &JsValue) -> String {
    value
        .dyn_ref::<js_sys::Error>()
        .map(|e| String::from(e.message()))
        .unwrap_or_else(|| format!("{value:?}"))
}

/// The browser may grow new states; read the raw string instead of
/// trusting the web-sys enum to stay exhaustive.
pub(super) fn connection_state_str(pc: &RtcPeerConnection) -> String {
    js_sys::Reflect::get(pc.as_ref(), &JsValue::from_str("connectionState"))
        .ok()
        .and_then(|v| v.as_string())
        .unwrap_or_else(|| "unknown".to_string())
}

pub(super) fn emit(rc: &Rc<RefCell<Inner>>, event: Event) {
    let inner = rc.borrow();
    if !inner.closed {
        let _ = inner.events.unbounded_send(event);
    }
}

pub(super) fn send_signal(rc: &Rc<RefCell<Inner>>, to: PlayerId, payload: &SignalPayload) {
    let inner = rc.borrow();
    if inner.closed || inner.ws.ready_state() != WebSocket::OPEN {
        return;
    }
    let text = encode_client_message(&ClientMessage::Signal { to, payload });
    // If the socket died, onclose reports it.
    let _ = inner.ws.send_with_str(&text);
}

// ---------------------------------------------------------------------------
// WebSocket lifecycle
// ---------------------------------------------------------------------------

pub(super) fn install_ws_handlers(rc: &Rc<RefCell<Inner>>) {
    let ws = rc.borrow().ws.clone();
    let mut closures: Vec<Box<dyn Any>> = Vec::new();

    {
        let weak = Rc::downgrade(rc);
        let onmessage = Closure::<dyn FnMut(MessageEvent)>::new(move |ev: MessageEvent| {
            let Some(rc) = weak.upgrade() else { return };
            let Some(text) = ev.data().as_string() else { return };
            match serde_json::from_str::<ServerMessage>(&text) {
                Ok(msg) => handle_server_message(&rc, msg),
                Err(_) => warn("malformed server message"),
            }
        });
        ws.set_onmessage(Some(onmessage.as_ref().unchecked_ref()));
        closures.push(Box::new(onmessage));
    }
    {
        let weak = Rc::downgrade(rc);
        let onclose = Closure::<dyn FnMut(CloseEvent)>::new(move |_ev: CloseEvent| {
            let Some(rc) = weak.upgrade() else { return };
            let report = {
                let mut inner = rc.borrow_mut();
                if inner.closed || inner.fatal_seen {
                    false
                } else {
                    inner.fatal_seen = true;
                    true
                }
            };
            if report {
                emit(&rc, Event::SignalingClosed {
                    code: "connection-lost".into(),
                    message: "signaling connection lost; existing peer channels stay up".into(),
                });
            }
        });
        ws.set_onclose(Some(onclose.as_ref().unchecked_ref()));
        closures.push(Box::new(onclose));
    }
    // onerror carries no useful detail; onclose covers the state change.
    ws.set_onerror(None);
    ws.set_onopen(None);

    rc.borrow_mut().ws_closures = closures;
}

pub(super) fn shutdown(rc: &Rc<RefCell<Inner>>) {
    {
        let mut inner = rc.borrow_mut();
        if inner.closed {
            return;
        }
        inner.closed = true;
        if inner.ws.ready_state() == WebSocket::OPEN {
            let _ = inner
                .ws
                .send_with_str(&encode_client_message(&ClientMessage::Leave));
        }
        let _ = inner.ws.close_with_code_and_reason(1000, "client closed");
        inner.ws.set_onmessage(None);
        inner.ws.set_onclose(None);
        inner.ws.set_onerror(None);
        inner.ws.set_onopen(None);
        inner.ws_closures.clear();
        storage::clear(inner.storage_key.as_deref(), inner.storage_kind);
    }
    teardown_peers(rc);
}

pub(super) fn teardown_peers(rc: &Rc<RefCell<Inner>>) {
    let (links, waiters, queues) = {
        let mut inner = rc.borrow_mut();
        let links: Vec<Rc<Link>> = inner.peers.drain().map(|(_, link)| link).collect();
        let waiters: Vec<_> = inner.link_waiters.drain().collect();
        let queues: Vec<_> = inner.send_queues.drain().collect();
        (links, waiters, queues)
    };
    for link in links {
        link.close();
    }
    // Dropping waiter senders fails pending awaits with "closed";
    // dropping queued jobs fails their oneshots the same way.
    drop(waiters);
    drop(queues);
}

pub(super) fn close_peer(rc: &Rc<RefCell<Inner>>, player_id: PlayerId) {
    let link = rc.borrow_mut().peers.remove(&player_id);
    if let Some(link) = link {
        link.close();
    }
}

// ---------------------------------------------------------------------------
// Lobby message handling
// ---------------------------------------------------------------------------

pub(super) fn handle_server_message(rc: &Rc<RefCell<Inner>>, msg: ServerMessage) {
    if rc.borrow().closed {
        return;
    }
    match msg {
        ServerMessage::PlayerJoined { player_id, players } => {
            {
                let mut inner = rc.borrow_mut();
                inner.roster = roster_from_snapshot(inner.max_players, &players);
            }
            emit(rc, Event::PlayerJoined { player_id });
            reset_peer(rc, player_id);
        }
        ServerMessage::PlayerLeft { player_id, reason } => {
            let reason = PlayerLeftReason::from_wire(&reason);
            {
                let mut inner = rc.borrow_mut();
                if let Some(slot) = inner.roster.get_mut(player_id as usize) {
                    if reason == PlayerLeftReason::ExplicitLeave {
                        slot.occupied = false;
                    }
                    slot.connected = false;
                }
            }
            if reason == PlayerLeftReason::ExplicitLeave {
                close_peer(rc, player_id);
            }
            // On "disconnected" the peer only lost signaling; keep it.
            emit(rc, Event::PlayerLeft { player_id, reason });
        }
        ServerMessage::PlayerRejoined { player_id, was_replacement } => {
            mark_occupied(rc, player_id);
            emit(rc, Event::PlayerRejoined { player_id, was_replacement });
            reset_peer(rc, player_id);
        }
        ServerMessage::PlayerReplaced { player_id } => {
            mark_occupied(rc, player_id);
            emit(rc, Event::PlayerReplaced { player_id });
            reset_peer(rc, player_id);
        }
        ServerMessage::RoomStarted => {
            rc.borrow_mut().started = true;
            emit(rc, Event::Started);
        }
        ServerMessage::Signal { from, payload } => {
            spawn_local(handle_signal(rc.clone(), from, payload));
        }
        ServerMessage::Error { code, message } => {
            if is_fatal_code(&code) {
                rc.borrow_mut().fatal_seen = true;
                if is_game_over_code(&code) {
                    teardown_peers(rc);
                    // Another tab of ours owns the new token under the
                    // same storage key — don't clobber it.
                    if code != "session-superseded" {
                        let inner = rc.borrow();
                        storage::clear(inner.storage_key.as_deref(), inner.storage_kind);
                    }
                }
                emit(rc, Event::SignalingClosed { code, message });
            } else {
                emit(rc, Event::LobbyError { code, message });
            }
        }
        // "joined" only expected once (handled in connect); unknown
        // message types are ignored for forward compatibility.
        ServerMessage::Joined(_) | ServerMessage::Unknown => {}
    }
}

fn mark_occupied(rc: &Rc<RefCell<Inner>>, player_id: PlayerId) {
    let mut inner = rc.borrow_mut();
    if let Some(slot) = inner.roster.get_mut(player_id as usize) {
        slot.occupied = true;
        slot.connected = true;
    }
}

/// A peer got a new session: drop the old link, re-offer if initiator.
fn reset_peer(rc: &Rc<RefCell<Inner>>, player_id: PlayerId) {
    if rc.borrow().self_id == player_id {
        return;
    }
    close_peer(rc, player_id);
    rc.borrow_mut().rebuild_counts.remove(&player_id);
    if rc.borrow().self_id < player_id {
        initiate_peer(rc, player_id);
    }
}

// ---------------------------------------------------------------------------
// WebRTC signaling
// ---------------------------------------------------------------------------

pub(super) fn initiate_peer(rc: &Rc<RefCell<Inner>>, player_id: PlayerId) {
    if rc.borrow().closed {
        return;
    }
    let link = match create_link(rc, player_id, true) {
        Ok(link) => link,
        Err(e) => {
            warn(&format!("creating peer connection to player {player_id} failed: {e}"));
            return;
        }
    };
    let rc = rc.clone();
    spawn_local(async move {
        if let Err(reason) = offer_flow(&rc, &link).await {
            warn(&format!("offer to player {player_id} failed: {reason}"));
        }
    });
}

async fn offer_flow(rc: &Rc<RefCell<Inner>>, link: &Rc<Link>) -> Result<(), String> {
    let offer = JsFuture::from(link.pc.create_offer())
        .await
        .map_err(|e| js_err(&e))?;
    if link.closed.get() {
        return Ok(());
    }
    JsFuture::from(link.pc.set_local_description(offer.unchecked_ref()))
        .await
        .map_err(|e| js_err(&e))?;
    if link.closed.get() {
        return Ok(());
    }
    let sdp = link
        .pc
        .local_description()
        .map(|d| d.sdp())
        .ok_or("no local description")?;
    send_signal(rc, link.player_id, &SignalPayload::Offer { sdp });
    Ok(())
}

pub(super) async fn handle_signal(rc: Rc<RefCell<Inner>>, from: PlayerId, payload: SignalPayload) {
    {
        let inner = rc.borrow();
        if inner.closed || from == inner.self_id {
            return;
        }
    }
    match payload {
        SignalPayload::Offer { sdp } => {
            if rc.borrow().self_id < from {
                warn(&format!(
                    "ignoring offer from higher-ID player {from} (protocol says we offer)"
                ));
                return;
            }
            // Every incoming offer starts a fresh session (initial
            // connect or the initiator rebuilding after a failure).
            let link = match create_link(&rc, from, false) {
                Ok(link) => link,
                Err(e) => {
                    warn(&format!("answering player {from} failed: {e}"));
                    return;
                }
            };
            if let Err(reason) = answer_flow(&rc, &link, from, sdp).await {
                warn(&format!("signal (offer) from player {from} failed: {reason}"));
            }
        }
        SignalPayload::Answer { sdp } => {
            let Some(link) = rc.borrow().peers.get(&from).cloned() else {
                warn(&format!("ignoring stale answer from player {from}"));
                return;
            };
            if link.closed.get()
                || link.pc.signaling_state() != RtcSignalingState::HaveLocalOffer
            {
                warn(&format!("ignoring stale answer from player {from}"));
                return;
            }
            let desc = RtcSessionDescriptionInit::new(RtcSdpType::Answer);
            desc.set_sdp(&sdp);
            if let Err(e) = JsFuture::from(link.pc.set_remote_description(&desc)).await {
                warn(&format!("signal (answer) from player {from} failed: {}", js_err(&e)));
                return;
            }
            flush_candidates(&link).await;
        }
        SignalPayload::Ice { candidate } => {
            let Some(link) = rc.borrow().peers.get(&from).cloned() else { return };
            if link.closed.get() {
                return;
            }
            // null/absent = end-of-candidates; nothing to add.
            let Some(candidate) = candidate else { return };
            {
                let mut pending = link.pending_candidates.borrow_mut();
                if let Some(pending) = pending.as_mut() {
                    pending.push(candidate);
                    return;
                }
            }
            add_candidate(&link, candidate).await;
        }
        SignalPayload::Unknown => {}
    }
}

async fn answer_flow(
    rc: &Rc<RefCell<Inner>>,
    link: &Rc<Link>,
    from: PlayerId,
    sdp: String,
) -> Result<(), String> {
    let desc = RtcSessionDescriptionInit::new(RtcSdpType::Offer);
    desc.set_sdp(&sdp);
    JsFuture::from(link.pc.set_remote_description(&desc))
        .await
        .map_err(|e| js_err(&e))?;
    if link.closed.get() {
        return Ok(());
    }
    flush_candidates(link).await;
    let answer = JsFuture::from(link.pc.create_answer())
        .await
        .map_err(|e| js_err(&e))?;
    if link.closed.get() {
        return Ok(());
    }
    JsFuture::from(link.pc.set_local_description(answer.unchecked_ref()))
        .await
        .map_err(|e| js_err(&e))?;
    if link.closed.get() {
        return Ok(());
    }
    let sdp = link
        .pc
        .local_description()
        .map(|d| d.sdp())
        .ok_or("no local description")?;
    send_signal(rc, from, &SignalPayload::Answer { sdp });
    Ok(())
}

async fn flush_candidates(link: &Rc<Link>) {
    let pending = link.pending_candidates.borrow_mut().take();
    let Some(pending) = pending else { return };
    for candidate in pending {
        add_candidate(link, candidate).await;
    }
}

async fn add_candidate(link: &Rc<Link>, candidate: serde_json::Value) {
    let json = match serde_json::to_string(&candidate) {
        Ok(json) => json,
        Err(_) => return,
    };
    let init: RtcIceCandidateInit = match js_sys::JSON::parse(&json) {
        Ok(js) => js.unchecked_into(),
        Err(e) => {
            warn(&format!(
                "malformed ICE candidate from player {}: {}",
                link.player_id,
                js_err(&e)
            ));
            return;
        }
    };
    let promise = link
        .pc
        .add_ice_candidate_with_opt_rtc_ice_candidate_init(Some(&init));
    if let Err(e) = JsFuture::from(promise).await {
        if !link.closed.get() {
            warn(&format!(
                "addIceCandidate for player {} failed: {}",
                link.player_id,
                js_err(&e)
            ));
        }
    }
}

/// Initiator rebuilds on failure with linear backoff, at most
/// MAX_PEER_REBUILDS times until the next successful connect.
pub(super) fn handle_peer_failure(rc: &Rc<RefCell<Inner>>, player_id: PlayerId, epoch: u64) {
    let count = {
        let mut inner = rc.borrow_mut();
        if inner.closed {
            return;
        }
        let Some(link) = inner.peers.get(&player_id) else { return };
        if link.epoch != epoch || !link.initiator {
            return;
        }
        let count = inner.rebuild_counts.get(&player_id).copied().unwrap_or(0) + 1;
        inner.rebuild_counts.insert(player_id, count);
        count
    };
    if count > MAX_PEER_REBUILDS {
        warn(&format!(
            "giving up on player {player_id} after {MAX_PEER_REBUILDS} rebuilds"
        ));
        return;
    }
    let rc = rc.clone();
    spawn_local(async move {
        crate::platform::sleep_ms(1000 * count as u64).await;
        let retry = {
            let inner = rc.borrow();
            !inner.closed
                && inner.peers.get(&player_id).is_some_and(|link| {
                    link.epoch == epoch && connection_state_str(&link.pc) == "failed"
                })
                && inner
                    .roster
                    .get(player_id as usize)
                    .is_some_and(|slot| slot.occupied && slot.connected)
        };
        if retry {
            initiate_peer(&rc, player_id);
        }
    });
}

// ---------------------------------------------------------------------------
// Candidate-pair stats (needed for force_relay validation)
// ---------------------------------------------------------------------------

pub(super) async fn report_candidate_pair(rc: Rc<RefCell<Inner>>, link: Rc<Link>) {
    let Ok(report) = JsFuture::from(link.pc.get_stats()).await else { return };
    // RTCStatsReport is a maplike; treat it as a JS Map.
    let map: js_sys::Map = report.unchecked_into();
    let field = |obj: &JsValue, key: &str| {
        js_sys::Reflect::get(obj, &JsValue::from_str(key))
            .ok()
            .filter(|v| !v.is_undefined() && !v.is_null())
    };
    let str_field =
        |obj: &JsValue, key: &str| field(obj, key).and_then(|v| v.as_string());

    let mut pair_id: Option<String> = None;
    map.for_each(&mut |value, _key| {
        if str_field(&value, "type").as_deref() == Some("transport") {
            if let Some(id) = str_field(&value, "selectedCandidatePairId") {
                pair_id = Some(id);
            }
        }
    });
    if pair_id.is_none() {
        map.for_each(&mut |value, _key| {
            if str_field(&value, "type").as_deref() != Some("candidate-pair") {
                return;
            }
            let selected = field(&value, "selected") == Some(JsValue::TRUE);
            let nominated = field(&value, "nominated") == Some(JsValue::TRUE)
                && str_field(&value, "state").as_deref() == Some("succeeded");
            if selected || nominated {
                if let Some(id) = str_field(&value, "id") {
                    pair_id = Some(id);
                }
            }
        });
    }
    let Some(pair_id) = pair_id else { return };
    if link.closed.get() {
        return;
    }
    let pair = map.get(&JsValue::from_str(&pair_id));
    if pair.is_undefined() {
        return;
    }
    let candidate_type = |key: &str| {
        str_field(&pair, key)
            .map(|id| map.get(&JsValue::from_str(&id)))
            .and_then(|c| str_field(&c, "candidateType"))
            .unwrap_or_else(|| "unknown".to_string())
    };
    let local = candidate_type("localCandidateId");
    let remote = candidate_type("remoteCandidateId");
    // Only report for the link that is still current.
    let current = rc
        .borrow()
        .peers
        .get(&link.player_id)
        .is_some_and(|l| Rc::ptr_eq(l, &link));
    if current && !link.closed.get() {
        emit(&rc, Event::CandidatePair { player_id: link.player_id, local, remote });
    }
}
