// Package turn mints temporary TURN credentials compatible with
// coturn's REST-API long-term credential mechanism (use-auth-secret /
// static-auth-secret).
package turn

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/danielrh/lobbylink/internal/protocol"
)

// Credentials returns the ephemeral username/password pair for one
// player in one room, valid for ttl from now:
//
//	username = "<unix expiry>:room-<code>-player-<id>"
//	password = base64(HMAC-SHA1(secret, username))
//
// coturn validates the password with the shared secret and rejects the
// pair after expiry.
func Credentials(secret []byte, ttl time.Duration, roomCode string, playerID int, now time.Time) (username, password string) {
	expiry := now.Add(ttl).Unix()
	username = fmt.Sprintf("%d:room-%s-player-%d", expiry, roomCode, playerID)
	mac := hmac.New(sha1.New, secret)
	mac.Write([]byte(username))
	password = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return username, password
}

// ICEServers assembles the joined.iceServers list: one credential-less
// entry for stun: URLs and one credentialed entry for turn:/turns:
// URLs. Returns nil if urls is empty.
func ICEServers(urls []string, secret []byte, ttl time.Duration, roomCode string, playerID int, now time.Time) []protocol.ICEServer {
	var stun, turn []string
	for _, u := range urls {
		switch {
		case strings.HasPrefix(u, "stun:"), strings.HasPrefix(u, "stuns:"):
			stun = append(stun, u)
		case strings.HasPrefix(u, "turn:"), strings.HasPrefix(u, "turns:"):
			turn = append(turn, u)
		}
	}
	var servers []protocol.ICEServer
	if len(stun) > 0 {
		servers = append(servers, protocol.ICEServer{URLs: stun})
	}
	if len(turn) > 0 {
		user, pass := Credentials(secret, ttl, roomCode, playerID, now)
		servers = append(servers, protocol.ICEServer{URLs: turn, Username: user, Credential: pass})
	}
	return servers
}
