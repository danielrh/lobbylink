//! One WebRTC session to one remote player, plus the per-player
//! sender machinery that serializes reliable sends and applies
//! backpressure (pause above SEND_HIGH_WATER, resume below
//! SEND_LOW_WATER via the buffered-amount-low callback with a poll
//! fallback).

use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::Arc;
use std::time::Duration;

use bytes::Bytes;
use tokio::sync::{mpsc, oneshot, watch, Notify};
use webrtc::data_channel::data_channel_state::RTCDataChannelState;
use webrtc::data_channel::RTCDataChannel;
use webrtc::peer_connection::RTCPeerConnection;

use crate::core::error::LobbyError;
use crate::core::framing::{chunk_count, make_frame, CHUNK_PAYLOAD};
use crate::core::limits::{
    CHANNEL_TIMEOUT_MS, DRAIN_POLL_MS, SEND_HIGH_WATER, SEND_LOW_WATER,
};
use crate::core::roster::PlayerId;

pub(super) struct Link {
    pub player_id: PlayerId,
    pub initiator: bool,
    /// Generation counter; callbacks from a torn-down session carry a
    /// stale epoch and are ignored by the actor.
    pub epoch: u64,
    pub pc: Arc<RTCPeerConnection>,
    pub reliable: Arc<RTCDataChannel>,
    pub best_effort: Arc<RTCDataChannel>,
    pub closed: AtomicBool,
    pub reliable_open: AtomicBool,
    /// Wakes waiters on reliable-channel open/close and link close.
    pub state_notify: Notify,
    /// Wakes senders when bufferedAmount drops below the low water mark.
    pub drain_notify: Notify,
    pub next_msg_id: AtomicU32,
}

impl Link {
    pub fn is_closed(&self) -> bool {
        self.closed.load(Ordering::SeqCst)
    }

    pub fn close(&self) {
        self.closed.store(true, Ordering::SeqCst);
        self.state_notify.notify_waiters();
        self.drain_notify.notify_waiters();
    }
}

pub(super) struct SendJob {
    pub data: Bytes,
    pub done: oneshot::Sender<Result<(), LobbyError>>,
}

/// Runs for the lifetime of one player's send queue: jobs are strictly
/// serialized so reliable messages to a peer keep their send order.
pub(super) async fn sender_task(
    player_id: PlayerId,
    mut queue: mpsc::UnboundedReceiver<SendJob>,
    mut link_rx: watch::Receiver<Option<Arc<Link>>>,
) {
    while let Some(job) = queue.recv().await {
        let result = send_one(player_id, &mut link_rx, job.data).await;
        let _ = job.done.send(result);
    }
}

async fn send_one(
    player_id: PlayerId,
    link_rx: &mut watch::Receiver<Option<Arc<Link>>>,
    data: Bytes,
) -> Result<(), LobbyError> {
    let link = wait_link(player_id, link_rx).await?;
    match tokio::time::timeout(
        Duration::from_millis(CHANNEL_TIMEOUT_MS),
        wait_reliable_open(&link),
    )
    .await
    {
        Ok(result) => result?,
        Err(_) => {
            return Err(LobbyError::new(
                "channel-timeout",
                format!("timed out opening channel to player {player_id}"),
            ))
        }
    }
    let msg_id = link.next_msg_id.fetch_add(1, Ordering::Relaxed);
    let total = chunk_count(data.len());
    for seq in 0..total {
        if link.is_closed() {
            return Err(LobbyError::new(
                "send-failed",
                format!("connection to player {player_id} closed mid-send"),
            ));
        }
        wait_drain(&link).await?;
        let start = seq as usize * CHUNK_PAYLOAD;
        let end = (start + CHUNK_PAYLOAD).min(data.len());
        let frame = make_frame(msg_id, seq, total, &data[start..end]);
        link.reliable
            .send(&Bytes::from(frame))
            .await
            .map_err(|e| {
                LobbyError::new(
                    "send-failed",
                    format!("send to player {player_id} failed: {e}"),
                )
            })?;
    }
    Ok(())
}

/// Resolve the current link to a peer, waiting up to CHANNEL_TIMEOUT
/// for one to appear (mirrors the TS awaitLink).
async fn wait_link(
    player_id: PlayerId,
    link_rx: &mut watch::Receiver<Option<Arc<Link>>>,
) -> Result<Arc<Link>, LobbyError> {
    let wait = async {
        loop {
            let current: Option<Arc<Link>> = {
                let value = link_rx.borrow_and_update();
                value.as_ref().filter(|l| !l.is_closed()).cloned()
            };
            if let Some(link) = current {
                return Ok(link);
            }
            if link_rx.changed().await.is_err() {
                return Err(LobbyError::new("closed", "game is closed"));
            }
        }
    };
    match tokio::time::timeout(Duration::from_millis(CHANNEL_TIMEOUT_MS), wait).await {
        Ok(result) => result,
        Err(_) => Err(LobbyError::new(
            "channel-timeout",
            format!("no WebRTC session with player {player_id} within {CHANNEL_TIMEOUT_MS}ms"),
        )),
    }
}

/// Resolves when the reliable channel is open; errors on close.
async fn wait_reliable_open(link: &Link) -> Result<(), LobbyError> {
    loop {
        if link.is_closed() {
            return Err(LobbyError::new(
                "peer-closed",
                format!("channel to player {} is closed", link.player_id),
            ));
        }
        match link.reliable.ready_state() {
            RTCDataChannelState::Open => return Ok(()),
            RTCDataChannelState::Connecting | RTCDataChannelState::Unspecified => {}
            _ => {
                return Err(LobbyError::new(
                    "peer-closed",
                    format!("channel to player {} closed", link.player_id),
                ))
            }
        }
        let notified = link.state_notify.notified();
        tokio::pin!(notified);
        notified.as_mut().enable();
        if link.is_closed()
            || link.reliable.ready_state() != RTCDataChannelState::Connecting
        {
            continue;
        }
        notified.await;
    }
}

/// Waits until bufferedAmount drains below the low-water mark (only
/// engages when it is above the high-water mark).
async fn wait_drain(link: &Link) -> Result<(), LobbyError> {
    if link.reliable.buffered_amount().await <= SEND_HIGH_WATER {
        return Ok(());
    }
    loop {
        if link.is_closed() {
            return Err(LobbyError::new(
                "peer-closed",
                format!("channel to player {} closed", link.player_id),
            ));
        }
        if link.reliable.buffered_amount().await <= SEND_LOW_WATER {
            return Ok(());
        }
        let notified = link.drain_notify.notified();
        tokio::pin!(notified);
        notified.as_mut().enable();
        // Poll fallback in case the low event never fires (teardown races).
        let _ = tokio::time::timeout(Duration::from_millis(DRAIN_POLL_MS), notified).await;
    }
}
