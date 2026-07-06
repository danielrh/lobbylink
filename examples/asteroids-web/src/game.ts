// DartRoids — browser client. Trust-based multiplayer asteroids on lobbylink.
// Wire-compatible with the Rust example (see ../../PROTOCOL.md).

import { P2PGame } from "./p2p-client.js";
import type { P2PEvent } from "./p2p-client.js";
import * as P from "./protocol.js";

// ---- DOM ------------------------------------------------------------------

const $ = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const canvas = $("view") as HTMLCanvasElement;
const ctx = canvas.getContext("2d")!;
const menu = $("menu");
const lobby = $("lobby");
const fireBtn = $("fire");
const hud = $("hud");
const toast = $("toast");
const lobbyInfo = $("lobbyInfo");

// ---- types ----------------------------------------------------------------

interface LocalBullet { x: number; y: number; vx: number; vy: number; born: number; }

interface Me {
  x: number; y: number; angle: number; vx: number; vy: number;
  alive: boolean; thrusting: boolean;
  deadUntil: number; invulnUntil: number; fireReady: number;
  bullets: LocalBullet[];
}

interface Remote {
  x: number; y: number; angle: number; vx: number; vy: number;
  px: number; py: number; pa: number; // rendered (eased) pose
  alive: boolean; invuln: boolean; thrusting: boolean;
  score: number; name: string;
  bullets: P.Bullet[];
  lastRecv: number; seq: number;
}

// ---- state ----------------------------------------------------------------

type Phase = "menu" | "lobby" | "arena";
let phase: Phase = "menu";

let game: P2PGame | null = null;
let selfId = 0;
let seed = 0;
let myName = "";
let myScore = 0;
let myKillSeq = 0;
let sendSeq = 0;
const seenKills = new Map<number, number>(); // shooterView: victimId -> last killSeq counted
let clockOffsetMs = 0;

const me: Me = {
  x: P.WORLD_W / 2, y: P.WORLD_H / 2, angle: -Math.PI / 2, vx: 0, vy: 0,
  alive: false, thrusting: false, deadUntil: 0, invulnUntil: 0, fireReady: 0, bullets: [],
};
const remotes = new Map<number, Remote>();

const input = { left: false, right: false, thrust: false, fire: false };
const touch = { active: false, wx: 0, wy: 0 };

const nowMs = () => performance.now();
const gameMs = () => Date.now() + clockOffsetMs;

// ---- helpers --------------------------------------------------------------

function wrap(v: number, m: number): number {
  v %= m;
  return v < 0 ? v + m : v;
}
/** shortest signed delta on a wrapped axis of length m */
function torDelta(d: number, m: number): number {
  d %= m;
  if (d > m / 2) d -= m;
  if (d < -m / 2) d += m;
  return d;
}
function torDist(ax: number, ay: number, bx: number, by: number): number {
  const dx = torDelta(ax - bx, P.WORLD_W);
  const dy = torDelta(ay - by, P.WORLD_H);
  return Math.hypot(dx, dy);
}
function steerToward(cur: number, target: number, maxStep: number): number {
  let d = target - cur;
  while (d > Math.PI) d -= 2 * Math.PI;
  while (d < -Math.PI) d += 2 * Math.PI;
  if (Math.abs(d) <= maxStep) return target;
  return cur + Math.sign(d) * maxStep;
}
function hue(id: number): number {
  return (id * 47) % 360;
}

// ---- connect / lobby ------------------------------------------------------

async function resolveServer(configured: string): Promise<string> {
  if (configured) return configured;
  try {
    const cfg = await (await fetch("./config.json")).json();
    if (cfg.wsUrl) return cfg.wsUrl;
  } catch {
    /* not served by the Go server; fall through */
  }
  return "https://pqrstuvw.xyz/lobbylink";
}

