package lobbylink

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

const (
	defaultConnectTimeout = 20 * time.Second
	wsWriteTimeout        = 10 * time.Second
	// Server messages are small JSON (SDPs top out around a few KiB).
	wsReadLimit = 1 << 20
)

// Game is a live membership in one room: the signaling session plus a
// mesh of WebRTC DataChannels to every other player. Create one with
// Connect; receive with Events; free the slot with Close.
type Game struct {
	code        string
	selfID      int
	maxPlayers  int
	resumeToken string
	iceServers  []ICEServer
	tokenFile   string
	logger      *slog.Logger

	api       *webrtc.API
	rtcConfig webrtc.Configuration

	ws        *websocket.Conn
	wsWriteMu sync.Mutex

	mu          sync.Mutex
	roster      []wirePlayer
	started     bool
	closed      bool
	fatal       bool
	peers       map[int]*peerLink
	rebuilds    map[int]int
	linkWaiters map[int][]chan *peerLink

	evMu      sync.Mutex
	evQueue   []Event
	evPing    chan struct{}
	evOut     chan Event
	evAbort   chan struct{}
	abortOnce sync.Once
}

// Connect joins (optionally creating) or claims a slot in a room. The
// returned Game is live: peers connect in the background and surface
// as PeerStateEvent on Events.
func Connect(ctx context.Context, opts Options) (*Game, error) {
	if !validCode(opts.Code) {
		return nil, errf("invalid-code", "room code must be 4-64 chars of [A-Za-z0-9_-]")
	}
	url, err := signalingURL(opts.Server)
	if err != nil {
		return nil, err
	}
	origin := defaultOrigin(url)
	if opts.Origin != nil {
		origin = *opts.Origin
	}
	timeout := opts.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialOpts := &websocket.DialOptions{}
	if origin != "" {
		dialOpts.HTTPHeader = map[string][]string{"Origin": {origin}}
	}
	ws, _, err := websocket.Dial(dialCtx, url, dialOpts)
	if err != nil {
		return nil, errf("connection-failed", "cannot open %s: %v", url, err)
	}
	ws.SetReadLimit(wsReadLimit)

	join := wireClientMessage{Type: "join", Code: opts.Code, AppID: opts.AppID}
	if opts.ClaimPlayerID != nil {
		join = wireClientMessage{Type: "claim-slot", Code: opts.Code, AppID: opts.AppID, PlayerID: opts.ClaimPlayerID}
	} else {
		token := opts.ResumeToken
		if token == "" && opts.TokenFile != "" {
			if b, err := os.ReadFile(opts.TokenFile); err == nil {
				token = string(b)
			}
		}
		join.ResumeToken = token
		if opts.Create != nil {
			c := opts.Create
			join.Create = &wireCreateOptions{
				MaxPlayers:       c.MaxPlayers,
				WaitUntilFull:    c.WaitUntilFull,
				AllowLateJoin:    c.AllowLateJoin,
				AllowReconnect:   c.AllowReconnect,
				AllowReplacement: c.AllowReplacement,
				ReconnectPolicy:  string(c.ReconnectPolicy),
			}
			if c.ClaimAfter > 0 {
				ms := c.ClaimAfter.Milliseconds()
				join.Create.ClaimAfterMs = &ms
			}
		}
	}
	if err := writeWS(dialCtx, ws, join); err != nil {
		ws.Close(websocket.StatusNormalClosure, "")
		return nil, errf("connection-failed", "join send failed: %v", err)
	}

	// Read until the server accepts or rejects the join.
	var joined *wireServerMessage
	for joined == nil {
		var msg wireServerMessage
		if err := readWS(dialCtx, ws, &msg); err != nil {
			ws.Close(websocket.StatusNormalClosure, "")
			if dialCtx.Err() != nil {
				return nil, errf("connect-timeout", "timed out connecting to %s", url)
			}
			return nil, errf("connection-closed", "connection closed before join completed: %v", err)
		}
		switch msg.Type {
		case "joined":
			joined = &msg
		case "error":
			ws.Close(websocket.StatusNormalClosure, "")
			return nil, &Error{Code: msg.Code, Message: msg.Message}
		default:
			// Anything else before "joined" is unexpected; ignore.
		}
	}

	iceServers := make([]ICEServer, 0, len(joined.ICEServers)+len(opts.ICEServers))
	rtcICE := make([]webrtc.ICEServer, 0, cap(iceServers))
	for _, s := range joined.ICEServers {
		iceServers = append(iceServers, ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	iceServers = append(iceServers, opts.ICEServers...)
	for _, s := range iceServers {
		rtcICE = append(rtcICE, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	rtcConfig := webrtc.Configuration{ICEServers: rtcICE}
	if opts.ForceRelay {
		rtcConfig.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}

	g := &Game{
		code:        joined.Code,
		selfID:      joined.SelfID,
		maxPlayers:  joined.MaxPlayers,
		resumeToken: joined.ResumeToken,
		iceServers:  iceServers,
		tokenFile:   opts.TokenFile,
		logger:      slog.Default().With("lib", "lobbylink"),
		api:         webrtc.NewAPI(),
		rtcConfig:   rtcConfig,
		ws:          ws,
		roster:      append([]wirePlayer(nil), joined.Players...),
		started:     joined.Started,
		peers:       make(map[int]*peerLink),
		rebuilds:    make(map[int]int),
		linkWaiters: make(map[int][]chan *peerLink),
		evPing:      make(chan struct{}, 1),
		evOut:       make(chan Event),
		evAbort:     make(chan struct{}),
	}
	if g.tokenFile != "" {
		if err := os.WriteFile(g.tokenFile, []byte(joined.ResumeToken), 0o600); err != nil {
			g.logger.Warn("cannot persist resume token", "file", g.tokenFile, "err", err)
		}
	}
	go g.eventPump()
	go g.readLoop()

	// Lower ID initiates: offer to every connected peer with a higher
	// ID. Lower-ID peers offer to us when they see player-joined.
	g.mu.Lock()
	for _, p := range g.roster {
		if p.ID != g.selfID && p.Occupied && p.Connected && g.selfID < p.ID {
			g.initiatePeerLocked(p.ID)
		}
	}
	g.mu.Unlock()
	return g, nil
}

// Code returns the room code.
func (g *Game) Code() string { return g.code }

// SelfID returns our stable player ID (0..MaxPlayers-1).
func (g *Game) SelfID() int { return g.selfID }

// MaxPlayers returns the room size.
func (g *Game) MaxPlayers() int { return g.maxPlayers }

// ResumeToken returns the hidden token that resumes this slot after a
// disconnect. It rotates on every (re)join.
func (g *Game) ResumeToken() string { return g.resumeToken }

// ICEServers returns the ICE set in use (server-issued plus Options).
func (g *Game) ICEServers() []ICEServer { return append([]ICEServer(nil), g.iceServers...) }

// Started reports whether the room reached its start condition.
func (g *Game) Started() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.started
}

// Players returns a snapshot of all room slots.
func (g *Game) Players() []PlayerInfo {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]PlayerInfo, 0, len(g.roster))
	for _, p := range g.roster {
		out = append(out, PlayerInfo{ID: p.ID, Occupied: p.Occupied, Connected: p.Connected})
	}
	return out
}

// Events returns the event stream. The channel closes when the game is
// closed. Consume it promptly; events queue without bound otherwise.
func (g *Game) Events() <-chan Event { return g.evOut }

// SendBestEffort sends one datagram on the unordered, no-retransmit
// channel. It is silently dropped if the peer link is not up or its
// buffer is full (that is the best-effort contract); errors are
// caller mistakes only: bad target or payload over 16000 bytes.
func (g *Game) SendBestEffort(to int, data []byte) error {
	if err := g.checkTarget(to); err != nil {
		return err
	}
	if len(data) > maxBestEffort {
		return errf("message-too-large", "best-effort payload %d exceeds %d bytes", len(data), maxBestEffort)
	}
	g.mu.Lock()
	link := g.peers[to]
	g.mu.Unlock()
	if link != nil {
		link.sendBestEffort(data)
	}
	return nil
}

// BroadcastBestEffort sends the datagram to every other occupied slot.
func (g *Game) BroadcastBestEffort(data []byte) error {
	if len(data) > maxBestEffort {
		return errf("message-too-large", "best-effort payload %d exceeds %d bytes", len(data), maxBestEffort)
	}
	g.mu.Lock()
	links := make([]*peerLink, 0, len(g.roster))
	for _, p := range g.roster {
		if p.ID != g.selfID && p.Occupied {
			if link := g.peers[p.ID]; link != nil {
				links = append(links, link)
			}
		}
	}
	g.mu.Unlock()
	for _, link := range links {
		link.sendBestEffort(data)
	}
	return nil
}

// SendReliable sends an ordered, reliable message (chunked, up to
// 16 MiB), blocking until every chunk is handed to the transport. It
// waits up to 30s for a usable channel to the target.
func (g *Game) SendReliable(to int, data []byte) error {
	if err := g.checkTarget(to); err != nil {
		return err
	}
	g.mu.Lock()
	occupied := to < len(g.roster) && g.roster[to].Occupied
	g.mu.Unlock()
	if !occupied {
		return errf("target-unavailable", "no player in slot %d", to)
	}
	if len(data) > maxReliableMessage {
		return errf("message-too-large", "reliable payload %d exceeds %d bytes", len(data), maxReliableMessage)
	}
	link, err := g.awaitLink(to)
	if err != nil {
		return err
	}
	return link.sendReliableMessage(data)
}

// Close leaves the room (freeing our slot), releases all resources,
// clears the stored resume token, and closes the Events channel.
func (g *Game) Close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	_ = writeWS(ctx, g.ws, wireClientMessage{Type: "leave"})
	cancel()
	_ = g.ws.Close(websocket.StatusNormalClosure, "client closed")
	g.teardownPeers()
	if g.tokenFile != "" {
		_ = os.Remove(g.tokenFile)
	}
	g.abortOnce.Do(func() { close(g.evAbort) })
}

