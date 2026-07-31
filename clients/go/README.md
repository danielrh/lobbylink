# lobbylink-client (Go)

A Go client for the lobbylink peer-to-peer lobby system: lobby
membership over a WebSocket signaling server plus direct WebRTC
DataChannels (via [pion](https://github.com/pion/webrtc)) between every
pair of players. Wire-compatible with the TypeScript (browser), Rust,
and Java clients — a Go game and a browser game can share one room.

Pure Go, no cgo: `go build` is the whole toolchain, and games
cross-compile the way Go programs always do.

## Use

```bash
go get github.com/danielrh/lobbylink/clients/go
```

```go
import (
    "context"
    lobbylink "github.com/danielrh/lobbylink/clients/go"
)

game, err := lobbylink.Connect(ctx, lobbylink.Options{
    Server: "https://pqrstuvw.xyz/lobbylink", // or https://host:4443, ws://127.0.0.1:8787
    Code:   "MYROOM",
    Create: lobbylink.NewCreateOptions(4), // omit to only join
})
if err != nil { ... }
defer game.Close()

for ev := range game.Events() {
    switch ev := ev.(type) {
    case lobbylink.PeerStateEvent:
        if ev.State == "connected" {
            game.SendReliable(ev.PlayerID, []byte("hello"))
        }
    case lobbylink.MessageEvent:
        handle(ev.From, ev.Kind, ev.Data)
    }
}
```

- `SendBestEffort` / `BroadcastBestEffort`: unordered, may drop,
  ≤ 16000 bytes (stay under ~1200 to avoid SCTP fragmentation).
- `SendReliable`: ordered and chunked, ≤ 16 MiB; blocks until handed
  to the transport.
- `Events()` closes when the game does. Consume it promptly — events
  queue without bound while you don't.

Errors are `*lobbylink.Error` with a stable `Code`: server codes
(`room-full`, `room-not-found`, …) pass through; client codes include
`connect-timeout`, `invalid-target`, `message-too-large`,
`channel-timeout`, `send-failed`, `closed`.

Reconnecting after a crash: pass `Options.TokenFile` (the native analog
of the browser `storageKey`) and the hidden resume token persists
across restarts, so a rejoin keeps your player ID. Or capture
`game.ResumeToken()` yourself and pass it back as
`Options.ResumeToken`; `Options.ClaimPlayerID` claims a silent slot
once the token is gone and the room's `claimAfter` has passed.

Native clients send an `Origin` header derived from the server URL
(e.g. `https://host:port`), which production deployments accept without
extra config. `Options.Origin` overrides it; point it at `""` to send
none (servers running `--allow-no-origin`).

## Wire contract

Identical to the other clients (see
[clients/ts/README.md](../ts/README.md) for the full spec): two
pre-negotiated DataChannels per peer pair (`"reliable"` id 1 ordered,
`"best-effort"` id 2 unordered `maxRetransmits: 0`), the lower player
ID of each pair makes the SDP offer, and reliable payloads travel as
16 KiB chunks under an 18-byte big-endian header (magic `0x4C`,
version 1).

## Test

```bash
go test ./...        # includes loopback integration tests
go test -short ./... # unit tests only
```

The integration tests build `cmd/p2p-lobby-server` from this repository
and run real WebSocket + WebRTC sessions against it on loopback; they
skip themselves outside the repo checkout.

## Example

[`examples/tetris-go`](../../examples/tetris-go) is a complete
multiplayer console game built on this client.
