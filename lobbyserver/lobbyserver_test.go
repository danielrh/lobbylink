package lobbyserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The embedding façade must boot a working lobby server and let a "plugin"
// mount extra routes in front of the lobby handler.
func TestEmbeddedServerServesLobbyAndPluginRoutes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SetListenHTTP("127.0.0.1:0")
	cfg.SetAllowedOrigins([]string{"http://localhost:5173"})
	cfg.SetLogLevel("error")
	if got := cfg.AllowedOrigins(); len(got) != 1 || got[0] != "http://localhost:5173" {
		t.Fatalf("AllowedOrigins roundtrip: %v", got)
	}

	srv, err := New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}

	// a plugin wraps the lobby handler with its own route
	mux := http.NewServeMux()
	mux.HandleFunc("GET /plugin/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})
	mux.Handle("/", srv.Handler())

	if body := get(t, mux, "/healthz"); body != "ok\n" {
		t.Fatalf("lobby route through plugin mux: %q", body)
	}
	if body := get(t, mux, "/plugin/ping"); body != "pong" {
		t.Fatalf("plugin route: %q", body)
	}

	// lifecycle: Run must serve and then return promptly on context cancel
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, mux) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not shut down after context cancel")
	}
}

func TestNewValidatesConfig(t *testing.T) {
	cfg := DefaultConfig() // no listeners configured -> invalid
	cfg.SetListenHTTP("")
	if _, err := New(cfg, "test"); err == nil {
		t.Fatal("expected a validation error with no listeners")
	}
}

func get(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}
