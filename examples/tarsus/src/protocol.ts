// Tarsus wire protocol. Big-endian on the wire, like the other examples.
//
// Trust model (mirrors DartRoids, see ../../PROTOCOL.md for the ideas):
//   - every client simulates its own ship and its own lasers, and broadcasts
//     a full snapshot best-effort at SEND_HZ;
//   - the *victim* is authoritative for damage to itself: when a remote laser
//     hits me I decrement my own shields and broadcast a reliable DAMAGE
//     message so everyone renders the hit (and the shooter retires the laser);
//   - the lowest-id connected player is the host and simulates the NPC
//     pirates, broadcasting them best-effort in NPC messages. Clients report
//     their laser hits on NPCs to the host over the reliable channel
//     (NPC_DAMAGE) and the host applies them.
//
// Velocities travel in px per 60 Hz tick — the unit the local simulation
// uses — so multiply by 60 for px/second when extrapolating by wall-clock age.

export const SEND_HZ = 16;
/** Fixed simulation timestep (s); the game always steps at 60 Hz. */
export const TICK = 1 / 60;
export const LASER_DAMAGE = 10;
export const MAX_WIRE_LASERS = 32;
export const MAX_NPCS = 8;
export const RESPAWN_DELAY_MS = 2500;
/** Drop a remote player after this long without a state message. */
export const REMOTE_TIMEOUT_MS = 5000;
/** A new lowest-id player waits this long before adopting/spawning NPCs. */
export const HOST_GRACE_MS = 3000;

/** DAMAGE.shooterSlot when the laser belonged to an NPC (host-simulated). */
export const NPC_SHOOTER = 0xfffe;
/** DAMAGE.shooterSlot / laserId when unknown. */
export const NO_ID = 0xffff;

export const MSG_STATE = 0x01;
export const MSG_DAMAGE = 0x02;
export const MSG_NPC = 0x03;
export const MSG_NPC_DAMAGE = 0x04;
export const MSG_NPC_KILL = 0x05;

const NAME_MAX = 24;

export const SPRITE_BASE = "https://graphics.stanford.edu/~danielh/sprites/";

/** Player ship roster; the wire carries an index into this list. */
export const SHIP_SPRITES = [
  "Orion.png",
  "Broadsword.png",
  "Centurion.png",
  "Stiletto.png",
  "Gladius.png",
  "Dralthi.png",
  "Tarsus.png",
  "Talon_-_Militia.png",
];

/** NPC pirate roster; NPC messages carry an index into this list. */
export const PIRATE_SPRITES = [
  "Talon_-_Pirate.png",
  "Talon_-_Retro.png",
  "Demon.png",
  "Gothri.png",
  "Dralthi.png",
  "Kamekh.png",
];

// ---- messages ---------------------------------------------------------------

export interface WireLaser {
  /** Sender-unique id so a hit is only ever counted once. */
  id: number;
  x: number;
  y: number;
  vx: number;
  vy: number;
}

export interface StateMsg {
  kind: "state";
  seq: number;
  alive: boolean;
  flash: boolean; // shields recently hit (render flicker)
  docked: boolean; // parked at a base: invulnerable and not a target
  x: number;
  y: number;
  angle: number; // degrees, math orientation
  angleSpeed: number; // degrees per tick
  vx: number;
  vy: number;
  shields: number;
  score: number; // kills + pirate bounties, kept client-side
  ship: number; // index into SHIP_SPRITES
  lasers: WireLaser[];
  name: string;
}

/** Reliable, broadcast by the victim of a laser hit. */
export interface DamageMsg {
  kind: "damage";
  died: boolean;
  npcLaser: boolean;
  /** Slot whose laser pool held the laser (host slot for NPC lasers). */
  shooterSlot: number;
  laserId: number;
  shieldsAfter: number;
  x: number;
  y: number;
}

export interface NpcShip {
  id: number;
  flash: boolean;
  /** index into PIRATE_SPRITES */
  sprite: number;
  x: number;
  y: number;
  angle: number;
  vx: number;
  vy: number;
  shields: number;
}

/** Best-effort, broadcast by the host: all NPC ships + all NPC lasers. */
export interface NpcMsg {
  kind: "npc";
  seq: number;
  ships: NpcShip[];
  lasers: WireLaser[];
}

