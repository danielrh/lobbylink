# DartRoids — a lobbylink example game

A tiny, trust-based, server-less multiplayer **asteroids** clone that shows off
[lobbylink](../README.md): players join a lobby by link, then fly quadrilateral
"dart" ships in a shared field, wrapping at the edges and shooting each other.
There is no game server — every client simulates itself and broadcasts its
position + bullets over lobbylink's peer-to-peer DataChannels 16×/second.

Three **independent, wire-compatible** codebases:

| dir | language | runtime | how to play |
|-----|----------|---------|-------------|
| [`asteroids-web/`](asteroids-web) | TypeScript | any browser incl. **Safari mobile** | arrows + space, or drag-to-steer + FIRE button |
| [`asteroids-native/`](asteroids-native) | Rust | standalone desktop (macroquad) | arrows/WASD + space, or mouse-drag + FIRE |
| [`asteroids-wasm/`](asteroids-wasm) | Rust → **WebAssembly** | any browser (web-sys Canvas 2D) | arrows/WASD + space, or drag-to-steer + FIRE |

The two browser builds are different demos of the *same* game: `asteroids-web`
uses the TypeScript client, `asteroids-wasm` compiles the Rust game (and Rust
lobbylink client) to wasm. All three share the same wire protocol.

> **Looking for a Java example?** [`snake-java/`](snake-java) is a separate game —
> a multiplayer worm built on the [Java client](../clients/java) — showing the
> same lobbylink ideas (lobby by link, best-effort broadcasts, a deterministic
> field driven by a shared clock and an automatic clock leader). Build it with
> plain `javac`; no build tool needed.

> **Want an open world with NPCs?** [`tarsus/`](tarsus) is a TypeScript
> spaceship game with drop-in multiplayer: pilots broadcast their ships and
> lasers, the *victim* reports damage on the reliable channel, the lowest-id
> player hosts the NPC pirates (with automatic handoff), and a fixed world
> seed places the same base landmarks on every client with zero messages.

Any of them in the **same room code** see and shoot each other — a TypeScript
browser tab, a Rust/wasm browser tab, and a native Rust desktop window all in
one field. The exact bytes on the wire — plus the deterministic asteroid field
and shared clock — are specified once in **[PROTOCOL.md](PROTOCOL.md)** and
mirrored by all three.

## What it demonstrates about lobbylink

- **Lobby by link.** The room code lives in the URL (web) or `--code` (native);
  share it and up to `maxPlayers` (default **32**, the test server's
  `max_players_hard` cap) pilots join the same room.
  Late joiners drop straight in — every packet is a full snapshot, so there is
  no state to catch up on.
- **Both channel kinds.** Unreliable **best-effort** for 16 Hz position/bullet
  broadcasts (drops are fine, the next one is 62 ms away) and **reliable** for
  the rare "you hit me" report.
- **Trust model.** No authority: each client decides locally when *it* got hit
  and tells the shooter, who counts the kill. Scores are kept client-side.
- **Peer-derived coordination, no server logic.**
  - The asteroid field is a pure function of `(room-code seed, second)`, so
    every client generates the identical rocks with zero messages. Asteroids
    spawn *just outside* the map and never wrap, so a pilot in the interior is
    never clobbered by a fresh spawn.
  - The lowest-id live player is the **clock leader** and broadcasts the shared
    game time so everyone agrees which "second" it is; leadership hands off
    automatically as players come and go.

## Quick start

Both default to the public test server `https://pqrstuvw.xyz/lobbylink`.

**Web:**
```bash
cd asteroids-web
make            # builds with the TS client's tsc; no new deps
make serve      # http://localhost:5173/  (an allowlisted origin on prod)
```
Open the URL on your desktop and phone with the same room code — or just open
two tabs. On mobile, drag on the field to steer/thrust and tap **FIRE**.

**Native (desktop):**
```bash
cd asteroids-native
cargo run -- --code DARTS --name rustpilot
```

Put them in the **same room code** and they share the field.

See each subdirectory's README for details, and [PROTOCOL.md](PROTOCOL.md) for
the wire format.
