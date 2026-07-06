//! DartRoids — standalone desktop client. Trust-based multiplayer asteroids
//! on lobbylink, wire-compatible with the browser example in
//! ../../asteroids-web (see ../../PROTOCOL.md).
//!
//! Rendering/input is macroquad on the main thread; the lobbylink client runs
//! on a tokio thread, bridged by channels.
//!
//! Usage:
//!   dartroids [--server URL] [--code CODE] [--name NAME] [--max N]
//!   cargo run -- --code DARTS --name rustpilot

mod protocol;
use protocol as P;

use bytes::Bytes;
use macroquad::prelude::*;
use std::collections::HashMap;

// ---- game <-> net channel messages ----------------------------------------

enum Outbound {
    Broadcast(Vec<u8>),
    Reliable(u16, Vec<u8>),
}

enum Inbound {
    Ready { self_id: u16, code: String },
    Msg { from: u16, data: Vec<u8> },
    Roster(Vec<(u16, bool, bool)>), // (id, occupied, connected)
    Note(String),
    Closed(String),
}

// ---- networking thread ----------------------------------------------------

fn spawn_net(
    server: String,
    code: String,
    create_max: u16,
) -> (
    tokio::sync::mpsc::UnboundedSender<Outbound>,
    std::sync::mpsc::Receiver<Inbound>,
) {
    let (out_tx, out_rx) = tokio::sync::mpsc::unbounded_channel::<Outbound>();
    let (in_tx, in_rx) = std::sync::mpsc::channel::<Inbound>();
    std::thread::spawn(move || {
        let rt = tokio::runtime::Runtime::new().expect("tokio runtime");
        rt.block_on(net_main(server, code, create_max, out_rx, in_tx));
    });
    (out_tx, in_rx)
}

async fn net_main(
    server: String,
    code: String,
    create_max: u16,
    mut out_rx: tokio::sync::mpsc::UnboundedReceiver<Outbound>,
    in_tx: std::sync::mpsc::Sender<Inbound>,
) {
    use p2p_lobby_client::{ConnectOptions, CreateOptions, Event, P2PGame};

    let mut game = match P2PGame::connect(ConnectOptions {
        server,
        code,
        create: Some(CreateOptions::new(create_max)),
        ..Default::default()
    })
    .await
    {
        Ok(g) => g,
        Err(e) => {
            let _ = in_tx.send(Inbound::Closed(format!("connect failed: {e}")));
            return;
        }
    };

    let _ = in_tx.send(Inbound::Ready {
        self_id: game.self_id(),
        code: game.code().to_string(),
    });
    send_roster(&game, &in_tx);

    loop {
        tokio::select! {
            ev = game.next_event() => {
                let Some(ev) = ev else { break };
                match ev {
                    Event::Message { from, data, .. } => {
                        let _ = in_tx.send(Inbound::Msg { from, data: data.to_vec() });
                    }
                    Event::PlayerJoined { .. }
                    | Event::PlayerLeft { .. }
                    | Event::PlayerRejoined { .. }
                    | Event::PlayerReplaced { .. } => send_roster(&game, &in_tx),
                    Event::SignalingClosed { code, .. } => {
                        let _ = in_tx.send(Inbound::Note(format!("signaling closed: {code}")));
                    }
                    _ => {}
                }
            }
            cmd = out_rx.recv() => {
                match cmd {
                    Some(Outbound::Broadcast(b)) => { let _ = game.broadcast_best_effort(Bytes::from(b)).await; }
                    Some(Outbound::Reliable(to, b)) => { let _ = game.send_reliable(to, Bytes::from(b)).await; }
                    None => break,
                }
            }
        }
    }
    let _ = game.close().await;
}

fn send_roster(game: &p2p_lobby_client::P2PGame, in_tx: &std::sync::mpsc::Sender<Inbound>) {
    let r = game
        .players()
        .iter()
        .map(|p| (p.id, p.occupied, p.connected))
        .collect();
    let _ = in_tx.send(Inbound::Roster(r));
}

// ---- entities -------------------------------------------------------------

struct LocalBullet {
    x: f32,
    y: f32,
    vx: f32,
    vy: f32,
    born: f64,
}

struct Me {
    x: f32,
    y: f32,
    angle: f32,
    vx: f32,
    vy: f32,
    alive: bool,
    thrusting: bool,
    dead_until: f64,
    invuln_until: f64,
    fire_ready: f64,
    bullets: Vec<LocalBullet>,
}

