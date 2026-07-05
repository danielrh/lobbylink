#!/bin/sh
# enable-turn-auth.sh <domain> [app-user]
# Step 2 of deploy/selfcontained/README.md. Run as root only if
# root-setup.sh reported that coturn lacks use-auth-secret.
#
# WARNING: modifies /etc/turnserver.conf (timestamped backup) and
# restarts coturn. If other applications use this coturn with
# long-term credentials, verify them afterwards.
set -eu

DOMAIN="${1:?usage: enable-turn-auth.sh <domain> [app-user]}"
APP_USER="${2:-${SUDO_USER:?app-user required when not run via sudo}}"
APP_HOME="$(getent passwd "$APP_USER" | cut -d: -f6)"
DIR="$APP_HOME/lobbylink"
CONF=/etc/turnserver.conf

if grep -q '^use-auth-secret' "$CONF"; then
    echo "use-auth-secret already enabled; nothing to do"
    exit 0
fi

SECRET="$(cat "$DIR/turn-secret")"
cp -a "$CONF" "$CONF.bak.$(date +%Y%m%d%H%M%S)"
{
    echo ""
    echo "# Added by lobbylink enable-turn-auth.sh"
    echo "use-auth-secret"
    echo "static-auth-secret=$SECRET"
} >> "$CONF"
systemctl restart coturn

# Restore the full ICE URL list now that relay auth will succeed.
sed -i 's|^urls = .*|urls = ["stun:'"$DOMAIN"':3478", "turn:'"$DOMAIN"':3478?transport=udp", "turn:'"$DOMAIN"':3478?transport=tcp"]|' "$DIR/config.toml"
chown "$APP_USER:$APP_USER" "$DIR/config.toml"
systemctl --user -M "$APP_USER@" restart lobbylink || true

echo "TURN auth enabled; coturn (and lobbylink, if running) restarted."
