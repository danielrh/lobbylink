#!/bin/sh
# setup-apache-proxy.sh <domain>
# Deployment Method 1: Apache owns :443 and reverse-proxies to the Go
# server on 127.0.0.1:8787. Run as root after install-common.sh.
set -eu
DOMAIN="${1:?usage: setup-apache-proxy.sh <domain>}"
SCRIPT_DIR="$(dirname "$0")"
TEMPLATE_DIR="$SCRIPT_DIR/../deploy"

a2enmod ssl headers proxy proxy_http proxy_wstunnel

sed "s|<DOMAIN>|$DOMAIN|g" "$TEMPLATE_DIR/apache-p2p-lobby.conf.template" \
    > "/etc/apache2/sites-available/p2p-lobby.conf"
a2ensite p2p-lobby
apachectl configtest
systemctl reload apache2

"$SCRIPT_DIR/install-systemd-service.sh" \
    "--config /etc/p2p-lobby/config.toml --listen-http 127.0.0.1:8787 --listen-https '' --behind-proxy --public-url https://$DOMAIN"

echo "setup-apache-proxy: done; verify with: curl -I https://$DOMAIN/healthz"
