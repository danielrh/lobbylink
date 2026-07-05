package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// guideExample is the config file from the implementation guide,
// verbatim (section 2.3) plus its optional app policy block.
const guideExample = `
[server]
public_url = "https://pqrstuvw.xyz:4443" # Apache mode: "https://pqrstuvw.xyz"
listen_http = ""                     # Apache/dev: "127.0.0.1:8787"
listen_https = ":4443"               # Apache/dev: ""
cert = "/var/lib/p2p-lobby/certs/fullchain.pem"
key = "/var/lib/p2p-lobby/certs/privkey.pem"
behind_proxy = false
trusted_proxies = ["127.0.0.1"]

[security]
allowed_origins = [
  "https://pqrstuvw.xyz", "https://pqrstuvw.xyz:4443",
  "https://graphics.stanford.edu",
  "https://anderrh.github.io", "https://danielrh.github.io",
  "http://localhost:5173", "http://127.0.0.1:5173"
]
max_ws_message_bytes = 1048576

[turn]
enabled = true
realm = "pqrstuvw.xyz"
shared_secret_file = "/var/lib/p2p-lobby/turn-secret"
ttl = "3600s"
urls = ["stun:pqrstuvw.xyz:3478", "turn:pqrstuvw.xyz:3478?transport=udp", "turn:pqrstuvw.xyz:3478?transport=tcp", "turns:pqrstuvw.xyz:5349?transport=tcp"]

[rooms]
empty_ttl = "300s"
max_ttl = "24h"
max_rooms = 10000
max_players_hard = 32
claim_after = "40s" # sole reclaim timer; silence-based, no required heartbeat
default_reconnect_policy = "token-or-claim-after-timeout"

[[apps]]
id = "anderrh-github"
allowed_origins = ["https://anderrh.github.io"]
max_players_max = 16
allow_turn = true
`

