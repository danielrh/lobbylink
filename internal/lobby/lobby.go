// Package lobby implements room and player-slot state: creation, stable
// player IDs, start semantics, resume tokens, timeout-based slot
// claiming, signal relay, and room garbage collection.
//
// The package is transport-agnostic: the server hands each player a
// Conn whose Enqueue must never block. Lobby operations therefore never
// block on network writes; a transport that cannot keep up is detached
// and kicked.
//
// Locking: Manager.mu guards the room map; each Room has its own mu.
// Order is always Manager.mu before Room.mu, and per-session operations
// (Signal/Leave/Disconnect/Touch) take only Room.mu, so the ordering is
// deadlock-free.
package lobby

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"

	"github.com/danielrh/lobbylink/internal/protocol"
)

// Conn is a client transport as seen by the lobby.
type Conn interface {
	// Enqueue queues msg for delivery without blocking. It returns
	// false if the client cannot accept more messages (closed or
	// backlogged); the lobby then detaches and kicks the transport.
	Enqueue(msg any) bool
	// Kick asynchronously closes the transport after delivering (on a
	// best-effort basis) a protocol error. Must not block.
	Kick(code, message string)
}

// Limits are the manager-wide room lifecycle limits.
type Limits struct {
	EmptyTTL time.Duration // destroy rooms with no active transports after this
	MaxTTL   time.Duration // destroy any room this long after creation
	MaxRooms int
}

// Session identifies one joined player; the server keeps it alongside
// the websocket and passes its fields to per-room operations.
type Session struct {
	Room     *Room
	PlayerID int
}

// JoinResult is the lobby-level outcome of a successful join, resume,
// or claim. The server's build callback turns it into the full joined
// message (adding ICE servers) while the room lock is still held, which
// guarantees the joined message is enqueued before any subsequent room
// event can reach the new transport.
type JoinResult struct {
	Code        string
	SelfID      int
	MaxPlayers  int
	Started     bool
	ResumeToken string
	Players     []protocol.PlayerInfo
}

// Manager owns all rooms.
type Manager struct {
	mu     sync.Mutex
	rooms  map[string]*Room
	limits Limits
	now    func() time.Time
}

// NewManager creates a Manager. now is injectable for tests; pass
// time.Now in production.
func NewManager(limits Limits, now func() time.Time) *Manager {
	return &Manager{rooms: make(map[string]*Room), limits: limits, now: now}
}

// RoomCount returns the number of live rooms.
func (m *Manager) RoomCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rooms)
}

type slot struct {
	occupied   bool
	conn       Conn
	hasToken   bool
	tokenHash  [sha256.Size]byte
	joinedAt   time.Time
	lastSeenAt time.Time
}

// Room holds one lobby. All fields are guarded by mu.
type Room struct {
	mu        sync.Mutex
	destroyed bool

	code  string
	appID string
	opts  protocol.RoomOptions

	started bool
	slots   []slot

	createdAt    time.Time
	emptySince   time.Time // zero while any transport is attached
	lastActivity time.Time

	now func() time.Time
}

// Code returns the room's immutable code.
func (r *Room) Code() string { return r.code }