// -- events -----------------------------------------------------------------

func (g *Game) emit(ev Event) {
	g.mu.Lock()
	closed := g.closed
	g.mu.Unlock()
	if closed {
		return
	}
	g.evMu.Lock()
	g.evQueue = append(g.evQueue, ev)
	g.evMu.Unlock()
	select {
	case g.evPing <- struct{}{}:
	default:
	}
}

func (g *Game) eventPump() {
	defer close(g.evOut)
	for {
		g.evMu.Lock()
		var ev Event
		if len(g.evQueue) > 0 {
			ev = g.evQueue[0]
			g.evQueue = g.evQueue[1:]
		}
		g.evMu.Unlock()
		if ev == nil {
			select {
			case <-g.evPing:
				continue
			case <-g.evAbort:
				return
			}
		}
		select {
		case g.evOut <- ev:
		case <-g.evAbort:
			return
		}
	}
}

// -- signaling --------------------------------------------------------------

func writeWS(ctx context.Context, ws *websocket.Conn, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, data)
}

func readWS(ctx context.Context, ws *websocket.Conn, out *wireServerMessage) error {
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		return json.Unmarshal(data, out)
	}
}

func (g *Game) sendWS(msg any) {
	g.wsWriteMu.Lock()
	defer g.wsWriteMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	defer cancel()
	if err := writeWS(ctx, g.ws, msg); err != nil {
		// Socket died; the read loop reports the closure.
		g.logger.Debug("signaling send failed", "err", err)
	}
}

