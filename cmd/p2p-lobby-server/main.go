// Command p2p-lobby-server is the HTTPS/HTTP static server, lobby
// manager, WebRTC signaling relay, and TURN credential issuer.
//
// Configuration precedence: built-in defaults < --config TOML file <
// explicitly-set CLI flags.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/danielrh/lobbylink/internal/config"
	"github.com/danielrh/lobbylink/internal/lobby"
	"github.com/danielrh/lobbylink/internal/server"
)

// version is stamped by the build: -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "p2p-lobby-server:", err)
		os.Exit(1)
	}
}

// listFlag accumulates repeatable, comma-separable string flags.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func run() error {
	cfg := config.Default()

	fs := flag.NewFlagSet("p2p-lobby-server", flag.ExitOnError)
	configPath := fs.String("config", "", "path to TOML config file")
	listenHTTP := fs.String("listen-http", "", "plain HTTP listen address, e.g. 127.0.0.1:8787")
	listenHTTPS := fs.String("listen-https", "", "HTTPS listen address, e.g. :4443")
	cert := fs.String("cert", "", "TLS certificate (fullchain.pem)")
	key := fs.String("key", "", "TLS private key (privkey.pem)")
	publicURL := fs.String("public-url", "", "public base URL, e.g. https://example.com:4443")
	var allowedOrigins listFlag
	fs.Var(&allowedOrigins, "allowed-origin", "allowed WebSocket/CORS origin (repeatable or comma-separated)")
	behindProxy := fs.Bool("behind-proxy", false, "trust X-Forwarded-* from trusted proxies")
	var trustedProxies listFlag
	fs.Var(&trustedProxies, "trusted-proxy", "trusted proxy IP (repeatable or comma-separated)")
	allowNoOrigin := fs.Bool("allow-no-origin", false, "accept WebSocket connections without an Origin header (native clients)")
	turnEnabled := fs.Bool("turn-enabled", false, "issue TURN credentials in join responses")
	turnRealm := fs.String("turn-realm", "", "TURN realm")
	turnSecretFile := fs.String("turn-shared-secret-file", "", "file holding the coturn static-auth-secret")
	turnTTL := fs.Duration("turn-ttl", time.Hour, "TURN credential lifetime")
	var turnURLs listFlag
	fs.Var(&turnURLs, "turn-urls", "STUN/TURN URLs (comma-separated)")
	roomEmptyTTL := fs.Duration("room-empty-ttl", 5*time.Minute, "destroy rooms with no connections after this")
	roomMaxTTL := fs.Duration("room-max-ttl", 24*time.Hour, "destroy any room this long after creation")
	maxRooms := fs.Int("max-rooms", 10000, "maximum concurrent rooms")
	claimAfter := fs.Duration("claim-after", 40*time.Second, "default silence before a slot may be claimed")
	logLevel := fs.String("log-level", "", "debug|info|warn|error")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	if *configPath != "" {
		if err := config.LoadFile(*configPath, &cfg); err != nil {
			return err
		}
	}

	// Apply only flags the user actually set, so config values survive.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen-http":
			cfg.Server.ListenHTTP = *listenHTTP
		case "listen-https":
			cfg.Server.ListenHTTPS = *listenHTTPS
		case "cert":
			cfg.Server.Cert = *cert
		case "key":
			cfg.Server.Key = *key
		case "public-url":
			cfg.Server.PublicURL = *publicURL
		case "allowed-origin":
			cfg.Security.AllowedOrigins = allowedOrigins
		case "behind-proxy":
			cfg.Server.BehindProxy = *behindProxy
		case "trusted-proxy":
			cfg.Server.TrustedProxies = trustedProxies
		case "allow-no-origin":
			cfg.Security.AllowNoOrigin = *allowNoOrigin
		case "turn-enabled":
			cfg.Turn.Enabled = *turnEnabled
		case "turn-realm":
			cfg.Turn.Realm = *turnRealm
		case "turn-shared-secret-file":
			cfg.Turn.SharedSecretFile = *turnSecretFile
		case "turn-ttl":
			cfg.Turn.TTL = *turnTTL
		case "turn-urls":
			cfg.Turn.URLs = turnURLs
		case "room-empty-ttl":
			cfg.Rooms.EmptyTTL = *roomEmptyTTL
		case "room-max-ttl":
			cfg.Rooms.MaxTTL = *roomMaxTTL
		case "max-rooms":
			cfg.Rooms.MaxRooms = *maxRooms
		case "claim-after":
			cfg.Rooms.ClaimAfter = *claimAfter
		case "log-level":
			cfg.Server.LogLevel = *logLevel
		}
	})

	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := newLogger(cfg.Server.LogLevel)
	mgr := lobby.NewManager(lobby.Limits{
		EmptyTTL: cfg.Rooms.EmptyTTL,
		MaxTTL:   cfg.Rooms.MaxTTL,
		MaxRooms: cfg.Rooms.MaxRooms,
	}, time.Now)
	srv := server.New(&cfg, mgr, logger, version)
	handler := srv.Handler()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		ticker := time.NewTicker(cfg.Rooms.GCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := mgr.GC(); n > 0 {
					logger.Info("room gc", "destroyed", n, "remaining", mgr.RoomCount())
				}
			}
		}
	}()

	errCh := make(chan error, 2)
	var servers []*http.Server

	if cfg.Server.ListenHTTP != "" {
		hs := &http.Server{
			Addr:              cfg.Server.ListenHTTP,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}
		servers = append(servers, hs)
		ln, err := net.Listen("tcp", cfg.Server.ListenHTTP)
		if err != nil {
			return err
		}
		logger.Info("listening", "mode", "http", "addr", ln.Addr().String())
		go func() { errCh <- hs.Serve(ln) }()
	}
	if cfg.Server.ListenHTTPS != "" {
		hs := &http.Server{
			Addr:              cfg.Server.ListenHTTPS,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}
		servers = append(servers, hs)
		ln, err := net.Listen("tcp", cfg.Server.ListenHTTPS)
		if err != nil {
			return err
		}
		logger.Info("listening", "mode", "https", "addr", ln.Addr().String())
		go func() { errCh <- hs.ServeTLS(ln, cfg.Server.Cert, cfg.Server.Key) }()
	}

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, hs := range servers {
		_ = hs.Shutdown(shutdownCtx)
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
