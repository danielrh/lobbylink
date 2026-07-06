# DartRoids — Rust in the browser (wasm)

The same game as `../asteroids-web` and `../asteroids-native`, but this one is
**Rust compiled to WebAssembly**: it renders with the browser Canvas 2D API
(`web-sys`) and drives lobbylink's **wasm** client on the browser event loop —
the "embed lobbylink in a wasm-bindgen app" scenario the client was built for.
No macroquad, no threads, no tokio. Wire-compatible with the TypeScript and
native examples (see [`../PROTOCOL.md`](../PROTOCOL.md)); the protocol/sim code
is the *exact same* `../asteroids-native/src/protocol.rs`, included via `#[path]`.

## Run it

You need [`wasm-pack`](https://rustwasm.github.io/wasm-pack/) and the wasm
target:

```bash
rustup target add wasm32-unknown-unknown
cargo install wasm-pack        # if you don't have it

cd examples/asteroids-wasm
make serve                     # wasm-pack build -> serve/pkg, then serves on :5173
# open http://localhost:5173/
```

`make serve` just runs `wasm-pack build --target web --out-dir serve/pkg --dev`
and then `python3 -m http.server 5173`. That's the "simple local webserver" —
any static file server works, but **use port 5173**: the game defaults to the
public test server `https://pqrstuvw.xyz/lobbylink`, and prod allowlists
`http://localhost:5173` as a WebSocket origin. On another port the P2P handshake
to prod is rejected (run your own lobbylink server with `--allowed-origin` to
use a different port, or fill in your own server in the menu).

`make` (without `serve`) just builds; then point any static server at the
`serve/` directory.

## Controls

Same as the web version: arrows/WASD + Space on desktop; drag-to-steer + the
**FIRE** button on touch. Share the room code (it's in the URL hash) and fly
with the TS and native clients in the same room.

## How it works

- `#[wasm_bindgen(start)]` wires the HTML menu/lobby and input listeners.
- **Networking** is one `spawn_local` task (`run_net`) that owns the wasm
  `P2PGame` and `futures::select!`s between `next_event()` (inbound) and an
  unbounded channel of outbound packets fed by the render loop. Everything is
  single-threaded, so the client's `!Send` internals are fine.
- The **render loop** is a plain `requestAnimationFrame` recursion; it and the
  input handlers share game state through `Rc<RefCell<Game>>`, only ever
  borrowing it synchronously (never across an `.await`).

## Verified

Built with `wasm-pack`; loaded headless and joined the **prod** server from
`localhost:5173`, auto-launched, and a TypeScript peer in the same room decoded
the wasm client's `STATE` broadcast over a real WebRTC DataChannel — confirming
the wasm client interoperates with the other implementations on the wire.