async function enterLobby(): Promise<void> {
  const code = ($("code") as HTMLInputElement).value.trim() || "DARTS";
  myName = (($("name") as HTMLInputElement).value.trim() || "player").slice(0, 24);
  const maxPlayers = Math.max(2, Math.min(64, Number(($("maxPlayers") as HTMLInputElement).value) || 64));
  const server = await resolveServer(($("server") as HTMLInputElement).value.trim());
  location.hash = code;
  showToast(`connecting to ${server} …`);
  try {
    game = await P2PGame.connect({
      server, code,
      create: { maxPlayers, waitUntilFull: false },
      storageKey: "dartroids-" + code,
      storage: "session",
    });
  } catch (err) {
    showToast("connect failed: " + String(err));
    return;
  }
  selfId = game.selfId;
  seed = P.seedFromCode(code);
  game.onEvent(onNet);
  menu.style.display = "none";
  lobby.style.display = "flex";
  phase = "lobby";
  updateLobby();
  requestAnimationFrame(frame);
}

function updateLobby(): void {
  if (!game) return;
  const here = game.players.filter((p) => p.occupied).length;
  lobbyInfo.textContent =
    `Room “${game.code}” — you are player ${selfId} of up to ${game.maxPlayers}.\n` +
    `${here} pilot${here === 1 ? "" : "s"} in the lobby.`;
}

function launch(): void {
  lobby.style.display = "none";
  phase = "arena";
  respawn(nowMs());
  showToast("launched — arrows/drag to fly, space/tap to fire", 2500);
}

function leave(): void {
  game?.close();
  game = null;
  remotes.clear();
  phase = "menu";
  lobby.style.display = "none";
  menu.style.display = "flex";
}

// ---- networking -----------------------------------------------------------

function onNet(ev: P2PEvent): void {
  switch (ev.type) {
    case "message": {
      const msg = P.decode(ev.data);
      if (!msg) return;
      if (msg.kind === "state") onState(ev.from, msg);
      else if (msg.kind === "clock") onClock(msg.gameMs);
      else if (msg.kind === "hit") onHit(ev.from, msg);
      break;
    }
    case "player-left":
      remotes.delete(ev.playerId);
      updateLobby();
      break;
    case "player-joined":
    case "player-rejoined":
    case "player-replaced":
      updateLobby();
      break;
    case "signaling-closed":
      showToast("signaling closed: " + ev.code);
      break;
    default:
      break;
  }
}

function onState(from: number, s: P.StateMsg): void {
  let r = remotes.get(from);
  if (!r) {
    r = {
      x: s.x, y: s.y, angle: s.angle, vx: s.vx, vy: s.vy,
      px: s.x, py: s.y, pa: s.angle,
      alive: s.alive, invuln: s.invuln, thrusting: s.thrusting,
      score: s.score, name: s.name, bullets: s.bullets,
      lastRecv: nowMs(), seq: s.seq,
    };
    remotes.set(from, r);
    return;
  }
  if (!P.seqNewer(s.seq, r.seq)) return;
  r.x = s.x; r.y = s.y; r.angle = s.angle; r.vx = s.vx; r.vy = s.vy;
  r.alive = s.alive; r.invuln = s.invuln; r.thrusting = s.thrusting;
  r.score = s.score; r.name = s.name; r.bullets = s.bullets;
  r.lastRecv = nowMs(); r.seq = s.seq;
}

function onClock(leaderMs: number): void {
  const target = leaderMs - Date.now();
  if (Math.abs(target - clockOffsetMs) > 1000) clockOffsetMs = target;
  else clockOffsetMs += (target - clockOffsetMs) * 0.25;
}

function onHit(from: number, m: P.HitMsg): void {
  // Someone we shot reports they died. Count once per (victim, killSeq).
  if (seenKills.get(from) === m.killSeq) return;
  seenKills.set(from, m.killSeq);
  myScore += 1;
  showToast(`you eliminated ${remotes.get(from)?.name ?? "player " + from}! +1`, 1500);
}

function isLeader(): boolean {
  if (!game) return false;
  let leader = selfId;
  for (const p of game.players) {
    if (p.occupied && p.connected && p.id < leader) leader = p.id;
  }
  return leader === selfId;
}

