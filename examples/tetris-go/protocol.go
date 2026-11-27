package main

import "encoding/binary"

// Multiplayer wire protocol, riding on lobbylink DataChannels:
//
//   0x01 STATE (best-effort, ~5 Hz + on every board change)
//     off  type  field
//     0    u8    type = 1
//     1    u8    flags     bit0 alive
//     2    u32   score     big-endian
//     6    u16   lines
//     8    u8    level
//     9    200B  board     row-major 20 rows x 10 cols, one byte per
//                          cell: 0 empty, 1-7 piece colors, 8 garbage;
//                          the sender's falling piece is baked in
//     209  u8    nameLen   0..24
//     210  ...   name      UTF-8
//
//   0x02 ATTACK (reliable): the "bump" — the receiver pushes this many
//   garbage rows into their board
//     0    u8    type = 2
//     1    u8    lines     1..4
//
// One full STATE describes a player completely, so late joiners need
// no history.

const (
	msgState  = 0x01
	msgAttack = 0x02

	maxNameLen = 24
)

type stateMsg struct {
	alive bool
	score uint32
	lines uint16
	level uint8
	board [boardH][boardW]uint8
	name  string
}

func encodeState(m *stateMsg) []byte {
	name := m.name
	if len(name) > maxNameLen {
		name = name[:maxNameLen]
	}
	out := make([]byte, 210+len(name))
	out[0] = msgState
	if m.alive {
		out[1] = 1
	}
	binary.BigEndian.PutUint32(out[2:], m.score)
	binary.BigEndian.PutUint16(out[6:], m.lines)
	out[8] = m.level
	for y := 0; y < boardH; y++ {
		copy(out[9+y*boardW:], m.board[y][:])
	}
	out[209] = uint8(len(name))
	copy(out[210:], name)
	return out
}

func decodeState(data []byte) (stateMsg, bool) {
	if len(data) < 210 || data[0] != msgState {
		return stateMsg{}, false
	}
	m := stateMsg{
		alive: data[1]&1 != 0,
		score: binary.BigEndian.Uint32(data[2:]),
		lines: binary.BigEndian.Uint16(data[6:]),
		level: data[8],
	}
	for y := 0; y < boardH; y++ {
		copy(m.board[y][:], data[9+y*boardW:9+(y+1)*boardW])
	}
	nameLen := int(data[209])
	if nameLen > maxNameLen || 210+nameLen > len(data) {
		nameLen = 0
	}
	m.name = string(data[210 : 210+nameLen])
	return m, true
}

func encodeAttack(lines int) []byte {
	return []byte{msgAttack, uint8(lines)}
}

func decodeAttack(data []byte) (int, bool) {
	if len(data) != 2 || data[0] != msgAttack {
		return 0, false
	}
	return int(data[1]), true
}
