//! Integration tests against a real local lobby server
//! (dist/p2p-lobby-server at the repo root, or $LOBBYLINK_SERVER).
//! Covers the guide §15 test plan minus TURN: join/create with
//! waitUntilFull, reliable small/0-byte/300KB/8MiB/concurrent/ordered,
//! best-effort both ways, room-full, explicit leave, token resume
//! with session-superseded.

#![cfg(not(target_arch = "wasm32"))]

use std::net::TcpListener;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::time::Duration;

use bytes::Bytes;
use p2p_lobby_client::{
    ConnectOptions, CreateOptions, Event, MessageKind, P2PGame, PlayerLeftReason,
};

fn server_binary() -> PathBuf {
    if let Ok(path) = std::env::var("LOBBYLINK_SERVER") {
        return PathBuf::from(path);
    }
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../dist/p2p-lobby-server")
}

struct Server {
    child: Child,
    port: u16,
}

impl Server {
    // The child is waited on in Drop.
    #[allow(clippy::zombie_processes)]
    async fn start() -> Server {
        let bin = server_binary();
        assert!(
            bin.exists(),
            "lobby server binary not found at {} (set LOBBYLINK_SERVER)",
            bin.display()
        );
        let port = {
            let listener = TcpListener::bind("127.0.0.1:0").expect("probe port");
            listener.local_addr().unwrap().port()
        };
        let child = Command::new(bin)
            .arg("--listen-http")
            .arg(format!("127.0.0.1:{port}"))
            .arg("--allow-no-origin")
            .arg("--allowed-origin")
            .arg(format!("http://127.0.0.1:{port}"))
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("start p2p-lobby-server");
        for _ in 0..100 {
            if tokio::net::TcpStream::connect(("127.0.0.1", port)).await.is_ok() {
                return Server { child, port };
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
        panic!("p2p-lobby-server did not come up on port {port}");
    }

    fn url(&self) -> String {
        format!("http://127.0.0.1:{}", self.port)
    }
}

impl Drop for Server {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// Scan the event stream (discarding others) until `f` matches.
async fn expect_event<T>(
    game: &mut P2PGame,
    what: &str,
    mut f: impl FnMut(&Event) -> Option<T>,
) -> T {
    match tokio::time::timeout(Duration::from_secs(25), async {
        loop {
            let event = game
                .next_event()
                .await
                .unwrap_or_else(|| panic!("event stream ended waiting for {what}"));
            if let Some(out) = f(&event) {
                return out;
            }
        }
    })
    .await
    {
        Ok(out) => out,
        Err(_) => panic!("timeout waiting for {what}"),
    }
}

async fn wait_connected(game: &mut P2PGame, peer: u16) {
    expect_event(game, &format!("peer {peer} connected"), |ev| match ev {
        Event::PeerState { player_id, state } if *player_id == peer && state == "connected" => {
            Some(())
        }
        Event::PeerState { player_id, state } if *player_id == peer && state == "failed" => {
            panic!("peer {peer} failed instead of connecting")
        }
        _ => None,
    })
    .await
}

async fn expect_reliable(game: &mut P2PGame, from: u16, what: &str) -> Bytes {
    expect_event(game, what, |ev| match ev {
        Event::Message { from: f, kind: MessageKind::Reliable, data } if *f == from => {
            Some(data.clone())
        }
        _ => None,
    })
    .await
}

fn pattern(len: usize, seed: u8) -> Bytes {
    Bytes::from((0..len).map(|i| (i as u8).wrapping_mul(31).wrapping_add(seed)).collect::<Vec<u8>>())
}

/// Send a best-effort datagram from `tx` (player `from`) until `rx`
/// sees it; the channel is lossy by contract, hence the retry loop.
async fn best_effort_roundtrip(tx: &P2PGame, rx: &mut P2PGame, from: u16, note: &'static [u8]) {
    for _ in 0..50 {
        tx.send_best_effort(1 - from, Bytes::from_static(note)).await.unwrap();
        let got = tokio::time::timeout(Duration::from_millis(300), async {
            loop {
                match rx.next_event().await {
                    Some(Event::Message { from: f, kind: MessageKind::BestEffort, data })
                        if f == from =>
                    {
                        return data;
                    }
                    Some(_) => {}
                    None => panic!("event stream ended"),
                }
            }
        })
        .await;
        if let Ok(data) = got {
            assert_eq!(data, note);
            return;
        }
    }
    panic!("best-effort from player {from} never arrived");
}

#[tokio::test]
async fn full_two_player_flow() {
    let server = Server::start().await;

    let mut create = CreateOptions::new(2);
    create.wait_until_full = true;
    let mut a = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "RUSTTEST".into(),
        create: Some(create),
        ..Default::default()
    })
    .await
    .expect("A connects");
    assert_eq!(a.self_id(), 0);
    assert_eq!(a.max_players(), 2);
    assert!(!a.started());
    assert!(!a.resume_token().is_empty());

    let mut b = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "RUSTTEST".into(),
        // Exercise the omit-Origin path (server runs --allow-no-origin).
        origin: Some(String::new()),
        ..Default::default()
    })
    .await
    .expect("B connects");
    assert_eq!(b.self_id(), 1);

    expect_event(&mut a, "player-joined 1", |ev| {
        matches!(ev, Event::PlayerJoined { player_id: 1 }).then_some(())
    })
    .await;
    expect_event(&mut a, "started", |ev| matches!(ev, Event::Started).then_some(())).await;

    wait_connected(&mut a, 1).await;
    wait_connected(&mut b, 0).await;

    // Selected candidate pair is reported once connected.
    let (local, remote) = expect_event(&mut a, "candidate-pair", |ev| match ev {
        Event::CandidatePair { player_id: 1, local, remote } => {
            Some((local.clone(), remote.clone()))
        }
        _ => None,
    })
    .await;
    assert!(!local.is_empty() && !remote.is_empty());

    // Small reliable message.
    a.send_reliable(1, Bytes::from_static(b"hello from A")).await.unwrap();
    assert_eq!(expect_reliable(&mut b, 0, "small reliable").await, "hello from A");

    // 0-byte reliable message.
    b.send_reliable(0, Bytes::new()).await.unwrap();
    assert!(expect_reliable(&mut a, 1, "empty reliable").await.is_empty());

    // 300 KB (multi-chunk).
    let big = pattern(300 * 1024, 7);
    a.send_reliable(1, big.clone()).await.unwrap();
    assert_eq!(expect_reliable(&mut b, 0, "300KB reliable").await, big);

    // 8 MiB (backpressure path).
    let huge = pattern(8 * 1024 * 1024, 13);
    a.send_reliable(1, huge.clone()).await.unwrap();
    assert_eq!(expect_reliable(&mut b, 0, "8MiB reliable").await, huge);

    // Two concurrent sends interleave msgIds but both arrive intact.
    let m1 = pattern(300 * 1024, 21);
    let m2 = pattern(300 * 1024, 42);
    let (r1, r2) = tokio::join!(a.send_reliable(1, m1.clone()), a.send_reliable(1, m2.clone()));
    r1.unwrap();
    r2.unwrap();
    let got1 = expect_reliable(&mut b, 0, "concurrent 1").await;
    let got2 = expect_reliable(&mut b, 0, "concurrent 2").await;
    assert!(
        (got1 == m1 && got2 == m2) || (got1 == m2 && got2 == m1),
        "concurrent sends corrupted"
    );

    // Sequential sends keep their order.
    a.send_reliable(1, Bytes::from_static(b"first")).await.unwrap();
    a.send_reliable(1, Bytes::from_static(b"second")).await.unwrap();
    assert_eq!(expect_reliable(&mut b, 0, "ordered 1").await, "first");
    assert_eq!(expect_reliable(&mut b, 0, "ordered 2").await, "second");

    // Best-effort both ways (lossy contract: retry until seen).
    best_effort_roundtrip(&a, &mut b, 0, b"ping").await;
    best_effort_roundtrip(&b, &mut a, 1, b"pong").await;

    // Room is full: a third join is rejected.
    let full = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "RUSTTEST".into(),
        ..Default::default()
    })
    .await;
    assert_eq!(full.expect_err("room should be full").code, "room-full");

    // Explicit leave frees the slot and reports explicit-leave.
    b.close().await.unwrap();
    expect_event(&mut a, "player-left explicit", |ev| match ev {
        Event::PlayerLeft { player_id: 1, reason: PlayerLeftReason::ExplicitLeave } => Some(()),
        _ => None,
    })
    .await;

    a.close().await.unwrap();
}

