# DartRoids — web client

Browser build of the DartRoids lobbylink demo. Runs on desktop and on **Safari
mobile**. Wire-compatible with the Rust desktop client (`../asteroids-native`);
see [`../PROTOCOL.md`](../PROTOCOL.md).

## Build & run

```bash
make            # tsc (from the lobbylink TS client) -> dist/*.js
                # + copies the lobbylink browser bundle to dist/p2p-client.js
make serve      # python3 -m http.server 5173  ->  http://localhost:5173/
make check      # type-check only
```

The only tool is the TypeScript compiler that already ships with
`../../clients/ts` — nothing new to install. Then open `index.html` from any
static server.

## Controls

- **Desktop:** ◄ ► arrows (or A/D) turn, ▲ (or W) thrusts, **Space** fires.
- **Touch / mobile:** drag anywhere on the field — your dart steers and thrusts
  toward your finger — and tap the **FIRE** button. (Mouse-drag works too.)

## Playing together

The room code is in the URL hash, e.g. `…/index.html#DARTS`. Enter a call sign
and code on the menu, hit **Enter lobby**, then **Launch**. Share the link (the
lobby screen has a *Copy invite link* button) — anyone who opens it and launches
joins the same field, including the Rust client on the same code.

## Server

Leave the *Server* field blank to auto-detect: it first tries `./config.json`
(present when the page is served by the lobbylink Go server) and otherwise falls
back to the public test server `https://pqrstuvw.xyz/lobbylink`. You can also
type any `https://host[:port][/path]` or `wss://…/ws` lobbylink endpoint.

## How it maps to lobbylink

- `P2PGame.connect({ server, code, create: { maxPlayers, waitUntilFull:false },
  storage: "session" })` — `storage:"session"` so two tabs are two pilots, not
  slot-stealing resumes.
- `game.broadcastBestEffort(bytes)` at 16 Hz for state (`src/protocol.ts`
  `encodeState`) and for the leader clock.
- `game.sendReliable(shooter, bytes)` once when you die to a foreign bullet.
- `game.onEvent` drives the roster (for clock-leader election) and inbound
  `message` decoding.

All game/protocol logic is `src/protocol.ts` (shared format) + `src/game.ts`
(sim, input, render). `src/p2p-client.d.ts` is a type-only shim so tsc resolves
the client's types while the emitted `import` targets the copied browser bundle.