// Join handles the join message: it creates the room when absent and
// create options are supplied, resumes a slot when resumeToken matches,
// or occupies the lowest free slot. build is invoked with the result
// under the room lock and its return value is the first message
// enqueued to conn.
func (m *Manager) Join(appID, code, resumeToken string, create *protocol.RoomOptions, conn Conn, build func(*JoinResult) any) (*Session, error) {
	now := m.now()

	m.mu.Lock()
	room := m.rooms[code]
	if room == nil {
		if create == nil {
			m.mu.Unlock()
			return nil, protocol.Errf(protocol.ErrCodeRoomNotFound, "room %q does not exist and no create options supplied", code)
		}
		if len(m.rooms) >= m.limits.MaxRooms {
			m.gcLocked(now) // opportunistic sweep before rejecting
			if len(m.rooms) >= m.limits.MaxRooms {
				m.mu.Unlock()
				return nil, protocol.Errf(protocol.ErrCodeTooManyRooms, "server room limit reached")
			}
		}
		room = &Room{
			code:         code,
			appID:        appID,
			opts:         *create,
			slots:        make([]slot, create.MaxPlayers),
			createdAt:    now,
			lastActivity: now,
			// No transport is attached yet; occupyLocked clears this.
			// Without it a creation that fails before attach would
			// leave a room the empty-TTL GC never collects.
			emptySince: now,
			now:        m.now,
		}
		m.rooms[code] = room
	}
	room.mu.Lock()
	m.mu.Unlock()
	defer room.mu.Unlock()

	if room.destroyed {
		return nil, protocol.Errf(protocol.ErrCodeRoomNotFound, "room %q no longer exists", code)
	}
	if room.appID != appID {
		return nil, protocol.Errf(protocol.ErrCodeAppMismatch, "room %q belongs to a different app", code)
	}
	room.lastActivity = now

	// Resume path: a matching token reclaims the original slot
	// immediately. A non-matching or unusable token falls through to a
	// fresh join so stale stored tokens never strand a client.
	if resumeToken != "" && room.opts.AllowReconnect {
		if id, ok := room.findTokenLocked(resumeToken); ok {
			return room.resumeLocked(id, conn, now, build)
		}
	}

	if room.started && !room.opts.AllowLateJoin {
		return nil, protocol.Errf(protocol.ErrCodeRoomStarted, "room %q has started and does not allow late join", code)
	}
	id := -1
	for i := range room.slots {
		if !room.slots[i].occupied {
			id = i
			break
		}
	}
	if id == -1 {
		return nil, protocol.Errf(protocol.ErrCodeRoomFull, "room %q is full", code)
	}
	return room.occupyLocked(id, conn, now, build)
}

// Claim handles the claim-slot message: tokenless recovery of a
// specific player ID. Occupied slots require claim_after of silence;
// unoccupied slots may be (re)taken directly. Both require the room
// policy to permit claims.
func (m *Manager) Claim(appID, code string, playerID int, conn Conn, build func(*JoinResult) any) (*Session, error) {
	now := m.now()

	m.mu.Lock()
	room := m.rooms[code]
	if room == nil {
		m.mu.Unlock()
		return nil, protocol.Errf(protocol.ErrCodeRoomNotFound, "room %q does not exist", code)
	}
	room.mu.Lock()
	m.mu.Unlock()
	defer room.mu.Unlock()

	if room.destroyed {
		return nil, protocol.Errf(protocol.ErrCodeRoomNotFound, "room %q no longer exists", code)
	}
	if room.appID != appID {
		return nil, protocol.Errf(protocol.ErrCodeAppMismatch, "room %q belongs to a different app", code)
	}
	room.lastActivity = now

	if playerID < 0 || playerID >= len(room.slots) {
		return nil, protocol.Errf(protocol.ErrCodeInvalidTarget, "playerId %d out of range 0..%d", playerID, len(room.slots)-1)
	}
	if !room.opts.AllowReplacement || !room.opts.ReconnectPolicy.AllowsClaim() {
		return nil, protocol.Errf(protocol.ErrCodeClaimNotAllowed, "room %q does not allow slot claiming", code)
	}
	s := &room.slots[playerID]
	if !s.occupied {
		// Recovery of an explicitly freed slot: behaves like a join
		// targeted at this specific ID.
		return room.occupyLocked(playerID, conn, now, build)
	}
	silence := now.Sub(s.lastSeenAt)
	if silence < room.opts.ClaimAfter {
		remaining := room.opts.ClaimAfter - silence
		return nil, protocol.Errf(protocol.ErrCodeSlotNotClaimable,
			"player %d was seen %dms ago; claimable in %dms", playerID, silence.Milliseconds(), remaining.Milliseconds())
	}
	return room.replaceLocked(playerID, conn, now, build)
}

