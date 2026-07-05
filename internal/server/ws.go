package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/danielrh/lobbylink/internal/config"
	"github.com/danielrh/lobbylink/internal/lobby"
	"github.com/danielrh/lobbylink/internal/protocol"
	"github.com/danielrh/lobbylink/internal/turn"
)

const (
	// outboundQueueSize bounds each connection's send channel; the
	// lobby drops the transport rather than block when it overflows.
	outboundQueueSize = 128
	writeTimeout      = 10 * time.Second
)

// wsClient adapts one websocket to lobby.Conn. Enqueue never blocks
// and the send channel is never closed (shutdown is signaled via done),
// so concurrent Enqueue is always safe.
type wsClient struct {
	conn *websocket.Conn
	send chan any
	done chan struct{}
	once sync.Once
}

func newWSClient(conn *websocket.Conn) *wsClient {
	return &wsClient{
		conn: conn,
		send: make(chan any, outboundQueueSize),
		done: make(chan struct{}),
	}
}

// Enqueue implements lobby.Conn.
func (c *wsClient) Enqueue(msg any) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

// Kick implements lobby.Conn: best-effort error delivery, then close.
func (c *wsClient) Kick(code, message string) {
	c.Enqueue(protocol.Error{Type: protocol.TypeError, Code: code, Message: message})
	c.shutdown()
}

func (c *wsClient) shutdown() {
	c.once.Do(func() { close(c.done) })
}

// writeLoop serializes all outbound traffic for one connection. After
// shutdown it flushes whatever is already queued (e.g. the kick error)
// before closing the socket.
func (c *wsClient) writeLoop() {
	defer c.conn.Close(websocket.StatusNormalClosure, "")
	for {
		select {
		case msg := <-c.send:
			if !c.write(msg) {
				c.shutdown()
				return
			}
		case <-c.done:
			for {
				select {
				case msg := <-c.send:
					if !c.write(msg) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (c *wsClient) write(msg any) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, data) == nil
}

// handleWS validates the Origin, upgrades, and runs the signaling
// session until the socket closes.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !s.wsOriginAllowed(origin) {
		s.log.Warn("ws origin rejected", "origin", origin, "ip", s.clientIP(r))
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Origin was validated above against the configured allowlist.
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.log.Debug("ws accept failed", "err", err)
		return
	}
	conn.SetReadLimit(s.cfg.Security.MaxWSMessageBytes)

	client := newWSClient(conn)
	go client.writeLoop()
	defer client.shutdown()

	// No explicit read cancellation on kick: the write loop closes the
	// socket after flushing the kick error, which unblocks the reader.
	sess := s.readLoop(r.Context(), client, origin)
	if sess != nil {
		sess.Room.Disconnect(sess.PlayerID, client)
	}
}

// wsOriginAllowed applies the WebSocket origin policy: allowlisted
// origins pass; a missing Origin (native client) passes only when
// allow_no_origin is enabled.
func (s *Server) wsOriginAllowed(origin string) bool {
	if origin == "" {
		return s.cfg.Security.AllowNoOrigin
	}
	return s.originKnown(origin)
}

// readLoop consumes client messages until the socket dies. It returns
// the session (if any) so the caller can record the disconnect.
func (s *Server) readLoop(ctx context.Context, client *wsClient, origin string) *lobby.Session {
	var sess *lobby.Session
	for {
		typ, data, err := client.conn.Read(ctx)
		if err != nil {
			return sess
		}
		if typ != websocket.MessageText {
			client.Kick(protocol.ErrCodeInvalidMessage, "signaling messages must be text frames")
			return sess
		}
		// Any inbound message counts as liveness for the silence-based
		// claim timer, even one that fails validation below.
		if sess != nil {
			sess.Room.Touch(sess.PlayerID, client)
		}
		msg, err := protocol.ParseClientMessage(data)
		if err != nil {
			client.Enqueue(protocol.ErrorMessage(err))
			continue
		}
		switch msg.Type {
		case protocol.TypeJoin, protocol.TypeClaimSlot:
			if sess != nil {
				client.Enqueue(protocol.Error{Type: protocol.TypeError, Code: protocol.ErrCodeAlreadyJoined, Message: "already in a room; send leave first"})
				continue
			}
			newSess, err := s.joinOrClaim(msg, client, origin)
			if err != nil {
				client.Enqueue(protocol.ErrorMessage(err))
				continue
			}
			sess = newSess
		case protocol.TypeSignal:
			if sess == nil {
				client.Enqueue(protocol.Error{Type: protocol.TypeError, Code: protocol.ErrCodeNotJoined, Message: "join a room before signaling"})
				continue
			}
			if err := sess.Room.Signal(sess.PlayerID, client, *msg.To, msg.Payload); err != nil {
				client.Enqueue(protocol.ErrorMessage(err))
			}
		case protocol.TypeLeave:
			if sess != nil {
				sess.Room.Leave(sess.PlayerID, client)
				sess = nil
			}
		}
	}
}

// joinOrClaim enforces app policy and dispatches to the lobby.
func (s *Server) joinOrClaim(msg *protocol.ClientMessage, client *wsClient, origin string) (*lobby.Session, error) {
	var app *config.App
	if msg.AppID != "" {
		app = s.cfg.AppByID(msg.AppID)
		if app == nil {
			return nil, protocol.Errf(protocol.ErrCodeUnknownApp, "unknown appId %q", msg.AppID)
		}
	}
	// Origin re-check at join time: an app-scoped origin only grants
	// access to that app's rooms; joins without appId need the global
	// allowlist.
	if origin != "" {
		allowed := s.cfg.OriginAllowed(origin)
		if !allowed && app != nil {
			allowed = s.cfg.OriginAllowedForApp(origin, app)
		}
		if !allowed {
			return nil, protocol.Errf(protocol.ErrCodeOriginForbidden, "origin %q not allowed for this app", origin)
		}
	}

	build := func(res *lobby.JoinResult) any { return s.buildJoined(res, app) }

	if msg.Type == protocol.TypeClaimSlot {
		return s.mgr.Claim(msg.AppID, msg.Code, *msg.PlayerID, client, build)
	}

	var opts *protocol.RoomOptions
	if msg.Create != nil {
		maxPlayers := s.cfg.Rooms.MaxPlayersHard
		if app != nil && app.MaxPlayersMax > 0 && app.MaxPlayersMax < maxPlayers {
			maxPlayers = app.MaxPlayersMax
		}
		resolved, err := protocol.ResolveCreate(msg.Create, maxPlayers, s.cfg.Rooms.ClaimAfter)
		if err != nil {
			return nil, err
		}
		opts = &resolved
	}
	return s.mgr.Join(msg.AppID, msg.Code, msg.ResumeToken, opts, client, build)
}

// buildJoined assembles the joined message, minting TURN credentials
// when enabled and permitted for the app. It runs under the room lock,
// so it must stay cheap and must not block.
func (s *Server) buildJoined(res *lobby.JoinResult, app *config.App) any {
	joined := protocol.Joined{
		Type:        protocol.TypeJoined,
		Code:        res.Code,
		SelfID:      res.SelfID,
		MaxPlayers:  res.MaxPlayers,
		Started:     res.Started,
		ResumeToken: res.ResumeToken,
		Players:     res.Players,
	}
	if s.cfg.Turn.Enabled && (app == nil || app.AllowTurn) {
		joined.ICEServers = turn.ICEServers(
			s.cfg.Turn.URLs, s.cfg.Turn.Secret, s.cfg.Turn.TTL,
			res.Code, res.SelfID, time.Now())
	}
	return joined
}
