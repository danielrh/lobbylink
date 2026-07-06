//! Echo server player: joins a room and sends every received message
//! straight back to its sender on the same channel kind.
//!
//! Usage:
//!   cargo run --example echo -- <server> <code> [--create N]

use p2p_lobby_client::{ConnectOptions, CreateOptions, Event, MessageKind, P2PGame};

fn usage() -> ! {
    eprintln!("usage: echo <server> <code> [--create N]");
    std::process::exit(2)
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut args = std::env::args().skip(1);
    let server = args.next().unwrap_or_else(|| usage());
    let code = args.next().unwrap_or_else(|| usage());
    let mut create = None;
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--create" => {
                let n = args.next().unwrap_or_else(|| usage());
                create = Some(CreateOptions::new(n.parse().expect("player count")));
            }
            _ => usage(),
        }
    }

    let mut game = P2PGame::connect(ConnectOptions {
        server,
        code,
        create,
        ..Default::default()
    })
    .await?;
    println!("echoing in room {} as player {}", game.code(), game.self_id());

    loop {
        tokio::select! {
            event = game.next_event() => {
                let Some(event) = event else { break };
                match event {
                    Event::Message { from, kind: MessageKind::Reliable, data } => {
                        println!("echoing {} reliable bytes to player {from}", data.len());
                        if let Err(e) = game.send_reliable(from, data).await {
                            eprintln!("echo failed: {e}");
                        }
                    }
                    Event::Message { from, kind: MessageKind::BestEffort, data } => {
                        let _ = game.send_best_effort(from, data).await;
                    }
                    other => println!("{other:?}"),
                }
            }
            _ = tokio::signal::ctrl_c() => break,
        }
    }
    game.close().await?;
    Ok(())
}
