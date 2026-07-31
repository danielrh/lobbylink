package lobbylink

import (
	"bytes"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	payload := []byte("hello, frame")
	f, err := parseFrame(makeFrame(7, 2, 5, payload))
	if err != nil {
		t.Fatal(err)
	}
	if f.msgID != 7 || f.seq != 2 || f.total != 5 || !bytes.Equal(f.payload, payload) {
		t.Fatalf("round trip mismatch: %+v", f)
	}
}

func TestFrameHeaderLayout(t *testing.T) {
	// Byte-for-byte layout check against the wire contract.
	frame := makeFrame(0x01020304, 0x05060708, 0x090a0b0c, []byte{0xff})
	want := []byte{
		0x4c, 0x01,
		0x01, 0x02, 0x03, 0x04,
		0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c,
		0x00, 0x00, 0x00, 0x01,
		0xff,
	}
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame layout mismatch:\n got %x\nwant %x", frame, want)
	}
}

func TestParseFrameRejects(t *testing.T) {
	good := makeFrame(1, 0, 1, []byte("x"))
	cases := map[string]func([]byte) []byte{
		"short":        func(b []byte) []byte { return b[:frameHeaderLen-1] },
		"magic":        func(b []byte) []byte { b[0] = 0x4d; return b },
		"version":      func(b []byte) []byte { b[1] = 0x02; return b },
		"zero total":   func(b []byte) []byte { b[13] = 0; return b },
		"seq >= total": func(b []byte) []byte { b[9] = 9; return b },
		"len mismatch": func(b []byte) []byte { return append(b, 0x00) },
	}
	for name, mutate := range cases {
		if _, err := parseFrame(mutate(append([]byte(nil), good...))); err == nil {
			t.Errorf("%s: expected parse error", name)
		}
	}
}

func TestReassemblerOrderAndDedup(t *testing.T) {
	r := newReassembler()
	now := time.Now()
	full := bytes.Repeat([]byte("abcdefgh"), 5000) // 40 KB, 3 chunks
	total := chunkCount(len(full))
	if total != 3 {
		t.Fatalf("expected 3 chunks, got %d", total)
	}
	chunk := func(seq uint32) frame {
		start := int(seq) * chunkPayload
		end := min(start+chunkPayload, len(full))
		return frame{msgID: 1, seq: seq, total: total, payload: full[start:end]}
	}
	// Out of order, with a duplicate.
	if got := r.push(chunk(2), now); got != nil {
		t.Fatal("incomplete message returned early")
	}
	if got := r.push(chunk(0), now); got != nil {
		t.Fatal("incomplete message returned early")
	}
	if got := r.push(chunk(0), now); got != nil {
		t.Fatal("duplicate chunk completed message")
	}
	got := r.push(chunk(1), now)
	if !bytes.Equal(got, full) {
		t.Fatalf("reassembled message mismatch (%d bytes vs %d)", len(got), len(full))
	}
}

func TestReassemblerTimeout(t *testing.T) {
	r := newReassembler()
	start := time.Now()
	r.push(frame{msgID: 1, seq: 0, total: 2, payload: []byte("a")}, start)
	// The half-done message is pruned once stale; the late second half
	// then starts a fresh (incomplete) entry instead of completing.
	late := start.Add(reassemblyTimeout + time.Second)
	if got := r.push(frame{msgID: 1, seq: 1, total: 2, payload: []byte("b")}, late); got != nil {
		t.Fatal("timed-out reassembly still completed")
	}
}

func TestSignalingURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com":              "wss://example.com/ws",
		"https://example.com/":             "wss://example.com/ws",
		"https://example.com/lobbylink":    "wss://example.com/lobbylink/ws",
		"https://example.com/lobbylink/ws": "wss://example.com/lobbylink/ws",
		"http://127.0.0.1:8787":            "ws://127.0.0.1:8787/ws",
		"wss://example.com:4443/ws?x=1#f":  "wss://example.com:4443/ws",
		"ws://localhost:8787/lobbylink///": "ws://localhost:8787/lobbylink/ws",
	}
	for in, want := range cases {
		got, err := signalingURL(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"example.com", "ftp://example.com", "https://"} {
		if _, err := signalingURL(bad); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestDefaultOrigin(t *testing.T) {
	cases := map[string]string{
		"wss://example.com/lobbylink/ws": "https://example.com",
		"ws://127.0.0.1:8787/ws":         "http://127.0.0.1:8787",
	}
	for in, want := range cases {
		if got := defaultOrigin(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
