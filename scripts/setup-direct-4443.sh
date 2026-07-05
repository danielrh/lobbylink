#!/bin/sh
# setup-direct-4443.sh <domain>
# Deployment Method 2: the Go binary owns public :4443 with copied
# Let's Encrypt certs. Run as root after install-common.sh.
set -eu
DOMAIN="${1:?usage: setup-direct-4443.sh <domain>}"
SCRIPT_DIR="$(dirname "$0")"

"$SCRIPT_DIR/copy-certs.sh" "$DOMAIN"

if command -v ufw >/dev/null 2>&1; then
    ufw allow 4443/tcp || true
    ufw allow 3478/udp || true
    ufw allow 3478/tcp || true
    ufw allow 5349/tcp || true
    ufw allow 49160:49260/udp || true
fi

"$SCRIPT_DIR/install-systemd-service.sh" \
    "--config /etc/p2p-lobby/config.toml --listen-https :4443 --public-url https://$DOMAIN:4443"

echo "setup-direct-4443: done; verify with: curl -I https://$DOMAIN:4443/healthz"
