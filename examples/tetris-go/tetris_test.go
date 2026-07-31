package main

import (
	"bytes"
	"math/rand"
	"testing"
)

func fixedRNG() *rand.Rand { return rand.New(rand.NewSource(1)) }

func TestORotationIsNoop(t *testing.T) {
	for r := 1; r < 4; r++ {
		if pieceRotations[1][r] != pieceRotations[1][0] {
			t.Fatalf("O rotation %d differs from spawn state", r)
		}
	}
}

func TestRotationsReturnHome(t *testing.T) {
	// Four clockwise rotations must be the identity for every piece.
	for k := 0; k < 7; k++ {
		p := piece{kind: k, rot: 3}
		rotated := pieceRotations[k][p.rot]
		var next [4][2]int
		size := 3
		if k == 0 { // I uses the 4x4 box
			size = 4
		}
		for i, c := range rotated {
			next[i] = [2]int{size - 1 - c[1], c[0]}
		}
		if k != 1 && next != pieceRotations[k][0] {
			t.Fatalf("piece %d: rot 3 + 1 != rot 0 (%v vs %v)", k, next, pieceRotations[k][0])
		}
	}
}

func TestLineClearAndScore(t *testing.T) {
	g := newTetris(fixedRNG())
	// Fill the bottom row except column 0, then lock a vertical I
	// hugging the left wall: exactly one line clears.
	for x := 1; x < boardW; x++ {
		g.board[boardH-1][x] = 1
	}
	g.cur = piece{kind: 0, rot: 1, x: -2, y: boardH - 5} // vertical I in col 0
	if g.collides(g.cur) {
		t.Fatal("test setup: piece placement collides")
	}
	g.hardDrop()
	if g.linesCleared != 1 {
		t.Fatalf("cleared %d lines, want 1", g.linesCleared)
	}
	if g.lines != 1 {
		t.Fatalf("total lines %d, want 1", g.lines)
	}
	if g.score < 40 {
		t.Fatalf("score %d, want >= 40", g.score)
	}
	// Remaining three I cells slid down to the bottom row.
	got := 0
	for y := 0; y < boardH; y++ {
		for x := 0; x < boardW; x++ {
			if g.board[y][x] != cellEmpty {
				got++
				if x != 0 {
					t.Fatalf("unexpected block at %d,%d", x, y)
				}
			}
		}
	}
	if got != 3 {
		t.Fatalf("%d blocks left, want 3", got)
	}
}

func TestGarbageBumpsBoard(t *testing.T) {
	g := newTetris(fixedRNG())
	g.board[boardH-1][0] = 3
	g.addGarbage(2)
	if g.over {
		t.Fatal("unexpected game over")
	}
	if g.board[boardH-3][0] != 3 {
		t.Fatal("existing stack was not pushed up by 2")
	}
	for _, y := range []int{boardH - 1, boardH - 2} {
		gaps := 0
		for x := 0; x < boardW; x++ {
			switch g.board[y][x] {
			case cellEmpty:
				gaps++
			case cellGarbage:
			default:
				t.Fatalf("row %d has non-garbage cell %d", y, g.board[y][x])
			}
		}
		if gaps != 1 {
			t.Fatalf("garbage row %d has %d gaps, want 1", y, gaps)
		}
	}
	// The gap column must line up across the attack's rows.
	for x := 0; x < boardW; x++ {
		if (g.board[boardH-1][x] == cellEmpty) != (g.board[boardH-2][x] == cellEmpty) {
			t.Fatal("gap column differs between garbage rows")
		}
	}
}

func TestGarbageTopOut(t *testing.T) {
	g := newTetris(fixedRNG())
	for y := 1; y < boardH; y++ {
		g.board[y][4] = 1
	}
	g.addGarbage(2) // pushing a full-height stack up tops out
	if !g.over {
		t.Fatal("expected game over from garbage top-out")
	}
}

func TestStateRoundTrip(t *testing.T) {
	msg := stateMsg{alive: true, score: 123456, lines: 42, level: 4, name: "gopher"}
	msg.board[19][0] = 7
	msg.board[0][9] = cellGarbage
	got, ok := decodeState(encodeState(&msg))
	if !ok {
		t.Fatal("decode failed")
	}
	if got != msg {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, msg)
	}
}

func TestAttackRoundTrip(t *testing.T) {
	for n := 1; n <= 4; n++ {
		got, ok := decodeAttack(encodeAttack(n))
		if !ok || got != n {
			t.Fatalf("attack %d round trip gave %d,%v", n, got, ok)
		}
	}
	if _, ok := decodeAttack([]byte{msgState, 1}); ok {
		t.Fatal("decoded a STATE header as attack")
	}
	if _, ok := decodeState(bytes.Repeat([]byte{0}, 100)); ok {
		t.Fatal("decoded a short buffer as state")
	}
}
