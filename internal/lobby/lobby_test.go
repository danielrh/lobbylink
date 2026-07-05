package lobby

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/danielrh/lobbylink/internal/protocol"
)

// fakeClock is a race-safe manual clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeConn records everything the lobby pushes at it.
type fakeConn struct {
	mu    sync.Mutex
	name  string
	msgs  []any
	kicks []string // kick codes
	full  bool     // simulate an overflowing outbound queue
}

func (c *fakeConn) Enqueue(msg any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.full {
		return false
	}
	c.msgs = append(c.msgs, msg)
	return true
}

func (c *fakeConn) Kick(code, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kicks = append(c.kicks, code)
}

func (c *fakeConn) messages() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.msgs...)
}

func (c *fakeConn) kickCodes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.kicks...)
}

func (c *fakeConn) setFull(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.full = v
}

// lastOfType returns the most recent message of type T pushed to c.
func lastOfType[T any](c *fakeConn) (T, bool) {
	msgs := c.messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if v, ok := msgs[i].(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

func countOfType[T any](c *fakeConn) int {
	n := 0
	for _, m := range c.messages() {
		if _, ok := m.(T); ok {
			n++
		}
	}
	return n
}

var passthrough = func(res *JoinResult) any { return res }

func defaultLimits() Limits {
	return Limits{EmptyTTL: 5 * time.Minute, MaxTTL: 24 * time.Hour, MaxRooms: 10000}
}

func testMgr(t *testing.T) (*Manager, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	return NewManager(defaultLimits(), clock.Now), clock
}

func opts(maxPlayers int, mutate ...func(*protocol.RoomOptions)) *protocol.RoomOptions {
	o := &protocol.RoomOptions{
		MaxPlayers:       maxPlayers,
		WaitUntilFull:    true,
		AllowLateJoin:    false,
		AllowReconnect:   true,
		AllowReplacement: true,
		ReconnectPolicy:  protocol.DefaultReconnectPolicy,
		ClaimAfter:       40 * time.Second,
	}
	for _, m := range mutate {
		m(o)
	}
	return o
}

func mustJoin(t *testing.T, m *Manager, code string, create *protocol.RoomOptions, conn *fakeConn, token string) (*Session, *JoinResult) {
	t.Helper()
	sess, err := m.Join("", code, token, create, conn, passthrough)
	if err != nil {
		t.Fatalf("join %s failed: %v", code, err)
	}
	res, ok := lastOfType[*JoinResult](conn)
	if !ok {
		t.Fatalf("no JoinResult delivered to %s", conn.name)
	}
	return sess, res
}

func errCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *protocol.ProtoError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not *ProtoError", err)
	}
	return pe.Code
}

func TestCreateAndJoinBasics(t *testing.T) {
	m, _ := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}

	sessA, resA := mustJoin(t, m, "ROOM01", opts(3), a, "")
	if sessA.PlayerID != 0 || resA.SelfID != 0 {
		t.Errorf("creator should get slot 0, got %d", resA.SelfID)
	}
	if resA.Started {
		t.Error("waitUntilFull room must not start with 1/3 players")
	}
	if resA.ResumeToken == "" || len(resA.ResumeToken) < 40 {
		t.Errorf("resume token weak/missing: %q", resA.ResumeToken)
	}
	if len(resA.Players) != 3 || !resA.Players[0].Occupied || !resA.Players[0].Connected || resA.Players[1].Occupied {
		t.Errorf("players snapshot wrong: %+v", resA.Players)
	}
	if resA.MaxPlayers != 3 || resA.Code != "ROOM01" {
		t.Errorf("joined fields wrong: %+v", resA)
	}

	// Second join without create options joins the existing room.
	sessB, resB := mustJoin(t, m, "ROOM01", nil, b, "")
	if sessB.PlayerID != 1 || resB.Started {
		t.Errorf("B: id=%d started=%v", sessB.PlayerID, resB.Started)
	}
	pj, ok := lastOfType[protocol.PlayerJoined](a)
	if !ok || pj.PlayerID != 1 || len(pj.Players) != 3 || !pj.Players[1].Connected {
		t.Errorf("A did not observe B joining: %+v ok=%v", pj, ok)
	}
	// The joined result must be the first message on a new transport.
	if _, isJoin := b.messages()[0].(*JoinResult); !isJoin {
		t.Errorf("first message to B was %T, want *JoinResult", b.messages()[0])
	}
	if m.RoomCount() != 1 {
		t.Errorf("RoomCount = %d", m.RoomCount())
	}
}

