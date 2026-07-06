# lobbylink-client (Java)

A Java client for the lobbylink peer-to-peer lobby system: lobby membership
over a WebSocket signaling server, plus direct WebRTC DataChannels between every
player. Shaped after the Rust native client (`clients/rust`) and wire-compatible
with it and the TypeScript browser client — a Java game and a Rust or browser
game can share the same room.

The point of this client is that **a beginner can build a real multiplayer game
with nothing but `javac`**: copy a folder of jars into `lib/`, write one `.java`
file, compile, run.

## Get the jars

You need the `lib/` folder of jars. Build it once with the included Gradle
wrapper (no system Gradle needed):

```bash
./gradlew lib                 # build/lib/ with the native library for THIS machine
./gradlew lib -Pplatforms=all # build/lib/ that runs on every supported OS/arch
```

`build/lib/` then contains three jars:

```
lobbylink-client-0.1.0.jar          # this client
webrtc-java-0.14.0.jar              # the WebRTC Java API
webrtc-java-0.14.0-linux-x86_64.jar # the native WebRTC library for your platform
```

Copy that whole folder into your game project as `lib/`. Prefer separate jars
(over one fat jar) so that a **security update** to any dependency — most
importantly the native WebRTC library — is a drop-in file replacement, no
rebuild. `./gradlew distZip` packages the same folder as a zip to hand to
someone who isn't running Gradle.

Supported platforms (native library): `linux-x86_64`, `linux-aarch64`,
`linux-aarch32`, `macos-x86_64`, `macos-aarch64`, `windows-x86_64`. Requires
JDK 17 or newer.

## Build your game with plain javac

Put your game and the `lib/` folder side by side:

```
mygame/
  Chat.java
  lib/            <- the jars you copied
```

```bash
javac -cp "lib/*" Chat.java
java  -cp "lib/*:." Chat https://pqrstuvw.xyz/lobbylink MYROOM
```

On Windows use `;` instead of `:` in the classpath. The `lib/*` wildcard picks
up every jar in the folder, so your command line never changes when you update a
jar inside it.

`examples/Chat.java` is a complete multiplayer chat in ~50 lines; run two copies
with the same room code and type at each other. `examples/EchoGame.java` and
`examples/InteropDriver.java` are also in this repo.

## Use

```java
import xyz.lobbylink.*;

// Join MYROOM, creating it (up to 2 players) if we're first in.
try (P2PGame game = P2PGame.connect(
        new ConnectOptions("https://pqrstuvw.xyz/lobbylink", "MYROOM")
            .create(new CreateOptions(2)))) {

    System.out.println("I am player " + game.selfId() + " of " + game.maxPlayers());

    Event ev;
    while ((ev = game.nextEvent()) != null) {         // null once closed
        if (ev instanceof Event.PeerState ps && ps.state().equals("connected")) {
            game.sendReliable(ps.playerId(), "hello".getBytes());
        } else if (ev instanceof Event.Message msg) {
            System.out.println("player " + msg.from() + ": " + new String(msg.data()));
        }
    }
}
```

### API

`P2PGame.connect(ConnectOptions)` blocks until the lobby returns `joined` or an
error (thrown as `LobbyException` with a stable `getCode()`).

| method | meaning |
|---|---|
| `nextEvent()` | block for the next `Event`; returns `null` after `close()` |
| `nextEvent(timeoutMillis)` | as above but returns `null` on timeout too |
| `sendReliable(to, bytes)` | ordered, chunked, ≤ 16 MiB; **blocks** until handed to the transport; throws on failure |
| `sendBestEffort(to, bytes)` | unordered, may drop, ≤ 16000 bytes; never blocks |
| `broadcastBestEffort(bytes)` | best-effort to every other occupied slot |
| `players()` | snapshot of all slots (`List<PlayerInfo>`) |
| `selfId()`, `maxPlayers()`, `code()`, `started()`, `resumeToken()`, `iceServers()` | accessors |
| `close()` | leave the room, tear down peers, clear the stored token |

`P2PGame` is `AutoCloseable`, so try-with-resources leaves the room cleanly.
`nextEvent()` is a blocking call — a simple `while` loop over it is the intended
shape; run it on its own thread if your game has another main loop (see
`examples/Chat.java`).

### Events

`Event` is a sealed interface; switch on it with `instanceof`:

`Message(from, kind, data)`, `PlayerJoined(playerId)`,
`PlayerLeft(playerId, reason)`, `PlayerRejoined(playerId, wasReplacement)`,
`PlayerReplaced(playerId)`, `Started()`, `PeerState(playerId, state)`,
`CandidatePair(playerId, local, remote)`, `LobbyError(code, message)`,
`SignalingClosed(code, message)`.

`state` in `PeerState` uses the browser's lowercase strings (`"new"`,
`"connecting"`, `"connected"`, `"disconnected"`, `"failed"`, `"closed"`) — send
to a peer once it is `"connected"`.

### Reconnecting

Pass a `storagePath(Path)` in `ConnectOptions` and the hidden resume token is
persisted there; reconnecting with the same path keeps your player id. Use a
**per-process** path — two clients sharing one token file steal each other's
slot. If the token is gone and the slot has been silent past the room's
`claimAfter`, reconnect with `claimPlayerId(id)` instead.

## Develop

```bash
./gradlew test      # unit tests (framing, reassembly, url, json, roster)
./gradlew build     # compile, test, and assemble build/lib/
```

Layout mirrors the Rust client:

```
src/main/java/xyz/lobbylink/          public API (P2PGame, Event, options, ...)
src/main/java/xyz/lobbylink/internal/ Json, Framing, Reassembler, Roster,
                                      SignalingUrl, Protocol, Signaling (WebSocket),
                                      PeerLink, Actor (the single-owner state machine)
examples/                             single-file games built with plain javac
```

The client is single-dependency: it uses the JDK's built-in
`java.net.http.WebSocket` for signaling and a hand-rolled JSON reader/writer, so
`webrtc-java` (and its native library) is the only third-party jar.

### A note on shutdown output

`webrtc-java` may print `VM attach current thread failed` when the JVM exits
while its native threads are still winding down. It is cosmetic and appears after
your game has already finished; it does not indicate a failure.

## Wire compatibility

Verified live against the Rust native client over `https://pqrstuvw.xyz/lobbylink`:
reliable echo (small, 0-byte, 300 KB, 8 MiB with backpressure), best-effort echo,
and explicit leave all round-trip Rust↔Java. The reliable framing, signaling
JSON, pre-negotiated channels (`reliable` id 1 ordered, `best-effort` id 2
unordered no-retransmit), and the "lower player id offers" rule all match
`clients/ts/README.md` §"Wire contract".
