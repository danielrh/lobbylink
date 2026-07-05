// Package config loads and validates server configuration from a TOML
// file (subset parser in toml.go) with CLI-flag overrides applied on
// top. Precedence: defaults < config file < flags.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config is the fully-resolved server configuration.
type Config struct {
	Server   Server
	Security Security
	Turn     Turn
	Rooms    Rooms
	Apps     []App
}

type Server struct {
	PublicURL      string
	ListenHTTP     string // e.g. "127.0.0.1:8787"; empty disables
	ListenHTTPS    string // e.g. ":4443"; empty disables
	Cert           string
	Key            string
	BehindProxy    bool
	TrustedProxies []string
	LogLevel       string // debug|info|warn|error
}

type Security struct {
	AllowedOrigins    []string
	MaxWSMessageBytes int64
	// AllowNoOrigin permits WebSocket connections without an Origin
	// header (native clients). Off by default.
	AllowNoOrigin bool
}

type Turn struct {
	Enabled          bool
	Realm            string
	SharedSecretFile string
	TTL              time.Duration
	URLs             []string

	// Secret is loaded from SharedSecretFile at startup.
	Secret []byte
}

type Rooms struct {
	EmptyTTL       time.Duration
	MaxTTL         time.Duration
	MaxRooms       int
	MaxPlayersHard int
	ClaimAfter     time.Duration
	GCInterval     time.Duration
}

type App struct {
	ID             string
	AllowedOrigins []string
	MaxPlayersMax  int
	AllowTurn      bool
}

// Default returns the built-in defaults matching the implementation guide.
func Default() Config {
	return Config{
		Server: Server{
			TrustedProxies: []string{"127.0.0.1"},
			LogLevel:       "info",
		},
		Security: Security{
			MaxWSMessageBytes: 1 << 20,
		},
		Turn: Turn{
			TTL: time.Hour,
		},
		Rooms: Rooms{
			EmptyTTL:       5 * time.Minute,
			MaxTTL:         24 * time.Hour,
			MaxRooms:       10000,
			MaxPlayersHard: 32,
			ClaimAfter:     40 * time.Second,
			GCInterval:     30 * time.Second,
		},
	}
}

// LoadFile parses the TOML file at path into cfg (which should start as
// Default()). Unknown sections or keys are errors.
func LoadFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	return loadTOML(string(data), cfg)
}

func loadTOML(src string, cfg *Config) error {
	doc, err := parseTOML(src)
	if err != nil {
		return err
	}
	for section, kv := range doc.tables {
		if len(kv) == 0 {
			continue
		}
		b := &binder{section: section, kv: kv}
		switch section {
		case "server":
			b.str("public_url", &cfg.Server.PublicURL)
			b.str("listen_http", &cfg.Server.ListenHTTP)
			b.str("listen_https", &cfg.Server.ListenHTTPS)
			b.str("cert", &cfg.Server.Cert)
			b.str("key", &cfg.Server.Key)
			b.boolean("behind_proxy", &cfg.Server.BehindProxy)
			b.strs("trusted_proxies", &cfg.Server.TrustedProxies)
			b.str("log_level", &cfg.Server.LogLevel)
		case "security":
			b.strs("allowed_origins", &cfg.Security.AllowedOrigins)
			b.integer("max_ws_message_bytes", &cfg.Security.MaxWSMessageBytes)
			b.boolean("allow_no_origin", &cfg.Security.AllowNoOrigin)
		case "turn":
			b.boolean("enabled", &cfg.Turn.Enabled)
			b.str("realm", &cfg.Turn.Realm)
			b.str("shared_secret_file", &cfg.Turn.SharedSecretFile)
			b.duration("ttl", &cfg.Turn.TTL)
			b.strs("urls", &cfg.Turn.URLs)
		case "rooms":
			b.duration("empty_ttl", &cfg.Rooms.EmptyTTL)
			b.duration("max_ttl", &cfg.Rooms.MaxTTL)
			b.intVal("max_rooms", &cfg.Rooms.MaxRooms)
			b.intVal("max_players_hard", &cfg.Rooms.MaxPlayersHard)
			b.duration("claim_after", &cfg.Rooms.ClaimAfter)
			b.duration("gc_interval", &cfg.Rooms.GCInterval)
			// default_reconnect_policy appears in the guide's example
			// config; accepted and checked but the protocol-level
			// default already matches it.
			var policy string
			b.str("default_reconnect_policy", &policy)
			if b.err == nil && policy != "" && policy != "token-or-claim-after-timeout" {
				b.err = fmt.Errorf("config [rooms]: default_reconnect_policy %q not supported (only token-or-claim-after-timeout)", policy)
			}
		case "":
			return fmt.Errorf("config: top-level keys not allowed: %v", firstKey(kv))
		default:
			return fmt.Errorf("config: unknown section [%s]", section)
		}
		if b.err != nil {
			return b.err
		}
		if extra := b.unused(); extra != "" {
			return fmt.Errorf("config [%s]: unknown key %q", section, extra)
		}
	}
	for name, entries := range doc.arrays {
		if name != "apps" {
			return fmt.Errorf("config: unknown array section [[%s]]", name)
		}
		for _, kv := range entries {
			app := App{AllowTurn: true}
			b := &binder{section: "apps", kv: kv}
			b.str("id", &app.ID)
			b.strs("allowed_origins", &app.AllowedOrigins)
			b.intVal("max_players_max", &app.MaxPlayersMax)
			b.boolean("allow_turn", &app.AllowTurn)
			if b.err != nil {
				return b.err
			}
			if extra := b.unused(); extra != "" {
				return fmt.Errorf("config [[apps]]: unknown key %q", extra)
			}
			if app.ID == "" {
				return fmt.Errorf("config [[apps]]: id is required")
			}
			cfg.Apps = append(cfg.Apps, app)
		}
	}
	return nil
}

