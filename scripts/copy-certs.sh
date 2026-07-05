#!/bin/sh
# copy-certs.sh <domain> [app-user]
# Copies Let's Encrypt certs into the app dir so the p2plobby user and
# coturn can read them. Root-only; install as a certbot deploy hook:
#
#   install -m 0755 scripts/copy-certs.sh /usr/local/sbin/p2p-lobby-copy-certs
#   cat >/etc/letsencrypt/renewal-hooks/deploy/p2p-lobby-copy-certs <<'HOOK'
#   #!/bin/sh
#   /usr/local/sbin/p2p-lobby-copy-certs <domain> p2plobby
#   HOOK
#   chmod +x /etc/letsencrypt/renewal-hooks/deploy/p2p-lobby-copy-certs
set -eu
DOMAIN="${1:?domain}"
APP_USER="${2:-p2plobby}"
DEST="/var/lib/p2p-lobby/certs"

install -d -m 0750 -o "$APP_USER" -g "$APP_USER" "$DEST"
install -m 0640 -o "$APP_USER" -g "$APP_USER" \
  "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" "$DEST/fullchain.pem"
install -m 0640 -o "$APP_USER" -g "$APP_USER" \
  "/etc/letsencrypt/live/$DOMAIN/privkey.pem" "$DEST/privkey.pem"

systemctl restart p2p-lobby || true
systemctl restart coturn || systemctl restart turnserver || true
