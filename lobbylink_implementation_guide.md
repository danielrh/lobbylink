# P2P Lobby + WebRTC DataChannel System: Compact Implementation Handoff

Audience: highly capable implementer. Goal: build a Go server, TypeScript browser client library, and Rust native client library for instant lobby-based multiplayer connectivity via WebRTC DataChannels, with coturn fallback. Keep the server boring, durable, low-dependency, and deployable either behind Apache or directly on `:4443`.

## 0. Core Decision Summary

- Server language: **Go**.
- Server role: **HTTPS/HTTP static server + lobby manager + WebRTC signaling relay + TURN credential issuer**.
- Server does **not** implement WebRTC peer connections.
- Browser TypeScript client uses native WebRTC APIs: `RTCPeerConnection`, `RTCDataChannel`.
- Rust client uses a well-maintained WebRTC crate plus WebSocket client.
- Signaling transport: **WebSocket**.
- TURN: **external coturn**, not embedded. Go server and coturn share a secret; Go server mints temporary TURN credentials.
- Data path: WebRTC DataChannels, direct when possible, relayed through TURN when needed.
- Deployment Method 1: Apache owns public `:443`, reverse-proxies to Go on `127.0.0.1:8787`.
- Deployment Method 2: Go binary owns public `:4443` with copied Let’s Encrypt certs; Apache may keep `:443` or be ignored.
- Recommended server dependency set: stdlib + `github.com/coder/websocket`.
- Recommended Rust client deps: `webrtc`, `tokio`, `tokio-tungstenite`, `serde`, `serde_json`, `futures`, `bytes`, `url`, `thiserror`.
- Recommended TS client: no runtime deps if possible; package as npm module and also emit browser bundle as embedded file.

---

## 1. Repository Layout

```text
p2p-lobby/
  go.mod
  cmd/p2p-lobby-server/main.go
  internal/config/config.go
  internal/protocol/protocol.go
  internal/lobby/lobby.go
  internal/server/http.go
  internal/server/ws.go
  internal/turn/credentials.go
  internal/static/static.go
  web/
    index.html
    p2p-client.js              # built artifact served by Go embed
    demo.js
  clients/ts/
    package.json
    tsconfig.json
    src/index.ts
    src/protocol.ts
    src/client.ts
    src/peer.ts
    src/framing.ts
    src/errors.ts
  clients/rust/
    Cargo.toml
    src/lib.rs
    src/protocol.rs
    src/client.rs
    src/peer.rs
    src/framing.rs
    examples/basic.rs
  scripts/
    install-common.sh
    copy-certs.sh
    setup-coturn.sh
    setup-apache-proxy.sh
    setup-direct-4443.sh
    install-systemd-service.sh
  deploy/
    p2p-lobby.service.template
    apache-p2p-lobby.conf.template
    turnserver.conf.template
```

---

## 2. Server Responsibilities

### 2.1 HTTP routes

Same binary supports all modes.

```text
GET  /                  embedded static demo/index
GET  /p2p-client.js      embedded browser client bundle
GET  /healthz            returns 200 OK
GET  /config.json        optional public config: ws URL, mode, version
WS   /ws                 lobby + signaling protocol
```

Support optional base path for Apache subpath deployments, but default `/` is enough for MVP. If static games are hosted on other HTTPS origins, add CORS only for allowlisted origins on HTTP/module/static/config endpoints; WebSocket uses `Origin` validation, not CORS.

### 2.2 CLI flags

Stdlib `flag` is enough; config file preferred, CLI overrides.

```text
--config /etc/p2p-lobby/config.toml
--listen-http 127.0.0.1:8787 | --listen-https :4443
--cert /path/fullchain.pem --key /path/privkey.pem
--public-url https://example.com[:port]
--allowed-origin https://example.com[:port]   # repeatable/comma-separated
--behind-proxy --trusted-proxy 127.0.0.1
--turn-enabled --turn-realm example.com --turn-shared-secret-file /var/lib/p2p-lobby/turn-secret
--turn-ttl 3600s --turn-urls stun:...,turn:...,turns:...
--room-empty-ttl 300s --room-max-ttl 24h --max-rooms 10000
--claim-after 40s
--log-level info
```

