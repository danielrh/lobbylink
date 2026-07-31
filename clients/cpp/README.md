# lobbylink-client (C++)

A C++17 client for the lobbylink peer-to-peer lobby system: lobby
membership over a WebSocket signaling server plus direct WebRTC
DataChannels (via
[libdatachannel](https://github.com/paullouisageneau/libdatachannel))
between every pair of players. Wire-compatible with the TypeScript
(browser), Rust, Go, and Java clients — a C++ game and a browser game
can share one room.

Single dependency: libdatachannel provides both the signaling WebSocket
and the WebRTC stack. JSON handling is a vendored copy of the
single-header [nlohmann/json](https://github.com/nlohmann/json)
(`third_party/nlohmann/json.hpp`, MIT license) that stays private to the
implementation — nothing of it leaks into the public header.

## Build

Install libdatachannel where CMake can find it (v0.24 or newer), then:

```bash
cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build
```

That produces the `lobbylink` static library, the `chat` example, and
the `interop` test binary. To use the client from your own CMake
project, `add_subdirectory(clients/cpp)` and link `lobbylink`; the
public header is `include/lobbylink/lobbylink.hpp`.

## Use

```cpp
#include <lobbylink/lobbylink.hpp>

lobbylink::ConnectOptions opts("https://pqrstuvw.xyz/lobbylink", "MYROOM");
opts.create = lobbylink::CreateOptions(4); // omit to only join

auto game = lobbylink::P2PGame::connect(opts); // throws LobbyError

for (;;) {
    lobbylink::Event ev = game->nextEvent();
    if (ev.type == lobbylink::Event::Type::PeerState && ev.state == "connected") {
        std::string hello = "hello";
        game->sendReliable(ev.playerId,
                           reinterpret_cast<const std::uint8_t *>(hello.data()),
                           hello.size());
    } else if (ev.type == lobbylink::Event::Type::Message) {
        handle(ev.playerId, ev.kind, ev.data);
    } else if (ev.type == lobbylink::Event::Type::Closed) {
        break;
    }
}
game->close();
```

### API

`P2PGame::connect(ConnectOptions)` blocks until the lobby returns
`joined` or an error (thrown as `LobbyError` with a stable `code()`).

| method | meaning |
|---|---|
| `nextEvent()` | block for the next `Event`; `Type::Closed` after `close()` |
| `nextEvent(timeout)` | as above but returns `std::nullopt` on timeout |
| `sendReliable(to, bytes)` | ordered, chunked, ≤ 16 MiB; **blocks** until handed to the transport; throws on failure |
| `sendBestEffort(to, bytes)` | unordered, may drop, ≤ 16000 bytes; never blocks |
| `broadcastBestEffort(bytes)` | best-effort to every other occupied slot |
| `players()` | snapshot of all slots (`std::vector<PlayerInfo>`) |
| `selfId()`, `maxPlayers()`, `code()`, `started()`, `resumeToken()`, `iceServers()` | accessors |
| `close()` | leave the room, tear down peers, clear the stored token |

The destructor calls `close()`, so a `unique_ptr<P2PGame>` leaves the
room cleanly. `nextEvent()` is a blocking call — a simple loop over it
is the intended shape; run it on its own thread if your game has
another main loop (see `examples/chat.cpp`). Sends are safe from any
thread.

### Events

`Event` is a tagged struct; switch on `ev.type`:

`Message` (playerId, kind, data), `PlayerJoined` (playerId),
`PlayerLeft` (playerId, reason), `PlayerRejoined` (playerId,
wasReplacement), `PlayerReplaced` (playerId), `Started`, `PeerState`
(playerId, state), `LobbyError` (code, message), `SignalingClosed`
(code, message), `Closed`.

`state` in `PeerState` uses the browser's lowercase strings (`"new"`,
`"connecting"`, `"connected"`, `"disconnected"`, `"failed"`,
`"closed"`) — send to a peer once it is `"connected"`. `PlayerLeft`
reason `"explicit-leave"` frees the slot; `"disconnected"` only lost
signaling, so an established DataChannel to that player may still be
alive. `SignalingClosed` means the WebSocket is gone; DataChannels
survive unless the code says the game is over (`replaced`,
`session-superseded`, `room-expired`).

### Reconnecting

Set `ConnectOptions::tokenFile` (the native analog of the browser
`storageKey`) and the hidden resume token persists across restarts, so
a rejoin keeps your player ID. Use a **per-process** path — two clients
sharing one token file steal each other's slot. Or capture
`resumeToken()` yourself and pass it back as
`ConnectOptions::resumeToken`; `claimPlayerId` claims a silent slot
once the token is gone and the room's `claimAfter` has passed.

Native clients send an `Origin` header derived from the server URL
(e.g. `https://host:port`), which production deployments accept without
extra config. `ConnectOptions::origin` overrides it; set it to `""` to
send none (servers running `--allow-no-origin`).

## Wire contract

Identical to the other clients (see
[clients/ts/README.md](../ts/README.md) for the full spec): two
pre-negotiated DataChannels per peer pair (`"reliable"` id 1 ordered,
`"best-effort"` id 2 unordered `maxRetransmits: 0`), the lower player
ID of each pair makes the SDP offer, and reliable payloads travel as
16 KiB chunks under an 18-byte big-endian header (magic `0x4C`,
version 1).

## Test

`test/interop.cpp` runs two live clients against a local server:
create+join slot assignment, peer connect, reliable single- and
multi-chunk messages both directions, a best-effort burst, and an
explicit leave freeing the slot.

```bash
go build -o /tmp/lobbylink-server ../../cmd/p2p-lobby-server   # from clients/cpp
/tmp/lobbylink-server --listen-http 127.0.0.1:8787 \
  --allowed-origin http://127.0.0.1:8787 --public-url http://127.0.0.1:8787 &
LOBBYLINK_SERVER=http://127.0.0.1:8787 ctest --test-dir build
```

Without `LOBBYLINK_SERVER` the test skips itself. Wire compatibility is
also verified live against the Go client (pion) in both offer
directions: reliable echo (small and 50 KiB), best-effort, and explicit
leave all round-trip Go↔C++.

## Example

`examples/chat.cpp` is a complete multiplayer chat: run two copies with
the same room code and type at each other. It is wire-compatible with
the other clients' chat examples, so a C++ chat and a Java chat can
share a room.
