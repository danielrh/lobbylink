# lobbylink-client (TypeScript, browser)

Zero-dependency browser client for the lobbylink P2P lobby + WebRTC
DataChannel system. One compiled ES module is both the npm entry point
and the copy-into-your-game browser bundle; the only build tool is the
TypeScript compiler.

## Build

```bash
make            # tsc -p . && cp dist/index.js ../../web/p2p-client.js
make check      # type-check only
```

(`npm run build` does the same; `make` fetches typescript via
`npm install` on first run.)

## Use

**Option A — copy one file.** Take `dist/index.js` (also served by the
Go server as `p2p-client.js`) and import it directly; it has no
imports of its own:

```html
<script type="module">
  import { P2PGame } from "./p2p-client.js";
</script>
```

**Option B — npm.** `npm install <this package>` and
`import { P2PGame } from "lobbylink-client"`.

```js
const game = await P2PGame.connect({
  server: "https://pqrstuvw.xyz/lobbylink", // or https://host:4443, wss://.../ws
  code: "MYROOM",
  create: { maxPlayers: 4, waitUntilFull: true }, // omit to only join
  storageKey: "mygame-MYROOM",  // persists the hidden resume token
});

game.onEvent((ev) => {
  if (ev.type === "message") handleBytes(ev.from, ev.kind, ev.data);
});

game.sendBestEffort(1, bytes);          // unordered, may drop, <= 16000 B
await game.sendReliable(1, bigBytes);   // ordered, chunked, <= 16 MiB
game.broadcastBestEffort(bytes);
game.close();                            // explicit leave, frees the slot
```

Reconnecting after a page reload: pass the same `storageKey` (token
resume, keeps your player ID), or `claimPlayerId` if the token is gone
and the slot has been silent past the room's `claimAfter`.

`connect` rejects and the API throws `LobbyError` with a stable `code`
(server codes like `room-full` pass through; client codes include
`connect-timeout`, `invalid-target`, `message-too-large`,
`channel-timeout`, `send-failed`).

Events beyond the guide's core set: `player-replaced`,
`candidate-pair` (selected ICE candidate types — host/srflx/relay, for
TURN debugging), `lobby-error` (non-fatal server error), and
`signaling-closed` (WebSocket gone; DataChannels survive unless the
code says the game is over: `replaced`, `session-superseded`,
`room-expired`). `ConnectOptions.forceRelay: true` forces TURN for
testing.

## Wire contract (the Rust client must match)

- **Channels** per peer pair, both sides create both (pre-negotiated):
  - `"reliable"`: `negotiated: true, id: 1, ordered: true`
  - `"best-effort"`: `negotiated: true, id: 2, ordered: false, maxRetransmits: 0`
- **Offer rule**: the lower player ID of each pair creates the SDP
  offer; the higher answers. Signal payloads:
  `{kind:"offer"|"answer", sdp}` and `{kind:"ice", candidate}`
  (`candidate` is an RTCIceCandidateInit; `null` = end of candidates,
  optional). Every incoming offer starts a fresh RTCPeerConnection on
  the answering side; initiators re-offer after ICE failure or when a
  peer rejoins/is replaced.
- **Best-effort**: raw payload, no framing, at most 16000 bytes
  (stay under ~1200 to avoid SCTP fragmentation loss amplification).
- **Reliable framing** (big-endian), one frame per SCTP message:

  | offset | type | field      | value                              |
  |-------:|------|------------|------------------------------------|
  | 0      | u8   | magic      | 0x4C (`'L'`)                       |
  | 1      | u8   | version    | 0x01                               |
  | 2      | u32  | msgId      | per-sender counter, wraps mod 2^32 |
  | 6      | u32  | seq        | chunk index, 0-based               |
  | 10     | u32  | total      | chunk count, >= 1                  |
  | 14     | u32  | payloadLen | payload bytes in this frame        |
  | 18     | ...  | payload    |                                    |

  Sender chunks at 16 KiB payload per frame (last chunk shorter);
  receivers accept any payloadLen up to 64 KiB, at most 4096 chunks and
  16 MiB per message, reassemble keyed by (sender, msgId), and drop
  incomplete messages after 30 s.
- **Backpressure**: pause sending when `bufferedAmount` > 1 MiB, resume
  below 256 KiB (`bufferedamountlow`). Best-effort sends are dropped
  instead of queued when the buffer is over the high-water mark.

## Layout note

The guide sketches `src/{protocol,client,peer,framing,errors}.ts`; this
implementation deliberately keeps those sections in a single
`src/index.ts` because plain tsc cannot bundle multi-file ES modules
into one file, and a single zero-import file is the whole point for
browser consumers. The file is organized in exactly those sections.
