# DartRoids — native (desktop) client

Standalone desktop build of the DartRoids lobbylink demo, rendered with
[macroquad](https://macroquad.rs). Wire-compatible with the browser client
(`../asteroids-web`); see [`../PROTOCOL.md`](../PROTOCOL.md). A Rust player and a
browser player in the same room code share the field.

## Run

```bash
cargo run -- --code DARTS --name rustpilot
# options (all optional):
#   --server URL   lobbylink endpoint (default https://pqrstuvw.xyz/lobbylink)
#   --code   CODE  room code (default DARTS)
#   --name   NAME  call sign (default rustpilot)
#   --max    N     maxPlayers when creating the room (default 32; the test
#                  server caps new rooms at max_players_hard = 32)
```

On Linux the macroquad window needs the usual GL/X11 (or Wayland) runtime
libraries; on macOS/Windows it works out of the box.

## Controls

- ◄ ► arrows or **A/D** turn, ▲ or **W** thrusts, **Space** fires.
- Mouse-drag on the field steers/thrusts toward the cursor; the on-screen
  **FIRE** circle (bottom-right) fires. On a touchscreen the same gestures work.
- On the **LOBBY** screen press **Enter** to launch into the arena.

## Architecture

macroquad owns the main thread (`#[macroquad::main]`): input, simulation,
rendering. The lobbylink client (`p2p-lobby-client`, native tokio + `webrtc`
backend) runs on its own thread with a tokio runtime, bridged by channels:

- game → net: an unbounded `tokio::sync::mpsc` of `Broadcast` / `Reliable` sends.
- net → game: a `std::sync::mpsc` of `Ready` / `Msg` / `Roster` / notices,
  drained each frame with `try_recv`.

The lobbylink object never crosses threads, so its `!Send` internals are fine.
`src/protocol.rs` is a byte-for-byte port of the browser's `protocol.ts`
(constants, mulberry32 RNG, deterministic asteroid field, message codecs), which
is what keeps the two clients interoperable.