#[tokio::test]
async fn token_resume_supersedes_old_session() {
    let server = Server::start().await;
    let token_file = std::env::temp_dir().join(format!(
        "lobbylink-test-token-{}-{}",
        std::process::id(),
        server.port
    ));
    let _ = std::fs::remove_file(&token_file);

    let mut a = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "RESUMEROOM".into(),
        create: Some(CreateOptions::new(2)),
        ..Default::default()
    })
    .await
    .expect("A connects");

    let mut b1 = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "RESUMEROOM".into(),
        storage_path: Some(token_file.clone()),
        ..Default::default()
    })
    .await
    .expect("B1 connects");
    assert_eq!(b1.self_id(), 1);
    assert_eq!(
        std::fs::read_to_string(&token_file).expect("token persisted"),
        b1.resume_token()
    );
    expect_event(&mut a, "player-joined 1", |ev| {
        matches!(ev, Event::PlayerJoined { player_id: 1 }).then_some(())
    })
    .await;
    wait_connected(&mut a, 1).await;
    wait_connected(&mut b1, 0).await;

    // Same storage: B2 resumes B1's slot; B1 is superseded.
    let mut b2 = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "RESUMEROOM".into(),
        storage_path: Some(token_file.clone()),
        ..Default::default()
    })
    .await
    .expect("B2 resumes");
    assert_eq!(b2.self_id(), 1, "token resume must reclaim the same slot");

    expect_event(&mut b1, "session-superseded", |ev| match ev {
        Event::SignalingClosed { code, .. } if code == "session-superseded" => Some(()),
        _ => None,
    })
    .await;
    // The superseded session must NOT clear the new session's token.
    assert_eq!(
        std::fs::read_to_string(&token_file).expect("token still present"),
        b2.resume_token()
    );

    // A sees the slot rejoin and rebuilds the peer; data still flows.
    expect_event(&mut a, "player-rejoined 1", |ev| {
        matches!(ev, Event::PlayerRejoined { player_id: 1, .. }).then_some(())
    })
    .await;
    wait_connected(&mut a, 1).await;
    wait_connected(&mut b2, 0).await;
    a.send_reliable(1, Bytes::from_static(b"after resume")).await.unwrap();
    assert_eq!(
        expect_reliable(&mut b2, 0, "post-resume message").await,
        "after resume"
    );

    b2.close().await.unwrap();
    assert!(!token_file.exists(), "close() must clear the stored token");
    a.close().await.unwrap();
    drop(b1);
}

