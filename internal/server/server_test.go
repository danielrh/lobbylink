package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielrh/lobbylink/internal/config"
	"github.com/danielrh/lobbylink/internal/lobby"
	"github.com/danielrh/lobbylink/internal/protocol"
	"github.com/danielrh/lobbylink/internal/turn"
)

const (
	okOrigin  = "https://ok.example"
	appOrigin = "https://apponly.example"
)

// srvMsg can decode every server->client message. Error.code and
// Joined.code share the JSON key; both are strings, so one field works.
type srvMsg struct {
	Type           string                `json:"type"`
	Code           string                `json:"code"`
	Message        string                `json:"message"`
	SelfID         int                   `json:"selfId"`
	MaxPlayers     int                   `json:"maxPlayers"`
	Started        bool                  `json:"started"`
	ResumeToken    string                `json:"resumeToken"`
	Players        []protocol.PlayerInfo `json:"players"`
	ICEServers     []protocol.ICEServer  `json:"iceServers"`
	PlayerID       int                   `json:"playerId"`
	Reason         string                `json:"reason"`
	WasReplacement bool                  `json:"wasReplacement"`
	From           int                   `json:"from"`
	Payload        json.RawMessage       `json:"payload"`
}

func newTestServer(t *testing.T, mutate func(*config.Config)) (*httptest.Server, *config.Config) {
	t.Helper()
	cfg := config.Default()
	cfg.Server.ListenHTTP = "127.0.0.1:0" // satisfies Validate; httptest listens itself
	cfg.Security.AllowedOrigins = []string{okOrigin}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := lobby.NewManager(lobby.Limits{
		EmptyTTL: cfg.Rooms.EmptyTTL,
		MaxTTL:   cfg.Rooms.MaxTTL,
		MaxRooms: cfg.Rooms.MaxRooms,
	}, time.Now)
	ts := httptest.NewServer(New(&cfg, mgr, logger, "test").Handler())
	t.Cleanup(ts.Close)
	return ts, &cfg
}

func wsURL(ts *httptest.Server) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
}

func dial(t *testing.T, ts *httptest.Server, origin string) *websocket.Conn {
	t.Helper()
	conn, err := tryDial(ts, origin)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func tryDial(ts *httptest.Server, origin string) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	conn, resp, err := websocket.Dial(ctx, wsURL(ts), &websocket.DialOptions{HTTPHeader: hdr})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	return conn, err
}

func send(t *testing.T, conn *websocket.Conn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatalf("write failed: %v", err)
	}
}

func recv(t *testing.T, conn *websocket.Conn) srvMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var m srvMsg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("bad server JSON %q: %v", data, err)
	}
	return m
}

func expect(t *testing.T, conn *websocket.Conn, wantType string) srvMsg {
	t.Helper()
	m := recv(t, conn)
	if m.Type != wantType {
		t.Fatalf("got %q message %+v, want %q", m.Type, m, wantType)
	}
	return m
}

func expectError(t *testing.T, conn *websocket.Conn, wantCode string) srvMsg {
	t.Helper()
	m := expect(t, conn, protocol.TypeError)
	if m.Code != wantCode {
		t.Fatalf("error code = %q (%s), want %q", m.Code, m.Message, wantCode)
	}
	return m
}

func TestHealthzAndStatic(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok\n" {
		t.Errorf("healthz: %d %q", resp.StatusCode, body)
	}

	resp, err = http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "P2P Lobby Demo") {
		t.Errorf("index: %d", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/p2p-client.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("p2p-client.js: %d", resp.StatusCode)
	}
}