Rules: HTTPS mode requires cert/key; proxy mode trusts `X-Forwarded-*` only from trusted proxy IPs; WebSocket `Origin` must match allowlist except explicit local/dev/native-no-origin modes.

### 2.3 Config file + static-host allowlist

Browsers may host static games on `graphics.stanford.edu` / GitHub Pages and connect to `wss://pqrstuvw.xyz[:port]/ws` if TLS is valid and request `Origin` is allowlisted. Config supports global origins plus optional per-app policies.

Minimal `/etc/p2p-lobby/config.toml`:

```toml
[server]
public_url = "https://pqrstuvw.xyz:4443" # Apache mode: "https://pqrstuvw.xyz"
listen_http = ""                     # Apache/dev: "127.0.0.1:8787"
listen_https = ":4443"               # Apache/dev: ""
cert = "/var/lib/p2p-lobby/certs/fullchain.pem"
key = "/var/lib/p2p-lobby/certs/privkey.pem"
behind_proxy = false
trusted_proxies = ["127.0.0.1"]

[security]
allowed_origins = [
  "https://pqrstuvw.xyz", "https://pqrstuvw.xyz:4443",
  "https://graphics.stanford.edu",
  "https://anderrh.github.io", "https://danielrh.github.io",
  "http://localhost:5173", "http://127.0.0.1:5173"
]
max_ws_message_bytes = 1048576

[turn]
enabled = true
realm = "pqrstuvw.xyz"
shared_secret_file = "/var/lib/p2p-lobby/turn-secret"
ttl = "3600s"
urls = ["stun:pqrstuvw.xyz:3478", "turn:pqrstuvw.xyz:3478?transport=udp", "turn:pqrstuvw.xyz:3478?transport=tcp", "turns:pqrstuvw.xyz:5349?transport=tcp"]

[rooms]
empty_ttl = "300s"
max_ttl = "24h"
max_rooms = 10000
max_players_hard = 32
claim_after = "40s" # sole reclaim timer; silence-based, no required heartbeat
default_reconnect_policy = "token-or-claim-after-timeout"
```

Optional app policies:

```toml
[[apps]]
id = "anderrh-github"
allowed_origins = ["https://anderrh.github.io"]
max_players_max = 16
allow_turn = true
```

If `apps` exist, clients may send `appId`; validate app exists, `Origin` is globally or app-allowlisted, requested room options fit app limits, and TURN is returned only if allowed. For WebSocket: validate `Origin`; native clients may omit it only if explicitly enabled. For cross-origin HTTP/module endpoints, echo `Access-Control-Allow-Origin` only for allowlisted origins and handle `OPTIONS`.

---

## 3. Lobby Semantics

### 3.1 Room creation

First successful join for a code creates the room if `create` options are supplied.

```json
{"type":"join","appId":"anderrh-github","code":"XYYYZZ","resumeToken":null,"create":{"maxPlayers":4,"waitUntilFull":true,"allowLateJoin":false,"allowReconnect":true,"allowReplacement":true,"reconnectPolicy":"token-or-claim-after-timeout","claimAfterMs":40000}}
```

Room fields:

```text
code string
maxPlayers int
waitUntilFull bool
allowLateJoin bool
allowReconnect bool
allowReplacement bool
started bool
players[maxPlayers] slot array
createdAt, emptySince, lastActivity
```

Player slot:

```text
id int              # stable 0..maxPlayers-1
conn *websocket.Conn or per-client send channel, nil/closed if no active signaling socket
resumeTokenHash []byte or token map entry
joinedAt, lastSeenAt # updated on any WS message; optional client keepalive may update it
claimableAfter = lastSeenAt + claim_after
```

### 3.2 Joining rules

- Stable IDs `0..maxPlayers-1`; never renumber. First creator gets `0`; normal join gets lowest open slot.
- `waitUntilFull=false`: room starts after creator joins. `true`: starts when occupied slot count reaches `maxPlayers`.
- After start, reject new joins unless `allowLateJoin`, valid resume, or policy-permitted slot claim.
- Valid hidden `resumeToken` reclaims original slot immediately when `allowReconnect=true`; it may replace the old transport connection for that same ID.
- Tokenless replacement/claim is allowed only when `allowReplacement=true`, room policy permits it, and target slot has been silent for `claim_after` since `lastSeenAt`.
- On socket close/silence: preserve occupied slot and ID. Do not impose a separate gameplay-disconnect timeout; games decide their own liveness. Destroy empty/expired rooms by TTL.

