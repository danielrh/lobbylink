//! Browser test harness for the wasm backend of p2p-lobby-client.
//! Logs "TESTLOG ..." lines; the runner greps for ALL-TESTS-PASS /
//! DRIVER-ALL-PASS. Modes:
//!  - pair:   two wasm P2PGames in this page run the full suite
//!  - echo:   wasm responder (echoes messages; "quit" -> close)
//!  - driver: wasm driver asserting against an echo responder

use std::future::{poll_fn, Future};
use std::pin::pin;
use std::task::Poll;

use bytes::Bytes;
use p2p_lobby_client::{
    ConnectOptions, CreateOptions, Event, MessageKind, P2PGame, PlayerLeftReason,
};
use wasm_bindgen::prelude::*;

fn log(msg: &str) {
    web_sys::console::log_1(&JsValue::from_str(msg));
}

async fn sleep_ms(ms: u64) {
    let promise = js_sys::Promise::new(&mut |resolve, _| {
        web_sys::window()
            .unwrap()
            .set_timeout_with_callback_and_timeout_and_arguments_0(&resolve, ms as i32)
            .unwrap();
    });
    let _ = wasm_bindgen_futures::JsFuture::from(promise).await;
}

async fn timeout_ms<F: Future>(ms: u64, fut: F) -> Option<F::Output> {
    let mut fut = pin!(fut);
    let mut timer = pin!(sleep_ms(ms));
    poll_fn(|cx| {
        if let Poll::Ready(v) = fut.as_mut().poll(cx) {
            return Poll::Ready(Some(v));
        }
        if timer.as_mut().poll(cx).is_ready() {
            return Poll::Ready(None);
        }
        Poll::Pending
    })
    .await
}

struct Suite {
    fails: u32,
}

impl Suite {
    fn check(&mut self, cond: bool, name: &str) {
        if cond {
            log(&format!("TESTLOG PASS {name}"));
        } else {
            self.fails += 1;
            log(&format!("TESTLOG FAIL {name}"));
        }
    }
}

async fn expect_event<T>(
    game: &mut P2PGame,
    what: &str,
    mut f: impl FnMut(&Event) -> Option<T>,
) -> Option<T> {
    timeout_ms(25_000, async {
        loop {
            let Some(ev) = game.next_event().await else {
                log(&format!("TESTLOG FAIL event stream ended waiting for {what}"));
                return None;
            };
            if let Some(v) = f(&ev) {
                return Some(v);
            }
        }
    })
    .await
    .flatten()
}

async fn wait_connected(game: &mut P2PGame, peer: u16) -> bool {
    expect_event(game, "connected", |ev| match ev {
        Event::PeerState { player_id, state } if *player_id == peer && state == "connected" => {
            Some(true)
        }
        Event::PeerState { player_id, state } if *player_id == peer && state == "failed" => {
            Some(false)
        }
        _ => None,
    })
    .await
    .unwrap_or(false)
}

async fn recv_reliable(game: &mut P2PGame, from: u16, what: &str) -> Option<Bytes> {
    expect_event(game, what, |ev| match ev {
        Event::Message { from: f, kind: MessageKind::Reliable, data } if *f == from => {
            Some(data.clone())
        }
        _ => None,
    })
    .await
}

fn pattern(len: usize, seed: u8) -> Bytes {
    Bytes::from(
        (0..len)
            .map(|i| (i as u8).wrapping_mul(31).wrapping_add(seed))
            .collect::<Vec<u8>>(),
    )
}

#[wasm_bindgen]
pub async fn run_mode(mode: String, server: String, code: String) {
    std::panic::set_hook(Box::new(|info| log(&format!("TESTLOG PANIC {info}"))));
    match mode.as_str() {
        "pair" => pair_test(server, code, false).await,
        "pair-relay" => pair_test(server, code, true).await,
        "echo" => echo_responder(server, code).await,
        "driver" => driver(server, code).await,
        other => log(&format!("TESTLOG FAIL unknown mode {other}")),
    }
}

