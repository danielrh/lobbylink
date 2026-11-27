package lobbylink

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Client -> server message envelope (implementation guide §4).
type wireClientMessage struct {
	Type        string             `json:"type"`
	AppID       string             `json:"appId,omitempty"`
	Code        string             `json:"code,omitempty"`
	ResumeToken string             `json:"resumeToken,omitempty"`
	Create      *wireCreateOptions `json:"create,omitempty"`
	PlayerID    *int               `json:"playerId,omitempty"`
	To          *int               `json:"to,omitempty"`
	Payload     json.RawMessage    `json:"payload,omitempty"`
}

type wireCreateOptions struct {
	MaxPlayers       int    `json:"maxPlayers"`
	WaitUntilFull    *bool  `json:"waitUntilFull,omitempty"`
	AllowLateJoin    *bool  `json:"allowLateJoin,omitempty"`
	AllowReconnect   *bool  `json:"allowReconnect,omitempty"`
	AllowReplacement *bool  `json:"allowReplacement,omitempty"`
	ReconnectPolicy  string `json:"reconnectPolicy,omitempty"`
	ClaimAfterMs     *int64 `json:"claimAfterMs,omitempty"`
}

type wirePlayer struct {
	ID        int  `json:"id"`
	Occupied  bool `json:"occupied"`
	Connected bool `json:"connected"`
}

type wireICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Server -> client messages share one envelope; Type discriminates.
type wireServerMessage struct {
	Type string `json:"type"`

	// joined
	Code        string          `json:"code,omitempty"`
	SelfID      int             `json:"selfId,omitempty"`
	MaxPlayers  int             `json:"maxPlayers,omitempty"`
	Started     bool            `json:"started,omitempty"`
	ResumeToken string          `json:"resumeToken,omitempty"`
	Players     []wirePlayer    `json:"players,omitempty"`
	ICEServers  []wireICEServer `json:"iceServers,omitempty"`

	// player-* events
	PlayerID       int    `json:"playerId,omitempty"`
	Reason         string `json:"reason,omitempty"`
	WasReplacement bool   `json:"wasReplacement,omitempty"`

	// signal
	From    int             `json:"from,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`

	// error ("code" doubles as the room code in joined and the error
	// code in error messages; Type disambiguates)
	Message string `json:"message,omitempty"`
}

// Signal payloads relayed between peers. Candidate mirrors the browser
// RTCIceCandidateInit dictionary; null/absent candidate is the optional
// end-of-candidates marker.
type wireSignal struct {
	Kind      string         `json:"kind"`
	SDP       string         `json:"sdp,omitempty"`
	Candidate *wireCandidate `json:"candidate,omitempty"`
}

type wireCandidate struct {
	Candidate        string  `json:"candidate"`
	SDPMid           *string `json:"sdpMid,omitempty"`
	SDPMLineIndex    *uint16 `json:"sdpMLineIndex,omitempty"`
	UsernameFragment *string `json:"usernameFragment,omitempty"`
}

// signalingURL normalizes a server URL to the ws(s) endpoint exactly
// like the TS and Rust clients: http(s) -> ws(s), "/ws" appended unless
// already present, query/fragment dropped.
func signalingURL(server string) (string, error) {
	scheme, rest, ok := strings.Cut(server, "://")
	if !ok {
		return "", &Error{Code: "invalid-server-url", Message: fmt.Sprintf("invalid server URL: %s", server)}
	}
	var wsScheme string
	switch strings.ToLower(scheme) {
	case "http", "ws":
		wsScheme = "ws"
	case "https", "wss":
		wsScheme = "wss"
	default:
		return "", &Error{Code: "invalid-server-url", Message: fmt.Sprintf("unsupported scheme %s in server URL", scheme)}
	}
	rest, _, _ = strings.Cut(rest, "#")
	rest, _, _ = strings.Cut(rest, "?")
	authority, path := rest, ""
	if i := strings.Index(rest, "/"); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	if authority == "" {
		return "", &Error{Code: "invalid-server-url", Message: fmt.Sprintf("invalid server URL: %s", server)}
	}
	path = strings.TrimRight(path, "/")
	if !strings.HasSuffix(path, "/ws") {
		path += "/ws"
	}
	return wsScheme + "://" + authority + path, nil
}

// defaultOrigin derives the http(s) origin matching a normalized ws(s)
// URL; native clients send it so servers do not need allow_no_origin.
func defaultOrigin(wsURL string) string {
	scheme, rest, _ := strings.Cut(wsURL, "://")
	authority, _, _ := strings.Cut(rest, "/")
	httpScheme := "https"
	if scheme == "ws" {
		httpScheme = "http"
	}
	return httpScheme + "://" + authority
}