struct Remote {
    x: f32,
    y: f32,
    angle: f32,
    vx: f32,
    vy: f32,
    px: f32,
    py: f32,
    pa: f32,
    alive: bool,
    invuln: bool,
    thrusting: bool,
    score: u32,
    name: String,
    bullets: Vec<P::Bullet>,
    last_recv: f64,
    seq: u16,
}

#[derive(PartialEq)]
enum Phase {
    Connecting,
    Lobby,
    Arena,
}

// ---- helpers --------------------------------------------------------------

fn wall_ms() -> f64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs_f64()
        * 1000.0
}

fn wrap(v: f32, m: f32) -> f32 {
    let r = v % m;
    if r < 0.0 {
        r + m
    } else {
        r
    }
}
fn tor_delta(d: f32, m: f32) -> f32 {
    let mut d = d % m;
    if d > m / 2.0 {
        d -= m;
    }
    if d < -m / 2.0 {
        d += m;
    }
    d
}
fn tor_dist(ax: f32, ay: f32, bx: f32, by: f32) -> f32 {
    let dx = tor_delta(ax - bx, P::WORLD_W);
    let dy = tor_delta(ay - by, P::WORLD_H);
    (dx * dx + dy * dy).sqrt()
}
fn steer_toward(cur: f32, target: f32, max_step: f32) -> f32 {
    let mut d = target - cur;
    while d > std::f32::consts::PI {
        d -= std::f32::consts::TAU;
    }
    while d < -std::f32::consts::PI {
        d += std::f32::consts::TAU;
    }
    if d.abs() <= max_step {
        target
    } else {
        cur + d.signum() * max_step
    }
}
/// HSL (h in degrees, s/l in 0..1) to an opaque macroquad Color.
fn hsl(h: f32, s: f32, l: f32) -> Color {
    let c = (1.0 - (2.0 * l - 1.0).abs()) * s;
    let hp = (h / 60.0) % 6.0;
    let x = c * (1.0 - (hp % 2.0 - 1.0).abs());
    let (r, g, b) = match hp as i32 {
        0 => (c, x, 0.0),
        1 => (x, c, 0.0),
        2 => (0.0, c, x),
        3 => (0.0, x, c),
        4 => (x, 0.0, c),
        _ => (c, 0.0, x),
    };
    let m = l - c / 2.0;
    Color::new(r + m, g + m, b + m, 1.0)
}
fn hue(id: u16) -> f32 {
    (id as f32 * 47.0) % 360.0
}

// ---- game -----------------------------------------------------------------

struct Game {
    out_tx: tokio::sync::mpsc::UnboundedSender<Outbound>,
    in_rx: std::sync::mpsc::Receiver<Inbound>,
    phase: Phase,
    self_id: u16,
    seed: u32,
    code: String,
    name: String,
    me: Me,
    remotes: HashMap<u16, Remote>,
    my_score: u32,
    my_kill_seq: u32,
    send_seq: u16,
    seen_kills: HashMap<u16, u32>,
    clock_offset_ms: f64,
    roster: Vec<(u16, bool, bool)>,
    send_acc: f64,
    clock_acc: f64,
    status: String,
    status_until: f64,
}

impl Game {
    fn game_ms(&self) -> f64 {
        wall_ms() + self.clock_offset_ms
    }

    fn is_leader(&self) -> bool {
        let mut leader = self.self_id;
        for &(id, occ, conn) in &self.roster {
            if occ && conn && id < leader {
                leader = id;
            }
        }
        leader == self.self_id
    }

    fn note(&mut self, s: impl Into<String>, secs: f64) {
        self.status = s.into();
        self.status_until = get_time() + secs;
    }

    fn poll_net(&mut self) {
        while let Ok(msg) = self.in_rx.try_recv() {
            match msg {
                Inbound::Ready { self_id, code } => {
                    self.self_id = self_id;
                    self.seed = P::seed_from_code(&code);
                    self.code = code;
                    if self.phase == Phase::Connecting {
                        self.phase = Phase::Lobby;
                    }
                }
                Inbound::Roster(r) => self.roster = r,
                Inbound::Note(s) => self.note(s, 3.0),
                Inbound::Closed(s) => self.note(format!("disconnected: {s}"), 30.0),
                Inbound::Msg { from, data } => self.on_msg(from, &data),
            }
        }
    }