/// Two wasm clients in one page: the full §15-style suite.
async fn pair_test(server: String, code: String, force_relay: bool) {
    let mut t = Suite { fails: 0 };

    let mut create = CreateOptions::new(2);
    create.wait_until_full = true;
    let a = P2PGame::connect(ConnectOptions {
        server: server.clone(),
        code: code.clone(),
        create: Some(create),
        force_relay,
        ..Default::default()
    })
    .await;
    let mut a = match a {
        Ok(game) => game,
        Err(e) => {
            log(&format!("TESTLOG FAIL connect A: {e}"));
            return;
        }
    };
    t.check(a.self_id() == 0, "A is player 0");
    t.check(!a.started(), "room not started while waiting");

    let b = P2PGame::connect(ConnectOptions {
        server: server.clone(),
        code: code.clone(),
        force_relay,
        ..Default::default()
    })
    .await;
    let mut b = match b {
        Ok(game) => game,
        Err(e) => {
            log(&format!("TESTLOG FAIL connect B: {e}"));
            return;
        }
    };
    t.check(b.self_id() == 1, "B is player 1");

    let joined = expect_event(&mut a, "player-joined", |ev| {
        matches!(ev, Event::PlayerJoined { player_id: 1 }).then_some(())
    })
    .await;
    t.check(joined.is_some(), "A sees B join");
    let started = expect_event(&mut a, "started", |ev| {
        matches!(ev, Event::Started).then_some(())
    })
    .await;
    t.check(started.is_some(), "waitUntilFull start");

    t.check(wait_connected(&mut a, 1).await, "A connected to B");
    t.check(wait_connected(&mut b, 0).await, "B connected to A");

    let pair = expect_event(&mut a, "candidate-pair", |ev| match ev {
        Event::CandidatePair { player_id: 1, local, remote } => {
            Some(format!("{local}/{remote}"))
        }
        _ => None,
    })
    .await;
    t.check(pair.is_some(), "candidate pair reported");
    let pair = pair.unwrap_or_default();
    log(&format!("TESTLOG candidate-pair {pair}"));
    if force_relay {
        t.check(pair == "relay/relay", "forced relay selected relay/relay");
    }

    t.check(
        a.send_reliable(1, Bytes::from_static(b"hello")).await.is_ok(),
        "send small",
    );
    t.check(
        recv_reliable(&mut b, 0, "small").await.as_deref() == Some(b"hello"),
        "recv small",
    );

    t.check(b.send_reliable(0, Bytes::new()).await.is_ok(), "send 0-byte");
    t.check(
        recv_reliable(&mut a, 1, "0-byte").await.is_some_and(|d| d.is_empty()),
        "recv 0-byte",
    );

    let big = pattern(300 * 1024, 7);
    t.check(a.send_reliable(1, big.clone()).await.is_ok(), "send 300KB");
    t.check(
        recv_reliable(&mut b, 0, "300KB").await.as_ref() == Some(&big),
        "recv 300KB",
    );

    let huge = pattern(8 * 1024 * 1024, 13);
    t.check(a.send_reliable(1, huge.clone()).await.is_ok(), "send 8MiB");
    t.check(
        recv_reliable(&mut b, 0, "8MiB").await.as_ref() == Some(&huge),
        "recv 8MiB (backpressure)",
    );

    t.check(
        a.send_reliable(1, Bytes::from_static(b"first")).await.is_ok()
            && a.send_reliable(1, Bytes::from_static(b"second")).await.is_ok(),
        "send ordered",
    );
    t.check(
        recv_reliable(&mut b, 0, "ordered1").await.as_deref() == Some(b"first")
            && recv_reliable(&mut b, 0, "ordered2").await.as_deref() == Some(b"second"),
        "recv ordered",
    );

    t.check(best_effort_roundtrip(&a, &mut b, 0).await, "best-effort A->B");
    t.check(best_effort_roundtrip(&b, &mut a, 1).await, "best-effort B->A");

    let full = P2PGame::connect(ConnectOptions {
        server: server.clone(),
        code: code.clone(),
        ..Default::default()
    })
    .await;
    t.check(
        full.err().is_some_and(|e| e.code == "room-full"),
        "third join rejected room-full",
    );

    let _ = b.close().await;
    let left = expect_event(&mut a, "player-left", |ev| match ev {
        Event::PlayerLeft { player_id: 1, reason: PlayerLeftReason::ExplicitLeave } => Some(()),
        _ => None,
    })
    .await;
    t.check(left.is_some(), "explicit leave visible");
    let _ = a.close().await;

    if t.fails == 0 {
        log("TESTLOG ALL-TESTS-PASS");
    } else {
        log(&format!("TESTLOG {} FAILURES", t.fails));
    }
}

