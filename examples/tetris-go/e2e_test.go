package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	lobbylink "github.com/danielrh/lobbylink/clients/go"
)

// End-to-end: run the real tetris binary against a local lobby server,
// join the room as a scripted peer, and check the rendered output for
// the peer's STATE (name shown) and for the garbage bump. Skipped with
// -short or outside the repo checkout.

func TestMultiplayerBumpEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test skipped with -short")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "p2p-lobby-server")); err != nil {
		t.Skip("repo checkout not found")
	}
	tmp := t.TempDir()

	build := func(out string, dir string, target string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-o", out, target)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", target, err, b)
		}
	}
	serverBin := filepath.Join(tmp, "server")
	tetrisBin := filepath.Join(tmp, "tetris")
	build(serverBin, root, "./cmd/p2p-lobby-server")
	build(tetrisBin, ".", ".")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	origin := "http://" + addr
	srv := exec.Command(serverBin, "--listen-http", addr, "--allowed-origin", origin, "--public-url", origin)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srv.Process.Kill(); srv.Wait() }()
	waitHealthy(t, origin)

	code := fmt.Sprintf("TETRIS-%d", os.Getpid())

	// Launch tetris with held-open stdin and captured stdout.
	stdinR, stdinW, _ := os.Pipe()
	defer stdinW.Close()
	tet := exec.Command(tetrisBin, "--name", "gamer", origin, code)
	tet.Stdin = stdinR
	var out lockedBuffer
	tet.Stdout = &out
	tet.Stderr = os.Stderr
	if err := tet.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { tet.Process.Kill(); tet.Wait() }()

	// Join the same room as a scripted second player, retrying until
	// the tetris process has created it.
	var peer *lobbylink.Game
	for i := 0; ; i++ {
		peer, err = lobbylink.Connect(context.Background(), lobbylink.Options{Server: origin, Code: code})
		if err == nil {
			break
		}
		if le, ok := err.(*lobbylink.Error); !ok || le.Code != "room-not-found" || i > 100 {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer peer.Close()
	deadline := time.After(30 * time.Second)
	for connected := false; !connected; {
		select {
		case ev := <-peer.Events():
			if ps, ok := ev.(lobbylink.PeerStateEvent); ok && ps.State == "connected" {
				connected = true
			}
		case <-deadline:
			t.Fatal("peer never connected to the tetris process")
		}
	}

	// Broadcast our STATE (best-effort, repeated) and send the bump.
	st := &stateMsg{alive: true, score: 777, name: "FAKEBOT"}
	st.board[boardH-1][0] = 3
	for i := 0; i < 10; i++ {
		if err := peer.BroadcastBestEffort(encodeState(st)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := peer.SendReliable(0, encodeAttack(2)); err != nil {
		t.Fatal(err)
	}

	waitOutput(t, &out, "FAKEBOT")               // opponent panel shows our name
	waitOutput(t, &out, "bumped you up 2 rows!") // the attack landed

	// And the tetris process broadcasts its own state back to us.
	stateSeen := false
	stateDeadline := time.After(10 * time.Second)
	for !stateSeen {
		select {
		case ev := <-peer.Events():
			if m, ok := ev.(lobbylink.MessageEvent); ok {
				if got, ok := decodeState(m.Data); ok && got.name == "gamer" {
					stateSeen = true
				}
			}
		case <-stateDeadline:
			t.Fatal("never received tetris STATE broadcast")
		}
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitOutput(t *testing.T, buf *lockedBuffer, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	tail := buf.String()
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}
	t.Fatalf("output never contained %q; tail:\n%s", want, tail)
}

func waitHealthy(t *testing.T, origin string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		resp, err := http.Get(origin + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server never became healthy")
}
