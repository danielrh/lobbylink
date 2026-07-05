package protocol

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateCode(t *testing.T) {
	valid := []string{"TEST", "XYYYZZ", "abcd", "A1-_z", strings.Repeat("a", 64)}
	for _, c := range valid {
		if err := ValidateCode(c); err != nil {
			t.Errorf("ValidateCode(%q) = %v, want nil", c, err)
		}
	}
	invalid := []string{"", "abc", strings.Repeat("a", 65), "has space", "semi;colon", "sla/sh", "unié", "dot.dot"}
	for _, c := range invalid {
		err := ValidateCode(c)
		if err == nil {
			t.Errorf("ValidateCode(%q) = nil, want error", c)
			continue
		}
		var pe *ProtoError
		if !errors.As(err, &pe) || pe.Code != ErrCodeInvalidCode {
			t.Errorf("ValidateCode(%q) code = %v, want invalid-code", c, err)
		}
	}
}

func mustParse(t *testing.T, s string) *ClientMessage {
	t.Helper()
	m, err := ParseClientMessage([]byte(s))
	if err != nil {
		t.Fatalf("ParseClientMessage(%s) failed: %v", s, err)
	}
	return m
}

func parseErrCode(t *testing.T, s string) string {
	t.Helper()
	_, err := ParseClientMessage([]byte(s))
	if err == nil {
		t.Fatalf("ParseClientMessage(%s) succeeded, want error", s)
	}
	var pe *ProtoError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not *ProtoError", err)
	}
	return pe.Code
}

func TestParseClientMessage(t *testing.T) {
	m := mustParse(t, `{"type":"join","code":"XYYYZZ","resumeToken":null,"create":{"maxPlayers":4,"waitUntilFull":true,"allowLateJoin":false,"allowReconnect":true,"allowReplacement":true,"reconnectPolicy":"token-or-claim-after-timeout","claimAfterMs":40000}}`)
	if m.Type != TypeJoin || m.Code != "XYYYZZ" || m.Create == nil || m.Create.MaxPlayers != 4 {
		t.Errorf("join parse wrong: %+v", m)
	}
	if m.Create.WaitUntilFull == nil || !*m.Create.WaitUntilFull || m.Create.AllowLateJoin == nil || *m.Create.AllowLateJoin {
		t.Errorf("create booleans wrong: %+v", m.Create)
	}
	if m.Create.ClaimAfterMs == nil || *m.Create.ClaimAfterMs != 40000 {
		t.Errorf("claimAfterMs wrong: %+v", m.Create)
	}

	m = mustParse(t, `{"type":"claim-slot","code":"XYYYZZ","playerId":2,"appId":"anderrh-github"}`)
	if m.PlayerID == nil || *m.PlayerID != 2 || m.AppID != "anderrh-github" {
		t.Errorf("claim parse wrong: %+v", m)
	}

	m = mustParse(t, `{"type":"signal","to":1,"payload":{"kind":"offer","sdp":"v=0..."}}`)
	if m.To == nil || *m.To != 1 || len(m.Payload) == 0 {
		t.Errorf("signal parse wrong: %+v", m)
	}
	mustParse(t, `{"type":"signal","to":0,"payload":{"kind":"answer","sdp":"x"}}`)
	mustParse(t, `{"type":"signal","to":0,"payload":{"kind":"ice","candidate":{"candidate":"..."}}}`)
	mustParse(t, `{"type":"leave"}`)

	// Unknown extra fields are tolerated (forward compatibility).
	mustParse(t, `{"type":"leave","futureField":123}`)
}

func TestParseClientMessageErrors(t *testing.T) {
	cases := map[string]string{
		`{`:                                    ErrCodeInvalidMessage,
		`{"type":"nope"}`:                      ErrCodeInvalidMessage,
		`{"code":"XYYYZZ"}`:                    ErrCodeInvalidMessage, // missing type
		`{"type":"join","code":"ab"}`:          ErrCodeInvalidCode,
		`{"type":"join","code":"bad code"}`:    ErrCodeInvalidCode,
		`{"type":"claim-slot","code":"ABCD"}`:  ErrCodeInvalidMessage, // missing playerId
		`{"type":"signal"}`:                    ErrCodeInvalidMessage, // missing to
		`{"type":"signal","to":1}`:             ErrCodeInvalidMessage, // missing payload
		`{"type":"signal","to":1,"payload":5}`: ErrCodeInvalidMessage,
		`{"type":"signal","to":1,"payload":{"kind":"evil"}}`:                             ErrCodeInvalidMessage,
		`{"type":"join","code":"ABCD","resumeToken":"` + strings.Repeat("x", 200) + `"}`: ErrCodeInvalidMessage,
	}
	for input, want := range cases {
		if got := parseErrCode(t, input); got != want {
			t.Errorf("ParseClientMessage(%s) code = %q, want %q", input, got, want)
		}
	}
}