function broadcastState(): void {
  if (!game || phase !== "arena") return;
  const p = nowMs();
  const bullets = me.bullets.slice(0, P.MAX_BULLETS).map((b) => ({ x: b.x, y: b.y, vx: b.vx, vy: b.vy }));
  const bytes = P.encodeState({
    alive: me.alive,
    thrusting: me.thrusting,
    invuln: me.alive && p < me.invulnUntil,
    seq: sendSeq++ & 0xffff,
    x: me.x, y: me.y, angle: me.angle, vx: me.vx, vy: me.vy,
    score: myScore, bullets, name: myName,
  });
  game.broadcastBestEffort(bytes);
}

function broadcastClock(): void {
  if (!game || !isLeader()) return;
  game.broadcastBestEffort(P.encodeClock(gameMs()));
}

// ---- simulation -----------------------------------------------------------

function respawn(p: number): void {
  const spot = safeSpawn();
  me.x = spot.x; me.y = spot.y;
  me.vx = 0; me.vy = 0;
  me.angle = Math.random() * Math.PI * 2;
  me.alive = true;
  me.bullets = [];
  me.invulnUntil = p + P.INVULN_TIME * 1000;
  me.fireReady = p;
}

function safeSpawn(): { x: number; y: number } {
  const field = P.asteroidsAt(seed, gameMs());
  let best = { x: P.WORLD_W / 2, y: P.WORLD_H / 2 };
  let bestClear = -1;
  for (let i = 0; i < 48; i++) {
    const x = 120 + Math.random() * (P.WORLD_W - 240);
    const y = 120 + Math.random() * (P.WORLD_H - 240);
    let clear = Infinity;
    for (const a of field) clear = Math.min(clear, Math.hypot(a.x - x, a.y - y) - a.radius);
    if (clear > bestClear) { bestClear = clear; best = { x, y }; }
    if (clear > 220) break;
  }
  return best;
}

function die(p: number, shooter: number | null): void {
  if (!me.alive) return;
  me.alive = false;
  me.thrusting = false;
  me.deadUntil = p + P.RESPAWN_DELAY * 1000;
  me.bullets = [];
  if (shooter !== null && game) {
    myKillSeq++;
    game.sendReliable(shooter, P.encodeHit(selfId, myKillSeq)).catch(() => {});
    showToast("you were hit!", 1200);
  } else {
    showToast("asteroid! you crashed", 1200);
  }
}

function update(dt: number): void {
  const p = nowMs();
  updateRemotes(dt, p);
  if (phase !== "arena") return;

  if (me.alive) {
    if (touch.active) {
      me.angle = steerToward(me.angle, Math.atan2(touch.wy - me.y, touch.wx - me.x), P.TURN_RATE * dt);
    } else {
      if (input.left) me.angle -= P.TURN_RATE * dt;
      if (input.right) me.angle += P.TURN_RATE * dt;
    }
    const thrusting = input.thrust || touch.active;
    if (thrusting) {
      me.vx += Math.cos(me.angle) * P.THRUST * dt;
      me.vy += Math.sin(me.angle) * P.THRUST * dt;
    }
    me.thrusting = thrusting;
    me.vx -= me.vx * P.DAMPING * dt;
    me.vy -= me.vy * P.DAMPING * dt;
    const sp = Math.hypot(me.vx, me.vy);
    if (sp > P.MAX_SPEED) { me.vx *= P.MAX_SPEED / sp; me.vy *= P.MAX_SPEED / sp; }
    me.x = wrap(me.x + me.vx * dt, P.WORLD_W);
    me.y = wrap(me.y + me.vy * dt, P.WORLD_H);

    if (input.fire && p >= me.fireReady) {
      if (me.bullets.length < P.MAX_BULLETS) {
        me.bullets.push({
          x: wrap(me.x + Math.cos(me.angle) * P.SHIP_RADIUS, P.WORLD_W),
          y: wrap(me.y + Math.sin(me.angle) * P.SHIP_RADIUS, P.WORLD_H),
          vx: me.vx + Math.cos(me.angle) * P.BULLET_SPEED,
          vy: me.vy + Math.sin(me.angle) * P.BULLET_SPEED,
          born: p,
        });
      }
      me.fireReady = p + P.FIRE_COOLDOWN * 1000;
    }
  } else if (p >= me.deadUntil) {
    respawn(p);
  }

  me.bullets = me.bullets.filter((b) => (p - b.born) / 1000 < P.BULLET_LIFE);
  for (const b of me.bullets) {
    b.x = wrap(b.x + b.vx * dt, P.WORLD_W);
    b.y = wrap(b.y + b.vy * dt, P.WORLD_H);
  }

  // collisions — only when vulnerable
  if (me.alive && p >= me.invulnUntil) {
    for (const a of P.asteroidsAt(seed, gameMs())) {
      if (Math.hypot(a.x - me.x, a.y - me.y) < a.radius + P.SHIP_RADIUS) { die(p, null); break; }
    }
  }
  if (me.alive && p >= me.invulnUntil) {
    outer: for (const [id, r] of remotes) {
      const age = Math.min((p - r.lastRecv) / 1000, 0.4);
      for (const b of r.bullets) {
        const bx = b.x + b.vx * age, by = b.y + b.vy * age;
        if (torDist(bx, by, me.x, me.y) < P.SHIP_RADIUS + P.BULLET_RADIUS) { die(p, id); break outer; }
      }
    }
  }
}