async fn best_effort_roundtrip(tx: &P2PGame, rx: &mut P2PGame, from: u16) -> bool {
    for _ in 0..50 {
        if tx
            .send_best_effort(1 - from, Bytes::from_static(b"dgram"))
            .await
            .is_err()
        {
            return false;
        }
        let got = timeout_ms(300, async {
            loop {
                match rx.next_event().await {
                    Some(Event::Message { from: f, kind: MessageKind::BestEffort, data })
                        if f == from =>
                    {
                        return data == "dgram";
                    }
                    Some(_) => {}
                    None => return false,
                }
            }
        })
        .await;
        if got == Some(true) {
            return true;
        }
    }
    false
}

/// Responder: create the room, echo everything back; reliable "quit"
/// closes the game.
async fn echo_responder(server: String, code: String) {
    let game = P2PGame::connect(ConnectOptions {
        server,
        code,
        create: Some(CreateOptions::new(2)),
        ..Default::default()
    })
    .await;
    let mut game = match game {
        Ok(game) => game,
        Err(e) => {
            log(&format!("TESTLOG FAIL responder connect: {e}"));
            return;
        }
    };
    log(&format!("TESTLOG responder joined as {}", game.self_id()));
    loop {
        let Some(ev) = game.next_event().await else { return };
        match ev {
            Event::Message { from, kind: MessageKind::Reliable, data } => {
                if data.as_ref() == b"quit" {
                    let _ = game.close().await;
                    log("TESTLOG RESPONDER-DONE");
                    return;
                }
                if let Err(e) = game.send_reliable(from, data).await {
                    log(&format!("TESTLOG FAIL responder echo: {e}"));
                }
            }
            Event::Message { from, kind: MessageKind::BestEffort, data } => {
                let _ = game.send_best_effort(from, data).await;
            }
            other => log(&format!("TESTLOG responder event {other:?}")),
        }
    }
}

/// Driver: join an existing room (retrying while the responder is
/// still starting) and assert echo roundtrips.
async fn driver(server: String, code: String) {
    let mut t = Suite { fails: 0 };
    let mut game = None;
    for _ in 0..40 {
        match P2PGame::connect(ConnectOptions {
            server: server.clone(),
            code: code.clone(),
            ..Default::default()
        })
        .await
        {
            Ok(g) => {
                game = Some(g);
                break;
            }
            Err(_) => sleep_ms(500).await,
        }
    }
    let Some(mut game) = game else {
        log("TESTLOG FAIL driver could not join room");
        return;
    };
    let peer = if game.self_id() == 0 { 1 } else { 0 };
    t.check(wait_connected(&mut game, peer).await, "driver connected");

    for (name, data) in [
        ("small", Bytes::from_static(b"interop-hello")),
        ("0-byte", Bytes::new()),
        ("300KB", pattern(300 * 1024, 3)),
        ("8MiB", pattern(8 * 1024 * 1024, 9)),
    ] {
        let ok = game.send_reliable(peer, data.clone()).await.is_ok();
        t.check(ok, &format!("driver send {name}"));
        let echo = recv_reliable(&mut game, peer, name).await;
        t.check(echo.as_ref() == Some(&data), &format!("driver echo {name}"));
    }

    let mut best_effort = false;
    for _ in 0..50 {
        let _ = game.send_best_effort(peer, Bytes::from_static(b"dgram")).await;
        let got = timeout_ms(300, async {
            loop {
                match game.next_event().await {
                    Some(Event::Message { kind: MessageKind::BestEffort, data, .. }) => {
                        return data == "dgram";
                    }
                    Some(_) => {}
                    None => return false,
                }
            }
        })
        .await;
        if got == Some(true) {
            best_effort = true;
            break;
        }
    }
    t.check(best_effort, "driver best-effort echo");

    t.check(
        game.send_reliable(peer, Bytes::from_static(b"quit")).await.is_ok(),
        "driver quit sent",
    );
    let left = expect_event(&mut game, "responder leave", |ev| match ev {
        Event::PlayerLeft { reason: PlayerLeftReason::ExplicitLeave, .. } => Some(()),
        _ => None,
    })
    .await;
    t.check(left.is_some(), "responder left explicitly");
    let _ = game.close().await;

    if t.fails == 0 {
        log("TESTLOG DRIVER-ALL-PASS");
    } else {
        log(&format!("TESTLOG DRIVER {} FAILURES", t.fails));
    }
}
