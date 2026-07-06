//! One browser RTCPeerConnection to one remote player, plus the
//! per-player send queue that serializes reliable sends and applies
//! backpressure. Single-threaded: Rc/RefCell/Cell, and no RefCell
//! borrow is ever held across an await.

use std::any::Any;
use std::cell::{Cell, RefCell};
use std::collections::VecDeque;
use std::rc::{Rc, Weak};

use bytes::Bytes;
use futures_channel::oneshot;
use wasm_bindgen::prelude::*;
use wasm_bindgen::JsCast;
use wasm_bindgen_futures::spawn_local;
use web_sys::{
    MessageEvent, RtcDataChannel, RtcDataChannelInit, RtcDataChannelState,
    RtcDataChannelType, RtcPeerConnection, RtcPeerConnectionIceEvent,
};

use super::inner::{
    connection_state_str, emit, handle_peer_failure, js_err, report_candidate_pair,
    send_signal, Inner,
};
use crate::core::error::LobbyError;
use crate::core::events::{Event, MessageKind};
use crate::core::framing::{chunk_count, make_frame, parse_frame, CHUNK_PAYLOAD};
use crate::core::limits::{
    BEST_EFFORT_CHANNEL_ID, BEST_EFFORT_LABEL, CHANNEL_TIMEOUT_MS, DRAIN_POLL_MS,
    RELIABLE_CHANNEL_ID, RELIABLE_LABEL, SEND_HIGH_WATER, SEND_LOW_WATER,
};
use crate::core::protocol::SignalPayload;
use crate::core::reassembly::Reassembler;
use crate::core::roster::PlayerId;
use crate::platform::warn;
use crate::util::timeout_ms;

pub(super) struct Link {
    pub player_id: PlayerId,
    pub initiator: bool,
    /// Generation counter; stale callbacks are ignored via this.
    pub epoch: u64,
    pub pc: RtcPeerConnection,
    pub reliable: RtcDataChannel,
    pub best_effort: RtcDataChannel,
    pub closed: Cell<bool>,
    pub next_msg_id: Cell<u32>,
    pub reassembler: RefCell<Reassembler>,
    /// Remote ICE candidates queued until the remote description is
    /// set; None once flushed.
    pub pending_candidates: RefCell<Option<Vec<serde_json::Value>>>,
    /// Resolved with true on reliable-channel open, false on close.
    open_waiters: RefCell<Vec<oneshot::Sender<bool>>>,
    drain_waiters: RefCell<Vec<oneshot::Sender<()>>>,
    /// Registered JS callbacks; kept alive here, dropped on close.
    closures: RefCell<Vec<Box<dyn Any>>>,
}

impl Link {
    pub fn close(&self) {
        if self.closed.replace(true) {
            return;
        }
        for waiter in self.open_waiters.borrow_mut().drain(..) {
            let _ = waiter.send(false);
        }
        for waiter in self.drain_waiters.borrow_mut().drain(..) {
            let _ = waiter.send(());
        }
        // Detach handlers before dropping their closures.
        self.reliable.set_onmessage(None);
        self.reliable.set_onopen(None);
        self.reliable.set_onclose(None);
        self.reliable.set_onbufferedamountlow(None);
        self.best_effort.set_onmessage(None);
        self.pc.set_onicecandidate(None);
        self.pc.set_onconnectionstatechange(None);
        self.reliable.close();
        self.best_effort.close();
        self.pc.close();
        self.closures.borrow_mut().clear();
    }
}

impl Drop for Link {
    fn drop(&mut self) {
        self.close();
    }
}

fn keep(link: &Link, closure: Box<dyn Any>) {
    link.closures.borrow_mut().push(closure);
}

fn message_bytes(data: JsValue) -> Option<Vec<u8>> {
    if let Some(buffer) = data.dyn_ref::<js_sys::ArrayBuffer>() {
        return Some(js_sys::Uint8Array::new(buffer).to_vec());
    }
    if let Some(text) = data.as_string() {
        return Some(text.into_bytes());
    }
    warn("dropping DataChannel message of unexpected type");
    None
}

