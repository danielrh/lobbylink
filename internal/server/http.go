// Package server wires HTTP routes and the WebSocket signaling
// endpoint to the lobby manager.
package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/danielrh/lobbylink/internal/config"
	"github.com/danielrh/lobbylink/internal/lobby"
	"github.com/danielrh/lobbylink/internal/static"
)

// Server holds the shared state behind all HTTP handlers.
type Server struct {
	cfg     *config.Config
	mgr     *lobby.Manager
	log     *slog.Logger
	version string
}

// New builds a Server. cfg must already be validated.
func New(cfg *config.Config, mgr *lobby.Manager, log *slog.Logger, version string) *Server {
	return &Server{cfg: cfg, mgr: mgr, log: log, version: version}
}

// Handler returns the root HTTP handler with all routes mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /config.json", s.handleConfigJSON)
	mux.HandleFunc("OPTIONS /config.json", s.handlePreflight)
	mux.HandleFunc("/ws", s.handleWS)
	mux.Handle("/", s.cors(static.Handler()))
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleConfigJSON(w http.ResponseWriter, r *http.Request) {
	s.setCORS(w, r)
	mode := "direct"
	if s.cfg.Server.BehindProxy {
		mode = "proxy"
	}
	body := map[string]any{
		"wsUrl":   s.wsURL(r),
		"mode":    mode,
		"version": s.version,
		"turn":    s.cfg.Turn.Enabled,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	s.setCORS(w, r)
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(http.StatusNoContent)
}

// cors wraps an HTTP handler with allowlisted-origin CORS headers so
// static assets (the client module in particular) can be imported from
// games hosted on other allowlisted origins.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.setCORS(w, r)
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// setCORS echoes Access-Control-Allow-Origin only for origins on the
// global or any app allowlist.
func (s *Server) setCORS(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	if s.originKnown(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
}

// originKnown reports whether origin appears on the global allowlist or
// any app allowlist. App-scoped origins are re-checked against the
// specific app policy at join time.
func (s *Server) originKnown(origin string) bool {
	if s.cfg.OriginAllowed(origin) {
		return true
	}
	for i := range s.cfg.Apps {
		if s.cfg.OriginAllowedForApp(origin, &s.cfg.Apps[i]) {
			return true
		}
	}
	return false
}

// wsURL derives the public WebSocket URL, preferring the configured
// public_url and falling back to the request host.
func (s *Server) wsURL(r *http.Request) string {
	if s.cfg.Server.PublicURL != "" {
		u, err := url.Parse(s.cfg.Server.PublicURL)
		if err == nil {
			scheme := "ws"
			if u.Scheme == "https" {
				scheme = "wss"
			}
			return scheme + "://" + u.Host + "/ws"
		}
	}
	scheme := "ws"
	if r.TLS != nil || s.headerProto(r) == "https" {
		scheme = "wss"
	}
	return scheme + "://" + r.Host + "/ws"
}

// headerProto returns X-Forwarded-Proto but only when the direct peer
// is a trusted proxy.
func (s *Server) headerProto(r *http.Request) string {
	if !s.cfg.Server.BehindProxy || !s.fromTrustedProxy(r) {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
}

func (s *Server) fromTrustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, p := range s.cfg.Server.TrustedProxies {
		if tp := net.ParseIP(p); tp != nil && tp.Equal(ip) {
			return true
		}
	}
	return false
}

// clientIP returns the best-effort client address for logging: the
// first X-Forwarded-For hop when the request comes from a trusted
// proxy, otherwise the socket peer.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.Server.BehindProxy && s.fromTrustedProxy(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