// Validate checks cross-field invariants and loads the TURN secret.
func (c *Config) Validate() error {
	if c.Server.ListenHTTP == "" && c.Server.ListenHTTPS == "" {
		return fmt.Errorf("no listener configured: set listen_http and/or listen_https")
	}
	if c.Server.ListenHTTPS != "" {
		if c.Server.Cert == "" || c.Server.Key == "" {
			return fmt.Errorf("listen_https requires cert and key")
		}
	}
	if c.Server.PublicURL != "" {
		u, err := url.Parse(c.Server.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("public_url must be http(s)://host[:port], got %q", c.Server.PublicURL)
		}
	}
	for _, o := range c.Security.AllowedOrigins {
		if err := validateOrigin(o); err != nil {
			return err
		}
	}
	for _, a := range c.Apps {
		for _, o := range a.AllowedOrigins {
			if err := validateOrigin(o); err != nil {
				return fmt.Errorf("app %q: %w", a.ID, err)
			}
		}
		if a.MaxPlayersMax < 0 || a.MaxPlayersMax > c.Rooms.MaxPlayersHard {
			return fmt.Errorf("app %q: max_players_max %d out of range [0,%d]", a.ID, a.MaxPlayersMax, c.Rooms.MaxPlayersHard)
		}
	}
	for _, p := range c.Server.TrustedProxies {
		if net.ParseIP(p) == nil {
			return fmt.Errorf("trusted proxy %q is not a valid IP", p)
		}
	}
	if c.Security.MaxWSMessageBytes < 4096 {
		return fmt.Errorf("max_ws_message_bytes must be >= 4096")
	}
	if c.Rooms.MaxPlayersHard < 1 || c.Rooms.MaxPlayersHard > 1024 {
		return fmt.Errorf("max_players_hard out of range [1,1024]")
	}
	if c.Rooms.MaxRooms < 1 {
		return fmt.Errorf("max_rooms must be >= 1")
	}
	if c.Rooms.EmptyTTL <= 0 || c.Rooms.MaxTTL <= 0 || c.Rooms.GCInterval <= 0 {
		return fmt.Errorf("room TTLs and gc_interval must be positive")
	}
	if c.Rooms.ClaimAfter < 0 {
		return fmt.Errorf("claim_after must be >= 0")
	}
	switch c.Server.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be debug|info|warn|error, got %q", c.Server.LogLevel)
	}
	if c.Turn.Enabled {
		if c.Turn.SharedSecretFile == "" {
			return fmt.Errorf("turn.enabled requires shared_secret_file")
		}
		if len(c.Turn.URLs) == 0 {
			return fmt.Errorf("turn.enabled requires urls")
		}
		if c.Turn.TTL <= 0 {
			return fmt.Errorf("turn.ttl must be positive")
		}
		secret, err := os.ReadFile(c.Turn.SharedSecretFile)
		if err != nil {
			return fmt.Errorf("read turn secret: %w", err)
		}
		c.Turn.Secret = []byte(strings.TrimSpace(string(secret)))
		if len(c.Turn.Secret) < 16 {
			return fmt.Errorf("turn secret too short (want >= 16 bytes)")
		}
	}
	seen := map[string]bool{}
	for _, a := range c.Apps {
		if seen[a.ID] {
			return fmt.Errorf("duplicate app id %q", a.ID)
		}
		seen[a.ID] = true
	}
	return nil
}