    fn on_msg(&mut self, from: u16, data: &[u8]) {
        match P::decode(data) {
            Some(P::Decoded::State(s)) => self.on_state(from, s),
            Some(P::Decoded::Clock { game_ms }) => {
                let target = game_ms - wall_ms();
                if (target - self.clock_offset_ms).abs() > 1000.0 {
                    self.clock_offset_ms = target;
                } else {
                    self.clock_offset_ms += (target - self.clock_offset_ms) * 0.25;
                }
            }
            Some(P::Decoded::Hit { kill_seq, .. }) => {
                if self.seen_kills.get(&from) != Some(&kill_seq) {
                    self.seen_kills.insert(from, kill_seq);
                    self.my_score += 1;
                    let who = self
                        .remotes
                        .get(&from)
                        .map(|r| r.name.clone())
                        .unwrap_or_else(|| format!("player {from}"));
                    self.note(format!("you eliminated {who}! +1"), 1.5);
                }
            }
            None => {}
        }
    }

    fn on_state(&mut self, from: u16, s: P::StateMsg) {
        let now = get_time();
        match self.remotes.get_mut(&from) {
            Some(r) if !P::seq_newer(s.seq, r.seq) => {}
            Some(r) => {
                r.x = s.x; r.y = s.y; r.angle = s.angle; r.vx = s.vx; r.vy = s.vy;
                r.alive = s.alive; r.invuln = s.invuln; r.thrusting = s.thrusting;
                r.score = s.score; r.name = s.name; r.bullets = s.bullets;
                r.last_recv = now; r.seq = s.seq;
            }
            None => {
                self.remotes.insert(
                    from,
                    Remote {
                        x: s.x, y: s.y, angle: s.angle, vx: s.vx, vy: s.vy,
                        px: s.x, py: s.y, pa: s.angle,
                        alive: s.alive, invuln: s.invuln, thrusting: s.thrusting,
                        score: s.score, name: s.name, bullets: s.bullets,
                        last_recv: now, seq: s.seq,
                    },
                );
            }
        }
    }

    fn respawn(&mut self, t: f64) {
        let (x, y) = self.safe_spawn();
        self.me.x = x;
        self.me.y = y;
        self.me.vx = 0.0;
        self.me.vy = 0.0;
        self.me.angle = rand::gen_range(0.0, std::f32::consts::TAU);
        self.me.alive = true;
        self.me.bullets.clear();
        self.me.invuln_until = t + P::INVULN_TIME as f64;
        self.me.fire_ready = t;
    }

    fn safe_spawn(&self) -> (f32, f32) {
        let field = P::asteroids_at(self.seed, self.game_ms());
        let mut best = (P::WORLD_W / 2.0, P::WORLD_H / 2.0);
        let mut best_clear = -1.0f32;
        for _ in 0..48 {
            let x = rand::gen_range(120.0, P::WORLD_W - 120.0);
            let y = rand::gen_range(120.0, P::WORLD_H - 120.0);
            let mut clear = f32::INFINITY;
            for a in &field {
                clear = clear.min(((a.x - x).powi(2) + (a.y - y).powi(2)).sqrt() - a.radius);
            }
            if clear > best_clear {
                best_clear = clear;
                best = (x, y);
            }
            if clear > 220.0 {
                break;
            }
        }
        best
    }

    fn die(&mut self, t: f64, shooter: Option<u16>) {
        if !self.me.alive {
            return;
        }
        self.me.alive = false;
        self.me.thrusting = false;
        self.me.dead_until = t + P::RESPAWN_DELAY as f64;
        self.me.bullets.clear();
        match shooter {
            Some(s) => {
                self.my_kill_seq += 1;
                let _ = self
                    .out_tx
                    .send(Outbound::Reliable(s, P::encode_hit(self.self_id, self.my_kill_seq)));
                self.note("you were hit!", 1.2);
            }
            None => self.note("asteroid! you crashed", 1.2),
        }
    }

