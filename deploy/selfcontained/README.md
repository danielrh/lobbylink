# LobbyLink self-contained deployment, step by step

This is the deployment we actually use on pqrstuvw.xyz: everything
lives in `~/lobbylink` on the server, runs as a **systemd user
service** (no root daemon), and is reachable two ways at once:

- `https://DOMAIN/lobbylink/` + `wss://DOMAIN/lobbylink/ws` — through
  the existing Apache on :443 (reverse proxy).
- `https://DOMAIN:4443/` + `wss://DOMAIN:4443/ws` — the Go binary's own
  TLS listener, for servers without Apache (standalone mode).

Either entry point can be disabled by editing `~/lobbylink/config.toml`
(`listen_http` for the proxy path, `listen_https` for standalone).

## Assumed starting state

- Ubuntu-ish server; you have SSH key access as a normal user and can
  `sudo` interactively.
- Apache already serves `https://DOMAIN/` with Let's Encrypt certs
  under `/etc/letsencrypt/live/DOMAIN/` (only needed for the proxy
  entry point).
- coturn installed and running, possibly with a stock config
  (`apt-get install coturn` if missing).
- Go 1.26+ on your **local** machine. Nothing needs to be built on the
  server.

## Step 0 — build + upload (local machine, no sudo)

```bash
deploy/selfcontained/build-and-upload.sh you@DOMAIN DOMAIN
```

Builds the static binary, uploads it plus all the scripts below to
`~/lobbylink/`, renders `config.toml` from the template (first upload
only — an existing config is never overwritten), installs the systemd
user unit, and restarts the service if it is already running. Re-run
this same script for every upgrade.

## Step 1 — root setup (server, sudo)

```bash
sudo ~/lobbylink/root-setup.sh DOMAIN
```

- Copies the Let's Encrypt certs into `~/lobbylink/certs` (readable by
  your user) and installs a certbot deploy hook so renewals re-copy
  them and restart the service.
- TURN secret: if coturn already has `use-auth-secret`, reuses its
  secret; otherwise generates one and switches `config.toml` to
  STUN-only until step 2 runs.
- Opens 4443/tcp in ufw if ufw is active (with a cloud provider,
  check its firewall too).
- `loginctl enable-linger` so the user service starts at boot.

## Step 2 — coturn REST auth (server, sudo; skip if step 1 said "reusing")

```bash
sudo ~/lobbylink/enable-turn-auth.sh DOMAIN
```

Appends `use-auth-secret` + `static-auth-secret=<~/lobbylink/turn-secret>`
to `/etc/turnserver.conf` (timestamped backup) and restarts coturn.
**Caution:** if other applications use this coturn with long-term
credentials, verify them afterwards.

## Step 3 — pin the TURN relay to the public interface (server, sudo)

```bash
sudo ~/lobbylink/turn-fix-relay.sh            # auto-detects the public IP
sudo ~/lobbylink/turn-fix-relay.sh 203.0.113.7  # or pass it explicitly
```

Without `relay-ip=`, coturn allocates relays round-robin across every
local interface, handing out private 10.x addresses that internet
peers cannot reach. If the server is behind 1:1 NAT (public IP not on
a local interface), also add `external-ip=<public>/<private>` to
`/etc/turnserver.conf` manually.

## Step 4 — Apache reverse proxy (server, sudo; skip on Apache-less hosts)

```bash
sudo ~/lobbylink/apache-setup.sh   # defaults: certbot's 000-default-le-ssl.conf, /lobbylink
sudo ~/lobbylink/apache-setup.sh /etc/apache2/sites-available/mysite-ssl.conf /lobbylink
```

Enables `proxy proxy_http proxy_wstunnel headers`, writes
`/etc/apache2/lobbylink-proxy.conf`, and adds a single `Include` line
inside the existing TLS vhost (timestamped backup, `configtest` before
reload). Nothing else in the vhost is touched.

## Step 5 — start (server, no sudo)

```bash
systemctl --user enable --now lobbylink
```

## Step 6 — verify

```bash
curl -s https://DOMAIN/lobbylink/healthz          # ok
curl -s https://DOMAIN/lobbylink/config.json      # wsUrl: wss://DOMAIN/lobbylink/ws
curl -s https://DOMAIN:4443/healthz               # ok (standalone entry)
curl -s https://DOMAIN:4443/config.json           # wsUrl: wss://DOMAIN:4443/ws
sudo ~/lobbylink/turn-diag.sh                     # active coturn lines + logs
```

Then open `https://DOMAIN/lobbylink/` in two browser tabs and join the
same room code. For TURN, `chrome://webrtc-internals` shows whether a
relay candidate pair gets selected when direct paths are blocked.

## Day-2 operations

- Logs: `journalctl --user -u lobbylink -f`
- Restart: `systemctl --user restart lobbylink`
- Upgrade: re-run step 0 from your local checkout.
- Config: edit `~/lobbylink/config.toml`, then restart. Add game-site
  origins to `allowed_origins` — the WebSocket rejects any origin not
  listed.
- Cert renewals: automatic (certbot deploy hook installed by step 1).