func TestConfigJSONAndCORS(t *testing.T) {
	ts, _ := newTestServer(t, func(c *config.Config) {
		c.Server.PublicURL = "https://pqrstuvw.xyz:4443"
	})

	req, _ := http.NewRequest("GET", ts.URL+"/config.json", nil)
	req.Header.Set("Origin", okOrigin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != okOrigin {
		t.Errorf("ACAO = %q", got)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["wsUrl"] != "wss://pqrstuvw.xyz:4443/ws" {
		t.Errorf("wsUrl = %v", body["wsUrl"])
	}
	if body["version"] != "test" {
		t.Errorf("version = %v", body["version"])
	}

	// Disallowed origin gets no ACAO echo.
	req, _ = http.NewRequest("GET", ts.URL+"/config.json", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if got := resp2.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("evil ACAO = %q", got)
	}
}

func TestWSOriginPolicy(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	if _, err := tryDial(ts, "https://evil.example"); err == nil {
		t.Error("disallowed origin accepted")
	}
	// Native clients (no Origin) rejected unless allow_no_origin.
	if _, err := tryDial(ts, ""); err == nil {
		t.Error("no-origin accepted while disabled")
	}

	ts2, _ := newTestServer(t, func(c *config.Config) { c.Security.AllowNoOrigin = true })
	conn, err := tryDial(ts2, "")
	if err != nil {
		t.Fatalf("no-origin rejected while enabled: %v", err)
	}
	conn.CloseNow()
}

func TestFullSignalingFlow(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	a := dial(t, ts, okOrigin)
	send(t, a, `{"type":"join","code":"FLOW01","create":{"maxPlayers":2,"waitUntilFull":true}}`)
	joinedA := expect(t, a, protocol.TypeJoined)
	if joinedA.SelfID != 0 || joinedA.Started || joinedA.MaxPlayers != 2 || joinedA.ResumeToken == "" || joinedA.Code != "FLOW01" {
		t.Fatalf("A joined wrong: %+v", joinedA)
	}

	b := dial(t, ts, okOrigin)
	send(t, b, `{"type":"join","code":"FLOW01"}`)
	joinedB := expect(t, b, protocol.TypeJoined)
	if joinedB.SelfID != 1 || !joinedB.Started {
		t.Fatalf("B joined wrong: %+v", joinedB)
	}

	// A sees the join, then the start — in that order.
	pj := expect(t, a, protocol.TypePlayerJoined)
	if pj.PlayerID != 1 || len(pj.Players) != 2 || !pj.Players[1].Connected {
		t.Fatalf("player-joined wrong: %+v", pj)
	}
	expect(t, a, protocol.TypeRoomStarted)

	// Offer / answer / ICE relay, byte-for-byte payloads.
	send(t, a, `{"type":"signal","to":1,"payload":{"kind":"offer","sdp":"v=0 offer"}}`)
	sig := expect(t, b, protocol.TypeSignal)
	if sig.From != 0 || string(sig.Payload) != `{"kind":"offer","sdp":"v=0 offer"}` {
		t.Fatalf("relayed offer wrong: %+v payload=%s", sig, sig.Payload)
	}
	send(t, b, `{"type":"signal","to":0,"payload":{"kind":"answer","sdp":"v=0 answer"}}`)
	sig = expect(t, a, protocol.TypeSignal)
	if sig.From != 1 || string(sig.Payload) != `{"kind":"answer","sdp":"v=0 answer"}` {
		t.Fatalf("relayed answer wrong: %+v", sig)
	}
	send(t, a, `{"type":"signal","to":1,"payload":{"kind":"ice","candidate":{"candidate":"c0","sdpMid":"0"}}}`)
	sig = expect(t, b, protocol.TypeSignal)
	if sig.From != 0 || !strings.Contains(string(sig.Payload), `"c0"`) {
		t.Fatalf("relayed ice wrong: %+v", sig)
	}

	// Explicit leave frees the slot and B hears about it.
	send(t, a, `{"type":"leave"}`)
	pl := expect(t, b, protocol.TypePlayerLeft)
	if pl.PlayerID != 0 || pl.Reason != protocol.LeftReasonExplicit {
		t.Fatalf("player-left wrong: %+v", pl)
	}
}

func TestSignalErrorsOverWS(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	a := dial(t, ts, okOrigin)
	send(t, a, `{"type":"signal","to":1,"payload":{"kind":"offer"}}`)
	expectError(t, a, protocol.ErrCodeNotJoined)

	send(t, a, `{"type":"join","code":"SIGERR1","create":{"maxPlayers":4,"waitUntilFull":true}}`)
	expect(t, a, protocol.TypeJoined)

	send(t, a, `{"type":"signal","to":0,"payload":{"kind":"offer"}}`)
	expectError(t, a, protocol.ErrCodeInvalidTarget)
	send(t, a, `{"type":"signal","to":2,"payload":{"kind":"offer"}}`)
	expectError(t, a, protocol.ErrCodeInvalidTarget) // unoccupied
	send(t, a, `{"type":"signal","to":1,"payload":{"kind":"hijack"}}`)
	expectError(t, a, protocol.ErrCodeInvalidMessage)
	send(t, a, `{"type":"join","code":"OTHER1","create":{"maxPlayers":2}}`)
	expectError(t, a, protocol.ErrCodeAlreadyJoined)
	send(t, a, `{"type":"???"}`)
	expectError(t, a, protocol.ErrCodeInvalidMessage)
	send(t, a, `not json at all`)
	expectError(t, a, protocol.ErrCodeInvalidMessage)

	// Connection is still healthy after all the non-fatal errors.
	send(t, a, `{"type":"signal","to":1,"payload":{"kind":"offer"}}`)
	expectError(t, a, protocol.ErrCodeInvalidTarget)
}

func TestResumeFlowOverWS(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	a := dial(t, ts, okOrigin)
	send(t, a, `{"type":"join","code":"RESUME1","create":{"maxPlayers":2,"waitUntilFull":true}}`)
	joinedA := expect(t, a, protocol.TypeJoined)

	b := dial(t, ts, okOrigin)
	send(t, b, `{"type":"join","code":"RESUME1"}`)
	expect(t, b, protocol.TypeJoined)
	expect(t, a, protocol.TypePlayerJoined)
	expect(t, a, protocol.TypeRoomStarted)

	// A's transport dies; the slot must survive.
	a.CloseNow()
	pl := expect(t, b, protocol.TypePlayerLeft)
	if pl.PlayerID != 0 || pl.Reason != protocol.LeftReasonDisconnected {
		t.Fatalf("disconnect not seen: %+v", pl)
	}

	// Token resume returns slot 0 in the started room.
	a2 := dial(t, ts, okOrigin)
	send(t, a2, `{"type":"join","code":"RESUME1","resumeToken":"`+joinedA.ResumeToken+`"}`)
	joinedA2 := expect(t, a2, protocol.TypeJoined)
	if joinedA2.SelfID != 0 || !joinedA2.Started || joinedA2.ResumeToken == joinedA.ResumeToken {
		t.Fatalf("resume wrong: %+v", joinedA2)
	}
	rj := expect(t, b, protocol.TypePlayerRejoined)
	if rj.PlayerID != 0 || rj.WasReplacement {
		t.Fatalf("player-rejoined wrong: %+v", rj)
	}

	// Signaling works again after resume.
	send(t, b, `{"type":"signal","to":0,"payload":{"kind":"offer","sdp":"re"}}`)
	sig := expect(t, a2, protocol.TypeSignal)
	if sig.From != 1 {
		t.Fatalf("post-resume signal wrong: %+v", sig)
	}
}

func TestClaimFlowOverWS(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	a := dial(t, ts, okOrigin)
	send(t, a, `{"type":"join","code":"CLAIM01","create":{"maxPlayers":2,"waitUntilFull":true,"claimAfterMs":0}}`)
	expect(t, a, protocol.TypeJoined)
	b := dial(t, ts, okOrigin)
	send(t, b, `{"type":"join","code":"CLAIM01"}`)
	expect(t, b, protocol.TypeJoined)
	expect(t, a, protocol.TypePlayerJoined)
	expect(t, a, protocol.TypeRoomStarted)

	// claimAfterMs=0 means instantly claimable.
	c := dial(t, ts, okOrigin)
	send(t, c, `{"type":"claim-slot","code":"CLAIM01","playerId":0}`)
	joinedC := expect(t, c, protocol.TypeJoined)
	if joinedC.SelfID != 0 || !joinedC.Started {
		t.Fatalf("claim joined wrong: %+v", joinedC)
	}
	// The replaced client is told and then its socket closes.
	expectError(t, a, protocol.ErrCodeReplaced)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := a.Read(ctx); err == nil {
		t.Error("replaced socket still open")
	}
	pr := expect(t, b, protocol.TypePlayerReplaced)
	if pr.PlayerID != 0 {
		t.Fatalf("player-replaced wrong: %+v", pr)
	}

	// Claim of a still-fresh slot is refused when claimAfter is long.
	ts2, _ := newTestServer(t, nil)
	a2 := dial(t, ts2, okOrigin)
	send(t, a2, `{"type":"join","code":"CLAIM02","create":{"maxPlayers":2,"claimAfterMs":3600000}}`)
	expect(t, a2, protocol.TypeJoined)
	c2 := dial(t, ts2, okOrigin)
	send(t, c2, `{"type":"claim-slot","code":"CLAIM02","playerId":0}`)
	expectError(t, c2, protocol.ErrCodeSlotNotClaimable)

	// token-only rooms refuse claims outright.
	a3 := dial(t, ts2, okOrigin)
	send(t, a3, `{"type":"join","code":"CLAIM03","create":{"maxPlayers":2,"claimAfterMs":0,"reconnectPolicy":"token-only"}}`)
	expect(t, a3, protocol.TypeJoined)
	c3 := dial(t, ts2, okOrigin)
	send(t, c3, `{"type":"claim-slot","code":"CLAIM03","playerId":0}`)
	expectError(t, c3, protocol.ErrCodeClaimNotAllowed)
}

func TestLeaveThenJoinAnotherRoomSameSocket(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	a := dial(t, ts, okOrigin)
	send(t, a, `{"type":"join","code":"FIRST1","create":{"maxPlayers":2}}`)
	expect(t, a, protocol.TypeJoined)
	send(t, a, `{"type":"leave"}`)
	send(t, a, `{"type":"join","code":"SECOND1","create":{"maxPlayers":2}}`)
	j := expect(t, a, protocol.TypeJoined)
	if j.Code != "SECOND1" || j.SelfID != 0 {
		t.Fatalf("second join wrong: %+v", j)
	}
}

func TestTURNCredentialsInJoin(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "turn-secret")
	secret := "integration-test-turn-secret"
	if err := os.WriteFile(secretPath, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts, cfg := newTestServer(t, func(c *config.Config) {
		c.Turn.Enabled = true
		c.Turn.Realm = "pqrstuvw.xyz"
		c.Turn.SharedSecretFile = secretPath
		c.Turn.TTL = time.Hour
		c.Turn.URLs = []string{
			"stun:pqrstuvw.xyz:3478",
			"turn:pqrstuvw.xyz:3478?transport=udp",
			"turns:pqrstuvw.xyz:5349?transport=tcp",
		}
	})

	a := dial(t, ts, okOrigin)
	send(t, a, `{"type":"join","code":"TURN01","create":{"maxPlayers":2}}`)
	j := expect(t, a, protocol.TypeJoined)
	if len(j.ICEServers) != 2 {
		t.Fatalf("iceServers = %+v", j.ICEServers)
	}
	if j.ICEServers[0].URLs[0] != "stun:pqrstuvw.xyz:3478" || j.ICEServers[0].Username != "" {
		t.Errorf("stun entry wrong: %+v", j.ICEServers[0])
	}
	tent := j.ICEServers[1]
	parts := strings.SplitN(tent.Username, ":", 2)
	if len(parts) != 2 || parts[1] != "room-TURN01-player-0" {
		t.Fatalf("turn username = %q", tent.Username)
	}
	// Cross-check the HMAC using the loaded secret and the embedded expiry.
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("bad expiry %q", parts[0])
	}
	wantUser, wantPass := turn.Credentials(cfg.Turn.Secret, 0, "TURN01", 0, time.Unix(expiry, 0))
	if wantUser != tent.Username || wantPass != tent.Credential {
		t.Errorf("credential mismatch: got %q/%q want %q/%q", tent.Username, tent.Credential, wantUser, wantPass)
	}
	if min, max := time.Now().Add(55*time.Minute).Unix(), time.Now().Add(65*time.Minute).Unix(); expiry < min || expiry > max {
		t.Errorf("expiry %d outside sane window", expiry)
	}
}

func TestAppPolicyOverWS(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "turn-secret")
	if err := os.WriteFile(secretPath, []byte("integration-test-turn-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts, _ := newTestServer(t, func(c *config.Config) {
		c.Turn.Enabled = true
		c.Turn.SharedSecretFile = secretPath
		c.Turn.URLs = []string{"stun:x.example:3478", "turn:x.example:3478"}
		c.Apps = []config.App{{
			ID:             "app1",
			AllowedOrigins: []string{appOrigin},
			MaxPlayersMax:  2,
			AllowTurn:      false,
		}}
	})

	// App-only origin can open the socket but cannot join without appId.
	a := dial(t, ts, appOrigin)
	send(t, a, `{"type":"join","code":"APPRM1","create":{"maxPlayers":2}}`)
	expectError(t, a, protocol.ErrCodeOriginForbidden)

	// Unknown app is rejected.
	send(t, a, `{"type":"join","appId":"ghost","code":"APPRM1","create":{"maxPlayers":2}}`)
	expectError(t, a, protocol.ErrCodeUnknownApp)

	// Over-limit maxPlayers for the app is rejected.
	send(t, a, `{"type":"join","appId":"app1","code":"APPRM1","create":{"maxPlayers":3}}`)
	expectError(t, a, protocol.ErrCodeInvalidCreate)

	// Valid app join succeeds and gets no TURN servers (allow_turn=false).
	send(t, a, `{"type":"join","appId":"app1","code":"APPRM1","create":{"maxPlayers":2}}`)
	j := expect(t, a, protocol.TypeJoined)
	if len(j.ICEServers) != 0 {
		t.Errorf("app with allow_turn=false got iceServers: %+v", j.ICEServers)
	}

	// Globally-allowed origin without appId cannot enter the app room.
	b := dial(t, ts, okOrigin)
	send(t, b, `{"type":"join","code":"APPRM1"}`)
	expectError(t, b, protocol.ErrCodeAppMismatch)

	// Global origin joining with the right appId works and still gets
	// no TURN entry.
	send(t, b, `{"type":"join","appId":"app1","code":"APPRM1"}`)
	j = expect(t, b, protocol.TypeJoined)
	if j.SelfID != 1 || len(j.ICEServers) != 0 {
		t.Errorf("global-origin app join wrong: %+v", j)
	}
}

func TestReadLimitCloses(t *testing.T) {
	ts, _ := newTestServer(t, func(c *config.Config) {
		c.Security.MaxWSMessageBytes = 4096
	})
	a := dial(t, ts, okOrigin)
	big := `{"type":"join","code":"BIG1","resumeToken":"` + strings.Repeat("x", 8192) + `"}`
	send(t, a, big)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := a.Read(ctx); err == nil {
		t.Error("oversized message did not close the connection")
	}
}

func TestBinaryFrameRejected(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	a := dial(t, ts, okOrigin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Write(ctx, websocket.MessageBinary, []byte(`{"type":"leave"}`)); err != nil {
		t.Fatal(err)
	}
	expectError(t, a, protocol.ErrCodeInvalidMessage)
	if _, _, err := a.Read(ctx); err == nil {
		t.Error("binary frame did not close the connection")
	}
}

func TestRoomNotFoundOverWS(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	a := dial(t, ts, okOrigin)
	send(t, a, `{"type":"join","code":"GHOST9"}`)
	expectError(t, a, protocol.ErrCodeRoomNotFound)
	send(t, a, `{"type":"claim-slot","code":"GHOST9","playerId":0}`)
	expectError(t, a, protocol.ErrCodeRoomNotFound)
}