func TestJoinWithoutCreateFails(t *testing.T) {
	m, _ := testMgr(t)
	_, err := m.Join("", "NOPE99", "", nil, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeRoomNotFound {
		t.Errorf("code = %v", err)
	}
}

func TestStartWhenFull(t *testing.T) {
	m, _ := testMgr(t)
	a, b, c := &fakeConn{name: "a"}, &fakeConn{name: "b"}, &fakeConn{name: "c"}
	mustJoin(t, m, "ROOM01", opts(3), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")
	_, resC := mustJoin(t, m, "ROOM01", nil, c, "")
	if !resC.Started {
		t.Error("third join should start a 3-player waitUntilFull room")
	}
	for _, conn := range []*fakeConn{a, b} {
		if n := countOfType[protocol.RoomStarted](conn); n != 1 {
			t.Errorf("%s got %d room-started, want 1", conn.name, n)
		}
	}
	// The triggering joiner learns from joined.started, not a broadcast.
	if n := countOfType[protocol.RoomStarted](c); n != 0 {
		t.Errorf("joiner got %d room-started broadcasts, want 0", n)
	}
}

func TestStartImmediatelyWithoutWaitUntilFull(t *testing.T) {
	m, _ := testMgr(t)
	a := &fakeConn{name: "a"}
	_, res := mustJoin(t, m, "ROOM01", opts(4, func(o *protocol.RoomOptions) {
		o.WaitUntilFull = false
		o.AllowLateJoin = true
	}), a, "")
	if !res.Started {
		t.Error("room must start when the creator joins if !waitUntilFull")
	}
	// Late join allowed: another player can still enter.
	b := &fakeConn{name: "b"}
	_, resB := mustJoin(t, m, "ROOM01", nil, b, "")
	if resB.SelfID != 1 || !resB.Started {
		t.Errorf("late join wrong: %+v", resB)
	}
}

func TestRoomFullAndLateJoinRules(t *testing.T) {
	m, _ := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	mustJoin(t, m, "ROOM01", opts(2), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")

	// Room is now full and started; a third join must fail with
	// room-started (late-join rule fires before slot search).
	_, err := m.Join("", "ROOM01", "", nil, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeRoomStarted {
		t.Errorf("code = %v", err)
	}

	// A not-yet-started room that is full reports room-full.
	m2, _ := testMgr(t)
	mustJoin(t, m2, "ROOM02", opts(2, func(o *protocol.RoomOptions) {
		o.MaxPlayers = 3
	}), &fakeConn{}, "")
	mustJoin(t, m2, "ROOM02", nil, &fakeConn{}, "")
	// 2/3 joined; free the semantics check by filling all slots without starting:
	// actually fill the third to start it... instead use a 2-player room with lateJoin.
	m3, _ := testMgr(t)
	mustJoin(t, m3, "ROOM03", opts(2, func(o *protocol.RoomOptions) { o.AllowLateJoin = true }), &fakeConn{}, "")
	mustJoin(t, m3, "ROOM03", nil, &fakeConn{}, "")
	_, err = m3.Join("", "ROOM03", "", nil, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeRoomFull {
		t.Errorf("full started room with lateJoin: code = %v", err)
	}
}

func TestExplicitLeaveFreesSlotAndReusesLowest(t *testing.T) {
	m, _ := testMgr(t)
	a, b, c := &fakeConn{name: "a"}, &fakeConn{name: "b"}, &fakeConn{name: "c"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(3, func(o *protocol.RoomOptions) { o.AllowLateJoin = true }), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")

	sessA.Room.Leave(sessA.PlayerID, a)
	pl, ok := lastOfType[protocol.PlayerLeft](b)
	if !ok || pl.PlayerID != 0 || pl.Reason != protocol.LeftReasonExplicit {
		t.Errorf("B missed explicit leave: %+v", pl)
	}

	// C takes the lowest open slot: 0. B keeps 1 (never renumbered).
	sessC, resC := mustJoin(t, m, "ROOM01", nil, c, "")
	if sessC.PlayerID != 0 {
		t.Errorf("C got slot %d, want 0", sessC.PlayerID)
	}
	if !resC.Players[1].Occupied || !resC.Players[1].Connected {
		t.Errorf("B slot wrong after A left: %+v", resC.Players)
	}

	// Stale leave from A must not evict C from slot 0.
	sessA.Room.Leave(sessA.PlayerID, a)
	_, resCheck := mustJoin(t, m, "ROOM01", nil, &fakeConn{name: "d"}, "")
	if !resCheck.Players[0].Occupied {
		t.Error("stale leave freed the reused slot")
	}
}

func TestDisconnectPreservesSlot(t *testing.T) {
	m, _ := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(2), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")

	sessA.Room.Disconnect(sessA.PlayerID, a)
	pl, ok := lastOfType[protocol.PlayerLeft](b)
	if !ok || pl.PlayerID != 0 || pl.Reason != protocol.LeftReasonDisconnected {
		t.Errorf("B missed disconnect: %+v", pl)
	}

	// Slot 0 remains occupied: a fresh join finds the room full/started.
	_, err := m.Join("", "ROOM01", "", nil, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeRoomStarted {
		t.Errorf("code = %v", err)
	}
}

func TestResumeToken(t *testing.T) {
	m, clock := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	sessA, resA := mustJoin(t, m, "ROOM01", opts(2), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")

	sessA.Room.Disconnect(sessA.PlayerID, a)
	clock.Advance(10 * time.Second)

	a2 := &fakeConn{name: "a2"}
	sessA2, resA2 := mustJoin(t, m, "ROOM01", nil, a2, resA.ResumeToken)
	if sessA2.PlayerID != 0 {
		t.Errorf("resume got slot %d, want 0", sessA2.PlayerID)
	}
	if resA2.ResumeToken == resA.ResumeToken || resA2.ResumeToken == "" {
		t.Error("resume must rotate the token")
	}
	if !resA2.Started {
		t.Error("resume must report current started state")
	}
	rj, ok := lastOfType[protocol.PlayerRejoined](b)
	if !ok || rj.PlayerID != 0 || rj.WasReplacement {
		t.Errorf("B missed rejoin: %+v", rj)
	}

	// The old token is dead; with both slots occupied+started this is
	// a fresh join attempt that must be rejected.
	_, err := m.Join("", "ROOM01", resA.ResumeToken, nil, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeRoomStarted {
		t.Errorf("stale token treated as resume: %v", err)
	}
}

func TestResumeSupersedesLiveConnection(t *testing.T) {
	m, _ := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	sessA, resA := mustJoin(t, m, "ROOM01", opts(2), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")

	a2 := &fakeConn{name: "a2"}
	sessA2, _ := mustJoin(t, m, "ROOM01", nil, a2, resA.ResumeToken)
	if kicks := a.kickCodes(); len(kicks) != 1 || kicks[0] != protocol.ErrCodeSuperseded {
		t.Errorf("old conn kicks = %v", kicks)
	}

	// The superseded transport can no longer act for player 0.
	err := sessA.Room.Signal(sessA.PlayerID, a, 1, json.RawMessage(`{"kind":"offer"}`))
	if errCode(t, err) != protocol.ErrCodeNotJoined {
		t.Errorf("superseded signal: %v", err)
	}
	// The new one can.
	if err := sessA2.Room.Signal(sessA2.PlayerID, a2, 1, json.RawMessage(`{"kind":"offer"}`)); err != nil {
		t.Errorf("new conn signal failed: %v", err)
	}
}

func TestResumeDisabledFallsBackToFreshJoin(t *testing.T) {
	m, _ := testMgr(t)
	a := &fakeConn{name: "a"}
	sessA, resA := mustJoin(t, m, "ROOM01", opts(3, func(o *protocol.RoomOptions) {
		o.AllowReconnect = false
		o.WaitUntilFull = false
		o.AllowLateJoin = true
	}), a, "")
	sessA.Room.Disconnect(sessA.PlayerID, a)

	// allowReconnect=false: the token is ignored and this behaves as a
	// fresh join into the next free slot (0 is still occupied).
	a2 := &fakeConn{name: "a2"}
	sessA2, _ := mustJoin(t, m, "ROOM01", nil, a2, resA.ResumeToken)
	if sessA2.PlayerID != 1 {
		t.Errorf("got slot %d, want 1 (fresh join)", sessA2.PlayerID)
	}
}

func TestBogusTokenFallsBackToFreshJoin(t *testing.T) {
	m, _ := testMgr(t)
	a := &fakeConn{name: "a"}
	mustJoin(t, m, "ROOM01", opts(2, func(o *protocol.RoomOptions) {
		o.WaitUntilFull = false
		o.AllowLateJoin = true
	}), a, "")
	b := &fakeConn{name: "b"}
	sessB, _ := mustJoin(t, m, "ROOM01", nil, b, "bogus-token-from-another-life")
	if sessB.PlayerID != 1 {
		t.Errorf("bogus token join got slot %d, want 1", sessB.PlayerID)
	}
}

func TestClaimAfterSilence(t *testing.T) {
	m, clock := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(2), a, "")
	sessB, _ := mustJoin(t, m, "ROOM01", nil, b, "")

	// 39s of silence: not claimable yet (threshold is 40s).
	clock.Advance(39 * time.Second)
	_, err := m.Claim("", "ROOM01", 0, &fakeConn{name: "c"}, passthrough)
	if errCode(t, err) != protocol.ErrCodeSlotNotClaimable {
		t.Errorf("39s claim: %v", err)
	}

	// Exactly 40s: claimable (>= threshold), even though A's transport
	// is still attached — silence is what counts.
	clock.Advance(1 * time.Second)
	c := &fakeConn{name: "c"}
	sessC, resC := m.mustClaim(t, "ROOM01", 0, c)
	if sessC.PlayerID != 0 || resC.SelfID != 0 {
		t.Errorf("claim result: %+v", resC)
	}
	if kicks := a.kickCodes(); len(kicks) != 1 || kicks[0] != protocol.ErrCodeReplaced {
		t.Errorf("A kicks = %v", kicks)
	}
	pr, ok := lastOfType[protocol.PlayerReplaced](b)
	if !ok || pr.PlayerID != 0 {
		t.Errorf("B missed player-replaced: %+v ok=%v", pr, ok)
	}

	// The replaced transport is powerless now.
	err = sessA.Room.Signal(sessA.PlayerID, a, 1, json.RawMessage(`{"kind":"offer"}`))
	if errCode(t, err) != protocol.ErrCodeNotJoined {
		t.Errorf("replaced conn signal: %v", err)
	}
	sessA.Room.Leave(sessA.PlayerID, a) // must be a no-op
	if err := sessC.Room.Signal(sessC.PlayerID, c, 1, json.RawMessage(`{"kind":"offer"}`)); err != nil {
		t.Errorf("claimant signal failed: %v", err)
	}
	_ = sessB
}

// mustClaim is a test helper mirroring mustJoin.
func (m *Manager) mustClaim(t *testing.T, code string, playerID int, conn *fakeConn) (*Session, *JoinResult) {
	t.Helper()
	sess, err := m.Claim("", code, playerID, conn, passthrough)
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	res, ok := lastOfType[*JoinResult](conn)
	if !ok {
		t.Fatal("no JoinResult delivered to claimant")
	}
	return sess, res
}

func TestActivityResetsClaimTimer(t *testing.T) {
	m, clock := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(2), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")

	clock.Advance(35 * time.Second)
	sessA.Room.Touch(sessA.PlayerID, a) // any inbound message counts
	clock.Advance(35 * time.Second)

	_, err := m.Claim("", "ROOM01", 0, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeSlotNotClaimable {
		t.Errorf("claim after touch: %v", err)
	}

	// Signaling also resets the timer.
	clock.Advance(6 * time.Second) // 41s since touch: claimable, so signal first
	sessA.Room.Signal(sessA.PlayerID, a, 1, json.RawMessage(`{"kind":"ice","candidate":{}}`))
	_, err = m.Claim("", "ROOM01", 0, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeSlotNotClaimable {
		t.Errorf("claim after signal: %v", err)
	}
}

func TestClaimPolicyEnforcement(t *testing.T) {
	m, clock := testMgr(t)
	a := &fakeConn{name: "a"}
	mustJoin(t, m, "TOKONLY", opts(2, func(o *protocol.RoomOptions) {
		o.ReconnectPolicy = protocol.PolicyTokenOnly
	}), a, "")
	clock.Advance(time.Hour)
	_, err := m.Claim("", "TOKONLY", 0, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeClaimNotAllowed {
		t.Errorf("token-only claim: %v", err)
	}

	b := &fakeConn{name: "b"}
	mustJoin(t, m, "NOREPL", opts(2, func(o *protocol.RoomOptions) {
		o.AllowReplacement = false
	}), b, "")
	clock.Advance(time.Hour)
	_, err = m.Claim("", "NOREPL", 0, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeClaimNotAllowed {
		t.Errorf("allowReplacement=false claim: %v", err)
	}
}

func TestClaimValidation(t *testing.T) {
	m, _ := testMgr(t)
	a := &fakeConn{name: "a"}
	mustJoin(t, m, "ROOM01", opts(2), a, "")

	_, err := m.Claim("", "MISSING1", 0, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeRoomNotFound {
		t.Errorf("claim missing room: %v", err)
	}
	_, err = m.Claim("", "ROOM01", 5, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeInvalidTarget {
		t.Errorf("claim out of range: %v", err)
	}
	_, err = m.Claim("", "ROOM01", -1, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeInvalidTarget {
		t.Errorf("claim negative: %v", err)
	}
}

func TestClaimUnoccupiedSlotActsAsTargetedJoin(t *testing.T) {
	m, _ := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(2), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")
	sessA.Room.Leave(sessA.PlayerID, a)

	// Room is started (2/2 filled earlier) and slot 0 is now free.
	// Claiming it is the recovery path even though allowLateJoin=false.
	c := &fakeConn{name: "c"}
	sessC, _ := m.mustClaim(t, "ROOM01", 0, c)
	if sessC.PlayerID != 0 {
		t.Errorf("claimed free slot = %d", sessC.PlayerID)
	}
	pj, ok := lastOfType[protocol.PlayerJoined](b)
	if !ok || pj.PlayerID != 0 {
		t.Errorf("B should see player-joined for a freed-slot claim: %+v", pj)
	}
}

func TestClaimZeroTimeout(t *testing.T) {
	m, _ := testMgr(t)
	a := &fakeConn{name: "a"}
	mustJoin(t, m, "ROOM01", opts(2, func(o *protocol.RoomOptions) {
		o.ClaimAfter = 0
	}), a, "")
	// claimAfter=0: instantly claimable, even while connected.
	if _, err := m.Claim("", "ROOM01", 0, &fakeConn{}, passthrough); err != nil {
		t.Errorf("zero-timeout claim failed: %v", err)
	}
}

func TestSignalValidationAndDelivery(t *testing.T) {
	m, _ := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(3, func(o *protocol.RoomOptions) {
		o.WaitUntilFull = false
		o.AllowLateJoin = true
	}), a, "")
	sessB, _ := mustJoin(t, m, "ROOM01", nil, b, "")

	payload := json.RawMessage(`{"kind":"offer","sdp":"v=0"}`)
	if err := sessA.Room.Signal(sessA.PlayerID, a, 1, payload); err != nil {
		t.Fatalf("signal failed: %v", err)
	}
	sig, ok := lastOfType[protocol.SignalIn](b)
	if !ok || sig.From != 0 || string(sig.Payload) != string(payload) {
		t.Errorf("B signal wrong: %+v", sig)
	}

	if got := errCode(t, sessA.Room.Signal(0, a, 0, payload)); got != protocol.ErrCodeInvalidTarget {
		t.Errorf("self signal: %v", got)
	}
	if got := errCode(t, sessA.Room.Signal(0, a, 7, payload)); got != protocol.ErrCodeInvalidTarget {
		t.Errorf("out of range: %v", got)
	}
	if got := errCode(t, sessA.Room.Signal(0, a, 2, payload)); got != protocol.ErrCodeInvalidTarget {
		t.Errorf("unoccupied: %v", got)
	}

	sessB.Room.Disconnect(sessB.PlayerID, b)
	if got := errCode(t, sessA.Room.Signal(0, a, 1, payload)); got != protocol.ErrCodeTargetUnavailable {
		t.Errorf("disconnected target: %v", got)
	}
}

func TestSlowConsumerIsDetachedAndAnnounced(t *testing.T) {
	m, _ := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	mustJoin(t, m, "ROOM01", opts(3, func(o *protocol.RoomOptions) {
		o.WaitUntilFull = false
		o.AllowLateJoin = true
	}), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")

	b.setFull(true)
	c := &fakeConn{name: "c"}
	_, resC := mustJoin(t, m, "ROOM01", nil, c, "")

	if kicks := b.kickCodes(); len(kicks) != 1 || kicks[0] != protocol.ErrCodeSlowConsumer {
		t.Errorf("B kicks = %v", kicks)
	}
	// A sees both the join of C and the resulting drop of B.
	pj, _ := lastOfType[protocol.PlayerJoined](a)
	if pj.PlayerID != 2 {
		t.Errorf("A missed C's join: %+v", pj)
	}
	pl, ok := lastOfType[protocol.PlayerLeft](a)
	if !ok || pl.PlayerID != 1 || pl.Reason != protocol.LeftReasonDisconnected {
		t.Errorf("A missed B's drop: %+v", pl)
	}
	// B's slot survives (occupied, disconnected) for later resume.
	if !resC.Players[1].Occupied {
		t.Error("B's slot was freed")
	}
}

func TestGCEmptyTTL(t *testing.T) {
	m, clock := testMgr(t)
	a := &fakeConn{name: "a"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(2), a, "")

	// Live connection: GC must not touch it, even far in the future
	// (short of MaxTTL).
	clock.Advance(6 * time.Minute)
	if n := m.GC(); n != 0 || m.RoomCount() != 1 {
		t.Errorf("GC destroyed a live room: n=%d count=%d", n, m.RoomCount())
	}

	sessA.Room.Disconnect(sessA.PlayerID, a)
	clock.Advance(4 * time.Minute)
	if n := m.GC(); n != 0 {
		t.Errorf("GC before empty TTL: n=%d", n)
	}
	clock.Advance(1 * time.Minute) // exactly 5m since disconnect
	if n := m.GC(); n != 1 || m.RoomCount() != 0 {
		t.Errorf("GC at empty TTL: n=%d count=%d", n, m.RoomCount())
	}

	// The code is reusable afterwards.
	_, res := mustJoin(t, m, "ROOM01", opts(2), &fakeConn{}, "")
	if res.SelfID != 0 || res.Started {
		t.Errorf("recreated room wrong: %+v", res)
	}
}

func TestGCEmptyTTLResetOnReattach(t *testing.T) {
	m, clock := testMgr(t)
	a := &fakeConn{name: "a"}
	sessA, resA := mustJoin(t, m, "ROOM01", opts(2), a, "")
	sessA.Room.Disconnect(sessA.PlayerID, a)

	clock.Advance(4 * time.Minute)
	a2 := &fakeConn{name: "a2"}
	mustJoin(t, m, "ROOM01", nil, a2, resA.ResumeToken) // re-attach resets emptiness

	clock.Advance(4 * time.Minute)
	if n := m.GC(); n != 0 {
		t.Errorf("GC destroyed room with live conn: n=%d", n)
	}
}

func TestGCMaxTTLKicksEveryone(t *testing.T) {
	m, clock := testMgr(t)
	a, b := &fakeConn{name: "a"}, &fakeConn{name: "b"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(2), a, "")
	mustJoin(t, m, "ROOM01", nil, b, "")

	clock.Advance(24 * time.Hour)
	// Keep A "active" to prove MaxTTL wins regardless of activity.
	sessA.Room.Touch(sessA.PlayerID, a)
	if n := m.GC(); n != 1 {
		t.Fatalf("GC at max TTL: n=%d", n)
	}
	for _, conn := range []*fakeConn{a, b} {
		if kicks := conn.kickCodes(); len(kicks) != 1 || kicks[0] != protocol.ErrCodeRoomExpired {
			t.Errorf("%s kicks = %v", conn.name, kicks)
		}
	}
	// Operations on the destroyed room fail cleanly.
	err := sessA.Room.Signal(sessA.PlayerID, a, 1, json.RawMessage(`{"kind":"offer"}`))
	if errCode(t, err) != protocol.ErrCodeRoomExpired {
		t.Errorf("signal on destroyed room: %v", err)
	}
}

func TestMaxRoomsWithOpportunisticGC(t *testing.T) {
	clock := newFakeClock()
	m := NewManager(Limits{EmptyTTL: time.Minute, MaxTTL: 24 * time.Hour, MaxRooms: 2}, clock.Now)

	a := &fakeConn{name: "a"}
	sessA, _ := mustJoin(t, m, "ROOM01", opts(2), a, "")
	mustJoin(t, m, "ROOM02", opts(2), &fakeConn{name: "b"}, "")

	_, err := m.Join("", "ROOM03", "", opts(2), &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeTooManyRooms {
		t.Errorf("over limit: %v", err)
	}

	// Once ROOM01 is empty past its TTL, creating ROOM03 succeeds via
	// the opportunistic sweep.
	sessA.Room.Disconnect(sessA.PlayerID, a)
	clock.Advance(2 * time.Minute)
	if _, err := m.Join("", "ROOM03", "", opts(2), &fakeConn{}, passthrough); err != nil {
		t.Errorf("create after sweep failed: %v", err)
	}
	if m.RoomCount() != 2 {
		t.Errorf("RoomCount = %d, want 2", m.RoomCount())
	}
}

func TestAppScoping(t *testing.T) {
	m, _ := testMgr(t)
	a := &fakeConn{name: "a"}
	if _, err := m.Join("app1", "ROOM01", "", opts(2), a, passthrough); err != nil {
		t.Fatal(err)
	}
	_, err := m.Join("", "ROOM01", "", nil, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeAppMismatch {
		t.Errorf("app mismatch join: %v", err)
	}
	_, err = m.Join("app2", "ROOM01", "", nil, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeAppMismatch {
		t.Errorf("wrong app join: %v", err)
	}
	_, err = m.Claim("app2", "ROOM01", 0, &fakeConn{}, passthrough)
	if errCode(t, err) != protocol.ErrCodeAppMismatch {
		t.Errorf("wrong app claim: %v", err)
	}
}

func TestTokensAreUniquePerPlayer(t *testing.T) {
	m, _ := testMgr(t)
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		conn := &fakeConn{name: fmt.Sprintf("p%d", i)}
		_, res := mustJoin(t, m, "BIGROOM1", func() *protocol.RoomOptions {
			if i == 0 {
				return opts(8)
			}
			return nil
		}(), conn, "")
		if seen[res.ResumeToken] {
			t.Fatalf("duplicate token at player %d", i)
		}
		seen[res.ResumeToken] = true
	}
}

// TestConcurrentChaos hammers one manager from many goroutines to give
// the race detector something to chew on. Assertions are minimal; the
// point is that invariants hold under contention without deadlock.
func TestConcurrentChaos(t *testing.T) {
	m, clock := testMgr(t)
	const workers = 8
	const iterations = 300

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			var sess *Session
			var conn *fakeConn
			for i := 0; i < iterations; i++ {
				switch rng.Intn(6) {
				case 0: // join/create
					if sess == nil {
						conn = &fakeConn{}
						create := opts(4, func(o *protocol.RoomOptions) {
							o.WaitUntilFull = false
							o.AllowLateJoin = true
							o.ClaimAfter = 0
						})
						s, err := m.Join("", "CHAOS001", "", create, conn, passthrough)
						if err == nil {
							sess = s
						}
					}
				case 1: // signal
					if sess != nil {
						_ = sess.Room.Signal(sess.PlayerID, conn, rng.Intn(5)-1, json.RawMessage(`{"kind":"ice"}`))
					}
				case 2: // leave
					if sess != nil {
						sess.Room.Leave(sess.PlayerID, conn)
						sess = nil
					}
				case 3: // disconnect
					if sess != nil {
						sess.Room.Disconnect(sess.PlayerID, conn)
						sess = nil
					}
				case 4: // claim
					if sess == nil {
						conn = &fakeConn{}
						s, err := m.Claim("", "CHAOS001", rng.Intn(4), conn, passthrough)
						if err == nil {
							sess = s
						}
					}
				case 5: // time passes + GC
					clock.Advance(time.Duration(rng.Intn(1000)) * time.Millisecond)
					m.GC()
				}
			}
		}(int64(w))
	}
	wg.Wait()
}
