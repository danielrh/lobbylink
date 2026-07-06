# DartRoids wire protocol (v1)

A trust-based, server-less multiplayer asteroids clone built on **lobbylink**.
Two independent codebases — `asteroids-web` (TypeScript, browser) and
`asteroids-native` (Rust, desktop) — are wire-compatible: a Rust player and a
browser player in the same room see and shoot each other. This file is the
single source of truth; both implementations mirror the constants and
algorithms below **exactly** so the deterministic parts (asteroid field, game
clock) agree bit-for-bit.

Everything rides on lobbylink DataChannels:

- **best-effort** (unordered, lossy): per-tick state and the clock. 16 Hz.
- **reliable** (ordered): "you killed me" hit reports. Rare.

There is no authority. Each client simulates its own ship, broadcasts it, and
locally decides when *it* got hit. The victim tells the shooter. That's the
whole trust model.

## Coordinate system

A fixed world rectangle. Players **wrap** at the edges; asteroids **do not**.

| name        | value  |
|-------------|--------|
| `WORLD_W`   | 1600.0 |
| `WORLD_H`   | 1000.0 |

All positions/velocities are world units (units/second) as IEEE `f32`.
Multi-byte integers and floats on the wire are **big-endian** (matching the
lobbylink reliable-framing house style).

## Messages

Every packet starts with a `u8` type tag.

### `0x01` STATE — best-effort, broadcast every 62.5 ms (16 Hz)

One packet fully describes a player, so late joiners need no history.

| off | type | field        | notes                                            |
|----:|------|--------------|--------------------------------------------------|
| 0   | u8   | type = 1     |                                                  |
| 1   | u8   | flags        | bit0 alive, bit1 thrusting, bit2 invulnerable    |
| 2   | u16  | seq          | sender tick counter; wraps. Drop stale packets.  |
| 4   | f32  | x            | world position                                   |
| 8   | f32  | y            |                                                  |
| 12  | f32  | angle        | heading, radians                                 |
| 16  | f32  | vx           | velocity, for dead-reckoning between packets     |
| 20  | f32  | vy           |                                                  |
| 24  | u32  | score        | sender's own kill count (advisory scoreboard)    |
| 28  | u8   | bulletCount  | 0..=`MAX_BULLETS` (8)                             |
| 29  | …    | bullets      | `bulletCount` × {f32 x, f32 y, f32 vx, f32 vy}    |
| …   | u8   | nameLen      | 0..=24                                            |
| …   | …    | name         | `nameLen` bytes UTF-8                             |

`seq` staleness test (unordered channel): accept a packet only if
`((seq - lastSeq) & 0xFFFF) < 0x8000`.

### `0x02` HIT — reliable, victim → shooter

Sent once when the victim decides a bullet owned by the shooter killed it.
The shooter increments its own score.

| off | type | field   | notes                                       |
|----:|------|---------|---------------------------------------------|
| 0   | u8   | type = 2 |                                            |
| 1   | u16  | victimId | sender id (redundant with lobbylink `from`) |
| 3   | u32  | killSeq  | victim death counter; dedupe on receiver    |

### `0x03` CLOCK — best-effort, leader → everyone, 4 Hz

The lowest-id live player is the clock leader and broadcasts the shared game
time. Everyone else slaves their clock to it. See "Clock" below.

| off | type | field    | notes                            |
|----:|------|----------|----------------------------------|
| 0   | u8   | type = 3 |                                  |
| 1   | f64  | gameMs   | leader's current game time in ms |

## Clock (shared time so the asteroid field agrees)

Asteroids are a pure function of `(seed, second)`, so every client must agree on
what second it is. Local wall clocks drift, so:

- `gameMs() = localNowMs() + offsetMs`, `offsetMs` starts at 0 (i.e. game time
  ≈ wall clock, already roughly NTP-synced across machines).
- **Leader** = the lowest player id that is either self or `occupied && connected`
  in the lobbylink roster. Only the leader broadcasts `CLOCK`. Leadership moves
  automatically as players come and go; the handoff is seamless because
  everyone tracks the same virtual time as an offset from their own clock.
- On receiving `CLOCK(gameMs)`: `target = gameMs - localNowMs()`. If
  `|target - offsetMs| > 1000` snap (`offsetMs = target`), else ease
  (`offsetMs += (target - offsetMs) * 0.25`).

## Asteroid field (deterministic, never broadcast)

`seed` is derived from the room code so all players agree with zero negotiation.

