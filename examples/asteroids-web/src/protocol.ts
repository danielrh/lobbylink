// DartRoids wire protocol + deterministic world, mirrored exactly by the
// Rust example in ../../asteroids-native/src/protocol.rs. See ../../PROTOCOL.md.
//
// Big-endian on the wire. All RNG / asteroid math is done in f64 and only
// narrowed to f32 at the wire, so both languages agree.

// ---- world + simulation constants -----------------------------------------

export const WORLD_W = 1600.0;
export const WORLD_H = 1000.0;

export const TURN_RATE = 3.6;
export const THRUST = 520.0;
export const DAMPING = 0.6;
export const MAX_SPEED = 720.0;
export const SHIP_RADIUS = 16.0;
export const BULLET_SPEED = 660.0;
export const BULLET_LIFE = 1.4;
export const FIRE_COOLDOWN = 0.22;
export const MAX_BULLETS = 8;
export const BULLET_RADIUS = 3.0;
export const RESPAWN_DELAY = 1.2;
export const INVULN_TIME = 2.5;

export const SEND_HZ = 16;
export const CLOCK_HZ = 4;

export const MSG_STATE = 0x01;
export const MSG_HIT = 0x02;
export const MSG_CLOCK = 0x03;

const NAME_MAX = 24;

// ---- deterministic RNG (bit-identical with Rust) --------------------------

/** FNV-1a/32 over UTF-8; the room code seeds the whole asteroid field. */
export function seedFromCode(code: string): number {
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(code)) {
    h = Math.imul(h ^ b, 0x01000193);
  }
  return h >>> 0;
}

/** mulberry32: 32-bit state, returns a double in [0, 1). */
function mulberry32(state: number): () => number {
  let a = state >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    t = (t ^ (t >>> 14)) >>> 0;
    return t / 4294967296;
  };
}

// ---- asteroids ------------------------------------------------------------

export interface Asteroid {
  /** stable unique id (spawnSecond*256 + index); for locally tracking shots */
  id: number;
  /** deterministic seed for the irregular outline (same rock on every client) */
  shape: number;
  x: number;
  y: number;
  vx: number;
  vy: number;
  radius: number;
}

/** Vertices in a rendered asteroid outline. */
export const ASTEROID_VERTS = 11;

/** Per-vertex radius multiplier in [0.80, 1.10); matches Rust `asteroid_vertex`. */
export function asteroidVertex(shape: number, i: number): number {
  let x = (shape ^ Math.imul(i, 0x9e3779b9)) >>> 0;
  x ^= x >>> 16;
  x = Math.imul(x, 0x7feb352d);
  x ^= x >>> 15;
  x = Math.imul(x, 0x846ca68b);
  x = (x ^ (x >>> 16)) >>> 0;
  return 0.8 + 0.3 * (x / 4294967296);
}

/** All asteroids currently near the map at game time `gameMs`. */
export function asteroidsAt(seed: number, gameMs: number): Asteroid[] {
  const nowSec = Math.floor(gameMs / 1000);
  const out: Asteroid[] = [];
  for (let t = nowSec - 16; t <= nowSec; t++) {
    if (t < 0) continue;
    genSecond(seed, t, gameMs, out);
  }
  return out;
}

function genSecond(seed: number, t: number, gameMs: number, out: Asteroid[]): void {
  const st = (seed ^ Math.imul(t >>> 0, 0x9e3779b9)) >>> 0;
  const r = mulberry32(st);
  const count = 1 + Math.floor(r() * 4);
  for (let index = 0; index < count; index++) {
    const edge = Math.floor(r() * 4);
    const radius = 22 + r() * 40;
    const speed = 120 + r() * 160;
    const along = r();
    const spread = (r() - 0.5) * 1.0;
    const margin = radius + 40;
    let sx: number, sy: number, base: number;
    switch (edge) {
      case 0: sx = along * WORLD_W; sy = -margin; base = Math.PI / 2; break;
      case 1: sx = WORLD_W + margin; sy = along * WORLD_H; base = Math.PI; break;
      case 2: sx = along * WORLD_W; sy = WORLD_H + margin; base = -Math.PI / 2; break;
      default: sx = -margin; sy = along * WORLD_H; base = 0; break;
    }
    const ang = base + spread;
    const vx = Math.cos(ang) * speed;
    const vy = Math.sin(ang) * speed;
    const age = (gameMs - t * 1000) / 1000;
    const x = sx + vx * age;
    const y = sy + vy * age;
    const m = radius + 80;
    if (x < -m || x > WORLD_W + m || y < -m || y > WORLD_H + m) continue;
    out.push({
      id: t * 256 + index,
      shape: (st ^ Math.imul(index, 0x9e3779b9)) >>> 0,
      x, y, vx, vy, radius,
    });
  }
}

