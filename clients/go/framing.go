package lobbylink

import (
	"encoding/binary"
	"errors"
	"time"
)

// Reliable-channel framing, wire-compatible with the TS client
// (clients/ts/README.md "Wire contract"): big-endian header, one frame
// per SCTP message.
const (
	frameMagic     = 0x4C // 'L'
	frameVersion   = 0x01
	frameHeaderLen = 18

	chunkPayload        = 16 * 1024
	maxFramePayload     = 64 * 1024
	maxReliableMessage  = 16 * 1024 * 1024
	maxReassemblyChunks = 4096
	reassemblyTimeout   = 30 * time.Second
	maxBestEffort       = 16_000
)

func makeFrame(msgID, seq, total uint32, payload []byte) []byte {
	frame := make([]byte, frameHeaderLen+len(payload))
	frame[0] = frameMagic
	frame[1] = frameVersion
	binary.BigEndian.PutUint32(frame[2:], msgID)
	binary.BigEndian.PutUint32(frame[6:], seq)
	binary.BigEndian.PutUint32(frame[10:], total)
	binary.BigEndian.PutUint32(frame[14:], uint32(len(payload)))
	copy(frame[frameHeaderLen:], payload)
	return frame
}

func chunkCount(n int) uint32 {
	if n == 0 {
		return 1
	}
	return uint32((n + chunkPayload - 1) / chunkPayload)
}

type frame struct {
	msgID, seq, total uint32
	payload           []byte // aliases the receive buffer; copy to keep
}

func parseFrame(buf []byte) (frame, error) {
	if len(buf) < frameHeaderLen {
		return frame{}, errors.New("frame shorter than header")
	}
	if buf[0] != frameMagic {
		return frame{}, errors.New("bad frame magic")
	}
	if buf[1] != frameVersion {
		return frame{}, errors.New("unsupported frame version")
	}
	f := frame{
		msgID: binary.BigEndian.Uint32(buf[2:]),
		seq:   binary.BigEndian.Uint32(buf[6:]),
		total: binary.BigEndian.Uint32(buf[10:]),
	}
	payloadLen := binary.BigEndian.Uint32(buf[14:])
	if f.total < 1 || f.total > maxReassemblyChunks {
		return frame{}, errors.New("bad frame total")
	}
	if f.seq >= f.total {
		return frame{}, errors.New("frame seq out of range")
	}
	if payloadLen > maxFramePayload {
		return frame{}, errors.New("frame payload too large")
	}
	if int(payloadLen) != len(buf)-frameHeaderLen {
		return frame{}, errors.New("frame payload length mismatch")
	}
	f.payload = buf[frameHeaderLen:]
	return f, nil
}

type reassembly struct {
	total     uint32
	chunks    [][]byte
	received  uint32
	bytes     int
	startedAt time.Time
}

// reassembler rebuilds chunked reliable messages from one sender,
// keyed by msgId. Incomplete messages are dropped after 30s.
type reassembler struct {
	inflight map[uint32]*reassembly
}

func newReassembler() *reassembler {
	return &reassembler{inflight: make(map[uint32]*reassembly)}
}

// push feeds one frame; it returns the complete message once the last
// chunk arrives, else nil.
func (r *reassembler) push(f frame, now time.Time) []byte {
	for id, entry := range r.inflight {
		if now.Sub(entry.startedAt) > reassemblyTimeout {
			delete(r.inflight, id)
		}
	}
	entry := r.inflight[f.msgID]
	if entry != nil && entry.total != f.total {
		// msgId reuse with different geometry: treat as a new message.
		entry = nil
	}
	if entry == nil {
		entry = &reassembly{
			total:     f.total,
			chunks:    make([][]byte, f.total),
			startedAt: now,
		}
		r.inflight[f.msgID] = entry
	}
	if entry.chunks[f.seq] == nil {
		entry.chunks[f.seq] = append([]byte(nil), f.payload...)
		entry.received++
		entry.bytes += len(f.payload)
		if entry.bytes > maxReliableMessage {
			delete(r.inflight, f.msgID)
			return nil
		}
	}
	if entry.received < entry.total {
		return nil
	}
	delete(r.inflight, f.msgID)
	out := make([]byte, 0, entry.bytes)
	for _, chunk := range entry.chunks {
		out = append(out, chunk...)
	}
	return out
}