/** Reliable, client → host: my laser hit NPC `npcId`. */
export interface NpcDamageMsg {
  kind: "npc-damage";
  npcId: number;
  damage: number;
}

/** Reliable, host → the client whose report destroyed NPC `npcId` (bounty). */
export interface NpcKillMsg {
  kind: "npc-kill";
  npcId: number;
  /** PIRATE_SPRITES index of the destroyed hull, so the killer can price it. */
  sprite: number;
}

export type Decoded = StateMsg | DamageMsg | NpcMsg | NpcDamageMsg | NpcKillMsg | null;

// ---- encode -----------------------------------------------------------------

function putLasers(dv: DataView, o: number, lasers: WireLaser[]): number {
  const n = Math.min(lasers.length, MAX_WIRE_LASERS);
  dv.setUint8(o, n);
  o += 1;
  // newest lasers win when over the cap
  for (let i = lasers.length - n; i < lasers.length; i++) {
    const l = lasers[i];
    dv.setUint16(o, l.id & 0xffff, false);
    dv.setFloat32(o + 2, l.x, false);
    dv.setFloat32(o + 6, l.y, false);
    dv.setFloat32(o + 10, l.vx, false);
    dv.setFloat32(o + 14, l.vy, false);
    o += 18;
  }
  return o;
}

function getLasers(dv: DataView, o: number, out: WireLaser[]): number {
  const n = dv.getUint8(o);
  o += 1;
  for (let i = 0; i < n; i++) {
    out.push({
      id: dv.getUint16(o, false),
      x: dv.getFloat32(o + 2, false),
      y: dv.getFloat32(o + 6, false),
      vx: dv.getFloat32(o + 10, false),
      vy: dv.getFloat32(o + 14, false),
    });
    o += 18;
  }
  return o;
}

export function encodeState(s: Omit<StateMsg, "kind">): Uint8Array {
  const nameBytes = new TextEncoder().encode(s.name).slice(0, NAME_MAX);
  const n = Math.min(s.lasers.length, MAX_WIRE_LASERS);
  const buf = new ArrayBuffer(38 + n * 18 + 1 + nameBytes.length);
  const dv = new DataView(buf);
  let flags = 0;
  if (s.alive) flags |= 1;
  if (s.flash) flags |= 2;
  if (s.docked) flags |= 4;
  dv.setUint8(0, MSG_STATE);
  dv.setUint8(1, flags);
  dv.setUint16(2, s.seq & 0xffff, false);
  dv.setFloat32(4, s.x, false);
  dv.setFloat32(8, s.y, false);
  dv.setFloat32(12, s.angle, false);
  dv.setFloat32(16, s.angleSpeed, false);
  dv.setFloat32(20, s.vx, false);
  dv.setFloat32(24, s.vy, false);
  dv.setFloat32(28, s.shields, false);
  dv.setUint32(32, s.score >>> 0, false);
  dv.setUint8(36, s.ship & 0xff);
  let o = putLasers(dv, 37, s.lasers);
  dv.setUint8(o, nameBytes.length);
  o += 1;
  new Uint8Array(buf, o).set(nameBytes);
  return new Uint8Array(buf);
}

export function encodeDamage(d: Omit<DamageMsg, "kind">): Uint8Array {
  const buf = new ArrayBuffer(18);
  const dv = new DataView(buf);
  let flags = 0;
  if (d.died) flags |= 1;
  if (d.npcLaser) flags |= 2;
  dv.setUint8(0, MSG_DAMAGE);
  dv.setUint8(1, flags);
  dv.setUint16(2, d.shooterSlot & 0xffff, false);
  dv.setUint16(4, d.laserId & 0xffff, false);
  dv.setFloat32(6, d.shieldsAfter, false);
  dv.setFloat32(10, d.x, false);
  dv.setFloat32(14, d.y, false);
  return new Uint8Array(buf);
}

