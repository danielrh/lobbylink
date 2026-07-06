//! Live smoke test against the production deployment (both entry
//! points). Ignored by default; run explicitly with:
//!   cargo test --test prod_smoke -- --ignored

#![cfg(not(target_arch = "wasm32"))]

use std::time::Duration;

use bytes::Bytes;
use p2p_lobby_client::{ConnectOptions, CreateOptions, Event, MessageKind, P2PGame};

#[tokio::test]
#[ignore = "hits the live pqrstuvw.xyz server"]
async fn prod_join_both_endpoints() {
    for (i, server) in ["https://pqrstuvw.xyz/lobbylink", "https://pqrstuvw.xyz:4443"]
        .into_iter()
        .enumerate()
    {
        let game = P2PGame::connect(ConnectOptions {
            server: server.to_string(),
            code: format!("RUSTSMOKE{}N{i}", std::process::id()),
            create: Some(CreateOptions::new(2)),
            ..Default::default()
        })
        .await
        .unwrap_or_else(|e| panic!("connect to {server} failed: {e}"));
        assert_eq!(game.self_id(), 0, "{server}");
        assert!(
            !game.ice_servers().is_empty(),
            "{server} should issue ICE servers"
        );
        game.close().await.unwrap();
    }
}

/// force_relay end-to-end: both sides must select a relay/relay
/// candidate pair through the prod coturn, and data must flow.
#[tokio::test]
#[ignore = "hits the live pqrstuvw.xyz server and TURN relay"]
async fn prod_force_relay() {
    let server = "https://pqrstuvw.xyz/lobbylink".to_string();
    let code = format!("RUSTRELAY{}", std::process::id());
    let mut a = P2PGame::connect(ConnectOptions {
        server: server.clone(),
        code: code.clone(),
        create: Some(CreateOptions::new(2)),
        force_relay: true,
        ..Default::default()
    })
    .await
    .expect("A connects");
    let mut b = P2PGame::connect(ConnectOptions {
        server,
        code,
        force_relay: true,
        ..Default::default()
    })
    .await
    .expect("B connects");

    let pair = tokio::time::timeout(Duration::from_secs(40), async {
        loop {
            match a.next_event().await.expect("event stream ended") {
                Event::CandidatePair { player_id: 1, local, remote } => {
                    return (local, remote)
                }
                Event::PeerState { state, .. } if state == "failed" => {
                    panic!("relay connection failed")
                }
                _ => {}
            }
        }
    })
    .await
    .expect("timed out waiting for relay candidate pair");
    assert_eq!(pair, ("relay".to_string(), "relay".to_string()));

    a.send_reliable(1, Bytes::from_static(b"via relay")).await.unwrap();
    let data = tokio::time::timeout(Duration::from_secs(20), async {
        loop {
            if let Event::Message { from: 0, kind: MessageKind::Reliable, data } =
                b.next_event().await.expect("event stream ended")
            {
                return data;
            }
        }
    })
    .await
    .expect("timed out waiting for relayed message");
    assert_eq!(data, "via relay");

    b.close().await.unwrap();
    a.close().await.unwrap();
}
