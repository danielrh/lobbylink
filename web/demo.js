// Demo driver for the embedded index page. Uses the browser client
// bundle once the TypeScript build has produced web/p2p-client.js.
import * as P2P from "./p2p-client.js";

const logEl = document.getElementById("log");
function log(...args) {
  logEl.textContent += args.map(a => typeof a === "string" ? a : JSON.stringify(a)).join(" ") + "\n";
  logEl.scrollTop = logEl.scrollHeight;
}

// Works at any mount point (/, /lobbylink/, ...): ask the server for its
// wsUrl, falling back to this page's own directory.
async function serverUrl() {
  try {
    const cfg = await (await fetch("./config.json")).json();
    if (cfg.wsUrl) return cfg.wsUrl;
  } catch {
    // fall through
  }
  return new URL(".", location.href).href;
}

let game = null;

function setConnected(on) {
  document.getElementById("connect").disabled = on;
  document.getElementById("leave").disabled = !on;
  document.getElementById("sendReliable").disabled = !on;
  document.getElementById("sendBestEffort").disabled = !on;
}

document.getElementById("connect").addEventListener("click", async () => {
  if (!P2P.P2PGame) {
    log("p2p-client.js is a placeholder; build clients/ts first.");
    return;
  }
  const code = document.getElementById("code").value.trim();
  try {
    game = await P2P.P2PGame.connect({
      server: await serverUrl(),
      code,
      create: {
        maxPlayers: Number(document.getElementById("maxPlayers").value),
        waitUntilFull: document.getElementById("waitUntilFull").checked,
      },
      storageKey: "p2p-demo-" + code,
      // Per-tab storage: several tabs in one browser must be separate
      // players, not resume (and so supersede) each other's slot.
      storage: "session",
    });
    log(`joined room ${code} as player ${game.selfId}/${game.maxPlayers}`);
    setConnected(true);
    game.onEvent(ev => {
      if (ev.type === "message") {
        log(`msg from ${ev.from} (${ev.kind}): ${new TextDecoder().decode(ev.data)}`);
      } else {
        log("event:", ev);
      }
    });
  } catch (err) {
    log("connect failed:", String(err));
  }
});

document.getElementById("leave").addEventListener("click", () => {
  if (game) { game.close(); game = null; }
  setConnected(false);
  log("left room");
});

function payload() {
  return new TextEncoder().encode(document.getElementById("message").value);
}

document.getElementById("sendReliable").addEventListener("click", async () => {
  if (!game) return;
  for (let i = 0; i < game.maxPlayers; i++) {
    if (i === game.selfId) continue;
    try { await game.sendReliable(i, payload()); log(`reliable -> ${i}`); }
    catch (err) { log(`reliable -> ${i} failed: ${String(err)}`); }
  }
});

document.getElementById("sendBestEffort").addEventListener("click", () => {
  if (!game) return;
  game.broadcastBestEffort(payload());
  log("broadcast best-effort");
});