export function encodeNpc(m: Omit<NpcMsg, "kind">): Uint8Array {
  const ships = m.ships.slice(0, MAX_NPCS);
  const n = Math.min(m.lasers.length, MAX_WIRE_LASERS);
  const buf = new ArrayBuffer(4 + ships.length * 27 + 1 + n * 18);
  const dv = new DataView(buf);
  dv.setUint8(0, MSG_NPC);
  dv.setUint16(1, m.seq & 0xffff, false);
  dv.setUint8(3, ships.length);
  let o = 4;
  for (const s of ships) {
    dv.setUint8(o, s.id & 0xff);
    dv.setUint8(o + 1, s.flash ? 1 : 0);
    dv.setUint8(o + 2, s.sprite & 0xff);
    dv.setFloat32(o + 3, s.x, false);
    dv.setFloat32(o + 7, s.y, false);
    dv.setFloat32(o + 11, s.angle, false);
    dv.setFloat32(o + 15, s.vx, false);
    dv.setFloat32(o + 19, s.vy, false);
    dv.setFloat32(o + 23, s.shields, false);
    o += 27;
  }
  putLasers(dv, o, m.lasers);
  return new Uint8Array(buf);
}

export function encodeNpcDamage(npcId: number, damage: number): Uint8Array {
  const buf = new ArrayBuffer(3);
  const dv = new DataView(buf);
  dv.setUint8(0, MSG_NPC_DAMAGE);
  dv.setUint8(1, npcId & 0xff);
  dv.setUint8(2, damage & 0xff);
  return new Uint8Array(buf);
}

export function encodeNpcKill(npcId: number, sprite: number): Uint8Array {
  const buf = new ArrayBuffer(3);
  const dv = new DataView(buf);
  dv.setUint8(0, MSG_NPC_KILL);
  dv.setUint8(1, npcId & 0xff);
  dv.setUint8(2, sprite & 0xff);
  return new Uint8Array(buf);
}

// ---- decode -----------------------------------------------------------------

export function decode(data: Uint8Array): Decoded {
  if (data.length < 1) return null;
  const dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
  switch (dv.getUint8(0)) {
    case MSG_STATE: {
      if (data.length < 38) return null;
      const flags = dv.getUint8(1);
      const lasers: WireLaser[] = [];
      const nLasers = dv.getUint8(37);
      if (data.length < 38 + nLasers * 18 + 1) return null;
      let o = getLasers(dv, 37, lasers);
      const nameLen = dv.getUint8(o);
      o += 1;
      if (data.length < o + nameLen) return null;
      const name = new TextDecoder().decode(data.subarray(o, o + nameLen));
      return {
        kind: "state",
        seq: dv.getUint16(2, false),
        alive: (flags & 1) !== 0,
        flash: (flags & 2) !== 0,
        docked: (flags & 4) !== 0,
        x: dv.getFloat32(4, false),
        y: dv.getFloat32(8, false),
        angle: dv.getFloat32(12, false),
        angleSpeed: dv.getFloat32(16, false),
        vx: dv.getFloat32(20, false),
        vy: dv.getFloat32(24, false),
        shields: dv.getFloat32(28, false),
        score: dv.getUint32(32, false),
        ship: dv.getUint8(36),
        lasers,
        name,
      };
    }
    case MSG_DAMAGE: {
      if (data.length < 18) return null;
      const flags = dv.getUint8(1);
      return {
        kind: "damage",
        died: (flags & 1) !== 0,
        npcLaser: (flags & 2) !== 0,
        shooterSlot: dv.getUint16(2, false),
        laserId: dv.getUint16(4, false),
        shieldsAfter: dv.getFloat32(6, false),
        x: dv.getFloat32(10, false),
        y: dv.getFloat32(14, false),
      };
    }
    case MSG_NPC: {
      if (data.length < 4) return null;
      const count = dv.getUint8(3);
      if (data.length < 4 + count * 27 + 1) return null;
      const ships: NpcShip[] = [];
      let o = 4;
      for (let i = 0; i < count; i++) {
        ships.push({
          id: dv.getUint8(o),
          flash: dv.getUint8(o + 1) !== 0,
          sprite: dv.getUint8(o + 2),
          x: dv.getFloat32(o + 3, false),
          y: dv.getFloat32(o + 7, false),
          angle: dv.getFloat32(o + 11, false),
          vx: dv.getFloat32(o + 15, false),
          vy: dv.getFloat32(o + 19, false),
          shields: dv.getFloat32(o + 23, false),
        });
        o += 27;
      }
      const nLasers = dv.getUint8(o);
      if (data.length < o + 1 + nLasers * 18) return null;
      const lasers: WireLaser[] = [];
      getLasers(dv, o, lasers);
      return { kind: "npc", seq: dv.getUint16(1, false), ships, lasers };
    }
    case MSG_NPC_DAMAGE:
      if (data.length < 3) return null;
      return { kind: "npc-damage", npcId: dv.getUint8(1), damage: dv.getUint8(2) };
    case MSG_NPC_KILL:
      if (data.length < 3) return null;
      return { kind: "npc-kill", npcId: dv.getUint8(1), sprite: dv.getUint8(2) };
    default:
      return null;
  }
}