### 3.3 Reconnect / replacement UX

Do not require memorized secrets. Use hidden resume tokens plus configurable slot claiming.

```text
Policies: token-only | token-or-claim-after-timeout | claim-after-timeout | host-approval(optional later)
Token reconnect: same slot immediately if valid stored token.
Manual claim: user enters room code + player ID; allow only if `now-lastSeenAt >= claimAfter` and policy allows replacement/claim.
Silence rule: `lastSeenAt` updates on any WS message. No required heartbeat and no separate `disconnectAfter`. Default `claimAfter=40s`; idle/sleeping clients can be claimed after that unless policy/longer timeout prevents it.
On successful resume/claim: issue new resumeToken and rebuild peer connections involving that player ID.
```

Protocol additions:

```json
{"type":"claim-slot","code":"XYYYZZ","playerId":2,"appId":"anderrh-github"}
{"type":"player-rejoined","playerId":2,"wasReplacement":false}
{"type":"player-replaced","playerId":2}
```

Security tradeoff: slot claiming proves only abandonment, not identity. For adversarial games use token-only, longer timeout, host approval, or an optional short PIN.

### 3.4 Join response

```json
{"type":"joined","selfId":0,"maxPlayers":4,"started":false,"resumeToken":"opaque","players":[{"id":0,"occupied":true,"lastSeenMsAgo":0}],"iceServers":[{"urls":["stun:pqrstuvw.xyz:3478"]},{"urls":["turn:pqrstuvw.xyz:3478?transport=udp","turn:pqrstuvw.xyz:3478?transport=tcp","turns:pqrstuvw.xyz:5349?transport=tcp"],"username":"1783300000:room-XYYYZZ-player-0","credential":"base64-hmac-sha1-password"}]}
```

---

## 4. WebSocket Signaling Protocol

All messages are JSON. Server must validate `type`, room membership, target player IDs, and payload size.

Client to server:

```json
{"type":"join","code":"XYYYZZ","resumeToken":null,"create":{...}}
{"type":"claim-slot","code":"XYYYZZ","playerId":2}
{"type":"signal","to":1,"payload":{"kind":"offer","sdp":"..."}}
{"type":"signal","to":1,"payload":{"kind":"answer","sdp":"..."}}
{"type":"signal","to":1,"payload":{"kind":"ice","candidate":{...}}}
{"type":"leave"}
```

Server to client:

```json
{"type":"joined",...}
{"type":"player-joined","playerId":1,"players":[...]}
{"type":"player-left","playerId":1,"reason":"explicit-leave"}
{"type":"player-rejoined","playerId":1,"wasReplacement":false}
{"type":"player-replaced","playerId":1}
{"type":"room-started"}
{"type":"signal","from":0,"payload":{"kind":"offer","sdp":"..."}}
{"type":"error","code":"room-full","message":"..."}
```

Deterministic offer rule:

```text
For each pair, lower player ID is polite initiator and creates the offer.
Higher player ID waits for offer and answers.
```

On `player-joined`, existing lower-ID peers initiate offers to the new peer if connection is allowed.

---

## 5. TURN/coturn Integration

### 5.1 coturn config essentials

Use external coturn. Enable long-term REST-secret credentials.

Template `/etc/turnserver.conf` essentials:

```text
listening-port=3478
tls-listening-port=5349
listening-ip=0.0.0.0
relay-ip=<SERVER_PUBLIC_IP>
external-ip=<SERVER_PUBLIC_IP>
realm=pqrstuvw.xyz
server-name=pqrstuvw.xyz
use-auth-secret
static-auth-secret=<SHARED_SECRET>
cert=/var/lib/p2p-lobby/certs/fullchain.pem
pkey=/var/lib/p2p-lobby/certs/privkey.pem
min-port=49160
max-port=49260
fingerprint
lt-cred-mech
no-multicast-peers
no-cli
log-file=/var/log/turnserver/turn.log
simple-log
```

Open firewall:

```text
3478/udp
3478/tcp
5349/tcp
49160-49260/udp
```

