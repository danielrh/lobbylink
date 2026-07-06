//! DartRoids wire protocol + deterministic world. Byte-for-byte mirror of
//! ../../asteroids-web/src/protocol.ts. See ../../PROTOCOL.md.
//!
//! Big-endian on the wire. RNG / asteroid math runs in f64 (matching JS
//! doubles) and is narrowed to f32 only for storage, so both clients agree.

pub const WORLD_W: f32 = 1600.0;
pub const WORLD_H: f32 = 1000.0;

pub const TURN_RATE: f32 = 3.6;
pub const THRUST: f32 = 520.0;
pub const DAMPING: f32 = 0.6;
pub const MAX_SPEED: f32 = 720.0;
pub const SHIP_RADIUS: f32 = 16.0;
pub const BULLET_SPEED: f32 = 660.0;
pub const BULLET_LIFE: f32 = 1.4;
pub const FIRE_COOLDOWN: f32 = 0.22;
pub const MAX_BULLETS: usize = 8;
pub const BULLET_RADIUS: f32 = 3.0;
pub const RESPAWN_DELAY: f32 = 1.2;
pub const INVULN_TIME: f32 = 2.5;

pub const SEND_HZ: f64 = 16.0;
pub const CLOCK_HZ: f64 = 4.0;

const MSG_STATE: u8 = 0x01;
const MSG_HIT: u8 = 0x02;
const MSG_CLOCK: u8 = 0x03;
const NAME_MAX: usize = 24;

// ---- deterministic RNG (bit-identical with the TS client) -----------------

/// FNV-1a/32 over UTF-8; the room code seeds the whole asteroid field.
pub fn seed_from_code(code: &str) -> u32 {
    let mut h: u32 = 0x811c9dc5;
    for b in code.bytes() {
        h = (h ^ b as u32).wrapping_mul(0x01000193);
    }
    h
}

/// mulberry32: 32-bit state, yields a double in [0, 1).
struct Rng {
    state: u32,
}
impl Rng {
    fn new(state: u32) -> Self {
        Rng { state }
    }
    fn next(&mut self) -> f64 {
        self.state = self.state.wrapping_add(0x6d2b79f5);
        let mut t = self.state;
        t = (t ^ (t >> 15)).wrapping_mul(t | 1);
        t ^= t.wrapping_add((t ^ (t >> 7)).wrapping_mul(t | 61));
        t ^= t >> 14;
        (t as f64) / 4294967296.0
    }
}

// ---- asteroids ------------------------------------------------------------

#[derive(Clone, Copy)]
#[allow(dead_code)] // vx/vy kept for wire parity with the TS Asteroid shape
pub struct Asteroid {
    /// Stable unique id (spawn-second << shift | index); used for
    /// locally tracking asteroids the player has shot.
    pub id: u64,
    /// Deterministic seed for the asteroid's irregular outline, so every
    /// client draws the same (non-wobbling) rock. Derived from spawn data,
    /// not the live position.
    pub shape: u32,
    pub x: f32,
    pub y: f32,
    pub vx: f32,
    pub vy: f32,
    pub radius: f32,
}

/// Number of vertices in a rendered asteroid outline.
pub const ASTEROID_VERTS: usize = 11;

/// Deterministic per-vertex radius multiplier in [0.80, 1.10) for the
/// asteroid outline. Bit-identical with the TS `asteroidVertex`.
pub fn asteroid_vertex(shape: u32, i: u32) -> f32 {
    // lowbias32 integer hash -> [0,1)
    let mut x = shape ^ i.wrapping_mul(0x9e3779b9);
    x ^= x >> 16;
    x = x.wrapping_mul(0x7feb352d);
    x ^= x >> 15;
    x = x.wrapping_mul(0x846ca68b);
    x ^= x >> 16;
    0.80 + 0.30 * (x as f32 / 4294967296.0)
}

/// All asteroids currently near the map at game time `game_ms`.
pub fn asteroids_at(seed: u32, game_ms: f64) -> Vec<Asteroid> {
    let now_sec = (game_ms / 1000.0).floor() as i64;
    let mut out = Vec::new();
    let mut t = now_sec - 16;
    while t <= now_sec {
        if t >= 0 {
            gen_second(seed, t as u32, game_ms, &mut out);
        }
        t += 1;
    }
    out
}

fn gen_second(seed: u32, t: u32, game_ms: f64, out: &mut Vec<Asteroid>) {
    let st = seed ^ t.wrapping_mul(0x9e3779b9);
    let mut r = Rng::new(st);
    let count = 1 + (r.next() * 4.0).floor() as i32;
    let w = WORLD_W as f64;
    let h = WORLD_H as f64;
    for index in 0..count {
        let edge = (r.next() * 4.0).floor() as i32;
        let radius = 22.0 + r.next() * 40.0;
        let speed = 120.0 + r.next() * 160.0;
        let along = r.next();
        let spread = (r.next() - 0.5) * 1.0;
        let margin = radius + 40.0;
        let (sx, sy, base) = match edge {
            0 => (along * w, -margin, std::f64::consts::FRAC_PI_2),
            1 => (w + margin, along * h, std::f64::consts::PI),
            2 => (along * w, h + margin, -std::f64::consts::FRAC_PI_2),
            _ => (-margin, along * h, 0.0),
        };
        let ang = base + spread;
        let vx = ang.cos() * speed;
        let vy = ang.sin() * speed;
        let age = (game_ms - (t as f64) * 1000.0) / 1000.0;
        let x = sx + vx * age;
        let y = sy + vy * age;
        let m = radius + 80.0;
        if x < -m || x > w + m || y < -m || y > h + m {
            continue;
        }
        out.push(Asteroid {
            id: ((t as u64) << 8) | index as u64,
            shape: st ^ (index as u32).wrapping_mul(0x9e3779b9),
            x: x as f32,
            y: y as f32,
            vx: vx as f32,
            vy: vy as f32,
            radius: radius as f32,
        });
    }
}

