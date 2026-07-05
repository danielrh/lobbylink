// Package protocol defines the JSON wire messages exchanged over the
// lobby/signaling WebSocket, plus strict validation for everything a
// client can send. The server never trusts a client-supplied field
// without it passing through this package first.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Message type strings, client -> server.
const (
	TypeJoin      = "join"
	TypeClaimSlot = "claim-slot"
	TypeSignal    = "signal"
	TypeLeave     = "leave"
)

// Message type strings, server -> client.
const (
	TypeJoined         = "joined"
	TypePlayerJoined   = "player-joined"
	TypePlayerLeft     = "player-left"
	TypePlayerRejoined = "player-rejoined"
	TypePlayerReplaced = "player-replaced"
	TypeRoomStarted    = "room-started"
	TypeError          = "error"
)

// Error codes carried in Error.Code. Stable API: clients switch on these.
const (
	ErrCodeInvalidMessage    = "invalid-message"
	ErrCodeInvalidCode       = "invalid-code"
	ErrCodeInvalidCreate     = "invalid-create"
	ErrCodeUnknownApp        = "unknown-app"
	ErrCodeAppMismatch       = "app-mismatch"
	ErrCodeOriginForbidden   = "origin-forbidden"
	ErrCodeRoomNotFound      = "room-not-found"
	ErrCodeRoomFull          = "room-full"
	ErrCodeRoomStarted       = "room-started"
	ErrCodeTooManyRooms      = "too-many-rooms"
	ErrCodeAlreadyJoined     = "already-joined"
	ErrCodeNotJoined         = "not-joined"
	ErrCodeInvalidTarget     = "invalid-target"
	ErrCodeTargetUnavailable = "target-unavailable"
	ErrCodeClaimNotAllowed   = "claim-not-allowed"
	ErrCodeSlotNotClaimable  = "slot-not-claimable"
	ErrCodeReplaced          = "replaced"
	ErrCodeSuperseded        = "session-superseded"
	ErrCodeRoomExpired       = "room-expired"
	ErrCodeSlowConsumer      = "slow-consumer"
	ErrCodeInternal          = "internal"
)

// Player-left reasons.
const (
	LeftReasonExplicit     = "explicit-leave"
	LeftReasonDisconnected = "disconnected"
)

// ReconnectPolicy controls how a slot may be re-acquired.
type ReconnectPolicy string

const (
	PolicyTokenOnly                ReconnectPolicy = "token-only"
	PolicyTokenOrClaimAfterTimeout ReconnectPolicy = "token-or-claim-after-timeout"
	PolicyClaimAfterTimeout        ReconnectPolicy = "claim-after-timeout"
	// PolicyHostApproval is reserved in the spec but not implemented; the
	// server rejects room creation that requests it.
	PolicyHostApproval ReconnectPolicy = "host-approval"
)

// Valid reports whether p is a policy this server implements.
func (p ReconnectPolicy) Valid() bool {
	switch p {
	case PolicyTokenOnly, PolicyTokenOrClaimAfterTimeout, PolicyClaimAfterTimeout:
		return true
	}
	return false
}

// AllowsClaim reports whether the policy permits tokenless slot claiming.
func (p ReconnectPolicy) AllowsClaim() bool {
	return p == PolicyTokenOrClaimAfterTimeout || p == PolicyClaimAfterTimeout
}

// DefaultReconnectPolicy is used when room creation omits the policy.
const DefaultReconnectPolicy = PolicyTokenOrClaimAfterTimeout

// Hard bounds on room codes (spec: length 4-64, safe charset only).
const (
	MinCodeLen = 4
	MaxCodeLen = 64
)

// Signal payload kinds a client may relay.
const (
	SignalKindOffer  = "offer"
	SignalKindAnswer = "answer"
	SignalKindICE    = "ice"
)

// ProtoError is a protocol-level failure with a stable machine-readable
// code. It is returned by validation and lobby operations and mapped
// directly onto an Error message.
type ProtoError struct {
	Code    string
	Message string
}

func (e *ProtoError) Error() string { return e.Code + ": " + e.Message }