    fn update(&mut self, dt: f32) {
        let t = get_time();
        self.update_remotes(dt, t);

        if self.phase == Phase::Lobby && is_key_pressed(KeyCode::Enter) {
            self.phase = Phase::Arena;
            self.respawn(t);
            self.note("launched — arrows/drag to fly, space/tap to fire", 2.5);
        }
        if self.phase != Phase::Arena {
            return;
        }

        // ---- input ----
        let left = is_key_down(KeyCode::Left) || is_key_down(KeyCode::A);
        let right = is_key_down(KeyCode::Right) || is_key_down(KeyCode::D);
        let key_thrust = is_key_down(KeyCode::Up) || is_key_down(KeyCode::W);
        let mut fire = is_key_down(KeyCode::Space);

        let (steer, touch_thrust) = self.pointer_steer(&mut fire);

        if self.me.alive {
            if let Some(target) = steer {
                self.me.angle = steer_toward(self.me.angle, target, P::TURN_RATE * dt);
            } else {
                if left {
                    self.me.angle -= P::TURN_RATE * dt;
                }
                if right {
                    self.me.angle += P::TURN_RATE * dt;
                }
            }
            let thrusting = key_thrust || touch_thrust;
            if thrusting {
                self.me.vx += self.me.angle.cos() * P::THRUST * dt;
                self.me.vy += self.me.angle.sin() * P::THRUST * dt;
            }
            self.me.thrusting = thrusting;
            self.me.vx -= self.me.vx * P::DAMPING * dt;
            self.me.vy -= self.me.vy * P::DAMPING * dt;
            let sp = (self.me.vx * self.me.vx + self.me.vy * self.me.vy).sqrt();
            if sp > P::MAX_SPEED {
                self.me.vx *= P::MAX_SPEED / sp;
                self.me.vy *= P::MAX_SPEED / sp;
            }
            self.me.x = wrap(self.me.x + self.me.vx * dt, P::WORLD_W);
            self.me.y = wrap(self.me.y + self.me.vy * dt, P::WORLD_H);

            if fire && t >= self.me.fire_ready {
                if self.me.bullets.len() < P::MAX_BULLETS {
                    let (c, s) = (self.me.angle.cos(), self.me.angle.sin());
                    self.me.bullets.push(LocalBullet {
                        x: wrap(self.me.x + c * P::SHIP_RADIUS, P::WORLD_W),
                        y: wrap(self.me.y + s * P::SHIP_RADIUS, P::WORLD_H),
                        vx: self.me.vx + c * P::BULLET_SPEED,
                        vy: self.me.vy + s * P::BULLET_SPEED,
                        born: t,
                    });
                }
                self.me.fire_ready = t + P::FIRE_COOLDOWN as f64;
            }
        } else if t >= self.me.dead_until {
            self.respawn(t);
        }

        // ---- bullets ----
        self.me
            .bullets
            .retain(|b| (t - b.born) < P::BULLET_LIFE as f64);
        for b in &mut self.me.bullets {
            b.x = wrap(b.x + b.vx * dt, P::WORLD_W);
            b.y = wrap(b.y + b.vy * dt, P::WORLD_H);
        }

        // ---- collisions (only when vulnerable) ----
        if self.me.alive && t >= self.me.invuln_until {
            let mut hit_asteroid = false;
            for a in P::asteroids_at(self.seed, self.game_ms()) {
                if ((a.x - self.me.x).powi(2) + (a.y - self.me.y).powi(2)).sqrt()
                    < a.radius + P::SHIP_RADIUS
                {
                    hit_asteroid = true;
                    break;
                }
            }
            if hit_asteroid {
                self.die(t, None);
            }
        }
        if self.me.alive && t >= self.me.invuln_until {
            let mut killer = None;
            'outer: for (id, r) in &self.remotes {
                let age = ((t - r.last_recv) as f32).min(0.4);
                for b in &r.bullets {
                    let bx = b.x + b.vx * age;
                    let by = b.y + b.vy * age;
                    if tor_dist(bx, by, self.me.x, self.me.y) < P::SHIP_RADIUS + P::BULLET_RADIUS {
                        killer = Some(*id);
                        break 'outer;
                    }
                }
            }
            if let Some(k) = killer {
                self.die(t, Some(k));
            }
        }