func TestLoadGuideExample(t *testing.T) {
	cfg := Default()
	if err := loadTOML(guideExample, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.PublicURL != "https://pqrstuvw.xyz:4443" ||
		cfg.Server.ListenHTTP != "" || cfg.Server.ListenHTTPS != ":4443" ||
		cfg.Server.Cert != "/var/lib/p2p-lobby/certs/fullchain.pem" ||
		cfg.Server.Key != "/var/lib/p2p-lobby/certs/privkey.pem" ||
		cfg.Server.BehindProxy {
		t.Errorf("server section wrong: %+v", cfg.Server)
	}
	if !reflect.DeepEqual(cfg.Server.TrustedProxies, []string{"127.0.0.1"}) {
		t.Errorf("trusted_proxies wrong: %v", cfg.Server.TrustedProxies)
	}
	wantOrigins := []string{
		"https://pqrstuvw.xyz", "https://pqrstuvw.xyz:4443",
		"https://graphics.stanford.edu",
		"https://anderrh.github.io", "https://danielrh.github.io",
		"http://localhost:5173", "http://127.0.0.1:5173",
	}
	if !reflect.DeepEqual(cfg.Security.AllowedOrigins, wantOrigins) {
		t.Errorf("allowed_origins wrong: %v", cfg.Security.AllowedOrigins)
	}
	if cfg.Security.MaxWSMessageBytes != 1048576 {
		t.Errorf("max_ws_message_bytes = %d", cfg.Security.MaxWSMessageBytes)
	}
	if !cfg.Turn.Enabled || cfg.Turn.Realm != "pqrstuvw.xyz" ||
		cfg.Turn.SharedSecretFile != "/var/lib/p2p-lobby/turn-secret" ||
		cfg.Turn.TTL != time.Hour || len(cfg.Turn.URLs) != 4 {
		t.Errorf("turn section wrong: %+v", cfg.Turn)
	}
	if cfg.Rooms.EmptyTTL != 5*time.Minute || cfg.Rooms.MaxTTL != 24*time.Hour ||
		cfg.Rooms.MaxRooms != 10000 || cfg.Rooms.MaxPlayersHard != 32 ||
		cfg.Rooms.ClaimAfter != 40*time.Second {
		t.Errorf("rooms section wrong: %+v", cfg.Rooms)
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("apps wrong: %+v", cfg.Apps)
	}
	app := cfg.Apps[0]
	if app.ID != "anderrh-github" || app.MaxPlayersMax != 16 || !app.AllowTurn ||
		!reflect.DeepEqual(app.AllowedOrigins, []string{"https://anderrh.github.io"}) {
		t.Errorf("app wrong: %+v", app)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]string{
		"unknown section":     "[nope]\nx = 1\n",
		"unknown key":         "[server]\npublic_uri = \"x\"\n",
		"unknown app key":     "[[apps]]\nid = \"a\"\nbogus = 1\n",
		"top-level key":       "public_url = \"x\"\n",
		"bad duration":        "[rooms]\nempty_ttl = \"5 parsecs\"\n",
		"negative duration":   "[rooms]\nempty_ttl = \"-5s\"\n",
		"wrong type str":      "[server]\npublic_url = 5\n",
		"wrong type bool":     "[turn]\nenabled = \"yes\"\n",
		"wrong type arr":      "[security]\nallowed_origins = \"https://x\"\n",
		"duplicate key":       "[server]\ncert = \"a\"\ncert = \"b\"\n",
		"duplicate table":     "[server]\n[server]\n",
		"unterminated string": "[server]\npublic_url = \"oops\n",
		"unterminated array":  "[security]\nallowed_origins = [\"a\",\n",
		"non-string array":    "[security]\nallowed_origins = [1,2]\n",
		"missing value":       "[server]\npublic_url =\n",
		"no equals":           "[server]\npublic_url\n",
		"float value":         "[rooms]\nmax_rooms = 1.5\n",
		"missing app id":      "[[apps]]\nallow_turn = true\n",
		"bad policy":          "[rooms]\ndefault_reconnect_policy = \"token-only\"\n",
		"unknown array table": "[[widgets]]\nid = \"x\"\n",
	}
	for name, src := range cases {
		cfg := Default()
		if err := loadTOML(src, &cfg); err == nil {
			t.Errorf("%s: no error for %q", name, src)
		}
	}
}

func TestTOMLQuirks(t *testing.T) {
	cfg := Default()
	src := `
# leading comment
[server]
public_url = "https://x.example" # trailing comment
log_level = "debug"

[security]
allow_no_origin = true
allowed_origins = [
  # comment inside array
  "https://a.example",
  "https://b.example", # more
]
max_ws_message_bytes = 1_048_576
`
	if err := loadTOML(src, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.PublicURL != "https://x.example" || cfg.Server.LogLevel != "debug" {
		t.Errorf("server wrong: %+v", cfg.Server)
	}
	if !reflect.DeepEqual(cfg.Security.AllowedOrigins, []string{"https://a.example", "https://b.example"}) {
		t.Errorf("origins wrong: %v", cfg.Security.AllowedOrigins)
	}
	if cfg.Security.MaxWSMessageBytes != 1048576 || !cfg.Security.AllowNoOrigin {
		t.Errorf("security wrong: %+v", cfg.Security)
	}

	// Escapes and a # inside a string value.
	cfg2 := Default()
	src2 := "[server]\npublic_url = \"https://x.example/#frag\\\"q\\\\\"\n"
	if err := loadTOML(src2, &cfg2); err != nil {
		t.Fatal(err)
	}
	if cfg2.Server.PublicURL != `https://x.example/#frag"q\` {
		t.Errorf("escaped string wrong: %q", cfg2.Server.PublicURL)
	}
}

func validBase(t *testing.T) Config {
	t.Helper()
	cfg := Default()
	cfg.Server.ListenHTTP = "127.0.0.1:8787"
	cfg.Security.AllowedOrigins = []string{"https://ok.example"}
	return cfg
}

func TestValidate(t *testing.T) {
	cfg := validBase(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid base rejected: %v", err)
	}

	c := validBase(t)
	c.Server.ListenHTTP = ""
	if err := c.Validate(); err == nil {
		t.Error("no listener accepted")
	}

	c = validBase(t)
	c.Server.ListenHTTPS = ":4443"
	if err := c.Validate(); err == nil {
		t.Error("https without cert accepted")
	}

	c = validBase(t)
	c.Server.PublicURL = "not a url"
	if err := c.Validate(); err == nil {
		t.Error("bad public_url accepted")
	}

	c = validBase(t)
	c.Security.AllowedOrigins = []string{"ftp://x"}
	if err := c.Validate(); err == nil {
		t.Error("bad origin scheme accepted")
	}

	c = validBase(t)
	c.Security.AllowedOrigins = []string{"https://x.example/path"}
	if err := c.Validate(); err == nil {
		t.Error("origin with path accepted")
	}

	c = validBase(t)
	c.Server.TrustedProxies = []string{"not-an-ip"}
	if err := c.Validate(); err == nil {
		t.Error("bad trusted proxy accepted")
	}

	c = validBase(t)
	c.Security.MaxWSMessageBytes = 10
	if err := c.Validate(); err == nil {
		t.Error("tiny message limit accepted")
	}

	c = validBase(t)
	c.Server.LogLevel = "loud"
	if err := c.Validate(); err == nil {
		t.Error("bad log level accepted")
	}

	c = validBase(t)
	c.Apps = []App{{ID: "a"}, {ID: "a"}}
	if err := c.Validate(); err == nil {
		t.Error("duplicate app ids accepted")
	}

	c = validBase(t)
	c.Apps = []App{{ID: "a", MaxPlayersMax: 64}}
	if err := c.Validate(); err == nil {
		t.Error("app max players above hard limit accepted")
	}
}

func TestValidateTurnSecret(t *testing.T) {
	c := validBase(t)
	c.Turn.Enabled = true
	c.Turn.URLs = []string{"stun:x:3478"}
	if err := c.Validate(); err == nil {
		t.Error("turn without secret file accepted")
	}

	dir := t.TempDir()
	secretPath := filepath.Join(dir, "turn-secret")
	if err := os.WriteFile(secretPath, []byte("s3cret-s3cret-s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Turn.SharedSecretFile = secretPath
	if err := c.Validate(); err != nil {
		t.Fatalf("valid turn config rejected: %v", err)
	}
	if string(c.Turn.Secret) != "s3cret-s3cret-s3cret" {
		t.Errorf("secret not trimmed/loaded: %q", c.Turn.Secret)
	}

	if err := os.WriteFile(secretPath, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Errorf("short secret accepted: %v", err)
	}
}

func TestOriginMatching(t *testing.T) {
	c := validBase(t)
	c.Security.AllowedOrigins = []string{"https://Ok.Example", "http://localhost:5173"}
	c.Apps = []App{{ID: "app1", AllowedOrigins: []string{"https://app.example"}}}

	if !c.OriginAllowed("https://ok.example") {
		t.Error("case-insensitive match failed")
	}
	if !c.OriginAllowed("http://localhost:5173") {
		t.Error("exact match failed")
	}
	if c.OriginAllowed("https://ok.example:8443") {
		t.Error("different port matched")
	}
	if c.OriginAllowed("https://evil.example") {
		t.Error("unlisted origin matched")
	}
	app := c.AppByID("app1")
	if app == nil {
		t.Fatal("AppByID failed")
	}
	if !c.OriginAllowedForApp("https://app.example", app) {
		t.Error("app origin rejected")
	}
	if !c.OriginAllowedForApp("https://ok.example", app) {
		t.Error("global origin rejected for app")
	}
	if c.OriginAllowedForApp("https://evil.example", app) {
		t.Error("unlisted origin allowed for app")
	}
	if c.AppByID("missing") != nil {
		t.Error("AppByID returned phantom app")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[server]\nlisten_http = \"127.0.0.1:0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	if err := LoadFile(path, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenHTTP != "127.0.0.1:0" {
		t.Errorf("listen_http = %q", cfg.Server.ListenHTTP)
	}
	if err := LoadFile(filepath.Join(dir, "missing.toml"), &cfg); err == nil {
		t.Error("missing file accepted")
	}
}

func TestDefaultsPreservedWhenAbsent(t *testing.T) {
	cfg := Default()
	if err := loadTOML("[server]\nlisten_http = \"127.0.0.1:1\"\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Rooms.ClaimAfter != 40*time.Second || cfg.Rooms.EmptyTTL != 5*time.Minute ||
		cfg.Rooms.MaxTTL != 24*time.Hour || cfg.Rooms.MaxRooms != 10000 ||
		cfg.Rooms.MaxPlayersHard != 32 || cfg.Security.MaxWSMessageBytes != 1<<20 {
		t.Errorf("defaults clobbered: %+v %+v", cfg.Rooms, cfg.Security)
	}
}