func (g *Game) sendSignal(to int, payload wireSignal) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	g.sendWS(wireClientMessage{Type: "signal", To: &to, Payload: raw})
}

func (g *Game) readLoop() {
	for {
		var msg wireServerMessage
		if err := readWS(context.Background(), g.ws, &msg); err != nil {
			g.mu.Lock()
			report := !g.closed && !g.fatal
			g.fatal = true
			g.mu.Unlock()
			if report {
				g.emit(SignalingClosedEvent{
					Code:    "connection-lost",
					Message: "signaling connection lost; existing peer channels stay up",
				})
			}
			return
		}
		g.handleServerMessage(&msg)
	}
}

func (g *Game) handleServerMessage(msg *wireServerMessage) {
	switch msg.Type {
	case "player-joined":
		g.mu.Lock()
		g.roster = append([]wirePlayer(nil), msg.Players...)
		g.mu.Unlock()
		g.emit(PlayerJoinedEvent{PlayerID: msg.PlayerID})
		g.resetPeer(msg.PlayerID)
	case "player-left":
		reason := "disconnected"
		if msg.Reason == "explicit-leave" {
			reason = "explicit-leave"
		}
		g.mu.Lock()
		if msg.PlayerID >= 0 && msg.PlayerID < len(g.roster) {
			if reason == "explicit-leave" {
				g.roster[msg.PlayerID].Occupied = false
			}
			g.roster[msg.PlayerID].Connected = false
		}
		g.mu.Unlock()
		if reason == "explicit-leave" {
			g.closePeer(msg.PlayerID)
		}
		// On "disconnected" the peer only lost signaling; an
		// established DataChannel may well still be alive, so keep it.
		g.emit(PlayerLeftEvent{PlayerID: msg.PlayerID, Reason: reason})
	case "player-rejoined":
		g.markOccupied(msg.PlayerID)
		g.emit(PlayerRejoinedEvent{PlayerID: msg.PlayerID, WasReplacement: msg.WasReplacement})
		g.resetPeer(msg.PlayerID)
	case "player-replaced":
		g.markOccupied(msg.PlayerID)
		g.emit(PlayerReplacedEvent{PlayerID: msg.PlayerID})
		g.resetPeer(msg.PlayerID)
	case "room-started":
		g.mu.Lock()
		g.started = true
		g.mu.Unlock()
		g.emit(StartedEvent{})
	case "signal":
		var payload wireSignal
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			g.logger.Warn("malformed signal payload", "from", msg.From)
			return
		}
		g.handleSignal(msg.From, payload)
	case "error":
		if isFatalCode(msg.Code) {
			g.mu.Lock()
			g.fatal = true
			g.mu.Unlock()
			if isGameOverCode(msg.Code) {
				g.teardownPeers()
				// "session-superseded" means our own token resumed
				// elsewhere; that process owns the token file now.
				if g.tokenFile != "" && msg.Code != "session-superseded" {
					_ = os.Remove(g.tokenFile)
				}
			}
			g.emit(SignalingClosedEvent{Code: msg.Code, Message: msg.Message})
		} else {
			g.emit(LobbyErrorEvent{Code: msg.Code, Message: msg.Message})
		}
	case "joined":
		// Only expected once, handled in Connect.
	default:
		// Unknown message types are ignored for forward compatibility.
	}
}

