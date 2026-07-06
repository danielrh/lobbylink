# Rust client implementation guide (terse)

Distilled from building/validating the TypeScript client (2026-07-05).
The TS client (`clients/ts/src/index.ts`) is the reference
implementation; `clients/ts/README.md` §"Wire contract" is normative.
Main spec: `lobbylink_implementation_guide.md` §8 (API), §4 (protocol),
§6 (channels/framing).

## Crate & targets

`clients/rust/`, lib name `p2p_lobby_client`. ONE crate, TWO backends
selected by target triple (cfg, not feature flags — `cargo build
--target wasm32-unknown-unknown` must Just Work with no flags):

- **native** (`cfg(not(target_arch = "wasm32"))`): tokio,
  tokio-tungstenite (rustls), webrtc (0.11+), plus shared deps.
  Examples: `basic.rs`, `echo.rs`.
- **wasm32-unknown-unknown** (`cfg(target_arch = "wasm32")`): the
  BROWSER's WebSocket + RTCPeerConnection via web-sys. It is meant to
  be dropped into an existing wasm-bindgen Rust app, so keep it lean:
  wasm-side deps are ONLY wasm-bindgen, js-sys, web-sys,
  wasm-bindgen-futures + the shared deps (serde, serde_json,
  futures-channel, thiserror). No tokio, no webrtc crate, no gloo, no
  url, no getrandom.
  `crate-type = ["lib"]` only — the HOST project owns the cdylib and
  the wasm-bindgen/wasm-pack step. Export NO `#[wasm_bindgen]` items
  and no `#[wasm_bindgen(start)]`; the public API is plain Rust and
  wasm-bindgen stays an internal detail.

Put backend deps under
`[target.'cfg(...)'.dependencies]` sections. Layout:

```
src/lib.rs        cfg-gated re-export of one backend's P2PGame
src/core/         shared, no I/O: framing.rs, reassembly.rs,
                  protocol.rs (serde wire types), events.rs,
                  error.rs, url.rs (signaling URL normalization),
                  roster.rs (slot state machine)
src/native/       tokio + webrtc backend
src/wasm/         web-sys backend
```

The two backends may differ freely under the hood, but both consume
`core` for everything wire-visible, so interop is by construction.
Hand-roll URL normalization in core (TS `signalingUrl` is ~25 lines);
don't pull the `url` crate just for that.

Shared code must NOT require `Send` futures — everything JS-side is
`!Send`. Public API: no `Send` bounds anywhere; native internals may
still spawn `Send` tasks privately. Event delivery on both targets:
`futures_channel::mpsc::unbounded` feeding `next_event()`.
Platform shims the core needs (cfg functions, keep the list short):
`now_ms()` (native `Instant`, wasm `js_sys::Date::now` —
`std::time::Instant` ABORTS on wasm32-unknown-unknown), `sleep(ms)`
(tokio::time vs ~15-line Promise/`setTimeout` + `JsFuture`), `spawn`
(tokio::spawn vs `wasm_bindgen_futures::spawn_local`).

Public API: guide §8 verbatim (`P2PGame::connect(ConnectOptions)`,
`send_best_effort`, `send_reliable`, `broadcast_best_effort`,
`next_event`, `close`). Match the TS extras where they map: events
`PlayerReplaced`, `CandidatePair{local,remote}`, `LobbyError{code,msg}`,
`SignalingClosed{code,msg}`; option `force_relay`.
Token persistence is the one cfg-gated option: native
`storage_path: Option<PathBuf>` (file), wasm `storage_key:
Option<String>` + `storage: Local|Session` (web Storage, like TS —
Session is the safe default when several tabs may join one room).

## Signaling (WebSocket, JSON)

- URL normalization: http(s)→ws(s); strip trailing `/`; append `/ws`
  unless already suffixed. `https://pqrstuvw.xyz/lobbylink` →
  `wss://pqrstuvw.xyz/lobbylink/ws`.
- **Origin header required**: prod has no `allow_no_origin`.
  Native: send an allowlisted `Origin` (e.g. `https://pqrstuvw.xyz`)
  in the WS handshake, like scratch `livetest` does.
  wasm: you CANNOT set headers from the browser; the page's own origin
  is sent automatically, so the page must be served from an
  allowlisted origin (dev: `http://localhost:5173`) or the local
  server must run with `--allow-no-origin`.