function updateRemotes(dt: number, p: number): void {
  for (const [id, r] of remotes) {
    if (p - r.lastRecv > 5000) { remotes.delete(id); continue; }
    const age = Math.min((p - r.lastRecv) / 1000, 0.4);
    const tx = wrap(r.x + r.vx * age, P.WORLD_W);
    const ty = wrap(r.y + r.vy * age, P.WORLD_H);
    const k = Math.min(1, dt * 12);
    r.px = wrap(r.px + torDelta(tx - r.px, P.WORLD_W) * k, P.WORLD_W);
    r.py = wrap(r.py + torDelta(ty - r.py, P.WORLD_H) * k, P.WORLD_H);
    r.pa = steerToward(r.pa, r.angle, Math.abs(torDelta(r.angle - r.pa, 2 * Math.PI)) * k);
  }
}

// ---- rendering ------------------------------------------------------------

let scale = 1, offX = 0, offY = 0;

function resize(): void {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth, h = canvas.clientHeight;
  if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
    canvas.width = Math.round(w * dpr);
    canvas.height = Math.round(h * dpr);
  }
  scale = Math.min(canvas.width / P.WORLD_W, canvas.height / P.WORLD_H);
  offX = (canvas.width - P.WORLD_W * scale) / 2;
  offY = (canvas.height - P.WORLD_H * scale) / 2;
}
const sx = (x: number) => offX + x * scale;
const sy = (y: number) => offY + y * scale;

function screenToWorld(clientX: number, clientY: number): { x: number; y: number } {
  const rect = canvas.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  const px = (clientX - rect.left) * dpr, py = (clientY - rect.top) * dpr;
  return { x: (px - offX) / scale, y: (py - offY) / scale };
}

function render(): void {
  resize();
  ctx.fillStyle = "#05060d";
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  // world border
  ctx.strokeStyle = "#1b2440";
  ctx.lineWidth = 2;
  ctx.strokeRect(sx(0), sy(0), P.WORLD_W * scale, P.WORLD_H * scale);
  drawStars();

  for (const a of P.asteroidsAt(seed, gameMs())) drawAsteroid(a);

  for (const [id, r] of remotes) {
    if (!r.alive) continue;
    forEachWrap(r.px, r.py, (x, y) => drawShip(x, y, r.pa, `hsl(${hue(id)} 70% 60%)`, r.thrusting, r.invuln));
    drawLabel(r.px, r.py, `${r.name} · ${r.score}`);
    const age = Math.min((nowMs() - r.lastRecv) / 1000, 0.4);
    for (const b of r.bullets) drawBullet(wrap(b.x + b.vx * age, P.WORLD_W), wrap(b.y + b.vy * age, P.WORLD_H), `hsl(${hue(id)} 90% 70%)`);
  }

  if (phase === "arena") {
    for (const b of me.bullets) drawBullet(b.x, b.y, "#eaf2ff");
    if (me.alive) {
      const inv = nowMs() < me.invulnUntil;
      forEachWrap(me.x, me.y, (x, y) => drawShip(x, y, me.angle, "#7fe9ff", me.thrusting, inv));
    }
  }
  drawHud();
}