func boolPtr(b bool) *bool                         { return &b }
func i64Ptr(n int64) *int64                        { return &n }
func policyPtr(p ReconnectPolicy) *ReconnectPolicy { return &p }

func TestResolveCreateDefaults(t *testing.T) {
	r, err := ResolveCreate(&CreateOptions{MaxPlayers: 4}, 32, 40*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := RoomOptions{
		MaxPlayers: 4, WaitUntilFull: false, AllowLateJoin: true,
		AllowReconnect: true, AllowReplacement: true,
		ReconnectPolicy: PolicyTokenOrClaimAfterTimeout, ClaimAfter: 40 * time.Second,
	}
	if r != want {
		t.Errorf("defaults = %+v, want %+v", r, want)
	}
}

func TestResolveCreateExplicit(t *testing.T) {
	r, err := ResolveCreate(&CreateOptions{
		MaxPlayers:       8,
		WaitUntilFull:    boolPtr(true),
		AllowLateJoin:    boolPtr(false),
		AllowReconnect:   boolPtr(false),
		AllowReplacement: boolPtr(false),
		ReconnectPolicy:  policyPtr(PolicyTokenOnly),
		ClaimAfterMs:     i64Ptr(0),
	}, 32, 40*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !r.WaitUntilFull || r.AllowLateJoin || r.AllowReconnect || r.AllowReplacement ||
		r.ReconnectPolicy != PolicyTokenOnly || r.ClaimAfter != 0 {
		t.Errorf("explicit = %+v", r)
	}
}

func TestResolveCreateErrors(t *testing.T) {
	cases := []*CreateOptions{
		nil,
		{MaxPlayers: 0},
		{MaxPlayers: -1},
		{MaxPlayers: 33},
		{MaxPlayers: 2, ReconnectPolicy: policyPtr(PolicyHostApproval)},
		{MaxPlayers: 2, ReconnectPolicy: policyPtr(ReconnectPolicy("bogus"))},
		{MaxPlayers: 2, ClaimAfterMs: i64Ptr(-1)},
		{MaxPlayers: 2, ClaimAfterMs: i64Ptr(int64(25 * time.Hour / time.Millisecond))},
	}
	for i, c := range cases {
		_, err := ResolveCreate(c, 32, 40*time.Second)
		var pe *ProtoError
		if err == nil || !errors.As(err, &pe) || pe.Code != ErrCodeInvalidCreate {
			t.Errorf("case %d: err = %v, want invalid-create", i, err)
		}
	}
	// App-limited cap applies through the hardMaxPlayers argument.
	if _, err := ResolveCreate(&CreateOptions{MaxPlayers: 17}, 16, time.Second); err == nil {
		t.Error("app cap not enforced")
	}
}

func TestErrorMessage(t *testing.T) {
	e := ErrorMessage(Errf(ErrCodeRoomFull, "room %q is full", "X"))
	if e.Type != TypeError || e.Code != ErrCodeRoomFull || e.Message != `room "X" is full` {
		t.Errorf("ErrorMessage = %+v", e)
	}
	e = ErrorMessage(errors.New("boom"))
	if e.Code != ErrCodeInternal || strings.Contains(e.Message, "boom") {
		t.Errorf("internal errors must not leak details: %+v", e)
	}
}

func TestPolicyHelpers(t *testing.T) {
	if !PolicyTokenOnly.Valid() || !PolicyTokenOrClaimAfterTimeout.Valid() || !PolicyClaimAfterTimeout.Valid() {
		t.Error("implemented policies must be valid")
	}
	if PolicyHostApproval.Valid() || ReconnectPolicy("x").Valid() {
		t.Error("unimplemented policies must be invalid")
	}
	if PolicyTokenOnly.AllowsClaim() {
		t.Error("token-only must not allow claims")
	}
	if !PolicyTokenOrClaimAfterTimeout.AllowsClaim() || !PolicyClaimAfterTimeout.AllowsClaim() {
		t.Error("claim policies must allow claims")
	}
}
