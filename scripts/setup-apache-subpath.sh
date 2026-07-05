#!/bin/sh
# setup-apache-subpath.sh <vhost-file> [base-path]
# Adds a LobbyLink reverse proxy under base-path (default /lobbylink)
# to an EXISTING Apache TLS vhost, e.g.:
#   setup-apache-subpath.sh /etc/apache2/sites-available/000-default-le-ssl.conf /lobbylink
# Run as root. Idempotent; takes a timestamped vhost backup.
# The Go server must listen on 127.0.0.1:8787 with behind_proxy=true.
set -eu
VHOST="${1:?usage: setup-apache-subpath.sh <vhost-file> [base-path]}"
BASE="${2:-/lobbylink}"
PROXYCONF="/etc/apache2/lobbylink-proxy.conf"
TEMPLATE="$(dirname "$0")/../deploy/apache-lobbylink-subpath.conf.template"

case "$BASE" in
    /*) ;;
    *) echo "base-path must start with /" >&2; exit 1 ;;
esac

a2enmod -q proxy proxy_http proxy_wstunnel headers
sed "s|<BASE_PATH>|$BASE|g" "$TEMPLATE" > "$PROXYCONF"

if ! grep -q "lobbylink-proxy.conf" "$VHOST"; then
    cp -a "$VHOST" "$VHOST.bak.$(date +%Y%m%d%H%M%S)"
    sed -i "s|</VirtualHost>|\tInclude $PROXYCONF\n</VirtualHost>|" "$VHOST"
fi

apachectl configtest
systemctl reload apache2
echo "setup-apache-subpath: proxying $BASE -> 127.0.0.1:8787"
