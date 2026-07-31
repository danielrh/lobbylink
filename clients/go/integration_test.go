package lobbylink

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Integration tests build the lobby server from this repository and run
// real WebSocket + WebRTC sessions against it on loopback. They are
// skipped with -short or when the repo root is not present.

var serverURL string // set by TestMain when the server is up

func TestMain(m *testing.M) {
	code, err := runWithServer(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration server unavailable:", err)
	}
	os.Exit(code)
}

func runWithServer(m *testing.M) (int, error) {
	root, err := filepath.Abs("../..")
	if err != nil {
		return m.Run(), err
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "p2p-lobby-server")); err != nil {
		return m.Run(), err // not in the repo checkout: unit tests only
	}
	tmp, err := os.MkdirTemp("", "lobbylink-server")
	if err != nil {
		return m.Run(), err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "p2p-lobby-server")
	build := exec.Command("go", "build", "-o", bin, "./cmd/p2p-lobby-server")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return m.Run(), fmt.Errorf("server build failed: %v\n%s", err, out)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return m.Run(), err
	}
	addr := l.Addr().String()
	l.Close()
	origin := "http://" + addr

	srv := exec.Command(bin,
		"--listen-http", addr,
		"--allowed-origin", origin,
		"--public-url", origin,
	)
	srv.Stdout = os.Stderr
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		return m.Run(), err
	}
	defer func() {
		srv.Process.Kill()
		srv.Wait()
	}()

	healthy := false
	for i := 0; i < 100; i++ {
		resp, err := http.Get(origin + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				healthy = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthy {
		return m.Run(), fmt.Errorf("server at %s never became healthy", origin)
	}
	serverURL = origin
	return m.Run(), nil
}

func requireServer(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	if serverURL == "" {
		t.Skip("local lobby server unavailable")
	}
}

func randomCode(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("GOTEST-%06d", rand.Intn(1_000_000))
}

// waitEvent drains events until match returns true, failing on timeout.
func waitEvent(t *testing.T, g *Game, what string, match func(Event) bool) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case ev, ok := <-g.Events():
			if !ok {
				t.Fatalf("event stream closed while waiting for %s", what)
			}
			if match(ev) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func waitConnected(t *testing.T, g *Game, peer int) {
	t.Helper()
	waitEvent(t, g, fmt.Sprintf("peer %d connected", peer), func(ev Event) bool {
		ps, ok := ev.(PeerStateEvent)
		return ok && ps.PlayerID == peer && ps.State == "connected"
	})
}

func TestTwoPeersExchangeMessages(t *testing.T) {
	requireServer(t)
	ctx := context.Background()
	code := randomCode(t)

	a, err := Connect(ctx, Options{
		Server: serverURL,
		Code:   code,
		Create: NewCreateOptions(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.SelfID() != 0 || a.MaxPlayers() != 2 {
		t.Fatalf("creator got selfID=%d maxPlayers=%d", a.SelfID(), a.MaxPlayers())
	}
	if a.ResumeToken() == "" {
		t.Fatal("no resume token issued")
	}

	b, err := Connect(ctx, Options{Server: serverURL, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.SelfID() != 1 {
		t.Fatalf("joiner got selfID=%d", b.SelfID())
	}

	waitConnected(t, a, 1)
	waitConnected(t, b, 0)

	// Reliable, single chunk, both directions.
	if err := a.SendReliable(1, []byte("hello from 0")); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, b, "reliable hello", func(ev Event) bool {
		m, ok := ev.(MessageEvent)
		return ok && m.From == 0 && m.Kind == Reliable && string(m.Data) == "hello from 0"
	})
	if err := b.SendReliable(0, []byte("hello from 1")); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, a, "reliable reply", func(ev Event) bool {
		m, ok := ev.(MessageEvent)
		return ok && m.From == 1 && m.Kind == Reliable && string(m.Data) == "hello from 1"
	})

	// Reliable, multi-chunk (100 KiB crosses the 16 KiB chunk size).
	big := bytes.Repeat([]byte{0xA5, 0x5A, 0x00, 0xFF}, 25*1024)
	if err := a.SendReliable(1, big); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, b, "chunked reliable message", func(ev Event) bool {
		m, ok := ev.(MessageEvent)
		return ok && m.From == 0 && m.Kind == Reliable && bytes.Equal(m.Data, big)
	})

	// Best-effort datagrams are lossy in principle but never dropped on
	// an idle loopback link; send a burst and require at least one.
	for i := 0; i < 20; i++ {
		if err := b.BroadcastBestEffort([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitEvent(t, a, "best-effort ping", func(ev Event) bool {
		m, ok := ev.(MessageEvent)
		return ok && m.From == 1 && m.Kind == BestEffort && string(m.Data) == "ping"
	})

	// Explicit leave frees the slot and notifies the peer.
	b.Close()
	waitEvent(t, a, "player-left explicit", func(ev Event) bool {
		pl, ok := ev.(PlayerLeftEvent)
		return ok && pl.PlayerID == 1 && pl.Reason == "explicit-leave"
	})
	for _, p := range a.Players() {
		if p.ID == 1 && p.Occupied {
			t.Fatal("slot 1 still occupied after explicit leave")
		}
	}
}

func TestSendValidation(t *testing.T) {
	requireServer(t)
	code := randomCode(t)
	g, err := Connect(context.Background(), Options{
		Server: serverURL,
		Code:   code,
		Create: NewCreateOptions(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	expectCode := func(err error, code string) {
		t.Helper()
		le, ok := err.(*Error)
		if !ok || le.Code != code {
			t.Fatalf("expected %s, got %v", code, err)
		}
	}
	expectCode(g.SendBestEffort(0, []byte("x")), "invalid-target") // self
	expectCode(g.SendBestEffort(7, []byte("x")), "invalid-target")
	expectCode(g.SendBestEffort(1, make([]byte, maxBestEffort+1)), "message-too-large")
	expectCode(g.SendReliable(1, []byte("x")), "target-unavailable") // empty slot
}

func TestJoinErrors(t *testing.T) {
	requireServer(t)
	ctx := context.Background()
	_, err := Connect(ctx, Options{Server: serverURL, Code: randomCode(t)})
	if le, ok := err.(*Error); !ok || le.Code != "room-not-found" {
		t.Fatalf("expected room-not-found, got %v", err)
	}

	code := randomCode(t)
	first, err := Connect(ctx, Options{Server: serverURL, Code: code, Create: NewCreateOptions(1)})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, err = Connect(ctx, Options{Server: serverURL, Code: code})
	if le, ok := err.(*Error); !ok || le.Code != "room-full" {
		t.Fatalf("expected room-full, got %v", err)
	}
}

func TestResumeToken(t *testing.T) {
	requireServer(t)
	ctx := context.Background()
	code := randomCode(t)

	a, err := Connect(ctx, Options{Server: serverURL, Code: code, Create: NewCreateOptions(2)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b1, err := Connect(ctx, Options{Server: serverURL, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	token := b1.ResumeToken()
	waitConnected(t, a, 1)
	waitConnected(t, b1, 0)

	// Simulate a crash: drop b1's socket without an explicit leave,
	// then resume the same slot with the token.
	b1.ws.CloseNow()
	waitEvent(t, a, "player-left disconnected", func(ev Event) bool {
		pl, ok := ev.(PlayerLeftEvent)
		return ok && pl.PlayerID == 1 && pl.Reason == "disconnected"
	})

	b2, err := Connect(ctx, Options{Server: serverURL, Code: code, ResumeToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if b2.SelfID() != 1 {
		t.Fatalf("resume gave slot %d, want 1", b2.SelfID())
	}
	if b2.ResumeToken() == token {
		t.Fatal("resume token was not rotated")
	}
	waitEvent(t, a, "player-rejoined", func(ev Event) bool {
		pr, ok := ev.(PlayerRejoinedEvent)
		return ok && pr.PlayerID == 1
	})
	waitConnected(t, a, 1)
	waitConnected(t, b2, 0)
	if err := b2.SendReliable(0, []byte("back")); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, a, "post-resume message", func(ev Event) bool {
		m, ok := ev.(MessageEvent)
		return ok && m.From == 1 && string(m.Data) == "back"
	})
}
