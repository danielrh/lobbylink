# lobbylink

Instant lobby-based multiplayer connectivity via WebRTC DataChannels
with coturn fallback: a Go signaling/lobby server plus wire-compatible
client libraries for browsers and native apps. Full design spec:
[lobbylink_implementation_guide.md](lobbylink_implementation_guide.md).

## Status

- **Go server** (`cmd/p2p-lobby-server`, `internal/`): implemented, with
  unit + integration tests.
- **TypeScript client** (`clients/ts`): implemented — zero runtime
  dependencies, built by plain `tsc` (`make` in `clients/ts`), browser
  test suite passes against production including forced-TURN relay.
  See [clients/ts/README.md](clients/ts/README.md) for the API and the
  wire/framing contract that every other client mirrors.
- **Rust client** (`clients/rust`): implemented — native (tokio +
  webrtc-rs) and wasm32 (browser) backends from one crate.
- **Java client** (`clients/java`): implemented — webrtc-java native
  bindings; games build with plain `javac` against a folder of jars.
- **Go client** (`clients/go`): implemented — pure Go (pion/webrtc, no
  cgo), loopback integration tests plus verified against production
  including forced-TURN relay.
- **C++ client** (`clients/cpp`): implemented — C++17 on libdatachannel,
  CMake build.
- **Swift client** (`clients/swift`): implemented — SwiftPM package;
  Foundation WebSocket signaling + libdatachannel's C API for the peer
  mesh.

Example games live in [`examples/`](examples/README.md) — asteroids
(TS/Rust/wasm, one shared wire protocol), snake (Java), tarsus (TS),
console tetris with garbage attacks (Go, `examples/tetris-go`) — plus
minimal chat apps in several clients' `examples/` folders that all
interoperate in one room.

## Build & test

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git rev-parse --short HEAD)" \
  -o dist/p2p-lobby-server ./cmd/p2p-lobby-server
go test -race ./...
```

The binary embeds `web/` (demo page + browser client bundle). Build the
TypeScript client first when you want the real `web/p2p-client.js`
instead of the placeholder.

## Run (dev)

```bash
./dist/p2p-lobby-server \
  --listen-http 127.0.0.1:8787 \
  --allowed-origin http://localhost:8787,http://127.0.0.1:8787 \
  --public-url http://127.0.0.1:8787
```

Then open http://127.0.0.1:8787/ for the demo page, or check
`/healthz` and `/config.json`.

## Configuration

Defaults < `--config /etc/p2p-lobby/config.toml` < CLI flags. See the
config example in the implementation guide §2.3; every key there is
supported, and unknown keys are rejected so typos fail fast.

Key server behaviors:

- WebSocket `Origin` must be on the global allowlist (or an app
  allowlist for `appId` joins); native clients without an Origin header
  are only accepted with `--allow-no-origin` / `allow_no_origin = true`.
- Stable player IDs `0..maxPlayers-1`; disconnects preserve slots.
- Hidden resume tokens (rotated on every resume/claim; only hashes are
  stored server-side) plus timeout-based slot claiming (`claim_after`,
  default 40s of silence; any WS message counts as liveness).
- TURN credentials are minted per player with coturn's REST-secret
  scheme (`use-auth-secret`).

## Embedding as a library

Other binaries can link the lobby in and mount their own HTTP routes next to
it ("plugins") through the public `lobbyserver` package — the stock
`cmd/p2p-lobby-server` runs through the same package, so embedders get the
exact production serving behavior. The lobby stays game-agnostic; anything
game-specific lives in the embedding module.

```go
cfg, _ := lobbyserver.LoadConfig("server.toml") // or DefaultConfig()
cfg.SetListenHTTP("127.0.0.1:8787")
srv, _ := lobbyserver.New(cfg, version)
mux := http.NewServeMux()
mux.Handle("/mygame/", myGameRoutes(cfg.AllowedOrigins()))
mux.Handle("/", srv.Handler())
err := srv.Run(ctx, mux) // listeners + room GC + graceful shutdown
```

## Deployment

**Recommended: the self-contained kit** — everything in `~/lobbylink`
on the server, run as a systemd user service, with dual entry points
(Apache subpath proxy + standalone `:4443`). Step-by-step guide and
all scripts: [deploy/selfcontained/README.md](deploy/selfcontained/README.md).

Alternative system-wide shapes from the guide (§10–11):

1. **Apache reverse proxy** — Apache owns `:443`, proxies `/` and `/ws`
   to Go on `127.0.0.1:8787`: `scripts/setup-apache-proxy.sh <domain>`.
2. **Direct HTTPS on :4443** — Go serves TLS itself with copied
   Let's Encrypt certs: `scripts/setup-direct-4443.sh <domain>`.

Common steps first:

```bash
sudo scripts/install-common.sh <domain>       # user, dirs, secret, config
sudo scripts/copy-certs.sh <domain>           # also a certbot deploy hook
sudo scripts/setup-coturn.sh <domain> <ip>    # coturn with shared secret
```

Templates for systemd/Apache/coturn live in `deploy/`.
