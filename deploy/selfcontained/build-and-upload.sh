#!/bin/sh
# build-and-upload.sh <user@host> [domain]
# Step 0 of deploy/selfcontained/README.md. Run locally; no sudo.
# Builds the static server binary and pushes the full ~/lobbylink
# bundle to the server. Safe to re-run for upgrades: the existing
# remote config.toml is never overwritten and the service is restarted
# only if already running.
set -eu

TARGET="${1:?usage: build-and-upload.sh <user@host> [domain]}"
DOMAIN="${2:-${TARGET#*@}}"
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
VERSION="$(git -C "$REPO" describe --always --dirty 2>/dev/null || date +%Y%m%d%H%M)"

echo "== build (version $VERSION)"
CGO_ENABLED=0 go build -C "$REPO" -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "$REPO/dist/p2p-lobby-server" ./cmd/p2p-lobby-server

echo "== upload bundle"
ssh "$TARGET" 'mkdir -p ~/lobbylink ~/.config/systemd/user'
scp -q "$REPO/dist/p2p-lobby-server" "$TARGET:lobbylink/p2p-lobby-server.new"
scp -q "$HERE/root-setup.sh" "$HERE/enable-turn-auth.sh" "$HERE/turn-fix-relay.sh" \
    "$HERE/apache-setup.sh" "$HERE/turn-diag.sh" "$TARGET:lobbylink/"
scp -q "$HERE/lobbylink.service" "$TARGET:.config/systemd/user/lobbylink.service"

echo "== render config.toml (first upload only)"
REMOTE_HOME="$(ssh "$TARGET" 'printf %s "$HOME"')"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
sed "s|__DOMAIN__|$DOMAIN|g; s|__HOME__|$REMOTE_HOME|g" "$HERE/config.toml.template" > "$TMP"
if ssh "$TARGET" 'test -f ~/lobbylink/config.toml'; then
    echo "   remote config.toml exists; leaving it alone"
else
    scp -q "$TMP" "$TARGET:lobbylink/config.toml"
    echo "   installed fresh config.toml for $DOMAIN"
fi

echo "== activate"
ssh "$TARGET" 'export XDG_RUNTIME_DIR=/run/user/$(id -u)
chmod +x ~/lobbylink/*.sh ~/lobbylink/p2p-lobby-server.new
mv ~/lobbylink/p2p-lobby-server.new ~/lobbylink/p2p-lobby-server
systemctl --user daemon-reload
if systemctl --user is-active --quiet lobbylink; then
    systemctl --user restart lobbylink && echo "   service restarted"
else
    echo "   service not running yet; continue with root-setup.sh then: systemctl --user enable --now lobbylink"
fi
~/lobbylink/p2p-lobby-server --version'