- Send one of:
  `{"type":"join","code",appId?,resumeToken?,create?}` or
  `{"type":"claim-slot","code","playerId",appId?}` (claim ignores
  token/create). `create` = `{maxPlayers, waitUntilFull?, allowLateJoin?,
  allowReconnect?, allowReplacement?, reconnectPolicy?, claimAfterMs?}`.
- First reply is `joined` (`code,selfId,maxPlayers,started,resumeToken,
  players[{id,occupied,connected,lastSeenMsAgo}],iceServers?`) or
  `error{code,message}` → fail connect with that code.
- Persist `resumeToken` (it rotates on every join/claim). On explicit
  `close()` send `{"type":"leave"}` and delete the stored token —
  EXCEPT don't delete on `session-superseded` (another process/tab of
  ours owns the new token). Invalid/stale tokens fall back to fresh
  join server-side; never error on them.
- Server → client after join: `player-joined{playerId,players}` (full
  roster snapshot), `player-left{playerId,reason:explicit-leave|
  disconnected}`, `player-rejoined{playerId,wasReplacement}`,
  `player-replaced{playerId}`, `room-started`, `signal{from,payload}`,
  `error{code,message}`. Unknown types: ignore (fwd compat).
- Roster upkeep: explicit-leave → slot unoccupied; disconnected →
  connected=false but KEEP the peer connection (P2P outlives
  signaling); rejoined/replaced → occupied+connected, rebuild peer.
- Fatal error codes (WS will die): `replaced`, `session-superseded`,
  `room-expired` (tear down peers too), `slow-consumer` (keep peers).
  Everything else (`invalid-target`, `target-unavailable`, ...) is a
  non-fatal LobbyError event.
- No heartbeat needed; ANY WS message counts as liveness (claim timer
  default 40 s of silence).

## WebRTC (rules are target-independent)

- Per pair, both sides create two **pre-negotiated** channels:
  `"reliable"` id=1 ordered; `"best-effort"` id=2 unordered,
  maxRetransmits=0. No ondatachannel path.
  - webrtc crate: `RTCDataChannelInit{negotiated: Some(id), ...}`.
  - web-sys: `RtcDataChannelInit` setters `.negotiated(true).id(n)
    .ordered(..).max_retransmits(0)` +
    `create_data_channel_with_data_channel_dict`.
- **Lower player ID creates the offer**, higher answers. At join:
  offer to every occupied+connected peer with id > selfId; peers with
  lower ids will offer to us when they see our player-joined/rejoined.
- Signal payloads: `{"kind":"offer","sdp"}`, `{"kind":"answer","sdp"}`,
  `{"kind":"ice","candidate":<RTCIceCandidateInit json>}` (candidate
  null/absent = end-of-candidates; safe to skip sending null).
