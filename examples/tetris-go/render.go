package main

import (
	"fmt"
	"sort"
	"strings"
)

// ANSI rendering: the whole frame is composed into one string and
// written in a single call, redrawing in place with cursor-home so
// there is no flicker and no curses dependency.

// 256-color codes per cell value (index 1-8).
var cellColors = [9]int{0, 196, 46, 226, 33, 201, 51, 208, 245}

const (
	ansiHome      = "\x1b[H"
	ansiClear     = "\x1b[2J"
	ansiReset     = "\x1b[0m"
	ansiHideCur   = "\x1b[?25l"
	ansiShowCur   = "\x1b[?25h"
	ansiClearLine = "\x1b[K"
)

func cellStr(v uint8) string {
	if v == cellEmpty {
		return "\x1b[38;5;236m· " + ansiReset
	}
	return fmt.Sprintf("\x1b[48;5;%dm  %s", cellColors[v], ansiReset)
}

func ghostStr(v uint8) string {
	return fmt.Sprintf("\x1b[38;5;%dm░░%s", cellColors[v], ansiReset)
}

// snapshotBoard returns the board with the falling piece (and nothing
// else) baked in; used both for rendering and for STATE broadcasts.
func snapshotBoard(t *tetris) [boardH][boardW]uint8 {
	b := t.board
	if !t.over {
		for _, c := range t.cur.cells() {
			if c[1] >= 0 && c[1] < boardH && c[0] >= 0 && c[0] < boardW {
				b[c[1]][c[0]] = uint8(t.cur.kind + 1)
			}
		}
	}
	return b
}

// ghostRow computes where the current piece would land (hard drop).
func ghostPiece(t *tetris) piece {
	g := t.cur
	for {
		next := g
		next.y++
		if t.collides(next) {
			return g
		}
		g = next
	}
}

type renderState struct {
	t         *tetris
	room      string // "" in single-player
	selfID    int
	name      string
	paused    bool
	status    string // transient one-line message
	opponents map[int]*opponent
}

type opponent struct {
	present bool
	state   stateMsg
}

// render composes the full frame.
func render(rs *renderState) string {
	var sb strings.Builder
	sb.WriteString(ansiHome)

	title := "lobbylink TETRIS — single player"
	if rs.room != "" {
		title = fmt.Sprintf("lobbylink TETRIS — room %s — %s (player %d)", rs.room, rs.name, rs.selfID)
	}
	fmt.Fprintf(&sb, " %s%s\r\n%s\r\n", title, ansiClearLine, ansiClearLine)

	board := snapshotBoard(rs.t)
	var ghost piece
	drawGhost := !rs.t.over && !rs.paused
	if drawGhost {
		ghost = ghostPiece(rs.t)
	}
	ghostCells := map[[2]int]bool{}
	if drawGhost {
		for _, c := range ghost.cells() {
			ghostCells[[2]int{c[0], c[1]}] = true
		}
	}

	side := sidePanel(rs)
	mini := opponentPanel(rs)

	sb.WriteString(" ┌────────────────────┐" + ansiClearLine + "\r\n")
	for y := 0; y < boardH; y++ {
		sb.WriteString(" │")
		for x := 0; x < boardW; x++ {
			switch {
			case board[y][x] != cellEmpty:
				sb.WriteString(cellStr(board[y][x]))
			case ghostCells[[2]int{x, y}]:
				sb.WriteString(ghostStr(uint8(rs.t.cur.kind + 1)))
			default:
				sb.WriteString(cellStr(cellEmpty))
			}
		}
		sb.WriteString("│")
		if y < len(side) {
			sb.WriteString("   " + side[y])
		}
		if y < len(mini) {
			sb.WriteString("\x1b[70G") // opponents column
			sb.WriteString(mini[y])
		}
		sb.WriteString(ansiClearLine + "\r\n")
	}
	sb.WriteString(" └────────────────────┘" + ansiClearLine + "\r\n")
	fmt.Fprintf(&sb, " %s%s\r\n", rs.status, ansiClearLine)
	return sb.String()
}

// sidePanel is the column right of the board: next piece, score, help.
func sidePanel(rs *renderState) []string {
	t := rs.t
	lines := []string{"Next:"}
	var box [2][4]uint8
	for _, c := range pieceRotations[t.next][0] {
		if c[1] >= 0 && c[1] < 2 && c[0] < 4 {
			box[c[1]][c[0]] = uint8(t.next + 1)
		}
	}
	for _, row := range box {
		var b strings.Builder
		for _, v := range row {
			if v == 0 {
				b.WriteString("  ")
			} else {
				b.WriteString(cellStr(v))
			}
		}
		lines = append(lines, b.String())
	}
	lines = append(lines,
		"",
		fmt.Sprintf("Score %d", t.score),
		fmt.Sprintf("Lines %d", t.lines),
		fmt.Sprintf("Level %d", t.level()),
		"",
	)
	switch {
	case t.over:
		lines = append(lines, "\x1b[1;31mGAME OVER\x1b[0m", "r to restart")
	case rs.paused:
		lines = append(lines, "\x1b[1;33mPAUSED\x1b[0m", "p to resume")
	default:
		lines = append(lines, "", "")
	}
	lines = append(lines,
		"",
		"←/→ or a/d  move",
		"↑ or w      rotate",
		"↓ or s      soft drop",
		"space       hard drop",
		"p pause  q quit",
	)
	return lines
}

// opponentPanel renders each opponent as a caption plus a half-height
// mini board (two board rows per text row via ▀), side by side.
func opponentPanel(rs *renderState) []string {
	if len(rs.opponents) == 0 {
		return nil
	}
	ids := make([]int, 0, len(rs.opponents))
	for id := range rs.opponents {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	const rows = boardH/2 + 2
	panel := make([]string, rows)
	for _, id := range ids {
		op := rs.opponents[id]
		st := &op.state
		name := st.name
		if name == "" {
			name = fmt.Sprintf("player %d", id)
		}
		caption := fmt.Sprintf("%-10.10s", name)
		info := fmt.Sprintf("%-10.10s", fmt.Sprintf("%d", st.score))
		if !op.present {
			info = "\x1b[2maway      \x1b[0m"
		} else if !st.alive {
			info = "\x1b[31mtopped out\x1b[0m"
		}
		panel[0] += caption + "  "
		panel[1] += info + "  "
		for y := 0; y < boardH; y += 2 {
			var b strings.Builder
			for x := 0; x < boardW; x++ {
				top, bot := st.board[y][x], st.board[y+1][x]
				switch {
				case top == cellEmpty && bot == cellEmpty:
					b.WriteString("\x1b[38;5;236m·\x1b[0m")
				case top == cellEmpty:
					fmt.Fprintf(&b, "\x1b[38;5;%dm▄\x1b[0m", cellColors[bot])
				case bot == cellEmpty:
					fmt.Fprintf(&b, "\x1b[38;5;%dm▀\x1b[0m", cellColors[top])
				default:
					fmt.Fprintf(&b, "\x1b[38;5;%d;48;5;%dm▀\x1b[0m", cellColors[top], cellColors[bot])
				}
			}
			panel[2+y/2] += b.String() + "  "
		}
	}
	return panel
}
