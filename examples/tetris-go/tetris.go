package main

import "math/rand"

// Core single-player tetris: a 10x20 board of color cells, the seven
// tetrominoes with a 7-bag randomizer, simple wall-kick rotation, NES
// scoring. No I/O and no networking in this file.

const (
	boardW = 10
	boardH = 20

	cellEmpty   = 0
	cellGarbage = 8 // gray rows sent by opponents
)

// Piece shapes as 4 rotation states of 4 cells each, in a 4x4 box.
// Index order: I, O, T, S, Z, J, L; cell value = piece index + 1.
var pieceRotations = [7][4][4][2]int{
	buildRotations([4][2]int{{0, 1}, {1, 1}, {2, 1}, {3, 1}}), // I
	fixedRotations([4][2]int{{1, 0}, {2, 0}, {1, 1}, {2, 1}}), // O (rotation is a no-op)
	buildRotations([4][2]int{{1, 0}, {0, 1}, {1, 1}, {2, 1}}), // T
	buildRotations([4][2]int{{1, 0}, {2, 0}, {0, 1}, {1, 1}}), // S
	buildRotations([4][2]int{{0, 0}, {1, 0}, {1, 1}, {2, 1}}), // Z
	buildRotations([4][2]int{{0, 0}, {0, 1}, {1, 1}, {2, 1}}), // J
	buildRotations([4][2]int{{2, 0}, {0, 1}, {1, 1}, {2, 1}}), // L
}

// fixedRotations gives a shape (the O) the same cells in all four
// rotation states.
func fixedRotations(base [4][2]int) [4][4][2]int {
	return [4][4][2]int{base, base, base, base}
}

// buildRotations precomputes the four clockwise rotations of a shape.
// O and I use the common 4x4-box rotation; everything else pivots in
// its 3x3 box.
func buildRotations(base [4][2]int) [4][4][2]int {
	var out [4][4][2]int
	cur := base
	size := 3
	for _, c := range base {
		if c[0] > 2 || c[1] > 2 {
			size = 4
		}
	}
	for r := 0; r < 4; r++ {
		out[r] = cur
		var next [4][2]int
		for i, c := range cur {
			// (x,y) -> (size-1-y, x): clockwise within the box.
			next[i] = [2]int{size - 1 - c[1], c[0]}
		}
		cur = next
	}
	return out
}

type piece struct {
	kind int // 0..6
	rot  int // 0..3
	x, y int // top-left of the rotation box on the board
}

func (p piece) cells() [4][2]int {
	var out [4][2]int
	for i, c := range pieceRotations[p.kind][p.rot] {
		out[i] = [2]int{p.x + c[0], p.y + c[1]}
	}
	return out
}

// tetris is one player's full game state.
type tetris struct {
	board [boardH][boardW]uint8
	cur   piece
	next  int
	bag   []int
	rng   *rand.Rand
	score uint32
	lines int
	over  bool
	// linesCleared is set by lockPiece for the caller to consume
	// (attack size in multiplayer).
	linesCleared int
}

func newTetris(rng *rand.Rand) *tetris {
	t := &tetris{rng: rng}
	t.next = t.draw()
	t.spawn()
	return t
}

// draw takes the next piece from the 7-bag.
func (t *tetris) draw() int {
	if len(t.bag) == 0 {
		t.bag = t.rng.Perm(7)
	}
	k := t.bag[0]
	t.bag = t.bag[1:]
	return k
}

func (t *tetris) level() int { return t.lines / 10 }

func (t *tetris) collides(p piece) bool {
	for _, c := range p.cells() {
		x, y := c[0], c[1]
		if x < 0 || x >= boardW || y >= boardH {
			return true
		}
		if y >= 0 && t.board[y][x] != cellEmpty {
			return true
		}
	}
	return false
}

func (t *tetris) spawn() {
	t.cur = piece{kind: t.next, rot: 0, x: 3, y: -1}
	t.next = t.draw()
	if t.collides(t.cur) {
		t.cur.y--
		if t.collides(t.cur) {
			t.over = true
		}
	}
}

// move tries a horizontal/vertical shift; reports whether it applied.
func (t *tetris) move(dx, dy int) bool {
	moved := t.cur
	moved.x += dx
	moved.y += dy
	if t.collides(moved) {
		return false
	}
	t.cur = moved
	return true
}

// rotate tries a clockwise rotation with simple wall kicks.
func (t *tetris) rotate() bool {
	rotated := t.cur
	rotated.rot = (rotated.rot + 1) % 4
	for _, kick := range [][2]int{{0, 0}, {-1, 0}, {1, 0}, {0, -1}, {-2, 0}, {2, 0}} {
		try := rotated
		try.x += kick[0]
		try.y += kick[1]
		if !t.collides(try) {
			t.cur = try
			return true
		}
	}
	return false
}

// stepDown advances gravity by one row, locking the piece when it
// cannot fall. Reports whether the piece locked.
func (t *tetris) stepDown() bool {
	if t.move(0, 1) {
		return false
	}
	t.lockPiece()
	return true
}

// hardDrop slams the piece to the bottom and locks it immediately.
func (t *tetris) hardDrop() {
	for t.move(0, 1) {
		t.score += 2
	}
	t.lockPiece()
}

// nesScore is the classic per-clear base score by lines cleared.
var nesScore = [5]uint32{0, 40, 100, 300, 1200}

func (t *tetris) lockPiece() {
	for _, c := range t.cur.cells() {
		x, y := c[0], c[1]
		if y < 0 {
			t.over = true
			continue
		}
		t.board[y][x] = uint8(t.cur.kind + 1)
	}
	cleared := 0
	for y := boardH - 1; y >= 0; y-- {
		full := true
		for x := 0; x < boardW; x++ {
			if t.board[y][x] == cellEmpty {
				full = false
				break
			}
		}
		if full {
			cleared++
			copy(t.board[1:y+1], t.board[0:y])
			t.board[0] = [boardW]uint8{}
			y++ // recheck the row that slid down
		}
	}
	t.linesCleared = cleared
	t.lines += cleared
	t.score += nesScore[cleared] * uint32(t.level()+1)
	if !t.over {
		t.spawn()
	}
}

// addGarbage bumps the board up by n gray rows with one shared gap
// column — the multiplayer attack. The current piece is nudged up if
// the new floor swallows it; topping out ends the game.
func (t *tetris) addGarbage(n int) {
	if t.over || n <= 0 {
		return
	}
	gap := t.rng.Intn(boardW)
	for i := 0; i < n; i++ {
		for x := 0; x < boardW; x++ {
			if t.board[0][x] != cellEmpty {
				t.over = true
				return
			}
		}
		copy(t.board[0:boardH-1], t.board[1:])
		row := [boardW]uint8{}
		for x := range row {
			row[x] = cellGarbage
		}
		row[gap] = cellEmpty
		t.board[boardH-1] = row
	}
	for t.collides(t.cur) {
		t.cur.y--
		if t.cur.y < -2 {
			t.over = true
			return
		}
	}
}
