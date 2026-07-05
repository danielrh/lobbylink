#!/bin/sh
# setup-coturn.sh <domain> <server-public-ip> [app-user]
# Installs and configures coturn with the shared REST-API secret used
# by the Go server. Idempotent; run as root.
set -eu
DOMAIN="${1:?usage: setup-coturn.sh <domain> <server-public-ip> [app-user]}"
PUBLIC_IP="${2:?server public IP required}"
APP_USER="${3:-p2plobby}"
TEMPLATE_DIR="$(dirname "$0")/../deploy"

if ! command -v turnserver >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update && apt-get install -y coturn
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y coturn
    else
        echo "install coturn manually, then re-run" >&2
        exit 1
    fi
fi

if [ ! -s /var/lib/p2p-lobby/turn-secret ]; then
    install -d -m 0750 -o "$APP_USER" -g "$APP_USER" /var/lib/p2p-lobby
    umask 077
    openssl rand -base64 32 > /var/lib/p2p-lobby/turn-secret
    chown "$APP_USER:$APP_USER" /var/lib/p2p-lobby/turn-secret
    chmod 0640 /var/lib/p2p-lobby/turn-secret
fi
SECRET="$(cat /var/lib/p2p-lobby/turn-secret)"

install -d -m 0755 /var/log/turnserver
if id turnserver >/dev/null 2>&1; then
    chown turnserver /var/log/turnserver
    # coturn must be able to read the certs copied by copy-certs.sh.
    usermod -a -G "$APP_USER" turnserver || true
fi

sed -e "s|<DOMAIN>|$DOMAIN|g" \
    -e "s|<SERVER_PUBLIC_IP>|$PUBLIC_IP|g" \
    -e "s|<SHARED_SECRET>|$SECRET|g" \
    "$TEMPLATE_DIR/turnserver.conf.template" > /etc/turnserver.conf
chmod 0640 /etc/turnserver.conf
if id turnserver >/dev/null 2>&1; then
    chgrp turnserver /etc/turnserver.conf
fi

# Debian/Ubuntu gate coturn behind /etc/default/coturn.
if [ -f /etc/default/coturn ]; then
    sed -i 's|^#*TURNSERVER_ENABLED=.*|TURNSERVER_ENABLED=1|' /etc/default/coturn
    grep -q TURNSERVER_ENABLED /etc/default/coturn || echo 'TURNSERVER_ENABLED=1' >> /etc/default/coturn
fi

if command -v ufw >/dev/null 2>&1; then
    ufw allow 3478/udp || true
    ufw allow 3478/tcp || true
    ufw allow 5349/tcp || true
    ufw allow 49160:49260/udp || true
fi

systemctl enable --now coturn 2>/dev/null || systemctl enable --now turnserver
systemctl restart coturn 2>/dev/null || systemctl restart turnserver
echo "setup-coturn: done for $DOMAIN ($PUBLIC_IP)"
