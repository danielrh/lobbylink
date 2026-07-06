//! DartRoids in the browser, written in Rust and compiled to wasm.
//!
//! Renders with the browser Canvas 2D API (web-sys) and drives the lobbylink
//! *wasm* client on the browser event loop — the "embed in a wasm-bindgen app"
//! scenario the client was designed for. No macroquad, no threads, no tokio:
//! networking is a `spawn_local` task that talks to the render loop through an
//! unbounded channel + shared `Rc<RefCell<Game>>`.
//!
//! The protocol/sim logic is the exact same `protocol.rs` as the native and
//! (byte-for-byte) the TypeScript examples, so all three interoperate.

#[path = "../../asteroids-native/src/protocol.rs"]
mod protocol;
use protocol as P;

use bytes::Bytes;
use futures::channel::mpsc;
use futures::{FutureExt, StreamExt};
use std::cell::RefCell;
use std::collections::{HashMap, HashSet};
use std::f64::consts::{FRAC_PI_2, PI, TAU};
use std::rc::Rc;
use wasm_bindgen::prelude::*;
use wasm_bindgen::JsCast;
use web_sys::{
    CanvasRenderingContext2d, HtmlCanvasElement, HtmlElement, HtmlInputElement, KeyboardEvent,
    PointerEvent,
};

// ---- tiny DOM helpers -----------------------------------------------------

fn window() -> web_sys::Window {
    web_sys::window().unwrap()
}
fn document() -> web_sys::Document {
    window().document().unwrap()
}
fn el(id: &str) -> HtmlElement {
    document().get_element_by_id(id).unwrap().dyn_into().unwrap()
}
fn input_value(id: &str) -> String {
    document()
        .get_element_by_id(id)
        .unwrap()
        .dyn_into::<HtmlInputElement>()
        .unwrap()
        .value()
}
fn set_display(id: &str, v: &str) {
    el(id).style().set_property("display", v).ok();
}
fn now_secs() -> f64 {
    window().performance().unwrap().now() / 1000.0
}
fn wall_ms() -> f64 {
    js_sys::Date::now()
}

// ---- entities (mirror of the native client) -------------------------------

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

#[derive(PartialEq, Clone, Copy)]
enum Phase {
    Menu,
    Connecting,
    Lobby,
    Arena,
}

enum Outbound {
    Broadcast(Vec<u8>),
    Reliable(u16, Vec<u8>),
}

#[derive(Default)]
struct Input {
    left: bool,
    right: bool,
    thrust: bool,
    fire: bool,
    touch: Option<(f32, f32)>, // world-space steer target
}

struct Game {
    ctx: CanvasRenderingContext2d,
    canvas: HtmlCanvasElement,
    out_tx: Option<mpsc::UnboundedSender<Outbound>>,
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
    /// Asteroid ids this player has shot (local-only; asteroids are
    /// deterministic so there is no wire change and interop is preserved).
    destroyed: HashSet<u64>,
    input: Input,
    last_secs: f64,
    send_acc: f64,
    clock_acc: f64,
    status: String,
    status_until: f64,
    // cached view transform (updated each render, used by pointer→world)
    scale: f32,
    off_x: f32,
    off_y: f32,
}

type Shared = Rc<RefCell<Game>>;

// ---- math helpers ---------------------------------------------------------

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
    while d > PI as f32 {
        d -= TAU as f32;
    }
    while d < -PI as f32 {
        d += TAU as f32;
    }
    if d.abs() <= max_step {
        target
    } else {
        cur + d.signum() * max_step
    }
}
fn hue(id: u16) -> f32 {
    (id as f32 * 47.0) % 360.0
}
fn rand01() -> f32 {
    js_sys::Math::random() as f32
}

// ---- entry point ----------------------------------------------------------

