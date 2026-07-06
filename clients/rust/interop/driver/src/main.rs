//! Native interop driver: joins an existing room (retrying while the
//! browser responder starts up) and asserts echo roundtrips. Prints
//! INTEROP-PASS on success, exits nonzero on failure.
//! Usage: interopdriver <server> <code> [--no-origin]

use std::time::Duration;

use bytes::Bytes;
use p2p_lobby_client::{ConnectOptions, Event, MessageKind, P2PGame, PlayerLeftReason};

fn pattern(len: usize, seed: u8) -> Bytes {
    Bytes::from(
        (0..len)
            .map(|i| (i as u8).wrapping_mul(31).wrapping_add(seed))
            .collect::<Vec<u8>>(),
    )
}

async fn recv_reliable(game: &mut P2PGame, from: u16, what: &str) -> Bytes {
    tokio::time::timeout(Duration::from_secs(25), async {
        loop {
            match game.next_event().await {
                Some(Event::Message { from: f, kind: MessageKind::Reliable, data })
                    if f == from =>
                {
                    return data;
                }
                Some(_) => {}
                None => panic!("event stream ended waiting for {what}"),
            }
        }
    })
    .await
    .unwrap_or_else(|_| panic!("timeout waiting for {what}"))
}

#[tokio::main]
async fn main() {
    let mut args = std::env::args().skip(1);
    let server = args.next().expect("server arg");
    let code = args.next().expect("code arg");
    let no_origin = args.next().as_deref() == Some("--no-origin");

    let mut game = None;
    for _ in 0..40 {
        match P2PGame::connect(ConnectOptions {
            server: server.clone(),
            code: code.clone(),
            origin: no_origin.then(String::new),
            ..Default::default()
        })
        .await
        {
            Ok(g) => {
                game = Some(g);
                break;
            }
            Err(e) => {
                eprintln!("waiting for room: {e}");
                tokio::time::sleep(Duration::from_millis(500)).await;
            }
        }
    }
    let mut game = game.expect("could not join room");
    let peer = if game.self_id() == 0 { 1 } else { 0 };
    println!("joined as player {}, driving against {peer}", game.self_id());

    // Wait for the peer connection.
    tokio::time::timeout(Duration::from_secs(30), async {
        loop {
            match game.next_event().await.expect("event stream ended") {
                Event::PeerState { player_id, state } if player_id == peer && state == "connected" => break,
                Event::PeerState { player_id, state } if player_id == peer && state == "failed" => {
                    panic!("peer connection failed")
                }
                _ => {}
            }
        }
    })
    .await
    .expect("timeout waiting for peer connection");
    println!("peer connected");

    for (name, data) in [
        ("small", Bytes::from_static(b"interop-hello")),
        ("0-byte", Bytes::new()),
        ("300KB", pattern(300 * 1024, 3)),
        ("8MiB", pattern(8 * 1024 * 1024, 9)),
    ] {
        game.send_reliable(peer, data.clone()).await.unwrap_or_else(|e| panic!("send {name}: {e}"));
        let echo = recv_reliable(&mut game, peer, name).await;
        assert_eq!(echo, data, "echo mismatch for {name}");
        println!("echo ok: {name} ({} bytes)", data.len());
    }

    // Best-effort echo (lossy: retry).
    let mut best_effort = false;
    for _ in 0..50 {
        game.send_best_effort(peer, Bytes::from_static(b"dgram")).await.expect("best-effort send");
        let got = tokio::time::timeout(Duration::from_millis(300), async {
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
        if got == Ok(true) {
            best_effort = true;
            break;
        }
    }
    assert!(best_effort, "best-effort echo never arrived");
    println!("echo ok: best-effort");

    game.send_reliable(peer, Bytes::from_static(b"quit")).await.expect("send quit");
    tokio::time::timeout(Duration::from_secs(15), async {
        loop {
            match game.next_event().await.expect("event stream ended") {
                Event::PlayerLeft { reason: PlayerLeftReason::ExplicitLeave, .. } => break,
                _ => {}
            }
        }
    })
    .await
    .expect("timeout waiting for responder leave");
    println!("responder left explicitly");

    game.close().await.expect("close");
    println!("INTEROP-PASS");
}