/// Build a fresh peer connection + the two pre-negotiated channels and
/// register it (closing any previous link to the player).
pub(super) fn create_link(
    rc: &Rc<RefCell<Inner>>,
    player_id: PlayerId,
    initiator: bool,
) -> Result<Rc<Link>, LobbyError> {
    super::inner::close_peer(rc, player_id);
    let (epoch, config) = {
        let mut inner = rc.borrow_mut();
        inner.epoch_counter += 1;
        (inner.epoch_counter, inner.rtc_config()?)
    };
    let pc = RtcPeerConnection::new_with_configuration(&config).map_err(|e| {
        LobbyError::new(
            "peer-failed",
            format!("cannot create RTCPeerConnection: {}", js_err(&e)),
        )
    })?;

    let reliable_init = RtcDataChannelInit::new();
    reliable_init.set_negotiated(true);
    reliable_init.set_id(RELIABLE_CHANNEL_ID);
    reliable_init.set_ordered(true);
    let reliable = pc.create_data_channel_with_data_channel_dict(RELIABLE_LABEL, &reliable_init);
    let best_effort_init = RtcDataChannelInit::new();
    best_effort_init.set_negotiated(true);
    best_effort_init.set_id(BEST_EFFORT_CHANNEL_ID);
    best_effort_init.set_ordered(false);
    best_effort_init.set_max_retransmits(0);
    let best_effort =
        pc.create_data_channel_with_data_channel_dict(BEST_EFFORT_LABEL, &best_effort_init);
    // Firefox defaults DataChannels to Blob.
    reliable.set_binary_type(RtcDataChannelType::Arraybuffer);
    best_effort.set_binary_type(RtcDataChannelType::Arraybuffer);

    let link = Rc::new(Link {
        player_id,
        initiator,
        epoch,
        pc: pc.clone(),
        reliable: reliable.clone(),
        best_effort: best_effort.clone(),
        closed: Cell::new(false),
        next_msg_id: Cell::new(0),
        reassembler: RefCell::new(Reassembler::new()),
        pending_candidates: RefCell::new(Some(Vec::new())),
        open_waiters: RefCell::new(Vec::new()),
        drain_waiters: RefCell::new(Vec::new()),
        closures: RefCell::new(Vec::new()),
    });

    let weak_inner: Weak<RefCell<Inner>> = Rc::downgrade(rc);
    let weak_link = Rc::downgrade(&link);

    {
        let weak_inner = weak_inner.clone();
        let weak_link = weak_link.clone();
        let onicecandidate =
            Closure::<dyn FnMut(RtcPeerConnectionIceEvent)>::new(move |ev: RtcPeerConnectionIceEvent| {
                let (Some(rc), Some(link)) = (weak_inner.upgrade(), weak_link.upgrade()) else {
                    return;
                };
                if link.closed.get() {
                    return;
                }
                // TS skips sending the null end-of-candidates marker.
                let Some(candidate) = ev.candidate() else { return };
                // JSON.stringify invokes toJSON => RTCIceCandidateInit.
                let Ok(json) = js_sys::JSON::stringify(&candidate) else { return };
                let Some(json) = json.as_string() else { return };
                let Ok(value) = serde_json::from_str::<serde_json::Value>(&json) else {
                    return;
                };
                send_signal(&rc, player_id, &SignalPayload::Ice { candidate: Some(value) });
            });
        pc.set_onicecandidate(Some(onicecandidate.as_ref().unchecked_ref()));
        keep(&link, Box::new(onicecandidate));
    }

    {
        let weak_inner = weak_inner.clone();
        let weak_link = weak_link.clone();
        let onstate = Closure::<dyn FnMut()>::new(move || {
            let (Some(rc), Some(link)) = (weak_inner.upgrade(), weak_link.upgrade()) else {
                return;
            };
            if link.closed.get() {
                return;
            }
            let state = connection_state_str(&link.pc);
            emit(&rc, Event::PeerState { player_id, state: state.clone() });
            match state.as_str() {
                "connected" => {
                    rc.borrow_mut().rebuild_counts.remove(&player_id);
                    spawn_local(report_candidate_pair(rc.clone(), link.clone()));
                }
                "failed" => handle_peer_failure(&rc, player_id, epoch),
                _ => {}
            }
        });
        pc.set_onconnectionstatechange(Some(onstate.as_ref().unchecked_ref()));
        keep(&link, Box::new(onstate));
    }

    {
        let weak_inner = weak_inner.clone();
        let weak_link = weak_link.clone();
        let onreliable = Closure::<dyn FnMut(MessageEvent)>::new(move |ev: MessageEvent| {
            let (Some(rc), Some(link)) = (weak_inner.upgrade(), weak_link.upgrade()) else {
                return;
            };
            if link.closed.get() {
                return;
            }
            let Some(bytes) = message_bytes(ev.data()) else { return };
            match parse_frame(&bytes) {
                Ok(frame) => {
                    let complete = link
                        .reassembler
                        .borrow_mut()
                        .push(&frame, crate::platform::now_ms());
                    if let Some(message) = complete {
                        emit(&rc, Event::Message {
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
        });
        reliable.set_onmessage(Some(onreliable.as_ref().unchecked_ref()));
        keep(&link, Box::new(onreliable));
    }

    {
        let weak_inner = weak_inner.clone();
        let weak_link = weak_link.clone();
        let onbest = Closure::<dyn FnMut(MessageEvent)>::new(move |ev: MessageEvent| {
            let (Some(rc), Some(link)) = (weak_inner.upgrade(), weak_link.upgrade()) else {
                return;
            };
            if link.closed.get() {
                return;
            }
            let Some(bytes) = message_bytes(ev.data()) else { return };
            emit(&rc, Event::Message {
                from: player_id,
                kind: MessageKind::BestEffort,
                data: Bytes::from(bytes),
            });
        });
        best_effort.set_onmessage(Some(onbest.as_ref().unchecked_ref()));
        keep(&link, Box::new(onbest));
    }

    {
        let weak_link = weak_link.clone();
        let onopen = Closure::<dyn FnMut()>::new(move || {
            let Some(link) = weak_link.upgrade() else { return };
            for waiter in link.open_waiters.borrow_mut().drain(..) {
                let _ = waiter.send(true);
            }
        });
        reliable.set_onopen(Some(onopen.as_ref().unchecked_ref()));
        keep(&link, Box::new(onopen));
    }
    {
        let weak_link = weak_link.clone();
        let onclose = Closure::<dyn FnMut()>::new(move || {
            let Some(link) = weak_link.upgrade() else { return };
            for waiter in link.open_waiters.borrow_mut().drain(..) {
                let _ = waiter.send(false);
            }
        });
        reliable.set_onclose(Some(onclose.as_ref().unchecked_ref()));
        keep(&link, Box::new(onclose));
    }
    reliable.set_buffered_amount_low_threshold(SEND_LOW_WATER as u32);
    {
        let weak_link = weak_link.clone();
        let onlow = Closure::<dyn FnMut()>::new(move || {
            let Some(link) = weak_link.upgrade() else { return };
            for waiter in link.drain_waiters.borrow_mut().drain(..) {
                let _ = waiter.send(());
            }
        });
        reliable.set_onbufferedamountlow(Some(onlow.as_ref().unchecked_ref()));
        keep(&link, Box::new(onlow));
    }

    let mut inner = rc.borrow_mut();
    inner.peers.insert(player_id, link.clone());
    if let Some(waiters) = inner.link_waiters.remove(&player_id) {
        for waiter in waiters {
            let _ = waiter.send(link.clone());
        }
    }
    Ok(link)
}

// ---------------------------------------------------------------------------
// Reliable send queue (per player, strictly serialized)
// ---------------------------------------------------------------------------

pub(super) struct SendJob {
    data: Bytes,
    done: oneshot::Sender<Result<(), LobbyError>>,
}

#[derive(Default)]
pub(super) struct SendQueue {
    items: VecDeque<SendJob>,
    running: bool,
}

pub(super) fn queue_reliable(
    rc: &Rc<RefCell<Inner>>,
    to: PlayerId,
    data: Bytes,
) -> oneshot::Receiver<Result<(), LobbyError>> {
    let (done_tx, done_rx) = oneshot::channel();
    let start_worker = {
        let mut inner = rc.borrow_mut();
        let queue = inner.send_queues.entry(to).or_default().clone();
        let mut queue = queue.borrow_mut();
        queue.items.push_back(SendJob { data, done: done_tx });
        !std::mem::replace(&mut queue.running, true)
    };
    if start_worker {
        spawn_local(send_worker(rc.clone(), to));
    }
    done_rx
}

async fn send_worker(rc: Rc<RefCell<Inner>>, to: PlayerId) {
    loop {
        let queue = match rc.borrow().send_queues.get(&to) {
            Some(queue) => queue.clone(),
            // Torn down: pending jobs were dropped, which fails their
            // oneshot receivers with "closed".
            None => return,
        };
        let job = {
            let mut queue = queue.borrow_mut();
            match queue.items.pop_front() {
                Some(job) => job,
                None => {
                    queue.running = false;
                    return;
                }
            }
        };
        let result = send_one(&rc, to, job.data).await;
        let _ = job.done.send(result);
    }
}

async fn send_one(
    rc: &Rc<RefCell<Inner>>,
    to: PlayerId,
    data: Bytes,
) -> Result<(), LobbyError> {
    let link = wait_link(rc, to).await?;
    wait_reliable_open(&link).await?;
    let msg_id = link.next_msg_id.get();
    link.next_msg_id.set(msg_id.wrapping_add(1));
    let total = chunk_count(data.len());
    for seq in 0..total {
        if link.closed.get() {
            return Err(LobbyError::new(
                "send-failed",
                format!("connection to player {to} closed mid-send"),
            ));
        }
        wait_drain(&link).await?;
        let start = seq as usize * CHUNK_PAYLOAD;
        let end = (start + CHUNK_PAYLOAD).min(data.len());
        let frame = make_frame(msg_id, seq, total, &data[start..end]);
        link.reliable.send_with_u8_array(&frame).map_err(|e| {
            LobbyError::new("send-failed", format!("send to player {to} failed: {}", js_err(&e)))
        })?;
    }
    Ok(())
}

async fn wait_link(rc: &Rc<RefCell<Inner>>, to: PlayerId) -> Result<Rc<Link>, LobbyError> {
    let waiter = {
        let inner = rc.borrow();
        if inner.closed {
            return Err(LobbyError::new("closed", "game is closed"));
        }
        if let Some(link) = inner.peers.get(&to) {
            if !link.closed.get() {
                return Ok(link.clone());
            }
        }
        drop(inner);
        let (tx, rx) = oneshot::channel();
        rc.borrow_mut().link_waiters.entry(to).or_default().push(tx);
        rx
    };
    match timeout_ms(CHANNEL_TIMEOUT_MS, waiter).await {
        Some(Ok(link)) => Ok(link),
        Some(Err(_)) => Err(LobbyError::new("closed", "game is closed")),
        None => Err(LobbyError::new(
            "channel-timeout",
            format!("no WebRTC session with player {to} within {CHANNEL_TIMEOUT_MS}ms"),
        )),
    }
}

async fn wait_reliable_open(link: &Rc<Link>) -> Result<(), LobbyError> {
    if link.closed.get() {
        return Err(LobbyError::new(
            "peer-closed",
            format!("channel to player {} is closed", link.player_id),
        ));
    }
    match link.reliable.ready_state() {
        RtcDataChannelState::Open => return Ok(()),
        RtcDataChannelState::Connecting => {}
        _ => {
            return Err(LobbyError::new(
                "peer-closed",
                format!("channel to player {} closed", link.player_id),
            ))
        }
    }
    let (tx, rx) = oneshot::channel();
    link.open_waiters.borrow_mut().push(tx);
    match timeout_ms(CHANNEL_TIMEOUT_MS, rx).await {
        Some(Ok(true)) => Ok(()),
        Some(_) => Err(LobbyError::new(
            "peer-closed",
            format!("channel to player {} closed", link.player_id),
        )),
        None => Err(LobbyError::new(
            "channel-timeout",
            format!("timed out opening channel to player {}", link.player_id),
        )),
    }
}

/// Waits until bufferedAmount drains below the low-water mark (only
/// engages above the high-water mark). Falls back to polling in case
/// the bufferedamountlow event never fires.
async fn wait_drain(link: &Rc<Link>) -> Result<(), LobbyError> {
    if link.reliable.buffered_amount() as usize <= SEND_HIGH_WATER {
        return Ok(());
    }
    loop {
        if link.closed.get() {
            return Err(LobbyError::new(
                "peer-closed",
                format!("channel to player {} closed", link.player_id),
            ));
        }
        if link.reliable.buffered_amount() as usize <= SEND_LOW_WATER {
            return Ok(());
        }
        let (tx, rx) = oneshot::channel();
        link.drain_waiters.borrow_mut().push(tx);
        let _ = timeout_ms(DRAIN_POLL_MS, rx).await;
    }
}

/// Best-effort datagram: silently dropped when there is no open
/// channel or its buffer is over the high-water mark.
pub(super) fn best_effort_to(inner: &Inner, to: PlayerId, data: &[u8]) {
    let Some(link) = inner.peers.get(&to) else { return };
    if link.closed.get() || link.best_effort.ready_state() != RtcDataChannelState::Open {
        return;
    }
    if link.best_effort.buffered_amount() as usize > SEND_HIGH_WATER {
        return;
    }
    let _ = link.best_effort.send_with_u8_array(data);
}
