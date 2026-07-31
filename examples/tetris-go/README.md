# tetris-go

Console tetris built on the [Go lobbylink client](../../clients/go) —
the terminal is the whole UI (plain ANSI, no curses). Single player out
of the box; point it at a lobbylink server and a room code and it
becomes a battle: **every line you clear bumps that many gray garbage
rows into every opponent's board**, and theirs bump yours. Last stack
standing wins bragging rights.

```bash
go build -o tetris .

./tetris                                     # single player
./tetris https://pqrstuvw.xyz/lobbylink RUMBLE   # multiplayer
./tetris http://127.0.0.1:8787 RUMBLE            # local dev server
```

The first player in creates the room (`--players N`, default 4);
everyone else just runs the same command. Opponents' boards render
live at half height beside yours, with names (`--name`, default your
OS user) and scores. A wide terminal (~100 columns) fits the full
multiplayer view.

Keys: `←/→` (or `a`/`d`) move, `↑`/`w` rotate, `↓`/`s` soft drop,
`space` hard drop, `p` pause, `r` restart after topping out, `q` quit.

## How the multiplayer works

Two message types ride on lobbylink DataChannels (layout in
[protocol.go](protocol.go)):

- **STATE** (best-effort, ~5 Hz): one packet fully describes a player
  — alive flag, score/lines/level, the whole 10x20 board with the
  falling piece baked in, and the display name. Late joiners need no
  history, and a lost packet just means the next one catches up.
- **ATTACK** (reliable): "I cleared N lines" — the bump. Reliable
  because a dropped attack would be unfair in the other direction.

The receiver of an ATTACK pushes N gray rows (sharing one random gap
column) into the bottom of its own board. There is no referee: like
the other lobbylink examples this is trust-based peer-to-peer — each
client is authoritative for its own board.

Garbage that pushes your stack over the top ends your game; press `r`
to rejoin the fray with a fresh board.

## Test

```bash
go test ./...      # game logic + an end-to-end test that runs the real
                   # binary against a locally built lobby server
go test -short ./... # logic tests only
```