func (g *Game) markOccupied(playerID int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if playerID >= 0 && playerID < len(g.roster) {
		g.roster[playerID].Occupied = true
		g.roster[playerID].Connected = true
	}
}

// -- WebRTC -----------------------------------------------------------------

// resetPeer drops any old link after a peer got a new session and
// re-offers if we are the initiator.
func (g *Game) resetPeer(playerID int) {
	if playerID == g.selfID {
		return
	}
	g.closePeer(playerID)
	g.mu.Lock()
	delete(g.rebuilds, playerID)
	if g.selfID < playerID && !g.closed {
		g.initiatePeerLocked(playerID)
	}
	g.mu.Unlock()
}

// createLinkLocked replaces any existing link to playerID. g.mu held.
func (g *Game) createLinkLocked(playerID int, initiator bool) (*peerLink, error) {
	if old := g.peers[playerID]; old != nil {
		delete(g.peers, playerID)
		go old.close()
	}
	link, err := newPeerLink(playerID, initiator, g.api, g.rtcConfig)
	if err != nil {
		return nil, err
	}
	g.peers[playerID] = link

	link.pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil || link.isClosed() {
			return
		}
		init := c.ToJSON()
		g.sendSignal(playerID, wireSignal{Kind: "ice", Candidate: &wireCandidate{
			Candidate:        init.Candidate,
			SDPMid:           init.SDPMid,
			SDPMLineIndex:    init.SDPMLineIndex,
			UsernameFragment: init.UsernameFragment,
		}})
	})
	link.pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if link.isClosed() {
			return
		}
		g.emit(PeerStateEvent{PlayerID: playerID, State: state.String()})
		switch state {
		case webrtc.PeerConnectionStateConnected:
			g.mu.Lock()
			delete(g.rebuilds, playerID)
			g.mu.Unlock()
			go g.reportCandidatePair(link)
		case webrtc.PeerConnectionStateFailed:
			g.handlePeerFailure(link)
		}
	})
	link.reliable.OnMessage(func(msg webrtc.DataChannelMessage) {
		f, err := parseFrame(msg.Data)
		if err != nil {
			g.logger.Warn("dropping reliable frame", "from", playerID, "err", err)
			return
		}
		link.mu.Lock()
		full := link.reassembler.push(f, time.Now())
		link.mu.Unlock()
		if full != nil {
			g.emit(MessageEvent{From: playerID, Kind: Reliable, Data: full})
		}
	})
	link.bestEff.OnMessage(func(msg webrtc.DataChannelMessage) {
		data := append([]byte(nil), msg.Data...)
		g.emit(MessageEvent{From: playerID, Kind: BestEffort, Data: data})
	})

	for _, waiter := range g.linkWaiters[playerID] {
		waiter <- link
	}
	delete(g.linkWaiters, playerID)
	return link, nil
}