### 5.2 Shared secret file

Create same secret for coturn and Go server.

```bash
install -d -m 0750 -o p2plobby -g p2plobby /var/lib/p2p-lobby
openssl rand -base64 32 > /var/lib/p2p-lobby/turn-secret
chown p2plobby:p2plobby /var/lib/p2p-lobby/turn-secret
chmod 0640 /var/lib/p2p-lobby/turn-secret
```

Render the same secret into coturn config as `static-auth-secret`.

### 5.3 Go TURN credential generation

Given shared secret `S`, TTL `T`, room `R`, player `P`:

```text
expiry = unix_now + T
username = "<expiry>:room-<R>-player-<P>"
password = base64(HMAC-SHA1(key=S, message=username))
```

Return one STUN-only server plus TURN servers with that username/password in `joined.iceServers`.

---

## 6. DataChannel Design

For every peer pair, create two channels:

```text
reliable:    ordered=true, reliable default
bestEffort:  ordered=false, maxRetransmits=0
```

API primitives exposed by TS and Rust clients:

```text
send_best_effort(to, bytes)
send_reliable(to, bytes) -> async result
broadcast_best_effort(bytes)
next_event()/onMessage/onPlayerJoin/onPlayerLeave
```

Message receive event:

```text
Message { from: PlayerId, kind: Reliable|BestEffort, data: bytes }
```

Reliable framing: chunk large reliable payloads into frames `{magic/version,msg_id,seq,total,payload_len,payload}`; default chunk 16-64 KiB; timeout incomplete reassemblies. Best-effort has bounded payload only, e.g. 1200-16000 bytes. Backpressure: browser checks `bufferedAmount`/`bufferedamountlow`; Rust uses crate send futures/limits; never unbounded queues.

---

## 7. TypeScript Client Library

Package name example: `@p2p-lobby/client`.

Public API:

```ts
export type ConnectOptions = {
  server: string;                 // https://host[:port] or wss://host[:port]/ws
  appId?: string;                 // optional app policy id for hosted static sites
  code: string;
  create?: {
    maxPlayers: number;
    waitUntilFull?: boolean; allowLateJoin?: boolean;
    allowReconnect?: boolean; allowReplacement?: boolean;
    reconnectPolicy?: "token-only" | "token-or-claim-after-timeout" | "claim-after-timeout" | "host-approval";
    claimAfterMs?: number;
  };
  resumeToken?: string;          // stored automatically by helper if storageKey set
  claimPlayerId?: number;        // manual room-code + player-ID recovery
  storageKey?: string;           // browser persistence for hidden resume token
  iceServers?: RTCIceServer[];     // optional override/append
};

export type MessageKind = "reliable" | "best-effort";

export type P2PEvent =
  | { type: "message"; from: number; kind: MessageKind; data: Uint8Array }
  | { type: "player-joined"; playerId: number }
  | { type: "player-left"; playerId: number }
  | { type: "player-rejoined"; playerId: number; wasReplacement: boolean }
  | { type: "started" }
  | { type: "peer-state"; playerId: number; state: RTCPeerConnectionState };

export class P2PGame {
  readonly selfId: number;
  readonly maxPlayers: number;
  readonly resumeToken: string;
  readonly players: readonly PlayerInfo[];

  static connect(opts: ConnectOptions): Promise<P2PGame>;
  sendBestEffort(to: number, data: Uint8Array | ArrayBuffer): void;
  sendReliable(to: number, data: Uint8Array | ArrayBuffer): Promise<void>;
  broadcastBestEffort(data: Uint8Array | ArrayBuffer): void;
  onEvent(cb: (ev: P2PEvent) => void): () => void;
  close(): void;
}
```

Implementation notes: normalize `https://...` to `wss://.../ws`; WebSocket join/claim; persist resumeToken under `storageKey`; no mandatory heartbeat; use joined `iceServers`; lower-ID offer rule; forward ICE/SDP; two channels; emit events; bundle to `web/p2p-client.js` and npm.

---

## 8. Rust Client Library

Crate name example: `p2p_lobby_client`.

Use native async Rust. Suggested deps:

```toml
[dependencies]
tokio = { version = "1", features = ["full"] }
tokio-tungstenite = { version = "0.24", features = ["rustls-tls-webpki-roots"] }
webrtc = "0.11"
serde = { version = "1", features = ["derive"] }
serde_json = "1"
futures = "0.3"
bytes = "1"
url = "2"
thiserror = "1"
base64 = "0.22"
```

Public API target:

```rust
pub struct ConnectOptions {
    pub server: String,      // https://host[:port] or wss://host[:port]/ws
    pub app_id: Option<String>,
    pub code: String,
    pub create: Option<CreateOptions>,
    pub resume_token: Option<String>,
    pub claim_player_id: Option<PlayerId>,
    pub storage_path: Option<std::path::PathBuf>,
}

pub struct CreateOptions {
    pub max_players: u16,
    pub wait_until_full: bool,
    pub allow_late_join: bool,
    pub allow_reconnect: bool,
    pub allow_replacement: bool,
    pub reconnect_policy: Option<ReconnectPolicy>,
    pub claim_after_ms: Option<u64>,
}

pub enum Event {
    Message { from: PlayerId, kind: MessageKind, data: bytes::Bytes },
    PlayerJoined { player_id: PlayerId },
    PlayerLeft { player_id: PlayerId },
    PlayerRejoined { player_id: PlayerId, was_replacement: bool },
    Started,
    PeerState { player_id: PlayerId, state: String },
}

pub struct P2PGame { ... }

impl P2PGame {
    pub async fn connect(opts: ConnectOptions) -> Result<Self>;
    pub async fn send_best_effort(&self, to: PlayerId, data: bytes::Bytes) -> Result<()>;
    pub async fn send_reliable(&self, to: PlayerId, data: bytes::Bytes) -> Result<()>;
    pub async fn broadcast_best_effort(&self, data: bytes::Bytes) -> Result<()>;
    pub async fn next_event(&mut self) -> Option<Event>;
    pub async fn close(self) -> Result<()>;
}
```

Implementation notes: mirror TS protocol; normalize HTTPS to WSS `/ws`; use `tokio-tungstenite` + `webrtc`; persist resume token if `storage_path`; no mandatory heartbeat; same lower-ID rule/channels/framing; examples: `basic.rs`, `echo.rs`.

---

## 9. Go Server Implementation Details

### 9.1 Packages

- `internal/protocol`: JSON structs and validation.
- `internal/lobby`: room/player state, locking, room GC.
- `internal/server`: HTTP routes, WebSocket accept loop, origin/proxy logic.
- `internal/turn`: shared-secret credential generation.
- `internal/static`: `//go:embed` web files.

### 9.2 Concurrency model

- One `LobbyManager` with `sync.RWMutex` or sharded map.
- Each active signaling connection gets a bounded outbound channel.
- WebSocket reader goroutine validates incoming messages.
- WebSocket writer goroutine serializes outbound messages.
- Lobby operations must never block on network writes; enqueue or close slow transport.
- Periodic GC goroutine deletes expired rooms and rooms empty by explicit leaves / no occupied slots.

### 9.3 Security basics

- Room codes length 4-64, safe charset only.
- Random hidden resume tokens: 32 bytes from `crypto/rand`, base64url; clients store locally.
- Store token hashes or opaque token map; do not log tokens.
- Track `lastSeenAt` from any inbound WS message; do not require active heartbeats. Optional client/game keepalive can refresh it. Allow slot claim after `claim_after` of silence only if policy permits. No separate server gameplay-disconnect timer/state.
- Max JSON message size, e.g. 1 MiB for signaling.
- Allow only configured origins for WebSocket.
- Trust `X-Forwarded-*` only if remote IP is trusted proxy.
- Reject signaling `to` self, invalid players, or unjoined players.
- No server-side game payload relay except signaling.

---

## 10. Deployment Method 1: Apache Reverse Proxy

Runtime shape:

```text
Browser -> https://pqrstuvw.xyz / wss://pqrstuvw.xyz/ws -> Apache :443
Apache -> http://127.0.0.1:8787 / ws://127.0.0.1:8787/ws -> Go
WebRTC fallback -> coturn :3478/:5349 + UDP relay range
```

Go service args: use `/etc/p2p-lobby/config.toml` plus minimal override:

```text
/usr/local/bin/p2p-lobby-server --config /etc/p2p-lobby/config.toml --listen-http 127.0.0.1:8787 --behind-proxy --public-url https://pqrstuvw.xyz
```

Apache vhost essentials:

```apache
<VirtualHost *:443>
    ServerName pqrstuvw.xyz

    SSLEngine On
    SSLCertificateFile /etc/letsencrypt/live/pqrstuvw.xyz/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/pqrstuvw.xyz/privkey.pem

    ProxyRequests Off
    ProxyPreserveHost On
    RequestHeader set X-Forwarded-Proto "https"
    RequestHeader set X-Forwarded-Port "443"

    ProxyPass        "/ws" "ws://127.0.0.1:8787/ws"
    ProxyPassReverse "/ws" "ws://127.0.0.1:8787/ws"

    ProxyPass        "/" "http://127.0.0.1:8787/"
    ProxyPassReverse "/" "http://127.0.0.1:8787/"
</VirtualHost>
```

Enable modules:

```bash
a2enmod ssl headers proxy proxy_http proxy_wstunnel
systemctl reload apache2
```

---

## 11. Deployment Method 2: Direct Go HTTPS on `:4443`

Runtime shape:

```text
Browser -> https://pqrstuvw.xyz:4443 / wss://pqrstuvw.xyz:4443/ws -> Go
Apache may still own :443 or be disabled; irrelevant.
WebRTC fallback -> coturn :3478/:5349 + UDP relay range
```

Go service args: use config plus minimal override:

```text
/usr/local/bin/p2p-lobby-server --config /etc/p2p-lobby/config.toml --listen-https :4443 --public-url https://pqrstuvw.xyz:4443
```

Firewall:

```bash
ufw allow 4443/tcp
ufw allow 3478/udp
ufw allow 3478/tcp
ufw allow 5349/tcp
ufw allow 49160:49260/udp
```

---

## 12. Required Setup Scripts

All scripts should be idempotent where possible.

### 12.1 `scripts/install-common.sh`

Inputs: domain, app user. Does:

```text
create system user p2plobby
create /var/lib/p2p-lobby, /var/lib/p2p-lobby/certs, /etc/p2p-lobby, /var/log/p2p-lobby
install binary to /usr/local/bin/p2p-lobby-server
create /var/lib/p2p-lobby/turn-secret if absent
chown/chmod app dirs
```

### 12.2 `scripts/copy-certs.sh` root-only

Inputs: domain, app user. Must be safe and minimal.

```bash
#!/bin/sh
set -eu
DOMAIN="${1:?domain}"
APP_USER="${2:-p2plobby}"
DEST="/var/lib/p2p-lobby/certs"

install -d -m 0750 -o "$APP_USER" -g "$APP_USER" "$DEST"
install -m 0640 -o "$APP_USER" -g "$APP_USER" \
  "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" "$DEST/fullchain.pem"
install -m 0640 -o "$APP_USER" -g "$APP_USER" \
  "/etc/letsencrypt/live/$DOMAIN/privkey.pem" "$DEST/privkey.pem"

systemctl restart p2p-lobby || true
systemctl restart coturn || systemctl restart turnserver || true
```

Install it as certbot deploy hook:

```bash
install -m 0755 scripts/copy-certs.sh /usr/local/sbin/p2p-lobby-copy-certs
cat >/etc/letsencrypt/renewal-hooks/deploy/p2p-lobby-copy-certs <<'HOOK'
#!/bin/sh
/usr/local/sbin/p2p-lobby-copy-certs pqrstuvw.xyz p2plobby
HOOK
chmod +x /etc/letsencrypt/renewal-hooks/deploy/p2p-lobby-copy-certs
```

### 12.3 `scripts/setup-coturn.sh`

Does:

```text
install coturn if missing
read/generate /var/lib/p2p-lobby/turn-secret
render /etc/turnserver.conf with static-auth-secret
set coturn enabled in /etc/default/coturn if distro uses it
open firewall if ufw exists
systemctl enable --now coturn OR turnserver depending distro
```

### 12.4 `scripts/setup-apache-proxy.sh`

Does:

```text
enable Apache modules ssl headers proxy proxy_http proxy_wstunnel
render vhost config for domain to proxy / and /ws to 127.0.0.1:8787
apachectl configtest
reload apache
install p2p-lobby.service with reverse-proxy args
systemctl enable --now p2p-lobby
```

### 12.5 `scripts/setup-direct-4443.sh`

Does:

```text
run copy-certs.sh
install p2p-lobby.service with direct :4443 args
open firewall 4443 + TURN ports if ufw exists
systemctl enable --now p2p-lobby
```

---

## 13. systemd Service Template

```ini
[Unit]
Description=P2P Lobby Server
After=network-online.target
Wants=network-online.target

[Service]
User=p2plobby
Group=p2plobby
ExecStart=/usr/local/bin/p2p-lobby-server <ARGS_RENDERED_BY_SETUP_SCRIPT>
Restart=on-failure
RestartSec=2
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/var/lib/p2p-lobby /var/log/p2p-lobby

[Install]
WantedBy=multi-user.target
```

Do not need privileged bind for `:4443` or `127.0.0.1:8787`.

---

## 14. Build Outputs

Server:

```bash
go build -trimpath -ldflags "-s -w" -o dist/p2p-lobby-server ./cmd/p2p-lobby-server
```

TypeScript:

```bash
cd clients/ts
npm install
npm run build
cp dist/browser/p2p-client.js ../../web/p2p-client.js
```

Rust:

```bash
cd clients/rust
cargo build --release
cargo run --example basic -- --server wss://pqrstuvw.xyz:4443/ws --code XYYYZZ
```

Build order:

```text
1. build TS browser bundle into web/
2. go build embeds web/
3. build/publish Rust crate independently
```

---

## 15. Minimal End-to-End Test Plan

### Method 1 Apache

```bash
curl -I https://pqrstuvw.xyz/healthz
open https://pqrstuvw.xyz/
```

Browser JS:

```js
await P2PGame.connect({server:"https://pqrstuvw.xyz", code:"TEST", create:{maxPlayers:2}})
```

### Method 2 Direct 4443

```bash
curl -I https://pqrstuvw.xyz:4443/healthz
open https://pqrstuvw.xyz:4443/
```

Browser JS:

```js
await P2PGame.connect({server:"https://pqrstuvw.xyz:4443", code:"TEST", create:{maxPlayers:2}})
```

### TURN

Use browser `chrome://webrtc-internals` or equivalent. Confirm selected candidate pair may show relay when direct path fails. Add client debug event exposing ICE candidate type: host/srflx/relay.

### Rust client

Run two clients with same code, one creates room. Verify join, reliable send, best-effort send, token reconnect, and 40s-silence slot claim.

---

## 16. MVP Completion Checklist

Server:

- [ ] Go binary builds static.
- [ ] Embedded web serving works.
- [ ] HTTP mode works.
- [ ] HTTPS `:4443` mode works.
- [ ] Apache proxy mode works.
- [ ] WebSocket origin checks + config-file allowlist for static host origins.
- [ ] Join/create room.
- [ ] Stable player IDs.
- [ ] Room start semantics.
- [ ] Hidden resume token.
- [ ] Heartbeat/silence tracking for 40s slot reclaim.
- [ ] Timeout-based slot claim/replacement.
- [ ] Replacement/late-join rules.
- [ ] Signal forwarding.
- [ ] TURN credential generation.
- [ ] Room GC.
- [ ] systemd + scripts.

TypeScript:

- [ ] Public API implemented.
- [ ] WebSocket signaling.
- [ ] WebRTC peer setup.
- [ ] Two DataChannels.
- [ ] Reliable chunking.
- [ ] Events.
- [ ] Resume token persistence + optional claimPlayerId flow.
- [ ] Browser bundle embedded.
- [ ] npm package build.

Rust:

- [ ] Public API implemented.
- [ ] WebSocket signaling.
- [ ] WebRTC peer setup.
- [ ] Two DataChannels.
- [ ] Reliable chunking compatible with TS.
- [ ] Resume token persistence + optional claim_player_id flow.
- [ ] Examples.

Deployment:

- [ ] coturn configured with shared secret.
- [ ] cert copy script.
- [ ] certbot deploy hook.
- [ ] Apache setup script.
- [ ] direct 4443 setup script.
- [ ] firewall ports.