// resumeLocked re-attaches a token-holder to their occupied slot.
func (r *Room) resumeLocked(id int, conn Conn, now time.Time, build func(*JoinResult) any) (*Session, error) {
	token, hash, err := newToken()
	if err != nil {
		return nil, protocol.Errf(protocol.ErrCodeInternal, "token generation failed")
	}
	s := &r.slots[id]
	if old := s.conn; old != nil {
		s.conn = nil
		old.Kick(protocol.ErrCodeSuperseded, "another connection resumed this player slot")
	}
	s.conn = conn
	s.tokenHash = hash
	s.hasToken = true
	s.lastSeenAt = now
	r.updateEmptyLocked(now)

	r.deliverJoinedLocked(id, token, conn, now, build)
	r.broadcastLocked(protocol.PlayerRejoined{Type: protocol.TypePlayerRejoined, PlayerID: id, WasReplacement: false}, id)
	return &Session{Room: r, PlayerID: id}, nil
}

// occupyLocked fills an unoccupied slot (fresh join or claim of a freed
// slot) and runs the start transition.
func (r *Room) occupyLocked(id int, conn Conn, now time.Time, build func(*JoinResult) any) (*Session, error) {
	token, hash, err := newToken()
	if err != nil {
		return nil, protocol.Errf(protocol.ErrCodeInternal, "token generation failed")
	}
	s := &r.slots[id]
	s.occupied = true
	s.conn = conn
	s.tokenHash = hash
	s.hasToken = true
	s.joinedAt = now
	s.lastSeenAt = now

	startedNow := false
	if !r.started && (!r.opts.WaitUntilFull || r.occupiedCountLocked() == r.opts.MaxPlayers) {
		r.started = true
		startedNow = true
	}
	r.updateEmptyLocked(now)

	r.deliverJoinedLocked(id, token, conn, now, build)
	r.broadcastLocked(protocol.PlayerJoined{Type: protocol.TypePlayerJoined, PlayerID: id, Players: r.snapshotLocked(now)}, id)
	if startedNow {
		// The joiner learns started=true from its joined message.
		r.broadcastLocked(protocol.RoomStarted{Type: protocol.TypeRoomStarted}, id)
	}
	return &Session{Room: r, PlayerID: id}, nil
}

// replaceLocked hands an occupied-but-silent slot to a new transport
// (tokenless claim).
func (r *Room) replaceLocked(id int, conn Conn, now time.Time, build func(*JoinResult) any) (*Session, error) {
	token, hash, err := newToken()
	if err != nil {
		return nil, protocol.Errf(protocol.ErrCodeInternal, "token generation failed")
	}
	s := &r.slots[id]
	if old := s.conn; old != nil {
		s.conn = nil
		old.Kick(protocol.ErrCodeReplaced, "your player slot was claimed after inactivity")
	}
	s.conn = conn
	s.tokenHash = hash
	s.hasToken = true
	s.joinedAt = now
	s.lastSeenAt = now
	r.updateEmptyLocked(now)

	r.deliverJoinedLocked(id, token, conn, now, build)
	r.broadcastLocked(protocol.PlayerReplaced{Type: protocol.TypePlayerReplaced, PlayerID: id}, id)
	return &Session{Room: r, PlayerID: id}, nil
}

// deliverJoinedLocked enqueues the joined message built by the server
// callback as the first message on the new transport.
func (r *Room) deliverJoinedLocked(id int, token string, conn Conn, now time.Time, build func(*JoinResult) any) {
	res := &JoinResult{
		Code:        r.code,
		SelfID:      id,
		MaxPlayers:  r.opts.MaxPlayers,
		Started:     r.started,
		ResumeToken: token,
		Players:     r.snapshotLocked(now),
	}
	if !conn.Enqueue(build(res)) {
		r.slots[id].conn = nil
		conn.Kick(protocol.ErrCodeSlowConsumer, "outbound queue overflow")
		r.updateEmptyLocked(now)
	}
}

