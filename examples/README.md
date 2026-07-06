# DartRoids — a lobbylink example game

A tiny, trust-based, server-less multiplayer **asteroids** clone that shows off
[lobbylink](../README.md): players join a lobby by link, then fly quadrilateral
"dart" ships in a shared field, wrapping at the edges and shooting each other.
There is no game server — every client simulates itself and broadcasts its
position + bullets over lobbylink's peer-to-peer DataChannels 16×/second.

Two **independent, wire-compatible** codebases:

| dir | language | runtime | how to play |
|-----|----------|---------|-------------|
| [`asteroids-web/`](asteroids-web) | TypeScript | any browser incl. **Safari mobile** | arrows + space, or drag-to-steer + FIRE button |
| [`asteroids-native/`](asteroids-native) | Rust | standalone desktop (macroquad) | arrows/WASD + space, or mouse-drag + FIRE |

A browser player and a Rust desktop player in the **same room code** see and
shoot each other. The exact bytes on the wire — plus the deterministic asteroid
field and shared clock — are specified once in **[PROTOCOL.md](PROTOCOL.md)** and
mirrored by both.

## What it demonstrates about lobbylink

- **Lobby by link.** The room code lives in the URL (web) or `--code` (native);
  share it and up to `maxPlayers` (default **64**) pilots join the same room.
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