// Errf builds a *ProtoError with a formatted message.
func Errf(code, format string, args ...any) *ProtoError {
	return &ProtoError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ClientMessage is the envelope for every client -> server message.
// Unknown JSON fields are ignored for forward compatibility; known
// fields are strictly validated per message type.
type ClientMessage struct {
	Type        string          `json:"type"`
	AppID       string          `json:"appId,omitempty"`
	Code        string          `json:"code,omitempty"`
	ResumeToken string          `json:"resumeToken,omitempty"`
	Create      *CreateOptions  `json:"create,omitempty"`
	PlayerID    *int            `json:"playerId,omitempty"`
	To          *int            `json:"to,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// CreateOptions are the client-supplied room creation options. Pointer
// fields distinguish "absent" from "false"/zero so defaults can apply.
type CreateOptions struct {
	MaxPlayers       int              `json:"maxPlayers"`
	WaitUntilFull    *bool            `json:"waitUntilFull,omitempty"`
	AllowLateJoin    *bool            `json:"allowLateJoin,omitempty"`
	AllowReconnect   *bool            `json:"allowReconnect,omitempty"`
	AllowReplacement *bool            `json:"allowReplacement,omitempty"`
	ReconnectPolicy  *ReconnectPolicy `json:"reconnectPolicy,omitempty"`
	ClaimAfterMs     *int64           `json:"claimAfterMs,omitempty"`
}

// RoomOptions is the fully-resolved, validated form of CreateOptions.
type RoomOptions struct {
	MaxPlayers       int
	WaitUntilFull    bool
	AllowLateJoin    bool
	AllowReconnect   bool
	AllowReplacement bool
	ReconnectPolicy  ReconnectPolicy
	ClaimAfter       time.Duration
}

// ResolveCreate validates opts against hard limits and fills defaults.
// hardMaxPlayers caps maxPlayers; defaultClaimAfter applies when the
// client omits claimAfterMs.
//
// Defaults: waitUntilFull=false, allowLateJoin=true, allowReconnect=true,
// allowReplacement=true, policy=token-or-claim-after-timeout. Note
// allowLateJoin defaults true so that a default room (which starts
// immediately because waitUntilFull=false) remains joinable.
func ResolveCreate(opts *CreateOptions, hardMaxPlayers int, defaultClaimAfter time.Duration) (RoomOptions, error) {
	if opts == nil {
		return RoomOptions{}, Errf(ErrCodeInvalidCreate, "create options required")
	}
	if opts.MaxPlayers < 1 {
		return RoomOptions{}, Errf(ErrCodeInvalidCreate, "maxPlayers must be >= 1")
	}
	if opts.MaxPlayers > hardMaxPlayers {
		return RoomOptions{}, Errf(ErrCodeInvalidCreate, "maxPlayers %d exceeds limit %d", opts.MaxPlayers, hardMaxPlayers)
	}
	r := RoomOptions{
		MaxPlayers:       opts.MaxPlayers,
		WaitUntilFull:    boolOr(opts.WaitUntilFull, false),
		AllowLateJoin:    boolOr(opts.AllowLateJoin, true),
		AllowReconnect:   boolOr(opts.AllowReconnect, true),
		AllowReplacement: boolOr(opts.AllowReplacement, true),
		ReconnectPolicy:  DefaultReconnectPolicy,
		ClaimAfter:       defaultClaimAfter,
	}
	if opts.ReconnectPolicy != nil {
		if !opts.ReconnectPolicy.Valid() {
			return RoomOptions{}, Errf(ErrCodeInvalidCreate, "unsupported reconnectPolicy %q", *opts.ReconnectPolicy)
		}
		r.ReconnectPolicy = *opts.ReconnectPolicy
	}
	if opts.ClaimAfterMs != nil {
		ms := *opts.ClaimAfterMs
		if ms < 0 {
			return RoomOptions{}, Errf(ErrCodeInvalidCreate, "claimAfterMs must be >= 0")
		}
		if ms > int64(24*time.Hour/time.Millisecond) {
			return RoomOptions{}, Errf(ErrCodeInvalidCreate, "claimAfterMs exceeds 24h")
		}
		r.ClaimAfter = time.Duration(ms) * time.Millisecond
	}
	return r, nil
}

func boolOr(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}

// ValidateCode enforces the room code rules: 4-64 chars from
// [A-Za-z0-9_-]. Codes are case-sensitive and matched exactly.
func ValidateCode(code string) error {
	if len(code) < MinCodeLen || len(code) > MaxCodeLen {
		return Errf(ErrCodeInvalidCode, "room code must be %d-%d characters", MinCodeLen, MaxCodeLen)
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return Errf(ErrCodeInvalidCode, "room code contains invalid character %q", string(c))
		}
	}
	return nil
}

// MaxResumeTokenLen bounds the resumeToken field (real tokens are 43
// chars of base64url; anything much longer is garbage).
const MaxResumeTokenLen = 128

// ParseClientMessage decodes and structurally validates a client
// message. It does not check room state; the lobby does that.
func ParseClientMessage(data []byte) (*ClientMessage, error) {
	var m ClientMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, Errf(ErrCodeInvalidMessage, "malformed JSON: %v", err)
	}
	switch m.Type {
	case TypeJoin:
		if err := ValidateCode(m.Code); err != nil {
			return nil, err
		}
		if len(m.ResumeToken) > MaxResumeTokenLen {
			return nil, Errf(ErrCodeInvalidMessage, "resumeToken too long")
		}
	case TypeClaimSlot:
		if err := ValidateCode(m.Code); err != nil {
			return nil, err
		}
		if m.PlayerID == nil {
			return nil, Errf(ErrCodeInvalidMessage, "claim-slot requires playerId")
		}
	case TypeSignal:
		if m.To == nil {
			return nil, Errf(ErrCodeInvalidMessage, "signal requires 'to'")
		}
		if err := validateSignalPayload(m.Payload); err != nil {
			return nil, err
		}
	case TypeLeave:
		// no fields
	case "":
		return nil, Errf(ErrCodeInvalidMessage, "missing message type")
	default:
		return nil, Errf(ErrCodeInvalidMessage, "unknown message type %q", m.Type)
	}
	return &m, nil
}

func validateSignalPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return Errf(ErrCodeInvalidMessage, "signal requires payload")
	}
	var peek struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return Errf(ErrCodeInvalidMessage, "signal payload must be a JSON object: %v", err)
	}
	switch peek.Kind {
	case SignalKindOffer, SignalKindAnswer, SignalKindICE:
		return nil
	default:
		return Errf(ErrCodeInvalidMessage, "signal payload kind must be offer, answer, or ice")
	}
}

// PlayerInfo is the public snapshot of one slot, sent in joined and
// player-joined messages. LastSeenMsAgo is only meaningful when
// Occupied is true.
type PlayerInfo struct {
	ID            int   `json:"id"`
	Occupied      bool  `json:"occupied"`
	Connected     bool  `json:"connected"`
	LastSeenMsAgo int64 `json:"lastSeenMsAgo"`
}

// ICEServer mirrors the WebRTC RTCIceServer dictionary.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Joined is the successful response to join or claim-slot.
type Joined struct {
	Type        string       `json:"type"`
	Code        string       `json:"code"`
	SelfID      int          `json:"selfId"`
	MaxPlayers  int          `json:"maxPlayers"`
	Started     bool         `json:"started"`
	ResumeToken string       `json:"resumeToken"`
	Players     []PlayerInfo `json:"players"`
	ICEServers  []ICEServer  `json:"iceServers,omitempty"`
}

// PlayerJoined announces a fresh occupant of a previously empty slot.
type PlayerJoined struct {
	Type     string       `json:"type"`
	PlayerID int          `json:"playerId"`
	Players  []PlayerInfo `json:"players"`
}

// PlayerLeft announces either an explicit leave (slot freed) or a
// transport disconnect (slot retained); Reason distinguishes.
type PlayerLeft struct {
	Type     string `json:"type"`
	PlayerID int    `json:"playerId"`
	Reason   string `json:"reason"`
}

// PlayerRejoined announces a token-based resume of an occupied slot.
type PlayerRejoined struct {
	Type           string `json:"type"`
	PlayerID       int    `json:"playerId"`
	WasReplacement bool   `json:"wasReplacement"`
}

// PlayerReplaced announces a tokenless claim of a silent occupied slot.
type PlayerReplaced struct {
	Type     string `json:"type"`
	PlayerID int    `json:"playerId"`
}

// RoomStarted announces the started transition.
type RoomStarted struct {
	Type string `json:"type"`
}

// SignalIn is a relayed signaling payload, server -> client.
type SignalIn struct {
	Type    string          `json:"type"`
	From    int             `json:"from"`
	Payload json.RawMessage `json:"payload"`
}

// Error is the server -> client error message.
type Error struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorMessage builds an Error from any error, mapping *ProtoError
// codes through and everything else to "internal".
func ErrorMessage(err error) Error {
	var pe *ProtoError
	if errors.As(err, &pe) {
		return Error{Type: TypeError, Code: pe.Code, Message: pe.Message}
	}
	return Error{Type: TypeError, Code: ErrCodeInternal, Message: "internal error"}
}
