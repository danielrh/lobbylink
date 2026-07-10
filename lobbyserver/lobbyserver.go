// Package lobbyserver exposes the p2p lobby server as an embeddable library,
// so other binaries can link it in and mount their own HTTP routes alongside
// the lobby ("plugins"). The lobby internals stay private; this façade is the
// supported surface:
//
//	cfg, _ := lobbyserver.LoadConfig("server.toml") // or DefaultConfig()
//	cfg.SetListenHTTP("127.0.0.1:8787")
//	srv, _ := lobbyserver.New(cfg, "v1.2.3")
//	root := myGameRoutes(srv.Handler())            // wrap / extend
//	err := srv.Run(ctx, root)                      // listeners + GC + shutdown
//
// The stock cmd/p2p-lobby-server binary runs through this same package, so
// embedders get exactly the production serving behavior.
package lobbyserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/danielrh/lobbylink/internal/config"
	"github.com/danielrh/lobbylink/internal/lobby"
	"github.com/danielrh/lobbylink/internal/server"
)

// Config wraps the server configuration. Load the full schema from a TOML
// file (same file the stock binary takes via --config); the setters cover the
// fields embedders most often override programmatically.
type Config struct {
	inner config.Config
}

// DefaultConfig returns the built-in defaults.
func DefaultConfig() *Config {
	return &Config{inner: config.Default()}
}

// LoadConfig returns defaults overlaid with the TOML file at path
// (path "" is allowed and yields plain defaults).
func LoadConfig(path string) (*Config, error) {
	cfg := config.Default()
	if path != "" {
		if err := config.LoadFile(path, &cfg); err != nil {
			return nil, err
		}
	}
	return &Config{inner: cfg}, nil
}

// SetListenHTTP sets the plain-HTTP listen address (e.g. "127.0.0.1:8787");
// empty disables the HTTP listener.
func (c *Config) SetListenHTTP(addr string) { c.inner.Server.ListenHTTP = addr }

// SetListenHTTPS sets the TLS listen address and certificate pair; empty addr
// disables the HTTPS listener.
func (c *Config) SetListenHTTPS(addr, certFile, keyFile string) {
	c.inner.Server.ListenHTTPS = addr
	c.inner.Server.Cert = certFile
	c.inner.Server.Key = keyFile
}

// SetPublicURL sets the advertised public base URL.
func (c *Config) SetPublicURL(u string) { c.inner.Server.PublicURL = u }

// SetLogLevel sets the log level: debug|info|warn|error.
func (c *Config) SetLogLevel(level string) { c.inner.Server.LogLevel = level }

// SetAllowedOrigins replaces the global WebSocket/CORS origin allowlist.
func (c *Config) SetAllowedOrigins(origins []string) { c.inner.Security.AllowedOrigins = origins }

// AllowedOrigins returns the global origin allowlist (for plugins that serve
// their own CORS-checked routes next to the lobby).
func (c *Config) AllowedOrigins() []string {
	out := make([]string, len(c.inner.Security.AllowedOrigins))
	copy(out, c.inner.Security.AllowedOrigins)
	return out
}

// Server is a ready-to-run lobby server.
type Server struct {
	cfg     *config.Config
	logger  *slog.Logger
	mgr     *lobby.Manager
	srv     *server.Server
	version string
}

// New validates cfg and wires the lobby manager + HTTP handlers.
func New(cfg *Config, version string) (*Server, error) {
	return FromInternal(&cfg.inner, version)
}

// FromInternal builds a Server directly from the module-internal config type.
// Only code inside this module can construct the argument; external embedders
// use New. (This is what lets cmd/p2p-lobby-server share the exact code path.)
func FromInternal(cfg *config.Config, version string) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := NewLogger(cfg.Server.LogLevel)
	mgr := lobby.NewManager(lobby.Limits{
		EmptyTTL: cfg.Rooms.EmptyTTL,
		MaxTTL:   cfg.Rooms.MaxTTL,
		MaxRooms: cfg.Rooms.MaxRooms,
	}, time.Now)
	return &Server{
		cfg:     cfg,
		logger:  logger,
		mgr:     mgr,
		srv:     server.New(cfg, mgr, logger, version),
		version: version,
	}, nil
}

// NewLogger builds the standard text logger for a level string.
func NewLogger(level string) *slog.Logger {
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

// Logger returns the server's logger (plugins usually share it).
func (s *Server) Logger() *slog.Logger { return s.logger }

// Handler returns the lobby's root HTTP handler (health, config.json,
// WebSocket signaling, static assets). Plugins typically wrap this in their
// own mux and pass the result to Run.
func (s *Server) Handler() http.Handler { return s.srv.Handler() }

// Run serves root on the configured listeners (root nil means Handler()),
// runs the room GC loop, and shuts down gracefully when ctx is cancelled.
func (s *Server) Run(ctx context.Context, root http.Handler) error {
	if root == nil {
		root = s.Handler()
	}

	go func() {
		ticker := time.NewTicker(s.cfg.Rooms.GCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := s.mgr.GC(); n > 0 {
					s.logger.Info("room gc", "destroyed", n, "remaining", s.mgr.RoomCount())
				}
			}
		}
	}()

	errCh := make(chan error, 2)
	var servers []*http.Server

	if s.cfg.Server.ListenHTTP != "" {
		hs := &http.Server{
			Addr:              s.cfg.Server.ListenHTTP,
			Handler:           root,
			ReadHeaderTimeout: 10 * time.Second,
		}
		servers = append(servers, hs)
		ln, err := net.Listen("tcp", s.cfg.Server.ListenHTTP)
		if err != nil {
			return err
		}
		s.logger.Info("listening", "mode", "http", "addr", ln.Addr().String())
		go func() { errCh <- hs.Serve(ln) }()
	}
	if s.cfg.Server.ListenHTTPS != "" {
		hs := &http.Server{
			Addr:              s.cfg.Server.ListenHTTPS,
			Handler:           root,
			ReadHeaderTimeout: 10 * time.Second,
		}
		servers = append(servers, hs)
		ln, err := net.Listen("tcp", s.cfg.Server.ListenHTTPS)
		if err != nil {
			return err
		}
		s.logger.Info("listening", "mode", "https", "addr", ln.Addr().String())
		go func() { errCh <- hs.ServeTLS(ln, s.cfg.Server.Cert, s.cfg.Server.Key) }()
	}

	var runErr error
	select {
	case <-ctx.Done():
		s.logger.Info("shutting down")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, hs := range servers {
		_ = hs.Shutdown(shutdownCtx)
	}
	return runErr
}