        // ---- outgoing ----
        self.send_acc += dt as f64;
        self.clock_acc += dt as f64;
        if self.send_acc >= 1.0 / P::SEND_HZ {
            self.broadcast_state(t);
            self.send_acc = 0.0;
        }
        if self.clock_acc >= 1.0 / P::CLOCK_HZ {
            if self.is_leader() {
                let _ = self.out_tx.send(Outbound::Broadcast(P::encode_clock(self.game_ms())));
            }
            self.clock_acc = 0.0;
        }
    }

    /// Returns (steer target angle if pointer-steering, thrust). Sets `fire`
    /// if a pointer is on the fire button. Works for touch and mouse.
    fn pointer_steer(&self, fire: &mut bool) -> (Option<f32>, bool) {
        let (bx, by, br) = fire_button();
        let mut steer_pt: Option<(f32, f32)> = None;
        for tp in touches() {
            if (tp.position.x - bx).powi(2) + (tp.position.y - by).powi(2) < br * br {
                *fire = true;
            } else {
                steer_pt = Some((tp.position.x, tp.position.y));
            }
        }
        if is_mouse_button_down(MouseButton::Left) {
            let (mx, my) = mouse_position();
            if (mx - bx).powi(2) + (my - by).powi(2) < br * br {
                *fire = true;
            } else {
                steer_pt = Some((mx, my));
            }
        }
        match steer_pt {
            Some((sx, sy)) => {
                let v = View::compute();
                let (wx, wy) = v.to_world(sx, sy);
                (Some((wy - self.me.y).atan2(wx - self.me.x)), true)
            }
            None => (None, false),
        }
    }

    fn broadcast_state(&mut self, t: f64) {
        let bullets: Vec<P::Bullet> = self
            .me
            .bullets
            .iter()
            .take(P::MAX_BULLETS)
            .map(|b| P::Bullet { x: b.x, y: b.y, vx: b.vx, vy: b.vy })
            .collect();
        let seq = self.send_seq;
        self.send_seq = self.send_seq.wrapping_add(1);
        let bytes = P::encode_state(
            self.me.alive,
            self.me.thrusting,
            self.me.alive && t < self.me.invuln_until,
            seq,
            self.me.x,
            self.me.y,
            self.me.angle,
            self.me.vx,
            self.me.vy,
            self.my_score,
            &bullets,
            &self.name,
        );
        let _ = self.out_tx.send(Outbound::Broadcast(bytes));
    }

    fn update_remotes(&mut self, dt: f32, t: f64) {
        let mut drop = Vec::new();
        for (id, r) in self.remotes.iter_mut() {
            if t - r.last_recv > 5.0 {
                drop.push(*id);
                continue;
            }
            let age = ((t - r.last_recv) as f32).min(0.4);
            let tx = wrap(r.x + r.vx * age, P::WORLD_W);
            let ty = wrap(r.y + r.vy * age, P::WORLD_H);
            let k = (dt * 12.0).min(1.0);
            r.px = wrap(r.px + tor_delta(tx - r.px, P::WORLD_W) * k, P::WORLD_W);
            r.py = wrap(r.py + tor_delta(ty - r.py, P::WORLD_H) * k, P::WORLD_H);
            r.pa = steer_toward(r.pa, r.angle, tor_delta(r.angle - r.pa, std::f32::consts::TAU).abs() * k);
        }
        for id in drop {
            self.remotes.remove(&id);
        }
    }

    fn render(&self) {
        clear_background(Color::from_rgba(5, 6, 13, 255));
        let v = View::compute();

        // world border
        let (x0, y0) = v.to_screen(0.0, 0.0);
        draw_rectangle_lines(x0, y0, P::WORLD_W * v.scale, P::WORLD_H * v.scale, 2.0, Color::from_rgba(27, 36, 64, 255));

        for a in P::asteroids_at(self.seed, self.game_ms()) {
            let (cx, cy) = v.to_screen(a.x, a.y);
            draw_poly(cx, cy, 9, a.radius * v.scale, 0.0, Color::from_rgba(42, 47, 69, 255));
            draw_poly_lines(cx, cy, 9, a.radius * v.scale, 0.0, 1.5, Color::from_rgba(90, 100, 136, 255));
        }

        let t = get_time();
        for (id, r) in &self.remotes {
            if r.alive {
                let col = hsl(hue(*id), 0.7, 0.6);
                for_each_wrap(r.px, r.py, |x, y| {
                    let (cx, cy) = v.to_screen(x, y);
                    draw_ship(cx, cy, r.pa, v.scale, col, r.thrusting, r.invuln);
                });
                let (lx, ly) = v.to_screen(r.px, r.py);
                let label = format!("{} · {}", r.name, r.score);
                draw_text(&label, lx - measure_text(&label, None, 16, 1.0).width / 2.0, ly - 24.0, 16.0, Color::from_rgba(220, 230, 255, 200));
            }
            let age = ((t - r.last_recv) as f32).min(0.4);
            for b in &r.bullets {
                let (cx, cy) = v.to_screen(wrap(b.x + b.vx * age, P::WORLD_W), wrap(b.y + b.vy * age, P::WORLD_H));
                draw_circle(cx, cy, (P::BULLET_RADIUS * v.scale).max(2.0), hsl(hue(*id), 0.9, 0.7));
            }
        }

        if self.phase == Phase::Arena {
            for b in &self.me.bullets {
                let (cx, cy) = v.to_screen(b.x, b.y);
                draw_circle(cx, cy, (P::BULLET_RADIUS * v.scale).max(2.0), Color::from_rgba(234, 242, 255, 255));
            }
            if self.me.alive {
                let inv = t < self.me.invuln_until;
                for_each_wrap(self.me.x, self.me.y, |x, y| {
                    let (cx, cy) = v.to_screen(x, y);
                    draw_ship(cx, cy, self.me.angle, v.scale, Color::from_rgba(127, 233, 255, 255), self.me.thrusting, inv);
                });
            }
        }

        self.render_hud(t);
    }

    fn render_hud(&self, t: f64) {
        let players = 1 + self.remotes.len();
        draw_text(&format!("SCORE {}", self.my_score), 14.0, 28.0, 26.0, Color::from_rgba(207, 227, 255, 255));
        let line = format!(
            "players {}   room {}   {}",
            players,
            self.code,
            if self.is_leader() { "*clock" } else { "" }
        );
        draw_text(&line, 14.0, 52.0, 20.0, Color::from_rgba(180, 195, 220, 255));

        match self.phase {
            Phase::Connecting => center_text("connecting…", 30, WHITE),
            Phase::Lobby => {
                let here = self.roster.iter().filter(|p| p.1).count();
                center_text(
                    &format!("LOBBY — {} pilot(s) here.  press ENTER to launch", here),
                    28,
                    Color::from_rgba(127, 233, 255, 255),
                );
            }
            Phase::Arena => {
                if !self.me.alive {
                    center_text("respawning…", 28, Color::from_rgba(255, 120, 120, 230));
                }
                // fire button
                let (bx, by, br) = fire_button();
                draw_circle_lines(bx, by, br, 2.0, Color::from_rgba(127, 233, 255, 255));
                let fw = measure_text("FIRE", None, 20, 1.0).width;
                draw_text("FIRE", bx - fw / 2.0, by + 6.0, 20.0, Color::from_rgba(127, 233, 255, 255));
            }
        }

        if t < self.status_until && !self.status.is_empty() {
            let w = measure_text(&self.status, None, 20, 1.0).width;
            draw_text(&self.status, screen_width() / 2.0 - w / 2.0, screen_height() - 70.0, 20.0, Color::from_rgba(234, 240, 255, 255));
        }
    }
}

