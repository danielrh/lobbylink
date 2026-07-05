#!/bin/sh
# turn-diag.sh [app-user]
# Read-only coturn diagnostics: active config lines (secrets
# redacted), whether the lobbylink secret is configured, recent logs.
# Run as root.
set -eu
APP_USER="${1:-${SUDO_USER:-$(id -un)}}"
APP_HOME="$(getent passwd "$APP_USER" | cut -d: -f6)"

echo "== active turnserver.conf lines =="
grep -nE '^[^#[:space:]]' /etc/turnserver.conf | sed 's/\(static-auth-secret=\).*/\1REDACTED/'
echo
echo "== static-auth-secret lines present: $(grep -c '^static-auth-secret=' /etc/turnserver.conf || true)"
if [ -f "$APP_HOME/lobbylink/turn-secret" ] &&
   grep -q "^static-auth-secret=$(cat "$APP_HOME/lobbylink/turn-secret")\$" /etc/turnserver.conf; then
    echo "== lobbylink turn-secret IS one of them"
else
    echo "== lobbylink turn-secret NOT found in turnserver.conf"
fi
echo
echo "== recent turnserver logs =="
LOG=$(sed -n 's/^log-file=//p' /etc/turnserver.conf | head -1)
if [ -n "${LOG:-}" ] && [ -f "$LOG" ]; then
    tail -25 "$LOG"
else
    journalctl -u coturn -n 25 --no-pager 2>/dev/null || echo "no logs found"
fi
