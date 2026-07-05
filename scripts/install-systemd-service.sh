#!/bin/sh
# install-systemd-service.sh "<server args>"
# Renders deploy/p2p-lobby.service.template with the given ExecStart
# arguments and enables the service. Run as root.
set -eu
ARGS="${1:?usage: install-systemd-service.sh \"<server args>\"}"
TEMPLATE="$(dirname "$0")/../deploy/p2p-lobby.service.template"

sed "s|<ARGS_RENDERED_BY_SETUP_SCRIPT>|$ARGS|" "$TEMPLATE" \
    > /etc/systemd/system/p2p-lobby.service
systemctl daemon-reload
systemctl enable --now p2p-lobby
systemctl restart p2p-lobby
systemctl --no-pager status p2p-lobby || true