// ---- view transform + drawing helpers -------------------------------------

struct View {
    scale: f32,
    off_x: f32,
    off_y: f32,
}
impl View {
    fn compute() -> View {
        let scale = (screen_width() / P::WORLD_W).min(screen_height() / P::WORLD_H);
        View {
            scale,
            off_x: (screen_width() - P::WORLD_W * scale) / 2.0,
            off_y: (screen_height() - P::WORLD_H * scale) / 2.0,
        }
    }
    fn to_screen(&self, x: f32, y: f32) -> (f32, f32) {
        (self.off_x + x * self.scale, self.off_y + y * self.scale)
    }
    fn to_world(&self, sx: f32, sy: f32) -> (f32, f32) {
        ((sx - self.off_x) / self.scale, (sy - self.off_y) / self.scale)
    }
}

fn fire_button() -> (f32, f32, f32) {
    (screen_width() - 78.0, screen_height() - 78.0, 50.0)
}

fn center_text(s: &str, size: u16, color: Color) {
    let w = measure_text(s, None, size, 1.0).width;
    draw_text(s, screen_width() / 2.0 - w / 2.0, screen_height() / 2.0, size as f32, color);
}

fn for_each_wrap(wx: f32, wy: f32, mut draw: impl FnMut(f32, f32)) {
    for dx in [-P::WORLD_W, 0.0, P::WORLD_W] {
        for dy in [-P::WORLD_H, 0.0, P::WORLD_H] {
            let x = wx + dx;
            let y = wy + dy;
            if x > -40.0 && x < P::WORLD_W + 40.0 && y > -40.0 && y < P::WORLD_H + 40.0 {
                draw(x, y);
            }
        }
    }
}

