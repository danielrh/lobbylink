# Snake — a lobbylink example game (Java)

A tiny, trust-based, **server-less multiplayer worm game** built on
[lobbylink](../../README.md) with the [Java client](../../clients/java). Every
player is a worm; you steer yours with the mouse and race everyone else in the
same room to grab apples. There is no game server — each client simulates its
own worm and broadcasts it over lobbylink's peer-to-peer DataChannels, and the
apples are a pure function of a shared clock, so nobody has to be "the host".

The worm itself — a chain of segments that trail a lead point — is adapted from
[anderrh/wildlifejava](https://github.com/anderrh/wildlifejava) (BSD 2-Clause;
see [LICENSE](LICENSE)). The original drew with Princeton's `StdDraw`; this
example ships a small self-contained `MiniDraw.java` renderer instead, so the
whole example is permissively licensed (BSD) and depends on nothing but the JDK
and the lobbylink client jars.

## Build and run

You need the Java client's `lib/` folder of jars. Build it once (JDK 17+):

```bash
cd ../../clients/java && ./gradlew lib && cd -
```

Then compile and run this example with plain `javac` — no build tool:

```bash
javac -cp "../../clients/java/build/lib/*" *.java
java  -cp "../../clients/java/build/lib/*:." Snake
```

On Windows use `;` instead of `:` in the classpath. Both arguments are optional:

```bash
java -cp "../../clients/java/build/lib/*:." Snake <server> <roomcode>
# defaults: https://pqrstuvw.xyz/lobbylink   SNAKE
```

Open two or more copies with the **same room code** — on one machine or several —
and you share the field. If the server can't be reached you still get a solo
game (it prints `playing offline`).

## How to play

- **Move the mouse** — your worm's head chases the cursor, the body trails. You
  start as a tiny 3-segment worm and get longer with every apple.
- **Eat the red apple** — touch it with your head to score and grow. A new apple
  appears somewhere else every 4 seconds; grab it before it moves or before
  another player does (once anyone eats it, it's gone for everyone).
- The **scoreboard** (top-left) lists every player and their apple count; yours
  is marked `(you)`.

## What it demonstrates about lobbylink

- **Lobby by link.** Everyone who joins room `SNAKE` shares the field; you are
  simply *the next player* to take a slot. Late joiners drop straight in — every
  packet is a full snapshot, so there is no state to catch up on.
- **Best-effort broadcasts.** Each worm broadcasts its head, length and score
  ~15×/second on the unreliable channel. Drops don't matter — the next packet is
  a complete snapshot 66 ms later. Each peer's head is **interpolated** toward its
  latest packet every frame, so remote worms glide smoothly at 15 Hz and ride
  right over the occasional dropped packet, instead of teleporting between updates.
- **Peer-derived coordination, no server logic.** Apples are a deterministic
  function of a shared clock (below), so every client spawns the identical apple
  at the identical spot with **zero** apple messages.
- **Automatic clock leadership.** The lowest-id present player is the clock
  leader; everyone slaves their clock to it, so all clients agree which apple is
  out. Leadership hands off by itself as players come and go.
- **Graceful offline.** No server? The game still runs solo — the same code path,
  with you as your own clock leader.

## Wire format

One best-effort **STATE** packet per broadcast, big-endian (lobbylink's reliable
framing house style), 31 bytes, a full snapshot:

| off | type | field       | notes                                        |
|----:|------|-------------|----------------------------------------------|
| 0   | u8   | type = 1    |                                              |
| 1   | u16  | seq         | sender tick; wraps. Stale packets dropped.   |
| 3   | f32  | headX       | world position of the head                   |
| 7   | f32  | headY       |                                              |
| 11  | u32  | score       | apples this worm has eaten                   |
| 15  | u32  | length      | segment count, so peers draw it at full size |
| 19  | f64  | clockMs     | sender's shared clock (see below)            |
| 27  | i32  | eatenIndex  | last apple index this worm ate, or -1        |

`seq` staleness test on the unordered channel: accept a packet only if
`((seq - lastSeq) & 0xFFFF) < 0x8000`.

### Shared clock and the apple field

`gameMs` is the clock leader's wall clock. The **leader** is the lowest player id
present; the leader uses its own clock, and everyone else uses the leader's last
broadcast `clockMs` advanced by the wall time elapsed since it arrived (each
received broadcast re-ticks the follower's clock).

The apple in play is index `floor(gameMs / 4000)` — one every 4 seconds. Its
position is a hash of that index, so it needs no messages:

```
seed = FNV-1a/32 over the UTF-8 room code
st   = seed ^ (index * 0x9E3779B9)          # 32-bit
rx   = mulberry32(st) ; ry = mulberry32(st)  # two draws in [0,1)
x    = MARGIN + rx*(WORLD - 2*MARGIN)
y    = MARGIN + ry*(WORLD - 2*MARGIN)        # WORLD=800, MARGIN=40
```

`mulberry32` is the same PRNG as [`../PROTOCOL.md`](../PROTOCOL.md), so this field
could interoperate with a client written in another language.

## A note on shutdown output

`webrtc-java` may print `Failed to attach thread …` when the JVM exits while its
native threads wind down. It is cosmetic and appears after the game has already
closed.

## Files

| file | license | what |
|------|---------|------|
| `Snake.java`    | repo LICENSE | the game: networking, apple field, worm logic |
| `Segment.java`  | BSD 2-Clause (anderrh) | one body segment; the trailing mechanic |
| `MiniDraw.java` | repo LICENSE | ~120-line self-contained AWT renderer (replaces StdDraw) |
