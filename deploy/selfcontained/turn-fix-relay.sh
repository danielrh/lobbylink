#!/bin/sh
# turn-fix-relay.sh [public-ip]
# Step 3 of deploy/selfcontained/README.md. Run as root. Idempotent.
# Pins coturn's relay allocations to the public interface; otherwise
# coturn round-robins across all local IPs and hands out private
# relay addresses that internet peers cannot reach.
#
# If the server sits behind 1:1 NAT (public IP not on any local
# interface), additionally add: external-ip=<public>/<private>
set -eu
CONF=/etc/turnserver.conf

PUBLIC_IP="${1:-}"
if [ -z "$PUBLIC_IP" ]; then
    PUBLIC_IP="$(ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -1)"
fi
[ -n "$PUBLIC_IP" ] || { echo "could not detect public IP; pass it as an argument" >&2; exit 1; }

if grep -q '^relay-ip=' "$CONF"; then
    echo "relay-ip already set:"
    grep '^relay-ip=' "$CONF"
else
    cp -a "$CONF" "$CONF.bak.$(date +%Y%m%d%H%M%S)"
    {
        echo ""
        echo "# Added by lobbylink turn-fix-relay.sh: only allocate relays"
        echo "# on the public interface."
        echo "relay-ip=$PUBLIC_IP"
    } >> "$CONF"
    systemctl restart coturn
    echo "relay-ip=$PUBLIC_IP added; coturn restarted"
fi