fn draw_ship(cx: f32, cy: f32, ang: f32, scale: f32, color: Color, thrust: bool, invuln: bool) {
    let (c, s) = (ang.cos(), ang.sin());
    let xf = |px: f32, py: f32| Vec2::new(cx + (px * c - py * s) * scale, cy + (px * s + py * c) * scale);
    if thrust {
        let flick = 6.0 + rand::gen_range(0.0, 8.0);
        draw_triangle(xf(-7.0, 0.0), xf(-7.0 - flick, 5.0), xf(-7.0 - flick, -5.0), Color::from_rgba(255, 170, 60, 230));
    }
    let mut col = color;
    if invuln {
        col.a = 0.4 + 0.3 * (get_time() as f32 * 11.0).sin();
    }
    let (nose, rb, tail, lb) = (xf(20.0, 0.0), xf(-14.0, 12.0), xf(-7.0, 0.0), xf(-14.0, -12.0));
    draw_triangle(nose, rb, tail, col);
    draw_triangle(nose, tail, lb, col);
    draw_triangle_lines(nose, rb, tail, 1.5, Color::from_rgba(255, 255, 255, 150));
    draw_triangle_lines(nose, tail, lb, 1.5, Color::from_rgba(255, 255, 255, 150));
}

fn draw_triangle_lines(a: Vec2, b: Vec2, c: Vec2, th: f32, color: Color) {
    draw_line(a.x, a.y, b.x, b.y, th, color);
    draw_line(b.x, b.y, c.x, c.y, th, color);
    draw_line(c.x, c.y, a.x, a.y, th, color);
}

// ---- entry point ----------------------------------------------------------

fn parse_args() -> (String, String, String, u16) {
    let mut server = "https://pqrstuvw.xyz/lobbylink".to_string();
    let mut code = "DARTS".to_string();
    let mut name = "rustpilot".to_string();
    // The server caps new rooms at max_players_hard (32 on the test server);
    // requesting more makes creation fail.
    let mut max: u16 = 32;
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        match a.as_str() {
            "--server" => server = args.next().unwrap_or(server),
            "--code" => code = args.next().unwrap_or(code),
            "--name" => name = args.next().unwrap_or(name),
            "--max" => max = args.next().and_then(|s| s.parse().ok()).unwrap_or(max),
            _ => {}
        }
    }
    (server, code, name, max)
}

fn window_conf() -> Conf {
    Conf {
        window_title: "DartRoids".to_string(),
        window_width: 1000,
        window_height: 640,
        high_dpi: true,
        ..Default::default()
    }
}

#[macroquad::main(window_conf)]
async fn main() {
    let (server, code, name, max) = parse_args();
    let seed = P::seed_from_code(&code);
    let (out_tx, in_rx) = spawn_net(server, code.clone(), max);

    let mut g = Game {
        out_tx,
        in_rx,
        phase: Phase::Connecting,
        self_id: 0,
        seed,
        code,
        name,
        me: Me {
            x: P::WORLD_W / 2.0,
            y: P::WORLD_H / 2.0,
            angle: -std::f32::consts::FRAC_PI_2,
            vx: 0.0,
            vy: 0.0,
            alive: false,
            thrusting: false,
            dead_until: 0.0,
            invuln_until: 0.0,
            fire_ready: 0.0,
            bullets: Vec::new(),
        },
        remotes: HashMap::new(),
        my_score: 0,
        my_kill_seq: 0,
        send_seq: 0,
        seen_kills: HashMap::new(),
        clock_offset_ms: 0.0,
        roster: Vec::new(),
        send_acc: 0.0,
        clock_acc: 0.0,
        status: String::new(),
        status_until: 0.0,
    };

    loop {
        g.poll_net();
        let dt = get_frame_time().min(0.05);
        g.update(dt);
        g.render();
        next_frame().await;
    }
}
