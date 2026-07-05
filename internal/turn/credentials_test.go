package turn

import (
	"testing"
	"time"

	"github.com/danielrh/lobbylink/internal/protocol"
)

// Reference vectors computed independently (Python hmac/hashlib):
//
//	HMAC-SHA1(key="secret0123456789", "1783300000:room-XYYYZZ-player-0")
//	  -> base64 "vF418BTAA1YHuRGAUcRQLtqfM1I="
//	HMAC-SHA1(key="secret0123456789", "1783300000:room-ABCD-player-3")
//	  -> base64 "uVhseB9md/fnlLZ+8z724/V0f94="
var vectorSecret = []byte("secret0123456789")

func TestCredentialsKnownVectors(t *testing.T) {
	// now + ttl must land exactly on the vector expiry of 1783300000.
	now := time.Unix(1783300000-3600, 0)
	ttl := time.Hour

	user, pass := Credentials(vectorSecret, ttl, "XYYYZZ", 0, now)
	if user != "1783300000:room-XYYYZZ-player-0" {
		t.Errorf("username = %q", user)
	}
	if pass != "vF418BTAA1YHuRGAUcRQLtqfM1I=" {
		t.Errorf("password = %q", pass)
	}

	user, pass = Credentials(vectorSecret, ttl, "ABCD", 3, now)
	if user != "1783300000:room-ABCD-player-3" {
		t.Errorf("username = %q", user)
	}
	if pass != "uVhseB9md/fnlLZ+8z724/V0f94=" {
		t.Errorf("password = %q", pass)
	}
}

func TestICEServersSplit(t *testing.T) {
	urls := []string{
		"stun:example.com:3478",
		"turn:example.com:3478?transport=udp",
		"turn:example.com:3478?transport=tcp",
		"turns:example.com:5349?transport=tcp",
	}
	now := time.Unix(1783300000-3600, 0)
	servers := ICEServers(urls, vectorSecret, time.Hour, "XYYYZZ", 0, now)
	if len(servers) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(servers), servers)
	}
	stun := servers[0]
	if len(stun.URLs) != 1 || stun.URLs[0] != "stun:example.com:3478" || stun.Username != "" || stun.Credential != "" {
		t.Errorf("stun entry wrong: %+v", stun)
	}
	trn := servers[1]
	if len(trn.URLs) != 3 {
		t.Errorf("turn urls wrong: %+v", trn.URLs)
	}
	if trn.Username != "1783300000:room-XYYYZZ-player-0" || trn.Credential != "vF418BTAA1YHuRGAUcRQLtqfM1I=" {
		t.Errorf("turn credentials wrong: %+v", trn)
	}
}

func TestICEServersEdgeCases(t *testing.T) {
	now := time.Now()
	if got := ICEServers(nil, vectorSecret, time.Hour, "X", 0, now); got != nil {
		t.Errorf("nil urls: want nil, got %+v", got)
	}
	only := ICEServers([]string{"stun:h:3478"}, vectorSecret, time.Hour, "X", 0, now)
	if len(only) != 1 || only[0].Username != "" {
		t.Errorf("stun-only wrong: %+v", only)
	}
	var _ []protocol.ICEServer = only
}
