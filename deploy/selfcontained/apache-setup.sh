#!/bin/sh
# apache-setup.sh [vhost-file] [base-path]
# Step 4 of deploy/selfcontained/README.md. Run as root. Idempotent.
# Adds a reverse proxy for base-path (default /lobbylink) to an
# EXISTING Apache TLS vhost (default: certbot's converted default
# vhost). Only one Include line is added to the vhost, with a
# timestamped backup and configtest before reload.
set -eu

VHOST="${1:-/etc/apache2/sites-available/000-default-le-ssl.conf}"
BASE="${2:-/lobbylink}"
PROXYCONF=/etc/apache2/lobbylink-proxy.conf

case "$BASE" in
    /*) ;;
    *) echo "base-path must start with /" >&2; exit 1 ;;
esac
[ -f "$VHOST" ] || { echo "vhost file $VHOST not found" >&2; exit 1; }

echo "== 1/4 enable modules"
a2enmod -q proxy proxy_http proxy_wstunnel headers

echo "== 2/4 write $PROXYCONF"
cat > "$PROXYCONF" <<EOF
# LobbyLink reverse proxy under $BASE -> Go server on 127.0.0.1:8787
# Included from the :443 vhost. Managed by lobbylink apache-setup.sh.
ProxyPreserveHost On
RequestHeader set X-Forwarded-Proto "https"
RequestHeader set X-Forwarded-Port "443"
RequestHeader set X-Forwarded-Prefix "$BASE"
RedirectMatch 301 ^$BASE\$ $BASE/
ProxyPass        "$BASE/ws" "ws://127.0.0.1:8787/ws"
ProxyPassReverse "$BASE/ws" "ws://127.0.0.1:8787/ws"
ProxyPass        "$BASE/"   "http://127.0.0.1:8787/"
ProxyPassReverse "$BASE/"   "http://127.0.0.1:8787/"
EOF

echo "== 3/4 include from the TLS vhost"
if grep -q "lobbylink-proxy.conf" "$VHOST"; then
    echo "   already included"
else
    cp -a "$VHOST" "$VHOST.bak.$(date +%Y%m%d%H%M%S)"
    sed -i "s|</VirtualHost>|\tInclude $PROXYCONF\n</VirtualHost>|" "$VHOST"
    echo "   added Include (backup: $VHOST.bak.*)"
fi

echo "== 4/4 configtest + reload"
apachectl configtest
systemctl reload apache2

echo "apache-setup: done. Verify: curl -s https://<domain>$BASE/healthz"
