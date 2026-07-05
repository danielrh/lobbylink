#!/bin/sh
# root-setup.sh <domain> [app-user]
# Step 1 of deploy/selfcontained/README.md. Run as root on the server.
# Idempotent. Does NOT modify coturn's configuration.
set -eu

DOMAIN="${1:?usage: root-setup.sh <domain> [app-user]}"
APP_USER="${2:-${SUDO_USER:?app-user required when not run via sudo}}"
APP_HOME="$(getent passwd "$APP_USER" | cut -d: -f6)"
DIR="$APP_HOME/lobbylink"

[ -d "$DIR" ] || { echo "$DIR missing; run build-and-upload.sh first" >&2; exit 1; }

echo "== 1/5 copy Let's Encrypt certs into $DIR/certs"
install -d -m 0750 -o "$APP_USER" -g "$APP_USER" "$DIR/certs"
install -m 0640 -o "$APP_USER" -g "$APP_USER" \
  "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" "$DIR/certs/fullchain.pem"
install -m 0640 -o "$APP_USER" -g "$APP_USER" \
  "/etc/letsencrypt/live/$DOMAIN/privkey.pem" "$DIR/certs/privkey.pem"

echo "== 2/5 install certbot renewal hook"
install -d /etc/letsencrypt/renewal-hooks/deploy
cat > /etc/letsencrypt/renewal-hooks/deploy/lobbylink-copy-certs <<EOF
#!/bin/sh
install -m 0640 -o $APP_USER -g $APP_USER \\
  "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" "$DIR/certs/fullchain.pem"
install -m 0640 -o $APP_USER -g $APP_USER \\
  "/etc/letsencrypt/live/$DOMAIN/privkey.pem" "$DIR/certs/privkey.pem"
systemctl --user -M $APP_USER@ restart lobbylink || true
EOF
chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/lobbylink-copy-certs

echo "== 3/5 TURN shared secret"
SECRET=""
if grep -q '^use-auth-secret' /etc/turnserver.conf 2>/dev/null; then
    SECRET="$(sed -n 's/^static-auth-secret=//p' /etc/turnserver.conf | head -1)"
fi
umask 077
if [ -n "$SECRET" ]; then
    printf '%s' "$SECRET" > "$DIR/turn-secret"
    echo "   reusing existing coturn REST secret -> full TURN relay enabled"
else
    if [ ! -s "$DIR/turn-secret" ]; then
        openssl rand -base64 32 > "$DIR/turn-secret"
    fi
    # Until enable-turn-auth.sh shares this secret with coturn, TURN
    # relay auth would fail; advertise STUN only.
    sed -i 's|^urls = .*|urls = ["stun:'"$DOMAIN"':3478"]|' "$DIR/config.toml"
    chown "$APP_USER:$APP_USER" "$DIR/config.toml"
    echo "   coturn has no use-auth-secret yet: configured STUN-only."
    echo "   For full TURN relay run: sudo $DIR/enable-turn-auth.sh $DOMAIN"
fi
chown "$APP_USER:$APP_USER" "$DIR/turn-secret"
chmod 0600 "$DIR/turn-secret"

echo "== 4/5 firewall"
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow 4443/tcp
else
    echo "   ufw not active; ensure 4443/tcp is reachable (cloud firewall?)"
fi

echo "== 5/5 allow user services to run at boot"
loginctl enable-linger "$APP_USER"

echo "root-setup: done. Continue (as $APP_USER):"
echo "  systemctl --user enable --now lobbylink"