/** invoke draw at the base position and any wrap-ghost within view */
function forEachWrap(wx: number, wy: number, draw: (x: number, y: number) => void): void {
  for (const dx of [-P.WORLD_W, 0, P.WORLD_W]) {
    for (const dy of [-P.WORLD_H, 0, P.WORLD_H]) {
      const x = wx + dx, y = wy + dy;
      if (x > -40 && x < P.WORLD_W + 40 && y > -40 && y < P.WORLD_H + 40) draw(x, y);
    }
  }
}

function drawShip(wx: number, wy: number, ang: number, color: string, thrust: boolean, invuln: boolean): void {
  const pts: [number, number][] = [[20, 0], [-14, 12], [-7, 0], [-14, -12]];
  ctx.save();
  ctx.translate(sx(wx), sy(wy));
  ctx.rotate(ang);
  ctx.scale(scale, scale);
  if (thrust) {
    ctx.fillStyle = "rgba(255,170,60,0.9)";
    ctx.beginPath();
    ctx.moveTo(-7, 0);
    ctx.lineTo(-13 - Math.random() * 8, 5);
    ctx.lineTo(-13 - Math.random() * 8, -5);
    ctx.closePath();
    ctx.fill();
  }
  ctx.beginPath();
  pts.forEach(([x, y], i) => (i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)));
  ctx.closePath();
  ctx.fillStyle = color;
  ctx.globalAlpha = invuln ? 0.4 + 0.3 * Math.sin(nowMs() / 90) : 1;
  ctx.fill();
  ctx.globalAlpha = 1;
  ctx.lineWidth = 1.5;
  ctx.strokeStyle = "rgba(255,255,255,0.6)";
  ctx.stroke();
  ctx.restore();
}

function drawBullet(wx: number, wy: number, color: string): void {
  ctx.fillStyle = color;
  ctx.beginPath();
  ctx.arc(sx(wx), sy(wy), Math.max(2, P.BULLET_RADIUS * scale), 0, Math.PI * 2);
  ctx.fill();
}

function drawAsteroid(a: P.Asteroid): void {
  ctx.save();
  ctx.translate(sx(a.x), sy(a.y));
  ctx.beginPath();
  const spikes = 9;
  for (let i = 0; i <= spikes; i++) {
    const t = (i / spikes) * Math.PI * 2;
    // deterministic lumpiness from position so it looks like a rock
    const lump = 0.82 + 0.18 * Math.sin(t * 3 + a.x * 0.05) * Math.cos(t * 2 + a.y * 0.05);
    const rr = a.radius * lump * scale;
    const x = Math.cos(t) * rr, y = Math.sin(t) * rr;
    if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
  }
  ctx.closePath();
  ctx.fillStyle = "#2a2f45";
  ctx.fill();
  ctx.lineWidth = 1.5;
  ctx.strokeStyle = "#5a6488";
  ctx.stroke();
  ctx.restore();
}

function drawLabel(wx: number, wy: number, text: string): void {
  ctx.font = "12px system-ui, sans-serif";
  ctx.fillStyle = "rgba(220,230,255,0.75)";
  ctx.textAlign = "center";
  ctx.fillText(text, sx(wx), sy(wy) - 22 * Math.max(0.6, scale));
}

let starSeed: { x: number; y: number }[] | null = null;
function drawStars(): void {
  if (!starSeed) {
    const r = (() => { let s = 12345; return () => { s = (s * 1103515245 + 12345) & 0x7fffffff; return s / 0x7fffffff; }; })();
    starSeed = Array.from({ length: 120 }, () => ({ x: r() * P.WORLD_W, y: r() * P.WORLD_H }));
  }
  ctx.fillStyle = "rgba(255,255,255,0.35)";
  for (const s of starSeed) ctx.fillRect(sx(s.x), sy(s.y), 1.5, 1.5);
}

