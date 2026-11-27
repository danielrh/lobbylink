package lobbylink

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	reliableChannelID   uint16 = 1
	bestEffortChannelID uint16 = 2

	sendHighWater   = 1 << 20
	sendLowWater    = 256 * 1024
	channelTimeout  = 30 * time.Second
	maxPeerRebuilds = 3
)

// peerLink is one RTCPeerConnection plus the two pre-negotiated
// DataChannels to a single remote player.
type peerLink struct {
	playerID  int
	initiator bool
	pc        *webrtc.PeerConnection
	reliable  *webrtc.DataChannel
	bestEff   *webrtc.DataChannel

	mu          sync.Mutex
	closed      bool
	nextMsgID   uint32
	reassembler *reassembler
	// ICE candidates that arrived before the remote description; nil
	// once flushed.
	pendingCandidates []*wireCandidate
	havePending       bool

	reliableOpen chan struct{} // closed when the reliable channel opens
	openOnce     sync.Once
	done         chan struct{} // closed on teardown
	doneOnce     sync.Once
	drain        chan struct{} // pinged by OnBufferedAmountLow

	sendMu sync.Mutex // serializes reliable sends to this peer
}

func newPeerLink(playerID int, initiator bool, api *webrtc.API, config webrtc.Configuration) (*peerLink, error) {
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, errf("internal", "NewPeerConnection: %v", err)
	}
	link := &peerLink{
		playerID:          playerID,
		initiator:         initiator,
		pc:                pc,
		reassembler:       newReassembler(),
		pendingCandidates: []*wireCandidate{},
		havePending:       true,
		reliableOpen:      make(chan struct{}),
		done:              make(chan struct{}),
		drain:             make(chan struct{}, 1),
	}
	trueV, falseV := true, false
	relID, beID := reliableChannelID, bestEffortChannelID
	var zeroRtx uint16
	link.reliable, err = pc.CreateDataChannel("reliable", &webrtc.DataChannelInit{
		Negotiated: &trueV, ID: &relID, Ordered: &trueV,
	})
	if err != nil {
		pc.Close()
		return nil, errf("internal", "create reliable channel: %v", err)
	}
	link.bestEff, err = pc.CreateDataChannel("best-effort", &webrtc.DataChannelInit{
		Negotiated: &trueV, ID: &beID, Ordered: &falseV, MaxRetransmits: &zeroRtx,
	})
	if err != nil {
		pc.Close()
		return nil, errf("internal", "create best-effort channel: %v", err)
	}
	link.reliable.OnOpen(func() {
		link.openOnce.Do(func() { close(link.reliableOpen) })
	})
	link.reliable.SetBufferedAmountLowThreshold(sendLowWater)
	link.reliable.OnBufferedAmountLow(func() {
		select {
		case link.drain <- struct{}{}:
		default:
		}
	})
	return link, nil
}

func (l *peerLink) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// queueCandidate stashes cand if the remote description is not set yet;
// it reports whether the candidate was queued.
func (l *peerLink) queueCandidate(cand *wireCandidate) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.havePending {
		return false
	}
	l.pendingCandidates = append(l.pendingCandidates, cand)
	return true
}

// takePending returns the queued candidates and switches the link to
// direct-add mode.
func (l *peerLink) takePending() []*wireCandidate {
	l.mu.Lock()
	defer l.mu.Unlock()
	queued := l.pendingCandidates
	l.pendingCandidates = nil
	l.havePending = false
	return queued
}

// waitReliableOpen blocks until the reliable channel opens, the link
// dies, or the channel timeout passes.
func (l *peerLink) waitReliableOpen() error {
	select {
	case <-l.reliableOpen:
		return nil
	default:
	}
	timer := time.NewTimer(channelTimeout)
	defer timer.Stop()
	select {
	case <-l.reliableOpen:
		return nil
	case <-l.done:
		return errf("peer-closed", "connection to player %d closed", l.playerID)
	case <-timer.C:
		return errf("channel-timeout", "timed out opening channel to player %d", l.playerID)
	}
}

// awaitDrain blocks until bufferedAmount is back under the low-water
// mark (or the link dies). Called only with bufferedAmount over the
// high-water mark.
func (l *peerLink) awaitDrain() error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if l.reliable.BufferedAmount() <= sendLowWater {
			return nil
		}
		select {
		case <-l.drain:
		case <-ticker.C: // fallback poll for teardown races
		case <-l.done:
			return errf("peer-closed", "connection to player %d closed", l.playerID)
		}
	}
}

// sendReliableMessage chunks and writes one message on the reliable
// channel, applying backpressure. Sends to one peer are serialized.
func (l *peerLink) sendReliableMessage(data []byte) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	if err := l.waitReliableOpen(); err != nil {
		return err
	}
	l.mu.Lock()
	msgID := l.nextMsgID
	l.nextMsgID++
	l.mu.Unlock()
	total := chunkCount(len(data))
	for seq := uint32(0); seq < total; seq++ {
		if l.isClosed() {
			return errf("send-failed", "connection to player %d closed mid-send", l.playerID)
		}
		if l.reliable.BufferedAmount() > sendHighWater {
			if err := l.awaitDrain(); err != nil {
				return errf("send-failed", "send to player %d failed: %s", l.playerID, err)
			}
		}
		start := int(seq) * chunkPayload
		end := min(start+chunkPayload, len(data))
		if err := l.reliable.Send(makeFrame(msgID, seq, total, data[start:end])); err != nil {
			return errf("send-failed", "send to player %d failed: %v", l.playerID, err)
		}
	}
	return nil
}

// sendBestEffort writes one datagram, dropping it if the channel is
// not open or its buffer is over the high-water mark (that is the
// best-effort contract).
func (l *peerLink) sendBestEffort(data []byte) {
	if l.isClosed() || l.bestEff.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	if l.bestEff.BufferedAmount() > sendHighWater {
		return
	}
	_ = l.bestEff.Send(data)
}

func (l *peerLink) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.pendingCandidates = nil
	l.havePending = false
	l.mu.Unlock()
	l.doneOnce.Do(func() { close(l.done) })
	_ = l.reliable.Close()
	_ = l.bestEff.Close()
	_ = l.pc.Close()
}