// Signal relays a validated signaling payload from the session player
// to another player. conn must be the session's current transport.
func (r *Room) Signal(playerID int, conn Conn, to int, payload json.RawMessage) error {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.destroyed {
		return protocol.Errf(protocol.ErrCodeRoomExpired, "room no longer exists")
	}
	if !r.isCurrentLocked(playerID, conn) {
		return protocol.Errf(protocol.ErrCodeNotJoined, "this connection no longer holds player %d", playerID)
	}
	r.slots[playerID].lastSeenAt = now
	r.lastActivity = now

	if to == playerID {
		return protocol.Errf(protocol.ErrCodeInvalidTarget, "cannot signal yourself")
	}
	if to < 0 || to >= len(r.slots) {
		return protocol.Errf(protocol.ErrCodeInvalidTarget, "playerId %d out of range 0..%d", to, len(r.slots)-1)
	}
	t := &r.slots[to]
	if !t.occupied {
		return protocol.Errf(protocol.ErrCodeInvalidTarget, "no player in slot %d", to)
	}
	if t.conn == nil {
		return protocol.Errf(protocol.ErrCodeTargetUnavailable, "player %d has no active connection", to)
	}
	msg := protocol.SignalIn{Type: protocol.TypeSignal, From: playerID, Payload: payload}
	if !t.conn.Enqueue(msg) {
		dead := t.conn
		t.conn = nil
		dead.Kick(protocol.ErrCodeSlowConsumer, "outbound queue overflow")
		r.broadcastLocked(protocol.PlayerLeft{Type: protocol.TypePlayerLeft, PlayerID: to, Reason: protocol.LeftReasonDisconnected}, -1)
		r.updateEmptyLocked(now)
		return protocol.Errf(protocol.ErrCodeTargetUnavailable, "player %d dropped (slow consumer)", to)
	}
	return nil
}

// Leave frees the session player's slot (explicit leave). The slot
// becomes unoccupied and the resume token is invalidated. A stale
// session (already replaced/superseded) is a no-op.
func (r *Room) Leave(playerID int, conn Conn) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.destroyed || !r.isCurrentLocked(playerID, conn) {
		return
	}
	s := &r.slots[playerID]
	s.occupied = false
	s.conn = nil
	s.hasToken = false
	s.tokenHash = [sha256.Size]byte{}
	r.lastActivity = now
	r.broadcastLocked(protocol.PlayerLeft{Type: protocol.TypePlayerLeft, PlayerID: playerID, Reason: protocol.LeftReasonExplicit}, -1)
	r.updateEmptyLocked(now)
}

// Disconnect detaches the transport after a socket close. The slot
// stays occupied and lastSeenAt stays frozen at the last message time,
// so the claim_after silence clock keeps running.
func (r *Room) Disconnect(playerID int, conn Conn) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.destroyed || !r.isCurrentLocked(playerID, conn) {
		return
	}
	r.slots[playerID].conn = nil
	r.broadcastLocked(protocol.PlayerLeft{Type: protocol.TypePlayerLeft, PlayerID: playerID, Reason: protocol.LeftReasonDisconnected}, -1)
	r.updateEmptyLocked(now)
}

// Touch records inbound activity for the session player (any WS
// message counts as liveness; there is no required heartbeat).
func (r *Room) Touch(playerID int, conn Conn) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.destroyed || !r.isCurrentLocked(playerID, conn) {
		return
	}
	r.slots[playerID].lastSeenAt = now
	r.lastActivity = now
}

// GC destroys rooms that have exceeded MaxTTL since creation or have
// had no attached transports for EmptyTTL. Returns rooms destroyed.
func (m *Manager) GC() int {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gcLocked(now)
}