function drawHud(): void {
  const players = 1 + remotes.size;
  hud.textContent =
    `SCORE ${myScore}\n` +
    `players ${players}   room ${game?.code ?? ""}   ${isLeader() ? "★clock" : ""}`;

  if (phase === "arena" && !me.alive) {
    ctx.fillStyle = "rgba(255,120,120,0.9)";
    ctx.font = "bold 28px system-ui, sans-serif";
    ctx.textAlign = "center";
    ctx.fillText("respawning…", canvas.width / 2, canvas.height / 2);
  }
}

let toastUntil = 0;
function showToast(text: string, ms = 2200): void {
  toast.textContent = text;
  toast.style.display = "block";
  toastUntil = nowMs() + ms;
}

// ---- main loops -----------------------------------------------------------

let last = nowMs();
let sendAcc = 0, clockAcc = 0;
function frame(): void {
  const p = nowMs();
  const dt = Math.min(0.05, (p - last) / 1000);
  last = p;
  update(dt);
  sendAcc += dt; clockAcc += dt;
  if (sendAcc >= 1 / P.SEND_HZ) { broadcastState(); sendAcc = 0; }
  if (clockAcc >= 1 / P.CLOCK_HZ) { broadcastClock(); clockAcc = 0; }
  if (phase === "lobby") updateLobby();
  if (p > toastUntil) toast.style.display = "none";
  render();
  if (phase !== "menu") requestAnimationFrame(frame);
}

// ---- input ----------------------------------------------------------------

function keyDown(e: KeyboardEvent): void {
  switch (e.key) {
    case "ArrowLeft": case "a": case "A": input.left = true; break;
    case "ArrowRight": case "d": case "D": input.right = true; break;
    case "ArrowUp": case "w": case "W": input.thrust = true; break;
    case " ": case "ArrowDown": input.fire = true; break;
    default: return;
  }
  e.preventDefault();
}
function keyUp(e: KeyboardEvent): void {
  switch (e.key) {
    case "ArrowLeft": case "a": case "A": input.left = false; break;
    case "ArrowRight": case "d": case "D": input.right = false; break;
    case "ArrowUp": case "w": case "W": input.thrust = false; break;
    case " ": case "ArrowDown": input.fire = false; break;
    default: return;
  }
  e.preventDefault();
}

function pointerFromEvent(e: PointerEvent | Touch): void {
  const w = screenToWorld(e.clientX, e.clientY);
  touch.active = true; touch.wx = w.x; touch.wy = w.y;
}

// ---- wiring ---------------------------------------------------------------

function init(): void {
  const hashCode = decodeURIComponent(location.hash.replace(/^#/, ""));
  if (hashCode) ($("code") as HTMLInputElement).value = hashCode;

  $("enter").addEventListener("click", () => void enterLobby());
  $("launch").addEventListener("click", launch);
  $("leave").addEventListener("click", leave);
  $("copy").addEventListener("click", () => {
    navigator.clipboard?.writeText(location.href).then(() => showToast("link copied", 1200));
  });

  window.addEventListener("keydown", keyDown);
  window.addEventListener("keyup", keyUp);

  // touch / mouse steering on the canvas
  canvas.addEventListener("pointerdown", (e) => { if (e.pointerType !== "mouse" || e.buttons) pointerFromEvent(e); });
  canvas.addEventListener("pointermove", (e) => { if (touch.active) pointerFromEvent(e); });
  const endTouch = () => { touch.active = false; };
  canvas.addEventListener("pointerup", endTouch);
  canvas.addEventListener("pointercancel", endTouch);
  canvas.addEventListener("pointerleave", endTouch);

  // fire button (mobile + mouse)
  const fireOn = (e: Event) => { input.fire = true; e.preventDefault(); };
  const fireOff = (e: Event) => { input.fire = false; e.preventDefault(); };
  fireBtn.addEventListener("pointerdown", fireOn);
  fireBtn.addEventListener("pointerup", fireOff);
  fireBtn.addEventListener("pointerleave", fireOff);
  fireBtn.addEventListener("pointercancel", fireOff);
}

init();
