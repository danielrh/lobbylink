#!/bin/sh
# install-common.sh <domain> [app-user]
# Creates the p2plobby system user, app directories, TURN secret, and
# installs the server binary. Idempotent; run as root.
set -eu

DOMAIN="${1:?usage: install-common.sh <domain> [app-user]}"
APP_USER="${2:-p2plobby}"
BINARY_SRC="${BINARY_SRC:-dist/p2p-lobby-server}"

if ! id "$APP_USER" >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/p2p-lobby --shell /usr/sbin/nologin "$APP_USER"
fi

install -d -m 0750 -o "$APP_USER" -g "$APP_USER" /var/lib/p2p-lobby
install -d -m 0750 -o "$APP_USER" -g "$APP_USER" /var/lib/p2p-lobby/certs
install -d -m 0755 /etc/p2p-lobby
install -d -m 0750 -o "$APP_USER" -g "$APP_USER" /var/log/p2p-lobby

if [ -f "$BINARY_SRC" ]; then
    install -m 0755 "$BINARY_SRC" /usr/local/bin/p2p-lobby-server
else
    echo "note: $BINARY_SRC not found; build with:" >&2
    echo "  go build -trimpath -ldflags '-s -w' -o dist/p2p-lobby-server ./cmd/p2p-lobby-server" >&2
fi

if [ ! -s /var/lib/p2p-lobby/turn-secret ]; then
    umask 077
    openssl rand -base64 32 > /var/lib/p2p-lobby/turn-secret
fi
chown "$APP_USER:$APP_USER" /var/lib/p2p-lobby/turn-secret
chmod 0640 /var/lib/p2p-lobby/turn-secret

if [ ! -f /etc/p2p-lobby/config.toml ]; then
    cat > /etc/p2p-lobby/config.toml <<EOF
[server]
public_url = "https://$DOMAIN:4443"
listen_http = ""
listen_https = ":4443"
cert = "/var/lib/p2p-lobby/certs/fullchain.pem"
key = "/var/lib/p2p-lobby/certs/privkey.pem"
behind_proxy = false
trusted_proxies = ["127.0.0.1"]

[security]
allowed_origins = [
  "https://$DOMAIN", "https://$DOMAIN:4443",
]
max_ws_message_bytes = 1048576

[turn]
enabled = true
realm = "$DOMAIN"
shared_secret_file = "/var/lib/p2p-lobby/turn-secret"
ttl = "3600s"
urls = ["stun:$DOMAIN:3478", "turn:$DOMAIN:3478?transport=udp", "turn:$DOMAIN:3478?transport=tcp", "turns:$DOMAIN:5349?transport=tcp"]

[rooms]
empty_ttl = "300s"
max_ttl = "24h"
max_rooms = 10000
max_players_hard = 32
claim_after = "40s"
EOF
    echo "wrote /etc/p2p-lobby/config.toml (edit allowed_origins as needed)"
fi

echo "install-common: done for $DOMAIN (user $APP_USER)"
