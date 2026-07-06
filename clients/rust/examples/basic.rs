//! Join (or create) a room, greet peers over the reliable channel and
//! print every event until Ctrl-C.
//!
//! Usage:
//!   cargo run --example basic -- <server> <code> [--create N] [--relay]
//!   cargo run --example basic -- http://127.0.0.1:8789 TESTROOM --create 2

use bytes::Bytes;
use p2p_lobby_client::{ConnectOptions, CreateOptions, Event, MessageKind, P2PGame};

fn usage() -> ! {
    eprintln!("usage: basic <server> <code> [--create N] [--relay]");
    std::process::exit(2)
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut args = std::env::args().skip(1);
    let server = args.next().unwrap_or_else(|| usage());
    let code = args.next().unwrap_or_else(|| usage());
    let mut create = None;
    let mut force_relay = false;
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--create" => {
                let n = args.next().unwrap_or_else(|| usage());
                create = Some(CreateOptions::new(n.parse().expect("player count")));
            }
            "--relay" => force_relay = true,
            _ => usage(),
        }
    }

    let mut game = P2PGame::connect(ConnectOptions {
        server,
        code,
        create,
        force_relay,
        ..Default::default()
    })
    .await?;
    println!(
        "joined room {} as player {} ({} slots, started: {})",
        game.code(),
        game.self_id(),
        game.max_players(),
        game.started()
    );

    loop {
        tokio::select! {
            event = game.next_event() => {
                let Some(event) = event else { break };
                match event {
                    Event::PeerState { player_id, state } => {
                        println!("peer {player_id}: {state}");
                        if state == "connected" {
                            let hello = format!("hello from player {}", game.self_id());
                            game.send_reliable(player_id, Bytes::from(hello)).await?;
                        }
                    }
                    Event::Message { from, kind, data } => {
                        let shown = String::from_utf8_lossy(&data);
                        let kind = match kind {
                            MessageKind::Reliable => "reliable",
                            MessageKind::BestEffort => "best-effort",
                        };
                        println!("[{kind}] player {from}: {shown}");
                    }
                    other => println!("{other:?}"),
                }
            }
            _ = tokio::signal::ctrl_c() => {
                println!("leaving...");
                break;
            }
        }
    }
    game.close().await?;
    Ok(())
}