func (m *Manager) gcLocked(now time.Time) int {
	n := 0
	for code, room := range m.rooms {
		room.mu.Lock()
		expired := now.Sub(room.createdAt) >= m.limits.MaxTTL
		empty := !room.emptySince.IsZero() && now.Sub(room.emptySince) >= m.limits.EmptyTTL
		if expired || empty {
			room.destroyed = true
			for i := range room.slots {
				if c := room.slots[i].conn; c != nil {
					room.slots[i].conn = nil
					c.Kick(protocol.ErrCodeRoomExpired, "room expired")
				}
			}
			delete(m.rooms, code)
			n++
		}
		room.mu.Unlock()
	}
	return n
}

// isCurrentLocked reports whether conn is the transport currently
// attached to playerID's occupied slot. This is what prevents a
// replaced or superseded session from acting on the new occupant's
// behalf during the window before its socket finishes closing.
func (r *Room) isCurrentLocked(playerID int, conn Conn) bool {
	if playerID < 0 || playerID >= len(r.slots) {
		return false
	}
	s := &r.slots[playerID]
	return s.occupied && s.conn != nil && s.conn == conn
}

func (r *Room) occupiedCountLocked() int {
	n := 0
	for i := range r.slots {
		if r.slots[i].occupied {
			n++
		}
	}
	return n
}

func (r *Room) findTokenLocked(token string) (int, bool) {
	hash := sha256.Sum256([]byte(token))
	for i := range r.slots {
		s := &r.slots[i]
		if s.occupied && s.hasToken &&
			subtle.ConstantTimeCompare(hash[:], s.tokenHash[:]) == 1 {
			return i, true
		}
	}
	return -1, false
}

func (r *Room) snapshotLocked(now time.Time) []protocol.PlayerInfo {
	out := make([]protocol.PlayerInfo, len(r.slots))
	for i := range r.slots {
		s := &r.slots[i]
		info := protocol.PlayerInfo{ID: i, Occupied: s.occupied, Connected: s.conn != nil}
		if s.occupied {
			info.LastSeenMsAgo = now.Sub(s.lastSeenAt).Milliseconds()
			if info.LastSeenMsAgo < 0 {
				info.LastSeenMsAgo = 0
			}
		}
		out[i] = info
	}
	return out
}

// updateEmptyLocked recomputes emptySince after any attach/detach.
func (r *Room) updateEmptyLocked(now time.Time) {
	for i := range r.slots {
		if r.slots[i].conn != nil {
			r.emptySince = time.Time{}
			return
		}
	}
	if r.emptySince.IsZero() {
		r.emptySince = now
	}
}

// broadcastLocked sends msg to every attached transport except player
// `except` (-1 for none). Transports that cannot keep up are detached
// and kicked, and their departure is announced to the remainder; the
// cascade terminates because each pass strictly reduces attached
// transports.
func (r *Room) broadcastLocked(msg any, except int) {
	dead := r.sendAllLocked(msg, except)
	for len(dead) > 0 {
		var next []int
		for _, id := range dead {
			left := protocol.PlayerLeft{Type: protocol.TypePlayerLeft, PlayerID: id, Reason: protocol.LeftReasonDisconnected}
			next = append(next, r.sendAllLocked(left, -1)...)
		}
		dead = next
	}
	r.updateEmptyLocked(r.now())
}

func (r *Room) sendAllLocked(msg any, except int) (dead []int) {
	for i := range r.slots {
		if i == except {
			continue
		}
		c := r.slots[i].conn
		if c == nil {
			continue
		}
		if !c.Enqueue(msg) {
			r.slots[i].conn = nil
			c.Kick(protocol.ErrCodeSlowConsumer, "outbound queue overflow")
			dead = append(dead, i)
		}
	}
	return dead
}

// newToken returns a fresh 32-byte random resume token (base64url,
// no padding) and its SHA-256 hash for storage. Raw tokens are never
// retained server-side.
func newToken() (string, [sha256.Size]byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", [sha256.Size]byte{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	return token, sha256.Sum256([]byte(token)), nil
}