/** Wrapping seq comparison for the unordered channel. */
export function seqNewer(seq: number, last: number): boolean {
  return ((seq - last) & 0xffff) < 0x8000;
}

// ---- deterministic world ----------------------------------------------------
// Bases are a pure function of WORLD_SEED, so every client places the same
// landmarks at the same coordinates with zero messages (same trick as the
// DartRoids asteroid field). Keep the math in f64; a future native port must
// mirror it bit-for-bit.

/** Fixed world seed ("TARS"): bases always show up in the same place. */
export const WORLD_SEED = 0x54415253;

export const BASE_SPRITES = [
  "Agricultural.png",
  "AsteroidBase.png",
  "Refinery.png",
  "Industrial.png",
];
export const BASE_TYPE_NAMES = ["Agri Dome", "Asteroid Base", "Refinery", "Foundry"];

// ---- trading ---------------------------------------------------------------
// A circular economy over the four base types: each commodity is bought at one
// type and delivered to the next. Credits/cargo are client-side (trust-based,
// like scores); only the credit total travels in STATE for the leaderboard.

export interface Commodity {
  name: string;
  /** BASE_SPRITES index where this is bought / sold */
  buyAt: number;
  sellAt: number;
  buyPrice: number;
  sellPrice: number;
}

export const COMMODITIES: Commodity[] = [
  { name: "grain", buyAt: 0, sellAt: 1, buyPrice: 10, sellPrice: 25 },
  { name: "ore", buyAt: 1, sellAt: 2, buyPrice: 15, sellPrice: 35 },
  { name: "fuel", buyAt: 2, sellAt: 3, buyPrice: 20, sellPrice: 45 },
  { name: "machinery", buyAt: 3, sellAt: 0, buyPrice: 25, sellPrice: 55 },
];

export const CARGO_CAPACITY = 20;
export const START_CREDITS = 50;

export interface Base {
  x: number;
  y: number;
  /** index into BASE_SPRITES */
  sprite: number;
  name: string;
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

export const BASE_COUNT = 12;
/** Bases scatter in this ring around the spawn point (320, 240). */
const BASE_RING_MIN = 1200;
const BASE_RING_MAX = 6000;
const BASE_MIN_SEPARATION = 900;

export function generateBases(): Base[] {
  const r = mulberry32(WORLD_SEED);
  const bases: Base[] = [];
  for (let i = 0; i < BASE_COUNT; i++) {
    let x = 320;
    let y = 240;
    // deterministic rejection sampling keeps landmarks from overlapping
    for (let attempt = 0; attempt < 20; attempt++) {
      const ang = r() * Math.PI * 2;
      const d = BASE_RING_MIN + r() * (BASE_RING_MAX - BASE_RING_MIN);
      x = 320 + Math.cos(ang) * d;
      y = 240 + Math.sin(ang) * d;
      let clear = true;
      for (const b of bases) {
        if (Math.hypot(b.x - x, b.y - y) < BASE_MIN_SEPARATION) {
          clear = false;
          break;
        }
      }
      if (clear) break;
    }
    // the first four bases cover every type, so no trade route can dead-end
    const sprite = i < BASE_SPRITES.length ? i : Math.floor(r() * BASE_SPRITES.length) % BASE_SPRITES.length;
    bases.push({ x, y, sprite, name: `${BASE_TYPE_NAMES[sprite]} ${i + 1}` });
  }
  return bases;
}