- Every INCOMING offer = tear down old pc, build a fresh one, answer
  (that's how rebuilds after failure/rejoin work). Initiator rebuilds
  (fresh pc + new offer) on connectionState failed (retry ≤3, backoff)
  and on player-rejoined/replaced. Ignore offers from higher IDs and
  stale answers (signalingState != have-local-offer).
- Queue remote ICE candidates until the remote description is set;
  addIceCandidate errors are warn-and-drop, never fatal.
- Use `joined.iceServers` (+ user-supplied extras). `force_relay` →
  ice_transport_policy Relay (both backends support it). Keep
  `iceServers` entries as passthrough JSON in core (`urls` may be a
  string OR an array); each backend converts to its own config type.

## wasm backend specifics (web-sys)

- web-sys features needed (roughly): `WebSocket, MessageEvent,
  CloseEvent, BinaryType, Window, Storage, RtcPeerConnection,
  RtcConfiguration, RtcIceServer, RtcIceTransportPolicy,
  RtcDataChannel, RtcDataChannelInit, RtcDataChannelType,
  RtcDataChannelState, RtcSessionDescriptionInit, RtcSdpType,
  RtcIceCandidate, RtcIceCandidateInit, RtcPeerConnectionIceEvent,
  RtcPeerConnectionState, RtcSignalingState, RtcStatsReport`.
- Every `Closure` registered as an `on*` handler MUST be kept alive —
  store them in the PeerLink/WS structs and drop on teardown. Never
  `.forget()` anything that gets rebuilt (leaks once per rebuild).
- Set binaryType to arraybuffer on the WebSocket AND both data
  channels (Firefox channels default to Blob).
- Single-threaded: `Rc<RefCell<...>>`, not `Arc<Mutex<...>>`; never
  hold a `RefCell` borrow across an `.await`.
- Outgoing ICE: `js_sys::JSON::stringify(&ev.candidate())` invokes
  toJSON and yields exactly the RTCIceCandidateInit JSON the wire
  wants; splice it into the signal as a raw `serde_json::Value`.
  Incoming: `JSON::parse` the value, or build `RtcIceCandidateInit`
  via setters (`candidate`, `sdp_mid`, `sdp_m_line_index`).
- create_offer/create_answer return `Promise<JsValue>`; await via
  `JsFuture`, read `.sdp` with `js_sys::Reflect::get`, pass the object
  on to set_local_description as `RtcSessionDescriptionInit`.
- Backpressure API is identical to the browser:
  `buffered_amount`, `set_buffered_amount_low_threshold`,
  `set_onbufferedamountlow`.
- candidate-pair stats: `pc.get_stats()` → `RtcStatsReport` is a JS
  Map; iterate its `entries()` with js_sys and Reflect. Needed for the
  force_relay validation, so don't skip it.

## Framing (must match TS byte-for-byte)

Reliable = one binary frame per SCTP message, big-endian:
`u8 magic 0x4C | u8 version 0x01 | u32 msg_id | u32 seq | u32 total |
u32 payload_len | payload`. Send: 16 KiB chunks (last short), msg_id =
per-peer wrapping counter. Receive: accept payload ≤64 KiB, total
≤4096, message ≤16 MiB; reassemble keyed (sender,msg_id); drop
incomplete after 30 s; duplicate seq = ignore.
Best-effort = raw payload, no framing, ≤16000 bytes (advise ≤1200).
Backpressure: pause when buffered_amount >1 MiB, resume <256 KiB
(buffered_amount_low_threshold + callback on both backends).
Best-effort over high-water: drop silently. SCTP handles
acks/retransmit/congestion — do NOT build an ack layer.
Framing/reassembly live in `core` and are pure (bytes in, bytes out) —
unit-test them once on the host target; they cannot diverge per
backend.

## Validation targets (all live)

- Server: `https://pqrstuvw.xyz/lobbylink` (Apache) and
  `https://pqrstuvw.xyz:4443` (standalone) — same process, both green.
  Local: `dist/p2p-lobby-server --listen-http 127.0.0.1:8789
  --allow-no-origin`.
- Test plan (guide §15 + what TS was tested with): two clients
  join/create (waitUntilFull), reliable small + 0-byte + 300 KB + 8 MiB
  (backpressure) + two concurrent, best-effort both ways, room-full
  error, explicit leave, token reconnect, 40 s claim, `force_relay`
  end-to-end (assert selected candidate pair is relay/relay).
- wasm runs: (a) `wasm-bindgen-test` headless-Chrome against a LOCAL
  `--allow-no-origin` server (the wbg test page's origin is a random
  localhost port, never allowlisted on prod); (b) a small demo page
  embedding the wasm build, served from `http://localhost:5173`
  (allowlisted) against prod.
- **Interop matrix** (same room, run all): TS↔native, TS↔wasm,
  native↔wasm, wasm↔wasm. native↔wasm over prod incl. force_relay is
  the money test — it proves the two backends share one wire dialect.
  Browser side: test harness in scratchpad `browsertest/`.

## Traps we already hit (don't rediscover)

- coturn must have `realm=<domain>`: empty-realm 401 challenges are
  accepted by pion/turnutils but rejected by browsers. Fixed on prod;
  `deploy/selfcontained/enable-turn-auth.sh` now enforces it.
- `turnutils_uclient` reports 401 on perfectly valid REST creds —
  never trust it. Use scratchpad `livecred/` (joins prod, allocates
  with the server-minted creds via pion) or `realmprobe/` (dumps 401
  REALM/NONCE).
- pqrstuvw.xyz DNS = DO floating IP 164.90.247.131; droplet is
  147.182.207.134 (= coturn relay-ip). Both work for TURN.
- TURN creds: username `"<expiry>:room-<code>-player-<id>"`, password
  base64(HMAC-SHA1(secret, username)) — but clients never compute
  this; they just pass through `joined.iceServers`.
- Storage collision: two clients sharing one token store supersede
  each other (`session-superseded`). Native `storage_path` should
  default per-process/instance, not per-room-global; wasm localStorage
  is shared by ALL tabs — same trap, hence the Session default.
- Reliable channel messages arrive whole (SCTP message-oriented): the
  webrtc crate delivers `DataChannelMessage` per SCTP message and the
  browser fires one `message` event per SCTP message — one frame each.
