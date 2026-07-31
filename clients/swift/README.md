# lobbylink-client (Swift)

A Swift client for the lobbylink peer-to-peer lobby system: lobby
membership over a WebSocket signaling server plus direct WebRTC
DataChannels (via [libdatachannel](https://github.com/paullouisageneau/libdatachannel))
between every pair of players. Wire-compatible with the TypeScript
(browser), Rust, Go, and Java clients — a Swift game and a browser game
can share one room.

Signaling uses Foundation's `URLSessionWebSocketTask`, so the only
non-system dependency is libdatachannel's C API. Linux is the primary
target; the code stays portable to macOS (conditional
`FoundationNetworking` imports).

## Prerequisites

libdatachannel (v0.24 or newer) installed under `/usr/local`:

```
/usr/local/include/rtc/rtc.h
/usr/local/lib/libdatachannel.so
```

The package hard-codes those paths: the `CDataChannel` system-library
module maps the header absolutely, and the `LobbyLink` target links with
`-L/usr/local/lib` via `linkerSettings` `unsafeFlags`. Because of
`unsafeFlags`, SwiftPM only accepts this package as a **local or
branch-based dependency**, not a versioned one — vendoring the
`clients/swift` folder (or a git submodule) is the intended consumption.

Toolchain note: the SwiftPM tools (not the binaries they produce) link
`libxml2.so.2`; on distros that ship only `libxml2.so.16`, point
`LD_LIBRARY_PATH` at a directory containing a
`libxml2.so.2 -> libxml2.so.16` symlink while building.

## Build

```bash
swift build            # library + the lobbylink-chat example
swift run lobbylink-chat https://pqrstuvw.xyz/lobbylink MYROOM
```

## Use

```swift
import LobbyLink

var options = ConnectOptions(server: "https://pqrstuvw.xyz/lobbylink", code: "MYROOM")
options.create = CreateOptions(maxPlayers: 4) // omit to only join

let game = try P2PGame.connect(options) // throws LobbyError with a stable code
defer { game.close() }

while let ev = game.nextEvent() {       // nil once closed
    switch ev {
    case .peerState(let id, "connected"):
        try game.sendReliable(to: id, data: Data("hello".utf8))
    case .message(let from, let kind, let data):
        handle(from, kind, data)
    default:
        break
    }
}
```

- `sendBestEffort(to:data:)` / `broadcastBestEffort(_:)`: unordered, may
  drop, ≤ 16000 bytes (stay under ~1200 to avoid SCTP fragmentation);
  never blocks.
- `sendReliable(to:data:)`: ordered and chunked, ≤ 16 MiB; blocks until
  handed to the transport (backpressure included).
- `nextEvent()` blocks; `nextEvent(timeout:)` returns nil on timeout
  too. A `while let` loop on its own thread is the intended shape (see
  the chat example).
- Accessors: `selfId`, `code`, `maxPlayers`, `started`, `resumeToken`,
  `players`, `iceServers`.
- `close()` leaves the room (freeing the slot), tears down peers, and
  clears the stored token; after it, `nextEvent()` returns nil.

Errors are `LobbyError` with a stable `code`: server codes
(`room-full`, `room-not-found`, …) pass through; client codes include
`connect-timeout`, `connection-failed`, `invalid-target`,
`message-too-large`, `channel-timeout`, `send-failed`, `closed`.

### Events

`Event` is an enum; switch on the cases you care about:
`.message(from:kind:data:)`, `.playerJoined(id:)`,
`.playerLeft(id:reason:)`, `.playerRejoined(id:wasReplacement:)`,
`.playerReplaced(id:)`, `.started`, `.peerState(id:state:)`,
`.lobbyError(code:message:)`, `.signalingClosed(code:message:)`.

`state` in `.peerState` uses the browser's lowercase strings (`"new"`,
`"connecting"`, `"connected"`, `"disconnected"`, `"failed"`,
`"closed"`) — send to a peer once it is `"connected"`.

### Reconnecting

Pass `tokenFile` in `ConnectOptions` (the native analog of the browser
`storageKey`) and the hidden resume token persists across restarts, so
a rejoin keeps your player ID. Use a **per-process** path — two clients
sharing one token file steal each other's slot. Or capture
`game.resumeToken` yourself and pass it back as `resumeToken`;
`claimPlayerId` claims a silent slot once the token is gone and the
room's `claimAfter` has passed.

Native clients send an `Origin` header derived from the server URL
(e.g. `https://host:port`), which production deployments accept without
extra config. `ConnectOptions.origin` overrides it; set it to `""` to
send none (servers running `--allow-no-origin`).

`ConnectOptions.forceRelay` maps to libdatachannel's
`iceTransportPolicy = relay` for TURN testing; TURN credentials from the
server are embedded into the ICE URIs percent-encoded
(`turn:user:pass@host:port?transport=udp`), as the C API expects.

## Wire contract

Identical to the other clients (see
[clients/ts/README.md](../ts/README.md) for the full spec): two
pre-negotiated DataChannels per peer pair (`"reliable"` id 1 ordered,
`"best-effort"` id 2 unordered `maxRetransmits: 0`), the lower player
ID of each pair makes the SDP offer, and reliable payloads travel as
16 KiB chunks under an 18-byte big-endian header (magic `0x4C`,
version 1). libdatachannel auto-negotiates: creating the first channel
triggers the offer, applying a remote offer triggers the answer.

## Test

```bash
swift test                              # unit + loopback integration tests
swift test --filter FramingTests        # unit tests only
```

The integration tests build `cmd/p2p-lobby-server` from this repository
(expecting `go` at `/usr/local/go/bin/go`) and run real WebSocket +
WebRTC sessions against it on loopback; they skip themselves outside
the repo checkout or when the server cannot start.

## Example

`Sources/lobbylink-chat` is a complete multiplayer chat in ~60 lines,
wire-compatible with the other clients' chat examples — run one Swift
chat and one Java chat with the same room code and type at each other.

## Layout

```
Sources/CDataChannel/   system-library module for libdatachannel's C API
Sources/LobbyLink/      the client: P2PGame, options/events, Wire (JSON +
                        URL normalization), Framing (chunking/reassembly),
                        Signaling (WebSocket), PeerLink (libdatachannel)
Sources/lobbylink-chat/ the chat example
Tests/LobbyLinkTests/   framing/URL unit tests + loopback integration tests
```