### Hashing / RNG (must be bit-identical across languages)

FNV-1a/32 over the UTF-8 room code:

```
seed = 0x811c9dc5
for b in code_utf8_bytes: seed = (seed ^ b) * 0x01000193   (mod 2^32)
```

mulberry32 PRNG, 32-bit state, returns a double in [0, 1):

```
next(state):                             # all ops mod 2^32
  state = state + 0x6D2B79F5
  t = state
  t = (t ^ (t >>> 15)) * (t | 1)
  t = t ^ (t + ((t ^ (t >>> 7)) * (t | 61)))
  t = t ^ (t >>> 14)
  return t / 4294967296                  # divide as f64
```

All derived arithmetic below is done in **f64**, converted to `f32` only when
written to the wire / stored, so both languages produce identical trajectories.

### Spawning

For each integer second `t` a small batch spawns just outside the map. State
for the batch: `st = seed ^ (t_u32 * 0x9E3779B9)  (mod 2^32)`, then draw with
`next(st)` in order:

```
count = 1 + floor(r()*4)                 # 1..=4
for i in 0..count:
  edge   = floor(r()*4)                  # 0 top,1 right,2 bottom,3 left
  radius = 22 + r()*40                   # 22..62
  speed  = 120 + r()*160                 # 120..280
  along  = r()                           # position along the edge, 0..1
  spread = (r() - 0.5) * 1.0             # heading jitter, radians
  margin = radius + 40
  # base heading points across the map, into the interior:
  #   top -> +y (PI/2), right -> -x (PI), bottom -> -y (-PI/2), left -> +x (0)
  # spawn point sits `margin` outside the chosen edge.
  angle  = baseAngle(edge) + spread
  vx = cos(angle)*speed ; vy = sin(angle)*speed
  spawnMs = t*1000
```

Edge spawn points (before applying spread to heading):

| edge | spawn x            | spawn y            | baseAngle |
|------|--------------------|--------------------|-----------|
| 0 top    | along*WORLD_W  | -margin            |  PI/2     |
| 1 right  | WORLD_W+margin | along*WORLD_H      |  PI       |
| 2 bottom | along*WORLD_W  | WORLD_H+margin     | -PI/2     |
| 3 left   | -margin        | along*WORLD_H      |  0        |

Position at time `now`: `age=(now-spawnMs)/1000`; `x=spawnX+vx*age`,
`y=spawnY+vy*age`. An asteroid is **retired** (removed, never wraps) once it is
more than `radius+80` outside every edge.

Each frame a client materialises the live field by iterating
`t in [floor(gameSec)-16 .. floor(gameSec)]` (16 s covers the slowest crossing)
and keeping asteroids that are still near the map. Because spawns are always
`margin` outside the border, a player sitting in the interior is **never**
clobbered by a freshly spawned asteroid — exactly the guarantee we want.

## Simulation constants (both clients)

| name              | value | meaning                                  |
|-------------------|-------|------------------------------------------|
| `TURN_RATE`       | 3.6   | rad/s ship rotation                      |
| `THRUST`          | 520   | units/s² forward acceleration            |
| `DAMPING`         | 0.6   | linear drag: `v += -v*DAMPING*dt`        |
| `MAX_SPEED`       | 720   | units/s cap                              |
| `SHIP_RADIUS`     | 16    | collision radius                         |
| `BULLET_SPEED`    | 660   | units/s, added to ship velocity          |
| `BULLET_LIFE`     | 1.4   | seconds                                  |
| `FIRE_COOLDOWN`   | 0.22  | seconds between shots                    |
| `MAX_BULLETS`     | 8     | broadcast + live cap                     |
| `BULLET_RADIUS`   | 3     | for hit tests                            |
| `RESPAWN_DELAY`   | 1.2   | seconds dead before respawn              |
| `INVULN_TIME`     | 2.5   | seconds of spawn protection              |
| `SEND_HZ`         | 16    | state broadcast rate                     |
| `CLOCK_HZ`        | 4     | leader clock rate                        |

Hit test: a foreign bullet kills you if `dist(bullet, you) < SHIP_RADIUS +
BULLET_RADIUS` and you are `alive && !invulnerable`. An asteroid kills you if
`dist(asteroid, you) < asteroid.radius + SHIP_RADIUS` (self-destruct, no score
changes hands). Bullets do not destroy asteroids (would desync the shared
field). Remote ships and their bullets are dead-reckoned by their last velocity
between packets and eased toward each fresh STATE.