// ---- messages -------------------------------------------------------------

#[derive(Clone, Copy)]
pub struct Bullet {
    pub x: f32,
    pub y: f32,
    pub vx: f32,
    pub vy: f32,
}

pub struct StateMsg {
    pub alive: bool,
    pub thrusting: bool,
    pub invuln: bool,
    pub seq: u16,
    pub x: f32,
    pub y: f32,
    pub angle: f32,
    pub vx: f32,
    pub vy: f32,
    pub score: u32,
    pub bullets: Vec<Bullet>,
    pub name: String,
}

#[allow(dead_code)] // victim_id kept for wire parity; receiver keys on `from`
pub enum Decoded {
    State(StateMsg),
    Hit { victim_id: u16, kill_seq: u32 },
    Clock { game_ms: f64 },
}

#[allow(clippy::too_many_arguments)]
pub fn encode_state(
    alive: bool,
    thrusting: bool,
    invuln: bool,
    seq: u16,
    x: f32,
    y: f32,
    angle: f32,
    vx: f32,
    vy: f32,
    score: u32,
    bullets: &[Bullet],
    name: &str,
) -> Vec<u8> {
    let mut name_bytes = name.as_bytes().to_vec();
    name_bytes.truncate(NAME_MAX);
    let n = bullets.len().min(MAX_BULLETS);
    let mut b = Vec::with_capacity(29 + n * 16 + 1 + name_bytes.len());
    let mut flags = 0u8;
    if alive {
        flags |= 1;
    }
    if thrusting {
        flags |= 2;
    }
    if invuln {
        flags |= 4;
    }
    b.push(MSG_STATE);
    b.push(flags);
    b.extend_from_slice(&seq.to_be_bytes());
    b.extend_from_slice(&x.to_be_bytes());
    b.extend_from_slice(&y.to_be_bytes());
    b.extend_from_slice(&angle.to_be_bytes());
    b.extend_from_slice(&vx.to_be_bytes());
    b.extend_from_slice(&vy.to_be_bytes());
    b.extend_from_slice(&score.to_be_bytes());
    b.push(n as u8);
    for bullet in &bullets[..n] {
        b.extend_from_slice(&bullet.x.to_be_bytes());
        b.extend_from_slice(&bullet.y.to_be_bytes());
        b.extend_from_slice(&bullet.vx.to_be_bytes());
        b.extend_from_slice(&bullet.vy.to_be_bytes());
    }
    b.push(name_bytes.len() as u8);
    b.extend_from_slice(&name_bytes);
    b
}

pub fn encode_hit(victim_id: u16, kill_seq: u32) -> Vec<u8> {
    let mut b = Vec::with_capacity(7);
    b.push(MSG_HIT);
    b.extend_from_slice(&victim_id.to_be_bytes());
    b.extend_from_slice(&kill_seq.to_be_bytes());
    b
}

pub fn encode_clock(game_ms: f64) -> Vec<u8> {
    let mut b = Vec::with_capacity(9);
    b.push(MSG_CLOCK);
    b.extend_from_slice(&game_ms.to_be_bytes());
    b
}

fn be_f32(d: &[u8], o: usize) -> f32 {
    f32::from_be_bytes([d[o], d[o + 1], d[o + 2], d[o + 3]])
}

pub fn decode(d: &[u8]) -> Option<Decoded> {
    match *d.first()? {
        MSG_STATE => {
            if d.len() < 29 {
                return None;
            }
            let flags = d[1];
            let n = d[28] as usize;
            if d.len() < 29 + n * 16 + 1 {
                return None;
            }
            let mut bullets = Vec::with_capacity(n);
            let mut o = 29;
            for _ in 0..n {
                bullets.push(Bullet {
                    x: be_f32(d, o),
                    y: be_f32(d, o + 4),
                    vx: be_f32(d, o + 8),
                    vy: be_f32(d, o + 12),
                });
                o += 16;
            }
            let name_len = d[o] as usize;
            o += 1;
            if d.len() < o + name_len {
                return None;
            }
            let name = String::from_utf8_lossy(&d[o..o + name_len]).into_owned();
            Some(Decoded::State(StateMsg {
                alive: flags & 1 != 0,
                thrusting: flags & 2 != 0,
                invuln: flags & 4 != 0,
                seq: u16::from_be_bytes([d[2], d[3]]),
                x: be_f32(d, 4),
                y: be_f32(d, 8),
                angle: be_f32(d, 12),
                vx: be_f32(d, 16),
                vy: be_f32(d, 20),
                score: u32::from_be_bytes([d[24], d[25], d[26], d[27]]),
                bullets,
                name,
            }))
        }
        MSG_HIT => {
            if d.len() < 7 {
                return None;
            }
            Some(Decoded::Hit {
                victim_id: u16::from_be_bytes([d[1], d[2]]),
                kill_seq: u32::from_be_bytes([d[3], d[4], d[5], d[6]]),
            })
        }
        MSG_CLOCK => {
            if d.len() < 9 {
                return None;
            }
            Some(Decoded::Clock {
                game_ms: f64::from_be_bytes([d[1], d[2], d[3], d[4], d[5], d[6], d[7], d[8]]),
            })
        }
        _ => None,
    }
}

/// Wrapping seq comparison for the unordered channel.
pub fn seq_newer(seq: u16, last: u16) -> bool {
    seq.wrapping_sub(last) < 0x8000
}