// initiatePeerLocked starts an offer to playerID. g.mu held.
func (g *Game) initiatePeerLocked(playerID int) {
	link, err := g.createLinkLocked(playerID, true)
	if err != nil {
		g.logger.Warn("cannot create peer connection", "player", playerID, "err", err)
		return
	}
	go func() {
		offer, err := link.pc.CreateOffer(nil)
		if err == nil && !link.isClosed() {
			err = link.pc.SetLocalDescription(offer)
		}
		if err != nil || link.isClosed() {
			if err != nil {
				g.logger.Warn("offer failed", "player", playerID, "err", err)
			}
			return
		}
		g.sendSignal(playerID, wireSignal{Kind: "offer", SDP: link.pc.LocalDescription().SDP})
	}()
}

func (g *Game) handleSignal(from int, payload wireSignal) {
	if from == g.selfID {
		return
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	switch payload.Kind {
	case "offer":
		if g.selfID < from {
			g.mu.Unlock()
			g.logger.Warn("ignoring offer from higher-ID player (protocol says we offer)", "from", from)
			return
		}
		// Every incoming offer starts a fresh session (initial connect
		// or the initiator rebuilding after a failure).
		link, err := g.createLinkLocked(from, false)
		g.mu.Unlock()
		if err != nil {
			g.logger.Warn("cannot create peer connection", "player", from, "err", err)
			return
		}
		go g.answerOffer(link, payload.SDP)
	case "answer":
		link := g.peers[from]
		g.mu.Unlock()
		if link == nil || link.isClosed() ||
			link.pc.SignalingState() != webrtc.SignalingStateHaveLocalOffer {
			g.logger.Warn("ignoring stale answer", "from", from)
			return
		}
		if err := link.pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer, SDP: payload.SDP,
		}); err != nil {
			g.logger.Warn("answer failed", "from", from, "err", err)
			return
		}
		g.flushCandidates(link)
	case "ice":
		link := g.peers[from]
		g.mu.Unlock()
		if link == nil || link.isClosed() {
			return
		}
		if link.queueCandidate(payload.Candidate) {
			return
		}
		g.addCandidate(link, payload.Candidate)
	default:
		g.mu.Unlock()
	}
}

func (g *Game) answerOffer(link *peerLink, sdp string) {
	err := link.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: sdp,
	})
	if err == nil && !link.isClosed() {
		g.flushCandidates(link)
		var answer webrtc.SessionDescription
		answer, err = link.pc.CreateAnswer(nil)
		if err == nil && !link.isClosed() {
			err = link.pc.SetLocalDescription(answer)
		}
		if err == nil && !link.isClosed() {
			g.sendSignal(link.playerID, wireSignal{Kind: "answer", SDP: link.pc.LocalDescription().SDP})
		}
	}
	if err != nil {
		g.logger.Warn("answering offer failed", "player", link.playerID, "err", err)
	}
}

func (g *Game) flushCandidates(link *peerLink) {
	for _, cand := range link.takePending() {
		g.addCandidate(link, cand)
	}
}

func (g *Game) addCandidate(link *peerLink, cand *wireCandidate) {
	if cand == nil {
		return // null is the optional end-of-candidates marker
	}
	err := link.pc.AddICECandidate(webrtc.ICECandidateInit{
		Candidate:        cand.Candidate,
		SDPMid:           cand.SDPMid,
		SDPMLineIndex:    cand.SDPMLineIndex,
		UsernameFragment: cand.UsernameFragment,
	})
	if err != nil && !link.isClosed() {
		g.logger.Warn("addIceCandidate failed", "player", link.playerID, "err", err)
	}
}