// AppByID returns the app policy for id, or nil.
func (c *Config) AppByID(id string) *App {
	for i := range c.Apps {
		if c.Apps[i].ID == id {
			return &c.Apps[i]
		}
	}
	return nil
}

// OriginAllowed reports whether origin (scheme://host[:port]) is in the
// global allowlist. Matching is exact and case-insensitive on
// scheme/host per RFC 6454 serialization conventions.
func (c *Config) OriginAllowed(origin string) bool {
	return originInList(origin, c.Security.AllowedOrigins)
}

// OriginAllowedForApp reports whether origin is acceptable for the app:
// either globally allowlisted or on the app's own allowlist.
func (c *Config) OriginAllowedForApp(origin string, app *App) bool {
	if c.OriginAllowed(origin) {
		return true
	}
	if app != nil && originInList(origin, app.AllowedOrigins) {
		return true
	}
	return false
}

func originInList(origin string, list []string) bool {
	for _, o := range list {
		if strings.EqualFold(strings.TrimSuffix(o, "/"), strings.TrimSuffix(origin, "/")) {
			return true
		}
	}
	return false
}

func validateOrigin(o string) error {
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("allowed origin must be scheme://host[:port], got %q", o)
	}
	return nil
}

func firstKey(m map[string]any) string {
	for k := range m {
		return k
	}
	return ""
}

// binder pulls typed values out of a parsed TOML table, tracking which
// keys were consumed so leftovers can be reported as typos.
type binder struct {
	section string
	kv      map[string]any
	used    map[string]bool
	err     error
}

func (b *binder) take(key string) (any, bool) {
	if b.err != nil {
		return nil, false
	}
	if b.used == nil {
		b.used = map[string]bool{}
	}
	v, ok := b.kv[key]
	if ok {
		b.used[key] = true
	}
	return v, ok
}

func (b *binder) typeErr(key, want string, got any) {
	b.err = fmt.Errorf("config [%s]: %s must be a %s (got %T)", b.section, key, want, got)
}

func (b *binder) str(key string, dst *string) {
	if v, ok := b.take(key); ok {
		s, ok := v.(string)
		if !ok {
			b.typeErr(key, "string", v)
			return
		}
		*dst = s
	}
}

func (b *binder) strs(key string, dst *[]string) {
	if v, ok := b.take(key); ok {
		s, ok := v.([]string)
		if !ok {
			b.typeErr(key, "string array", v)
			return
		}
		*dst = s
	}
}

func (b *binder) boolean(key string, dst *bool) {
	if v, ok := b.take(key); ok {
		x, ok := v.(bool)
		if !ok {
			b.typeErr(key, "boolean", v)
			return
		}
		*dst = x
	}
}

func (b *binder) integer(key string, dst *int64) {
	if v, ok := b.take(key); ok {
		n, ok := v.(int64)
		if !ok {
			b.typeErr(key, "integer", v)
			return
		}
		*dst = n
	}
}

func (b *binder) intVal(key string, dst *int) {
	var n int64 = int64(*dst)
	b.integer(key, &n)
	if b.err == nil {
		*dst = int(n)
	}
}

func (b *binder) duration(key string, dst *time.Duration) {
	if v, ok := b.take(key); ok {
		s, ok := v.(string)
		if !ok {
			b.typeErr(key, `duration string like "300s"`, v)
			return
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			b.err = fmt.Errorf("config [%s]: %s: %v", b.section, key, err)
			return
		}
		if d < 0 {
			b.err = fmt.Errorf("config [%s]: %s must not be negative", b.section, key)
			return
		}
		*dst = d
	}
}

func (b *binder) unused() string {
	for k := range b.kv {
		if !b.used[k] {
			return k
		}
	}
	return ""
}