#[tokio::test]
async fn caller_error_checks() {
    let server = Server::start().await;

    let err = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "ab".into(),
        ..Default::default()
    })
    .await
    .expect_err("short code");
    assert_eq!(err.code, "invalid-code");

    let game = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "ERRROOM".into(),
        create: Some(CreateOptions::new(3)),
        ..Default::default()
    })
    .await
    .expect("connect");

    let err = game.send_best_effort(0, Bytes::from_static(b"self")).await.unwrap_err();
    assert_eq!(err.code, "invalid-target");
    let err = game.send_best_effort(3, Bytes::from_static(b"range")).await.unwrap_err();
    assert_eq!(err.code, "invalid-target");
    let err = game
        .send_best_effort(1, Bytes::from(vec![0u8; 16_001]))
        .await
        .unwrap_err();
    assert_eq!(err.code, "message-too-large");
    let err = game.send_reliable(1, Bytes::from_static(b"x")).await.unwrap_err();
    assert_eq!(err.code, "target-unavailable", "empty slot must be rejected");
    let err = game
        .send_reliable(3, Bytes::from_static(b"x"))
        .await
        .unwrap_err();
    assert_eq!(err.code, "invalid-target");

    // Unknown room without create.
    let err = P2PGame::connect(ConnectOptions {
        server: server.url(),
        code: "NOSUCHROOM".into(),
        ..Default::default()
    })
    .await
    .expect_err("nonexistent room");
    assert!(!err.code.is_empty());

    game.close().await.unwrap();
}