// handlePeerFailure schedules an initiator-side rebuild with backoff.
func (g *Game) handlePeerFailure(link *peerLink) {
	if !link.initiator {
		return
	}
	playerID := link.playerID
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.rebuilds[playerID]++
	count := g.rebuilds[playerID]
	g.mu.Unlock()
	if count > maxPeerRebuilds {
		g.logger.Warn("giving up on peer after rebuilds", "player", playerID, "rebuilds", maxPeerRebuilds)
		return
	}
	time.AfterFunc(time.Duration(count)*time.Second, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		slotOK := playerID < len(g.roster) && g.roster[playerID].Occupied && g.roster[playerID].Connected
		if g.closed || g.peers[playerID] != link ||
			link.pc.ConnectionState() != webrtc.PeerConnectionStateFailed || !slotOK {
			return
		}
		g.initiatePeerLocked(playerID)
	})
}

func (g *Game) reportCandidatePair(link *peerLink) {
	stats := link.pc.GetStats()
	var pair *webrtc.ICECandidatePairStats
	for _, s := range stats {
		if p, ok := s.(webrtc.ICECandidatePairStats); ok {
			if p.State == webrtc.StatsICECandidatePairStateSucceeded && p.Nominated {
				pair = &p
				break
			}
		}
	}
	if pair == nil || link.isClosed() {
		return
	}
	candidateType := func(id string) string {
		switch c := stats[id].(type) {
		case webrtc.ICECandidateStats:
			return c.CandidateType.String()
		default:
			return "unknown"
		}
	}
	g.emit(CandidatePairEvent{
		PlayerID: link.playerID,
		Local:    candidateType(pair.LocalCandidateID),
		Remote:   candidateType(pair.RemoteCandidateID),
	})
}

// -- data path helpers ------------------------------------------------------

func (g *Game) checkTarget(to int) error {
	if to < 0 || to >= g.maxPlayers {
		return errf("invalid-target", "player id %d out of range 0..%d", to, g.maxPlayers-1)
	}
	if to == g.selfID {
		return errf("invalid-target", "cannot send to yourself")
	}
	return nil
}

// awaitLink resolves the current link to a peer, waiting for one to be
// created if necessary.
func (g *Game) awaitLink(playerID int) (*peerLink, error) {
	g.mu.Lock()
	if link := g.peers[playerID]; link != nil && !link.isClosed() {
		g.mu.Unlock()
		return link, nil
	}
	if g.closed {
		g.mu.Unlock()
		return nil, errf("closed", "game is closed")
	}
	waiter := make(chan *peerLink, 1)
	g.linkWaiters[playerID] = append(g.linkWaiters[playerID], waiter)
	g.mu.Unlock()

	timer := time.NewTimer(channelTimeout)
	defer timer.Stop()
	select {
	case link := <-waiter:
		if link == nil {
			return nil, errf("closed", "game is closed")
		}
		return link, nil
	case <-timer.C:
		g.mu.Lock()
		waiters := g.linkWaiters[playerID]
		for i, w := range waiters {
			if w == waiter {
				g.linkWaiters[playerID] = append(waiters[:i], waiters[i+1:]...)
				break
			}
		}
		g.mu.Unlock()
		return nil, errf("channel-timeout", "no WebRTC session with player %d within %s", playerID, channelTimeout)
	}
}

// -- teardown ---------------------------------------------------------------

func (g *Game) closePeer(playerID int) {
	g.mu.Lock()
	link := g.peers[playerID]
	delete(g.peers, playerID)
	g.mu.Unlock()
	if link != nil {
		link.close()
	}
}

func (g *Game) teardownPeers() {
	g.mu.Lock()
	links := make([]*peerLink, 0, len(g.peers))
	for id, link := range g.peers {
		links = append(links, link)
		delete(g.peers, id)
	}
	waiters := g.linkWaiters
	g.linkWaiters = make(map[int][]chan *peerLink)
	g.mu.Unlock()
	for _, link := range links {
		link.close()
	}
	for _, list := range waiters {
		for _, w := range list {
			w <- nil
		}
	}
}
