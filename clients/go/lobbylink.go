// Package lobbylink is the Go client for the lobbylink P2P lobby
// system: lobby membership over a WebSocket signaling server plus
// direct WebRTC DataChannels (via pion) between every pair of players.
// It is wire-compatible with the TypeScript (browser), Rust, and Java
// clients — a Go game and a browser game can share one room.
//
// Contract highlights (see clients/ts/README.md for the full wire
// contract):
//
//   - Per peer pair, two pre-negotiated SCTP DataChannels:
//     "reliable" (negotiated id=1, ordered) and "best-effort"
//     (negotiated id=2, unordered, maxRetransmits=0). Both sides create
//     both; the lower player ID of the pair makes the SDP offer.
//   - Reliable payloads are chunked into 16 KiB frames with an 18-byte
//     big-endian header (magic 0x4C, version 1); max 16 MiB.
//   - Best-effort payloads are raw datagrams, at most 16000 bytes, and
//     may be dropped anywhere.
package lobbylink

import (
	"fmt"
	"time"
)

// Error is a lobby failure with a stable machine-readable Code.
// Server-reported codes (e.g. "room-full", "slot-not-claimable") pass
// through unchanged; client-side failures use codes like
// "connect-timeout", "invalid-target", "message-too-large",
// "channel-timeout", "send-failed", "closed".
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ReconnectPolicy controls how a room slot may be re-acquired.
type ReconnectPolicy string

const (
	TokenOnly                ReconnectPolicy = "token-only"
	TokenOrClaimAfterTimeout ReconnectPolicy = "token-or-claim-after-timeout"
	ClaimAfterTimeout        ReconnectPolicy = "claim-after-timeout"
)

// CreateOptions configure room creation for the first player in.
type CreateOptions struct {
	MaxPlayers       int
	WaitUntilFull    *bool
	AllowLateJoin    *bool
	AllowReconnect   *bool
	AllowReplacement *bool
	ReconnectPolicy  ReconnectPolicy
	ClaimAfter       time.Duration // 0 = server default
}

// NewCreateOptions returns CreateOptions with server defaults for
// everything but the player count.
func NewCreateOptions(maxPlayers int) *CreateOptions {
	return &CreateOptions{MaxPlayers: maxPlayers}
}

// ICEServer mirrors the WebRTC RTCIceServer dictionary.
type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// Options configure Connect.
type Options struct {
	// Server is "https://host[:port][/path]" or a ws(s) URL; "/ws" is
	// appended automatically, so subpath deployments work unchanged.
	Server string
	// Code is the room code, 4-64 chars of [A-Za-z0-9_-].
	Code string
	// AppID is the optional app policy id for hosted static sites.
	AppID string
	// Create makes the room if it does not exist; nil joins only.
	Create *CreateOptions
	// ResumeToken resumes our old slot after a reconnect. Overrides the
	// token stored in TokenFile.
	ResumeToken string
	// ClaimPlayerID claims a specific silent slot after the resume
	// token is gone (claim-slot); nil for a normal join.
	ClaimPlayerID *int
	// TokenFile, if set, persists the hidden resume token across
	// process restarts (the native analog of the browser storageKey).
	TokenFile string
	// Origin overrides the Origin header. nil derives it from Server
	// (e.g. "https://host:port"), which production servers accept
	// without extra config; point to "" to send no Origin header
	// (servers running --allow-no-origin).
	Origin *string
	// ICEServers are appended to the set issued by the server.
	ICEServers []ICEServer
	// ForceRelay forces TURN (iceTransportPolicy relay); for testing.
	ForceRelay bool
	// ConnectTimeout bounds the join handshake; 0 means 20s.
	ConnectTimeout time.Duration
}

// MessageKind says which DataChannel carried a message.
type MessageKind string

const (
	Reliable   MessageKind = "reliable"
	BestEffort MessageKind = "best-effort"
)

// PlayerInfo is the public snapshot of one room slot.
type PlayerInfo struct {
	ID        int
	Occupied  bool
	Connected bool
}

// Event is a lobby or peer event; switch on the concrete type.
// The stream matches the other clients' event surface.
type Event interface{ isEvent() }

// MessageEvent is a payload from another player.
type MessageEvent struct {
	From int
	Kind MessageKind
	Data []byte
}

// PlayerJoinedEvent announces a fresh occupant of an empty slot.
type PlayerJoinedEvent struct{ PlayerID int }

// PlayerLeftEvent announces a leave. Reason "explicit-leave" frees the
// slot; "disconnected" only lost signaling — an established
// DataChannel to that player may still be alive.
type PlayerLeftEvent struct {
	PlayerID int
	Reason   string
}

// PlayerRejoinedEvent announces a token-based resume of a slot.
type PlayerRejoinedEvent struct {
	PlayerID       int
	WasReplacement bool
}

// PlayerReplacedEvent announces a tokenless claim of a silent slot.
type PlayerReplacedEvent struct{ PlayerID int }

// StartedEvent announces the room reached its start condition.
type StartedEvent struct{}

// PeerStateEvent reports the WebRTC connection state to one player:
// "connecting", "connected", "disconnected", "failed", "closed".
type PeerStateEvent struct {
	PlayerID int
	State    string
}

// CandidatePairEvent reports the selected ICE candidate types
// (host/srflx/relay) once a peer connects; for TURN debugging.
type CandidatePairEvent struct {
	PlayerID      int
	Local, Remote string
}

// LobbyErrorEvent is a non-fatal error reported by the lobby server.
type LobbyErrorEvent struct{ Code, Message string }

// SignalingClosedEvent means the signaling WebSocket is gone.
// Established DataChannels keep working unless Code is "replaced",
// "session-superseded" or "room-expired" (game over, peers torn down).
// A plain transport drop uses code "connection-lost".
type SignalingClosedEvent struct{ Code, Message string }

func (MessageEvent) isEvent()         {}
func (PlayerJoinedEvent) isEvent()    {}
func (PlayerLeftEvent) isEvent()      {}
func (PlayerRejoinedEvent) isEvent()  {}
func (PlayerReplacedEvent) isEvent()  {}
func (StartedEvent) isEvent()         {}
func (PeerStateEvent) isEvent()       {}
func (CandidatePairEvent) isEvent()   {}
func (LobbyErrorEvent) isEvent()      {}
func (SignalingClosedEvent) isEvent() {}

func isFatalCode(code string) bool {
	switch code {
	case "replaced", "session-superseded", "room-expired", "slow-consumer":
		return true
	}
	return false
}

func isGameOverCode(code string) bool {
	switch code {
	case "replaced", "session-superseded", "room-expired":
		return true
	}
	return false
}

func validCode(code string) bool {
	if len(code) < 4 || len(code) > 64 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
