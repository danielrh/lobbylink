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

CHANGED=0
backup_once() {
    if [ "$CHANGED" = 0 ]; then
        cp -a "$CONF" "$CONF.bak.$(date +%Y%m%d%H%M%S)"
        CHANGED=1
    fi
}

if grep -q '^use-auth-secret' "$CONF"; then
    echo "use-auth-secret already enabled"
else
    SECRET="$(cat "$DIR/turn-secret")"
    backup_once
    {
        echo ""
        echo "# Added by lobbylink enable-turn-auth.sh"
        echo "use-auth-secret"
        echo "static-auth-secret=$SECRET"
    } >> "$CONF"
fi

# An active realm= line is required: without it coturn sends 401
# challenges with an EMPTY realm, which browsers (Chrome: "Setting
# realm to the empty string, this is not supported") reject, so TURN
# allocation never succeeds even though permissive clients work.
if grep -q '^realm=' "$CONF"; then
    echo "realm already set: $(grep '^realm=' "$CONF" | head -1)"
else
    backup_once
    {
        echo "# Browsers reject 401 challenges with an empty realm."
        echo "realm=$DOMAIN"
    } >> "$CONF"
fi

if [ "$CHANGED" = 1 ]; then
    systemctl restart coturn
else
    echo "coturn config unchanged"
fi

# Restore the full ICE URL list now that relay auth will succeed.
sed -i 's|^urls = .*|urls = ["stun:'"$DOMAIN"':3478", "turn:'"$DOMAIN"':3478?transport=udp", "turn:'"$DOMAIN"':3478?transport=tcp"]|' "$DIR/config.toml"
chown "$APP_USER:$APP_USER" "$DIR/config.toml"
systemctl --user -M "$APP_USER@" restart lobbylink || true

echo "TURN auth enabled; coturn (and lobbylink, if running) restarted."