#[wasm_bindgen(start)]
pub fn start() -> Result<(), JsValue> {
    console_error_hook();
    let canvas: HtmlCanvasElement = document()
        .get_element_by_id("view")
        .unwrap()
        .dyn_into()
        .unwrap();
    let ctx: CanvasRenderingContext2d = canvas
        .get_context("2d")?
        .unwrap()
        .dyn_into()
        .unwrap();

    // prefill room code from the URL hash so links are shareable
    if let Ok(hash) = window().location().hash() {
        let code = hash.trim_start_matches('#');
        if !code.is_empty() {
            el("code")
                .dyn_into::<HtmlInputElement>()
                .unwrap()
                .set_value(code);
        }
    }

    let game = Game {
        ctx,
        canvas,
        out_tx: None,
        phase: Phase::Menu,
        self_id: 0,
        seed: 0,
        code: String::new(),
        name: String::new(),
        me: Me {
            x: P::WORLD_W / 2.0,
            y: P::WORLD_H / 2.0,
            angle: -FRAC_PI_2 as f32,
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
        destroyed: HashSet::new(),
        input: Input::default(),
        last_secs: now_secs(),
        send_acc: 0.0,
        clock_acc: 0.0,
        status: String::new(),
        status_until: 0.0,
        scale: 1.0,
        off_x: 0.0,
        off_y: 0.0,
    };
    let shared: Shared = Rc::new(RefCell::new(game));

    wire_ui(&shared);
    wire_input(&shared);
    start_raf(shared);
    Ok(())
}

fn console_error_hook() {
    std::panic::set_hook(Box::new(|info| {
        web_sys::console::error_1(&format!("panic: {info}").into());
    }));
}

// ---- UI + input wiring -----------------------------------------------------

fn wire_ui(shared: &Shared) {
    // Enter lobby
    {
        let shared = shared.clone();
        let cb = Closure::<dyn FnMut()>::new(move || enter_lobby(&shared));
        el("enter")
            .add_event_listener_with_callback("click", cb.as_ref().unchecked_ref())
            .unwrap();
        cb.forget();
    }
    // Launch
    {
        let shared = shared.clone();
        let cb = Closure::<dyn FnMut()>::new(move || {
            let mut g = shared.borrow_mut();
            g.phase = Phase::Arena;
            let t = now_secs();
            respawn(&mut g, t);
            set_status(&mut g, "launched — arrows/drag to fly, space/tap to fire", 2.5);
            drop(g);
            set_display("lobby", "none");
        });
        el("launch")
            .add_event_listener_with_callback("click", cb.as_ref().unchecked_ref())
            .unwrap();
        cb.forget();
    }
    // Leave -> reload frees the slot
    {
        let cb = Closure::<dyn FnMut()>::new(move || {
            window().location().reload().ok();
        });
        el("leave")
            .add_event_listener_with_callback("click", cb.as_ref().unchecked_ref())
            .unwrap();
        cb.forget();
    }
}

fn enter_lobby(shared: &Shared) {
    let name = {
        let n = input_value("name");
        let n = n.trim();
        if n.is_empty() { "player".to_string() } else { n.chars().take(24).collect() }
    };
    let code = {
        let c = input_value("code");
        let c = c.trim();
        if c.is_empty() { "DARTS".to_string() } else { c.to_string() }
    };
    let mut server = input_value("server");
    server = server.trim().to_string();
    if server.is_empty() {
        server = "https://pqrstuvw.xyz/lobbylink".to_string();
    }
    let max: u16 = input_value("maxPlayers").parse().unwrap_or(32).clamp(2, 32);

    window().location().set_hash(&code).ok();

    let (tx, rx) = mpsc::unbounded::<Outbound>();
    {
        let mut g = shared.borrow_mut();
        g.name = name;
        g.code = code.clone();
        g.seed = P::seed_from_code(&code);
        g.out_tx = Some(tx);
        g.phase = Phase::Connecting;
        set_status(&mut g, "connecting…", 3.0);
    }
    set_display("menu", "none");
    set_display("lobby", "flex");

    let shared = shared.clone();
    wasm_bindgen_futures::spawn_local(run_net(shared, server, code, max, rx));
}

fn wire_input(shared: &Shared) {
    // keyboard
    {
        let shared = shared.clone();
        let cb = Closure::<dyn FnMut(KeyboardEvent)>::new(move |e: KeyboardEvent| {
            if apply_key(&shared, &e.key(), true) {
                e.prevent_default();
            }
        });
        window()
            .add_event_listener_with_callback("keydown", cb.as_ref().unchecked_ref())
            .unwrap();
        cb.forget();
    }
    {
        let shared = shared.clone();
        let cb = Closure::<dyn FnMut(KeyboardEvent)>::new(move |e: KeyboardEvent| {
            if apply_key(&shared, &e.key(), false) {
                e.prevent_default();
            }
        });
        window()
            .add_event_listener_with_callback("keyup", cb.as_ref().unchecked_ref())
            .unwrap();
        cb.forget();
    }
    // pointer steering on the canvas
    let canvas = shared.borrow().canvas.clone();
    for (ev, down) in [("pointerdown", Some(true)), ("pointermove", None), ("pointerup", Some(false)), ("pointercancel", Some(false)), ("pointerleave", Some(false))] {
        let shared = shared.clone();
        let cb = Closure::<dyn FnMut(PointerEvent)>::new(move |e: PointerEvent| {
            let mut g = shared.borrow_mut();
            match down {
                Some(false) => g.input.touch = None,
                Some(true) => {
                    let p = screen_to_world(&g, e.client_x() as f32, e.client_y() as f32);
                    g.input.touch = Some(p);
                }
                None => {
                    if g.input.touch.is_some() {
                        let p = screen_to_world(&g, e.client_x() as f32, e.client_y() as f32);
                        g.input.touch = Some(p);
                    }
                }
            }
        });
        canvas
            .add_event_listener_with_callback(ev, cb.as_ref().unchecked_ref())
            .unwrap();
        cb.forget();
    }
    // fire button
    for (ev, on) in [("pointerdown", true), ("pointerup", false), ("pointerleave", false), ("pointercancel", false)] {
        let shared = shared.clone();
        let cb = Closure::<dyn FnMut(PointerEvent)>::new(move |e: PointerEvent| {
            shared.borrow_mut().input.fire = on;
            e.prevent_default();
        });
        el("fire")
            .add_event_listener_with_callback(ev, cb.as_ref().unchecked_ref())
            .unwrap();
        cb.forget();
    }
}

fn apply_key(shared: &Shared, key: &str, down: bool) -> bool {
    let mut g = shared.borrow_mut();
    match key {
        "ArrowLeft" | "a" | "A" => g.input.left = down,
        "ArrowRight" | "d" | "D" => g.input.right = down,
        "ArrowUp" | "w" | "W" => g.input.thrust = down,
        " " | "ArrowDown" => g.input.fire = down,
        _ => return false,
    }
    true
}

fn screen_to_world(g: &Game, client_x: f32, client_y: f32) -> (f32, f32) {
    let rect = g.canvas.get_bounding_client_rect();
    let dpr = window().device_pixel_ratio() as f32;
    let px = (client_x - rect.left() as f32) * dpr;
    let py = (client_y - rect.top() as f32) * dpr;
    ((px - g.off_x) / g.scale, (py - g.off_y) / g.scale)
}

// ---- networking task -------------------------------------------------------

async fn run_net(shared: Shared, server: String, code: String, max: u16, mut out_rx: mpsc::UnboundedReceiver<Outbound>) {
    use p2p_lobby_client::{ConnectOptions, CreateOptions, Event, P2PGame, StorageKind};

    let opts = ConnectOptions {
        server,
        code: code.clone(),
        create: Some(CreateOptions::new(max)),
        storage_key: Some(format!("dartroids-{code}")),
        storage: StorageKind::Session,
        ..Default::default()
    };
    let mut game = match P2PGame::connect(opts).await {
        Ok(g) => g,
        Err(e) => {
            let mut g = shared.borrow_mut();
            set_status(&mut g, format!("connect failed: {e}"), 60.0);
            return;
        }
    };
    {
        let mut g = shared.borrow_mut();
        g.self_id = game.self_id();
        if g.phase == Phase::Connecting {
            g.phase = Phase::Lobby;
        }
    }
    store_roster(&shared, &game);

    enum Step {
        In(Event),
        Out(Outbound),
        End,
    }
    loop {
        let step = {
            let ev = game.next_event().fuse();
            let cf = out_rx.next().fuse();
            futures::pin_mut!(ev, cf);
            futures::select! {
                e = ev => match e { Some(evt) => Step::In(evt), None => Step::End },
                c = cf => match c { Some(cmd) => Step::Out(cmd), None => Step::End },
            }
        };
        match step {
            Step::In(evt) => {
                if handle_event(&shared, evt) {
                    store_roster(&shared, &game);
                }
            }
            Step::Out(Outbound::Broadcast(b)) => {
                let _ = game.broadcast_best_effort(Bytes::from(b)).await;
            }
            Step::Out(Outbound::Reliable(to, b)) => {
                let _ = game.send_reliable(to, Bytes::from(b)).await;
            }
            Step::End => break,
        }
    }
    let _ = game.close().await;
}

fn store_roster(shared: &Shared, game: &p2p_lobby_client::P2PGame) {
    let r: Vec<(u16, bool, bool)> = game
        .players()
        .iter()
        .map(|p| (p.id, p.occupied, p.connected))
        .collect();
    shared.borrow_mut().roster = r;
}

/// Applies an inbound event to shared state. Returns true if the roster
/// should be refreshed (membership changed).
fn handle_event(shared: &Shared, evt: p2p_lobby_client::Event) -> bool {
    use p2p_lobby_client::Event;
    match evt {
        Event::Message { from, data, .. } => {
            on_msg(shared, from, &data);
            false
        }
        Event::PlayerJoined { .. }
        | Event::PlayerLeft { .. }
        | Event::PlayerRejoined { .. }
        | Event::PlayerReplaced { .. } => true,
        Event::SignalingClosed { code, .. } => {
            let mut g = shared.borrow_mut();
            set_status(&mut g, format!("signaling closed: {code}"), 4.0);
            false
        }
        _ => false,
    }
}

fn on_msg(shared: &Shared, from: u16, data: &[u8]) {
    let mut g = shared.borrow_mut();
    match P::decode(data) {
        Some(P::Decoded::State(s)) => on_state(&mut g, from, s),
        Some(P::Decoded::Clock { game_ms }) => {
            let target = game_ms - wall_ms();
            if (target - g.clock_offset_ms).abs() > 1000.0 {
                g.clock_offset_ms = target;
            } else {
                g.clock_offset_ms += (target - g.clock_offset_ms) * 0.25;
            }
        }
        Some(P::Decoded::Hit { kill_seq, .. }) => {
            if g.seen_kills.get(&from) != Some(&kill_seq) {
                g.seen_kills.insert(from, kill_seq);
                g.my_score += 1;
                let who = g.remotes.get(&from).map(|r| r.name.clone()).unwrap_or_else(|| format!("player {from}"));
                set_status(&mut g, format!("you eliminated {who}! +1"), 1.5);
            }
        }
        None => {}
    }
}

fn on_state(g: &mut Game, from: u16, s: P::StateMsg) {
    let now = now_secs();
    match g.remotes.get_mut(&from) {
        Some(r) if !P::seq_newer(s.seq, r.seq) => {}
        Some(r) => {
            r.x = s.x; r.y = s.y; r.angle = s.angle; r.vx = s.vx; r.vy = s.vy;
            r.alive = s.alive; r.invuln = s.invuln; r.thrusting = s.thrusting;
            r.score = s.score; r.name = s.name; r.bullets = s.bullets;
            r.last_recv = now; r.seq = s.seq;
        }
        None => {
            g.remotes.insert(from, Remote {
                x: s.x, y: s.y, angle: s.angle, vx: s.vx, vy: s.vy,
                px: s.x, py: s.y, pa: s.angle,
                alive: s.alive, invuln: s.invuln, thrusting: s.thrusting,
                score: s.score, name: s.name, bullets: s.bullets,
                last_recv: now, seq: s.seq,
            });
        }
    }
}

// ---- simulation ------------------------------------------------------------

fn set_status(g: &mut Game, s: impl Into<String>, secs: f64) {
    g.status = s.into();
    g.status_until = now_secs() + secs;
}

fn is_leader(g: &Game) -> bool {
    let mut leader = g.self_id;
    for &(id, occ, conn) in &g.roster {
        if occ && conn && id < leader {
            leader = id;
        }
    }
    leader == g.self_id
}

fn game_ms(g: &Game) -> f64 {
    wall_ms() + g.clock_offset_ms
}

/// The deterministic field minus asteroids this client has shot.
fn live_asteroids(g: &Game) -> Vec<P::Asteroid> {
    let mut v = P::asteroids_at(g.seed, game_ms(g));
    v.retain(|a| !g.destroyed.contains(&a.id));
    v
}

fn respawn(g: &mut Game, t: f64) {
    let (x, y) = safe_spawn(g);
    g.me.x = x;
    g.me.y = y;
    g.me.vx = 0.0;
    g.me.vy = 0.0;
    g.me.angle = rand01() * TAU as f32;
    g.me.alive = true;
    g.me.bullets.clear();
    g.me.invuln_until = t + P::INVULN_TIME as f64;
    g.me.fire_ready = t;
}

fn safe_spawn(g: &Game) -> (f32, f32) {
    let field = live_asteroids(g);
    let mut best = (P::WORLD_W / 2.0, P::WORLD_H / 2.0);
    let mut best_clear = -1.0f32;
    for _ in 0..48 {
        let x = 120.0 + rand01() * (P::WORLD_W - 240.0);
        let y = 120.0 + rand01() * (P::WORLD_H - 240.0);
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

fn die(g: &mut Game, t: f64, shooter: Option<u16>) {
    if !g.me.alive {
        return;
    }
    g.me.alive = false;
    g.me.thrusting = false;
    g.me.dead_until = t + P::RESPAWN_DELAY as f64;
    // Bullets are intentionally NOT cleared: a shot already in flight when you
    // die still counts, so return fire is fair (fixes points concentrating on
    // whoever happens to land the first hit and erase the other's bullets).
    match shooter {
        Some(s) => {
            g.my_kill_seq += 1;
            let bytes = P::encode_hit(g.self_id, g.my_kill_seq);
            if let Some(tx) = &g.out_tx {
                let _ = tx.unbounded_send(Outbound::Reliable(s, bytes));
            }
            set_status(g, "you were hit!", 1.2);
        }
        None => set_status(g, "asteroid! you crashed", 1.2),
    }
}

fn update(g: &mut Game, dt: f32) {
    let t = now_secs();
    update_remotes(g, dt, t);
    if g.phase != Phase::Arena {
        return;
    }

    let fire_btn_hit = g.input.fire;
    if g.me.alive {
        if let Some((tx, ty)) = g.input.touch {
            g.me.angle = steer_toward(g.me.angle, (ty - g.me.y).atan2(tx - g.me.x), P::TURN_RATE * dt);
        } else {
            if g.input.left {
                g.me.angle -= P::TURN_RATE * dt;
            }
            if g.input.right {
                g.me.angle += P::TURN_RATE * dt;
            }
        }
        let thrusting = g.input.thrust || g.input.touch.is_some();
        if thrusting {
            g.me.vx += g.me.angle.cos() * P::THRUST * dt;
            g.me.vy += g.me.angle.sin() * P::THRUST * dt;
        }
        g.me.thrusting = thrusting;
        g.me.vx -= g.me.vx * P::DAMPING * dt;
        g.me.vy -= g.me.vy * P::DAMPING * dt;
        let sp = (g.me.vx * g.me.vx + g.me.vy * g.me.vy).sqrt();
        if sp > P::MAX_SPEED {
            g.me.vx *= P::MAX_SPEED / sp;
            g.me.vy *= P::MAX_SPEED / sp;
        }
        g.me.x = wrap(g.me.x + g.me.vx * dt, P::WORLD_W);
        g.me.y = wrap(g.me.y + g.me.vy * dt, P::WORLD_H);

        if fire_btn_hit && t >= g.me.fire_ready {
            if g.me.bullets.len() < P::MAX_BULLETS {
                let (c, s) = (g.me.angle.cos(), g.me.angle.sin());
                g.me.bullets.push(LocalBullet {
                    x: wrap(g.me.x + c * P::SHIP_RADIUS, P::WORLD_W),
                    y: wrap(g.me.y + s * P::SHIP_RADIUS, P::WORLD_H),
                    vx: g.me.vx + c * P::BULLET_SPEED,
                    vy: g.me.vy + s * P::BULLET_SPEED,
                    born: t,
                });
            }
            g.me.fire_ready = t + P::FIRE_COOLDOWN as f64;
        }
    } else if t >= g.me.dead_until {
        respawn(g, t);
    }

    g.me.bullets.retain(|b| (t - b.born) < P::BULLET_LIFE as f64);
    for b in &mut g.me.bullets {
        b.x = wrap(b.x + b.vx * dt, P::WORLD_W);
        b.y = wrap(b.y + b.vy * dt, P::WORLD_H);
    }

    let field = live_asteroids(g);

    // bullets destroy asteroids (locally; no points, only kills score)
    if !g.me.bullets.is_empty() {
        let mut hit_ids: Vec<u64> = Vec::new();
        g.me.bullets.retain(|b| {
            for a in &field {
                if hit_ids.contains(&a.id) {
                    continue;
                }
                if (a.x - b.x).powi(2) + (a.y - b.y).powi(2) < (a.radius + P::BULLET_RADIUS).powi(2) {
                    hit_ids.push(a.id);
                    return false;
                }
            }
            true
        });
        for id in &hit_ids {
            g.destroyed.insert(*id);
        }
    }
    let old_sec = ((game_ms(g) / 1000.0) as u64).saturating_sub(20);
    g.destroyed.retain(|id| (id >> 8) >= old_sec);

    // remove my bullets that visually strike a live, vulnerable ship (cosmetic;
    // the kill is still decided by the victim and reported over the HIT channel).
    if !g.me.bullets.is_empty() {
        let remotes = &g.remotes;
        g.me.bullets.retain(|b| {
            !remotes.values().any(|r| {
                r.alive && !r.invuln && tor_dist(b.x, b.y, r.px, r.py) < P::SHIP_RADIUS + P::BULLET_RADIUS
            })
        });
    }

    // collisions
    if g.me.alive && t >= g.me.invuln_until {
        let (mx, my) = (g.me.x, g.me.y);
        let mut crash = false;
        for a in &field {
            if ((a.x - mx).powi(2) + (a.y - my).powi(2)).sqrt() < a.radius + P::SHIP_RADIUS {
                crash = true;
                break;
            }
        }
        if crash {
            die(g, t, None);
        }
    }
    if g.me.alive && t >= g.me.invuln_until {
        let (mx, my) = (g.me.x, g.me.y);
        let mut killer = None;
        'outer: for (id, r) in &g.remotes {
            let age = ((t - r.last_recv) as f32).min(0.4);
            for b in &r.bullets {
                if tor_dist(b.x + b.vx * age, b.y + b.vy * age, mx, my) < P::SHIP_RADIUS + P::BULLET_RADIUS {
                    killer = Some(*id);
                    break 'outer;
                }
            }
        }
        if let Some(k) = killer {
            die(g, t, Some(k));
        }
    }

    // outgoing
    g.send_acc += dt as f64;
    g.clock_acc += dt as f64;
    if g.send_acc >= 1.0 / P::SEND_HZ {
        broadcast_state(g, t);
        g.send_acc = 0.0;
    }
    if g.clock_acc >= 1.0 / P::CLOCK_HZ {
        if is_leader(g) {
            let bytes = P::encode_clock(game_ms(g));
            if let Some(tx) = &g.out_tx {
                let _ = tx.unbounded_send(Outbound::Broadcast(bytes));
            }
        }
        g.clock_acc = 0.0;
    }
}

fn broadcast_state(g: &mut Game, t: f64) {
    let bullets: Vec<P::Bullet> = g
        .me
        .bullets
        .iter()
        .take(P::MAX_BULLETS)
        .map(|b| P::Bullet { x: b.x, y: b.y, vx: b.vx, vy: b.vy })
        .collect();
    let seq = g.send_seq;
    g.send_seq = g.send_seq.wrapping_add(1);
    let bytes = P::encode_state(
        g.me.alive,
        g.me.thrusting,
        g.me.alive && t < g.me.invuln_until,
        seq,
        g.me.x, g.me.y, g.me.angle, g.me.vx, g.me.vy,
        g.my_score,
        &bullets,
        &g.name,
    );
    if let Some(tx) = &g.out_tx {
        let _ = tx.unbounded_send(Outbound::Broadcast(bytes));
    }
}

fn update_remotes(g: &mut Game, dt: f32, t: f64) {
    let mut drop = Vec::new();
    for (id, r) in g.remotes.iter_mut() {
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
        r.pa = steer_toward(r.pa, r.angle, tor_delta(r.angle - r.pa, TAU as f32).abs() * k);
    }
    for id in drop {
        g.remotes.remove(&id);
    }
}

// ---- rendering (Canvas 2D) -------------------------------------------------

fn fill(ctx: &CanvasRenderingContext2d, style: &str) {
    ctx.set_fill_style_str(style);
}
fn stroke_style(ctx: &CanvasRenderingContext2d, style: &str) {
    ctx.set_stroke_style_str(style);
}

fn render(g: &mut Game) {
    let dpr = window().device_pixel_ratio();
    let cw = (g.canvas.client_width() as f64 * dpr).round();
    let ch = (g.canvas.client_height() as f64 * dpr).round();
    if g.canvas.width() as f64 != cw {
        g.canvas.set_width(cw as u32);
    }
    if g.canvas.height() as f64 != ch {
        g.canvas.set_height(ch as u32);
    }
    let cw = cw as f32;
    let ch = ch as f32;
    g.scale = (cw / P::WORLD_W).min(ch / P::WORLD_H);
    g.off_x = (cw - P::WORLD_W * g.scale) / 2.0;
    g.off_y = (ch - P::WORLD_H * g.scale) / 2.0;

    let ctx = g.ctx.clone();
    fill(&ctx, "#05060d");
    ctx.fill_rect(0.0, 0.0, cw as f64, ch as f64);

    stroke_style(&ctx, "#1b2440");
    ctx.set_line_width(2.0);
    ctx.stroke_rect(sx(g, 0.0) as f64, sy(g, 0.0) as f64, (P::WORLD_W * g.scale) as f64, (P::WORLD_H * g.scale) as f64);

    for a in live_asteroids(g) {
        draw_asteroid(g, &a);
    }

    let t = now_secs();
    // All shared borrows of `g` here (remotes + the draw_* calls), so no clone.
    for (id, r) in &g.remotes {
        if r.alive {
            let col = format!("hsl({} 70% 60%)", hue(*id));
            for_each_wrap(r.px, r.py, |x, y| draw_ship(g, x, y, r.pa, &col, r.thrusting, r.invuln));
            let label = format!("{} · {}", r.name, r.score);
            draw_label(g, r.px, r.py, &label);
        }
        let age = ((t - r.last_recv) as f32).min(0.4);
        let bc = format!("hsl({} 90% 70%)", hue(*id));
        for b in &r.bullets {
            draw_bullet(g, wrap(b.x + b.vx * age, P::WORLD_W), wrap(b.y + b.vy * age, P::WORLD_H), &bc);
        }
    }

    if g.phase == Phase::Arena {
        let bullets: Vec<(f32, f32)> = g.me.bullets.iter().map(|b| (b.x, b.y)).collect();
        for (bx, by) in bullets {
            draw_bullet(g, bx, by, "#eaf2ff");
        }
        if g.me.alive {
            let inv = t < g.me.invuln_until;
            let (mx, my, ma, thr) = (g.me.x, g.me.y, g.me.angle, g.me.thrusting);
            for_each_wrap(mx, my, |x, y| draw_ship(g, x, y, ma, "#7fe9ff", thr, inv));
        }
    }

    draw_hud(g);
}

fn sx(g: &Game, x: f32) -> f32 {
    g.off_x + x * g.scale
}
fn sy(g: &Game, y: f32) -> f32 {
    g.off_y + y * g.scale
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

fn draw_ship(g: &Game, wx: f32, wy: f32, ang: f32, color: &str, thrust: bool, invuln: bool) {
    let ctx = &g.ctx;
    ctx.save();
    ctx.translate(sx(g, wx) as f64, sy(g, wy) as f64).ok();
    ctx.rotate(ang as f64).ok();
    ctx.scale(g.scale as f64, g.scale as f64).ok();
    if thrust {
        let flick = 13.0 + rand01() as f64 * 8.0;
        fill(ctx, "rgba(255,170,60,0.9)");
        ctx.begin_path();
        ctx.move_to(-7.0, 0.0);
        ctx.line_to(-flick, 5.0);
        ctx.line_to(-flick, -5.0);
        ctx.close_path();
        ctx.fill();
    }
    ctx.begin_path();
    let pts = [(20.0, 0.0), (-14.0, 12.0), (-7.0, 0.0), (-14.0, -12.0)];
    ctx.move_to(pts[0].0, pts[0].1);
    for p in &pts[1..] {
        ctx.line_to(p.0, p.1);
    }
    ctx.close_path();
    fill(ctx, color);
    ctx.set_global_alpha(if invuln { 0.4 + 0.3 * (now_secs() * 11.0).sin() } else { 1.0 });
    ctx.fill();
    ctx.set_global_alpha(1.0);
    ctx.set_line_width(1.5);
    stroke_style(ctx, "rgba(255,255,255,0.6)");
    ctx.stroke();
    ctx.restore();
}

fn draw_bullet(g: &Game, wx: f32, wy: f32, color: &str) {
    let ctx = &g.ctx;
    fill(ctx, color);
    ctx.begin_path();
    ctx.arc(sx(g, wx) as f64, sy(g, wy) as f64, (P::BULLET_RADIUS * g.scale).max(2.0) as f64, 0.0, TAU).ok();
    ctx.fill();
}

fn draw_asteroid(g: &Game, a: &P::Asteroid) {
    let ctx = &g.ctx;
    ctx.save();
    ctx.translate(sx(g, a.x) as f64, sy(g, a.y) as f64).ok();
    ctx.begin_path();
    // Stable irregular outline seeded by `a.shape` (identical on every client,
    // and independent of the live position so the rock does not wobble).
    let n = P::ASTEROID_VERTS;
    for i in 0..=n {
        let th = (i % n) as f64 / n as f64 * TAU;
        let rr = a.radius as f64 * P::asteroid_vertex(a.shape, (i % n) as u32) as f64 * g.scale as f64;
        let (x, y) = (th.cos() * rr, th.sin() * rr);
        if i == 0 {
            ctx.move_to(x, y);
        } else {
            ctx.line_to(x, y);
        }
    }
    ctx.close_path();
    fill(ctx, "#2a2f45");
    ctx.fill();
    ctx.set_line_width(1.5);
    stroke_style(ctx, "#5a6488");
    ctx.stroke();
    ctx.restore();
}

fn draw_label(g: &Game, wx: f32, wy: f32, text: &str) {
    let ctx = &g.ctx;
    ctx.set_font("12px system-ui, sans-serif");
    fill(ctx, "rgba(220,230,255,0.75)");
    ctx.set_text_align("center");
    ctx.fill_text(text, sx(g, wx) as f64, (sy(g, wy) - 22.0 * g.scale.max(0.6)) as f64).ok();
}

fn draw_hud(g: &Game) {
    let ctx = &g.ctx;
    ctx.set_text_align("left");
    fill(ctx, "#cfe3ff");
    ctx.set_font("bold 22px system-ui, sans-serif");
    ctx.fill_text(&format!("SCORE {}", g.my_score), 14.0, 30.0).ok();
    ctx.set_font("16px system-ui, sans-serif");
    let line = format!(
        "players {}   room {}   {}",
        1 + g.remotes.len(),
        g.code,
        if is_leader(g) { "★clock" } else { "" }
    );
    ctx.fill_text(&line, 14.0, 52.0).ok();

    let cw = g.canvas.width() as f64;
    let ch = g.canvas.height() as f64;
    ctx.set_text_align("center");
    match g.phase {
        Phase::Lobby => {
            let here = g.roster.iter().filter(|p| p.1).count();
            fill(ctx, "#7fe9ff");
            // (the HTML lobby overlay also shows this; keep an in-canvas hint)
            let _ = here;
        }
        Phase::Arena if !g.me.alive => {
            fill(ctx, "rgba(255,120,120,0.9)");
            ctx.set_font("bold 28px system-ui, sans-serif");
            ctx.fill_text("respawning…", cw / 2.0, ch / 2.0).ok();
        }
        _ => {}
    }

    if now_secs() < g.status_until && !g.status.is_empty() {
        fill(ctx, "#eaf0ff");
        ctx.set_font("16px system-ui, sans-serif");
        ctx.fill_text(&g.status, cw / 2.0, ch - 70.0).ok();
    }

    // keep the HTML lobby player count fresh
    if g.phase == Phase::Lobby {
        let here = g.roster.iter().filter(|p| p.1).count().max(1);
        if let Some(e) = document().get_element_by_id("lobbyInfo") {
            e.set_text_content(Some(&format!(
                "Room “{}” — you are player {} of up to {}.\n{} pilot(s) in the lobby.",
                g.code, g.self_id, "32", here
            )));
        }
    }
}

// ---- rAF loop --------------------------------------------------------------

fn start_raf(shared: Shared) {
    let f = Rc::new(RefCell::new(None::<Closure<dyn FnMut()>>));
    let g2 = f.clone();
    *g2.borrow_mut() = Some(Closure::wrap(Box::new(move || {
        {
            let mut game = shared.borrow_mut();
            let t = now_secs();
            let dt = ((t - game.last_secs) as f32).min(0.05);
            game.last_secs = t;
            update(&mut game, dt);
            render(&mut game);
        }
        request_animation_frame(f.borrow().as_ref().unwrap());
    }) as Box<dyn FnMut()>));
    request_animation_frame(g2.borrow().as_ref().unwrap());
}

fn request_animation_frame(f: &Closure<dyn FnMut()>) {
    window()
        .request_animation_frame(f.as_ref().unchecked_ref())
        .unwrap();
}