// ---- message encode / decode ----------------------------------------------

export interface Bullet {
  x: number;
  y: number;
  vx: number;
  vy: number;
}

export interface StateMsg {
  kind: "state";
  alive: boolean;
  thrusting: boolean;
  invuln: boolean;
  seq: number;
  x: number;
  y: number;
  angle: number;
  vx: number;
  vy: number;
  score: number;
  bullets: Bullet[];
  name: string;
}

export interface HitMsg {
  kind: "hit";
  victimId: number;
  killSeq: number;
}

export interface ClockMsg {
  kind: "clock";
  gameMs: number;
}

export type Decoded = StateMsg | HitMsg | ClockMsg | null;

export function encodeState(s: Omit<StateMsg, "kind">): Uint8Array {
  const nameBytes = new TextEncoder().encode(s.name).slice(0, NAME_MAX);
  const n = Math.min(s.bullets.length, MAX_BULLETS);
  const buf = new ArrayBuffer(29 + n * 16 + 1 + nameBytes.length);
  const dv = new DataView(buf);
  let flags = 0;
  if (s.alive) flags |= 1;
  if (s.thrusting) flags |= 2;
  if (s.invuln) flags |= 4;
  dv.setUint8(0, MSG_STATE);
  dv.setUint8(1, flags);
  dv.setUint16(2, s.seq & 0xffff, false);
  dv.setFloat32(4, s.x, false);
  dv.setFloat32(8, s.y, false);
  dv.setFloat32(12, s.angle, false);
  dv.setFloat32(16, s.vx, false);
  dv.setFloat32(20, s.vy, false);
  dv.setUint32(24, s.score >>> 0, false);
  dv.setUint8(28, n);
  let o = 29;
  for (let i = 0; i < n; i++) {
    const b = s.bullets[i]!;
    dv.setFloat32(o, b.x, false);
    dv.setFloat32(o + 4, b.y, false);
    dv.setFloat32(o + 8, b.vx, false);
    dv.setFloat32(o + 12, b.vy, false);
    o += 16;
  }
  dv.setUint8(o, nameBytes.length);
  o += 1;
  new Uint8Array(buf, o).set(nameBytes);
  return new Uint8Array(buf);
}

export function encodeHit(victimId: number, killSeq: number): Uint8Array {
  const buf = new ArrayBuffer(7);
  const dv = new DataView(buf);
  dv.setUint8(0, MSG_HIT);
  dv.setUint16(1, victimId & 0xffff, false);
  dv.setUint32(3, killSeq >>> 0, false);
  return new Uint8Array(buf);
}

export function encodeClock(gameMs: number): Uint8Array {
  const buf = new ArrayBuffer(9);
  const dv = new DataView(buf);
  dv.setUint8(0, MSG_CLOCK);
  dv.setFloat64(1, gameMs, false);
  return new Uint8Array(buf);
}

export function decode(data: Uint8Array): Decoded {
  if (data.length < 1) return null;
  const dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
  switch (dv.getUint8(0)) {
    case MSG_STATE: {
      if (data.length < 29) return null;
      const flags = dv.getUint8(1);
      const n = dv.getUint8(28);
      if (data.length < 29 + n * 16 + 1) return null;
      const bullets: Bullet[] = [];
      let o = 29;
      for (let i = 0; i < n; i++) {
        bullets.push({
          x: dv.getFloat32(o, false),
          y: dv.getFloat32(o + 4, false),
          vx: dv.getFloat32(o + 8, false),
          vy: dv.getFloat32(o + 12, false),
        });
        o += 16;
      }
      const nameLen = dv.getUint8(o);
      o += 1;
      if (data.length < o + nameLen) return null;
      const name = new TextDecoder().decode(data.subarray(o, o + nameLen));
      return {
        kind: "state",
        alive: (flags & 1) !== 0,
        thrusting: (flags & 2) !== 0,
        invuln: (flags & 4) !== 0,
        seq: dv.getUint16(2, false),
        x: dv.getFloat32(4, false),
        y: dv.getFloat32(8, false),
        angle: dv.getFloat32(12, false),
        vx: dv.getFloat32(16, false),
        vy: dv.getFloat32(20, false),
        score: dv.getUint32(24, false),
        bullets,
        name,
      };
    }
    case MSG_HIT:
      if (data.length < 7) return null;
      return { kind: "hit", victimId: dv.getUint16(1, false), killSeq: dv.getUint32(3, false) };
    case MSG_CLOCK:
      if (data.length < 9) return null;
      return { kind: "clock", gameMs: dv.getFloat64(1, false) };
    default:
      return null;
  }
}

/** Wrapping seq comparison for the unordered channel. */
export function seqNewer(seq: number, last: number): boolean {
  return ((seq - last) & 0xffff) < 0x8000;
}
